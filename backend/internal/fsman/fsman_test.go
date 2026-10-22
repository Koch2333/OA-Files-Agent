package fsman

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func setup(t *testing.T) (*Manager, string) {
	t.Helper()
	root := t.TempDir()
	write := func(rel, c string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("财务部/2026/合同/a.pdf", "A")
	write("财务部/2026/合同/b.docx", "BB")
	write("人事部/c.png", "CCC")
	write(".staging/x", "hidden")
	write(".journal/j.json", "{}")
	return &Manager{Root: root}, root
}

func TestListHidesInternalDirsAndSortsFoldersFirst(t *testing.T) {
	m, _ := setup(t)
	top, err := m.List("")
	if err != nil || len(top) != 2 || top[0].Name != "人事部" || !top[0].IsDir {
		t.Fatalf("root listing: %v %+v", err, top)
	}
	files, err := m.List("财务部/2026/合同")
	if err != nil || len(files) != 2 || files[0].Name != "a.pdf" || !files[0].Previewable || files[1].Previewable || files[1].SizeBytes != 2 || files[0].Path != "财务部/2026/合同/a.pdf" {
		t.Fatalf("folder listing: %v %+v", err, files)
	}
	if _, err := m.List("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing folder: %v", err)
	}
	if _, err := m.List("人事部/c.png"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("listing a file must be rejected: %v", err)
	}
}

func TestHostileAndReservedPathsAreRejected(t *testing.T) {
	m, _ := setup(t)
	for _, p := range []string{"../x", "a/../../x", "/etc", `C:\x`, `a\b`, ".staging", ".journal/j.json", ".trash", "财务部/.hidden", "a//b", "x*y", "CON", "财务部/"} {
		if _, err := m.List(p); !errors.Is(err, ErrInvalid) {
			t.Errorf("List(%q) = %v, want ErrInvalid", p, err)
		}
		if _, err := m.Mkdir(p); !errors.Is(err, ErrInvalid) {
			t.Errorf("Mkdir(%q) = %v, want ErrInvalid", p, err)
		}
		if _, err := m.Move("人事部/c.png", p); !errors.Is(err, ErrInvalid) {
			t.Errorf("Move to %q = %v, want ErrInvalid", p, err)
		}
		if _, err := m.Trash(p); !errors.Is(err, ErrInvalid) {
			t.Errorf("Trash(%q) = %v", p, err)
		}
	}
	if _, _, err := m.Open("../../etc/passwd"); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := m.Trash(""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("the root itself can never be trashed: %v", err)
	}
}

func TestSymlinksAreNeitherFollowedNorListed(t *testing.T) {
	m, root := setup(t)
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("s"), 0o644)
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}
	top, _ := m.List("")
	for _, e := range top {
		if e.Name == "link" {
			t.Fatal("symlinks must not be listed")
		}
	}
	if _, err := m.List("link"); err == nil {
		t.Fatal("must not follow a symlinked folder")
	}
	if _, _, err := m.Open("link/secret.txt"); err == nil {
		t.Fatal("must not read through a symlink")
	}
	if _, err := m.Move("人事部/c.png", "link/c.png"); err == nil {
		t.Fatal("must not move through a symlink")
	}
}

func TestMoveNeverOverwritesAndRefusesFolderIntoItself(t *testing.T) {
	m, root := setup(t)
	if _, err := m.Move("财务部/2026/合同/a.pdf", "财务部/2026/合同/b.docx"); !errors.Is(err, ErrExists) {
		t.Fatalf("existing target: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "财务部/2026/合同/b.docx")); string(b) != "BB" {
		t.Fatal("target content must be untouched")
	}
	e, err := m.Move("财务部/2026/合同/a.pdf", "财务部/2026/合同/新名.pdf")
	if err != nil || e.Path != "财务部/2026/合同/新名.pdf" || e.IsDir {
		t.Fatalf("rename: %v %+v", err, e)
	}
	if _, err := os.Stat(filepath.Join(root, "财务部/2026/合同/a.pdf")); !os.IsNotExist(err) {
		t.Fatal("source must be gone after a move")
	}
	if _, err := m.Move("财务部", "财务部/2026/x"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("folder into itself: %v", err)
	}
	if _, err := m.Move("财务部/2026", "人事部/2026"); err != nil {
		t.Fatalf("folder move: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "人事部/2026/合同/新名.pdf")); string(b) != "A" {
		t.Fatal("folder contents must follow the move")
	}
	if _, err := m.Move("人事部/c.png", "不存在的夹/c.png"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing target folder: %v", err)
	}
	if _, err := m.Move("人事部/c.png", "人事部/c.png"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("same path: %v", err)
	}
	if _, err := m.Move("没有.pdf", "x.pdf"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing source: %v", err)
	}
}

func TestConcurrentMovesToOneTargetOnlyOneWins(t *testing.T) {
	m, root := setup(t)
	for i := 0; i < 8; i++ {
		os.WriteFile(filepath.Join(root, "人事部", strings.Repeat("s", i+1)+".txt"), []byte{byte('0' + i)}, 0o644)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := m.Move("人事部/"+strings.Repeat("s", i+1)+".txt", "人事部/target.txt"); err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("exactly one move may win, got %d", wins)
	}
}

func TestMkdir(t *testing.T) {
	m, _ := setup(t)
	if e, err := m.Mkdir("人事部/新建"); err != nil || !e.IsDir {
		t.Fatalf("mkdir: %v %+v", err, e)
	}
	if _, err := m.Mkdir("人事部/新建"); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate: %v", err)
	}
	if _, err := m.Mkdir("没有/新建"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing parent: %v", err)
	}
}

func TestOpenReadsFilesOnly(t *testing.T) {
	m, _ := setup(t)
	f, fi, err := m.Open("人事部/c.png")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(f)
	f.Close()
	if string(b) != "CCC" || fi.Size() != 3 {
		t.Fatal("content mismatch")
	}
	if _, _, err := m.Open("人事部"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("opening a folder: %v", err)
	}
}

func TestTrashAndRestore(t *testing.T) {
	m, root := setup(t)
	it, err := m.Trash("财务部/2026/合同/b.docx")
	if err != nil || it.OriginalPath != "财务部/2026/合同/b.docx" || it.SizeBytes != 2 || it.IsDir {
		t.Fatalf("trash: %v %+v", err, it)
	}
	if _, err := os.Stat(filepath.Join(root, "财务部/2026/合同/b.docx")); !os.IsNotExist(err) {
		t.Fatal("file must leave its folder")
	}
	if ls, _ := m.List("财务部/2026/合同"); len(ls) != 1 {
		t.Fatalf("listing after trash: %+v", ls)
	}
	if _, err := m.List(".trash"); !errors.Is(err, ErrInvalid) {
		t.Fatal("trash must not be browsable as a normal folder")
	}
	items, err := m.ListTrash()
	if err != nil || len(items) != 1 || items[0].ID != it.ID {
		t.Fatalf("list trash: %v %+v", err, items)
	}
	// occupy the original name, then restore → suffix, never overwrite
	os.WriteFile(filepath.Join(root, "财务部/2026/合同/b.docx"), []byte("NEW"), 0o644)
	p, err := m.Restore(it.ID)
	if err != nil || p != "财务部/2026/合同/b (已恢复).docx" {
		t.Fatalf("restore: %v %q", err, p)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "财务部/2026/合同/b.docx")); string(b) != "NEW" {
		t.Fatal("occupying file must be untouched")
	}
	if b, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(p))); string(b) != "BB" {
		t.Fatal("restored content mismatch")
	}
	if left, _ := m.ListTrash(); len(left) != 0 {
		t.Fatalf("trash must be empty after restore: %+v", left)
	}
	if _, err := m.Restore(it.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second restore: %v", err)
	}
}

func TestRestoreRecreatesDeletedParentsAndHandlesFolders(t *testing.T) {
	m, root := setup(t)
	it, err := m.Trash("财务部")
	if err != nil || !it.IsDir || it.SizeBytes != 3 {
		t.Fatalf("trash folder: %v %+v", err, it)
	}
	if _, err := m.Mkdir("财务部"); err != nil { // someone recreated the name
		t.Fatal(err)
	}
	p, err := m.Restore(it.ID)
	if err != nil || p != "财务部 (已恢复)" {
		t.Fatalf("restore folder: %v %q", err, p)
	}
	if _, err := os.Stat(filepath.Join(root, "财务部 (已恢复)", "2026", "合同", "a.pdf")); err != nil {
		t.Fatal("folder contents must be back")
	}
	it2, _ := m.Trash("人事部/c.png")
	os.RemoveAll(filepath.Join(root, "人事部"))
	if p, err := m.Restore(it2.ID); err != nil || p != "人事部/c.png" {
		t.Fatalf("restore into a deleted parent: %v %q", err, p)
	}
}

func TestRestoreRejectsForgedIDsAndRecords(t *testing.T) {
	m, root := setup(t)
	for _, id := range []string{"../../x", "", "not-a-uuid", strings.Repeat("a", 36)} {
		if _, err := m.Restore(id); err == nil {
			t.Errorf("Restore(%q) must fail", id)
		}
	}
	forged := "11111111-1111-4111-8111-111111111111"
	dir := filepath.Join(root, ".trash", forged)
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "item"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{"id":"`+forged+`","original_path":"../../escape.txt","name":"escape.txt"}`), 0o644)
	if _, err := m.Restore(forged); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a tampered record must not restore outside the root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escape.txt")); err == nil {
		t.Fatal("escape file was created")
	}
}

func TestAbs(t *testing.T) {
	m, root := setup(t)
	got, err := m.Abs("人事部/c.png")
	real, _ := filepath.EvalSymlinks(root)
	if err != nil || got != filepath.Join(real, "人事部", "c.png") {
		t.Fatalf("abs: %v %q", err, got)
	}
	if r, err := m.Abs(""); err != nil || r != real {
		t.Fatalf("root abs: %v %q", err, r)
	}
	if _, err := m.Abs("没有"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}
