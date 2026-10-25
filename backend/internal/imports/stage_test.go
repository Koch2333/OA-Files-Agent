package imports

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const batchA = "11111111-1111-4111-8111-111111111111"

func newStage(t *testing.T) (*Service, *memStore) {
	t.Helper()
	ms := &memStore{}
	return &Service{Store: ms, Root: t.TempDir()}, ms
}

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func status(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

func TestChunkedUploadResumeIdempotencyAndCommit(t *testing.T) {
	s, ms := newStage(t)
	data := bytes.Repeat([]byte("0123456789abcdef"), 1000) // 16000 bytes
	ctx := context.Background()
	sess, err := s.CreateUpload("u1", CreateUploadReq{BatchID: batchA, Name: "a.pdf", Path: "合同/a.pdf", Size: int64(len(data)), SHA256: strings.ToUpper(sum(data))})
	if err != nil || sess.Offset != 0 {
		t.Fatal(err)
	}
	// first chunk
	got, err := s.AppendChunk(ctx, "u1", sess.ID, 0, bytes.NewReader(data[:5000]), 5000)
	if err != nil || got.Offset != 5000 {
		t.Fatalf("chunk1: %v %+v", err, got)
	}
	// duplicate chunk (lost response, client retries): idempotent
	got, err = s.AppendChunk(ctx, "u1", sess.ID, 0, bytes.NewReader(data[:5000]), 5000)
	if err != nil || got.Offset != 5000 {
		t.Fatalf("duplicate chunk must be accepted without change: %v %+v", err, got)
	}
	// overlapping retry: only the new tail is appended
	got, err = s.AppendChunk(ctx, "u1", sess.ID, 3000, bytes.NewReader(data[3000:9000]), 6000)
	if err != nil || got.Offset != 9000 {
		t.Fatalf("overlap: %v %+v", err, got)
	}
	// gap: offset ahead of what the server has → OffsetError with the current offset
	_, err = s.AppendChunk(ctx, "u1", sess.ID, 12000, bytes.NewReader(data[12000:]), 4000)
	var oe *OffsetError
	if !errors.As(err, &oe) || oe.Current != 9000 {
		t.Fatalf("gap must report offset 9000, got %v", err)
	}
	// resume query
	q, err := s.GetUpload("u1", sess.ID)
	if err != nil || q.Offset != 9000 {
		t.Fatalf("query: %v %+v", err, q)
	}
	// complete before everything arrived
	if _, err = s.CompleteUpload(ctx, "u1", sess.ID); !errors.As(err, &oe) || oe.Current != 9000 {
		t.Fatalf("early complete: %v", err)
	}
	// a body that dies midway keeps the prefix; the client resumes from the reported offset
	_, err = s.AppendChunk(ctx, "u1", sess.ID, 9000, io_errReader(data[9000:11000]), 7000)
	if err == nil {
		t.Fatal("broken body must fail")
	}
	q, _ = s.GetUpload("u1", sess.ID)
	if q.Offset != 11000 {
		t.Fatalf("partial bytes must be kept, offset = %d", q.Offset)
	}
	if _, err = s.AppendChunk(ctx, "u1", sess.ID, q.Offset, bytes.NewReader(data[q.Offset:]), int64(len(data))-q.Offset); err != nil {
		t.Fatal(err)
	}
	done, err := s.CompleteUpload(ctx, "u1", sess.ID)
	if err != nil || !done.Complete || done.SHA256 != sum(data) {
		t.Fatalf("complete: %v %+v", err, done)
	}
	if again, err := s.CompleteUpload(ctx, "u1", sess.ID); err != nil || !again.Complete {
		t.Fatalf("complete is idempotent: %v", err)
	}
	// other users cannot touch it
	if _, err = s.GetUpload("u2", sess.ID); status(err) != 403 {
		t.Fatalf("foreign access → %v", err)
	}
	// a second, unsupported + an empty file, then commit
	bad, _ := s.CreateUpload("u1", CreateUploadReq{BatchID: batchA, Name: "x.zip", Size: 3})
	s.AppendChunk(ctx, "u1", bad.ID, 0, strings.NewReader("abc"), 3)
	s.CompleteUpload(ctx, "u1", bad.ID)
	empty, _ := s.CreateUpload("u1", CreateUploadReq{BatchID: batchA, Name: "e.pdf", Size: 0})
	s.CompleteUpload(ctx, "u1", empty.ID)
	b, err := s.CommitStaged(ctx, "u1", batchA, "k1", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Files) != 1 || len(b.Failures) != 2 || b.Files[0].SHA256 != sum(data) || b.Files[0].SourcePath != "合同/a.pdf" || b.Files[0].SizeBytes != int64(len(data)) {
		t.Fatalf("batch: %+v %+v", b.Files, b.Failures)
	}
	raw, err := os.ReadFile(b.Files[0].StoredPath)
	if err != nil || !bytes.Equal(raw, data) {
		t.Fatalf("stored bytes differ: %v", err)
	}
	if ms.saved == nil || b.Job.TotalFiles != 3 {
		t.Fatal("not saved")
	}
	if _, err = s.CreateUpload("u1", CreateUploadReq{BatchID: batchA, Name: "n.pdf", Size: 1}); status(err) != 409 {
		t.Fatalf("committed batch must refuse new uploads: %v", err)
	}
	leftovers, _ := filepath.Glob(filepath.Join(s.batchDir(batchA), "*.part"))
	if len(leftovers) != 0 {
		t.Fatal("part files must not survive a commit")
	}
}

type errBody struct {
	b   []byte
	pos int
}

func io_errReader(b []byte) *errBody { return &errBody{b: b} }
func (e *errBody) Read(p []byte) (int, error) {
	if e.pos >= len(e.b) {
		return 0, errors.New("connection reset")
	}
	n := copy(p, e.b[e.pos:])
	e.pos += n
	return n, nil
}

func TestChecksumMismatchIsRejectedAndCleaned(t *testing.T) {
	s, _ := newStage(t)
	ctx := context.Background()
	sess, _ := s.CreateUpload("u", CreateUploadReq{BatchID: batchA, Name: "a.pdf", Size: 4, SHA256: strings.Repeat("0", 64)})
	if _, err := s.AppendChunk(ctx, "u", sess.ID, 0, strings.NewReader("data"), 4); err != nil {
		t.Fatal(err)
	}
	_, err := s.CompleteUpload(ctx, "u", sess.ID)
	var e *Error
	if !errors.As(err, &e) || e.Code != "CHECKSUM_MISMATCH" || e.Status != 422 {
		t.Fatalf("got %v", err)
	}
	if _, err = s.GetUpload("u", sess.ID); status(err) != 404 {
		t.Fatalf("a corrupt upload must be removed: %v", err)
	}
	ents, _ := os.ReadDir(s.batchDir(batchA))
	for _, en := range ents {
		if en.Name() != "batch.json" {
			t.Fatalf("leftover %s", en.Name())
		}
	}
}

func TestChunkBeyondDeclaredSizeIsRefused(t *testing.T) {
	s, _ := newStage(t)
	sess, _ := s.CreateUpload("u", CreateUploadReq{BatchID: batchA, Name: "a.pdf", Size: 4})
	_, err := s.AppendChunk(context.Background(), "u", sess.ID, 0, strings.NewReader("too long"), 8)
	if status(err) != 422 {
		t.Fatalf("got %v", err)
	}
	if q, _ := s.GetUpload("u", sess.ID); q.Offset != 4 {
		t.Fatalf("file must be cut back to the declared size, offset %d", q.Offset)
	}
}

func TestAbortRemovesEverything(t *testing.T) {
	s, _ := newStage(t)
	ctx := context.Background()
	sess, _ := s.CreateUpload("u", CreateUploadReq{BatchID: batchA, Name: "a.pdf", Size: 10})
	s.AppendChunk(ctx, "u", sess.ID, 0, strings.NewReader("12345"), 5)
	if err := s.AbortUpload("u", sess.ID); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(s.batchDir(batchA))
	for _, en := range ents {
		if en.Name() != "batch.json" {
			t.Fatalf("leftover %s", en.Name())
		}
	}
	if _, err := s.GetUpload("u", sess.ID); status(err) != 404 {
		t.Fatalf("got %v", err)
	}
}

func TestAbandonedSessionsExpire(t *testing.T) {
	s, _ := newStage(t)
	now := time.Now()
	s.Limits.Now = func() time.Time { return now }
	s.Limits.SessionTTL = time.Hour
	s.CreateUpload("u", CreateUploadReq{BatchID: batchA, Name: "a.pdf", Size: 10})
	if n := s.GC(); n != 0 {
		t.Fatalf("fresh batch collected: %d", n)
	}
	// age every file in the staging dir
	old := now.Add(-2 * time.Hour)
	filepath.Walk(s.stagingRoot(), func(p string, _ os.FileInfo, _ error) error { return os.Chtimes(p, old, old) })
	s.Limits.Now = func() time.Time { return now }
	if n := s.GC(); n != 1 {
		t.Fatalf("expired batch must be collected, got %d", n)
	}
	if _, err := os.Stat(s.batchDir(batchA)); !os.IsNotExist(err) {
		t.Fatal("batch dir must be gone")
	}
}

func TestDiskSpaceRefusalOnCreateAndAppend(t *testing.T) {
	s, _ := newStage(t)
	free := uint64(10 << 30)
	s.Limits = Limits{DiskReserve: 1 << 30, FreeSpace: func(string) (uint64, error) { return free, nil }}
	if _, err := s.CreateUpload("u", CreateUploadReq{BatchID: batchA, Name: "big.pdf", Size: 12 << 30}); status(err) != 507 {
		t.Fatalf("create: %v", err)
	}
	var e *Error
	_, err := s.CreateUpload("u", CreateUploadReq{BatchID: batchA, Name: "big.pdf", Size: 12 << 30})
	if !errors.As(err, &e) || !strings.Contains(e.Message, "12.0 GiB") || !strings.Contains(e.Message, "10.0 GiB") {
		t.Fatalf("message must say need X free Y: %v", err)
	}
	sess, err := s.CreateUpload("u", CreateUploadReq{BatchID: batchA, Name: "ok.pdf", Size: 100})
	if err != nil {
		t.Fatal(err)
	}
	free = 500 // disk filled up meanwhile
	if _, err = s.AppendChunk(context.Background(), "u", sess.ID, 0, strings.NewReader("x"), 1); status(err) != 507 {
		t.Fatalf("append: %v", err)
	}
}

func TestOperatorValvesApplyToChunkedUploads(t *testing.T) {
	s, _ := newStage(t)
	s.Limits = Limits{MaxUploadBytes: 100, MaxBatchFiles: 1}
	if _, err := s.CreateUpload("u", CreateUploadReq{BatchID: batchA, Name: "a.pdf", Size: 101}); status(err) != 413 {
		t.Fatalf("per-file cap: %v", err)
	}
	if _, err := s.CreateUpload("u", CreateUploadReq{BatchID: batchA, Name: "a.pdf", Size: 100}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUpload("u", CreateUploadReq{BatchID: batchA, Name: "b.pdf", Size: 1}); status(err) != 413 {
		t.Fatalf("batch cap: %v", err)
	}
}

func TestUploadInputValidation(t *testing.T) {
	s, _ := newStage(t)
	for name, req := range map[string]CreateUploadReq{
		"traversal": {Name: "x.pdf", Path: "../x.pdf", Size: 1},
		"bad batch": {BatchID: "../../etc", Name: "x.pdf", Size: 1},
		"neg size":  {Name: "x.pdf", Size: -1},
		"bad sha":   {Name: "x.pdf", Size: 1, SHA256: "zz"},
		"no name":   {Size: 1},
	} {
		if _, err := s.CreateUpload("u", req); status(err) != 400 {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := s.GetUpload("u", "../../etc/passwd"); status(err) != 404 {
		t.Fatalf("hostile id: %v", err)
	}
}

func TestCommitIsIdempotentWithKey(t *testing.T) {
	s, _ := newStage(t)
	ctx := context.Background()
	sess, _ := s.CreateUpload("u", CreateUploadReq{BatchID: batchA, Name: "a.pdf", Size: 3})
	s.AppendChunk(ctx, "u", sess.ID, 0, strings.NewReader("abc"), 3)
	s.CompleteUpload(ctx, "u", sess.ID)
	first, err := s.CommitStaged(ctx, "u", batchA, "key", "")
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.CommitStaged(ctx, "u", batchA, "key", "")
	if err != nil || again.Fingerprint != first.Fingerprint {
		t.Fatalf("replay path must hit the store with the stored fingerprint: %v", err)
	}
	if _, err = s.CommitStaged(ctx, "u", batchA, "", ""); status(err) != 409 {
		t.Fatalf("recommit without key: %v", err)
	}
}

// ---- local-path import ----

func waitPath(t *testing.T, p *PathImport) PathStatus {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		st := p.Status()
		if st.State != "scanning" && st.State != "running" && st.State != "committing" {
			return st
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("path import did not finish")
	return PathStatus{}
}

func TestPathImportCopiesWalksAndNeverTouchesSources(t *testing.T) {
	s, _ := newStage(t)
	src := t.TempDir()
	write := func(rel, content string) string {
		p := filepath.Join(src, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write("项目/合同/a.pdf", "AAAA")
	write("项目/b.docx", "BB")
	write("项目/notes.txt", "skip me")
	write("项目/.hidden.pdf", "hidden")
	write("项目/empty.pdf", "")
	single := write("single.xlsx", "SS")
	if runtime.GOOS != "windows" {
		os.Symlink(filepath.Join(src, "项目"), filepath.Join(src, "项目", "loop")) // directory loop through a symlink
		os.Symlink(single, filepath.Join(src, "项目", "link.xlsx"))
	}
	before, _ := os.ReadFile(single)
	p, err := s.StartPathImport("u", batchA, []string{filepath.Join(src, "项目"), single, filepath.Join(src, "nope.pdf"), "relative/x.pdf"}, PathOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	st := waitPath(t, p)
	if st.State != "done" || st.Copied != 3 {
		t.Fatalf("status: %+v", st)
	}
	codes := map[string]string{}
	for _, r := range st.Results {
		codes[filepath.Base(r.Path)+":"+r.Status] = r.Code
	}
	if codes["empty.pdf:failed"] != "EMPTY_FILE" || codes["nope.pdf:failed"] != "NOT_FOUND" || codes["x.pdf:failed"] != "INVALID_PATH" || codes["notes.txt:skipped"] != "UNSUPPORTED_FORMAT" {
		t.Fatalf("results: %v", codes)
	}
	if _, hidden := codes[".hidden.pdf:copied"]; hidden {
		t.Fatal("hidden files are skipped by default")
	}
	if runtime.GOOS != "windows" {
		if codes["loop:skipped"] != "LINK" || codes["link.xlsx:skipped"] != "LINK" {
			t.Fatalf("symlinks must be skipped: %v", codes)
		}
	}
	after, _ := os.ReadFile(single)
	if !bytes.Equal(before, after) {
		t.Fatal("source modified")
	}
	if _, err := os.Stat(single); err != nil {
		t.Fatal("source must remain")
	}
	b, err := s.CommitStaged(context.Background(), "u", batchA, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Files) != 3 || len(b.Failures) != 1 {
		t.Fatalf("commit: %d files %d failures %+v", len(b.Files), len(b.Failures), b.Failures)
	}
	rels := map[string]string{}
	for _, f := range b.Files {
		rels[f.SourcePath] = f.SHA256
		raw, _ := os.ReadFile(f.StoredPath)
		if sum(raw) != f.SHA256 {
			t.Fatalf("hash of %s", f.SourcePath)
		}
	}
	if rels["项目/合同/a.pdf"] != sum([]byte("AAAA")) || rels["项目/b.docx"] == "" || rels["single.xlsx"] == "" {
		t.Fatalf("rels: %v", rels)
	}
}

func TestPathImportUnreadableFileDoesNotAbortBatch(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs POSIX permissions and a non-root user")
	}
	s, _ := newStage(t)
	src := t.TempDir()
	bad := filepath.Join(src, "locked.pdf")
	os.WriteFile(bad, []byte("secret"), 0o000)
	os.WriteFile(filepath.Join(src, "ok.pdf"), []byte("fine"), 0o644)
	p, _ := s.StartPathImport("u", batchA, []string{src}, PathOptions{}, nil)
	st := waitPath(t, p)
	if st.State != "done" || st.Copied != 1 || st.Failed != 1 {
		t.Fatalf("status %+v", st)
	}
	for _, r := range st.Results {
		if r.Status == "failed" && r.Code != "READ_FAILED" {
			t.Fatalf("code %s", r.Code)
		}
	}
}

func TestPathImportHugeSparseFileIsHandledByLimitsNotByMemory(t *testing.T) {
	src := t.TempDir()
	huge := filepath.Join(src, "huge.pdf")
	f, err := os.Create(huge)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(3 << 30); err != nil { // 3 GiB sparse
		f.Close()
		t.Skip("filesystem cannot create a 3 GiB sparse file")
	}
	f.Close()
	if st, _ := os.Stat(huge); st.Size() != 3<<30 {
		t.Skip("no sparse file support")
	}
	// disk too small: refused up front with the 507 message, nothing copied, batch marked failed
	s, _ := newStage(t)
	s.Limits = Limits{DiskReserve: 1 << 20, FreeSpace: func(string) (uint64, error) { return 1 << 30, nil }}
	p, _ := s.StartPathImport("u", batchA, []string{huge}, PathOptions{}, nil)
	st := waitPath(t, p)
	if st.State != "failed" || !strings.Contains(st.Error, "磁盘空间不足") || st.BytesCopied != 0 {
		t.Fatalf("status %+v", st)
	}
	// operator valve
	s2, _ := newStage(t)
	s2.Limits = Limits{MaxUploadBytes: 1 << 20}
	p2, _ := s2.StartPathImport("u", batchA, []string{huge}, PathOptions{}, nil)
	st2 := waitPath(t, p2)
	if st2.Failed != 1 || st2.Results[0].Code != "FILE_TOO_LARGE" || st2.Results[0].Size != 3<<30 {
		t.Fatalf("status %+v", st2)
	}
}

func TestPathImportRejectsDevicePathsAndOwnSpool(t *testing.T) {
	s, _ := newStage(t)
	if err := s.ValidateSourcePath(filepath.Join(s.Root, "staging", "x.pdf")); err == nil {
		t.Fatal("own spool must be refused")
	}
	if err := s.ValidateSourcePath("relative.pdf"); err == nil {
		t.Fatal("relative paths must be refused")
	}
	if err := s.ValidateSourcePath("/tmp/a\x00.pdf"); err == nil {
		t.Fatal("NUL must be refused")
	}
	if runtime.GOOS == "windows" {
		for _, bad := range []string{`\\.\PhysicalDrive0`, `\\?\GLOBALROOT\Device\X`} {
			if s.ValidateSourcePath(bad) == nil {
				t.Fatalf("%s must be refused", bad)
			}
		}
	}
}

func TestPathImportCancelAndFileCap(t *testing.T) {
	s, _ := newStage(t)
	s.Limits = Limits{MaxBatchFiles: 1}
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "a.pdf"), []byte("a"), 0o644)
	os.WriteFile(filepath.Join(src, "b.pdf"), []byte("b"), 0o644)
	p, _ := s.StartPathImport("u", batchA, []string{src}, PathOptions{}, nil)
	st := waitPath(t, p)
	if st.State != "failed" || !strings.Contains(st.Error, "每批文件数上限") {
		t.Fatalf("status %+v", st)
	}
}
