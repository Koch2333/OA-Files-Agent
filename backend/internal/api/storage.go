package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"oa-agent/backend/internal/cloud"
	"oa-agent/backend/internal/imports"
	"oa-agent/backend/internal/storage"
	"oa-agent/backend/internal/store"
)

// StorageManager is the optional storage-connection service (implemented by storage.Service).
type StorageManager interface {
	Kinds() []cloud.Kind
	List(ctx context.Context) ([]storage.View, error)
	Get(ctx context.Context, id string) (storage.View, error)
	Create(ctx context.Context, in storage.Input, actor string) (storage.View, error)
	Update(ctx context.Context, id string, p storage.Patch, actor string) (storage.View, error)
	Delete(ctx context.Context, id, actor string) error
	TestDraft(ctx context.Context, in storage.TestInput, actor string) (storage.TestResult, error)
	TestSaved(ctx context.Context, id string, checkWrite bool, acceptHostKey, actor string) (storage.TestResult, error)
	Browse(ctx context.Context, id, path string) (storage.Listing, error)
	ImportRemote(ctx context.Context, connID string, items []string, templateID, key, actor string) (imports.Batch, error)
	Replicate(ctx context.Context, id, prefix, actor string) (int64, error)
	ListReplications(ctx context.Context, p store.Page, connID, status string) ([]store.Replication, int, error)
	RetryReplication(ctx context.Context, id string) (store.Replication, error)
}

func (s *Server) storageRoutes() {
	s.handle("/v1/storage/kinds", map[string]http.HandlerFunc{"GET": s.stKinds})
	s.handle("/v1/storage/connections", map[string]http.HandlerFunc{"GET": s.stList, "POST": s.stCreate})
	s.handle("/v1/storage/connections/test", map[string]http.HandlerFunc{"POST": s.stTestDraft})
	s.handle("/v1/storage/connections/{id}", map[string]http.HandlerFunc{"GET": s.stGet, "PATCH": s.stPatch, "DELETE": s.stDelete})
	s.handle("/v1/storage/connections/{id}/test", map[string]http.HandlerFunc{"POST": s.stTestSaved})
	s.handle("/v1/storage/connections/{id}/browse", map[string]http.HandlerFunc{"GET": s.stBrowse})
	s.handle("/v1/storage/connections/{id}/replicate", map[string]http.HandlerFunc{"POST": s.stReplicate})
	s.handle("/v1/storage/replications", map[string]http.HandlerFunc{"GET": s.stReplications})
	s.handle("/v1/storage/replications/{id}/retry", map[string]http.HandlerFunc{"POST": s.stRetry})
	s.handle("/v1/imports/remote", map[string]http.HandlerFunc{"POST": s.stImportRemote})
}

func (s *Server) stErr(w http.ResponseWriter, r *http.Request, err error) {
	var ve *storage.ValidationError
	var ue *storage.UpstreamError
	var ie *imports.Error
	switch {
	case errors.As(err, &ve):
		s.fail(w, r, 422, ve.Code, ve.Msg, nil)
	case errors.As(err, &ue):
		s.fail(w, r, 502, "UPSTREAM_ERROR", ue.Msg, nil)
	case errors.As(err, &ie):
		s.fail(w, r, ie.Status, ie.Code, ie.Message, nil)
	case errors.Is(err, storage.ErrNameTaken):
		s.fail(w, r, 409, "NAME_TAKEN", "已有同名的存储连接", nil)
	case errors.Is(err, store.ErrNotFound):
		s.fail(w, r, 404, "NOT_FOUND", "存储连接或路径不存在", nil)
	case errors.Is(err, store.ErrState):
		s.fail(w, r, 409, "INVALID_STATE", "该连接未启用同步，或当前状态不允许此操作", nil)
	case errors.Is(err, store.ErrConflict):
		s.fail(w, r, 409, "IDEMPOTENCY_CONFLICT", "相同 Idempotency-Key 对应了不同的内容", nil)
	default:
		s.internal(w, r, "内部错误", err)
	}
}

func (s *Server) stKinds(w http.ResponseWriter, r *http.Request) { s.ok(w, 200, s.d.Storage.Kinds()) }

func (s *Server) stList(w http.ResponseWriter, r *http.Request) {
	v, err := s.d.Storage.List(r.Context())
	if err != nil {
		s.stErr(w, r, err)
		return
	}
	s.ok(w, 200, v)
}

func (s *Server) stGet(w http.ResponseWriter, r *http.Request) {
	v, err := s.d.Storage.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		s.stErr(w, r, err)
		return
	}
	s.ok(w, 200, v)
}

func (s *Server) stCreate(w http.ResponseWriter, r *http.Request) {
	var in storage.Input
	if !s.decode(w, r, &in) {
		return
	}
	v, err := s.d.Storage.Create(r.Context(), in, actorOf(r))
	if err != nil {
		s.stErr(w, r, err)
		return
	}
	s.ok(w, 201, v)
}

func (s *Server) stPatch(w http.ResponseWriter, r *http.Request) {
	var p storage.Patch
	if !s.decode(w, r, &p) {
		return
	}
	v, err := s.d.Storage.Update(r.Context(), r.PathValue("id"), p, actorOf(r))
	if err != nil {
		s.stErr(w, r, err)
		return
	}
	s.ok(w, 200, v)
}

func (s *Server) stDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.d.Storage.Delete(r.Context(), r.PathValue("id"), actorOf(r)); err != nil {
		s.stErr(w, r, err)
		return
	}
	w.WriteHeader(204)
}

func (s *Server) stTestDraft(w http.ResponseWriter, r *http.Request) {
	var in storage.TestInput
	if !s.decode(w, r, &in) {
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(3 * time.Minute))
	res, err := s.d.Storage.TestDraft(r.Context(), in, actorOf(r))
	if err != nil {
		s.stErr(w, r, err)
		return
	}
	s.ok(w, 200, res)
}

func (s *Server) stTestSaved(w http.ResponseWriter, r *http.Request) {
	var in struct {
		CheckWrite    bool   `json:"check_write"`
		AcceptHostKey string `json:"accept_host_key"`
	}
	if r.ContentLength != 0 && !s.decode(w, r, &in) {
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(3 * time.Minute))
	res, err := s.d.Storage.TestSaved(r.Context(), r.PathValue("id"), in.CheckWrite, in.AcceptHostKey, actorOf(r))
	if err != nil {
		s.stErr(w, r, err)
		return
	}
	s.ok(w, 200, res)
}

func (s *Server) stBrowse(w http.ResponseWriter, r *http.Request) {
	l, err := s.d.Storage.Browse(r.Context(), r.PathValue("id"), r.URL.Query().Get("path"))
	if err != nil {
		s.stErr(w, r, err)
		return
	}
	entries := make([]map[string]any, 0, len(l.Entries))
	for _, e := range l.Entries {
		var mt any
		if !e.ModTime.IsZero() {
			mt = e.ModTime
		}
		entries = append(entries, map[string]any{"name": e.Name, "path": e.Path, "is_dir": e.IsDir, "size_bytes": e.Size, "modified_at": mt})
	}
	s.ok(w, 200, map[string]any{"path": l.Path, "parent": l.Parent, "entries": entries, "truncated": l.Truncated})
}

func (s *Server) stReplicate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		PathPrefix string `json:"path_prefix"`
	}
	if r.ContentLength != 0 && !s.decode(w, r, &in) {
		return
	}
	n, err := s.d.Storage.Replicate(r.Context(), r.PathValue("id"), in.PathPrefix, actorOf(r))
	if err != nil {
		s.stErr(w, r, err)
		return
	}
	s.ok(w, 202, map[string]any{"queued": n})
}

func (s *Server) stReplications(w http.ResponseWriter, r *http.Request) {
	page, size := pageParams(r)
	q := r.URL.Query()
	conn, status := q.Get("connection_id"), q.Get("status")
	if conn != "" && !isUUID(conn) {
		s.fail(w, r, 400, "INVALID_ARGUMENT", "connection_id 无效", nil)
		return
	}
	switch status {
	case "", "queued", "running", "done", "skipped", "failed":
	default:
		s.fail(w, r, 400, "INVALID_ARGUMENT", "status 取值无效", nil)
		return
	}
	items, total, err := s.d.Storage.ListReplications(r.Context(), store.Page{Page: page, Size: size}, conn, status)
	if err != nil {
		s.stErr(w, r, err)
		return
	}
	s.list(w, items, total, page, size)
}

func (s *Server) stRetry(w http.ResponseWriter, r *http.Request) {
	rep, err := s.d.Storage.RetryReplication(r.Context(), r.PathValue("id"))
	if err != nil {
		s.stErr(w, r, err)
		return
	}
	s.ok(w, 200, rep)
}

func (s *Server) stImportRemote(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Idempotency-Key")
	if len(key) > 128 {
		s.fail(w, r, 400, "INVALID_IDEMPOTENCY_KEY", "Idempotency-Key 过长", nil)
		return
	}
	var in struct {
		ConnectionID string `json:"connection_id"`
		Items        []struct {
			Path string `json:"path"`
		} `json:"items"`
		TemplateID string         `json:"template_id"`
		Options    *importOptions `json:"options"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if !isUUID(in.ConnectionID) || len(in.Items) == 0 || len(in.Items) > 100 {
		s.fail(w, r, 400, "INVALID_ARGUMENT", "需要 connection_id 与 1–100 个 items", nil)
		return
	}
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(20 * time.Minute))
	_ = rc.SetWriteDeadline(time.Now().Add(20 * time.Minute))
	paths := make([]string, 0, len(in.Items))
	for _, it := range in.Items {
		paths = append(paths, it.Path)
	}
	ictx := r.Context()
	if s.d.LLM != nil {
		var o importOptions
		if in.Options != nil {
			o = *in.Options
		}
		ai, oerr := s.taskAI(ictx, subjectOf(r), o)
		if oerr != nil {
			s.fail(w, r, oerr.status, oerr.code, oerr.msg, nil)
			return
		}
		ictx = imports.WithAI(ictx, ai)
	}
	b, err := s.d.Storage.ImportRemote(ictx, in.ConnectionID, paths, in.TemplateID, key, actorOf(r))
	if err != nil {
		s.stErr(w, r, err)
		return
	}
	s.auditAutoAccepted(r.Context(), r, b.Job, b.Replay)
	_ = s.d.Store.AppendEvent(r.Context(), b.Job.ID, "queued", "task_created", nil, createdSummary(b.Job)+"（来自存储连接）", createdDetails(b.Job))
	s.ok(w, 202, b.Job)
}
