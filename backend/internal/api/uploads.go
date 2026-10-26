package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"oa-agent/backend/internal/formats"
	"strconv"
	"time"

	"oa-agent/backend/internal/imports"
	"oa-agent/backend/internal/store"
)

// Unbounded imports: resumable chunked uploads, batch commit and local-path import. There is no size or count cap here;
// the only built-in protection is the free-disk-space check (507) and optional operator valves (OA_MAX_UPLOAD_BYTES,
// OA_MAX_BATCH_FILES), enforced inside internal/imports.

const uploadIdle = 2 * time.Minute // a stalled connection (no bytes for this long) is dropped; a slow-but-moving one never is

func (s *Server) uploadRoutes() {
	s.handle("/v1/imports/config", map[string]http.HandlerFunc{"GET": s.importConfig})
	s.handle("/v1/imports/uploads", map[string]http.HandlerFunc{"POST": s.createUpload})
	s.handle("/v1/imports/uploads/{id}", map[string]http.HandlerFunc{"GET": s.getUpload, "PUT": s.putChunk, "DELETE": s.abortUpload})
	s.handle("/v1/imports/uploads/{id}/complete", map[string]http.HandlerFunc{"POST": s.completeUpload})
	s.handle("/v1/imports/batches/{id}", map[string]http.HandlerFunc{"GET": s.getImportBatch, "DELETE": s.discardImportBatch})
	s.handle("/v1/imports/batches/{id}/commit", map[string]http.HandlerFunc{"POST": s.commitImportBatch})
	s.handle("/v1/imports/from-paths", map[string]http.HandlerFunc{"POST": s.importFromPaths})
}

// idleBody extends the read deadline on every read, so a big upload is only cut when the client stops sending.
type idleDeadlineBody struct {
	io.ReadCloser
	rc *http.ResponseController
}

func (b idleDeadlineBody) Read(p []byte) (int, error) {
	_ = b.rc.SetReadDeadline(time.Now().Add(uploadIdle))
	return b.ReadCloser.Read(p)
}

func (s *Server) idleBody(w http.ResponseWriter, body io.ReadCloser) io.ReadCloser {
	return idleDeadlineBody{body, http.NewResponseController(w)}
}

// extendWrite gives the response its own write window (the server-level WriteTimeout started before a long upload).
func (s *Server) extendWrite(w http.ResponseWriter) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(time.Minute))
}

func (s *Server) importErr(w http.ResponseWriter, r *http.Request, err error) {
	var ie *imports.Error
	var mbe *http.MaxBytesError
	var oe *imports.OffsetError
	switch {
	case errors.As(err, &oe):
		w.Header().Set("Upload-Offset", strconv.FormatInt(oe.Current, 10))
		s.fail(w, r, 409, "OFFSET_MISMATCH", "offset 与服务器已接收的位置不一致，请从 details.offset 继续", map[string]any{"offset": oe.Current})
	case errors.As(err, &ie):
		s.fail(w, r, ie.Status, ie.Code, ie.Message, nil)
	case errors.As(err, &mbe):
		s.fail(w, r, 413, "REQUEST_TOO_LARGE", "请求体超过上限", nil)
	case errors.Is(err, store.ErrConflict):
		s.fail(w, r, 409, "IDEMPOTENCY_CONFLICT", "相同 Idempotency-Key 对应了不同的上传内容", nil)
	case errors.Is(err, context.Canceled):
		s.fail(w, r, 499, "CANCELED", "请求已取消", nil)
	default:
		s.internal(w, r, "导入失败", err)
	}
}

func ownerOf(r *http.Request) string { return subjectOf(r).ID }

func nullable[T comparable](v T) any {
	var zero T
	if v == zero {
		return nil
	}
	return v
}

// importConfig tells clients how to upload: chunk size, the (normally absent) operator caps, free space and whether
// local-path import is available to this caller.
func (s *Server) importConfig(w http.ResponseWriter, r *http.Request) {
	lim := s.d.Imports.Limits
	out := map[string]any{
		"chunk_size":          imports.DefaultChunkBytes,
		"max_upload_bytes":    nullable(lim.MaxUploadBytes),
		"max_batch_files":     nullable(lim.MaxBatchFiles),
		"session_ttl_seconds": int64(lim.SessionTTL.Seconds()),
		"disk_reserve_bytes":  lim.DiskReserve,
		"local_path_import":   s.localPathAllowed(r),
		// supported_extensions: formats whose CONTENT can be read. Any other file is still accepted and archived by its
		// name only (accepts_any_extension).
		"supported_extensions":  formats.Extensions(),
		"accepts_any_extension": true,
		"disk_free_bytes":       nil,
	}
	if free, err := lim.Free(s.d.Imports.Root); err == nil {
		out["disk_free_bytes"] = free
	}
	s.ok(w, 200, out)
}

type createUploadBody struct {
	BatchID string `json:"batch_id"`
	Name    string `json:"name"`
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
}

func (s *Server) createUpload(w http.ResponseWriter, r *http.Request) {
	var b createUploadBody
	if !s.decode(w, r, &b) {
		return
	}
	sess, err := s.d.Imports.CreateUpload(ownerOf(r), imports.CreateUploadReq{BatchID: b.BatchID, Name: b.Name, Path: b.Path, Size: b.Size, SHA256: b.SHA256})
	if err != nil {
		s.importErr(w, r, err)
		return
	}
	w.Header().Set("Location", "/v1/imports/uploads/"+sess.ID)
	w.Header().Set("Upload-Offset", "0")
	s.ok(w, 201, sess)
}

func setUploadHeaders(w http.ResponseWriter, sess imports.Session) {
	w.Header().Set("Upload-Offset", strconv.FormatInt(sess.Offset, 10))
	w.Header().Set("Upload-Length", strconv.FormatInt(sess.Size, 10))
	w.Header().Set("Cache-Control", "no-store")
}

func (s *Server) getUpload(w http.ResponseWriter, r *http.Request) {
	sess, err := s.d.Imports.GetUpload(ownerOf(r), r.PathValue("id"))
	if err != nil {
		s.importErr(w, r, err)
		return
	}
	setUploadHeaders(w, sess)
	s.ok(w, 200, sess)
}

func (s *Server) putChunk(w http.ResponseWriter, r *http.Request) {
	offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil || offset < 0 {
		s.fail(w, r, 400, "INVALID_ARGUMENT", "offset 必须是非负整数", nil)
		return
	}
	body := s.idleBody(w, r.Body)
	sess, err := s.d.Imports.AppendChunk(r.Context(), ownerOf(r), r.PathValue("id"), offset, body, r.ContentLength)
	s.extendWrite(w)
	if err != nil {
		if sess.ID != "" {
			setUploadHeaders(w, sess)
		}
		s.importErr(w, r, err)
		return
	}
	setUploadHeaders(w, sess)
	s.ok(w, 200, sess)
}

func (s *Server) completeUpload(w http.ResponseWriter, r *http.Request) {
	// hashing a multi-GB file takes a while: the response gets a generous write window
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(30 * time.Minute))
	sess, err := s.d.Imports.CompleteUpload(r.Context(), ownerOf(r), r.PathValue("id"))
	if err != nil {
		if sess.ID != "" {
			setUploadHeaders(w, sess)
		}
		s.importErr(w, r, err)
		return
	}
	setUploadHeaders(w, sess)
	s.ok(w, 200, sess)
}

func (s *Server) abortUpload(w http.ResponseWriter, r *http.Request) {
	if err := s.d.Imports.AbortUpload(ownerOf(r), r.PathValue("id")); err != nil {
		s.importErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getImportBatch(w http.ResponseWriter, r *http.Request) {
	st, err := s.d.Imports.GetBatch(ownerOf(r), r.PathValue("id"))
	if err != nil {
		s.importErr(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.ok(w, 200, st)
}

func (s *Server) discardImportBatch(w http.ResponseWriter, r *http.Request) {
	if err := s.d.Imports.DiscardBatch(ownerOf(r), r.PathValue("id")); err != nil {
		s.importErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type commitBody struct {
	TemplateID string          `json:"template_id"`
	Options    json.RawMessage `json:"options"`
}

// aiContext resolves the task AI settings for a JSON-driven import (commit / from-paths): the same rules as the multipart
// "options" part.
func (s *Server) aiContext(ctx context.Context, r *http.Request, options json.RawMessage) (context.Context, *optErr) {
	ictx, oerr := s.withImportAI(ctx, r)
	if oerr != nil {
		return ctx, oerr
	}
	if len(options) == 0 || string(options) == "null" {
		return ictx, nil
	}
	if s.d.LLM == nil {
		return ctx, &optErr{400, "INVALID_OPTIONS", "当前未启用识别引擎，不能设置 options"}
	}
	o, pe := parseOptions(options)
	if pe != nil {
		return ctx, pe
	}
	ai, pe := s.taskAI(ctx, subjectOf(r), o)
	if pe != nil {
		return ctx, pe
	}
	return imports.WithAI(ictx, ai), nil
}

func (s *Server) checkTemplate(w http.ResponseWriter, r *http.Request, templateID string) bool {
	if templateID == "" {
		return true
	}
	t, err := s.d.Store.Template(r.Context(), templateID)
	if err != nil {
		s.fail(w, r, 400, "INVALID_TEMPLATE", "指定的 OA 模板不存在", nil)
		return false
	}
	if !t.Enabled {
		s.fail(w, r, 400, "TEMPLATE_DISABLED", "指定的 OA 模板已停用", nil)
		return false
	}
	return true
}

func (s *Server) commitImportBatch(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Idempotency-Key")
	if len(key) > 128 {
		s.fail(w, r, 400, "INVALID_IDEMPOTENCY_KEY", "Idempotency-Key 过长", nil)
		return
	}
	var b commitBody
	if r.ContentLength != 0 {
		if !s.decode(w, r, &b) {
			return
		}
	}
	if !s.checkTemplate(w, r, b.TemplateID) {
		return
	}
	ictx, oerr := s.aiContext(r.Context(), r, b.Options)
	if oerr != nil {
		s.fail(w, r, oerr.status, oerr.code, oerr.msg, nil)
		return
	}
	batch, err := s.d.Imports.CommitStaged(ictx, ownerOf(r), r.PathValue("id"), key, b.TemplateID)
	if err != nil {
		s.importErr(w, r, err)
		return
	}
	s.afterImport(r.Context(), r, batch, "import.create")
	s.ok(w, 202, batch.Job)
}

// localPathAllowed: local single-user mode AND the request comes from this very machine (never behind a proxy).
func (s *Server) localPathAllowed(r *http.Request) bool {
	if s.d.Auth == nil || !s.fromLoopback(r) {
		return false
	}
	set, err := s.d.Auth.Settings(r.Context())
	return err == nil && set.Mode == "local"
}

type fromPathsBody struct {
	BatchID       string          `json:"batch_id"`
	Paths         []string        `json:"paths"`
	IncludeHidden bool            `json:"include_hidden"`
	Commit        *bool           `json:"commit"`
	TemplateID    string          `json:"template_id"`
	Options       json.RawMessage `json:"options"`
}

func (s *Server) importFromPaths(w http.ResponseWriter, r *http.Request) {
	if !s.localPathAllowed(r) {
		s.fail(w, r, 403, "LOCAL_ONLY", "本机路径导入仅在本机（local）模式下、由本机发起的请求可用；请改用文件上传", nil)
		return
	}
	var b fromPathsBody
	if !s.decode(w, r, &b) {
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if len(key) > 128 {
		s.fail(w, r, 400, "INVALID_IDEMPOTENCY_KEY", "Idempotency-Key 过长", nil)
		return
	}
	commit := b.Commit == nil || *b.Commit
	if !s.checkTemplate(w, r, b.TemplateID) {
		return
	}
	ictx, oerr := s.aiContext(r.Context(), r, b.Options)
	if oerr != nil {
		s.fail(w, r, oerr.status, oerr.code, oerr.msg, nil)
		return
	}
	owner, actor := ownerOf(r), actorOf(r)
	detached := context.WithoutCancel(ictx)
	var onDone func(*imports.PathImport)
	if commit {
		onDone = func(p *imports.PathImport) {
			batch, err := s.d.Imports.CommitStaged(detached, owner, p.Status().BatchID, key, b.TemplateID)
			if err != nil {
				p.SetTask("", err)
				return
			}
			s.d.Store.Audit(detached, actor, "import.from_paths", batch.Job.ID, "本机路径导入："+strconv.Itoa(batch.Job.TotalFiles)+" 个文件（"+strconv.Itoa(batch.Job.FailureCount)+" 个被拒绝）")
			s.auditAutoAcceptedBy(detached, actor, batch.Job, batch.Replay)
			_ = s.d.Store.AppendEvent(detached, batch.Job.ID, "queued", "task_created", nil, createdSummary(batch.Job), createdDetails(batch.Job))
			p.SetTask(batch.Job.ID, nil)
		}
	}
	p, err := s.d.Imports.StartPathImport(owner, b.BatchID, b.Paths, imports.PathOptions{IncludeHidden: b.IncludeHidden}, onDone)
	if err != nil {
		s.importErr(w, r, err)
		return
	}
	s.d.Store.Audit(r.Context(), actor, "import.from_paths.start", p.Status().BatchID, "开始本机路径导入："+strconv.Itoa(len(b.Paths))+" 个路径")
	s.ok(w, 202, p.Status())
}
