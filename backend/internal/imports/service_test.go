package imports

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
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

func TestImportAcceptsEveryFileKeepsRelativePathsAndNeverFailsTheBatchOnFormat(t *testing.T) {
	b, ms, err, root := run(t,
		paths("财务/a.docx", "财务/旧.doc", "空.pdf", "图/b.PNG"),
		part{"files", "a.docx", "hello"}, part{"files", "旧.doc", "legacy"}, part{"files", "空.pdf", ""}, part{"files", "b.PNG", "img"})
	if err != nil {
		t.Fatal(err)
	}
	// legacy .doc, an empty .pdf and (below) an unknown extension are all accepted: content that cannot be read is
	// handled later by name only, so intake never rejects on format or emptiness.
	if b.Job.TotalFiles != 4 || len(b.Files) != 4 || len(b.Failures) != 0 || b.Job.FailureCount != 0 || b.Job.ProcessedFiles != 0 || b.Job.Status != "queued" {
		t.Fatalf("batch = %+v", b.Job)
	}
	if b.Files[0].SourcePath != "财务/a.docx" || b.Files[1].MIMEType != "application/msword" || b.Files[2].SizeBytes != 0 || b.Files[3].SourcePath != "图/b.PNG" || b.Files[3].MIMEType != "image/png" {
		t.Fatalf("files = %+v", b.Files)
	}
	codes := map[string]string{}
	for _, f := range b.Failures {
		codes[f.SourcePath] = f.Code
		if len(f.SHA256) != 64 {
			t.Errorf("failure %s has no hash", f.SourcePath)
		}
	}
	if len(codes) != 0 {
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

func TestImportHasNoDefaultCountOrSizeLimit(t *testing.T) {
	var ps []string
	var parts []part
	for i := 0; i < 300; i++ {
		ps = append(ps, "d/f"+strings.Repeat("a", i%7)+string(rune('0'+i%10))+string(rune('a'+i/26))+string(rune('a'+i%26))+".pdf")
	}
	parts = append(parts, paths(ps...))
	for _, p := range ps {
		parts = append(parts, part{"files", p[2:], "x"})
	}
	b, _, err, _ := run(t, parts...)
	if err != nil || len(b.Files) != 300 {
		t.Fatalf("300 files must be accepted: %v", err)
	}
	big := strings.Repeat("x", 25<<20) // the old 20 MiB cap is gone
	b, _, err, _ = run(t, paths("big.pdf"), part{"files", "big.pdf", big})
	if err != nil || len(b.Files) != 1 || len(b.Failures) != 0 || b.Files[0].SizeBytes != int64(len(big)) {
		t.Fatalf("25 MiB file must be accepted: %v %+v", err, b.Failures)
	}
}

func TestOptionalOperatorValvesStillWork(t *testing.T) {
	root := t.TempDir()
	svc := &Service{Store: &memStore{}, Root: root, Limits: Limits{MaxUploadBytes: 1 << 10, MaxBatchFiles: 2}}
	b, err := svc.Import(context.Background(), body(t, paths("big.pdf", "ok.pdf"), part{"files", "big.pdf", strings.Repeat("x", 2000)}, part{"files", "ok.pdf", "fine"}), "", "")
	if err != nil || len(b.Files) != 1 || len(b.Failures) != 1 || b.Failures[0].Code != "FILE_TOO_LARGE" || b.Failures[0].SizeBytes != 2000 {
		t.Fatalf("over-cap file must be a per-file failure: %v %+v", err, b.Failures)
	}
	_, err = svc.Import(context.Background(), body(t, paths("a.pdf", "b.pdf", "c.pdf")), "", "")
	if code(err) != "TOO_MANY_FILES" {
		t.Fatalf("3 files with cap 2 → %v", err)
	}
}

func TestImportRefusesWhenDiskIsFull(t *testing.T) {
	root := t.TempDir()
	svc := &Service{Store: &memStore{}, Root: root, Limits: Limits{DiskReserve: 100, FreeSpace: func(string) (uint64, error) { return 150, nil }}}
	ctx := WithExpectedBytes(context.Background(), 1000)
	_, err := svc.Import(ctx, body(t, paths("a.pdf"), part{"files", "a.pdf", "x"}), "", "")
	var e *Error
	if !errors.As(err, &e) || e.Status != 507 || !strings.Contains(e.Message, "需要") || !strings.Contains(e.Message, "剩余") {
		t.Fatalf("want 507 with need/free, got %v", err)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Fatal("nothing may be written when refusing")
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

func runOpts(t *testing.T, h OptionsHandler, parts ...part) (Batch, *memStore, error) {
	t.Helper()
	ms := &memStore{}
	ctx := WithAI(context.Background(), TaskAI{Source: SourceGlobal, Model: "default-model"})
	if h != nil {
		ctx = WithOptionsHandler(ctx, h)
	}
	b, err := (&Service{Store: ms, Root: t.TempDir()}).Import(ctx, body(t, parts...), "", "")
	return b, ms, err
}

func TestOptionsPartIsParsedInOrderAndAppliedToTheTask(t *testing.T) {
	var got []byte
	h := func(raw []byte) (TaskAI, error) {
		got = raw
		return TaskAI{Instruction: "只处理采购", Automation: AutomationAuto, Source: SourcePersonal, Owner: "u1", Model: "m1", AckVersion: "v1", AckBy: "alice", Fingerprint: "fp-1"}, nil
	}
	b, ms, err := runOpts(t, h, paths("a.docx"), part{"options", "", `{"instruction":"x"}`}, part{"files", "a.docx", "hello"})
	if err != nil {
		t.Fatal(err)
	}
	j := ms.saved.Job
	if string(got) != `{"instruction":"x"}` || j.Instruction != "只处理采购" || j.Automation != "auto" || j.LLMSource != "personal" || j.LLMOwner != "u1" || j.LLMModel != "m1" || j.AckVersion != "v1" || j.AckBy != "alice" || j.AckAt == nil || b.Job.ID != j.ID {
		t.Fatalf("job = %+v", j)
	}
	// without an options part the default from the context applies (a plain import still records its model)
	_, ms, err = runOpts(t, h, paths("a.docx"), part{"files", "a.docx", "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if j := ms.saved.Job; j.LLMSource != "global" || j.LLMModel != "default-model" || j.Automation != "review" || j.AckAt != nil {
		t.Fatalf("default job = %+v", j)
	}
	// no AI context at all: review mode and no model
	_, ms, _, _ = run(t, paths("a.docx"), part{"files", "a.docx", "hello"})
	if j := ms.saved.Job; j.Automation != "review" || j.LLMSource != "none" {
		t.Fatalf("legacy job = %+v", j)
	}
}

func TestOptionsPartMisuseIsRejectedBeforeAnyFileIsStored(t *testing.T) {
	h := func([]byte) (TaskAI, error) { return TaskAI{}, nil }
	opts := part{"options", "", `{}`}
	file := part{"files", "a.docx", "hello"}
	cases := map[string][]part{
		"before paths":   {opts, paths("a.docx"), file},
		"after file":     {paths("a.docx"), file, opts},
		"twice":          {paths("a.docx"), opts, opts, file},
		"without paths":  {opts, file},
		"paths after it": {paths("a.docx"), opts, paths("a.docx"), file},
	}
	for name, parts := range cases {
		if _, _, err := runOpts(t, h, parts...); code(err) != "INVALID_MULTIPART" {
			t.Errorf("%s: %v", name, err)
		}
	}
	// without a handler the part is not supported at all
	if _, _, err := runOpts(t, nil, paths("a.docx"), opts, file); code(err) != "INVALID_MULTIPART" {
		t.Errorf("no handler: %v", err)
	}
	// oversized options
	big := part{"options", "", strings.Repeat("x", MaxOptionsBytes+10)}
	if _, _, err := runOpts(t, h, paths("a.docx"), big, file); code(err) != "INVALID_OPTIONS" {
		t.Errorf("oversized: %v", err)
	}
	// a handler error aborts the import and nothing is spooled
	root := t.TempDir()
	reject := func([]byte) (TaskAI, error) { return TaskAI{}, &Error{422, "LLM_REQUIRED", "x"} }
	ms := &memStore{}
	_, err := (&Service{Store: ms, Root: root}).Import(WithOptionsHandler(context.Background(), reject), body(t, paths("a.docx"), opts, file), "", "")
	if code(err) != "LLM_REQUIRED" || ms.saved != nil {
		t.Fatalf("%v %v", err, ms.saved)
	}
	if ents, _ := os.ReadDir(root); len(ents) != 0 {
		t.Fatalf("spool must be empty: %v", ents)
	}
}

func TestOptionsAreBoundIntoTheIdempotencyFingerprint(t *testing.T) {
	mk := func(fp string) string {
		h := func([]byte) (TaskAI, error) { return TaskAI{Fingerprint: fp}, nil }
		_, ms, err := runOpts(t, h, paths("a.docx"), part{"options", "", `{}`}, part{"files", "a.docx", "hello"})
		if err != nil {
			t.Fatal(err)
		}
		return ms.saved.Fingerprint
	}
	a, b, c := mk("one"), mk("one"), mk("two")
	if a != b || a == c {
		t.Fatalf("fingerprints: %s %s %s", a, b, c)
	}
	_, ms, _, _ := run(t, paths("a.docx"), part{"files", "a.docx", "hello"})
	if ms.saved.Fingerprint == a {
		t.Fatal("a request with options must not collide with one without")
	}
}

func TestRemoteImportRecordsAISettingsAndFingerprint(t *testing.T) {
	open := func(context.Context) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("hello")), nil }
	files := []RemoteFile{{RelPath: "a.docx", Size: 5, Open: open}}
	do := func(ai TaskAI) *Batch {
		ms := &memStore{}
		_, err := (&Service{Store: ms, Root: t.TempDir()}).ImportRemote(WithAI(context.Background(), ai), files, "", "", "conn-1", nil)
		if err != nil {
			t.Fatal(err)
		}
		return ms.saved
	}
	s := do(TaskAI{Instruction: "按项目", Automation: AutomationAuto, Source: SourceGlobal, Owner: "u1", Model: "m", AckVersion: "v1", AckBy: "alice", Fingerprint: "fp"})
	if j := s.Job; j.Instruction != "按项目" || j.Automation != "auto" || j.LLMSource != "global" || j.LLMOwner != "u1" || j.AckVersion != "v1" || j.AckAt == nil {
		t.Fatalf("%+v", j)
	}
	plain := do(TaskAI{})
	if plain.Job.Automation != "review" || plain.Job.LLMSource != "none" || plain.Fingerprint == s.Fingerprint {
		t.Fatalf("%+v", plain.Job)
	}
	if do(TaskAI{Fingerprint: "a"}).Fingerprint == do(TaskAI{Fingerprint: "b"}).Fingerprint {
		t.Fatal("options must change the fingerprint")
	}
}

func TestImportStoresOddExtensionsSafelyAndKeepsTheOriginalName(t *testing.T) {
	long := "a." + strings.Repeat("x", 80)
	b, _, err, root := run(t,
		paths("x/图纸.dwg", "x/"+long, "x/noext", "x/压缩.ZIP"),
		part{"files", "图纸.dwg", "d"}, part{"files", long, "l"}, part{"files", "noext", "n"}, part{"files", "压缩.ZIP", "z"})
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Files) != 4 || len(b.Failures) != 0 {
		t.Fatalf("every extension is accepted: %+v %+v", b.Files, b.Failures)
	}
	for _, f := range b.Files {
		if f.MIMEType != "application/octet-stream" {
			t.Errorf("%s: unknown formats are octet-stream, got %s", f.SourceName, f.MIMEType)
		}
		if !strings.HasPrefix(f.StoredPath, root) || len(filepath.Base(f.StoredPath)) > 60 {
			t.Errorf("stored name must stay short and inside the spool: %s", f.StoredPath)
		}
	}
	if b.Files[0].SourceName != "图纸.dwg" || b.Files[3].SourceName != "压缩.ZIP" || !strings.HasSuffix(b.Files[0].StoredPath, ".dwg") || strings.Contains(b.Files[1].StoredPath, "xxxxxxxx") {
		t.Fatalf("names: %+v", b.Files)
	}
}
