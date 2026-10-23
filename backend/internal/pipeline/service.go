// Package pipeline runs the durable import queue (scan → analyse → plan) and the execute/rollback
// operations that the API delegates to. All file writes go through archive.Executor.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"oa-agent/backend/internal/api"
	"oa-agent/backend/internal/archive"
	"oa-agent/backend/internal/diag"
	"oa-agent/backend/internal/harness"
	"oa-agent/backend/internal/imports"
	"oa-agent/backend/internal/llm"
	"oa-agent/backend/internal/logx"
	"oa-agent/backend/internal/store"
)

// Analyzer is the harness agent (an interface so tests can substitute it).
type Analyzer interface {
	AnalyzeFile(ctx context.Context, f harness.FileInput, tpls []harness.Template, emit func(harness.Event)) (harness.Result, error)
}

type Config struct {
	Store        *store.Store
	Analyzer     Analyzer
	ArchiveRoot  string
	SpoolRoot    string
	Workers      int // concurrent jobs
	FileParallel int // concurrent files within a job
	ProviderName string
	Model        string
	ProviderOK   bool
	// ProviderInfo, when set, reports the live recognition engine (name, model, state) instead of the static fields.
	ProviderInfo func() (name, model, state string)
	// Models resolves the model a task runs with from the task's stored llm_source/llm_owner (implemented by
	// llm.Manager). nil: every task uses the Analyzer's own provider (unit tests).
	Models ModelResolver
	Poll   time.Duration
}

// ModelResolver turns a task's stored model binding into a provider. There is no fallback between sources: an
// error means the task's model is gone and its files fail with the error text.
type ModelResolver interface {
	TaskProvider(ctx context.Context, source, owner string) (llm.TaskModel, error)
}

type Service struct {
	cfg  Config
	exec *archive.Executor
	mu   sync.Mutex
	plan map[string]*sync.Mutex // per-plan execution guards
}

func New(cfg Config) (*Service, error) {
	if cfg.Workers < 1 {
		cfg.Workers = 2
	}
	if cfg.FileParallel < 1 {
		cfg.FileParallel = 2
	}
	if cfg.Poll <= 0 {
		cfg.Poll = time.Second
	}
	root, err := filepath.Abs(cfg.ArchiveRoot)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	cfg.ArchiveRoot = root
	return &Service{cfg: cfg, exec: &archive.Executor{Root: root}, plan: map[string]*sync.Mutex{}}, nil
}

// Run recovers interrupted work and then processes queued jobs until ctx is cancelled.
func (s *Service) Run(ctx context.Context) {
	if n, err := s.exec.Recover(ctx); err != nil {
		log.Printf("archive recovery: %v", err)
		reportError("pipeline", "ARCHIVE_RECOVERY_FAILED", "启动时的归档恢复失败", err)
	} else if n > 0 {
		log.Printf("archive recovery removed %d orphaned staging files", n)
	}
	if n, err := s.cfg.Store.RecoverJobs(ctx); err != nil {
		log.Printf("job recovery: %v", err)
		reportError("pipeline", "JOB_RECOVERY_FAILED", "启动时的任务恢复失败", err)
	} else if n > 0 {
		log.Printf("re-queued %d interrupted jobs", n)
	}
	if ids, err := s.cfg.Store.RecoverArchiving(ctx); err == nil {
		for _, id := range ids {
			diag.Go("pipeline.resume", func() { s.resumeExecution(ctx, id) })
		}
	}
	// Automatic plans that were approved but never executed (crash right after planning).
	if refs, err := s.cfg.Store.RecoverAutoPlans(ctx); err == nil {
		for _, r := range refs {
			diag.Go("pipeline.resume_auto", func() { s.resumeAuto(ctx, r) })
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < s.cfg.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			diag.Loop(ctx, "pipeline.loop", s.loop)
		}()
	}
	wg.Wait()
}

func (s *Service) loop(ctx context.Context) {
	t := time.NewTicker(s.cfg.Poll)
	defer t.Stop()
	var bo diag.Backoff
	for {
		for {
			job, err := s.cfg.Store.ClaimJob(ctx)
			if err != nil {
				if errors.Is(err, store.ErrNotFound) || ctx.Err() != nil {
					bo.OK("claim job")
					break
				}
				// database unreachable: back off instead of hammering it (and the log) every poll
				wait, first := bo.Fail("claim job", err)
				if first {
					reportError("pipeline", "CLAIM_JOB_FAILED", "领取任务失败（数据库不可用？）", err)
				}
				if !diag.Sleep(ctx, wait) {
					return
				}
				break
			}
			bo.OK("claim job")
			// A panic while processing fails this one task with a numbered message; the worker loop and the process live on.
			if id := diag.SafeD("pipeline.process", map[string]any{"task_id": job.ID}, func() { s.process(ctx, job) }); id != "" {
				msg := diag.UserMessage(id)
				_ = s.cfg.Store.FailJob(context.WithoutCancel(ctx), job.ID, msg)
				s.event(context.WithoutCancel(ctx), job.ID, "failed", "task_failed", nil, msg, nil)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Service) event(ctx context.Context, taskID, phase, typ string, fileID *string, summary string, details map[string]any) {
	if err := s.cfg.Store.AppendEvent(ctx, taskID, phase, typ, fileID, summary, details); err != nil {
		log.Printf("append event: %v", err)
	}
}

func (s *Service) process(ctx context.Context, job imports.Job) {
	logx.Debugf("job picked up: %s", job.ID)
	if processHook != nil {
		processHook(job)
	}
	st := s.cfg.Store
	fail := func(msg string, err error) {
		log.Printf("job %s: %s: %v", job.ID, msg, err)
		reportTask("pipeline", "JOB_FAILED", msg, err, job.ID, "", nil)
		_ = st.FailJob(context.WithoutCancel(ctx), job.ID, msg)
		s.event(context.WithoutCancel(ctx), job.ID, "failed", "task_failed", nil, msg, nil)
	}
	cancelled := func() bool {
		c, err := st.CancelRequested(ctx, job.ID)
		if err == nil && c {
			_ = st.SetJobStatus(context.WithoutCancel(ctx), job.ID, "cancelled")
			s.event(context.WithoutCancel(ctx), job.ID, "cancelled", "task_cancelled", nil, "任务已取消", nil)
			return true
		}
		return false
	}
	s.event(ctx, job.ID, "scanning", "phase_started", nil, "扫描导入文件并核对完整性", nil)
	files, err := st.JobFiles(ctx, job.ID)
	if err != nil {
		fail("读取文件列表失败", err)
		return
	}
	var tpls []harness.Template
	if all, err := st.Templates(ctx); err == nil {
		for _, t := range all {
			if !t.Enabled || (job.TemplateID != nil && t.ID != *job.TemplateID) {
				continue
			}
			tpls = append(tpls, toHarnessTemplate(t))
		}
	}
	// scan: verify the spooled copy is still there and unmodified size-wise.
	for _, f := range files {
		if f.Error != nil || f.StoredPath == "" {
			continue
		}
		if fi, err := os.Stat(f.StoredPath); err != nil || fi.Size() != f.SizeBytes {
			msg := "暂存文件丢失或被改动，请重新导入"
			_ = st.SaveAnalysis(ctx, f.ID, store.AnalysisResult{ReviewRequired: true, Error: &msg})
		}
	}
	_ = st.Progress(ctx, job.ID)
	if cancelled() {
		return
	}
	_ = st.SetJobStatus(ctx, job.ID, "analyzing")
	s.event(ctx, job.ID, "analyzing", "phase_started", nil, "Agent 解析文档并识别部门、类型与日期", nil)
	files, err = st.JobFiles(ctx, job.ID)
	if err != nil {
		fail("读取文件列表失败", err)
		return
	}
	// The model is fixed by the task row, never by whoever is logged in now (this goroutine has no HTTP user).
	tm, merr := s.taskModel(ctx, job)
	if merr != nil {
		reportError("llm", "TASK_MODEL_UNAVAILABLE", "任务绑定的大模型不可用（来源："+job.LLMSource+"）", merr)
	}
	sem := make(chan struct{}, s.cfg.FileParallel)
	var wg sync.WaitGroup
	for _, f := range files {
		if f.Error != nil || f.ArchiveStatus == "archived" || f.StoredPath == "" {
			continue
		}
		if done, _ := s.fileProcessed(ctx, f.ID); done {
			continue
		}
		if cancelled() {
			break
		}
		if merr != nil {
			// No fallback to another model: the file fails with a clear reason and can be retried after the cause is fixed.
			msg := userMessage(merr)
			_ = st.SaveAnalysis(ctx, f.ID, store.AnalysisResult{ReviewRequired: true, Error: &msg})
			fid := f.ID
			s.event(ctx, job.ID, "analyzing", "file_failed", &fid, f.SourceName+"："+msg, nil)
			_ = st.Progress(ctx, job.ID)
			continue
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(f imports.File) {
			defer wg.Done()
			defer func() { <-sem }()
			// A panic fails this one file with a numbered message instead of killing the process.
			meta := map[string]any{"task_id": job.ID, "file_id": f.ID, "ext": filepath.Ext(f.SourceName), "size_bytes": f.SizeBytes}
			if id := diag.SafeD("pipeline.analyze", meta, func() { s.analyzeOne(ctx, job, f, tpls, tm) }); id != "" {
				msg := diag.UserMessage(id)
				fid := f.ID
				_ = st.SaveAnalysis(ctx, f.ID, store.AnalysisResult{ReviewRequired: true, Error: &msg})
				s.event(ctx, job.ID, "analyzing", "file_failed", &fid, f.SourceName+"："+msg, nil)
				_ = st.Progress(ctx, job.ID)
			}
		}(f)
	}
	wg.Wait()
	if ctx.Err() != nil {
		return // shutting down: job is re-queued by RecoverJobs at next start
	}
	if cancelled() {
		return
	}
	s.buildPlan(ctx, job)
}

func (s *Service) fileProcessed(ctx context.Context, id string) (bool, error) {
	var p bool
	err := s.cfg.Store.Pool.QueryRow(ctx, `SELECT processed FROM file_records WHERE id=$1`, id).Scan(&p)
	return p, err
}

// taskModel resolves the model a task is bound to and records the model that actually runs it.
func (s *Service) taskModel(ctx context.Context, job imports.Job) (llm.TaskModel, error) {
	if s.cfg.Models == nil {
		return llm.TaskModel{}, nil
	}
	tm, err := s.cfg.Models.TaskProvider(ctx, job.LLMSource, job.LLMOwner)
	if err != nil {
		return tm, err
	}
	if job.LLMSource != imports.SourceNone && tm.Model != "" && tm.Model != job.LLMModel {
		_ = s.cfg.Store.SetJobModel(ctx, job.ID, tm.Model)
	}
	s.event(ctx, job.ID, "analyzing", "model_selected", nil, modelSummary(job, tm.Model), map[string]any{"llm_source": job.LLMSource, "model": tm.Model, "automation": job.Automation, "instruction": job.Instruction != ""})
	return tm, nil
}

func modelSummary(job imports.Job, model string) string {
	switch job.LLMSource {
	case imports.SourcePersonal:
		return "使用创建者的个人大模型：" + model
	case imports.SourceGlobal:
		return "使用全局大模型：" + model
	}
	return "未使用大模型：离线规则识别"
}

func (s *Service) analyzeOne(ctx context.Context, job imports.Job, f imports.File, tpls []harness.Template, tm llm.TaskModel) {
	taskID := job.ID
	fid := f.ID
	emit := func(e harness.Event) {
		s.event(ctx, taskID, e.Phase, e.Type, &fid, e.Summary, e.Details)
	}
	started := time.Now()
	res, err := s.cfg.Analyzer.AnalyzeFile(ctx, harness.FileInput{FileID: f.ID, SourceName: f.SourceName, SourcePath: f.SourcePath, MIMEType: f.MIMEType, StoredPath: f.StoredPath,
		Instruction: job.Instruction, Provider: tm.Provider, Vision: tm.Vision}, tpls, emit)
	logx.Debugf("analyze file=%s task=%s ext=%s took=%s err=%v", f.ID, taskID, filepath.Ext(f.SourceName), time.Since(started).Round(time.Millisecond), err)
	if ctx.Err() != nil {
		return
	}
	if err == nil {
		// A tool observation with a fatal worker code (encrypted, corrupt, unsupported, too large) means the file
		// itself is unusable: report it as a file error instead of guessing from the filename alone.
		for _, t := range res.Trace {
			switch t.ErrorCode {
			case harness.CodeEncrypted, harness.CodeUnreadableFile, harness.CodeUnsupportedFormat, harness.CodeFileTooLarge:
				err = &harness.WorkerError{Code: t.ErrorCode}
			}
		}
	}
	if err != nil {
		s.reportAnalyzeFailure(job, f, err)
		msg := userMessage(err)
		_ = s.cfg.Store.SaveAnalysis(ctx, f.ID, store.AnalysisResult{ReviewRequired: true, Error: &msg})
		s.event(ctx, taskID, "analyzing", "file_failed", &fid, f.SourceName+"："+msg, nil)
		_ = s.cfg.Store.Progress(ctx, taskID)
		return
	}
	ocr := false
	for _, t := range res.Trace {
		if t.Name == "run_ocr" && t.OK {
			ocr = true
		}
	}
	an := map[string]any{"trace": res.Trace, "warnings": res.Warnings}
	if res.OCR != nil && res.OCR.Used {
		an["ocr"] = res.OCR
		ocr = true
	}
	if err := s.cfg.Store.SaveAnalysis(ctx, f.ID, store.AnalysisResult{Department: res.Department, DocumentType: res.DocumentType, DocumentDate: res.DocumentDate, Evidence: res.Evidence, ReviewRequired: res.ReviewRequired, TemplateID: res.TemplateID, OCRUsed: ocr, Analysis: an}); err != nil {
		log.Printf("save analysis: %v", err)
		reportTask("pipeline", "SAVE_ANALYSIS_FAILED", "保存识别结果失败", err, taskID, f.ID, nil)
		return
	}
	_ = s.cfg.Store.Progress(ctx, taskID)
	d, t, dt := strOr(res.Department), strOr(res.DocumentType), strOr(res.DocumentDate)
	s.event(ctx, taskID, "analyzing", "file_classified", &fid, fmt.Sprintf("%s → %s / %s / %s", f.SourceName, d, t, dt), nil)
}

func strOr(p *string) string {
	if p == nil {
		return "待审核"
	}
	return *p
}

// userMessage turns analysis errors into short, non-sensitive Chinese messages.
func userMessage(err error) string {
	var tme *llm.TaskModelError
	if errors.As(err, &tme) {
		return tme.Msg
	}
	var we *harness.WorkerError
	if errors.As(err, &we) {
		switch we.Code {
		case harness.CodeEncrypted:
			return "文件已加密，无法读取"
		case harness.CodeUnreadableFile:
			return "文件损坏或无法解析"
		case harness.CodeUnsupportedFormat:
			return "不支持的文件格式"
		case harness.CodeFileTooLarge:
			return "文件过大"
		case harness.CodeOCRUnavailable:
			return "OCR 组件不可用，无法识别扫描件"
		case harness.CodeExtractTimeout:
			return "解析超时"
		case harness.CodeWorkerUnavailable:
			return "文档解析服务不可用"
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "识别超时"
	}
	return "识别失败"
}

func toHarnessTemplate(t store.Template) harness.Template {
	strs := func(m map[string]any, k string) []string {
		var out []string
		if arr, ok := m[k].([]any); ok {
			for _, x := range arr {
				if s, ok := x.(string); ok && strings.TrimSpace(s) != "" {
					out = append(out, strings.TrimSpace(s))
				}
			}
		}
		return out
	}
	str := func(m map[string]any, k string) string { s, _ := m[k].(string); return strings.TrimSpace(s) }
	return harness.Template{ID: t.ID, Name: t.Name, Enabled: t.Enabled, Priority: t.Priority,
		FilenameKeywords: strs(t.MatchRules, "filename_keywords"), TextKeywords: strs(t.MatchRules, "text_keywords"),
		Department: str(t.FieldMappings, "department"), DocumentType: str(t.FieldMappings, "document_type"), DateSource: str(t.FieldMappings, "date_source")}
}

// ---- planning ----

func (s *Service) activeRule(ctx context.Context) (store.Rule, archive.Rule) {
	r, err := s.cfg.Store.ActiveRule(ctx)
	if err != nil {
		r = store.Rule{NamingPattern: "{date}_{department}_{type}_{original}", DirectoryPattern: "{department}/{year}/{type}"}
	}
	return r, archive.Rule{NamingPattern: r.NamingPattern, DirectoryPattern: r.DirectoryPattern}
}

func (s *Service) buildPlan(ctx context.Context, job imports.Job) {
	st := s.cfg.Store
	if cur, err := st.GetJob(ctx, job.ID); err == nil {
		job = cur // the model recorded at the start of analysis
	}
	// Fully automatic archiving only ever runs on top of a real model; the API guarantees it, this keeps it true.
	auto := job.Automation == imports.AutomationAuto && job.LLMSource != imports.SourceNone
	_ = st.SetJobStatus(ctx, job.ID, "planning")
	s.event(ctx, job.ID, "planning", "phase_started", nil, "按规则生成标准文件名与归档路径", nil)
	files, err := st.JobFiles(ctx, job.ID)
	if err != nil {
		_ = st.FailJob(ctx, job.ID, "读取文件失败")
		return
	}
	stored, rule := s.activeRule(ctx)
	var cand []archive.PlanItem
	meta := map[string]imports.File{}
	renderWarn := map[string][]string{}
	for _, f := range files {
		if f.Error != nil || f.ArchiveStatus == "archived" {
			continue
		}
		name, dir, warns, err := archive.Render(rule, f.SourceName, archive.Meta{Department: f.Department, DocumentType: f.DocumentType, Date: f.Date})
		if err != nil {
			msg := "归档规则渲染失败：" + err.Error()
			_ = st.SaveAnalysis(ctx, f.ID, store.AnalysisResult{Department: f.Department, DocumentType: f.DocumentType, DocumentDate: f.Date, Evidence: f.Evidence, ReviewRequired: true, Error: &msg})
			continue
		}
		cand = append(cand, archive.PlanItem{FileID: f.ID, Dir: dir, Name: name})
		meta[f.ID] = f
		renderWarn[f.ID] = warns
	}
	_ = st.Progress(ctx, job.ID)
	if len(cand) == 0 {
		archived := 0
		for _, f := range files {
			if f.ArchiveStatus == "archived" {
				archived++
			}
		}
		if archived > 0 { // retry of a job whose remaining files cannot be processed
			_ = st.SetJobStatus(ctx, job.ID, "partial_failure")
			s.event(ctx, job.ID, "planning", "task_finished", nil, "没有新的可归档文件；已归档文件保持不变", nil)
			return
		}
		_ = st.FailJob(ctx, job.ID, "没有可归档的文件")
		s.event(ctx, job.ID, "failed", "task_failed", nil, "没有可归档的文件（全部解析失败或被拒绝）", nil)
		return
	}
	planned := archive.Plan(func(rel string) bool {
		_, err := os.Lstat(filepath.Join(s.cfg.ArchiveRoot, filepath.FromSlash(rel)))
		return err == nil
	}, cand)
	items := make([]store.PlanItem, 0, len(planned))
	for _, p := range planned {
		f := meta[p.FileID]
		warns := append(append([]string{}, renderWarn[p.FileID]...), p.Warnings...)
		duplicate := false
		if prev, ok, _ := st.ArchivedByHash(ctx, f.SHA256); ok {
			warns = append(warns, "与已归档文件内容相同："+prev)
			duplicate = true
		}
		review := f.ReviewRequired || f.Department == nil || f.DocumentType == nil || f.Date == nil
		if review {
			warns = append(warns, "字段置信度低或信息缺失，需人工确认")
		}
		if auto && duplicate && !review {
			// a repeated import is a human decision, never an automatic one
			review = true
			warns = append(warns, "全自动模式：内容与已归档文件相同，需人工确认是否重复归档")
		}
		items = append(items, store.PlanItem{FileID: f.ID, OriginalName: f.SourceName, SuggestedName: p.Name, SourcePath: f.SourcePath, DestinationPath: p.Rel, Department: f.Department, DocumentType: f.DocumentType, DocumentDate: f.Date, Warnings: warns, ReviewRequired: review})
	}
	var ruleID *string
	if stored.ID != "" {
		ruleID = &stored.ID
	}
	if auto {
		var ready, left []store.PlanItem
		for _, it := range items {
			if it.ReviewRequired {
				left = append(left, it)
			} else {
				ready = append(ready, it)
			}
		}
		if len(ready) > 0 {
			s.planAndRunAuto(ctx, job, ruleID, ready, left)
			return
		}
		s.event(ctx, job.ID, "planning", "auto_nothing_ready", nil, "全自动模式：模型没有能够完全确定的条目，未自动归档任何文件，全部转人工审核", map[string]any{"automation": "auto", "model": job.LLMModel})
	}
	plan, err := st.CreatePlan(ctx, job.ID, ruleID, items)
	if err != nil {
		log.Printf("create plan: %v", err)
		reportTask("pipeline", "CREATE_PLAN_FAILED", "生成归档计划失败", err, job.ID, "", nil)
		_ = st.FailJob(ctx, job.ID, "生成归档计划失败")
		return
	}
	if c, err := st.CancelRequested(ctx, job.ID); err == nil && c {
		_ = st.SetJobStatus(ctx, job.ID, "cancelled")
		s.event(ctx, job.ID, "cancelled", "task_cancelled", nil, "任务已取消", nil)
		return
	}
	_ = st.SetJobStatus(ctx, job.ID, "awaiting_review")
	s.event(ctx, job.ID, "planning", "plan_ready", nil, fmt.Sprintf("归档计划已生成，共 %d 项，等待审批", len(plan.Items)), map[string]any{"plan_id": plan.ID})
}

// Derive recomputes the standard name and destination for one plan item (used after manual edits).
func (s *Service) Derive(ctx context.Context, it store.PlanItem) (string, string, []string, error) {
	_, rule := s.activeRule(ctx)
	name, dir, warns, err := archive.Render(rule, it.OriginalName, archive.Meta{Department: it.Department, DocumentType: it.DocumentType, Date: it.DocumentDate})
	if err != nil {
		return "", "", nil, err
	}
	planned := archive.Plan(func(rel string) bool {
		_, err := os.Lstat(filepath.Join(s.cfg.ArchiveRoot, filepath.FromSlash(rel)))
		return err == nil
	}, []archive.PlanItem{{FileID: it.FileID, Dir: dir, Name: name}})
	p := planned[0]
	return p.Name, p.Rel, append(warns, p.Warnings...), nil
}

func (s *Service) ValidateRule(r store.Rule) error {
	if r.NamingPattern == "" || r.DirectoryPattern == "" {
		return errors.New("命名规则与目录规则不能为空")
	}
	d, dep, ty := "2026-03-15", "财务部", "合同"
	if strings.Contains(r.DirectoryPattern, "..") || strings.ContainsAny(r.DirectoryPattern, "\\:") || strings.HasPrefix(r.DirectoryPattern, "/") {
		return errors.New("目录规则必须是不含 .. 的相对路径")
	}
	name, dir, _, err := archive.Render(archive.Rule{NamingPattern: r.NamingPattern, DirectoryPattern: r.DirectoryPattern}, "示例.docx", archive.Meta{Department: &dep, DocumentType: &ty, Date: &d})
	if err != nil {
		return fmt.Errorf("规则无效：%v（可用占位符：{date}{year}{month}{day}{department}{type}{original}{stem}{ext}）", err)
	}
	if name == "" || strings.HasPrefix(name, ".") {
		return errors.New("命名规则渲染后文件名无效")
	}
	if dir != "" {
		if err := archive.ValidateRelative(dir); err != nil {
			return errors.New("目录规则必须渲染为不含 .. 的相对路径")
		}
	}
	return nil
}

func (s *Service) Providers() []api.ProviderStatus {
	if s.cfg.ProviderInfo != nil {
		n, m, st := s.cfg.ProviderInfo()
		return []api.ProviderStatus{{Name: n, Model: m, State: st}}
	}
	state := "ok"
	if !s.cfg.ProviderOK {
		state = "unconfigured"
	}
	return []api.ProviderStatus{{Name: s.cfg.ProviderName, Model: s.cfg.Model, State: state}}
}

// ---- execute / rollback ----

func (s *Service) planLock(id string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.plan[id]
	if m == nil {
		m = &sync.Mutex{}
		s.plan[id] = m
	}
	return m
}

func (s *Service) ExecutePlan(ctx context.Context, planID, key, actor string) (store.Plan, error) {
	return s.executePlan(ctx, planID, key, actor, store.DecisionManual, "")
}

// executePlan runs a plan; decision/model mark who decided (a human approval, or the model in automatic mode).
func (s *Service) executePlan(ctx context.Context, planID, key, actor, decision, model string) (store.Plan, error) {
	l := s.planLock(planID)
	l.Lock()
	defer l.Unlock()
	// A client disconnect must not abort a half-finished archive run; the journal makes re-runs safe anyway.
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Minute)
	defer cancel()
	op, plan, replay, err := s.cfg.Store.BeginExecutionAs(dctx, planID, key, decision, model)
	if err != nil {
		return plan, err
	}
	if replay {
		return plan, nil
	}
	if op.Status == "completed" {
		return s.cfg.Store.GetPlan(dctx, planID)
	}
	return s.run(dctx, op, plan, actor)
}

func (s *Service) run(ctx context.Context, op store.Operation, plan store.Plan, actor string) (store.Plan, error) {
	ops := make([]archive.Op, 0, len(plan.Items))
	for _, it := range plan.Items {
		f, err := s.cfg.Store.GetFile(ctx, it.FileID)
		if err != nil {
			return plan, err
		}
		if err := archive.ValidateRelative(it.DestinationPath); err != nil {
			return plan, &api.ExecError{Status: 422, Code: "INVALID_PATH", Msg: "计划中存在非法目标路径，请修订后重新批准"}
		}
		if f.StoredPath == "" {
			return plan, &api.ExecError{Status: 409, Code: "SOURCE_MISSING", Msg: "存在缺少暂存文件的条目"}
		}
		ops = append(ops, archive.Op{OpID: op.ID, FileID: it.FileID, SourceStored: f.StoredPath, DestRel: it.DestinationPath, Sha256: f.SHA256})
	}
	auto := op.Decision == store.DecisionAIAuto
	if auto {
		s.event(ctx, plan.TaskID, "archiving", "auto_archive_started", nil, fmt.Sprintf("全自动模式：未经人工审批，按模型 %s 的判断开始归档 %d 个文件", op.DecisionModel, len(ops)), map[string]any{"automation": "auto", "model": op.DecisionModel, "operation_id": op.ID})
	} else {
		s.event(ctx, plan.TaskID, "archiving", "phase_started", nil, fmt.Sprintf("开始归档 %d 个文件", len(ops)), nil)
	}
	res := s.exec.Execute(ctx, ops)
	out := make([]store.OpResult, 0, len(res))
	for _, r := range res {
		out = append(out, store.OpResult{OpID: r.OpID, FileID: r.FileID, FinalRel: r.FinalRel, Status: r.Status, Err: r.Err, SHA256: r.Sha256, CreatedDirs: r.CreatedDirs})
	}
	done, err := s.cfg.Store.FinishExecution(ctx, op, plan, out)
	if err != nil {
		return plan, err
	}
	published := 0
	for _, r := range out {
		if r.Status != archive.StatusFailed {
			published++
		}
	}
	s.cfg.Store.Audit(ctx, actor, "plan.execute", plan.ID, fmt.Sprintf("执行归档：成功 %d / %d", published, len(out)))
	if auto {
		s.cfg.Store.Audit(ctx, actor, "ai.auto_archive.executed", plan.ID, fmt.Sprintf("全自动归档：任务 %s，成功 %d / %d，模型 %s，操作 %s", plan.TaskID, published, len(out), op.DecisionModel, op.ID))
	}
	typ := "archive_completed"
	if published < len(out) {
		typ = "archive_partial"
	}
	summary := fmt.Sprintf("已归档 %d/%d 个文件并写入台账", published, len(out))
	details := map[string]any{"operation_id": op.ID}
	if auto {
		summary = fmt.Sprintf("全自动模式：已自动归档 %d/%d 个文件并写入台账（模型 %s 决定；可在操作历史中撤销）", published, len(out), op.DecisionModel)
		details["automation"], details["model"] = "auto", op.DecisionModel
	}
	s.event(ctx, plan.TaskID, "archiving", typ, nil, summary, details)
	return done, nil
}

func (s *Service) resumeExecution(ctx context.Context, planID string) {
	l := s.planLock(planID)
	l.Lock()
	defer l.Unlock()
	op, err := s.cfg.Store.RunningOperation(ctx, planID)
	if err != nil {
		return
	}
	plan, err := s.cfg.Store.GetPlan(ctx, planID)
	if err != nil {
		return
	}
	actor := "系统恢复"
	if op.Decision == store.DecisionAIAuto && plan.ApprovedBy != nil {
		actor = *plan.ApprovedBy
	}
	if _, err := s.run(ctx, op, plan, actor); err != nil {
		log.Printf("resume execution %s: %v", planID, err)
		reportError("pipeline", "RESUME_EXECUTION_FAILED", "恢复归档执行失败", err)
	}
}

// planAndRunAuto stores the automatic plan (+ a leftover plan for items that need a human) and executes the former.
func (s *Service) planAndRunAuto(ctx context.Context, job imports.Job, ruleID *string, ready, left []store.PlanItem) {
	st := s.cfg.Store
	actor := "ai-auto:" + job.AckBy
	auto, leftover, err := st.CreateAutoPlans(ctx, job.ID, ruleID, ready, left, actor)
	if err != nil {
		reportError("pipeline", "AUTO_PLAN_FAILED", "生成全自动归档计划失败", err)
		_ = st.FailJob(ctx, job.ID, "生成归档计划失败")
		return
	}
	details := map[string]any{"plan_id": auto.ID, "automation": "auto", "model": job.LLMModel, "auto_items": len(ready), "pending_items": len(left)}
	summary := fmt.Sprintf("全自动模式：%d 项已由模型 %s 完全确定，将自动归档", len(ready), job.LLMModel)
	if leftover != nil {
		details["pending_plan_id"] = leftover.ID
		summary += fmt.Sprintf("；%d 项未能确定，保留在待审核计划中", len(left))
	}
	s.event(ctx, job.ID, "planning", "auto_plan_ready", nil, summary, details)
	if c, err := st.CancelRequested(ctx, job.ID); err == nil && c {
		// cancelled while planning: nothing has been written yet
		_ = st.SetJobStatus(ctx, job.ID, "cancelled")
		s.event(ctx, job.ID, "cancelled", "task_cancelled", nil, "任务已取消", nil)
		return
	}
	s.runAuto(ctx, job, auto.ID)
}

// runAuto executes an approved automatic plan under the actor ai-auto:<username> and an idempotency key derived from
// the task, so a repeated call (crash recovery) can never archive twice.
func (s *Service) runAuto(ctx context.Context, job imports.Job, planID string) {
	actor := "ai-auto:" + job.AckBy
	if _, err := s.executePlan(ctx, planID, "auto:"+job.ID, actor, store.DecisionAIAuto, job.LLMModel); err != nil {
		reportError("pipeline", "AUTO_EXECUTE_FAILED", "全自动归档执行失败", err)
		s.event(context.WithoutCancel(ctx), job.ID, "archiving", "auto_archive_failed", nil, "全自动归档未能完成，请在任务中查看并手动处理", map[string]any{"automation": "auto"})
	}
}

func (s *Service) resumeAuto(ctx context.Context, ref store.AutoPlanRef) {
	job, err := s.cfg.Store.GetJob(ctx, ref.TaskID)
	if err != nil || job.Automation != imports.AutomationAuto {
		return
	}
	s.runAuto(ctx, job, ref.PlanID)
}

func (s *Service) Rollback(ctx context.Context, opID, actor string) error {
	op, err := s.cfg.Store.Operation(ctx, opID)
	if err != nil {
		return err
	}
	if op.Status != "completed" {
		return &api.ExecError{Status: 409, Code: "INVALID_STATE", Msg: "仅已完成的归档操作可以恢复"}
	}
	cur, err := s.cfg.Store.OperationLedger(ctx, op.ID)
	if err != nil {
		return err
	}
	for _, r := range op.Results {
		if l, ok := cur[r.FileID]; ok && r.Status != archive.StatusFailed && (l.Status != "archived" || l.Path != r.FinalRel) {
			return &api.ExecError{Status: 409, Code: "FILES_CHANGED", Msg: "部分文件已在文件管理中被移动、重命名或删除，不能整体撤销；请在文件管理中单独处理"}
		}
	}
	res := make([]archive.OpResult, 0, len(op.Results))
	for _, r := range op.Results {
		res = append(res, archive.OpResult{OpID: r.OpID, FileID: r.FileID, FinalRel: r.FinalRel, Status: r.Status, Err: r.Err, Sha256: r.SHA256, CreatedDirs: r.CreatedDirs})
	}
	if err := s.exec.Rollback(ctx, res); err != nil {
		return &api.ExecError{Status: 409, Code: "ROLLBACK_INCOMPLETE", Msg: "部分文件已被修改，未删除这些文件：" + err.Error()}
	}
	if err := s.cfg.Store.MarkRolledBack(ctx, op); err != nil {
		return err
	}
	s.cfg.Store.Audit(ctx, actor, "operation.rollback", op.ID, "恢复归档操作（仅移除本次发布的文件，原件未动）")
	return nil
}

// ---- template test ----

var testExt = map[string]string{".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document", ".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", ".pdf": "application/pdf", ".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg"}

func (s *Service) TestTemplate(ctx context.Context, t store.Template, filename string, content ioReader) (api.TemplateTestResult, error) {
	ext := strings.ToLower(path.Ext(filename))
	mime, ok := testExt[ext]
	if !ok {
		return api.TemplateTestResult{}, &api.ExecError{Status: 422, Code: "UNSUPPORTED_FORMAT", Msg: "样本格式不支持（支持 docx/xlsx/pdf/png/jpg）"}
	}
	dir := filepath.Join(s.cfg.SpoolRoot, "template-tests")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return api.TemplateTestResult{}, err
	}
	id, _ := imports.NewID()
	tmp := filepath.Join(dir, id+ext)
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return api.TemplateTestResult{}, err
	}
	defer os.Remove(tmp)
	n, err := copyN(f, content, 20<<20)
	f.Close()
	if err != nil {
		return api.TemplateTestResult{}, err
	}
	if n == 0 {
		return api.TemplateTestResult{}, &api.ExecError{Status: 422, Code: "EMPTY_FILE", Msg: "样本文件为空"}
	}
	if n > 20<<20 {
		return api.TemplateTestResult{}, &api.ExecError{Status: 413, Code: "FILE_TOO_LARGE", Msg: "样本超过 20 MiB"}
	}
	ht := toHarnessTemplate(t)
	ht.Enabled = true // testing works for disabled templates too
	res, err := s.cfg.Analyzer.AnalyzeFile(ctx, harness.FileInput{FileID: id, SourceName: filepath.Base(filename), SourcePath: filepath.Base(filename), MIMEType: mime, StoredPath: tmp}, []harness.Template{ht}, func(harness.Event) {})
	if err != nil {
		return api.TemplateTestResult{}, &api.ExecError{Status: 422, Code: "UNREADABLE_FILE", Msg: userMessage(err)}
	}
	return api.TemplateTestResult{Matched: res.TemplateID != nil, Department: res.Department, DocumentType: res.DocumentType, DocumentDate: res.DocumentDate, Evidence: append([]string{}, res.Evidence...)}, nil
}
