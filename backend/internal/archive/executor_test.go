package archive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

type env struct {
	t     *testing.T
	root  string
	spool string
	ex    *Executor
}

func newEnv(t *testing.T) *env {
	t.Helper()
	base := t.TempDir()
	e := &env{t: t, root: filepath.Join(base, "archive"), spool: filepath.Join(base, "spool")}
	if err := os.MkdirAll(e.spool, 0o700); err != nil {
		t.Fatal(err)
	}
	e.ex = &Executor{Root: e.root}
	return e
}

func (e *env) src(name string, content string) Op {
	e.t.Helper()
	p := filepath.Join(e.spool, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		e.t.Fatal(err)
	}
	return Op{FileID: name, SourceStored: p, Sha256: sum([]byte(content))}
}

func (e *env) read(rel string) string {
	e.t.Helper()
	b, err := os.ReadFile(filepath.Join(e.root, filepath.FromSlash(rel)))
	if err != nil {
		e.t.Fatal(err)
	}
	return string(b)
}

func (e *env) exists(rel string) bool {
	_, err := os.Lstat(filepath.Join(e.root, filepath.FromSlash(rel)))
	return err == nil
}

func TestExecutePublishAndSourcePreserved(t *testing.T) {
	e := newEnv(t)
	op := e.src("s1.pdf", "hello")
	op.DestRel = "财务部/合同/2024/a.pdf"
	res := e.ex.Execute(context.Background(), []Op{op})
	if len(res) != 1 || res[0].Status != StatusPublished || res[0].FinalRel != op.DestRel || res[0].Sha256 != op.Sha256 {
		t.Fatalf("%+v", res)
	}
	if e.read(op.DestRel) != "hello" {
		t.Fatal("content")
	}
	if _, err := os.Stat(op.SourceStored); err != nil {
		t.Fatal("source must be preserved")
	}
	if len(res[0].CreatedDirs) != 3 {
		t.Fatalf("created dirs %v", res[0].CreatedDirs)
	}
	if ents, _ := os.ReadDir(filepath.Join(e.root, ".staging")); len(ents) != 0 {
		t.Fatal("staging not cleaned")
	}
	if _, err := os.Stat(filepath.Join(e.root, ".journal")); err != nil {
		t.Fatal("journal dir")
	}
}

func TestIdenticalContentIsIdempotent(t *testing.T) {
	e := newEnv(t)
	op := e.src("s.pdf", "same")
	op.DestRel = "d/a.pdf"
	op.OpID = "op1"
	r1 := e.ex.Execute(context.Background(), []Op{op})
	// Same OpID: journaled result, no redo.
	r2 := e.ex.Execute(context.Background(), []Op{op})
	if r2[0].Status != StatusPublished || r2[0].FinalRel != r1[0].FinalRel {
		t.Fatalf("%+v", r2)
	}
	// New OpID, same content/dest: skipped as identical, no suffix file.
	op.OpID = "op2"
	r3 := e.ex.Execute(context.Background(), []Op{op})
	if r3[0].Status != StatusSkipped || r3[0].FinalRel != "d/a.pdf" || r3[0].DuplicateOf != "d/a.pdf" {
		t.Fatalf("%+v", r3)
	}
	if e.exists("d/a_2.pdf") {
		t.Fatal("unexpected suffix")
	}
	// Reusing OpID with different ops is refused.
	op.DestRel = "d/other.pdf"
	op.OpID = "op1"
	if r := e.ex.Execute(context.Background(), []Op{op}); r[0].Status != StatusFailed {
		t.Fatalf("%+v", r)
	}
}

func TestDifferentContentGetsSuffixNeverOverwrites(t *testing.T) {
	e := newEnv(t)
	a := e.src("a", "AAA")
	a.DestRel = "d/x.pdf"
	b := e.src("b", "BBB")
	b.DestRel = "d/x.pdf"
	c := e.src("c", "CCC")
	c.DestRel = "d/x.pdf"
	res := e.ex.Execute(context.Background(), []Op{a, b, c})
	if res[0].FinalRel != "d/x.pdf" || res[1].FinalRel != "d/x_2.pdf" || res[2].FinalRel != "d/x_3.pdf" {
		t.Fatalf("%+v", res)
	}
	if e.read("d/x.pdf") != "AAA" || e.read("d/x_2.pdf") != "BBB" || e.read("d/x_3.pdf") != "CCC" {
		t.Fatal("content mismatch")
	}
}

func TestPreexistingUserFileNotOverwritten(t *testing.T) {
	e := newEnv(t)
	if err := os.MkdirAll(filepath.Join(e.root, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(e.root, "d", "x.pdf"), []byte("user"), 0o600)
	op := e.src("a", "new")
	op.DestRel = "d/x.pdf"
	res := e.ex.Execute(context.Background(), []Op{op})
	if res[0].Status != StatusPublished || res[0].FinalRel != "d/x_2.pdf" || e.read("d/x.pdf") != "user" {
		t.Fatalf("%+v", res)
	}
}

func TestHashMismatchRejected(t *testing.T) {
	e := newEnv(t)
	op := e.src("a", "content")
	op.Sha256 = sum([]byte("other"))
	op.DestRel = "d/a.pdf"
	res := e.ex.Execute(context.Background(), []Op{op})
	if res[0].Status != StatusFailed || !strings.Contains(res[0].Err, "SHA-256") {
		t.Fatalf("%+v", res)
	}
	if e.exists("d/a.pdf") {
		t.Fatal("must not publish")
	}
	if ents, _ := os.ReadDir(filepath.Join(e.root, ".staging")); len(ents) != 0 {
		t.Fatal("staging leftover")
	}
	op.Sha256 = ""
	if r := e.ex.Execute(context.Background(), []Op{op}); r[0].Status != StatusFailed {
		t.Fatal("missing hash must fail")
	}
}

func TestPathTraversalRejected(t *testing.T) {
	e := newEnv(t)
	for i, bad := range []string{"../x.pdf", `..\x.pdf`, "/abs/x.pdf", "C:/x.pdf", `\\srv\share\x.pdf`, "a/../../x.pdf", ".staging/x.pdf", ".journal/x.json"} {
		op := e.src("s"+string(rune('a'+i)), "x")
		op.DestRel = bad
		res := e.ex.Execute(context.Background(), []Op{op})
		if res[0].Status != StatusFailed {
			t.Errorf("%q accepted: %+v", bad, res)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(e.root), "x.pdf")); err == nil {
		t.Fatal("escaped root")
	}
}

func TestSymlinkEscapeAndSymlinkSource(t *testing.T) {
	e := newEnv(t)
	outside := filepath.Join(filepath.Dir(e.root), "outside")
	os.MkdirAll(outside, 0o755)
	os.MkdirAll(e.root, 0o755)
	if err := os.Symlink(outside, filepath.Join(e.root, "link")); err != nil {
		t.Skipf("symlinks cannot be created on this filesystem/privilege level: %v (symlink escape tests skipped)", err)
	}
	op := e.src("a", "data")
	op.DestRel = "link/x.pdf"
	res := e.ex.Execute(context.Background(), []Op{op})
	if res[0].Status != StatusFailed {
		t.Fatalf("escape accepted: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(outside, "x.pdf")); err == nil {
		t.Fatal("file written outside root")
	}
	// symlinked source
	real := e.src("real", "data")
	ln := filepath.Join(e.spool, "ln")
	if err := os.Symlink(real.SourceStored, ln); err != nil {
		t.Skip(err)
	}
	op2 := Op{FileID: "ln", SourceStored: ln, Sha256: real.Sha256, DestRel: "d/y.pdf"}
	if r := e.ex.Execute(context.Background(), []Op{op2}); r[0].Status != StatusFailed {
		t.Fatalf("symlink source accepted: %+v", r)
	}
}

func TestMissingSourceFails(t *testing.T) {
	e := newEnv(t)
	op := Op{FileID: "x", SourceStored: filepath.Join(e.spool, "nope"), Sha256: sum(nil), DestRel: "d/a.pdf"}
	if r := e.ex.Execute(context.Background(), []Op{op}); r[0].Status != StatusFailed {
		t.Fatal(r)
	}
	op.SourceStored = "relative/path"
	if r := e.ex.Execute(context.Background(), []Op{op}); r[0].Status != StatusFailed {
		t.Fatal(r)
	}
}

func TestRecoverRemovesStaging(t *testing.T) {
	e := newEnv(t)
	op := e.src("a", "x")
	op.DestRel = "d/a.pdf"
	e.ex.Execute(context.Background(), []Op{op})
	orphan := filepath.Join(e.root, ".staging", "dead-beef")
	os.WriteFile(orphan, []byte("partial"), 0o600)
	tmp := filepath.Join(e.root, "d", tmpPrefix+"orphan")
	os.WriteFile(tmp, []byte("partial"), 0o600)
	n, err := e.ex.Recover(context.Background())
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if _, err := os.Stat(orphan); err == nil {
		t.Fatal("orphan remains")
	}
	if e.read("d/a.pdf") != "x" {
		t.Fatal("published file must survive")
	}
}

func TestCrashResumeAdoptsPublishedIntent(t *testing.T) {
	e := newEnv(t)
	op := e.src("a", "x")
	op.DestRel = "d/a.pdf"
	op.OpID = "crash1"
	res := e.ex.Execute(context.Background(), []Op{op})
	// Simulate crash after publish but before the result was journaled: pending intent.
	realRoot, _ := filepath.EvalSymlinks(e.root)
	jp := e.ex.journalPath(realRoot, "crash1")
	j, err := loadJournal(jp)
	if err != nil {
		t.Fatal(err)
	}
	j.State = stateRunning
	j.Results[0].Status = statusPending
	if err = saveJournal(jp, j); err != nil {
		t.Fatal(err)
	}
	res2 := e.ex.Execute(context.Background(), []Op{op})
	if res2[0].Status != StatusPublished || res2[0].FinalRel != res[0].FinalRel || e.exists("d/a_2.pdf") {
		t.Fatalf("%+v", res2)
	}
	if err := e.ex.Rollback(context.Background(), res2); err != nil {
		t.Fatal(err)
	}
	if e.exists("d/a.pdf") || e.exists("d") {
		t.Fatal("rollback after resume should remove file and dir")
	}
}

func TestRollbackRemovesOnlyOwnUnmodified(t *testing.T) {
	e := newEnv(t)
	a := e.src("a", "AAA")
	a.DestRel = "新/d/a.pdf"
	b := e.src("b", "BBB")
	b.DestRel = "新/d/b.pdf"
	res := e.ex.Execute(context.Background(), []Op{a, b})
	os.WriteFile(filepath.Join(e.root, "新", "d", "b.pdf"), []byte("edited"), 0o600)
	err := e.ex.Rollback(context.Background(), res)
	if err == nil || !strings.Contains(err.Error(), "已被修改") {
		t.Fatalf("want modified error, got %v", err)
	}
	if e.exists("新/d/a.pdf") {
		t.Fatal("unmodified file should be removed")
	}
	if e.read("新/d/b.pdf") != "edited" {
		t.Fatal("modified file must be kept")
	}
	if !e.exists("新/d") {
		t.Fatal("non-empty dir must be kept")
	}
	// Remaining: clean rollback prunes dirs.
	os.Remove(filepath.Join(e.root, "新", "d", "b.pdf"))
	if err := e.ex.Rollback(context.Background(), res); err != nil {
		t.Fatal(err)
	}
	if e.exists("新") {
		t.Fatal("created dirs should be pruned")
	}
	if _, err := os.Stat(e.root); err != nil {
		t.Fatal("root must remain")
	}
	// After rollback the same OpID can be re-executed.
	again := e.ex.Execute(context.Background(), []Op{a, b})
	if again[0].Status != StatusPublished || again[1].Status != StatusPublished {
		t.Fatalf("%+v", again)
	}
}

func TestRollbackDoesNotTouchSkippedOrPreexisting(t *testing.T) {
	e := newEnv(t)
	op := e.src("a", "same")
	op.DestRel = "d/a.pdf"
	op.OpID = "first"
	e.ex.Execute(context.Background(), []Op{op})
	op.OpID = "second"
	res := e.ex.Execute(context.Background(), []Op{op})
	if res[0].Status != StatusSkipped {
		t.Fatal(res)
	}
	if err := e.ex.Rollback(context.Background(), res); err != nil {
		t.Fatal(err)
	}
	if e.read("d/a.pdf") != "same" {
		t.Fatal("skipped file must not be deleted")
	}
}

func TestRollbackRejectsForgedPaths(t *testing.T) {
	e := newEnv(t)
	victim := filepath.Join(filepath.Dir(e.root), "victim.txt")
	os.WriteFile(victim, []byte("v"), 0o600)
	os.MkdirAll(e.root, 0o755)
	err := e.ex.Rollback(context.Background(), []OpResult{{Status: StatusPublished, FinalRel: "../victim.txt", Sha256: sum([]byte("v"))}})
	if err == nil {
		t.Fatal("want error")
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatal("victim deleted")
	}
}

func TestContextCancelledMidway(t *testing.T) {
	e := newEnv(t)
	var ops []Op
	for i := 0; i < 4; i++ {
		o := e.src("f"+string(rune('0'+i)), "c"+string(rune('0'+i)))
		o.DestRel = "d/f" + string(rune('0'+i)) + ".pdf"
		o.OpID = "cancel1"
		ops = append(ops, o)
	}
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	orig := linkFile
	linkFile = func(a, b string) error {
		err := orig(a, b)
		if calls++; calls == 2 {
			cancel()
		}
		return err
	}
	res := e.ex.Execute(ctx, ops)
	linkFile = orig
	if res[0].Status != StatusPublished || res[1].Status != StatusPublished || res[2].Status != StatusFailed || res[3].Status != StatusFailed {
		t.Fatalf("%+v", res)
	}
	if !errors.Is(context.Canceled, context.Canceled) || !strings.Contains(res[2].Err, "canceled") {
		t.Fatal(res[2].Err)
	}
	if e.exists("d/f2.pdf") {
		t.Fatal("cancelled op must not publish")
	}
	// Resume with the same OpID finishes the rest and keeps the first two.
	res = e.ex.Execute(context.Background(), ops)
	for i, r := range res {
		if r.Status != StatusPublished || r.FinalRel != ops[i].DestRel {
			t.Fatalf("resume %d: %+v", i, r)
		}
	}
	if ents, _ := os.ReadDir(filepath.Join(e.root, ".staging")); len(ents) != 0 {
		t.Fatal("staging leftover")
	}
	// Already-cancelled context does nothing.
	ctx2, c2 := context.WithCancel(context.Background())
	c2()
	o := e.src("z", "zz")
	o.DestRel = "d/z.pdf"
	if r := e.ex.Execute(ctx2, []Op{o}); r[0].Status != StatusFailed || e.exists("d/z.pdf") {
		t.Fatalf("%+v", r)
	}
}

func TestConcurrentSameDestination(t *testing.T) {
	e := newEnv(t)
	const n = 12
	var wg sync.WaitGroup
	results := make([]OpResult, n)
	contents := make([]string, n)
	for i := 0; i < n; i++ {
		contents[i] = "content-" + string(rune('A'+i))
	}
	ops := make([]Op, n)
	for i := 0; i < n; i++ {
		ops[i] = e.src("c"+string(rune('A'+i)), contents[i])
		ops[i].DestRel = "d/same.pdf"
	}
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// two independent Executors on the same root, as separate callers would
			ex := &Executor{Root: e.root}
			results[i] = ex.Execute(context.Background(), []Op{ops[i]})[0]
		}(i)
	}
	wg.Wait()
	seen := map[string]bool{}
	for i, r := range results {
		if r.Status != StatusPublished {
			t.Fatalf("%d: %+v", i, r)
		}
		if seen[strings.ToLower(r.FinalRel)] {
			t.Fatalf("duplicate destination %s", r.FinalRel)
		}
		seen[strings.ToLower(r.FinalRel)] = true
		if e.read(r.FinalRel) != contents[i] {
			t.Fatalf("content of %s was overwritten", r.FinalRel)
		}
	}
}

func TestConcurrentIdenticalContent(t *testing.T) {
	e := newEnv(t)
	op := e.src("a", "dup")
	op.DestRel = "d/a.pdf"
	var wg sync.WaitGroup
	res := make([]OpResult, 8)
	for i := range res {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			o := op
			o.OpID = "id" + string(rune('a'+i))
			res[i] = e.ex.Execute(context.Background(), []Op{o})[0]
		}(i)
	}
	wg.Wait()
	pub := 0
	for _, r := range res {
		if r.Status == StatusPublished {
			pub++
		} else if r.Status != StatusSkipped {
			t.Fatalf("%+v", r)
		}
	}
	if pub != 1 || e.exists("d/a_2.pdf") {
		t.Fatalf("published=%d", pub)
	}
}

func TestFallbackWhenHardLinkUnsupported(t *testing.T) {
	e := newEnv(t)
	orig := linkFile
	linkFile = func(a, b string) error { return errors.New("links unsupported") }
	defer func() { linkFile = orig }()
	a := e.src("a", "AAA")
	a.DestRel = "d/x.pdf"
	b := e.src("b", "BBB")
	b.DestRel = "d/x.pdf"
	res := e.ex.Execute(context.Background(), []Op{a, b})
	if res[0].FinalRel != "d/x.pdf" || res[1].FinalRel != "d/x_2.pdf" || e.read("d/x.pdf") != "AAA" || e.read("d/x_2.pdf") != "BBB" {
		t.Fatalf("%+v", res)
	}
}

func TestPublishNoReplaceCopyFallback(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	os.WriteFile(src, []byte("s"), 0o600)
	orig := linkFile
	linkFile = func(a, b string) error { return errors.New("nope") }
	defer func() { linkFile = orig }()
	if err := publishNoReplace(context.Background(), src, dst); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(src, []byte("t"), 0o600)
	if err := publishNoReplace(context.Background(), src, dst); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("want ErrExist, got %v", err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "s" {
		t.Fatal("overwritten")
	}
}

func TestJournalWritten(t *testing.T) {
	e := newEnv(t)
	op := e.src("a", "x")
	op.DestRel = "d/a.pdf"
	op.OpID = "jr-1"
	e.ex.Execute(context.Background(), []Op{op})
	b, err := os.ReadFile(filepath.Join(e.root, ".journal", "jr-1.json"))
	if err != nil || !strings.Contains(string(b), `"state": "done"`) || !strings.Contains(string(b), "d/a.pdf") {
		t.Fatalf("%v %s", err, b)
	}
	bad := op
	bad.OpID = "../evil"
	if r := e.ex.Execute(context.Background(), []Op{bad}); r[0].Status != StatusFailed {
		t.Fatal("bad OpID accepted")
	}
}

func TestSameNameOtherContentKeepsAutoSuffixAndReportsNoDuplicate(t *testing.T) {
	e := newEnv(t)
	a := e.src("a", "first")
	a.DestRel, a.OpID = "d/a.pdf", "o1"
	e.ex.Execute(context.Background(), []Op{a})
	b := e.src("b", "second")
	b.DestRel, b.OpID = "d/a.pdf", "o2"
	r := e.ex.Execute(context.Background(), []Op{b})
	if r[0].Status != StatusPublished || r[0].FinalRel != "d/a_2.pdf" || r[0].DuplicateOf != "" || !e.exists("d/a.pdf") {
		t.Fatalf("different content must never overwrite and is not a duplicate: %+v", r[0])
	}
}
