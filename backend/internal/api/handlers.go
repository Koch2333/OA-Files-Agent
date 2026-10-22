package api

import (
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"

	"oa-agent/backend/internal/imports"
	"oa-agent/backend/internal/store"
)

func (s *Server) business() {
	s.handle("/v1/imports", map[string]http.HandlerFunc{"POST": s.createImport})
	s.handle("/v1/tasks", map[string]http.HandlerFunc{"GET": s.listTasks})
	s.handle("/v1/tasks/{id}", map[string]http.HandlerFunc{"GET": s.getTask})
	s.handle("/v1/tasks/{id}/events", map[string]http.HandlerFunc{"GET": s.taskEvents})
	s.handle("/v1/tasks/{id}/cancel", map[string]http.HandlerFunc{"POST": s.cancelTask})
	s.handle("/v1/tasks/{id}/retry", map[string]http.HandlerFunc{"POST": s.retryTask})
	s.handle("/v1/files", map[string]http.HandlerFunc{"GET": s.listFiles})
	s.handle("/v1/files/{id}", map[string]http.HandlerFunc{"GET": s.getFile})
	s.handle("/v1/files/{id}/correction-proposals", map[string]http.HandlerFunc{"POST": s.proposeCorrection})
	s.handle("/v1/correction-proposals/{id}/approve", map[string]http.HandlerFunc{"POST": s.approveCorrection})
	s.handle("/v1/archive-plans/{id}", map[string]http.HandlerFunc{"GET": s.getPlan, "PATCH": s.patchPlan})
	s.handle("/v1/archive-plans/{id}/approve", map[string]http.HandlerFunc{"POST": s.approvePlan})
	s.handle("/v1/archive-plans/{id}/execute", map[string]http.HandlerFunc{"POST": s.executePlan})
	s.handle("/v1/archive-operations/{id}/rollback", map[string]http.HandlerFunc{"POST": s.rollback})
	s.handle("/v1/ledger", map[string]http.HandlerFunc{"GET": s.ledger})
	s.handle("/v1/ledger/export", map[string]http.HandlerFunc{"GET": s.exportLedger})
	s.handle("/v1/oa-templates", map[string]http.HandlerFunc{"GET": s.listTemplates, "POST": s.createTemplate})
	s.handle("/v1/oa-templates/{id}", map[string]http.HandlerFunc{"GET": s.getTemplate, "PATCH": s.patchTemplate, "DELETE": s.deleteTemplate})
	s.handle("/v1/oa-templates/{id}/test", map[string]http.HandlerFunc{"POST": s.testTemplate})
	s.handle("/v1/archive-rules", map[string]http.HandlerFunc{"GET": s.listRules, "POST": s.createRule})
	s.handle("/v1/audit-logs", map[string]http.HandlerFunc{"GET": s.auditLogs})
	s.handle("/v1/providers", map[string]http.HandlerFunc{"GET": s.providers})
}

// ---- imports & tasks ----

func (s *Server) createImport(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Idempotency-Key")
	if len(key) > 128 {
		s.fail(w, r, 400, "INVALID_IDEMPOTENCY_KEY", "Idempotency-Key 过长", nil)
		return
	}
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(10 * time.Minute))
	_ = rc.SetWriteDeadline(time.Now().Add(10 * time.Minute))
	templateID := r.URL.Query().Get("template_id")
	if templateID != "" {
		t, err := s.d.Store.Template(r.Context(), templateID)
		if err != nil {
			s.fail(w, r, 400, "INVALID_TEMPLATE", "指定的 OA 模板不存在", nil)
			return
		}
		if !t.Enabled {
			s.fail(w, r, 400, "TEMPLATE_DISABLED", "指定的 OA 模板已停用", nil)
			return
		}
	}
	r.Body = http.MaxBytesReader(w, r.Body, imports.MaxRequestBytes)
	reader, err := r.MultipartReader()
	if err != nil {
		s.fail(w, r, 400, "INVALID_MULTIPART", "请使用 multipart/form-data 上传", nil)
		return
	}
	ictx, oerr := s.withImportAI(r.Context(), r)
	if oerr != nil {
		s.fail(w, r, oerr.status, oerr.code, oerr.msg, nil)
		return
	}
	batch, err := s.d.Imports.Import(ictx, reader, key, templateID)
	if err != nil {
		var ie *imports.Error
		var mbe *http.MaxBytesError
		switch {
		case errors.As(err, &ie):
			s.fail(w, r, ie.Status, ie.Code, ie.Message, nil)
		case errors.As(err, &mbe):
			s.fail(w, r, 413, "REQUEST_TOO_LARGE", "请求体超过上限", nil)
		case errors.Is(err, store.ErrConflict):
			s.fail(w, r, 409, "IDEMPOTENCY_CONFLICT", "相同 Idempotency-Key 对应了不同的上传内容", nil)
		default:
			s.internal(w, r, "导入失败", err)
		}
		return
	}
	s.d.Store.Audit(r.Context(), actorOf(r), "import.create", batch.Job.ID, fmt.Sprintf("导入 %d 个文件，其中 %d 个在接收时被拒绝", batch.Job.TotalFiles, batch.Job.FailureCount))
	s.auditAutoAccepted(r.Context(), r, batch.Job, batch.Replay)
	_ = s.d.Store.AppendEvent(r.Context(), batch.Job.ID, "queued", "task_created", nil, createdSummary(batch.Job), createdDetails(batch.Job))
	s.ok(w, 202, batch.Job)
}

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	page, size := pageParams(r)
	status := r.URL.Query().Get("status")
	if status != "" && !validStatus[status] {
		s.fail(w, r, 400, "INVALID_ARGUMENT", "status 取值无效", nil)
		return
	}
	jobs, total, err := s.d.Store.ListJobs(r.Context(), store.Page{Page: page, Size: size}, status)
	if err != nil {
		s.storeErr(w, r, err, "任务")
		return
	}
	s.list(w, jobs, total, page, size)
}

var validStatus = map[string]bool{"queued": true, "scanning": true, "extracting": true, "analyzing": true, "planning": true, "awaiting_review": true, "approved": true, "archiving": true, "completed": true, "partial_failure": true, "failed": true, "cancelled": true}

func (s *Server) getTask(w http.ResponseWriter, r *http.Request) {
	j, err := s.d.Store.GetJob(r.Context(), r.PathValue("id"))
	if err != nil {
		s.storeErr(w, r, err, "任务")
		return
	}
	s.ok(w, 200, j)
}

func (s *Server) cancelTask(w http.ResponseWriter, r *http.Request) {
	j, err := s.d.Store.CancelJob(r.Context(), r.PathValue("id"))
	if err != nil {
		s.storeErr(w, r, err, "任务")
		return
	}
	s.d.Store.Audit(r.Context(), actorOf(r), "task.cancel", j.ID, "请求取消任务")
	_ = s.d.Store.AppendEvent(r.Context(), j.ID, "cancelled", "cancel_requested", nil, "已请求取消任务", nil)
	s.ok(w, 200, j)
}

func (s *Server) retryTask(w http.ResponseWriter, r *http.Request) {
	j, err := s.d.Store.RetryJob(r.Context(), r.PathValue("id"))
	if err != nil {
		s.storeErr(w, r, err, "任务")
		return
	}
	s.d.Store.Audit(r.Context(), actorOf(r), "task.retry", j.ID, "重新排队任务")
	_ = s.d.Store.AppendEvent(r.Context(), j.ID, "queued", "task_retry", nil, "任务已重新排队", nil)
	s.ok(w, 200, j)
}

// ---- files ----

func (s *Server) listFiles(w http.ResponseWriter, r *http.Request) {
	page, size := pageParams(r)
	q := r.URL.Query()
	f := store.FileFilter{Q: q.Get("q"), Department: q.Get("department"), Type: q.Get("type"), DateFrom: q.Get("date_from"), DateTo: q.Get("date_to"), ArchiveStatus: q.Get("archive_status"), TaskID: q.Get("task_id")}
	for _, d := range []string{f.DateFrom, f.DateTo} {
		if d != "" {
			if _, err := time.Parse("2006-01-02", d); err != nil {
				s.fail(w, r, 400, "INVALID_ARGUMENT", "日期格式应为 YYYY-MM-DD", nil)
				return
			}
		}
	}
	if f.TaskID != "" && !isUUID(f.TaskID) {
		s.fail(w, r, 400, "INVALID_ARGUMENT", "task_id 无效", nil)
		return
	}
	if f.ArchiveStatus != "" && f.ArchiveStatus != "pending" && f.ArchiveStatus != "archived" && f.ArchiveStatus != "failed" {
		s.fail(w, r, 400, "INVALID_ARGUMENT", "archive_status 取值无效", nil)
		return
	}
	if v := q.Get("review_required"); v != "" {
		b := v == "true"
		if v != "true" && v != "false" {
			s.fail(w, r, 400, "INVALID_ARGUMENT", "review_required 须为 true/false", nil)
			return
		}
		f.ReviewRequired = &b
	}
	files, total, err := s.d.Store.ListFiles(r.Context(), store.Page{Page: page, Size: size}, f)
	if err != nil {
		s.storeErr(w, r, err, "文件")
		return
	}
	s.list(w, files, total, page, size)
}

func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

func (s *Server) getFile(w http.ResponseWriter, r *http.Request) {
	f, err := s.d.Store.GetFile(r.Context(), r.PathValue("id"))
	if err != nil {
		s.storeErr(w, r, err, "文件")
		return
	}
	s.ok(w, 200, f)
}

// ---- corrections ----

func (s *Server) proposeCorrection(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Instruction string `json:"instruction"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	body.Instruction = strings.TrimSpace(body.Instruction)
	if body.Instruction == "" || len([]rune(body.Instruction)) > 500 {
		s.fail(w, r, 400, "INVALID_ARGUMENT", "请用 1–500 字描述需要如何修正", nil)
		return
	}
	c, err := s.d.Backend.ProposeCorrection(r.Context(), r.PathValue("id"), body.Instruction)
	if err != nil {
		var ue *UnderstandError
		if errors.As(err, &ue) {
			s.fail(w, r, 422, "UNDERSTAND_FAILED", ue.Error(), nil)
			return
		}
		s.storeErr(w, r, err, "文件")
		return
	}
	s.ok(w, 201, c)
}

// UnderstandError means the instruction could not be mapped to concrete field changes.
type UnderstandError struct{ Msg string }

func (e *UnderstandError) Error() string { return e.Msg }

func (s *Server) approveCorrection(w http.ResponseWriter, r *http.Request) {
	c, err := s.d.Store.Correction(r.Context(), r.PathValue("id"))
	if err != nil {
		s.storeErr(w, r, err, "修改建议")
		return
	}
	c, err = s.d.Store.ApplyCorrection(r.Context(), c, func(it store.PlanItem) (string, string, []string, error) { return s.d.Backend.Derive(r.Context(), it) })
	if err != nil {
		s.storeErr(w, r, err, "修改建议")
		return
	}
	s.d.Store.Audit(r.Context(), actorOf(r), "correction.approve", c.FileID, "应用单文件修正（不改动全局规则）")
	s.ok(w, 200, c)
}

// ---- plans ----

func (s *Server) getPlan(w http.ResponseWriter, r *http.Request) {
	p, err := s.d.Store.GetPlan(r.Context(), r.PathValue("id"))
	if err != nil {
		s.storeErr(w, r, err, "归档计划")
		return
	}
	s.ok(w, 200, p)
}

func (s *Server) patchPlan(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Version int               `json:"version"`
		Items   []store.PlanPatch `json:"items"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	if body.Version < 1 || len(body.Items) == 0 || len(body.Items) > 500 {
		s.fail(w, r, 400, "INVALID_ARGUMENT", "需要 version 与 1–500 个修订项", nil)
		return
	}
	for _, it := range body.Items {
		if !isUUID(it.FileID) {
			s.fail(w, r, 400, "INVALID_ARGUMENT", "file_id 无效", nil)
			return
		}
		if it.DocumentDate != nil && *it.DocumentDate != "" {
			if _, err := time.Parse("2006-01-02", *it.DocumentDate); err != nil {
				s.fail(w, r, 422, "INVALID_DATE", "日期格式应为 YYYY-MM-DD 且为有效日期", nil)
				return
			}
		}
		for _, v := range []*string{it.Department, it.DocumentType} {
			if v != nil && len([]rune(*v)) > 60 {
				s.fail(w, r, 422, "INVALID_ARGUMENT", "部门/类型过长", nil)
				return
			}
		}
		if it.SuggestedName != nil {
			if err := imports.ValidatePath(*it.SuggestedName); err != nil || strings.Contains(*it.SuggestedName, "/") {
				s.fail(w, r, 422, "INVALID_NAME", "标准文件名无效（不能含路径分隔符或非法字符）", nil)
				return
			}
		}
		if it.DestinationPath != nil {
			if err := imports.ValidatePath(*it.DestinationPath); err != nil {
				s.fail(w, r, 422, "INVALID_PATH", "目标路径必须是不含 .. 的相对路径", nil)
				return
			}
		}
	}
	p, err := s.d.Store.PatchPlan(r.Context(), r.PathValue("id"), body.Version, body.Items, func(it store.PlanItem) (string, string, []string, error) {
		return s.d.Backend.Derive(r.Context(), it)
	})
	if err != nil {
		s.storeErr(w, r, err, "归档计划")
		return
	}
	s.d.Store.Audit(r.Context(), actorOf(r), "plan.patch", p.ID, fmt.Sprintf("人工修订 %d 项，版本 v%d", len(body.Items), p.Version))
	s.ok(w, 200, p)
}

func (s *Server) approvePlan(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Version int `json:"version"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	if body.Version < 1 {
		s.fail(w, r, 400, "INVALID_ARGUMENT", "需要 version", nil)
		return
	}
	p, err := s.d.Store.ApprovePlan(r.Context(), r.PathValue("id"), body.Version, actorOf(r))
	if err != nil {
		s.storeErr(w, r, err, "归档计划")
		return
	}
	s.d.Store.Audit(r.Context(), actorOf(r), "plan.approve", p.ID, fmt.Sprintf("批准归档计划 v%d，%d 项", p.Version, len(p.Items)))
	_ = s.d.Store.AppendEvent(r.Context(), p.TaskID, "approved", "plan_approved", nil, "归档计划已批准，等待执行", nil)
	s.ok(w, 200, p)
}

func (s *Server) executePlan(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Idempotency-Key")
	if key == "" || len(key) > 128 {
		s.fail(w, r, 400, "IDEMPOTENCY_KEY_REQUIRED", "执行归档必须提供 Idempotency-Key（≤128 字符）", nil)
		return
	}
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Now().Add(10 * time.Minute))
	p, err := s.d.Backend.ExecutePlan(r.Context(), r.PathValue("id"), key, actorOf(r))
	if err != nil {
		var ee *ExecError
		if errors.As(err, &ee) {
			s.fail(w, r, ee.Status, ee.Code, ee.Msg, nil)
			return
		}
		if errors.Is(err, store.ErrState) {
			s.fail(w, r, 409, "NOT_APPROVED", "计划尚未批准或状态不允许执行", nil)
			return
		}
		s.storeErr(w, r, err, "归档计划")
		return
	}
	s.ok(w, 200, p)
}

// ExecError lets the backend return a specific contract error from plan execution.
type ExecError struct {
	Status    int
	Code, Msg string
}

func (e *ExecError) Error() string { return e.Code }

func (s *Server) rollback(w http.ResponseWriter, r *http.Request) {
	if err := s.d.Backend.Rollback(r.Context(), r.PathValue("id"), actorOf(r)); err != nil {
		var ee *ExecError
		if errors.As(err, &ee) {
			s.fail(w, r, ee.Status, ee.Code, ee.Msg, nil)
			return
		}
		s.storeErr(w, r, err, "归档操作")
		return
	}
	s.ok(w, 200, map[string]string{"status": "rolled_back"})
}

// ---- ledger ----

func ledgerFilter(r *http.Request) store.LedgerFilter {
	q := r.URL.Query()
	return store.LedgerFilter{Q: q.Get("q"), Department: q.Get("department"), Type: q.Get("type"), DateFrom: q.Get("date_from"), DateTo: q.Get("date_to"), Sort: q.Get("sort"), Order: q.Get("order"), Decision: q.Get("decision")}
}

// validDecision accepts an empty filter or manual|ai_auto.
func validDecision(r *http.Request) bool {
	d := r.URL.Query().Get("decision")
	return d == "" || d == store.DecisionManual || d == store.DecisionAIAuto
}

func (s *Server) ledger(w http.ResponseWriter, r *http.Request) {
	page, size := pageParams(r)
	if !validDecision(r) {
		s.fail(w, r, 400, "INVALID_ARGUMENT", "decision 须为 manual 或 ai_auto", nil)
		return
	}
	f := ledgerFilter(r)
	items, total, err := s.d.Store.Ledger(r.Context(), store.Page{Page: page, Size: size}, f)
	if err != nil {
		s.storeErr(w, r, err, "台账")
		return
	}
	s.list(w, items, total, page, size)
}

// ---- templates ----

type templateInput struct {
	Name          *string         `json:"name"`
	Enabled       *bool           `json:"enabled"`
	Priority      *int            `json:"priority"`
	MatchRules    *map[string]any `json:"match_rules"`
	FieldMappings *map[string]any `json:"field_mappings"`
}

func validateTemplate(t store.Template) string {
	n := len([]rune(strings.TrimSpace(t.Name)))
	if n < 1 || n > 120 {
		return "模板名称需为 1–120 个字符"
	}
	if t.Priority < 0 || t.Priority > 10000 {
		return "优先级需在 0–10000"
	}
	count := 0
	for k, v := range t.MatchRules {
		if k != "filename_keywords" && k != "text_keywords" {
			return "match_rules 仅支持 filename_keywords 与 text_keywords"
		}
		arr, ok := v.([]any)
		if !ok || len(arr) > 50 {
			return k + " 必须是最多 50 项的字符串数组"
		}
		for _, x := range arr {
			str, ok := x.(string)
			if !ok || strings.TrimSpace(str) == "" || len([]rune(str)) > 60 {
				return k + " 的每一项须为 1–60 字符的非空字符串"
			}
			count++
		}
	}
	if count == 0 {
		return "至少需要一个文件名或正文关键词，否则模板不会匹配任何文件"
	}
	for k, v := range t.FieldMappings {
		str, ok := v.(string)
		if !ok {
			return "field_mappings 的值必须是字符串"
		}
		switch k {
		case "department", "document_type":
			if len([]rune(str)) > 60 {
				return k + " 过长"
			}
		case "date_source":
			if str != "ocr_text" && str != "filename" && str != "file_metadata" {
				return "date_source 须为 ocr_text / filename / file_metadata"
			}
		default:
			return "field_mappings 不支持字段 " + k
		}
	}
	return ""
}

func (s *Server) listTemplates(w http.ResponseWriter, r *http.Request) {
	ts, err := s.d.Store.Templates(r.Context())
	if err != nil {
		s.storeErr(w, r, err, "模板")
		return
	}
	s.ok(w, 200, ts)
}

func (s *Server) getTemplate(w http.ResponseWriter, r *http.Request) {
	t, err := s.d.Store.Template(r.Context(), r.PathValue("id"))
	if err != nil {
		s.storeErr(w, r, err, "模板")
		return
	}
	s.ok(w, 200, t)
}

func (s *Server) createTemplate(w http.ResponseWriter, r *http.Request) {
	var in templateInput
	if !s.decode(w, r, &in) {
		return
	}
	t := store.Template{Enabled: true, Priority: 100, MatchRules: map[string]any{}, FieldMappings: map[string]any{}}
	applyTemplate(&t, in)
	if msg := validateTemplate(t); msg != "" {
		s.fail(w, r, 422, "INVALID_TEMPLATE", msg, nil)
		return
	}
	t.Name = strings.TrimSpace(t.Name)
	out, err := s.d.Store.CreateTemplate(r.Context(), t)
	if err != nil {
		s.storeErr(w, r, err, "模板")
		return
	}
	s.d.Store.Audit(r.Context(), actorOf(r), "template.create", out.ID, "新建 OA 模板「"+out.Name+"」")
	s.ok(w, 201, out)
}

func applyTemplate(t *store.Template, in templateInput) {
	if in.Name != nil {
		t.Name = *in.Name
	}
	if in.Enabled != nil {
		t.Enabled = *in.Enabled
	}
	if in.Priority != nil {
		t.Priority = *in.Priority
	}
	if in.MatchRules != nil {
		t.MatchRules = *in.MatchRules
	}
	if in.FieldMappings != nil {
		t.FieldMappings = *in.FieldMappings
	}
}

func (s *Server) patchTemplate(w http.ResponseWriter, r *http.Request) {
	var in templateInput
	if !s.decode(w, r, &in) {
		return
	}
	t, err := s.d.Store.Template(r.Context(), r.PathValue("id"))
	if err != nil {
		s.storeErr(w, r, err, "模板")
		return
	}
	applyTemplate(&t, in)
	if msg := validateTemplate(t); msg != "" {
		s.fail(w, r, 422, "INVALID_TEMPLATE", msg, nil)
		return
	}
	t.Name = strings.TrimSpace(t.Name)
	out, err := s.d.Store.UpdateTemplate(r.Context(), t)
	if err != nil {
		s.storeErr(w, r, err, "模板")
		return
	}
	s.d.Store.Audit(r.Context(), actorOf(r), "template.update", out.ID, "更新 OA 模板「"+out.Name+"」")
	s.ok(w, 200, out)
}

func (s *Server) deleteTemplate(w http.ResponseWriter, r *http.Request) {
	if err := s.d.Store.DeleteTemplate(r.Context(), r.PathValue("id")); err != nil {
		s.storeErr(w, r, err, "模板")
		return
	}
	s.d.Store.Audit(r.Context(), actorOf(r), "template.delete", r.PathValue("id"), "删除 OA 模板")
	w.WriteHeader(204)
}

const maxTestSample = 20 << 20

func (s *Server) testTemplate(w http.ResponseWriter, r *http.Request) {
	t, err := s.d.Store.Template(r.Context(), r.PathValue("id"))
	if err != nil {
		s.storeErr(w, r, err, "模板")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxTestSample+(1<<20))
	reader, err := r.MultipartReader()
	if err != nil {
		s.fail(w, r, 400, "INVALID_MULTIPART", "请以 multipart/form-data 上传样本文件（字段名 file）", nil)
		return
	}
	var part *multipart.Part
	for {
		p, e := reader.NextPart()
		if e != nil {
			break
		}
		if p.FormName() == "file" {
			part = p
			break
		}
	}
	if part == nil || part.FileName() == "" {
		s.fail(w, r, 400, "INVALID_MULTIPART", "缺少样本文件（字段名 file）", nil)
		return
	}
	res, err := s.d.Backend.TestTemplate(r.Context(), t, part.FileName(), io.LimitReader(part, maxTestSample+1))
	if err != nil {
		var ee *ExecError
		if errors.As(err, &ee) {
			s.fail(w, r, ee.Status, ee.Code, ee.Msg, nil)
			return
		}
		s.internal(w, r, "模板测试失败", err)
		return
	}
	s.ok(w, 200, res)
}

// ---- rules / audit / providers ----

func (s *Server) listRules(w http.ResponseWriter, r *http.Request) {
	rs, err := s.d.Store.Rules(r.Context())
	if err != nil {
		s.storeErr(w, r, err, "规则")
		return
	}
	s.ok(w, 200, rs)
}

func (s *Server) createRule(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name             string `json:"name"`
		NamingPattern    string `json:"naming_pattern"`
		DirectoryPattern string `json:"directory_pattern"`
		Enabled          *bool  `json:"enabled"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	rule := store.Rule{Name: strings.TrimSpace(in.Name), NamingPattern: strings.TrimSpace(in.NamingPattern), DirectoryPattern: strings.TrimSpace(in.DirectoryPattern), Enabled: in.Enabled == nil || *in.Enabled}
	if n := len([]rune(rule.Name)); n < 1 || n > 120 {
		s.fail(w, r, 422, "INVALID_RULE", "规则名称需为 1–120 个字符", nil)
		return
	}
	if err := s.d.Backend.ValidateRule(rule); err != nil {
		s.fail(w, r, 422, "INVALID_RULE", err.Error(), nil)
		return
	}
	out, err := s.d.Store.CreateRule(r.Context(), rule)
	if err != nil {
		s.storeErr(w, r, err, "规则")
		return
	}
	s.d.Store.Audit(r.Context(), actorOf(r), "rule.create", out.ID, "新增命名与归档规则「"+out.Name+"」")
	s.ok(w, 201, out)
}

func (s *Server) auditLogs(w http.ResponseWriter, r *http.Request) {
	page, size := pageParams(r)
	items, total, err := s.d.Store.AuditLogs(r.Context(), store.Page{Page: page, Size: size})
	if err != nil {
		s.storeErr(w, r, err, "审计日志")
		return
	}
	s.list(w, items, total, page, size)
}

func (s *Server) providers(w http.ResponseWriter, r *http.Request) {
	s.ok(w, 200, s.d.Backend.Providers())
}

var _ = url.QueryEscape
