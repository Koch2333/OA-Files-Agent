package diag

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFingerprintGroupsSameBug(t *testing.T) {
	a1, id1 := Fingerprint("api", "INTERNAL", "open /data/archive/2024/a.docx: permission denied (task 12)", "")
	a2, id2 := Fingerprint("api", "INTERNAL", "open C:\\data\\archive\\2025\\b.docx: permission denied (task 99)", "")
	if a1 != a2 || id1 != id2 {
		t.Fatal("paths and numbers must be normalised")
	}
	b1, _ := Fingerprint("api", "INTERNAL", "job 1b9d6bcd-bbfd-4b2d-9b5d-ab8dfbbd4bed failed at 2026-10-22T10:11:12Z", "")
	b2, _ := Fingerprint("api", "INTERNAL", "job 0aaaaaaa-bbfd-4b2d-9b5d-ab8dfbbd4bed failed at 2027-01-01 01:02:03", "")
	if b1 != b2 {
		t.Fatal("ids and timestamps must be normalised")
	}
	if c, _ := Fingerprint("pipeline", "INTERNAL", "open /x/y: permission denied", ""); c == a1 {
		t.Fatal("source must matter")
	}
	if c, _ := Fingerprint("api", "OTHER", "open /x/y: permission denied", ""); c == a1 {
		t.Fatal("code must matter")
	}
	if c, _ := Fingerprint("api", "INTERNAL", "connection refused", ""); c == a1 {
		t.Fatal("message must matter")
	}
	if len(id1) != 36 || id1[8] != '-' {
		t.Fatalf("id %q", id1)
	}
}

func TestFingerprintStackFrames(t *testing.T) {
	st := func(line string, addr string) string {
		return "goroutine 7 [running]:\nruntime/debug.Stack()\n\t/go/src/runtime/debug/stack.go:26 +0x5e\noa-agent/backend/internal/diag.Safe.func1()\n\t/x/diag.go:1 +0x1\npanic({0x1, 0x2})\n\t/go/src/runtime/panic.go:1 +0x1\noa-agent/backend/internal/pipeline.(*Service).process(" + addr + ")\n\t/x/service.go:" + line + " +0x1d\noa-agent/backend/internal/pipeline.(*Service).loop(0xc0000)\n\t/x/service.go:60 +0x1\n"
	}
	f1, _ := Fingerprint("pipeline", "PANIC", "boom", st("123", "0xc000123"))
	f2, _ := Fingerprint("pipeline", "PANIC", "boom", st("456", "0xc000999"))
	if f1 != f2 {
		t.Fatal("line numbers and addresses must not change the fingerprint")
	}
	fr := TopFrames(st("1", "0x1"), 3)
	if len(fr) != 2 || !strings.Contains(fr[0], "process") {
		t.Fatalf("frames %v", fr)
	}
	f3, _ := Fingerprint("pipeline", "PANIC", "boom", strings.Replace(st("1", "0x1"), "process", "analyzeOne", 1))
	if f3 == f1 {
		t.Fatal("different top frame must differ")
	}
}

func TestFingerprintMinifiedJS(t *testing.T) {
	a := "TypeError: x is undefined\n    at Object.run (https://host.example/assets/index-Ab12Cd34.js:1:23456)\n    at n (https://host.example/assets/vendor-Zz99Yy88.js:2:99)"
	b := "TypeError: x is undefined\n    at Object.run (http://other:8080/assets/index-Qq77Ww66.js:9:111)\n    at n (http://other:8080/assets/vendor-Hh11Jj22.js:5:7)"
	f1, _ := Fingerprint("client", "CLIENT", "TypeError: x is undefined", a)
	f2, _ := Fingerprint("client", "CLIENT", "TypeError: x is undefined", b)
	if f1 != f2 {
		t.Fatalf("bundle hashes/line numbers leaked into fingerprint: %v vs %v", TopFrames(a, 3), TopFrames(b, 3))
	}
}

type fakeSink struct {
	mu    sync.Mutex
	fail  atomic.Bool
	got   map[string]int
	calls int
}

func (f *fakeSink) write(ctx context.Context, a *agg) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.fail.Load() {
		return errors.New("db down")
	}
	if f.got == nil {
		f.got = map[string]int{}
	}
	f.got[a.rec.fp] += a.count
	return nil
}

func (f *fakeSink) total() (n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.got {
		n += c
	}
	return
}

func newTestReporter(t *testing.T, o Options) *Reporter {
	t.Helper()
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
	o.FlushEvery = 10 * time.Millisecond
	r := New(o)
	t.Cleanup(r.Close)
	return r
}

func TestReporterCoalescesAndRedacts(t *testing.T) {
	var lines []string
	var mu sync.Mutex
	r := newTestReporter(t, Options{Logf: func(f string, a ...any) { mu.Lock(); lines = append(lines, strings.TrimSpace(f)); mu.Unlock() }})
	sk := &fakeSink{}
	r.setSink(sk)
	for i := 0; i < 50; i++ {
		r.Report(context.Background(), Entry{Source: "api", Code: "INTERNAL", Message: "failed for password=CANARYX task " + string(rune('0'+i%10)), User: "u"})
	}
	if !r.Flush(2 * time.Second) {
		t.Fatal("flush timeout")
	}
	if sk.total() != 50 || len(sk.got) != 1 {
		t.Fatalf("expected one group with 50 occurrences, got %v", sk.got)
	}
	for _, l := range lines {
		if strings.Contains(l, "CANARYX") {
			t.Fatal("log line leaks secret")
		}
	}
	if len(lines) != 1 {
		t.Fatalf("log lines are rate limited per fingerprint, got %d", len(lines))
	}
}

func TestReporterDropsWhenQueueFull(t *testing.T) {
	r := newTestReporter(t, Options{QueueSize: 4, RatePerSec: 100000})
	r.hold.Lock() // freeze the writer
	for i := 0; i < 50; i++ {
		done := make(chan struct{})
		go func() { r.Report(context.Background(), Entry{Source: "api", Message: "x"}); close(done) }()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("Report blocked on a full queue")
		}
	}
	if r.Stats().Dropped < 40 {
		t.Fatalf("drops not counted: %+v", r.Stats())
	}
	r.hold.Unlock()
}

func TestReporterGlobalRateLimit(t *testing.T) {
	r := newTestReporter(t, Options{RatePerSec: 10})
	for i := 0; i < 1000; i++ {
		r.Report(context.Background(), Entry{Source: "api", Message: "flood"})
	}
	if r.Stats().Dropped < 900 {
		t.Fatalf("a hot loop must be throttled: %+v", r.Stats())
	}
}

func TestReporterDBDownFallbackAndRecovery(t *testing.T) {
	dir := t.TempDir()
	r := newTestReporter(t, Options{FallbackPath: filepath.Join(dir, "fb.jsonl"), MaxPending: 3})
	sk := &fakeSink{}
	sk.fail.Store(true)
	r.setSink(sk)
	for i := 0; i < 6; i++ {
		r.Report(context.Background(), Entry{Source: "api", Code: "C" + string(rune('A'+i)), Message: "token=CANARYFB down"})
	}
	r.Flush(2 * time.Second)
	raw, err := os.ReadFile(filepath.Join(dir, "fb.jsonl"))
	if err != nil || !strings.Contains(string(raw), `"code":"CA"`) {
		t.Fatalf("fallback file missing: %v %q", err, raw)
	}
	if strings.Contains(string(raw), "CANARYFB") {
		t.Fatal("fallback leaks secret")
	}
	if st := r.Stats(); st.Pending > 3 || st.Dropped == 0 || st.Failed == 0 {
		t.Fatalf("backlog must be bounded and drops counted: %+v", st)
	}
	sk.fail.Store(false)
	time.Sleep(60 * time.Millisecond)
	r.Flush(2 * time.Second)
	if sk.total() == 0 || r.Stats().Pending != 0 {
		t.Fatalf("pending groups must be written once the DB is back: %+v", r.Stats())
	}
}

func TestReporterNoSinkKeepsBounded(t *testing.T) {
	r := newTestReporter(t, Options{MaxPending: 5, RatePerSec: 100000})
	for i := 0; i < 50; i++ {
		r.Report(context.Background(), Entry{Source: "api", Code: "K" + strings.Repeat("x", i), Message: "m"})
	}
	r.Flush(2 * time.Second)
	if p := r.Stats().Pending; p > 5 {
		t.Fatalf("pending %d", p)
	}
	sk := &fakeSink{}
	r.setSink(sk)
	r.Flush(2 * time.Second)
	if sk.total() == 0 {
		t.Fatal("reports made before the database was attached must be written afterwards")
	}
}

func TestReportNeverPanics(t *testing.T) {
	r := newTestReporter(t, Options{})
	var nilCtx context.Context
	r.Report(nilCtx, Entry{Message: "\x00\xff bad utf8", Source: "nope", Severity: "weird", Details: map[string]any{"c": make(chan int), "f": func() {}}})
}

func TestSafeAndGo(t *testing.T) {
	r := newTestReporter(t, Options{})
	sk := &fakeSink{}
	r.setSink(sk)
	old := SetDefault(r)
	defer SetDefault(old)
	if id := Safe("pipeline.loop", func() {}); id != "" {
		t.Fatal("no panic, no id")
	}
	id := Safe("pipeline.loop", func() { panic("boom password=CANARYP") })
	if id == "" {
		t.Fatal("panic must yield a report id")
	}
	done := make(chan struct{})
	Go("storage.work", func() { defer close(done); panic("again") })
	<-done
	for i := 0; i < 100 && sk.total() != 2; i++ {
		r.Flush(time.Second)
		time.Sleep(10 * time.Millisecond)
	}
	if sk.total() != 2 || len(sk.got) != 2 {
		t.Fatalf("got %v", sk.got)
	}
}

func TestLoopRestartsAfterPanic(t *testing.T) {
	r := newTestReporter(t, Options{})
	old := SetDefault(r)
	defer SetDefault(old)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var runs atomic.Int32
	LoopBackoff(ctx, "pipeline.loop", time.Millisecond, 5*time.Millisecond, func(ctx context.Context) {
		if runs.Add(1) < 4 {
			panic("loop panic")
		}
	})
	if runs.Load() != 4 {
		t.Fatalf("runs=%d", runs.Load())
	}
	// cancelled context stops the restart loop
	runs.Store(0)
	cancel()
	LoopBackoff(ctx, "x", time.Millisecond, time.Millisecond, func(context.Context) { runs.Add(1); panic("x") })
	if runs.Load() != 0 {
		t.Fatal("must not run with cancelled ctx")
	}
}

func TestLimiter(t *testing.T) {
	now := time.Unix(1000, 0)
	l := NewLimiter(3, time.Minute)
	l.Now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		if ok, _ := l.Allow("u"); !ok {
			t.Fatal("within limit")
		}
	}
	ok, wait := l.Allow("u")
	if ok || wait <= 0 || wait > time.Minute {
		t.Fatalf("ok=%v wait=%v", ok, wait)
	}
	if ok, _ := l.Allow("other"); !ok {
		t.Fatal("keys are independent")
	}
	now = now.Add(61 * time.Second)
	if ok, _ := l.Allow("u"); !ok {
		t.Fatal("window slides")
	}
}

func TestRunLock(t *testing.T) {
	dir := t.TempDir()
	alive := map[int]bool{}
	probe := func(pid int) bool { return alive[pid] }
	prev, release, err := AcquireRunLock(dir, probe)
	if err != nil || prev.Unclean {
		t.Fatalf("first start: %+v %v", prev, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "run.lock")); err != nil {
		t.Fatal("lock not created")
	}
	release()
	if _, err := os.Stat(filepath.Join(dir, "run.lock")); !os.IsNotExist(err) {
		t.Fatal("graceful exit must remove the lock")
	}
	prev, release, _ = AcquireRunLock(dir, probe)
	release()
	if prev.Unclean {
		t.Fatal("clean after graceful exit")
	}

	// stale lock from a dead process
	writeLock(t, dir, 424242, time.Now().Add(-time.Hour))
	crash := filepath.Join(dir, "crash-20260101T000000Z.txt")
	os.WriteFile(crash, []byte("x"), 0o600)
	prev, release, _ = AcquireRunLock(dir, probe)
	if !prev.Unclean || prev.Info.PID != 424242 || len(prev.CrashFiles) != 1 {
		t.Fatalf("stale lock: %+v", prev)
	}
	release()

	// lock owned by a live foreign process: not unclean, and not touched
	writeLock(t, dir, 777, time.Now())
	alive[777] = true
	prev, release, _ = AcquireRunLock(dir, probe)
	if prev.Unclean || !prev.OtherAlive {
		t.Fatalf("live foreign lock: %+v", prev)
	}
	release()
	if _, err := os.Stat(filepath.Join(dir, "run.lock")); err != nil {
		t.Fatal("must leave the foreign lock alone")
	}

	// unreadable lock = writer died mid-write
	os.WriteFile(filepath.Join(dir, "run.lock"), []byte("{trunc"), 0o600)
	prev, release, _ = AcquireRunLock(dir, probe)
	release()
	if !prev.Unclean {
		t.Fatal("corrupt lock means unclean")
	}

	// our own pid in the lock (restart in the same process / pid reuse by us) is not an unclean exit
	writeLock(t, dir, os.Getpid(), time.Now())
	prev, release, _ = AcquireRunLock(dir, probe)
	release()
	if prev.Unclean {
		t.Fatal("own pid is not unclean")
	}
}

func writeLock(t *testing.T, dir string, pid int, at time.Time) {
	t.Helper()
	b := []byte(`{"pid":` + itoa(pid) + `,"started_at":"` + at.UTC().Format(time.RFC3339) + `"}`)
	if err := os.WriteFile(filepath.Join(dir, "run.lock"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func itoa(n int) string { return sprint(n) }

func TestReportPrevRun(t *testing.T) {
	r := newTestReporter(t, Options{})
	sk := &fakeSink{}
	r.setSink(sk)
	old := SetDefault(r)
	defer SetDefault(old)
	if ReportPrevRun(context.Background(), PrevRun{}) != "" {
		t.Fatal("clean run reports nothing")
	}
	id := ReportPrevRun(context.Background(), PrevRun{Unclean: true, Info: RunInfo{PID: 9}, CrashFiles: []string{"crash-1.txt"}})
	if id == "" {
		t.Fatal("no id")
	}
	r.Flush(time.Second)
	if sk.total() != 1 {
		t.Fatal("not written")
	}
}

func TestCrashFileHasNoSecrets(t *testing.T) {
	dir := t.TempDir()
	SetLogDir(dir)
	defer SetLogDir("")
	defer ResetSecrets()
	RegisterSecret("CANARY-WORKER-TOKEN-123456")
	t.Setenv("OA_WORKER_TOKEN", "CANARY-WORKER-TOKEN-123456")
	t.Setenv("OA_SECRET_KEY", "CANARYSECRETKEYVALUE")
	t.Setenv("OA_LLM_API_KEY", "sk-CANARYLLMKEY0123456789")
	t.Setenv("OA_DATABASE_URL", "postgres://oa:CANARYDBPASS@127.0.0.1/oa")
	t.Setenv("OA_LISTEN_ADDR", "127.0.0.1:1234")
	p, err := WriteCrashFile("fatal", "boom with password=CANARYMSG and Bearer CANARYBEARER12345", "oa/x.f()\n\t/home/alice/x.go:1\ntoken: CANARYSTACK")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(filepath.Base(p), "crash-") || !strings.HasSuffix(p, ".txt") {
		t.Fatal(p)
	}
	raw, _ := os.ReadFile(p)
	s := string(raw)
	for _, bad := range []string{"CANARY", "alice"} {
		if strings.Contains(s, bad) {
			t.Fatalf("crash file leaks %q:\n%s", bad, s)
		}
	}
	for _, want := range []string{"OA_LISTEN_ADDR=127.0.0.1:1234", "OA_WORKER_TOKEN=[set]", "version:", "message:", "stack:"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q:\n%s", want, s)
		}
	}
}

func TestFatalfWritesCrashFileAndReleasesLock(t *testing.T) {
	dir := t.TempDir()
	SetLogDir(dir)
	defer SetLogDir("")
	r := newTestReporter(t, Options{})
	sk := &fakeSink{}
	r.setSink(sk)
	old := SetDefault(r)
	defer SetDefault(old)
	code := -1
	exitFn = func(c int) { code = c }
	defer func() { exitFn = os.Exit }()
	released := false
	SetRunLockRelease(func() { released = true })
	defer SetRunLockRelease(nil)
	Fatalf("cannot start: %s", "password=CANARYF")
	if code != 1 || !released {
		t.Fatalf("code=%d released=%v", code, released)
	}
	m, _ := filepath.Glob(filepath.Join(dir, "crash-*.txt"))
	if len(m) != 1 {
		t.Fatalf("crash files %v", m)
	}
	if sk.total() != 1 {
		t.Fatal("fatal report not written")
	}
}

func TestRecoverMain(t *testing.T) {
	dir := t.TempDir()
	SetLogDir(dir)
	defer SetLogDir("")
	r := newTestReporter(t, Options{})
	old := SetDefault(r)
	defer SetDefault(old)
	code := -1
	exitFn = func(c int) { code = c }
	defer func() { exitFn = os.Exit }()
	func() {
		defer RecoverMain()
		panic("main went wrong")
	}()
	if code != 2 {
		t.Fatalf("code %d", code)
	}
	raw, _ := os.ReadFile(firstGlob(t, dir))
	if !strings.Contains(string(raw), "main went wrong") || !strings.Contains(string(raw), "kind: panic") {
		t.Fatal(string(raw))
	}
}

func firstGlob(t *testing.T, dir string) string {
	m, _ := filepath.Glob(filepath.Join(dir, "crash-*.txt"))
	if len(m) == 0 {
		t.Fatal("no crash file")
	}
	return m[0]
}

func TestEnvSummary(t *testing.T) {
	t.Setenv("OA_WORKER_TOKEN", "CANARYTOKEN")
	t.Setenv("OA_LISTEN_ADDR", "0.0.0.0:80")
	t.Setenv("NOT_OA_THING", "x")
	seen := map[string]string{}
	for _, e := range EnvSummary() {
		seen[e.Name] = e.Value
	}
	if seen["OA_WORKER_TOKEN"] != "[set]" || seen["OA_LISTEN_ADDR"] != "0.0.0.0:80" {
		t.Fatalf("%v", seen)
	}
	if _, ok := seen["NOT_OA_THING"]; ok {
		t.Fatal("only OA_* names")
	}
}

func TestDiskUsage(t *testing.T) {
	free, total, err := DiskUsage(t.TempDir())
	if err != nil || total == 0 || free > total {
		t.Fatalf("%d %d %v", free, total, err)
	}
}
