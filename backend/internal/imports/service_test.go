package imports

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"os"
	"strings"
	"testing"
)

type memStore struct{ saved *Batch }

func (m *memStore) Save(_ context.Context, b Batch) (Batch, bool, error) {
	m.saved = &b
	return b, false, nil
}

type part struct{ field, filename, content string }

func body(t *testing.T, parts ...part) *multipart.Reader {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, p := range parts {
		if p.filename == "" {
			_ = mw.WriteField(p.field, p.content)
			continue
		}
		w, _ := mw.CreateFormFile(p.field, p.filename)
		w.Write([]byte(p.content))
	}
	mw.Close()
	return multipart.NewReader(&buf, mw.Boundary())
}

func paths(p ...string) part {
	b, _ := json.Marshal(p)
	return part{field: "paths", content: string(b)}
}

func run(t *testing.T, parts ...part) (Batch, *memStore, error, string) {
	t.Helper()
	root := t.TempDir()
	ms := &memStore{}
	b, err := (&Service{Store: ms, Root: root}).Import(context.Background(), body(t, parts...), "", "")
	return b, ms, err, root
}

func code(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func TestImportKeepsRelativePathsAndFlagsUnsupportedFilesWithoutFailingTheBatch(t *testing.T) {
	b, ms, err, root := run(t,
		paths("财务/a.docx", "财务/旧.doc", "空.pdf", "图/b.PNG"),
		part{"files", "a.docx", "hello"}, part{"files", "旧.doc", "legacy"}, part{"files", "空.pdf", ""}, part{"files", "b.PNG", "img"})
	if err != nil {
		t.Fatal(err)
	}
	if b.Job.TotalFiles != 4 || len(b.Files) != 2 || len(b.Failures) != 2 || b.Job.FailureCount != 2 || b.Job.ProcessedFiles != 2 || b.Job.Status != "queued" {
		t.Fatalf("batch = %+v", b.Job)
	}
	if b.Files[0].SourcePath != "财务/a.docx" || b.Files[1].SourcePath != "图/b.PNG" || b.Files[1].MIMEType != "image/png" {
		t.Fatalf("files = %+v", b.Files)
	}
	codes := map[string]string{}
	for _, f := range b.Failures {
		codes[f.SourcePath] = f.Code
		if len(f.SHA256) != 64 {
			t.Errorf("failure %s has no hash", f.SourcePath)
		}
	}
	if codes["财务/旧.doc"] != "UNSUPPORTED_FORMAT" || codes["空.pdf"] != "EMPTY_FILE" {
		t.Fatalf("codes = %v", codes)
	}
	if ms.saved == nil || len(b.Fingerprint) != 64 {
		t.Fatal("batch must be saved with a request fingerprint")
	}
	for _, f := range b.Files {
		if !strings.HasPrefix(f.StoredPath, root) || strings.Contains(f.StoredPath, "财务") {
			t.Fatalf("client paths are metadata only; stored path = %s", f.StoredPath)
		}
		if st, err := os.Stat(f.StoredPath); err != nil || st.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
			t.Fatalf("stored file: %v %v", st, err)
		}
	}
}

func TestImportRejectsHostilePathsAndBadManifests(t *testing.T) {
	cases := map[string][]part{
		"traversal":         {paths("../x.docx"), {"files", "x.docx", "a"}},
		"absolute":          {paths("/etc/x.docx"), {"files", "x.docx", "a"}},
		"drive":             {paths(`C:\x.docx`), {"files", "x.docx", "a"}},
		"backslash":         {paths(`a\x.docx`), {"files", "x.docx", "a"}},
		"reserved name":     {paths("CON.docx"), {"files", "CON.docx", "a"}},
		"trailing dot":      {paths("a/x.docx."), {"files", "x.docx.", "a"}},
		"name != path base": {paths("a/y.docx"), {"files", "x.docx", "a"}},
		"count mismatch":    {paths("a.docx", "b.docx"), {"files", "a.docx", "a"}},
		"no files":          {paths()},
		"paths after files": {{"files", "a.docx", "a"}, paths("a.docx")},
		"unknown field":     {paths("a.docx"), {"evil", "a.docx", "a"}},
		"manifest not json": {{field: "paths", content: "nope"}, {"files", "a.docx", "a"}},
	}
	for name, parts := range cases {
		t.Run(name, func(t *testing.T) {
			_, ms, err, root := run(t, parts...)
			if err == nil || code(err) == "" {
				t.Fatalf("expected a typed rejection, got %v", err)
			}
			if ms.saved != nil {
				t.Fatal("nothing may be saved for a rejected request")
			}
			if entries, _ := os.ReadDir(root); len(entries) != 0 {
				t.Fatalf("spool must be cleaned after rejection, found %d entries", len(entries))
			}
		})
	}
}

func TestImportEnforcesFileCountAndSize(t *testing.T) {
	var ps []string
	var parts []part
	for i := 0; i < MaxFiles+1; i++ {
		n := "f" + strings.Repeat("a", i%30) + string(rune('0'+i%10)) + ".pdf"
		ps = append(ps, "d/"+string(rune('a'+i/26))+string(rune('a'+i%26))+n)
	}
	parts = append(parts, paths(ps...))
	_, _, err, _ := run(t, parts...)
	if code(err) != "TOO_MANY_FILES" {
		t.Fatalf("101 files → %v", err)
	}
	big := strings.Repeat("x", int(MaxFileBytes)+10)
	b, _, err, root := run(t, paths("big.pdf", "ok.pdf"), part{"files", "big.pdf", big}, part{"files", "ok.pdf", "fine"})
	if err != nil || len(b.Files) != 1 || len(b.Failures) != 1 || b.Failures[0].Code != "FILE_TOO_LARGE" || b.Failures[0].SizeBytes != int64(len(big)) {
		t.Fatalf("oversize file must be a per-file failure: %v %+v", err, b.Failures)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 1 {
		t.Fatal("spool dir missing")
	}
}

func TestValidatePath(t *testing.T) {
	for _, ok := range []string{"a.docx", "财务部/2026/合同 一.pdf", "a/b/c.png"} {
		if err := ValidatePath(ok); err != nil {
			t.Errorf("%q should be valid: %v", ok, err)
		}
	}
	for _, bad := range []string{"", ".", "..", "a/../b", "/a", "a//b", "a/./b", "a\\b", "C:/x", "a:b", "x*.pdf", "a?.pdf", "NUL", "com1.txt", "a/LPT9.docx", "x\x00.pdf", "tab\t.pdf", strings.Repeat("a", 241)} {
		if err := ValidatePath(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}
