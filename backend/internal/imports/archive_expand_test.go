package imports

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
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

// Files pulled from cloud storage follow the same rule: a ZIP becomes the files inside it.
func TestRemoteImportExpandsAZip(t *testing.T) {
	src := filepath.Join(t.TempDir(), "r.zip")
	writeZip(t, src, func(w *zip.Writer) {
		add(t, w, &zip.FileHeader{Name: "合同/甲.txt"}, "contract")
		add(t, w, &zip.FileHeader{Name: "../bad.txt"}, "x")
	})
	raw, _ := os.ReadFile(src)
	open := func(context.Context) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(raw)), nil }
	ms := &memStore{}
	b, err := (&Service{Store: ms, Root: t.TempDir()}).ImportRemote(context.Background(), []RemoteFile{{RelPath: "云盘/资料.zip", Size: int64(len(raw)), Open: open}}, "", "", "conn-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Files) != 1 || b.Files[0].SourcePath != "云盘/资料/合同/甲.txt" || len(b.Failures) != 1 || b.Failures[0].Code != "ARCHIVE_UNSAFE_PATH" || b.Job.TotalFiles != 2 {
		t.Fatalf("batch: %+v %+v %+v", b.Files, b.Failures, b.Job)
	}
	if body, err := os.ReadFile(b.Files[0].StoredPath); err != nil || string(body) != "contract" {
		t.Fatalf("stored copy: %q %v", body, err)
	}
}

// ---- 7z (real archives from tests/samples/archives) and RAR (a RAR5 file written byte by byte below) ----

func sampleArchive(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join("..", "..", "..", "tests", "samples", "archives", name)
	if _, err := os.Stat(p); err != nil {
		t.Skipf("fixture missing: %v", err)
	}
	return p
}

func copyTo(t *testing.T, src, dstDir, dstName string) string {
	t.Helper()
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dstDir, dstName)
	if err := os.WriteFile(dst, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return dst
}

func TestExpandArchives7z(t *testing.T) {
	dir := t.TempDir()
	stored := copyTo(t, sampleArchive(t, "sample-lzma2.7z"), dir, "s7.7z")
	s := &Service{Root: dir}
	b := &Batch{Job: Job{ID: "t"}, Files: []File{{ID: "a", SourceName: "资料.7z", SourcePath: "来源/资料.7z", StoredPath: stored}}}
	consumed := s.expandArchives(dir, b)
	if len(consumed) != 1 || len(b.Files) == 0 {
		t.Fatalf("a normal 7z is expanded: consumed=%v files=%d failures=%+v", consumed, len(b.Files), b.Failures)
	}
	for _, f := range b.Files {
		raw, err := os.ReadFile(f.StoredPath)
		if err != nil || int64(len(raw)) != f.SizeBytes || f.SHA256 != sum(raw) {
			t.Fatalf("%s: stored copy/size/hash wrong: %v", f.SourcePath, err)
		}
		if !strings.HasPrefix(f.SourcePath, "来源/资料/") || filepath.Dir(f.StoredPath) != dir {
			t.Fatalf("source path / location wrong: %s %s", f.SourcePath, f.StoredPath)
		}
	}
}

func TestEncrypted7zStaysAnOrdinaryFile(t *testing.T) {
	dir := t.TempDir()
	stored := copyTo(t, sampleArchive(t, "sample-encrypted.7z"), dir, "e.7z")
	b := &Batch{Job: Job{ID: "t"}, Files: []File{{ID: "e", SourceName: "e.7z", SourcePath: "e.7z", StoredPath: stored}}}
	if consumed := (&Service{Root: dir}).expandArchives(dir, b); len(consumed) != 0 || len(b.Files) != 1 || b.Files[0].ID != "e" {
		t.Fatalf("an encrypted archive is never opened: %v %+v", consumed, b.Files)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Fatalf("no stray files: %v", ents)
	}
}

// rar5 writes a minimal RAR5 archive (stored method) with valid header and data CRCs.
func rar5(entries map[string]string, order []string) []byte {
	vint := func(v uint64) []byte {
		var o []byte
		for v >= 0x80 {
			o = append(o, byte(v)|0x80)
			v >>= 7
		}
		return append(o, byte(v))
	}
	crc := func(b []byte) uint32 { return crc32.ChecksumIEEE(b) }
	block := func(body []byte) []byte { // CRC32 | size | body ; the CRC covers size+body
		sz := vint(uint64(len(body)))
		inner := append(append([]byte{}, sz...), body...)
		var c [4]byte
		binary.LittleEndian.PutUint32(c[:], crc(inner))
		return append(c[:], inner...)
	}
	out := []byte("Rar!\x1a\x07\x01\x00")
	out = append(out, block(append(append(vint(1), vint(0)...), vint(0)...))...) // main header: type 1, flags 0, archive flags 0
	for _, name := range order {
		data := []byte(entries[name])
		var fc [4]byte
		binary.LittleEndian.PutUint32(fc[:], crc(data))
		body := vint(2)                                 // file header
		body = append(body, vint(0x0002)...)            // header flags: data area present
		body = append(body, vint(uint64(len(data)))...) // data size
		body = append(body, vint(0x0004)...)            // file flags: CRC32 present
		body = append(body, vint(uint64(len(data)))...) // unpacked size
		body = append(body, vint(0)...)                 // attributes
		body = append(body, fc[:]...)                   // data CRC32
		body = append(body, vint(0)...)                 // compression: stored
		body = append(body, vint(1)...)                 // host OS: unix
		body = append(body, vint(uint64(len(name)))...) // name length
		body = append(body, []byte(name)...)
		out = append(out, block(body)...)
		out = append(out, data...)
	}
	return append(out, block(append(append(vint(5), vint(0)...), vint(0)...))...) // end of archive
}

func TestExpandArchivesRar(t *testing.T) {
	dir := t.TempDir()
	order := []string{"合同/甲.txt", "b.txt", "../evil.txt"}
	raw := rar5(map[string]string{"合同/甲.txt": "contract", "b.txt": "note", "../evil.txt": "x"}, order)
	stored := filepath.Join(dir, "in.rar")
	if err := os.WriteFile(stored, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	b := &Batch{Job: Job{ID: "t"}, Files: []File{{ID: "r", SourceName: "项目.rar", SourcePath: "项目.rar", StoredPath: stored}}}
	consumed := (&Service{Root: dir}).expandArchives(dir, b)
	got := map[string]string{}
	for _, f := range b.Files {
		body, _ := os.ReadFile(f.StoredPath)
		got[f.SourcePath] = string(body)
	}
	if len(consumed) != 1 || got["项目/合同/甲.txt"] != "contract" || got["项目/b.txt"] != "note" || len(got) != 2 {
		t.Fatalf("rar: consumed=%v files=%v failures=%+v", consumed, got, b.Failures)
	}
	if len(b.Failures) != 1 || b.Failures[0].Code != "ARCHIVE_UNSAFE_PATH" {
		t.Fatalf("the traversal entry is refused and reported: %+v", b.Failures)
	}
}

func TestDamagedRarAndZipLookalikesStayOrdinaryFiles(t *testing.T) {
	dir := t.TempDir()
	rar := filepath.Join(dir, "x.rar")
	os.WriteFile(rar, []byte("Rar!\x1a\x07\x01\x00 truncated"), 0o600)
	b := &Batch{Job: Job{ID: "t"}, Files: []File{{ID: "r", SourceName: "x.rar", SourcePath: "x.rar", StoredPath: rar}}}
	if consumed := (&Service{Root: dir}).expandArchives(dir, b); len(consumed) != 0 || len(b.Files) != 1 {
		t.Fatalf("damaged rar: %v %+v", consumed, b.Files)
	}
}

func TestCopyEntryEnforcesTheDeclaredSizeAndTheBudget(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := copyEntry(strings.NewReader("12345"), 3, filepath.Join(dir, "a"), 1000); err == nil {
		t.Fatal("a header that lies about the size must fail")
	}
	if _, _, err := copyEntry(strings.NewReader("12345"), -1, filepath.Join(dir, "b"), 3); !errors.Is(err, errBudget) {
		t.Fatalf("unknown size beyond the budget: %v", err)
	}
	if sum, n, err := copyEntry(strings.NewReader("12345"), 5, filepath.Join(dir, "c"), 1000); err != nil || n != 5 || sum == "" {
		t.Fatalf("exact size: %v", err)
	}
}
