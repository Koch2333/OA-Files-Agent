package preview

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"oa-agent/backend/internal/dbtest"
	"oa-agent/backend/internal/fsman"
	"oa-agent/backend/internal/imports"
)

type fakeWorker struct {
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []map[string]any
	auth []string
	// reply decides the answer per path
	reply func(path string, body map[string]any) (int, string)
}

func newWorker(t *testing.T, reply func(string, map[string]any) (int, string)) *fakeWorker {
	f := &fakeWorker{reply: reply}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.reqs = append(f.reqs, body)
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		f.mu.Unlock()
		code, out := f.reply(r.URL.Path, body)
		w.WriteHeader(code)
		w.Write([]byte(out))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func pngB64(t *testing.T) string {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func archive(t *testing.T) (string, *fsman.Manager) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "财务部", "2026"), 0o755)
	for _, n := range []string{"财务部/2026/a.docx", "财务部/2026/b.xlsx", "财务部/2026/c.pdf", "财务部/2026/old.doc"} {
		os.WriteFile(filepath.Join(root, filepath.FromSlash(n)), []byte("x"), 0o644)
	}
	return root, &fsman.Manager{Root: root}
}

func TestArchivedPreviewKindsAndWorkerContract(t *testing.T) {
	_, fm := archive(t)
	w := newWorker(t, func(path string, body map[string]any) (int, string) {
		p := body["path"].(string)
		switch {
		case strings.HasSuffix(p, "a.docx"):
			return 200, `{"data":{"kind":"document","blocks":[{"type":"heading","level":1,"text":"标题"},{"type":"paragraph","runs":[{"text":"正文","bold":true}]}],"header":"页眉","truncated":false,"warnings":["含 2 张图片未显示"]}}`
		case strings.HasSuffix(p, "b.xlsx"):
			return 200, `{"data":{"kind":"spreadsheet","sheets":[{"name":"S1","rows":[["A","B"]],"rows_total":500,"cols_total":2,"truncated":true}],"truncated":true,"warnings":[]}}`
		case strings.HasSuffix(p, "c.pdf"):
			return 200, `{"data":{"kind":"pdf","pages":7}}`
		default:
			return 422, `{"error":{"code":"UNSUPPORTED_FORMAT","message":"x"}}`
		}
	})
	s := &Service{FS: fm, Worker: w.srv.URL, Token: "tok-123"}
	ctx := context.Background()

	d, err := s.Archived(ctx, "财务部/2026/a.docx")
	if err != nil || d.Kind != "document" || d.Name != "a.docx" || d.Header != "页眉" || len(d.Warnings) != 1 || d.PageURL != nil || d.Pages != nil {
		t.Fatalf("document: %v %+v", err, d)
	}
	var blocks []map[string]any
	_ = json.Unmarshal(d.Blocks, &blocks)
	if len(blocks) != 2 || blocks[0]["type"] != "heading" {
		t.Fatalf("blocks pass through untouched: %s", d.Blocks)
	}
	x, err := s.Archived(ctx, "财务部/2026/b.xlsx")
	if err != nil || x.Kind != "spreadsheet" || !x.Truncated || len(x.Sheets) == 0 {
		t.Fatalf("spreadsheet: %v %+v", err, x)
	}
	p, err := s.Archived(ctx, "财务部/2026/c.pdf")
	if err != nil || p.Kind != "pdf" || p.Pages == nil || *p.Pages != 7 || p.PageURL == nil || !strings.Contains(*p.PageURL, "{page}") || !strings.Contains(*p.PageURL, "%E8%B4%A2%E5%8A%A1%E9%83%A8") {
		t.Fatalf("pdf: %v %+v", err, p)
	}
	u, err := s.Archived(ctx, "财务部/2026/old.doc")
	if err != nil || u.Kind != "unsupported" || !strings.Contains(u.Message, "另存为") || u.Warnings == nil {
		t.Fatalf("legacy formats are an answer, not an error: %v %+v", err, u)
	}
	raw, _ := json.Marshal(d)
	if !strings.Contains(string(raw), `"warnings":["`) || !strings.Contains(string(raw), `"message":""`) || strings.Contains(string(raw), `"sheets"`) {
		t.Fatalf("JSON shape: %s", raw)
	}
	for _, a := range w.auth {
		if a != "Bearer tok-123" {
			t.Fatalf("worker calls must carry the token: %q", a)
		}
	}
	// the worker receives an absolute path inside the archive, never the relative user input
	first := w.reqs[0]["path"].(string)
	if !filepath.IsAbs(first) || !strings.HasSuffix(filepath.ToSlash(first), "财务部/2026/a.docx") {
		t.Fatalf("worker path: %q", first)
	}
}

func TestArchivedRejectsUnsafeAndMissingPathsBeforeCallingTheWorker(t *testing.T) {
	_, fm := archive(t)
	calls := 0
	w := newWorker(t, func(string, map[string]any) (int, string) { calls++; return 200, `{"data":{"kind":"pdf","pages":1}}` })
	s := &Service{FS: fm, Worker: w.srv.URL, Token: "t"}
	for _, p := range []string{"../x.pdf", ".journal/x", "/etc/passwd", "财务部/..", ""} {
		if _, err := s.Archived(context.Background(), p); !errors.Is(err, fsman.ErrInvalid) {
			t.Errorf("%q: %v", p, err)
		}
		if _, err := s.ArchivedPage(context.Background(), p, 1, 800); !errors.Is(err, fsman.ErrInvalid) {
			t.Errorf("page %q: %v", p, err)
		}
	}
	if _, err := s.Archived(context.Background(), "财务部/2026/none.pdf"); !errors.Is(err, fsman.ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
	if _, err := s.Archived(context.Background(), "财务部/2026"); !errors.Is(err, fsman.ErrInvalid) {
		t.Errorf("folder: %v", err)
	}
	if calls != 0 {
		t.Fatalf("the worker must never be asked about rejected paths (%d calls)", calls)
	}
}

func TestWorkerErrorsAreMappedAndBadAnswersRejected(t *testing.T) {
	_, fm := archive(t)
	code := ""
	status := 422
	body := ""
	w := newWorker(t, func(string, map[string]any) (int, string) {
		if body != "" {
			return status, body
		}
		return status, `{"error":{"code":"` + code + `"}}`
	})
	s := &Service{FS: fm, Worker: w.srv.URL, Token: "t"}
	for _, c := range []string{"ENCRYPTED", "UNREADABLE_FILE", "PATH_NOT_ALLOWED", "EXTRACT_TIMEOUT"} {
		code = c
		_, err := s.Archived(context.Background(), "财务部/2026/a.docx")
		var we *WorkerError
		if !errors.As(err, &we) || we.Code != c {
			t.Errorf("%s: %v", c, err)
		}
	}
	for name, b := range map[string]string{"not json": `nope`, "unknown kind": `{"data":{"kind":"script"}}`, "empty": `{}`} {
		body, status = b, 200
		if _, err := s.Archived(context.Background(), "财务部/2026/a.docx"); err == nil {
			t.Errorf("%s must be rejected", name)
		}
	}
	body = `{"data":{"kind":"document","blocks":"` + strings.Repeat("x", maxPreviewJSON) + `"}}`
	if _, err := s.Archived(context.Background(), "财务部/2026/a.docx"); err == nil {
		t.Error("oversized worker answers are refused")
	}
	// worker down
	w.srv.Close()
	var we *WorkerError
	if _, err := s.Archived(context.Background(), "财务部/2026/a.docx"); !errors.As(err, &we) || we.Code != "WORKER_UNAVAILABLE" || we.Status != 503 {
		t.Errorf("unavailable: %v", err)
	}
}

func TestPageImages(t *testing.T) {
	_, fm := archive(t)
	img := pngB64(t)
	mode := "ok"
	w := newWorker(t, func(path string, body map[string]any) (int, string) {
		if path != "/v1/render" {
			return 404, `{}`
		}
		switch mode {
		case "range":
			return 400, `{"error":{"code":"INVALID_REQUEST"}}`
		case "notpng":
			return 200, `{"data":{"image_base64":"` + base64.StdEncoding.EncodeToString([]byte("GIF89a....")) + `","mime_type":"image/png"}}`
		case "badb64":
			return 200, `{"data":{"image_base64":"%%%","mime_type":"image/png"}}`
		case "jpeg":
			return 200, `{"data":{"image_base64":"` + img + `","mime_type":"image/jpeg"}}`
		}
		return 200, `{"data":{"image_base64":"` + img + `","mime_type":"image/png","width":8,"height":8,"page":` + jsonNum(body["page"]) + `,"pages":3}}`
	})
	s := &Service{FS: fm, Worker: w.srv.URL, Token: "t"}
	ctx := context.Background()
	out, err := s.ArchivedPage(ctx, "财务部/2026/c.pdf", 2, 900)
	if err != nil || len(out) < 8 || string(out[1:4]) != "PNG" {
		t.Fatalf("page: %v", err)
	}
	last := w.reqs[len(w.reqs)-1]
	if last["page"].(float64) != 2 || last["max_side"].(float64) != 900 {
		t.Fatalf("params forwarded: %v", last)
	}
	if _, err = s.ArchivedPage(ctx, "财务部/2026/c.pdf", 0, 10); err != nil {
		t.Fatal(err)
	}
	last = w.reqs[len(w.reqs)-1]
	if last["page"].(float64) != 1 || last["max_side"].(float64) != 1400 {
		t.Fatalf("out-of-range arguments are normalised to safe defaults: %v", last)
	}
	for m, want := range map[string]error{"range": ErrBadPage} {
		mode = m
		if _, err = s.ArchivedPage(ctx, "财务部/2026/c.pdf", 99, 800); !errors.Is(err, want) {
			t.Errorf("%s: %v", m, err)
		}
	}
	for _, m := range []string{"notpng", "badb64", "jpeg"} {
		mode = m
		var we *WorkerError
		if _, err = s.ArchivedPage(ctx, "财务部/2026/c.pdf", 1, 800); !errors.As(err, &we) || we.Code != "WORKER_BAD_RESPONSE" {
			t.Errorf("%s must be refused as a bad worker answer: %v", m, err)
		}
	}
}

func jsonNum(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestFilePreviewUsesTheStoredOriginalCopy(t *testing.T) {
	st := dbtest.New(t)
	spool := t.TempDir()
	stored := filepath.Join(spool, "orig.docx")
	os.WriteFile(stored, []byte("x"), 0o600)
	id, _ := imports.NewID()
	fid, _ := imports.NewID()
	now := time.Now().UTC()
	b := imports.Batch{Job: imports.Job{ID: id, Status: "queued", TotalFiles: 1, CreatedAt: now, UpdatedAt: now}, Failures: []imports.Failure{},
		Files: []imports.File{{ID: fid, TaskID: id, SourceName: "报销申请.docx", SourcePath: "in/报销申请.docx", MIMEType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document", SHA256: strings.Repeat("a", 64), SizeBytes: 1, Evidence: []string{}, ArchiveStatus: "pending", CreatedAt: now, StoredPath: stored}}}
	if _, _, err := st.Save(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	w := newWorker(t, func(path string, body map[string]any) (int, string) {
		return 200, `{"data":{"kind":"document","blocks":[],"truncated":false,"warnings":[]}}`
	})
	s := &Service{Store: st, Worker: w.srv.URL, Token: "t"}
	p, err := s.File(context.Background(), fid)
	if err != nil || p.Name != "报销申请.docx" || p.Kind != "document" || p.SizeBytes != 1 {
		t.Fatalf("file preview: %v %+v", err, p)
	}
	if w.reqs[0]["path"] != stored {
		t.Fatalf("the worker must read the stored copy: %v", w.reqs[0]["path"])
	}
	for _, bad := range []string{"not-a-uuid", "00000000-0000-4000-8000-0000000000aa"} {
		if _, err := s.File(context.Background(), bad); !errors.Is(err, ErrNotFound) {
			t.Errorf("%q: %v", bad, err)
		}
		if _, err := s.FilePage(context.Background(), bad, 1, 800); !errors.Is(err, ErrNotFound) {
			t.Errorf("page %q: %v", bad, err)
		}
	}
	os.Remove(stored) // the spool copy was cleaned up
	if _, err := s.File(context.Background(), fid); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a missing original copy is a 404, not a worker call: %v", err)
	}
}
