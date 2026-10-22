package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"oa-agent/backend/internal/ctl/compose"
	"oa-agent/backend/internal/ctl/mounts"
)

// memProber answers from a map; paths not listed look like a healthy empty directory.
type memProber map[string]mounts.Probe

func (m memProber) Probe(p string) (mounts.Probe, error) {
	if pr, ok := m[p]; ok {
		return pr, nil
	}
	uid := 10001
	if strings.Contains(p, "pg") {
		uid = 70
	}
	mode := os.FileMode(0o750)
	if strings.Contains(p, "/mnt/") {
		mode = 0o755
	}
	return mounts.Probe{Exists: true, IsDir: true, Mode: mode, UID: uid, GID: uid, FreeBytes: 500 << 30, TotalBytes: 900 << 30, FSType: "ext4", Empty: true}, nil
}

type rig struct {
	app      *App
	f        *compose.Fake
	out, err *bytes.Buffer
	dir      string
	prober   memProber
	execs    [][]string
	uid      int
}

const secretKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func newRig(t *testing.T) *rig {
	t.Helper()
	d := t.TempDir()
	env := "COMPOSE_FILE=docker-compose.yml:docker-compose.mounts.yml\nOA_PORT=8080\nOA_PUBLIC_URL=http://srv:8080\nOA_SECRET_KEY=" + secretKey + "\nOA_WORKER_TOKEN=tokentokentokentokentokentokentoken12\nOA_POSTGRES_PASSWORD=pgpass-very-secret\n"
	os.WriteFile(filepath.Join(d, ".env"), []byte(env), 0o600)
	os.WriteFile(filepath.Join(d, "docker-compose.yml"), []byte("services: {}\n"), 0o644)
	cfg := mounts.Config{Version: 1, Archive: mounts.Path{Host: "/srv/oa/archive"}, Spool: mounts.Path{Host: "/srv/oa/spool"},
		App: mounts.Path{Host: "/srv/oa/app"}, PGData: mounts.Path{Host: "/srv/oa/pgdata"}}
	if _, err := mounts.Save(d, cfg); err != nil {
		t.Fatal(err)
	}
	r := &rig{f: &compose.Fake{}, out: &bytes.Buffer{}, err: &bytes.Buffer{}, dir: d, prober: memProber{}}
	r.app = &App{Out: r.out, Err: r.err, In: strings.NewReader(""), Runner: r.f, Prober: r.prober,
		IsTTY: func() bool { return false }, Getuid: func() int { return r.uid + 1000 }, Version: "test",
		Poll: time.Millisecond, HealthTimeout: time.Second, HostIP: func() string { return "10.0.0.9" },
		Exec: func(ctx context.Context, name string, args ...string) error {
			r.execs = append(r.execs, append([]string{name}, args...))
			return nil
		}}
	r.f.On("ps", compose.Result{Stdout: psUp}, nil)
	return r
}

const psUp = `{"Service":"postgres","State":"running","Health":"healthy","Status":"Up 1 hour (healthy)"}
{"Service":"api","State":"running","Health":"healthy","Status":"Up 1 hour (healthy)","Publishers":[{"URL":"0.0.0.0","TargetPort":8080,"PublishedPort":8080,"Protocol":"tcp"}]}
{"Service":"worker","State":"running","Status":"Up 1 hour"}`

const psDown = `{"Service":"postgres","State":"exited"}
{"Service":"api","State":"exited"}`

func (r *rig) run(args ...string) int {
	r.out.Reset()
	r.err.Reset()
	return r.app.Main(append([]string{"--dir", r.dir}, args...))
}

func (r *rig) mountsJSON(t *testing.T) mounts.Config {
	c, _, err := mounts.Load(r.dir, nil2(t, r))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func nil2(t *testing.T, r *rig) *envfileAlias {
	s, err := r.app.Open(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	return s.Inst.Env
}

func (r *rig) exitIs(t *testing.T, want int, args ...string) {
	t.Helper()
	if got := r.run(args...); got != want {
		t.Fatalf("%v: exit %d want %d\nstdout: %s\nstderr: %s", args, got, want, r.out, r.err)
	}
}

func TestExitCodes(t *testing.T) {
	r := newRig(t)
	r.exitIs(t, ExitUsage, "bogus")
	r.exitIs(t, ExitUsage, "restart", "a", "b")
	r.exitIs(t, ExitUsage, "restart", "nosuch")
	r.exitIs(t, ExitUsage, "logs", "--tail", "abc")
	r.exitIs(t, ExitUsage, "mounts")
	r.exitIs(t, ExitOK, "help")
	r.exitIs(t, ExitOK, "--help")
	if r.app.Main([]string{}) != ExitUsage { // no TTY: interactive UI refused with a clear message
		t.Error("no-arg without TTY must exit 2")
	}
	if !strings.Contains(r.err.String(), "不是终端") {
		t.Errorf("message: %s", r.err)
	}
	if got := r.app.Main([]string{"--dir", filepath.Join(r.dir, "nope"), "status"}); got != ExitNoInst {
		t.Errorf("no install: %d", got)
	}
	r.exitIs(t, ExitRefused, "stop") // confirmation needed, stdin is not a terminal
	if !strings.Contains(r.err.String(), "--yes") {
		t.Errorf("must tell about --yes: %s", r.err)
	}
	r.f.On("compose ps", compose.Result{}, compose.ErrDockerMissing)
}

func TestStatusJSON(t *testing.T) {
	r := newRig(t)
	r.exitIs(t, 0, "status", "--json")
	var got struct {
		URL      string `json:"url"`
		OK       bool   `json:"ok"`
		Services []struct{ Service, State, Health, Ports, Uptime string }
	}
	if err := json.Unmarshal(r.out.Bytes(), &got); err != nil {
		t.Fatalf("%v\n%s", err, r.out)
	}
	if got.URL != "http://srv:8080" || !got.OK || len(got.Services) != 3 || got.Services[1].Ports != "0.0.0.0:8080->8080/tcp" {
		t.Errorf("%+v", got)
	}
	r.exitIs(t, 0, "status")
	if !strings.Contains(r.out.String(), "postgres") || !strings.Contains(r.out.String(), "http://srv:8080") {
		t.Errorf("%s", r.out)
	}
	r.f.Rules = nil
	r.f.On("ps", compose.Result{Stdout: psDown}, nil)
	r.exitIs(t, 0, "status", "--json")
	if strings.Contains(r.out.String(), `"ok": true`) {
		t.Error("ok must be false when services are down")
	}
}

func TestStartStopRestart(t *testing.T) {
	r := newRig(t)
	r.exitIs(t, 0, "start")
	if !r.f.Called("compose up -d") {
		t.Errorf("%v", r.f.Joined())
	}
	r.exitIs(t, 0, "stop", "--yes")
	if !r.f.Called("compose stop") {
		t.Errorf("%v", r.f.Joined())
	}
	r.exitIs(t, 0, "restart", "api")
	if !r.f.Called("up -d --force-recreate api worker") {
		t.Errorf("api+worker must restart together: %v", r.f.Joined())
	}
	n := len(r.f.Calls)
	r.exitIs(t, 0, "--dry-run", "restart")
	if len(r.f.Calls) != n {
		t.Error("dry-run called docker")
	}
}

func TestLogsArgs(t *testing.T) {
	r := newRig(t)
	r.f.On("logs", compose.Result{Stdout: "line1\nline2\n"}, nil)
	r.exitIs(t, 0, "logs", "api", "-f", "--tail", "50")
	if !r.f.Called("compose logs --no-color --tail 50 --follow api") {
		t.Errorf("%v", r.f.Joined())
	}
	if r.out.String() != "line1\nline2\n" {
		t.Errorf("%q", r.out)
	}
	r.exitIs(t, 0, "logs")
	if !r.f.Called("compose logs --no-color --tail 200") {
		t.Errorf("default tail: %v", r.f.Joined())
	}
}

func TestConfig(t *testing.T) {
	r := newRig(t)
	r.exitIs(t, 0, "config", "show")
	out := r.out.String()
	for _, leak := range []string{secretKey, "pgpass-very-secret", "tokentoken"} {
		if strings.Contains(out, leak) {
			t.Errorf("config show leaks %q", leak)
		}
	}
	if !strings.Contains(out, "OA_PORT=8080") {
		t.Errorf("%s", out)
	}
	r.exitIs(t, 0, "config", "show", "--json")
	if strings.Contains(r.out.String(), secretKey) || !strings.Contains(r.out.String(), "********") {
		t.Errorf("json leak: %s", r.out)
	}
	r.exitIs(t, 0, "config", "set", "OA_PORT", "9090")
	b, _ := os.ReadFile(filepath.Join(r.dir, ".env"))
	if !strings.Contains(string(b), "OA_PORT=9090") || !strings.Contains(string(b), "OA_SECRET_KEY="+secretKey) {
		t.Errorf("%s", b)
	}
	r.exitIs(t, ExitFail, "config", "set", "OA_PORT", "99999")
	r.exitIs(t, ExitFail, "config", "set", "OA_SECRET_KEY", "x")
	r.exitIs(t, ExitFail, "config", "set", "OA_PORT", "1;rm")
	r.exitIs(t, ExitUsage, "config", "set", "OA_PORT")
	r.exitIs(t, 0, "--dry-run", "config", "set", "OA_PORT", "7777")
	b, _ = os.ReadFile(filepath.Join(r.dir, ".env"))
	if strings.Contains(string(b), "7777") {
		t.Error("dry-run wrote")
	}
	r.app.In = strings.NewReader("sk-fromstdin\n")
	r.exitIs(t, 0, "config", "set", "OA_LLM_API_KEY", "-")
	if strings.Contains(r.out.String(), "sk-fromstdin") {
		t.Error("secret echoed")
	}
	b, _ = os.ReadFile(filepath.Join(r.dir, ".env"))
	if !strings.Contains(string(b), "OA_LLM_API_KEY=sk-fromstdin") {
		t.Errorf("stdin value not stored:\n%s", b)
	}
	r.exitIs(t, 0, "--dry-run", "config", "set", "OA_LLM_API_KEY", "sk-visible")
	if strings.Contains(r.out.String(), "sk-visible") {
		t.Error("dry-run echoed secret")
	}
}

func TestUsers(t *testing.T) {
	r := newRig(t)
	r.f.On("list-users --json", compose.Result{Stdout: `[{"username":"alice","role":"admin","status":"active","mfa":true,"locked":false}]`}, nil)
	r.exitIs(t, 0, "users", "list", "--json")
	if !strings.Contains(r.out.String(), `"username": "alice"`) {
		t.Errorf("%s", r.out)
	}
	r.exitIs(t, 0, "users", "list")
	if !strings.Contains(r.out.String(), "alice") || !strings.Contains(r.out.String(), "admin") {
		t.Errorf("%s", r.out)
	}

	// reset-password: needs confirmation, shows the password once on stdout only
	r.f.On("reset-password alice", compose.Result{Stdout: "Temporary password: Tmp-Pa55w0rd\n"}, nil)
	r.exitIs(t, ExitRefused, "users", "reset-password", "alice")
	if r.f.Called("reset-password alice") {
		t.Error("ran without confirmation")
	}
	r.exitIs(t, 0, "users", "reset-password", "alice", "--yes")
	if !strings.Contains(r.out.String(), "Tmp-Pa55w0rd") || strings.Count(r.out.String(), "Tmp-Pa55w0rd") != 1 {
		t.Errorf("password must appear exactly once: %s", r.out)
	}
	if strings.Contains(r.err.String(), "Tmp-Pa55w0rd") {
		t.Error("password leaked to stderr")
	}
	r.exitIs(t, 0, "users", "reset-password", "alice", "--yes", "--json")
	var js map[string]string
	json.Unmarshal(r.out.Bytes(), &js)
	if js["temporary_password"] != "Tmp-Pa55w0rd" || js["username"] != "alice" {
		t.Errorf("%v", js)
	}
	// unparsable output: raw shown once with a warning
	r.f.On("create-admin carol", compose.Result{Stdout: "created carol; secret is hunter2hunter2\n"}, nil)
	r.exitIs(t, 0, "users", "create-admin", "carol", "--yes")
	if !strings.Contains(r.out.String(), "hunter2hunter2") || !strings.Contains(r.err.String(), "无法识别") {
		t.Errorf("out=%s err=%s", r.out, r.err)
	}

	r.exitIs(t, ExitUsage, "users", "reset-password", "bad name; rm -rf /")
	r.exitIs(t, ExitUsage, "users", "reset-password")
	r.exitIs(t, ExitUsage, "users", "frobnicate", "x")
	n := len(r.f.Calls)
	r.exitIs(t, 0, "--dry-run", "users", "reset-mfa", "alice")
	if len(r.f.Calls) != n {
		t.Error("dry-run ran oa-admin")
	}
	r.f.On("unlock bob", compose.Result{Stdout: "unlocked\n"}, nil)
	r.exitIs(t, 0, "users", "unlock", "bob", "--yes")
	if !strings.Contains(r.out.String(), "unlocked") {
		t.Errorf("%s", r.out)
	}
	r.f.On("disable-user bob", compose.Result{Stderr: "last admin", ExitCode: 1}, &compose.ExitError{Result: compose.Result{Stderr: "last admin", ExitCode: 1}})
	r.exitIs(t, ExitFail, "users", "disable-user", "bob", "--yes")
	if !strings.Contains(r.err.String(), "last admin") {
		t.Errorf("%s", r.err)
	}
}

func TestAdminPassthroughAndRunFallback(t *testing.T) {
	r := newRig(t)
	r.f.On("oa-admin", compose.Result{Stdout: "OUT"}, nil)
	r.exitIs(t, 0, "admin", "reset-mfa", "--weird", "--yes", "alice")
	if !r.f.Called("compose exec -T api oa-admin reset-mfa --weird --yes alice") {
		t.Errorf("flags after admin must pass through untouched: %v", r.f.Joined())
	}
	r.f.Rules = nil
	r.f.On("ps", compose.Result{Stdout: psDown}, nil)
	r.exitIs(t, 0, "admin", "status")
	if !r.f.Called("compose run --rm -T api oa-admin status") {
		t.Errorf("api down must use run --rm: %v", r.f.Joined())
	}
}

func TestSetupToken(t *testing.T) {
	r := newRig(t)
	r.f.On("logs", compose.Result{Stdout: "SETUP TOKEN: ABCDEF234567\n"}, nil)
	r.exitIs(t, 0, "setup-token")
	if strings.TrimSpace(r.out.String()) != "ABCDEF234567" {
		t.Errorf("%q", r.out)
	}
}

func TestMountsListAndCheck(t *testing.T) {
	r := newRig(t)
	r.exitIs(t, 0, "mounts", "list")
	if !strings.Contains(r.out.String(), "/srv/oa/archive") || !strings.Contains(r.out.String(), "/data/archive") {
		t.Errorf("%s", r.out)
	}
	r.exitIs(t, 0, "mounts", "list", "--json")
	r.exitIs(t, 0, "mounts", "check")
	r.prober["/srv/oa/pgdata"] = mounts.Probe{Exists: true, IsDir: true, Mode: 0o755, UID: 0, GID: 0}
	r.exitIs(t, ExitFail, "mounts", "check")
	if !strings.Contains(r.out.String(), "chown -R 70:70 '/srv/oa/pgdata'") {
		t.Errorf("exact fix command expected:\n%s", r.out)
	}
}

func TestMountsAdd(t *testing.T) {
	r := newRig(t)
	r.exitIs(t, ExitRefused, "mounts", "add", "nas1", "/mnt/nas1") // no confirmation
	if len(r.mountsJSON(t).Extra) != 0 {
		t.Error("unconfirmed add must not write")
	}
	r.exitIs(t, 0, "--dry-run", "mounts", "add", "nas1", "/mnt/nas1")
	if !strings.Contains(r.out.String(), "/mnt/nas1:/mnt/storage/nas1:ro") || len(r.mountsJSON(t).Extra) != 0 {
		t.Errorf("dry-run: %s", r.out)
	}
	r.exitIs(t, 0, "mounts", "add", "nas1", "/mnt/nas1", "--yes")
	c := r.mountsJSON(t)
	if len(c.Extra) != 1 || !c.Extra[0].ReadOnly {
		t.Errorf("default must be read-only: %+v", c.Extra)
	}
	over, _ := os.ReadFile(filepath.Join(r.dir, "docker-compose.mounts.yml"))
	if !strings.Contains(string(over), "/mnt/nas1:/mnt/storage/nas1:ro") || !strings.Contains(string(over), "OA_LOCAL_ROOTS: /mnt/storage") {
		t.Errorf("%s", over)
	}
	if r.f.Called("up -d") {
		t.Error("restart only with --restart")
	}
	// invalid inputs
	r.exitIs(t, ExitFail, "mounts", "add", "bad", "/etc/x", "--yes")
	r.exitIs(t, ExitFail, "mounts", "add", "Bad Name", "/mnt/x", "--yes")
	r.exitIs(t, ExitFail, "mounts", "add", "nas1", "/mnt/other", "--yes")           // duplicate
	r.exitIs(t, ExitFail, "mounts", "add", "inner", "/srv/oa/archive/sub", "--yes") // nested in archive
	r.exitIs(t, ExitFail, "mounts", "add", "up", "/mnt/../etc", "--yes")            // ..
	r.exitIs(t, ExitUsage, "mounts", "add", "onlyname")
	// ro -> rw on an existing extra needs the typed acknowledgement (not available without a TTY)
	r.exitIs(t, ExitRefused, "mounts", "add", "nas1", "/mnt/nas1", "--rw", "--replace", "--yes")
	if !r.mountsJSON(t).Extra[0].ReadOnly {
		t.Error("ro->rw must not have been applied")
	}
	r.exitIs(t, 0, "mounts", "add", "nas1", "/mnt/nas1", "--rw", "--replace", "--yes", "--accept-data-move")
	if r.mountsJSON(t).Extra[0].ReadOnly {
		t.Error("explicit ack should allow rw")
	}
	// a missing external directory is an error: we never create someone else's directory
	r.prober["/mnt/gone"] = mounts.Probe{}
	r.exitIs(t, ExitFail, "mounts", "add", "gone", "/mnt/gone", "--yes")
	r.exitIs(t, 0, "mounts", "remove", "nas1", "--yes")
	if len(r.mountsJSON(t).Extra) != 0 {
		t.Error("remove")
	}
	r.exitIs(t, ExitFail, "mounts", "remove", "nas1", "--yes")
}

func TestMountsSetArchiveSafety(t *testing.T) {
	r := newRig(t)
	full := mounts.Probe{Exists: true, IsDir: true, Mode: 0o750, UID: 10001, GID: 10001, Empty: false, FreeBytes: 500 << 30, TotalBytes: 900 << 30}
	r.prober["/srv/oa/archive"] = full
	before := r.mountsJSON(t)
	// non-empty old archive: refused without explicit acknowledgement, nothing changes, nothing is moved
	r.exitIs(t, ExitRefused, "mounts", "set", "archive", "/srv/new/archive", "--yes")
	if r.mountsJSON(t).Archive.Host != before.Archive.Host || len(r.execs) != 0 {
		t.Error("state changed without ack")
	}
	if !strings.Contains(r.err.String(), "不会】移动或删除") || !strings.Contains(r.err.String(), "/srv/oa/archive") {
		t.Errorf("warning must explain and name the old path:\n%s", r.err)
	}
	r.exitIs(t, 0, "mounts", "set", "archive", "/srv/new/archive", "--yes", "--accept-data-move")
	if r.mountsJSON(t).Archive.Host != "/srv/new/archive" {
		t.Error("not applied with ack")
	}
	if len(r.execs) != 0 {
		t.Error("no copy was requested, none may run")
	}
	// empty old archive: no ack needed
	r2 := newRig(t)
	r2.exitIs(t, 0, "mounts", "set", "archive", "/srv/new2/archive", "--yes")
}

func TestMountsSetArchiveCopy(t *testing.T) {
	r := newRig(t)
	root := t.TempDir()
	from, to := filepath.Join(root, "old"), filepath.Join(root, "new")
	os.MkdirAll(from, 0o755)
	os.WriteFile(filepath.Join(from, "a.txt"), []byte("1"), 0o644)
	// the real copy is faked: cp -a is simulated by really copying one file
	r.app.Exec = func(ctx context.Context, name string, args ...string) error {
		r.execs = append(r.execs, append([]string{name}, args...))
		b, _ := os.ReadFile(filepath.Join(from, "a.txt"))
		return os.WriteFile(filepath.Join(to, "a.txt"), b, 0o644)
	}
	fromPosix, toPosix := filepath.ToSlash(from), filepath.ToSlash(to)
	if !strings.HasPrefix(fromPosix, "/") {
		t.Skip("needs POSIX absolute temp paths")
	}
	r.prober[fromPosix] = mounts.Probe{Exists: true, IsDir: true, Empty: false, Mode: 0o750, UID: 10001, GID: 10001}
	r.app.getuidForTest(0)
	cfg := r.mountsJSON(t).WithCore("archive", fromPosix)
	mounts.Save(r.dir, cfg)
	r.exitIs(t, 0, "mounts", "set", "archive", toPosix, "--copy", "--yes", "--accept-data-move")
	if len(r.execs) != 1 || r.execs[0][0] != "cp" || r.execs[0][1] != "-a" || r.execs[0][2] != fromPosix+"/." {
		t.Errorf("%v", r.execs)
	}
	if _, err := os.Stat(filepath.Join(from, "a.txt")); err != nil {
		t.Error("source must remain")
	}
	if r.mountsJSON(t).Archive.Host != toPosix {
		t.Error("not switched after verified copy")
	}
}

func TestMountsSetPgdata(t *testing.T) {
	r := newRig(t)
	// stack running: refused no matter what
	r.exitIs(t, ExitFail, "mounts", "set", "pgdata", "/srv/newpg", "--yes", "--accept-data-move")
	if r.mountsJSON(t).PGData.Host != "/srv/oa/pgdata" {
		t.Error("pgdata changed while running")
	}
	if !strings.Contains(r.err.String(), "oa-agentctl stop") {
		t.Errorf("must say how: %s", r.err)
	}
	r.f.Rules = nil
	r.f.On("ps", compose.Result{Stdout: psDown}, nil)
	r.exitIs(t, 0, "mounts", "set", "pgdata", "/srv/newpg", "--yes")
	if r.mountsJSON(t).PGData.Host != "/srv/newpg" {
		t.Error("stopped stack with empty old pgdata should work")
	}
	// non-empty old db requires acknowledgement even when stopped
	r.prober["/srv/newpg"] = mounts.Probe{Exists: true, IsDir: true, Empty: false, Mode: 0o700, UID: 70, GID: 70}
	r.prober["/srv/other"] = mounts.Probe{Exists: true, IsDir: true, Empty: true, Mode: 0o700, UID: 70, GID: 70}
	r.exitIs(t, ExitRefused, "mounts", "set", "pgdata", "/srv/other", "--yes")
}

func TestMountsApplyUnattended(t *testing.T) {
	r := newRig(t)
	// no mounts.json at all: the legacy layout from .env is used and mounts.json gets created
	os.Remove(filepath.Join(r.dir, "mounts.json"))
	env, _ := os.ReadFile(filepath.Join(r.dir, ".env"))
	os.WriteFile(filepath.Join(r.dir, ".env"), append(env, []byte("OA_ARCHIVE_DIR=/srv/x/archive\nOA_SPOOL_DIR=/srv/x/spool\nOA_APP_DIR=/srv/x/app\nOA_PG_DIR=/srv/x/pg\n")...), 0o600)
	r.exitIs(t, 0, "mounts", "apply") // the installer calls it exactly like this: no --yes, no TTY
	over, _ := os.ReadFile(filepath.Join(r.dir, "docker-compose.mounts.yml"))
	if !strings.Contains(string(over), "/srv/x/archive:/data/archive") || !strings.Contains(string(over), "generated — do not edit") {
		t.Errorf("%s", over)
	}
	if _, err := os.Stat(filepath.Join(r.dir, "mounts.json")); err != nil {
		t.Error("mounts.json not created")
	}
	// determinism: second run changes nothing
	r.exitIs(t, 0, "mounts", "apply")
	over2, _ := os.ReadFile(filepath.Join(r.dir, "docker-compose.mounts.yml"))
	if string(over) != string(over2) {
		t.Error("apply is not deterministic")
	}
	// invalid mounts.json: non-zero with a message, files untouched
	os.WriteFile(filepath.Join(r.dir, "mounts.json"), []byte(`{"version":1,"archive":{"host":"/etc/oa"},"spool":{"host":"/srv/s"},"app":{"host":"/srv/a"},"pgdata":{"host":"/srv/p"}}`), 0o644)
	r.exitIs(t, ExitFail, "mounts", "apply")
	if !strings.Contains(r.err.String(), "/etc") {
		t.Errorf("%s", r.err)
	}
	after, _ := os.ReadFile(filepath.Join(r.dir, "docker-compose.mounts.yml"))
	if string(after) != string(over) {
		t.Error("invalid input must not rewrite the override")
	}
	os.WriteFile(filepath.Join(r.dir, "mounts.json"), []byte("{broken"), 0o644)
	r.exitIs(t, ExitFail, "mounts", "apply")
}

func TestDomain(t *testing.T) {
	r := newRig(t)
	os.WriteFile(filepath.Join(r.dir, "Caddyfile.template"), []byte("__EMAIL_LINE__\n__DOMAIN__ {}\n"), 0o644)
	r.exitIs(t, ExitUsage, "domain", "not a domain")
	r.exitIs(t, ExitRefused, "domain", "oa.example.com")
	r.f.Rules = nil
	r.f.On("ps", compose.Result{Stdout: psUp + "\n{\"Service\":\"caddy\",\"State\":\"running\"}"}, nil)
	r.exitIs(t, 0, "domain", "oa.example.com", "--yes")
	env, _ := os.ReadFile(filepath.Join(r.dir, ".env"))
	if !strings.Contains(string(env), "OA_PUBLIC_URL=https://oa.example.com") {
		t.Errorf("%s", env)
	}
	r.exitIs(t, 0, "domain", "other.example.com", "--yes", "--no-start")
	if !strings.Contains(r.err.String(), "Passkey") {
		t.Errorf("passkey warning expected: %s", r.err)
	}
	r.exitIs(t, 0, "domain", "--none", "--yes", "--no-start")
	env, _ = os.ReadFile(filepath.Join(r.dir, ".env"))
	if !strings.Contains(string(env), "OA_PUBLIC_URL=http://10.0.0.9:8080") || strings.Contains(string(env), "https.yml") {
		t.Errorf("%s", env)
	}
}

func TestVersionWorksWithoutInstall(t *testing.T) {
	r := newRig(t)
	if got := r.app.Main([]string{"--dir", filepath.Join(r.dir, "none"), "version"}); got != 0 {
		t.Fatalf("exit %d: %s", got, r.err)
	}
	if !strings.Contains(r.out.String(), "oa-agentctl test") {
		t.Errorf("%s", r.out)
	}
}

func TestBackupDryRunAndRestoreConfirm(t *testing.T) {
	r := newRig(t)
	n := len(r.f.Calls)
	r.exitIs(t, 0, "--dry-run", "backup")
	if len(r.f.Calls) != n {
		t.Error("dry-run touched docker")
	}
	r.exitIs(t, ExitFail, "restore", filepath.Join(r.dir, "missing.tar.gz"))
	r.exitIs(t, ExitUsage, "restore")
}

func TestMountsApplyRestartRollbackExit(t *testing.T) {
	r := newRig(t)
	boom := &compose.ExitError{Result: compose.Result{ExitCode: 1, Stderr: "no such device"}}
	h := &compose.Rule{Match: "up -d --force-recreate api worker", Result: compose.Result{ExitCode: 1}, Err: boom, Times: 1}
	r.f.Rules = append([]*compose.Rule{h}, r.f.Rules...)
	before, _ := os.ReadFile(filepath.Join(r.dir, "mounts.json"))
	envBefore, _ := os.ReadFile(filepath.Join(r.dir, ".env"))
	r.exitIs(t, ExitFail, "mounts", "apply", "--restart")
	if !strings.Contains(r.err.String(), "回滚") {
		t.Errorf("%s", r.err)
	}
	after, _ := os.ReadFile(filepath.Join(r.dir, "mounts.json"))
	envAfter, _ := os.ReadFile(filepath.Join(r.dir, ".env"))
	if string(before) != string(after) || string(envBefore) != string(envAfter) {
		t.Error("files must be restored after a failed restart")
	}
}
