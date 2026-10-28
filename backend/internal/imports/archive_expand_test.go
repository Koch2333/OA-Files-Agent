package imports

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
)

func writeZip(t *testing.T, path string, build func(w *zip.Writer)) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	build(w)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

func add(t *testing.T, w *zip.Writer, h *zip.FileHeader, body string) {
	t.Helper()
	h.Method = zip.Store
	fw, err := w.CreateHeader(h)
	if err != nil {
		t.Fatal(err)
	}
	fw.Write([]byte(body))
}

func TestExpandArchivesZip(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "stored.zip")
	gbk, _ := simplifiedchinese.GBK.NewEncoder().String("财务部/报销单.txt") // Windows tools write GBK names without the UTF-8 flag
	writeZip(t, zipPath, func(w *zip.Writer) {
		add(t, w, &zip.FileHeader{Name: "合同/甲方合同.txt"}, "contract A")
		add(t, w, &zip.FileHeader{Name: gbk}, "reimbursement")
		add(t, w, &zip.FileHeader{Name: "../evil.txt"}, "x")
		add(t, w, &zip.FileHeader{Name: "/abs/evil.txt"}, "x")
		add(t, w, &zip.FileHeader{Name: "C:\\win\\evil.txt"}, "x")
		add(t, w, &zip.FileHeader{Name: "secret.txt", Flags: 0x1}, "encrypted-looking")
		add(t, w, &zip.FileHeader{Name: "dir/"}, "")
	})
	s := &Service{Root: dir}
	b := &Batch{Job: Job{ID: "task"}, Files: []File{
		{ID: "z", SourceName: "资料.zip", SourcePath: "来源/资料.zip", StoredPath: zipPath},
		{ID: "p", SourceName: "plain.pdf", SourcePath: "plain.pdf", StoredPath: filepath.Join(dir, "plain.pdf")},
	}}
	consumed := s.expandArchives(dir, b)
	if len(consumed) != 1 || consumed[0] != zipPath {
		t.Fatalf("consumed: %v", consumed)
	}
	if _, err := os.Stat(zipPath); err != nil {
		t.Fatal("the archive must stay until the caller removes it (a failed save is retried)")
	}
	got := map[string]string{}
	for _, f := range b.Files {
		body := ""
		if f.StoredPath != "" && f.ID != "p" {
			raw, err := os.ReadFile(f.StoredPath)
			if err != nil {
				t.Fatalf("%s: %v", f.SourcePath, err)
			}
			body = string(raw)
			if !strings.HasPrefix(f.StoredPath, dir) || filepath.Dir(f.StoredPath) != dir {
				t.Fatalf("entry written outside the batch directory: %s", f.StoredPath)
			}
		}
		got[f.SourcePath] = body
	}
	// a leading "/" is stripped like common unzip tools do (the file stays inside the archive folder); "..", drive letters and ":" are refused
	if got["来源/资料/合同/甲方合同.txt"] != "contract A" || got["来源/资料/财务部/报销单.txt"] != "reimbursement" || got["来源/资料/abs/evil.txt"] != "x" || len(got) != 4 {
		t.Fatalf("expanded files: %v", got)
	}
	if _, ok := got["plain.pdf"]; !ok {
		t.Fatal("other files stay as they are")
	}
	codes := map[string]int{}
	for _, f := range b.Failures {
		codes[f.Code]++
		if strings.Contains(f.SourcePath, "..") {
			t.Fatalf("failure path leaks the traversal: %s", f.SourcePath)
		}
	}
	if codes["ARCHIVE_UNSAFE_PATH"] != 2 || codes["ARCHIVE_ENCRYPTED"] != 1 {
		t.Fatalf("failures: %v", codes)
	}
	if b.Job.TotalFiles != len(b.Files)+len(b.Failures) || b.Job.FailureCount != len(b.Failures) {
		t.Fatalf("counts: %+v", b.Job)
	}
	for i, f := range b.Failures {
		if f.Index != i {
			t.Fatalf("failure indexes must be unique and sequential: %+v", b.Failures)
		}
	}
}

func TestExpandArchivesLeavesBadArchivesAlone(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "broken.zip")
	os.WriteFile(bad, []byte("this is not a zip"), 0o600)
	onlyBad := filepath.Join(dir, "unsafe.zip")
	writeZip(t, onlyBad, func(w *zip.Writer) { add(t, w, &zip.FileHeader{Name: "../x.txt"}, "x") })
	s := &Service{Root: dir}
	b := &Batch{Job: Job{ID: "t"}, Files: []File{
		{ID: "1", SourceName: "broken.zip", SourcePath: "broken.zip", StoredPath: bad},
		{ID: "2", SourceName: "unsafe.zip", SourcePath: "unsafe.zip", StoredPath: onlyBad},
	}}
	if consumed := s.expandArchives(dir, b); len(consumed) != 0 || len(b.Files) != 2 || len(b.Failures) != 0 {
		t.Fatalf("a damaged archive, or one with nothing usable inside, stays an ordinary file: %v %d %d", consumed, len(b.Files), len(b.Failures))
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 2 {
		t.Fatalf("no stray files may be left behind: %v", ents)
	}
}

func TestZipEntrySizeIsEnforcedWhileCopying(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.zip")
	writeZip(t, p, func(w *zip.Writer) { add(t, w, &zip.FileHeader{Name: "ok.txt"}, "12345") })
	zr, err := zip.OpenReader(p)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	zr.File[0].UncompressedSize64 = 3 // a header that lies about the size must not make us write more than it declares
	if _, _, err := copyEntry(zr.File[0], filepath.Join(dir, "out")); err == nil {
		t.Fatal("size mismatch must fail")
	}
}

const batchZ = "22222222-2222-4222-8222-222222222222"

// A ZIP uploaded in chunks and committed becomes the files inside it; the archive itself is gone from the spool afterwards.
func TestCommitStagedExpandsAZip(t *testing.T) {
	ms := &memStore{}
	s := &Service{Store: ms, Root: t.TempDir()}
	src := filepath.Join(t.TempDir(), "in.zip")
	writeZip(t, src, func(w *zip.Writer) {
		add(t, w, &zip.FileHeader{Name: "a/合同.txt"}, "contract")
		add(t, w, &zip.FileHeader{Name: "b.txt"}, "note")
		add(t, w, &zip.FileHeader{Name: "../bad.txt"}, "x")
	})
	raw, _ := os.ReadFile(src)
	ctx := context.Background()
	sess, err := s.CreateUpload("u1", CreateUploadReq{BatchID: batchZ, Name: "项目资料.zip", Path: "项目资料.zip", Size: int64(len(raw)), SHA256: sum(raw)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AppendChunk(ctx, "u1", sess.ID, 0, bytes.NewReader(raw), int64(len(raw))); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CompleteUpload(ctx, "u1", sess.ID); err != nil {
		t.Fatal(err)
	}
	b, err := s.CommitStaged(ctx, "u1", batchZ, "kz", "")
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]bool{}
	for _, f := range b.Files {
		paths[f.SourcePath] = true
		if body, err := os.ReadFile(f.StoredPath); err != nil || len(body) == 0 || f.SHA256 != sum(body) {
			t.Fatalf("%s: stored copy or hash wrong: %v", f.SourcePath, err)
		}
		if strings.HasSuffix(f.StoredPath, ".zip") {
			t.Fatalf("the archive must not remain as a file of the task: %s", f.StoredPath)
		}
	}
	if len(b.Files) != 2 || !paths["项目资料/a/合同.txt"] || !paths["项目资料/b.txt"] {
		t.Fatalf("files: %v", paths)
	}
	if len(b.Failures) != 1 || b.Failures[0].Code != "ARCHIVE_UNSAFE_PATH" || b.Job.TotalFiles != 3 || b.Job.FailureCount != 1 {
		t.Fatalf("failures/counts: %+v %+v", b.Failures, b.Job)
	}
	if ms.saved == nil || len(ms.saved.Files) != 2 {
		t.Fatal("the saved batch must hold the expanded files")
	}
}
