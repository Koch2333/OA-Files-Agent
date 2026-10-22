package api

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"

	"oa-agent/backend/internal/fsman"
	"oa-agent/backend/internal/preview"
)

// Previewer renders in-app previews of originals (implemented by preview.Service).
type Previewer interface {
	File(ctx context.Context, id string) (preview.Preview, error)
	FilePage(ctx context.Context, id string, page, maxSide int) ([]byte, error)
	Archived(ctx context.Context, rel string) (preview.Preview, error)
	ArchivedPage(ctx context.Context, rel string, page, maxSide int) ([]byte, error)
}

func (s *Server) previewRoutes() {
	s.handle("/v1/files/{id}/preview", map[string]http.HandlerFunc{"GET": s.previewFile})
	s.handle("/v1/files/{id}/preview/page", map[string]http.HandlerFunc{"GET": s.previewFilePage})
	s.handle("/v1/fs/preview", map[string]http.HandlerFunc{"GET": s.previewArchived})
	s.handle("/v1/fs/preview/page", map[string]http.HandlerFunc{"GET": s.previewArchivedPage})
}

func (s *Server) previewErr(w http.ResponseWriter, r *http.Request, err error) {
	var we *preview.WorkerError
	switch {
	case errors.Is(err, preview.ErrNotFound), errors.Is(err, fsman.ErrNotFound):
		s.fail(w, r, 404, "NOT_FOUND", "文件不存在或原件副本已被清理", nil)
	case errors.Is(err, fsman.ErrInvalid), errors.Is(err, fsman.ErrOutside):
		s.fail(w, r, 400, "INVALID_PATH", "路径无效", nil)
	case errors.Is(err, preview.ErrBadPage):
		s.fail(w, r, 400, "INVALID_PAGE", "页码超出范围", nil)
	case errors.As(err, &we):
		switch we.Code {
		case "ENCRYPTED":
			s.fail(w, r, 422, "ENCRYPTED", "文件已加密，无法预览", nil)
		case "UNREADABLE_FILE":
			s.fail(w, r, 422, "UNREADABLE_FILE", "文件损坏或无法解析，无法预览", nil)
		case "UNSUPPORTED_FORMAT":
			s.fail(w, r, 422, "UNSUPPORTED_FORMAT", "该格式暂不支持预览", nil)
		case "FILE_NOT_FOUND":
			s.fail(w, r, 404, "NOT_FOUND", "文件不存在", nil)
		case "PATH_NOT_ALLOWED":
			log.Printf("preview: worker refused the path (request %s): add the archive root to OA_ALLOWED_ROOTS for the worker", requestID(r))
			s.fail(w, r, 503, "PREVIEW_UNAVAILABLE", "预览服务未被允许读取该目录，请检查 Worker 的 OA_ALLOWED_ROOTS 配置", nil)
		default:
			log.Printf("preview: worker error %s/%d (request %s)", we.Code, we.Status, requestID(r))
			s.fail(w, r, 503, "PREVIEW_UNAVAILABLE", "预览服务暂不可用", nil)
		}
	default:
		s.internal(w, r, "内部错误", err)
	}
}

func pageArgs(r *http.Request) (page, maxSide int, ok bool) {
	page, maxSide = 1, 1400
	q := r.URL.Query()
	if v := q.Get("page"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100000 {
			return 0, 0, false
		}
		page = n
	}
	if v := q.Get("max_side"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 256 || n > 2000 {
			return 0, 0, false
		}
		maxSide = n
	}
	return page, maxSide, true
}

func (s *Server) writePNG(w http.ResponseWriter, png []byte) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(png)))
	_, _ = w.Write(png)
}

func (s *Server) previewFile(w http.ResponseWriter, r *http.Request) {
	p, err := s.d.Preview.File(r.Context(), r.PathValue("id"))
	if err != nil {
		s.previewErr(w, r, err)
		return
	}
	s.ok(w, 200, p)
}

func (s *Server) previewFilePage(w http.ResponseWriter, r *http.Request) {
	page, side, ok := pageArgs(r)
	if !ok {
		s.fail(w, r, 400, "INVALID_ARGUMENT", "page/max_side 无效", nil)
		return
	}
	png, err := s.d.Preview.FilePage(r.Context(), r.PathValue("id"), page, side)
	if err != nil {
		s.previewErr(w, r, err)
		return
	}
	s.writePNG(w, png)
}

func (s *Server) previewArchived(w http.ResponseWriter, r *http.Request) {
	p, err := s.d.Preview.Archived(r.Context(), r.URL.Query().Get("path"))
	if err != nil {
		s.previewErr(w, r, err)
		return
	}
	s.ok(w, 200, p)
}

func (s *Server) previewArchivedPage(w http.ResponseWriter, r *http.Request) {
	page, side, ok := pageArgs(r)
	if !ok {
		s.fail(w, r, 400, "INVALID_ARGUMENT", "page/max_side 无效", nil)
		return
	}
	png, err := s.d.Preview.ArchivedPage(r.Context(), r.URL.Query().Get("path"), page, side)
	if err != nil {
		s.previewErr(w, r, err)
		return
	}
	s.writePNG(w, png)
}
