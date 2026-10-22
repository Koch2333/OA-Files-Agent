package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"oa-agent/backend/internal/fileops"
	"oa-agent/backend/internal/fsman"
)

type fakeFiles struct {
	revealCalls []string
	canReveal   bool
	root        string
	err         error
}

func (f *fakeFiles) List(_ context.Context, rel string) (fileops.Listing, error) {
	return fileops.Listing{Path: rel, Entries: []fileops.ListedEntry{}}, f.err
}
func (f *fakeFiles) Content(rel string) (*os.File, os.FileInfo, error) {
	m := &fsman.Manager{Root: f.root}
	return m.Open(rel)
}
func (f *fakeFiles) Mkdir(context.Context, string, string) (fileops.ListedEntry, error) {
	return fileops.ListedEntry{}, f.err
}
func (f *fakeFiles) Move(context.Context, string, string, string) (fileops.ListedEntry, int64, error) {
	return fileops.ListedEntry{}, 0, f.err
}
func (f *fakeFiles) Trash(context.Context, string, string) (fsman.TrashItem, int64, error) {
	return fsman.TrashItem{}, 0, f.err
}
func (f *fakeFiles) ListTrash() ([]fsman.TrashItem, error) { return nil, f.err }
func (f *fakeFiles) Restore(context.Context, string, string) (string, int64, error) {
	return "", 0, f.err
}
func (f *fakeFiles) Reveal(_ context.Context, rel, _ string) (string, error) {
	f.revealCalls = append(f.revealCalls, rel)
	return rel, f.err
}
func (f *fakeFiles) Root() string    { return f.root }
func (f *fakeFiles) CanReveal() bool { return f.canReveal }

func newFSServer(t *testing.T, f *fakeFiles, strict bool) http.Handler {
	t.Helper()
	return NewServer(Deps{Files: f, StrictLocal: strict})
}

func do(h http.Handler, method, target, body string, hdr map[string]string, remote string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	if remote != "" {
		r.RemoteAddr = remote
	}
	for k, v := range hdr {
		if k == "Host" {
			r.Host = v
			continue
		}
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestRevealIsLocalOnlyAndHonoursCapabilities(t *testing.T) {
	f := &fakeFiles{canReveal: true, root: t.TempDir()}
	h := newFSServer(t, f, false)
	w := do(h, "POST", "/v1/fs/reveal", `{"path":"a/b.pdf"}`, nil, "127.0.0.1:5555")
	if w.Code != 200 || len(f.revealCalls) != 1 || f.revealCalls[0] != "a/b.pdf" {
		t.Fatalf("local reveal: %d %s calls=%v", w.Code, w.Body, f.revealCalls)
	}
	w = do(h, "POST", "/v1/fs/reveal", `{"path":"a"}`, nil, "192.168.1.20:5555")
	if w.Code != 403 || len(f.revealCalls) != 1 || !strings.Contains(w.Body.String(), "LOCAL_ONLY") {
		t.Fatalf("remote client must be refused without launching anything: %d %s", w.Code, w.Body)
	}
	f.err = fileops.ErrRevealUnsupported
	if w = do(h, "POST", "/v1/fs/reveal", `{}`, nil, "[::1]:1"); w.Code != 501 || !strings.Contains(w.Body.String(), "NOT_SUPPORTED") {
		t.Fatalf("unsupported platform: %d %s", w.Code, w.Body)
	}
	f.err = fileops.ErrRevealDisabled
	if w = do(h, "POST", "/v1/fs/reveal", `{}`, nil, "127.0.0.1:1"); w.Code != 403 || !strings.Contains(w.Body.String(), "REVEAL_DISABLED") {
		t.Fatalf("disabled: %d %s", w.Code, w.Body)
	}
	f.err = fsman.ErrInvalid
	if w = do(h, "POST", "/v1/fs/reveal", `{"path":"../x"}`, nil, "127.0.0.1:1"); w.Code != 400 {
		t.Fatalf("invalid path: %d", w.Code)
	}
}

func TestListingCapabilitiesDependOnCallerAndPlatform(t *testing.T) {
	f := &fakeFiles{canReveal: true, root: `D:\归档`}
	h := newFSServer(t, f, false)
	type resp struct {
		Data struct {
			Capabilities struct {
				Reveal bool    `json:"reveal"`
				Root   *string `json:"root"`
			} `json:"capabilities"`
		} `json:"data"`
	}
	var local, remote resp
	_ = json.Unmarshal(do(h, "GET", "/v1/fs", "", nil, "127.0.0.1:9").Body.Bytes(), &local)
	_ = json.Unmarshal(do(h, "GET", "/v1/fs", "", nil, "10.0.0.7:9").Body.Bytes(), &remote)
	if !local.Data.Capabilities.Reveal || local.Data.Capabilities.Root == nil || *local.Data.Capabilities.Root != `D:\归档` {
		t.Fatalf("local capabilities: %+v", local)
	}
	if remote.Data.Capabilities.Reveal || remote.Data.Capabilities.Root != nil {
		t.Fatalf("remote callers must not learn the server path nor see reveal: %+v", remote)
	}
	f.canReveal = false
	_ = json.Unmarshal(do(h, "GET", "/v1/fs", "", nil, "127.0.0.1:9").Body.Bytes(), &local)
	if local.Data.Capabilities.Reveal {
		t.Fatal("reveal must be off when the service cannot reveal")
	}
}

func TestCrossSiteWritesAreRefused(t *testing.T) {
	f := &fakeFiles{root: t.TempDir()}
	h := newFSServer(t, f, false)
	for name, hdr := range map[string]map[string]string{
		"foreign origin": {"Origin": "https://evil.example"},
		"null origin":    {"Origin": "null"},
		"fetch metadata": {"Sec-Fetch-Site": "cross-site"},
		"same-site only": {"Sec-Fetch-Site": "same-site"},
	} {
		if w := do(h, "POST", "/v1/fs/trash", `{"path":"a.pdf"}`, hdr, "127.0.0.1:1"); w.Code != 403 || !strings.Contains(w.Body.String(), "CROSS_ORIGIN") {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	for name, hdr := range map[string]map[string]string{
		"no origin (curl, scripts)": nil,
		"same origin":               {"Origin": "http://example.com", "Sec-Fetch-Site": "same-origin"},
		"direct navigation":         {"Sec-Fetch-Site": "none"},
	} {
		if w := do(h, "POST", "/v1/fs/trash", `{"path":"a.pdf"}`, hdr, "127.0.0.1:1"); w.Code == 403 {
			t.Errorf("%s must be allowed: %d %s", name, w.Code, w.Body)
		}
	}
	if w := do(h, "GET", "/v1/fs", "", map[string]string{"Origin": "https://evil.example"}, "127.0.0.1:1"); w.Code == 403 {
		t.Error("reads are not blocked by the origin check (the browser's CORS policy hides the response)")
	}
}

func TestStrictLocalRejectsForeignHostHeaders(t *testing.T) {
	h := newFSServer(t, &fakeFiles{root: t.TempDir()}, true)
	for host, want := range map[string]int{"127.0.0.1:8080": 200, "localhost:8080": 200, "[::1]:8080": 200, "evil.example": 403, "evil.example:8080": 403, "127.0.0.1.evil.example": 403} {
		if w := do(h, "GET", "/v1/fs", "", map[string]string{"Host": host}, "127.0.0.1:1"); w.Code != want {
			t.Errorf("Host %q → %d, want %d", host, w.Code, want)
		}
	}
	if w := do(h, "GET", "/healthz", "", map[string]string{"Host": "evil.example"}, "127.0.0.1:1"); w.Code != 403 {
		t.Errorf("even health must not answer foreign hosts: %d", w.Code)
	}
}

func TestContentHeadersForPreviewAndDownload(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "d"), 0o755)
	os.WriteFile(filepath.Join(root, "d", "报告.pdf"), []byte("%PDF-1.4 test"), 0o644)
	os.WriteFile(filepath.Join(root, "d", "page.html"), []byte("<script>alert(1)</script>"), 0o644)
	os.WriteFile(filepath.Join(root, "d", "图.png"), []byte("png"), 0o644)
	h := newFSServer(t, &fakeFiles{root: root}, false)
	w := do(h, "GET", "/v1/fs/content?path="+"d/报告.pdf", "", nil, "127.0.0.1:1")
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "inline") || w.Header().Get("Content-Type") != "application/pdf" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("pdf preview: %d %v", w.Code, w.Header())
	}
	if !strings.Contains(w.Header().Get("Content-Disposition"), "%E6%8A%A5%E5%91%8A.pdf") {
		t.Fatalf("UTF-8 filename missing: %s", w.Header().Get("Content-Disposition"))
	}
	if w = do(h, "GET", "/v1/fs/content?path=d/报告.pdf&download=1", "", nil, "127.0.0.1:1"); !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("download flag: %v", w.Header())
	}
	w = do(h, "GET", "/v1/fs/content?path=d/page.html", "", nil, "127.0.0.1:1")
	if !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment") || w.Header().Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("active content must never render inline: %v", w.Header())
	}
	r := httptest.NewRequest("GET", "/v1/fs/content?path=d/图.png", nil)
	r.Header.Set("Range", "bytes=1-2")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != 206 || rec.Body.String() != "ng" {
		t.Fatalf("range request: %d %q", rec.Code, rec.Body)
	}
	for _, p := range []string{"../etc/passwd", "d", ".journal/x", "nope.pdf", ""} {
		if w = do(h, "GET", "/v1/fs/content?path="+p, "", nil, "127.0.0.1:1"); w.Code != 400 && w.Code != 404 {
			t.Errorf("path %q → %d", p, w.Code)
		}
	}
}

func TestFileManagerErrorMapping(t *testing.T) {
	f := &fakeFiles{root: t.TempDir()}
	h := newFSServer(t, f, false)
	for err, want := range map[error]int{fsman.ErrInvalid: 400, fsman.ErrOutside: 400, fsman.ErrNotFound: 404, fsman.ErrExists: 409, fsman.ErrConflict: 409} {
		f.err = err
		if w := do(h, "POST", "/v1/fs/move", `{"from":"a","to":"b"}`, nil, "127.0.0.1:1"); w.Code != want {
			t.Errorf("%v → %d, want %d", err, w.Code, want)
		}
	}
	f.err = nil
	if w := do(h, "POST", "/v1/fs/move", `{"from":"a","to":"b","extra":1}`, nil, "127.0.0.1:1"); w.Code != 400 {
		t.Errorf("unknown field: %d", w.Code)
	}
	if w := do(h, "PUT", "/v1/fs/move", ``, nil, "127.0.0.1:1"); w.Code != 405 {
		t.Errorf("method: %d", w.Code)
	}
}
