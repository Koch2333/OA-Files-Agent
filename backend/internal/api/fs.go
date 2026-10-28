package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"oa-agent/backend/internal/fileops"
	"oa-agent/backend/internal/fsman"
	"oa-agent/backend/internal/store"
)

// FileManager is the file-manager backend (implemented by fileops.Service).
type FileManager interface {
	List(ctx context.Context, rel string) (fileops.Listing, error)
	Content(rel string) (*os.File, os.FileInfo, error)
	Mkdir(ctx context.Context, rel, actor string) (fileops.ListedEntry, error)
	Move(ctx context.Context, from, to, actor string) (fileops.ListedEntry, int64, error)
	Trash(ctx context.Context, rel, actor string) (fsman.TrashItem, int64, error)
	ListTrash() ([]fsman.TrashItem, error)
	Restore(ctx context.Context, id, actor string) (string, int64, error)
	Reveal(ctx context.Context, rel, actor string) (string, error)
	Entry(ctx context.Context, rel string) (fileops.ListedEntry, error)
	MoveBatch(ctx context.Context, req fileops.MoveRequest, actor string) (fileops.BatchResult, error)
	BulkTrash(ctx context.Context, paths []string, actor string) (fileops.BatchResult, error)
	RestoreWith(ctx context.Context, id string, pol fileops.Policy, actor string) (fileops.RestoreResult, error)
	Verify(ctx context.Context, paths []string, actor string) (fileops.VerifyReport, error)
	PlanDownload(paths []string, now time.Time) (fileops.DownloadPlan, error)
	WriteZip(ctx context.Context, w io.Writer, plan fileops.DownloadPlan, actor string) (fileops.ZipReport, error)
	Reclassify(ctx context.Context, id string, rc fileops.Reclass, derive fileops.Derive, actor string) (store.LedgerEntry, error)
	Root() string
	CanReveal() bool
}

func (s *Server) fsRoutes() {
	s.handle("/v1/fs", map[string]http.HandlerFunc{"GET": s.fsList})
	s.handle("/v1/fs/content", map[string]http.HandlerFunc{"GET": s.fsContent})
	s.handle("/v1/fs/folders", map[string]http.HandlerFunc{"POST": s.fsMkdir})
	s.handle("/v1/fs/move", map[string]http.HandlerFunc{"POST": s.fsMove})
	s.handle("/v1/fs/trash", map[string]http.HandlerFunc{"GET": s.fsTrashList, "POST": s.fsTrash})
	s.handle("/v1/fs/trash/{id}/restore", map[string]http.HandlerFunc{"POST": s.fsRestore})
	s.handle("/v1/fs/reveal", map[string]http.HandlerFunc{"POST": s.fsReveal})
	s.handle("/v1/fs/download", map[string]http.HandlerFunc{"GET": s.fsDownload, "POST": s.fsDownload})
	s.handle("/v1/fs/verify", map[string]http.HandlerFunc{"POST": s.fsVerify})
	s.handle("/v1/fs/bulk/move", map[string]http.HandlerFunc{"POST": s.fsBulkMove})
	s.handle("/v1/fs/bulk/trash", map[string]http.HandlerFunc{"POST": s.fsBulkTrash})
}

func (s *Server) fsErr(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, fsman.ErrInvalid), errors.Is(err, fsman.ErrOutside):
		msg := strings.TrimPrefix(err.Error(), "invalid path: ")
		s.fail(w, r, 400, "INVALID_PATH", "路径无效："+msg, nil)
	case errors.Is(err, fsman.ErrNotFound):
		s.fail(w, r, 404, "NOT_FOUND", "文件或文件夹不存在", nil)
	case errors.Is(err, fsman.ErrExists):
		s.fail(w, r, 409, "ALREADY_EXISTS", "目标已存在，未做任何改动（不会覆盖）", nil)
	case errors.Is(err, fsman.ErrConflict):
		s.fail(w, r, 409, "CONFLICT", "存在冲突，请重试", nil)
	case errors.Is(err, fileops.ErrRevealUnsupported):
		s.fail(w, r, 501, "NOT_SUPPORTED", "“在资源管理器中显示”仅在 Windows 本机可用", nil)
	case errors.Is(err, fileops.ErrRevealDisabled):
		s.fail(w, r, 403, "REVEAL_DISABLED", "已通过配置关闭“在资源管理器中显示”", nil)
	default:
		s.internal(w, r, "内部错误", err)
	}
}

func (s *Server) fsList(w http.ResponseWriter, r *http.Request) {
	l, err := s.d.Files.List(r.Context(), r.URL.Query().Get("path"))
	if err != nil {
		s.fsErr(w, r, err)
		return
	}
	local := isLoopbackRequest(r)
	var root *string
	if local {
		rt := s.d.Files.Root()
		root = &rt
	}
	s.ok(w, 200, map[string]any{
		"path": l.Path, "parent": l.Parent, "entries": l.Entries,
		"capabilities": map[string]any{"reveal": local && s.d.Files.CanReveal(), "root": root},
	})
}

func (s *Server) fsContent(w http.ResponseWriter, r *http.Request) {
	s.serveFile(w, r, r.URL.Query().Get("path"), r.URL.Query().Get("download") == "1")
}

func (s *Server) serveFile(w http.ResponseWriter, r *http.Request, rel string, forceDownload bool) {
	f, fi, err := s.d.Files.Content(rel)
	if err != nil {
		s.fsErr(w, r, err)
		return
	}
	defer f.Close()
	name := path.Base(rel)
	ctype := mime.TypeByExtension(strings.ToLower(path.Ext(name)))
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	inline := !forceDownload
	switch strings.ToLower(path.Ext(name)) {
	case ".pdf", ".png", ".jpg", ".jpeg":
	default:
		inline = false
		ctype = "application/octet-stream"
	}
	disp := "attachment"
	if inline {
		disp = "inline"
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Disposition", disp+"; filename=\"download\"; filename*=UTF-8''"+url.PathEscape(name))
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, name, fi.ModTime(), f)
}

func (s *Server) fsMkdir(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Path string `json:"path"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	e, err := s.d.Files.Mkdir(r.Context(), in.Path, actorOf(r))
	if err != nil {
		s.fsErr(w, r, err)
		return
	}
	s.ok(w, 201, e)
}

func (s *Server) fsTrash(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Path string `json:"path"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	it, _, err := s.d.Files.Trash(r.Context(), in.Path, actorOf(r))
	if err != nil {
		s.fsErr(w, r, err)
		return
	}
	s.ok(w, 201, it)
}

func (s *Server) fsTrashList(w http.ResponseWriter, r *http.Request) {
	items, err := s.d.Files.ListTrash()
	if err != nil {
		s.fsErr(w, r, err)
		return
	}
	s.ok(w, 200, items)
}

func (s *Server) fsReveal(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Path string `json:"path"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if !isLoopbackRequest(r) {
		s.fail(w, r, 403, "LOCAL_ONLY", "只有本机请求可以在资源管理器中显示文件", nil)
		return
	}
	shown, err := s.d.Files.Reveal(r.Context(), in.Path, actorOf(r))
	if err != nil {
		s.fsErr(w, r, err)
		return
	}
	s.ok(w, 200, map[string]string{"revealed": shown})
}

// ---- conflict-aware move, bulk operations, restore, download, verify ----

const maxFsPaths = 5000

func (s *Server) conflictsFail(w http.ResponseWriter, r *http.Request, code string, cs []fileops.Conflict) {
	s.fail(w, r, 409, code, fmt.Sprintf("发现 %d 处冲突，未做任何改动。请选择处理方式：跳过、保留两者，或替换（旧文件进入回收站）", len(cs)), map[string]any{"conflicts": cs})
}

// itemErr maps a failed single item to the error the old single-item endpoints returned.
func (s *Server) itemErr(w http.ResponseWriter, r *http.Request, it fileops.ItemResult) {
	switch it.Code {
	case "INVALID_PATH":
		s.fail(w, r, 400, "INVALID_PATH", "路径无效："+it.Message, nil)
	case "NOT_FOUND":
		s.fail(w, r, 404, "NOT_FOUND", it.Message, nil)
	case "ALREADY_EXISTS", "TYPE_CONFLICT", "BATCH_DUPLICATE":
		s.fail(w, r, 409, it.Code, it.Message, nil)
	case "CANCELLED":
		s.fail(w, r, 499, "CANCELED", "请求已取消", nil)
	default:
		s.internal(w, r, "内部错误", errors.New(it.Code+": "+it.Message))
	}
}

func (s *Server) policyOf(w http.ResponseWriter, r *http.Request, v string) (fileops.Policy, bool) {
	p, ok := fileops.ParsePolicy(v, fileops.PolicyAsk)
	if !ok {
		s.fail(w, r, 400, "INVALID_REQUEST", "on_conflict 只能是 ask、skip、rename 或 replace", nil)
	}
	return p, ok
}

func (s *Server) resolutionsOf(w http.ResponseWriter, r *http.Request, in map[string]string) (map[string]fileops.Policy, bool) {
	out := map[string]fileops.Policy{}
	for k, v := range in {
		p, ok := fileops.ParsePolicy(v, "")
		if !ok || p == "" {
			s.fail(w, r, 400, "INVALID_REQUEST", "resolutions 的值只能是 skip、rename、replace 或 ask", nil)
			return nil, false
		}
		out[k] = p
	}
	return out, true
}

func (s *Server) fsMove(w http.ResponseWriter, r *http.Request) {
	var in struct {
		From        string            `json:"from"`
		To          string            `json:"to"`
		OnConflict  string            `json:"on_conflict"`
		Resolutions map[string]string `json:"resolutions"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	pol, ok := s.policyOf(w, r, in.OnConflict)
	if !ok {
		return
	}
	res, ok := s.resolutionsOf(w, r, in.Resolutions)
	if !ok {
		return
	}
	out, err := s.d.Files.MoveBatch(r.Context(), fileops.MoveRequest{Items: []fileops.MoveSpec{{From: in.From, To: in.To}}, OnConflict: pol, Resolutions: res}, actorOf(r))
	if err != nil {
		s.fsErr(w, r, err)
		return
	}
	if len(out.Conflicts) > 0 {
		s.conflictsFail(w, r, "ALREADY_EXISTS", out.Conflicts)
		return
	}
	it := out.Items[0]
	if it.Status == fileops.StFailed {
		s.itemErr(w, r, it)
		return
	}
	body := map[string]any{"ledger_updated": it.LedgerUpdated, "status": it.Status, "final_path": it.FinalPath, "hash_status": it.HashStatus,
		"modified_outside": it.ModifiedOutside, "duplicate_of": it.DuplicateOf, "message": it.Message}
	if it.FinalPath != "" && (it.Status == fileops.StMoved || it.Status == fileops.StRenamed || it.Status == fileops.StReplaced) {
		if e, eerr := s.d.Files.Entry(r.Context(), it.FinalPath); eerr == nil {
			body["entry"] = e
		}
	}
	s.ok(w, 200, body)
}

func (s *Server) fsBulkMove(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Paths       []string          `json:"paths"`
		TargetDir   string            `json:"target_dir"`
		OnConflict  string            `json:"on_conflict"`
		Resolutions map[string]string `json:"resolutions"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if len(in.Paths) == 0 || len(in.Paths) > maxFsPaths {
		s.fail(w, r, 400, "INVALID_REQUEST", fmt.Sprintf("paths 需要 1 到 %d 项", maxFsPaths), nil)
		return
	}
	if err := fsman.Check(in.TargetDir, true); err != nil {
		s.fsErr(w, r, err)
		return
	}
	pol, ok := s.policyOf(w, r, in.OnConflict)
	if !ok {
		return
	}
	res, ok := s.resolutionsOf(w, r, in.Resolutions)
	if !ok {
		return
	}
	specs := make([]fileops.MoveSpec, 0, len(in.Paths))
	for _, p := range in.Paths {
		if err := fsman.Check(p, false); err != nil {
			s.fsErr(w, r, err)
			return
		}
		to := path.Base(p)
		if in.TargetDir != "" {
			to = in.TargetDir + "/" + to
		}
		specs = append(specs, fileops.MoveSpec{From: p, To: to})
	}
	out, err := s.d.Files.MoveBatch(r.Context(), fileops.MoveRequest{Items: specs, OnConflict: pol, Resolutions: res}, actorOf(r))
	if err != nil {
		s.fsErr(w, r, err)
		return
	}
	if len(out.Conflicts) > 0 {
		s.conflictsFail(w, r, "NAME_CONFLICT", out.Conflicts)
		return
	}
	s.ok(w, 200, out)
}

func (s *Server) fsBulkTrash(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Paths []string `json:"paths"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if len(in.Paths) == 0 || len(in.Paths) > maxFsPaths {
		s.fail(w, r, 400, "INVALID_REQUEST", fmt.Sprintf("paths 需要 1 到 %d 项", maxFsPaths), nil)
		return
	}
	out, err := s.d.Files.BulkTrash(r.Context(), in.Paths, actorOf(r))
	if err != nil {
		s.fsErr(w, r, err)
		return
	}
	s.ok(w, 200, out)
}

func (s *Server) fsRestore(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in struct {
		OnConflict string `json:"on_conflict"`
	}
	if r.ContentLength != 0 && r.Body != nil && r.Body != http.NoBody {
		if !s.decode(w, r, &in) {
			return
		}
	}
	if in.OnConflict == "" { // legacy call: restore to the original place, numbered suffix when it is taken
		p, n, err := s.d.Files.Restore(r.Context(), id, actorOf(r))
		if err != nil {
			s.fsErr(w, r, err)
			return
		}
		s.ok(w, 200, map[string]any{"path": p, "ledger_updated": n, "status": "restored", "hash_status": fileops.HashNotTask})
		return
	}
	pol, ok := s.policyOf(w, r, in.OnConflict)
	if !ok {
		return
	}
	res, err := s.d.Files.RestoreWith(r.Context(), id, pol, actorOf(r))
	if err != nil {
		s.fsErr(w, r, err)
		return
	}
	if len(res.Conflicts) > 0 {
		s.conflictsFail(w, r, "ALREADY_EXISTS", res.Conflicts)
		return
	}
	s.ok(w, 200, res)
}

func (s *Server) fsVerify(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Paths []string `json:"paths"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if len(in.Paths) == 0 || len(in.Paths) > maxFsPaths {
		s.fail(w, r, 400, "INVALID_REQUEST", fmt.Sprintf("paths 需要 1 到 %d 项", maxFsPaths), nil)
		return
	}
	rep, err := s.d.Files.Verify(r.Context(), in.Paths, actorOf(r))
	if err != nil {
		if errors.Is(err, context.Canceled) {
			s.fail(w, r, 499, "CANCELED", "请求已取消", nil)
			return
		}
		s.fsErr(w, r, err)
		return
	}
	s.ok(w, 200, rep)
}

// attrChar reports whether b may appear unescaped in an RFC 5987 value.
func attrChar(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	}
	return strings.IndexByte("!#$&+-.^_`|~", b) >= 0
}

// contentDisposition builds `attachment` with an ASCII fallback and the RFC 5987 UTF-8 name.
func contentDisposition(name string) string {
	var enc, ascii strings.Builder
	for _, r := range name {
		if r < 0x80 && r >= 0x20 && r != '"' && r != '\\' && r != '%' {
			ascii.WriteRune(r)
		} else {
			ascii.WriteByte('_')
		}
	}
	for _, b := range []byte(name) {
		if attrChar(b) {
			enc.WriteByte(b)
		} else {
			fmt.Fprintf(&enc, "%%%02X", b)
		}
	}
	return `attachment; filename="` + ascii.String() + `"; filename*=UTF-8''` + enc.String()
}

// deadlineWriter pushes the write deadline forward while data keeps flowing, so a long ZIP is not cut by the
// server-level WriteTimeout but a stalled client still is.
type deadlineWriter struct {
	w    io.Writer
	rc   *http.ResponseController
	last time.Time
}

func (d *deadlineWriter) Write(p []byte) (int, error) {
	if now := time.Now(); now.Sub(d.last) > time.Second {
		_ = d.rc.SetWriteDeadline(now.Add(time.Minute))
		d.last = now
	}
	return d.w.Write(p)
}

// fsDownload serves one file as is, and a folder or several items as a streamed ZIP (no temp file, no size limit).
func (s *Server) fsDownload(w http.ResponseWriter, r *http.Request) {
	var paths []string
	if r.Method == http.MethodPost {
		var in struct {
			Paths []string `json:"paths"`
		}
		if !s.decode(w, r, &in) {
			return
		}
		paths = in.Paths
	} else {
		paths = r.URL.Query()["path"]
	}
	if len(paths) == 0 || len(paths) > maxFsPaths {
		s.fail(w, r, 400, "INVALID_REQUEST", fmt.Sprintf("需要 1 到 %d 个路径", maxFsPaths), nil)
		return
	}
	plan, err := s.d.Files.PlanDownload(paths, time.Now())
	if err != nil {
		s.fsErr(w, r, err)
		return
	}
	if plan.SingleFile {
		s.serveFile(w, r, plan.Roots[0].Path, true)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", contentDisposition(plan.Name))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	dw := &deadlineWriter{w: w, rc: http.NewResponseController(w)}
	if _, err := s.d.Files.WriteZip(r.Context(), dw, plan, actorOf(r)); err != nil {
		// the status line is gone; the unfinished archive (no central directory) tells the client it is incomplete
		if !errors.Is(err, context.Canceled) {
			log.Printf("fs download aborted: %v", err)
		}
	}
}

// reclassify corrects department/type/date of an archived file afterwards (fixes the placeholders the automatic mode leaves).
func (s *Server) reclassify(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Department   *string `json:"department"`
		DocumentType *string `json:"document_type"`
		DocumentDate *string `json:"document_date"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if in.Department == nil && in.DocumentType == nil && in.DocumentDate == nil {
		s.fail(w, r, 400, "INVALID_ARGUMENT", "至少修改部门、类型、日期中的一项", nil)
		return
	}
	e, err := s.d.Files.Reclassify(r.Context(), r.PathValue("id"), fileops.Reclass{Department: in.Department, DocumentType: in.DocumentType, DocumentDate: in.DocumentDate},
		func(ctx context.Context, it store.PlanItem) (string, string, []string, error) {
			return s.d.Backend.Derive(ctx, it)
		}, actorOf(r))
	switch {
	case err == nil:
		s.ok(w, 200, e)
	case errors.Is(err, fileops.ErrReclassInvalid):
		s.fail(w, r, 422, "VALIDATION_FAILED", strings.TrimPrefix(err.Error(), "invalid correction: "), nil)
	case errors.Is(err, store.ErrNotFound):
		s.fail(w, r, 404, "NOT_FOUND", "台账记录不存在", nil)
	default:
		s.fsErr(w, r, err)
	}
}
