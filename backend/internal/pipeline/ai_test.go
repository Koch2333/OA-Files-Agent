package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"oa-agent/backend/internal/harness"
	"oa-agent/backend/internal/imports"
	"oa-agent/backend/internal/llm"
	"oa-agent/backend/internal/store"
)

// importAI is importFiles with AI settings on the task row.
func importAI(t *testing.T, st *store.Store, spool string, files map[string]string, job func(*imports.Job)) imports.Batch {
	t.Helper()
	id, _ := imports.NewID()
	now := time.Now().UTC()
	b := imports.Batch{Job: imports.Job{ID: id, Status: "queued", TotalFiles: len(files), CreatedAt: now, UpdatedAt: now}, Files: []imports.File{}, Failures: []imports.Failure{}}
	job(&b.Job)
	for name, content := range files {
		fid, _ := imports.NewID()
		p := filepath.Join(spool, fid+".pdf")
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		h := sha256.Sum256([]byte(content))
		b.Files = append(b.Files, imports.File{ID: fid, TaskID: id, SourceName: name, SourcePath: "in/" + name, MIMEType: "application/pdf", SHA256: hex.EncodeToString(h[:]), SizeBytes: int64(len(content)), Evidence: []string{}, ArchiveStatus: "pending", CreatedAt: now, StoredPath: p})
	}
	saved, _, err := st.Save(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

// recordingAnalyzer remembers what each file was analysed with.
type recordingAnalyzer struct {
	fakeAnalyzer
	mu   sync.Mutex
	seen map[string]harness.FileInput
}

func (r *recordingAnalyzer) AnalyzeFile(ctx context.Context, in harness.FileInput, t []harness.Template, emit func(harness.Event)) (harness.Result, error) {
	r.mu.Lock()
	if r.seen == nil {
		r.seen = map[string]harness.FileInput{}
	}
	r.seen[in.SourceName] = in
	r.mu.Unlock()
	return r.fakeAnalyzer.AnalyzeFile(ctx, in, t, emit)
}

type tagProvider struct{ tag string }

func (tagProvider) Chat(context.Context, []harness.Message, []harness.ToolSpec) (harness.Response, error) {
	return harness.Response{}, nil
}

// fakeModels maps (source, owner) to a tagged provider; unknown personal owners are "deleted".
type fakeModels struct {
	mu    sync.Mutex
	calls [][2]string
	gone  map[string]bool
}

func (f *fakeModels) TaskProvider(_ context.Context, source, owner string) (llm.TaskModel, error) {
	f.mu.Lock()
	f.calls = append(f.calls, [2]string{source, owner})
	f.mu.Unlock()
	switch source {
	case "none":
		return llm.TaskModel{Provider: harness.Heuristic{}}, nil
	case "global":
		return llm.TaskModel{Provider: tagProvider{"global"}, Model: "g-model"}, nil
	}
	if f.gone[owner] {
		return llm.TaskModel{}, &llm.TaskModelError{Msg: "创建者的个人大模型配置已被删除，任务无法继续"}
	}
	return llm.TaskModel{Provider: tagProvider{"personal:" + owner}, Model: "p-" + owner, Vision: func() harness.VisionTranscriber { return nil }}, nil
}

func newAISvc(t *testing.T, st *store.Store, an Analyzer, m ModelResolver) (*Service, string, string) {
	t.Helper()
	spool, arch := t.TempDir(), t.TempDir()
	svc, err := New(Config{Store: st, Analyzer: an, ArchiveRoot: arch, SpoolRoot: spool, Models: m})
	if err != nil {
		t.Fatal(err)
	}
	return svc, spool, arch
}

func runJob(t *testing.T, st *store.Store, svc *Service) imports.Job {
	t.Helper()
	job, err := st.ClaimJob(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	svc.process(context.Background(), job)
	return job
}

func tagOf(in harness.FileInput) string {
	if p, ok := in.Provider.(tagProvider); ok {
		return p.tag
	}
	if _, ok := in.Provider.(harness.Heuristic); ok {
		return "heuristic"
	}
	return "<nil>"
}

func TestEachTaskIsAnalysedWithItsOwnStoredModelAndInstruction(t *testing.T) {
	st := testStore(t)
	good := harness.Result{Department: sp("采购部"), DocumentType: sp("合同"), DocumentDate: sp("2026-03-15"), Evidence: []string{"x"}}
	an := &recordingAnalyzer{fakeAnalyzer: fakeAnalyzer{results: map[string]harness.Result{"a.pdf": good, "b.pdf": good, "c.pdf": good, "d.pdf": good}}}
	models := &fakeModels{}
	svc, spool, _ := newAISvc(t, st, an, models)
	ctx := context.Background()
	mk := func(src, owner, model, instr string) func(*imports.Job) {
		return func(j *imports.Job) { j.LLMSource, j.LLMOwner, j.LLMModel, j.Instruction = src, owner, model, instr }
	}
	b1 := importAI(t, st, spool, map[string]string{"a.pdf": "A1"}, mk("personal", "alice", "p-alice", "只处理采购类文件"))
	b2 := importAI(t, st, spool, map[string]string{"b.pdf": "B2"}, mk("personal", "bob", "p-bob", ""))
	b3 := importAI(t, st, spool, map[string]string{"c.pdf": "C3"}, mk("global", "alice", "g-model", ""))
	b4 := importAI(t, st, spool, map[string]string{"d.pdf": "D4"}, mk("none", "alice", "", ""))
	for i := 0; i < 4; i++ {
		runJob(t, st, svc)
	}
	want := map[string]struct{ tag, instr string }{"a.pdf": {"personal:alice", "只处理采购类文件"}, "b.pdf": {"personal:bob", ""}, "c.pdf": {"global", ""}, "d.pdf": {"heuristic", ""}}
	for name, w := range want {
		in, ok := an.seen[name]
		if !ok || tagOf(in) != w.tag || in.Instruction != w.instr {
			t.Errorf("%s analysed with provider=%q instruction=%q, want %q/%q", name, tagOf(in), in.Instruction, w.tag, w.instr)
		}
	}
	if in := an.seen["a.pdf"]; in.Vision == nil {
		t.Error("a personal task brings its own vision selection")
	}
	// the model actually used is recorded on the task (a personal config may have changed since creation)
	for _, c := range []struct {
		id, model string
	}{{b1.Job.ID, "p-alice"}, {b2.Job.ID, "p-bob"}, {b3.Job.ID, "g-model"}, {b4.Job.ID, ""}} {
		if j, _ := st.GetJob(ctx, c.id); j.LLMModel != c.model {
			t.Errorf("task %s llm_model = %q, want %q", c.id, j.LLMModel, c.model)
		}
	}
	// the selection event says which engine ran
	evs, _ := st.Events(ctx, b1.Job.ID, 0, 100)
	found := false
	for _, e := range evs {
		if e.EventType == "model_selected" && strings.Contains(e.Summary, "个人大模型") && e.Details["model"] == "p-alice" {
			found = true
		}
	}
	if !found {
		t.Error("model_selected event missing")
	}
}

func TestTaskWhoseModelIsGoneFailsItsFilesWithoutFallingBack(t *testing.T) {
	st := testStore(t)
	an := &recordingAnalyzer{fakeAnalyzer: fakeAnalyzer{results: map[string]harness.Result{"a.pdf": {Department: sp("采购部"), DocumentType: sp("合同"), DocumentDate: sp("2026-03-15"), Evidence: []string{"x"}}}}}
	svc, spool, _ := newAISvc(t, st, an, &fakeModels{gone: map[string]bool{"alice": true}})
	var reported []string
	ErrorReportHook = func(source, code, msg string) { reported = append(reported, source+"/"+code) }
	defer func() { ErrorReportHook = nil }()
	b := importAI(t, st, spool, map[string]string{"a.pdf": "A1"}, func(j *imports.Job) { j.LLMSource, j.LLMOwner, j.LLMModel = "personal", "alice", "p-alice" })
	runJob(t, st, svc)
	if len(an.seen) != 0 {
		t.Fatal("the analyzer must not run with some other model")
	}
	files, _ := st.JobFiles(context.Background(), b.Job.ID)
	if files[0].Error == nil || !strings.Contains(*files[0].Error, "已被删除") {
		t.Fatalf("file error must say why: %v", files[0].Error)
	}
	if j, _ := st.GetJob(context.Background(), b.Job.ID); j.Status != "failed" {
		t.Fatalf("no file could be analysed: %s", j.Status)
	}
	if len(reported) != 1 || reported[0] != "llm/TASK_MODEL_UNAVAILABLE" {
		t.Fatalf("the error report hook must fire once: %v", reported)
	}
}

func autoJob(model string) func(*imports.Job) {
	return func(j *imports.Job) {
		j.Automation, j.LLMSource, j.LLMOwner, j.LLMModel, j.AckVersion, j.AckBy = "auto", "global", "alice", model, llm.AutoAckVersion, "alice"
	}
}

func auditHas(t *testing.T, st *store.Store, action, actor string) store.AuditLog {
	t.Helper()
	logs, _, err := st.AuditLogs(context.Background(), store.Page{Size: 200})
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range logs {
		if l.Action == action && (actor == "" || l.Actor == actor) {
			return l
		}
	}
	t.Fatalf("audit entry %s (actor %q) not found in %+v", action, actor, logs)
	return store.AuditLog{}
}

func TestAutomaticTaskArchivesEverythingWithPlaceholders(t *testing.T) {
	st := testStore(t)
	good := func(d string) harness.Result {
		return harness.Result{Department: sp("采购部"), DocumentType: sp("合同"), DocumentDate: sp(d), Evidence: []string{"文件名含“合同”"}}
	}
	an := &fakeAnalyzer{results: map[string]harness.Result{
		"合同甲.pdf": good("2026-03-15"), "合同乙.pdf": good("2026-03-16"),
		"不明.pdf":  {ReviewRequired: true, Evidence: []string{}},
		"缺部门.pdf": {DocumentType: sp("合同"), Evidence: []string{"x"}, ReviewRequired: false},
	}}
	svc, spool, arch := newAISvc(t, st, an, &fakeModels{})
	ctx := context.Background()
	b := importAI(t, st, spool, map[string]string{"合同甲.pdf": "AAA", "合同乙.pdf": "BBB", "不明.pdf": "CCC", "缺部门.pdf": "DDD"}, autoJob("g-model"))
	runJob(t, st, svc)

	job, _ := st.GetJob(ctx, b.Job.ID)
	if job.Status != "completed" || job.PlanID == nil {
		t.Fatalf("automatic mode leaves nothing pending: every file is archived, undetermined fields get placeholders: %+v", job)
	}
	rows, total, _ := st.Ledger(ctx, store.Page{}, store.LedgerFilter{})
	if total != 4 {
		t.Fatalf("ledger rows = %d, want 4", total)
	}
	placeholder := 0
	for _, r := range rows {
		if r.Decision != "ai_auto" || r.DecisionModel != "g-model" || r.Status != "archived" {
			t.Fatalf("ledger decision: %+v", r)
		}
		if got, err := os.ReadFile(filepath.Join(arch, filepath.FromSlash(r.ArchivePath))); err != nil || len(got) != 3 {
			t.Fatalf("archived file %s: %v", r.ArchivePath, err)
		}
		if r.Department == "其他" || r.DocumentType == "其他" {
			placeholder++
		}
	}
	if placeholder != 2 {
		t.Fatalf("the two undetermined files are archived under the placeholder 其他 (to be corrected afterwards in the ledger), got %d", placeholder)
	}
	exec := auditHas(t, st, "ai.auto_archive.executed", "ai-auto:alice")
	if !strings.Contains(exec.Summary, "g-model") || !strings.Contains(exec.Summary, "4 / 4") || strings.Contains(strings.ToLower(exec.Summary), "key") {
		t.Fatalf("audit summary: %q", exec.Summary)
	}
	auditHas(t, st, "plan.execute", "ai-auto:alice")
	// the trace says clearly that the automatic mode ran
	evs, _ := st.Events(ctx, b.Job.ID, 0, 200)
	seen := map[string]bool{}
	for _, e := range evs {
		seen[e.EventType] = true
		if e.EventType == "auto_archive_started" && !strings.Contains(e.Summary, "未经人工审批") {
			t.Errorf("start event must say no human approved: %q", e.Summary)
		}
	}
	for _, typ := range []string{"auto_plan_ready", "auto_archive_started", "archive_completed"} {
		if !seen[typ] {
			t.Errorf("event %s missing", typ)
		}
	}

	// rollback of the automatic operation works and is audited as before
	autoPlan, err := planOf(st, b.Job.ID, "executed")
	if err != nil || autoPlan.OperationID == nil {
		t.Fatalf("auto plan: %+v %v", autoPlan, err)
	}
	if autoPlan.ApprovedBy == nil || *autoPlan.ApprovedBy != "ai-auto:alice" || autoPlan.RequiresApproval {
		t.Fatalf("the automatic plan must show it was approved by the automatic mode: %+v", autoPlan)
	}
	if err := svc.Rollback(ctx, *autoPlan.OperationID, "alice"); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if _, err := os.Stat(filepath.Join(arch, filepath.FromSlash(r.ArchivePath))); !os.IsNotExist(err) {
			t.Fatalf("rollback must remove the published copy: %v", err)
		}
	}
	auditHas(t, st, "operation.rollback", "alice")
	if _, total, _ := st.Ledger(ctx, store.Page{}, store.LedgerFilter{}); total != 0 {
		t.Fatal("rolled-back entries leave the ledger")
	}
	// startup recovery must NOT re-run a rolled-back automatic plan
	if refs, _ := st.RecoverAutoPlans(ctx); len(refs) != 0 {
		t.Fatalf("rollback must stay rolled back: %+v", refs)
	}
	if j, _ := st.GetJob(ctx, b.Job.ID); j.Status != "approved" {
		t.Fatalf("task after rollback: %s", j.Status)
	}
}

func planOf(st *store.Store, taskID, status string) (store.Plan, error) {
	var id string
	err := st.Pool.QueryRow(context.Background(), `SELECT id::text FROM archive_plans WHERE task_id=$1 AND status=$2`, taskID, status).Scan(&id)
	if err != nil {
		return store.Plan{}, err
	}
	return st.GetPlan(context.Background(), id)
}

func TestAutomaticTaskWithEverythingDeterminedCompletes(t *testing.T) {
	st := testStore(t)
	res := harness.Result{Department: sp("财务部"), DocumentType: sp("报表"), DocumentDate: sp("2026-01-31"), Evidence: []string{"x"}}
	svc, spool, arch := newAISvc(t, st, &fakeAnalyzer{results: map[string]harness.Result{"报表.pdf": res}}, &fakeModels{})
	b := importAI(t, st, spool, map[string]string{"报表.pdf": "RPT"}, autoJob("g-model"))
	runJob(t, st, svc)
	ctx := context.Background()
	job, _ := st.GetJob(ctx, b.Job.ID)
	if job.Status != "completed" {
		t.Fatalf("nothing pending: completed, got %s", job.Status)
	}
	plan, _ := st.GetPlan(ctx, *job.PlanID)
	if plan.Status != "executed" || len(plan.Items) != 1 {
		t.Fatalf("%+v", plan)
	}
	if _, err := os.Stat(filepath.Join(arch, filepath.FromSlash(plan.Items[0].DestinationPath))); err != nil {
		t.Fatal(err)
	}
	// idempotent: running the same automatic plan again changes nothing
	svc.runAuto(ctx, job, plan.ID)
	if _, total, _ := st.Ledger(ctx, store.Page{}, store.LedgerFilter{}); total != 1 {
		t.Fatalf("ledger rows = %d", total)
	}
}

func TestAutomaticModeArchivesEvenWhenNothingIsDetermined(t *testing.T) {
	st := testStore(t)
	svc, spool, arch := newAISvc(t, st, &fakeAnalyzer{results: map[string]harness.Result{"不明.pdf": {ReviewRequired: true, Evidence: []string{}}}}, &fakeModels{})
	b := importAI(t, st, spool, map[string]string{"不明.pdf": "X"}, autoJob("g-model"))
	runJob(t, st, svc)
	ctx := context.Background()
	job, _ := st.GetJob(ctx, b.Job.ID)
	if job.Status != "completed" {
		t.Fatalf("no review step before completion: %+v", job)
	}
	rows, total, _ := st.Ledger(ctx, store.Page{}, store.LedgerFilter{})
	if total != 1 || rows[0].Department != "其他" || rows[0].DocumentType != "其他" || rows[0].Decision != "ai_auto" {
		t.Fatalf("archived under the placeholders, marked as an automatic decision: %+v", rows)
	}
	if ents, _ := os.ReadDir(arch); len(ents) == 0 {
		t.Fatal("the file must be in the archive")
	}
	evs, _ := st.Events(ctx, b.Job.ID, 0, 100)
	found := false
	for _, e := range evs {
		found = found || (e.EventType == "auto_plan_ready" && strings.Contains(e.Summary, "占位"))
	}
	if !found {
		t.Error("the trace must say that placeholders were used")
	}
}

func TestAutomaticModeHoldsRepeatedImportsForAHuman(t *testing.T) {
	st := testStore(t)
	res := harness.Result{Department: sp("财务部"), DocumentType: sp("报表"), DocumentDate: sp("2026-01-31"), Evidence: []string{"x"}}
	svc, spool, _ := newAISvc(t, st, &fakeAnalyzer{results: map[string]harness.Result{"报表.pdf": res}}, &fakeModels{})
	ctx := context.Background()
	importAI(t, st, spool, map[string]string{"报表.pdf": "SAME"}, autoJob("g-model"))
	runJob(t, st, svc)
	b2 := importAI(t, st, spool, map[string]string{"报表.pdf": "SAME"}, autoJob("g-model"))
	runJob(t, st, svc)
	job, _ := st.GetJob(ctx, b2.Job.ID)
	plan, _ := st.GetPlan(ctx, *job.PlanID)
	if job.Status != "awaiting_review" || len(plan.Items) != 1 || !plan.Items[0].ReviewRequired {
		t.Fatalf("a duplicate of an archived file is a human decision: %+v %+v", job, plan)
	}
	if _, total, _ := st.Ledger(ctx, store.Page{}, store.LedgerFilter{}); total != 1 {
		t.Fatalf("ledger rows = %d", total)
	}
}

func TestAutomaticFlagWithoutAModelNeverRunsAutomatically(t *testing.T) {
	st := testStore(t)
	res := harness.Result{Department: sp("财务部"), DocumentType: sp("报表"), DocumentDate: sp("2026-01-31"), Evidence: []string{"x"}}
	svc, spool, _ := newAISvc(t, st, &fakeAnalyzer{results: map[string]harness.Result{"报表.pdf": res}}, &fakeModels{})
	b := importAI(t, st, spool, map[string]string{"报表.pdf": "R"}, func(j *imports.Job) { j.Automation, j.LLMSource = "auto", "none" })
	runJob(t, st, svc)
	job, _ := st.GetJob(context.Background(), b.Job.ID)
	if job.Status != "awaiting_review" {
		t.Fatalf("a heuristic task must never auto-archive: %s", job.Status)
	}
	if _, total, _ := st.Ledger(context.Background(), store.Page{}, store.LedgerFilter{}); total != 0 {
		t.Fatal("archived")
	}
}

func TestStartupRecoversAnApprovedButNeverExecutedAutomaticPlan(t *testing.T) {
	st := testStore(t)
	res := harness.Result{Department: sp("财务部"), DocumentType: sp("报表"), DocumentDate: sp("2026-01-31"), Evidence: []string{"x"}}
	svc, spool, arch := newAISvc(t, st, &fakeAnalyzer{results: map[string]harness.Result{"报表.pdf": res}}, &fakeModels{})
	ctx := context.Background()
	b := importAI(t, st, spool, map[string]string{"报表.pdf": "RPT"}, autoJob("g-model"))
	files, _ := st.JobFiles(ctx, b.Job.ID)
	if err := st.SaveAnalysis(ctx, files[0].ID, store.AnalysisResult{Department: res.Department, DocumentType: res.DocumentType, DocumentDate: res.DocumentDate, Evidence: res.Evidence}); err != nil {
		t.Fatal(err)
	}
	// simulate a crash right after planning: the automatic plan exists and is approved, but nothing ran
	it := store.PlanItem{FileID: files[0].ID, OriginalName: "报表.pdf", SuggestedName: "报表.pdf", SourcePath: "in/报表.pdf", DestinationPath: "财务部/2026/报表.pdf", Department: res.Department, DocumentType: res.DocumentType, DocumentDate: res.DocumentDate, Warnings: []string{}}
	auto, _, err := st.CreateAutoPlans(ctx, b.Job.ID, nil, []store.PlanItem{it}, nil, "ai-auto:alice")
	if err != nil {
		t.Fatal(err)
	}
	refs, _ := st.RecoverAutoPlans(ctx)
	if len(refs) != 1 {
		t.Fatalf("%+v", refs)
	}
	svc.resumeAuto(ctx, refs[0])
	if _, err := os.Stat(filepath.Join(arch, "财务部", "2026", "报表.pdf")); err != nil {
		t.Fatalf("recovered plan must be executed: %v", err)
	}
	rows, _, _ := st.Ledger(ctx, store.Page{}, store.LedgerFilter{})
	if len(rows) != 1 || rows[0].Decision != "ai_auto" || rows[0].DecisionModel != "g-model" {
		t.Fatalf("%+v", rows)
	}
	if j, _ := st.GetJob(ctx, b.Job.ID); j.Status != "completed" || *j.PlanID != auto.ID {
		t.Fatalf("%+v", j)
	}
}
