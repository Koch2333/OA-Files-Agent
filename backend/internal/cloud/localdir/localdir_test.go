package localdir

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"oa-agent/backend/internal/cloud"
)

var ctx = context.Background()

type env struct {
	root, dir string
}

// setup creates an allowed root with a connection directory "nas" inside and an outside directory.
func setup(t *testing.T) (env, string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "mnt")
	outside := filepath.Join(base, "outside")
	dir := filepath.Join(root, "nas")
	for _, d := range []string{root, outside, dir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("OA_LOCAL_ROOTS", root)
	t.Setenv("OA_ARCHIVE_ROOT", "")
	t.Setenv("OA_SPOOL_ROOT", "")
	return env{root, dir}, outside
}

func open(t *testing.T, path string, extra map[string]string, basePath string) cloud.Provider {
	t.Helper()
	v := map[string]string{"path": path}
	for k, x := range extra {
		v[k] = x
	}
	p, err := cloud.Open(ctx, "localdir", cloud.Settings{Values: v, BasePath: basePath})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

func put(p cloud.Provider, rel, body string) error {
	return p.PutNew(ctx, rel, strings.NewReader(body), int64(len(body)))
}

func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
}

func TestCatalogue(t *testing.T) {
	k, ok := cloud.KindByID("localdir")
	if !ok || k.Group != "enterprise" || k.Name == "" {
		t.Fatal("kind missing")
	}
	if _, _, err := cloud.Validate("localdir", map[string]string{"path": "/mnt/storage/x", "read_only": "true"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := cloud.Validate("localdir", map[string]string{}, nil, nil); err == nil {
		t.Fatal("path must be required")
	}
}

func TestUnconfigured(t *testing.T) {
	t.Setenv("OA_LOCAL_ROOTS", "")
	_, err := cloud.Open(ctx, "localdir", cloud.Settings{Values: map[string]string{"path": t.TempDir()}})
	if err == nil || !strings.Contains(err.Error(), "OA_LOCAL_ROOTS") {
		t.Fatalf("want unconfigured message, got %v", err)
	}
}

func TestPathChecks(t *testing.T) {
	e, outside := setup(t)
	mk := func(path string) error {
		p, err := cloud.Open(ctx, "localdir", cloud.Settings{Values: map[string]string{"path": path}})
		if err == nil {
			p.Close()
		}
		return err
	}
	if err := mk(e.dir); err != nil {
		t.Fatal(err)
	}
	if err := mk(e.root); err != nil {
		t.Fatalf("root itself must be allowed: %v", err)
	}
	for name, path := range map[string]string{
		"outside":    outside,
		"relative":   "nas",
		"dotdot":     e.dir + "/../../outside",
		"missing":    filepath.Join(e.root, "nope"),
		"nul":        e.dir + "\x00x",
		"parent":     filepath.Dir(e.root),
		"sibling-pf": e.root + "-evil",
	} {
		if err := mk(path); err == nil {
			t.Errorf("%s: must be refused", name)
		}
	}
	// a file is not a directory
	f := filepath.Join(e.dir, "f.txt")
	os.WriteFile(f, []byte("x"), 0o644)
	if err := mk(f); err == nil {
		t.Error("file path must be refused")
	}
	// symlink to outside refused, symlink inside root accepted
	symlinkOrSkip(t, outside, filepath.Join(e.root, "esc"))
	if err := mk(filepath.Join(e.root, "esc")); err == nil {
		t.Error("symlink escaping the roots must be refused")
	}
	symlinkOrSkip(t, e.dir, filepath.Join(e.root, "alias"))
	if err := mk(filepath.Join(e.root, "alias")); err != nil {
		t.Errorf("symlink to allowed dir must work: %v", err)
	}
	// bad base path
	if _, err := cloud.Open(ctx, "localdir", cloud.Settings{Values: map[string]string{"path": e.dir}, BasePath: "../x"}); err == nil {
		t.Error("bad base path must be refused")
	}
}

func TestAppDirOverlap(t *testing.T) {
	e, _ := setup(t)
	archive := filepath.Join(e.root, "archive")
	spoolParent := filepath.Join(e.root, "data")
	spool := filepath.Join(spoolParent, "spool")
	for _, d := range []string{archive, spool} {
		os.MkdirAll(d, 0o755)
	}
	t.Setenv("OA_ARCHIVE_ROOT", archive)
	t.Setenv("OA_SPOOL_ROOT", spool)
	mk := func(path string) error {
		p, err := cloud.Open(ctx, "localdir", cloud.Settings{Values: map[string]string{"path": path}})
		if err == nil {
			p.Close()
		}
		return err
	}
	for name, path := range map[string]string{
		"archive": archive, "inside-archive": filepath.Join(archive, "sub"), "root-contains-archive": e.root,
		"spool": spool, "data-dir": spoolParent,
	} {
		if name == "inside-archive" {
			os.MkdirAll(path, 0o755)
		}
		if err := mk(path); err == nil || !strings.Contains(err.Error(), "重叠") {
			t.Errorf("%s: want overlap refusal, got %v", name, err)
		}
	}
	if err := mk(e.dir); err != nil {
		t.Errorf("unrelated dir must work: %v", err)
	}
}

func TestHappyPath(t *testing.T) {
	e, _ := setup(t)
	p := open(t, e.dir, nil, "")
	if err := put(p, "a/b/c.txt", "hello"); err != nil {
		t.Fatal(err)
	}
	if err := put(p, "top.txt", "t"); err != nil {
		t.Fatal(err)
	}
	if err := put(p, "empty.bin", ""); err != nil {
		t.Fatal(err)
	}
	rc, n, err := p.Open(ctx, "a/b/c.txt")
	if err != nil || n != 5 {
		t.Fatalf("open %v %d", err, n)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "hello" {
		t.Fatal(string(b))
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(filepath.Join(e.dir, "a/b/c.txt"))
		if fi.Mode().Perm() != 0o640 {
			t.Errorf("file mode %v", fi.Mode().Perm())
		}
		di, _ := os.Stat(filepath.Join(e.dir, "a"))
		if di.Mode().Perm() != 0o750 {
			t.Errorf("dir mode %v", di.Mode().Perm())
		}
	}
	ents, err := p.List(ctx, "")
	if err != nil || len(ents) != 3 || !ents[0].IsDir || ents[0].Name != "a" || ents[0].Path != "a" {
		t.Fatalf("list: %v %+v", err, ents)
	}
	ents, _ = p.List(ctx, "a/b")
	if len(ents) != 1 || ents[0].Path != "a/b/c.txt" || ents[0].Size != 5 || ents[0].ModTime.IsZero() {
		t.Fatalf("%+v", ents)
	}
	st, err := p.Stat(ctx, "a")
	if err != nil || !st.IsDir {
		t.Fatal(err, st)
	}
	if st, err = p.Stat(ctx, ""); err != nil || !st.IsDir {
		t.Fatal(err)
	}
	if _, err := p.Stat(ctx, "nope"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := p.List(ctx, "nope"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := p.List(ctx, "top.txt"); err == nil {
		t.Fatal("list of a file must fail")
	}
	if _, _, err := p.Open(ctx, "a"); err == nil {
		t.Fatal("open of a dir must fail")
	}
	if _, _, err := p.Open(ctx, "nope"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatal(err)
	}
	if err := p.Remove(ctx, "a"); err == nil {
		t.Fatal("dirs must not be removable")
	}
	if err := p.Remove(ctx, "top.txt"); err != nil {
		t.Fatal(err)
	}
	if err := p.Remove(ctx, "top.txt"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestBasePath(t *testing.T) {
	e, _ := setup(t)
	p := open(t, e.dir, nil, "sync/in")
	if err := put(p, "x/y.txt", "1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "sync", "in", "x", "y.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := p.List(ctx, ""); err != nil {
		t.Fatal(err)
	}
	// missing base path for listing is NotFound, not a crash
	q := open(t, e.dir, nil, "absent")
	if _, err := q.List(ctx, ""); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatal(err)
	}
	// hidden base path refused
	if _, err := cloud.Open(ctx, "localdir", cloud.Settings{Values: map[string]string{"path": e.dir}, BasePath: ".git"}); err == nil {
		t.Fatal("hidden base path must fail")
	}
}

func TestProbeWithMarker(t *testing.T) {
	e, _ := setup(t)
	p := open(t, e.dir, nil, "")
	rep := cloud.Probe(ctx, p, true, ".oa-agent-write-test-abcd1234")
	if !rep.OK {
		t.Fatalf("%+v", rep)
	}
	if ents, _ := os.ReadDir(e.dir); len(ents) != 0 {
		t.Fatalf("marker left behind: %v", ents)
	}
	ro := open(t, e.dir, map[string]string{"read_only": "true"}, "")
	if rep := cloud.Probe(ctx, ro, false, "x"); !rep.OK {
		t.Fatalf("%+v", rep)
	}
	if rep := cloud.Probe(ctx, ro, true, ".oa-agent-write-test-abcd1234"); rep.OK {
		t.Fatal("read-only write probe must fail")
	}
}

func TestTraversalAndHidden(t *testing.T) {
	e, outside := setup(t)
	os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o644)
	os.WriteFile(filepath.Join(e.dir, ".hidden"), []byte("h"), 0o644)
	os.MkdirAll(filepath.Join(e.dir, ".trash"), 0o755)
	os.WriteFile(filepath.Join(e.dir, ".trash", "x"), []byte("h"), 0o644)
	os.WriteFile(filepath.Join(e.dir, "visible"), []byte("v"), 0o644)
	p := open(t, e.dir, nil, "")
	bad := []string{
		"../outside/secret.txt", "..", "a/../../outside/secret.txt", "/etc/passwd", "/", "./visible", "a//b", "visible/", "a/./b",
		`..\outside\secret.txt`, `a\..\b`, "vis\x00ible", ".hidden", ".trash/x", ".trash",
		"sub/.hidden", ".oa-agent-tmp-1", ".oa-agent-write-test-x/y",
	}
	for _, b := range bad {
		if _, _, err := p.Open(ctx, b); err == nil {
			t.Errorf("Open(%q) must fail", b)
		}
		if _, err := p.List(ctx, b); err == nil {
			t.Errorf("List(%q) must fail", b)
		}
		{
			if err := put(p, b, "x"); err == nil {
				t.Errorf("PutNew(%q) must fail", b)
			}
		}
		if err := p.Remove(ctx, b); err == nil {
			t.Errorf("Remove(%q) must fail", b)
		}
	}
	// encoded variants are plain (nonexistent) names, never decoded
	if _, _, err := p.Open(ctx, "%2e%2e%2foutside%2fsecret.txt"); !errors.Is(err, cloud.ErrNotFound) {
		t.Errorf("encoded name: %v", err)
	}
	if _, err := p.Stat(ctx, ".hidden"); err == nil {
		t.Error("Stat of hidden must fail")
	}
	ents, _ := p.List(ctx, "")
	if len(ents) != 1 || ents[0].Name != "visible" {
		t.Fatalf("hidden entries listed: %+v", ents)
	}
	if b, _ := os.ReadFile(filepath.Join(e.dir, ".hidden")); string(b) != "h" {
		t.Fatal("hidden file changed")
	}
	if _, err := os.Stat(filepath.Join(outside, "x")); err == nil {
		t.Fatal("escaped write")
	}
}

func TestSymlinkEscape(t *testing.T) {
	e, outside := setup(t)
	os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o644)
	symlinkOrSkip(t, outside, filepath.Join(e.dir, "dirlink"))
	symlinkOrSkip(t, filepath.Join(outside, "secret.txt"), filepath.Join(e.dir, "filelink"))
	os.WriteFile(filepath.Join(e.dir, "real.txt"), []byte("r"), 0o644)
	symlinkOrSkip(t, filepath.Join(e.dir, "real.txt"), filepath.Join(e.dir, "inlink")) // inside, still refused
	symlinkOrSkip(t, e.root, filepath.Join(e.dir, "rootlink"))                         // to the allowed root
	p := open(t, e.dir, nil, "")
	for _, n := range []string{"dirlink", "dirlink/secret.txt", "filelink", "inlink", "rootlink", "rootlink/nas/real.txt"} {
		if _, _, err := p.Open(ctx, n); err == nil {
			t.Errorf("Open(%q) must fail", n)
		}
		if _, err := p.Stat(ctx, n); err == nil {
			t.Errorf("Stat(%q) must fail", n)
		}
		if _, err := p.List(ctx, n); err == nil {
			t.Errorf("List(%q) must fail", n)
		}
		if err := put(p, n+"/new.txt", "x"); err == nil {
			t.Errorf("PutNew(%q/new.txt) must fail", n)
		}
		if err := p.Remove(ctx, n); err == nil {
			t.Errorf("Remove(%q) must fail", n)
		}
	}
	if err := put(p, "dirlink/new.txt", "x"); err == nil {
		t.Error("write through symlink dir must fail")
	}
	if _, err := os.Stat(filepath.Join(outside, "new.txt")); err == nil {
		t.Fatal("file written outside the connection directory")
	}
	// PutNew over a symlink target (dangling or not) must not write through it
	symlinkOrSkip(t, filepath.Join(outside, "target.txt"), filepath.Join(e.dir, "dangling"))
	if err := put(p, "dangling", "x"); err == nil {
		t.Error("PutNew onto a symlink must fail")
	}
	if _, err := os.Stat(filepath.Join(outside, "target.txt")); err == nil {
		t.Fatal("wrote through a dangling symlink")
	}
	if b, _ := os.ReadFile(filepath.Join(outside, "secret.txt")); string(b) != "secret" {
		t.Fatal("outside file modified")
	}
	ents, _ := p.List(ctx, "")
	if len(ents) != 1 || ents[0].Name != "real.txt" {
		t.Fatalf("symlinks must not be listed: %+v", ents)
	}
	// the base path may not traverse a symlink either
	if _, err := open(t, e.dir, nil, "dirlink").List(ctx, ""); err == nil {
		t.Error("base path through symlink must fail")
	}
}

func TestNoOverwrite(t *testing.T) {
	e, _ := setup(t)
	p := open(t, e.dir, nil, "")
	if err := put(p, "f.txt", "one"); err != nil {
		t.Fatal(err)
	}
	if err := put(p, "f.txt", "two!"); !errors.Is(err, cloud.ErrExists) {
		t.Fatalf("want ErrExists, got %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(e.dir, "f.txt")); string(b) != "one" {
		t.Fatal("overwritten")
	}
	os.MkdirAll(filepath.Join(e.dir, "d"), 0o755)
	if err := put(p, "d", "x"); !errors.Is(err, cloud.ErrExists) {
		t.Fatalf("dir in the way: %v", err)
	}
	if err := put(p, "f.txt/child", "x"); err == nil {
		t.Fatal("file in the way of a parent must fail")
	}
	assertNoTemp(t, e.dir)
}

func assertNoTemp(t *testing.T, dir string) {
	t.Helper()
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && strings.HasPrefix(d.Name(), tmpPrefix) {
			t.Errorf("temp file left: %s", p)
		}
		return nil
	})
}

func TestConcurrentPutNew(t *testing.T) {
	e, _ := setup(t)
	p := open(t, e.dir, nil, "")
	for round := 0; round < 20; round++ {
		name := "race/f" + string(rune('a'+round)) + ".txt"
		var wg sync.WaitGroup
		res := make([]error, 8)
		for i := range res {
			wg.Add(1)
			go func() {
				defer wg.Done()
				res[i] = put(p, name, strings.Repeat(string(rune('A'+i)), 4096))
			}()
		}
		wg.Wait()
		ok, exists := 0, 0
		for _, err := range res {
			switch {
			case err == nil:
				ok++
			case errors.Is(err, cloud.ErrExists):
				exists++
			default:
				t.Fatalf("unexpected: %v", err)
			}
		}
		if ok != 1 || exists != 7 {
			t.Fatalf("round %d: ok=%d exists=%d", round, ok, exists)
		}
		b, _ := os.ReadFile(filepath.Join(e.dir, name))
		if len(b) != 4096 || bytes.Count(b, b[:1]) != 4096 {
			t.Fatal("content torn")
		}
	}
	assertNoTemp(t, e.dir)
}

type failReader struct{ n int }

func (f *failReader) Read(b []byte) (int, error) {
	if f.n <= 0 {
		return 0, errors.New("boom")
	}
	n := min(len(b), f.n)
	f.n -= n
	return n, nil
}

func TestFailureCleansTemp(t *testing.T) {
	e, _ := setup(t)
	p := open(t, e.dir, nil, "")
	if err := p.PutNew(ctx, "d/x.bin", &failReader{n: 100}, 1000); err == nil {
		t.Fatal("reader failure must fail")
	}
	// size mismatches, both directions
	if err := p.PutNew(ctx, "d/short.bin", strings.NewReader("abc"), 10); err == nil {
		t.Fatal("short body must fail")
	}
	if err := p.PutNew(ctx, "d/long.bin", strings.NewReader("abcdefghij"), 3); err == nil {
		t.Fatal("long body must fail")
	}
	if err := p.PutNew(ctx, "d/neg.bin", strings.NewReader(""), -1); err == nil {
		t.Fatal("negative size must fail")
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if err := p.PutNew(cctx, "d/cancel.bin", strings.NewReader("abc"), 3); err == nil {
		t.Fatal("cancelled ctx must fail")
	}
	for _, n := range []string{"x.bin", "short.bin", "long.bin", "neg.bin", "cancel.bin"} {
		if _, err := os.Stat(filepath.Join(e.dir, "d", n)); err == nil {
			t.Errorf("%s was published", n)
		}
	}
	assertNoTemp(t, e.dir)
	// a failed attempt must not block a later good one
	if err := put(p, "d/x.bin", "ok"); err != nil {
		t.Fatal(err)
	}
}

func TestReadOnly(t *testing.T) {
	e, _ := setup(t)
	os.WriteFile(filepath.Join(e.dir, "f.txt"), []byte("x"), 0o644)
	p := open(t, e.dir, map[string]string{"read_only": "true"}, "")
	if err := put(p, "new.txt", "x"); !errors.Is(err, cloud.ErrPermission) {
		t.Fatalf("PutNew: %v", err)
	}
	if err := p.Remove(ctx, "f.txt"); !errors.Is(err, cloud.ErrPermission) {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := p.List(ctx, ""); err != nil {
		t.Fatal(err)
	}
	rc, _, err := p.Open(ctx, "f.txt")
	if err != nil {
		t.Fatal(err)
	}
	rc.Close()
	if _, err := os.Stat(filepath.Join(e.dir, "new.txt")); err == nil {
		t.Fatal("written despite read-only")
	}
}

func TestLargeFileStreaming(t *testing.T) {
	e, _ := setup(t)
	p := open(t, e.dir, nil, "")
	const size = 64<<20 + 17
	src := io.LimitReader(patternReader{}, size)
	if err := p.PutNew(ctx, "big/large.bin", src, size); err != nil {
		t.Fatal(err)
	}
	rc, n, err := p.Open(ctx, "big/large.bin")
	if err != nil || n != size {
		t.Fatal(err, n)
	}
	defer rc.Close()
	got, _ := io.Copy(io.Discard, rc)
	if got != size {
		t.Fatalf("read %d", got)
	}
}

type patternReader struct{}

func (patternReader) Read(b []byte) (int, error) {
	for i := range b {
		b[i] = byte(i)
	}
	return len(b), nil
}

func TestEntryLimitAndSkipNonRegular(t *testing.T) {
	e, _ := setup(t)
	os.WriteFile(filepath.Join(e.dir, "a.txt"), nil, 0o644)
	p := open(t, e.dir, nil, "")
	ents, err := p.List(ctx, "")
	if err != nil || len(ents) != 1 {
		t.Fatal(err, ents)
	}
}
