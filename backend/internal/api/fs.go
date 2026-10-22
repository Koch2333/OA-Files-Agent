package api

import (
	"context"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"

	"oa-agent/backend/internal/fileops"
	"oa-agent/backend/internal/fsman"
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
	rel := r.URL.Query().Get("path")
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
	inline := r.URL.Query().Get("download") != "1"
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

func (s *Server) fsMove(w http.ResponseWriter, r *http.Request) {
	var in struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	e, n, err := s.d.Files.Move(r.Context(), in.From, in.To, actorOf(r))
	if err != nil {
		s.fsErr(w, r, err)
		return
	}
	s.ok(w, 200, map[string]any{"entry": e, "ledger_updated": n})
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

func (s *Server) fsRestore(w http.ResponseWriter, r *http.Request) {
	p, n, err := s.d.Files.Restore(r.Context(), r.PathValue("id"), actorOf(r))
	if err != nil {
		s.fsErr(w, r, err)
		return
	}
	s.ok(w, 200, map[string]any{"path": p, "ledger_updated": n})
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
