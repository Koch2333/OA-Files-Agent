package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"oa-agent/backend/internal/fsman"
	"oa-agent/backend/internal/preview"
)

type fakePreview struct {
	err      error
	png      []byte
	lastPage int
	lastSide int
	lastPath string
}

func (f *fakePreview) File(_ context.Context, id string) (preview.Preview, error) {
	return preview.Preview{Kind: "pdf", Name: id, Warnings: []string{}}, f.err
}
func (f *fakePreview) FilePage(_ context.Context, id string, page, side int) ([]byte, error) {
	f.lastPage, f.lastSide = page, side
	return f.png, f.err
}
func (f *fakePreview) Archived(_ context.Context, rel string) (preview.Preview, error) {
	f.lastPath = rel
	return preview.Preview{Kind: "document", Name: rel, Warnings: []string{}}, f.err
}
func (f *fakePreview) ArchivedPage(_ context.Context, rel string, page, side int) ([]byte, error) {
	f.lastPath, f.lastPage, f.lastSide = rel, page, side
	return f.png, f.err
}

func previewServer(f *fakePreview) http.Handler { return NewServer(Deps{Preview: f}) }

func TestPreviewPageResponsesAreNoStoreImages(t *testing.T) {
	f := &fakePreview{png: []byte("\x89PNG\r\n\x1a\nxxxx")}
	h := previewServer(f)
	for _, target := range []string{"/v1/files/11111111-1111-4111-8111-111111111111/preview/page?page=3&max_side=900", "/v1/fs/preview/page?path=a/b.pdf&page=3&max_side=900"} {
		w := do(h, "GET", target, "", nil, "127.0.0.1:1")
		if w.Code != 200 || w.Header().Get("Content-Type") != "image/png" || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" || !strings.HasPrefix(w.Body.String(), "\x89PNG") {
			t.Fatalf("%s: %d %v", target, w.Code, w.Header())
		}
		if f.lastPage != 3 || f.lastSide != 900 {
			t.Fatalf("args forwarded: %d %d", f.lastPage, f.lastSide)
		}
	}
	// defaults
	do(h, "GET", "/v1/fs/preview/page?path=a.pdf", "", nil, "127.0.0.1:1")
	if f.lastPage != 1 || f.lastSide != 1400 {
		t.Fatalf("defaults: %d %d", f.lastPage, f.lastSide)
	}
	for _, q := range []string{"page=0", "page=-1", "page=abc", "page=1000001", "max_side=100", "max_side=5000", "max_side=x"} {
		if w := do(h, "GET", "/v1/fs/preview/page?path=a.pdf&"+q, "", nil, "127.0.0.1:1"); w.Code != 400 || !strings.Contains(w.Body.String(), "INVALID_ARGUMENT") {
			t.Errorf("%s → %d %s", q, w.Code, w.Body)
		}
	}
}

func TestPreviewJSONEndpointsAndPathHandling(t *testing.T) {
	f := &fakePreview{}
	h := previewServer(f)
	w := do(h, "GET", "/v1/fs/preview?path="+"财务部%2F2026%2Fa.docx", "", nil, "127.0.0.1:1")
	if w.Code != 200 || f.lastPath != "财务部/2026/a.docx" || !strings.Contains(w.Body.String(), `"kind":"document"`) {
		t.Fatalf("archived preview: %d %s path=%q", w.Code, w.Body, f.lastPath)
	}
	if w = do(h, "GET", "/v1/files/22222222-2222-4222-8222-222222222222/preview", "", nil, "127.0.0.1:1"); w.Code != 200 || !strings.Contains(w.Body.String(), `"kind":"pdf"`) {
		t.Fatalf("file preview: %d %s", w.Code, w.Body)
	}
	if w = do(h, "POST", "/v1/fs/preview?path=a", "", nil, "127.0.0.1:1"); w.Code != 405 {
		t.Fatalf("read-only endpoint: %d", w.Code)
	}
}

func TestPreviewErrorMapping(t *testing.T) {
	f := &fakePreview{}
	h := previewServer(f)
	for err, want := range map[error][2]string{
		preview.ErrNotFound: {"404", "NOT_FOUND"},
		fsman.ErrNotFound:   {"404", "NOT_FOUND"},
		fsman.ErrInvalid:    {"400", "INVALID_PATH"},
		fsman.ErrOutside:    {"400", "INVALID_PATH"},
		preview.ErrBadPage:  {"400", "INVALID_PAGE"},
		&preview.WorkerError{Code: "ENCRYPTED", Status: 422}:          {"422", "ENCRYPTED"},
		&preview.WorkerError{Code: "UNREADABLE_FILE", Status: 422}:    {"422", "UNREADABLE_FILE"},
		&preview.WorkerError{Code: "UNSUPPORTED_FORMAT", Status: 422}: {"422", "UNSUPPORTED_FORMAT"},
		&preview.WorkerError{Code: "PATH_NOT_ALLOWED", Status: 403}:   {"503", "PREVIEW_UNAVAILABLE"},
		&preview.WorkerError{Code: "WORKER_UNAVAILABLE", Status: 503}: {"503", "PREVIEW_UNAVAILABLE"},
		&preview.WorkerError{Code: "EXTRACT_TIMEOUT", Status: 504}:    {"503", "PREVIEW_UNAVAILABLE"},
		errors.New("database password is hunter2 at 10.0.0.9"):        {"500", "INTERNAL"},
	} {
		f.err = err
		for _, target := range []string{"/v1/fs/preview?path=a.pdf", "/v1/fs/preview/page?path=a.pdf", "/v1/files/33333333-3333-4333-8333-333333333333/preview", "/v1/files/33333333-3333-4333-8333-333333333333/preview/page"} {
			w := do(h, "GET", target, "", nil, "127.0.0.1:1")
			if want[0] != strconv.Itoa(w.Code) || !strings.Contains(w.Body.String(), want[1]) || strings.Contains(w.Body.String(), "hunter2") || strings.Contains(w.Body.String(), "10.0.0.9") {
				t.Errorf("%v @ %s → %d %s, want %v", err, target, w.Code, w.Body, want)
			}
		}
	}
}
