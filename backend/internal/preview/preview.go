// Package preview serves in-app previews of original documents: PDF/images as page images, Word/Excel as
// structured content produced by the worker (rendered by the UI without HTML injection).
package preview

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"oa-agent/backend/internal/fsman"
	"oa-agent/backend/internal/store"
)

var (
	ErrNotFound = errors.New("not found")
	ErrBadPage  = errors.New("page out of range")
)

// WorkerError carries the worker's error code for the API to map (e.g. ENCRYPTED, UNREADABLE_FILE).
type WorkerError struct {
	Code   string
	Status int
}

func (e *WorkerError) Error() string { return "worker: " + e.Code }

type Preview struct {
	Kind      string          `json:"kind"`
	Name      string          `json:"name"`
	MIMEType  string          `json:"mime_type"`
	SizeBytes int64           `json:"size_bytes"`
	Pages     *int            `json:"pages"`
	PageURL   *string         `json:"page_url"`
	Message   string          `json:"message"`
	Truncated bool            `json:"truncated"`
	Warnings  []string        `json:"warnings"`
	Header    string          `json:"header,omitempty"`
	Footer    string          `json:"footer,omitempty"`
	Blocks    json.RawMessage `json:"blocks,omitempty"`
	Sheets    json.RawMessage `json:"sheets,omitempty"`
}

type Service struct {
	Store  *store.Store
	FS     *fsman.Manager
	Worker string // loopback worker base URL (already validated by the caller)
	Token  string
	HTTP   *http.Client
}

const (
	maxPreviewJSON = 6 << 20
	maxPageB64     = 4 << 20
)

func (s *Service) client() *http.Client {
	if s.HTTP != nil {
		return s.HTTP
	}
	return &http.Client{Timeout: 90 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{Proxy: nil}}
}

// call posts JSON to the worker and returns the "data" object, mapping worker errors.
func (s *Service) call(ctx context.Context, path string, body any) (json.RawMessage, error) {
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.Worker, "/")+path, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.Token)
	res, err := s.client().Do(req)
	if err != nil {
		return nil, &WorkerError{Code: "WORKER_UNAVAILABLE", Status: 503}
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxPreviewJSON+1))
	if err != nil || len(raw) > maxPreviewJSON {
		return nil, &WorkerError{Code: "WORKER_BAD_RESPONSE", Status: 502}
	}
	var env struct {
		Data  json.RawMessage `json:"data"`
		Error *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &env) != nil {
		return nil, &WorkerError{Code: "WORKER_BAD_RESPONSE", Status: 502}
	}
	if res.StatusCode != 200 || env.Error != nil {
		code := "WORKER_ERROR"
		if env.Error != nil && env.Error.Code != "" {
			code = env.Error.Code
		}
		return nil, &WorkerError{Code: code, Status: res.StatusCode}
	}
	return env.Data, nil
}

type target struct {
	abs, name, mime string
	size            int64
	pageURL         string
}

func (s *Service) build(ctx context.Context, t target) (Preview, error) {
	if fi, err := os.Stat(t.abs); err != nil || fi.IsDir() {
		return Preview{}, ErrNotFound
	}
	data, err := s.call(ctx, "/v1/preview", map[string]any{"path": t.abs, "max_rows": 200, "max_blocks": 600})
	p := Preview{Name: t.name, MIMEType: t.mime, SizeBytes: t.size, Warnings: []string{}}
	var we *WorkerError
	if errors.As(err, &we) && we.Code == "UNSUPPORTED_FORMAT" {
		p.Kind = "unsupported"
		p.Message = "该格式暂不支持预览；可下载后查看（文件仍可按文件名归档）。"
		return p, nil
	}
	if err != nil {
		return Preview{}, err
	}
	var w struct {
		Kind      string          `json:"kind"`
		Pages     *int            `json:"pages"`
		Truncated bool            `json:"truncated"`
		Warnings  []string        `json:"warnings"`
		Header    string          `json:"header"`
		Footer    string          `json:"footer"`
		Blocks    json.RawMessage `json:"blocks"`
		Sheets    json.RawMessage `json:"sheets"`
	}
	if json.Unmarshal(data, &w) != nil {
		return Preview{}, &WorkerError{Code: "WORKER_BAD_RESPONSE", Status: 502}
	}
	switch w.Kind {
	case "pdf", "image", "document", "spreadsheet":
	default:
		return Preview{}, &WorkerError{Code: "WORKER_BAD_RESPONSE", Status: 502}
	}
	p.Kind, p.Pages, p.Truncated, p.Header, p.Footer, p.Blocks, p.Sheets = w.Kind, w.Pages, w.Truncated, w.Header, w.Footer, w.Blocks, w.Sheets
	if w.Warnings != nil {
		p.Warnings = w.Warnings
	}
	if (w.Kind == "pdf" || w.Kind == "image") && w.Pages != nil {
		u := t.pageURL
		p.PageURL = &u
	}
	return p, nil
}

func (s *Service) page(ctx context.Context, abs string, page, maxSide int) ([]byte, error) {
	if page < 1 {
		page = 1
	}
	if maxSide < 256 || maxSide > 2000 {
		maxSide = 1400
	}
	data, err := s.call(ctx, "/v1/render", map[string]any{"path": abs, "page": page, "max_side": maxSide})
	var we *WorkerError
	if errors.As(err, &we) && we.Code == "INVALID_REQUEST" {
		return nil, ErrBadPage
	}
	if err != nil {
		return nil, err
	}
	var r struct {
		Image string `json:"image_base64"`
		MIME  string `json:"mime_type"`
	}
	if json.Unmarshal(data, &r) != nil || r.MIME != "image/png" || len(r.Image) == 0 || len(r.Image) > maxPageB64 {
		return nil, &WorkerError{Code: "WORKER_BAD_RESPONSE", Status: 502}
	}
	png, err := base64.StdEncoding.DecodeString(r.Image)
	if err != nil || len(png) < 8 || string(png[1:4]) != "PNG" {
		return nil, &WorkerError{Code: "WORKER_BAD_RESPONSE", Status: 502}
	}
	return png, nil
}

func (s *Service) fileTarget(ctx context.Context, id string) (target, error) {
	f, err := s.Store.GetFile(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return target{}, ErrNotFound
		}
		return target{}, err
	}
	if f.StoredPath == "" {
		return target{}, ErrNotFound
	}
	return target{abs: f.StoredPath, name: f.SourceName, mime: f.MIMEType, size: f.SizeBytes, pageURL: "/v1/files/" + f.ID + "/preview/page?page={page}"}, nil
}

func (s *Service) archivedTarget(rel string) (target, error) {
	e, err := s.FS.Stat(rel)
	if err != nil {
		return target{}, err
	}
	if e.IsDir {
		return target{}, fmt.Errorf("%w: not a file", fsman.ErrInvalid)
	}
	abs, err := s.FS.Abs(rel)
	if err != nil {
		return target{}, err
	}
	return target{abs: abs, name: e.Name, mime: e.MIMEType, size: e.SizeBytes, pageURL: "/v1/fs/preview/page?path=" + url.QueryEscape(rel) + "&page={page}"}, nil
}

func (s *Service) File(ctx context.Context, id string) (Preview, error) {
	t, err := s.fileTarget(ctx, id)
	if err != nil {
		return Preview{}, err
	}
	return s.build(ctx, t)
}

func (s *Service) FilePage(ctx context.Context, id string, page, maxSide int) ([]byte, error) {
	t, err := s.fileTarget(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.page(ctx, t.abs, page, maxSide)
}

func (s *Service) Archived(ctx context.Context, rel string) (Preview, error) {
	t, err := s.archivedTarget(rel)
	if err != nil {
		return Preview{}, err
	}
	return s.build(ctx, t)
}

func (s *Service) ArchivedPage(ctx context.Context, rel string, page, maxSide int) ([]byte, error) {
	t, err := s.archivedTarget(rel)
	if err != nil {
		return nil, err
	}
	return s.page(ctx, t.abs, page, maxSide)
}
