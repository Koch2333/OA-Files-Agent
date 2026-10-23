package tui

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"oa-agent/backend/internal/ctl/cli"
	"oa-agent/backend/internal/ctl/compose"
	"oa-agent/backend/internal/ctl/mounts"
)

type memProber map[string]mounts.Probe

func (m memProber) Probe(p string) (mounts.Probe, error) {
	if pr, ok := m[p]; ok {
		return pr, nil
	}
	uid := 10001
	if strings.Contains(p, "pg") {
		uid = 70
	}
	mode := os.FileMode(0o755)
	if uid == 70 {
		mode = 0o700
	}
	return mounts.Probe{Exists: true, IsDir: true, Mode: mode, UID: uid, GID: uid, FreeBytes: 500 << 30, TotalBytes: 900 << 30, FSType: "ext4", Empty: true}, nil
}

const psUp = `{"Service":"postgres","State":"running","Health":"healthy","Status":"Up 2 hours (healthy)"}
{"Service":"api","State":"running","Health":"healthy","Status":"Up 2 hours (healthy)","Publishers":[{"URL":"0.0.0.0","TargetPort":8080,"PublishedPort":8080,"Protocol":"tcp"}]}
{"Service":"worker","State":"running","Status":"Up 2 hours"}`

type env struct {
	b      *Backend
	f      *compose.Fake
	dir    string
	prober memProber
}

func newEnv(t *testing.T) *env {
	t.Helper()
	d := t.TempDir()
	os.WriteFile(filepath.Join(d, ".env"), []byte("COMPOSE_FILE=docker-compose.yml:docker-compose.mounts.yml\nOA_PORT=8080\nOA_PUBLIC_URL=http://srv:8080\nOA_SECRET_KEY=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\nOA_WORKER_TOKEN=tokentokentokentokentokentokentoken12\n"), 0o600)
	os.WriteFile(filepath.Join(d, "docker-compose.yml"), []byte("x"), 0o644)
	cfg := mounts.Config{Version: 1, Archive: mounts.Path{Host: "/srv/oa/archive"}, Spool: mounts.Path{Host: "/srv/oa/spool"},
		App: mounts.Path{Host: "/srv/oa/app"}, PGData: mounts.Path{Host: "/srv/oa/pgdata"}}
	mounts.Save(d, cfg)
	f := &compose.Fake{}
	f.On("ps", compose.Result{Stdout: psUp}, nil)
	pr := memProber{}
	app := &cli.App{Runner: f, Prober: pr, Getuid: func() int { return 1000 }, Poll: time.Millisecond, HealthTimeout: time.Second}
	s, err := app.Open(d)
	if err != nil {
		t.Fatal(err)
	}
	return &env{b: &Backend{S: s, Ctx: context.Background(), HostIP: "10.0.0.1", ClockSync: func() string { return "yes" }}, f: f, dir: d, prober: pr}
}

func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "space":
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune(" ")}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// drive feeds msg to the root model and runs the returned commands to completion (Tick commands are not run).
func drive(m *Model, msgs ...tea.Msg) *Model {
	queue := append([]tea.Msg(nil), msgs...)
	for guard := 0; len(queue) > 0 && guard < 5000; guard++ {
		msg := queue[0]
		queue = queue[1:]
		_, cmd := m.Update(msg)
		queue = append(queue, runCmd(cmd)...)
	}
	return m
}

func runCmd(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		switch v := msg.(type) {
		case nil:
			return nil
		case tea.BatchMsg:
			var out []tea.Msg
			for _, c := range v {
				out = append(out, runCmd(c)...)
			}
			return out
		case tea.QuitMsg:
			return nil
		}
		return []tea.Msg{msg}
	case <-time.After(300 * time.Millisecond): // a tick or a blocked stream: skip
		return nil
	}
}

func typeText(m *Model, s string) {
	for _, r := range s {
		drive(m, key(string(r)))
	}
}

func view(m *Model) string { return stripANSI(m.View()) }

func sized(m *Model) *Model {
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return m
}

func TestMenuAndResize(t *testing.T) {
	e := newEnv(t)
	m := New(e.b)
	if v := view(m); !strings.Contains(v, "用户与密码") || !strings.Contains(v, "存储挂载") || !strings.Contains(v, "诊断") {
		t.Errorf("menu missing items:\n%s", v)
	}
	// 80x24 is the minimum: exactly 24 lines, help bar last
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	lines := strings.Split(strings.TrimRight(view(m), "\n"), "\n")
	if len(lines) != 24 {
		t.Errorf("height %d != 24", len(lines))
	}
	for _, l := range lines {
		if lipgloss.Width(l) > 80 {
			t.Errorf("line wider than 80: %q", l)
		}
	}
	if !strings.Contains(lines[23], "退出") {
		t.Errorf("help bar should be last: %q", lines[23])
	}
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	if v := view(m); !strings.Contains(v, "至少 80x24") || !strings.Contains(v, "60x20") {
		t.Errorf("small-terminal message: %s", v)
	}
	sized(m)
	drive(m, key("down"), key("down"))
	if m.sel != 2 {
		t.Errorf("sel=%d", m.sel)
	}
	drive(m, key("3"))
	if m.cur == nil || !strings.Contains(m.cur.Title(), "存储挂载") {
		t.Error("hotkey did not open mounts")
	}
	drive(m, key("esc"))
	if m.cur != nil {
		t.Error("esc must return to the menu")
	}
	_, cmd := m.Update(key("q"))
	if cmd == nil {
		t.Error("q must quit")
	}
}

func TestDashboardRender(t *testing.T) {
	e := newEnv(t)
	m := sized(New(e.b))
	m.cur = newDashboard(e.b)
	d := m.cur.(*dashboard)
	if v := view(m); !strings.Contains(v, "加载中") {
		t.Errorf("loading state: %s", v)
	}
	drive(m, d.refresh()())
	v := view(m)
	for _, want := range []string{"postgres", "api", "worker", "healthy", "0.0.0.0:8080->8080/tcp", "2 hours", "http://srv:8080", "/srv/oa/archive", "GiB", "访问地址", "磁盘"} {
		if !strings.Contains(v, want) {
			t.Errorf("dashboard missing %q:\n%s", want, v)
		}
	}
	// down state shows missing/exited, never crashes
	e.f.Rules = nil
	e.f.On("ps", compose.Result{Stdout: `{"Service":"api","State":"exited"}`}, nil)
	drive(m, d.refresh()())
	v = view(m)
	if !strings.Contains(v, "exited") || !strings.Contains(v, "missing") {
		t.Errorf("down state:\n%s", v)
	}
	// docker failure is shown, not hidden
	e.f.Rules = nil
	e.f.On("ps", compose.Result{}, compose.ErrDockerMissing)
	drive(m, d.refresh()())
	if v := view(m); !strings.Contains(v, "docker") {
		t.Errorf("error not shown:\n%s", v)
	}
	// stale messages from a closed dashboard are ignored
	d.Close()
	d.Update(dashMsg{id: 12345})
}

func openUsers(t *testing.T, e *env) (*Model, *users) {
	e.f.On("list-users --json", compose.Result{Stdout: `[{"username":"alice","role":"admin","status":"active","mfa":true,"locked":false},{"username":"bob","role":"user","status":"disabled","mfa":false,"locked":true}]`}, nil)
	m := sized(New(e.b))
	drive(m, key("2"))
	return m, m.cur.(*users)
}

func TestUsersListAndPasswordShownOnce(t *testing.T) {
	e := newEnv(t)
	e.f.On("reset-password alice", compose.Result{Stdout: "Temporary password: Zx9-Tmp-Secret\n"}, nil)
	m, u := openUsers(t, e)
	v := view(m)
	for _, w := range []string{"alice", "bob", "admin", "disabled", "用户名", "MFA"} {
		if !strings.Contains(v, w) {
			t.Errorf("list missing %q:\n%s", w, v)
		}
	}
	drive(m, key("p"))
	if !strings.Contains(view(m), "alice") || !strings.Contains(view(m), "请输入 alice") {
		t.Errorf("typed confirmation expected:\n%s", view(m))
	}
	// wrong word: nothing runs
	typeText(m, "ali")
	drive(m, key("enter"))
	if e.f.Called("reset-password") {
		t.Fatal("ran without the typed confirmation")
	}
	typeText(m, "ce")
	drive(m, key("enter"))
	v = view(m)
	if !strings.Contains(v, "Zx9-Tmp-Secret") || !strings.Contains(v, "临时密码") || !strings.Contains(v, "不会再次显示") {
		t.Fatalf("secret screen:\n%s", v)
	}
	if !strings.Contains(m.cur.Help(), "不会再次显示") {
		t.Error("help bar must carry the warning")
	}
	// unrelated messages must not dismiss it
	drive(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	if !strings.Contains(view(m), "Zx9-Tmp-Secret") {
		t.Error("dismissed by non-key message")
	}
	drive(m, key("x"))
	if v := view(m); strings.Contains(v, "Zx9-Tmp-Secret") {
		t.Errorf("password still visible after dismissal:\n%s", v)
	}
	if u.secret.Password != "" || u.secret.Raw != "" || u.secret.Output != "" {
		t.Errorf("secret kept in memory: %+v", u.secret)
	}
	// the temporary password never reached the fake docker call log
	for _, c := range e.f.Joined() {
		if strings.Contains(c, "Zx9") {
			t.Errorf("secret in call log: %s", c)
		}
	}
}

func TestUsersOtherActions(t *testing.T) {
	e := newEnv(t)
	e.f.On("unlock bob", compose.Result{Stdout: "unlocked"}, nil)
	e.f.On("create-admin carol", compose.Result{Stdout: "password: NewAdminPw1\n"}, nil)
	m, _ := openUsers(t, e)
	drive(m, key("down"), key("u"))
	if !strings.Contains(view(m), "y 是") {
		t.Errorf("y/n confirm expected:\n%s", view(m))
	}
	drive(m, key("n"))
	if e.f.Called("unlock") {
		t.Error("n must cancel")
	}
	drive(m, key("u"), key("y"))
	if !e.f.Called("oa-admin unlock bob") || !strings.Contains(view(m), "unlock bob") {
		t.Errorf("unlock: %v\n%s", e.f.Joined(), view(m))
	}
	drive(m, key("x")) // dismiss result
	// create admin: invalid name rejected, valid goes to confirm then secret
	drive(m, key("a"))
	typeText(m, "bad name")
	drive(m, key("enter"))
	if e.f.Called("create-admin") || !strings.Contains(view(m), "invalid username") {
		t.Errorf("invalid name:\n%s", view(m))
	}
	for i := 0; i < 8; i++ {
		drive(m, tea.KeyMsg{Type: tea.KeyBackspace})
	}
	typeText(m, "carol")
	drive(m, key("enter"), key("y"))
	if !strings.Contains(view(m), "NewAdminPw1") {
		t.Errorf("create-admin secret:\n%s", view(m))
	}
	drive(m, key("x"))
	if strings.Contains(view(m), "NewAdminPw1") {
		t.Error("still visible")
	}
}

func TestUsersUnavailable(t *testing.T) {
	e := newEnv(t)
	e.f.On("list-users", compose.Result{ExitCode: 1, Stderr: "boom"}, &compose.ExitError{Result: compose.Result{ExitCode: 1, Stderr: "boom"}})
	m := sized(New(e.b))
	drive(m, key("2"))
	if v := view(m); !strings.Contains(v, "无法获取用户列表") {
		t.Errorf("%s", v)
	}
	// keys on an empty list must not panic
	drive(m, key("p"), key("d"), key("m"), key("down"), key("up"))
}

func openMounts(e *env) (*Model, *mountsScreen) {
	m := sized(New(e.b))
	drive(m, key("3"))
	return m, m.cur.(*mountsScreen)
}

func TestMountsListView(t *testing.T) {
	e := newEnv(t)
	m, _ := openMounts(e)
	v := view(m)
	for _, w := range []string{"archive", "pgdata", "/srv/oa/archive", "/data/archive", "/var/lib/postgresql/data", "ext4", "500G", "尚无外部目录"} {
		if !strings.Contains(v, w) {
			t.Errorf("missing %q:\n%s", w, v)
		}
	}
}

func fillAdd(m *Model, name, path string) {
	drive(m, key("a"))
	typeText(m, name)
	drive(m, key("down"))
	typeText(m, path)
}

func TestMountsAddValidationMessages(t *testing.T) {
	e := newEnv(t)
	m, s := openMounts(e)
	cases := []struct{ name, path, want string }{
		{"api", "/mnt/x", "保留名称"},
		{"Bad", "/mnt/x", "不合法"},
		{"nas1", "/etc/shadow", "系统目录"},
		{"nas1", "/srv/oa/archive/sub", "嵌套"},
		{"nas1", "/mnt/a b", "不允许的字符"},
		{"nas1", "/", "根目录"},
	}
	for _, c := range cases {
		if s.st == mForm {
			drive(m, key("esc"))
		}
		fillAdd(m, c.name, c.path)
		v := view(m)
		if !strings.Contains(v, c.want) {
			t.Errorf("%s %s: want %q in\n%s", c.name, c.path, c.want, v)
		}
		// Enter must not advance to the review while invalid
		drive(m, key("enter"))
		if s.st != mForm {
			t.Errorf("%s %s: form advanced despite errors", c.name, c.path)
		}
	}
	// a valid entry advances to the review, read-only by default
	drive(m, key("esc"))
	fillAdd(m, "nas1", "/mnt/nas1")
	if !s.ro {
		t.Error("default must be read-only")
	}
	drive(m, key("enter"))
	if s.st != mReview {
		t.Fatalf("state %d\n%s", s.st, view(m))
	}
	v := view(m)
	if !strings.Contains(v, "nas1") || !strings.Contains(v, "/mnt/nas1") {
		t.Errorf("review summary:\n%s", v)
	}
	if e.f.Called("up") {
		t.Error("nothing may run before confirming")
	}
	if _, err := os.Stat(filepath.Join(e.dir, "docker-compose.mounts.yml")); err == nil {
		t.Error("override written before confirming")
	}
}

func TestMountsRwToggleAndMissingExtraDir(t *testing.T) {
	e := newEnv(t)
	e.prober["/mnt/gone"] = mounts.Probe{}
	m, s := openMounts(e)
	fillAdd(m, "gone", "/mnt/gone")
	if v := view(m); !strings.Contains(v, "目录不存在") {
		t.Errorf("missing dir should show:\n%s", v)
	}
	drive(m, key("enter"))
	if s.st != mForm {
		t.Error("missing external dir must block")
	}
	drive(m, key("esc"))
	fillAdd(m, "ok", "/mnt/ok")
	drive(m, key("down"), key("space"))
	if s.ro {
		t.Error("space should toggle to rw")
	}
	if !strings.Contains(view(m), "读写") {
		t.Errorf("rw label:\n%s", view(m))
	}
}

func TestMountsApplyFlowSuccess(t *testing.T) {
	e := newEnv(t)
	m, s := openMounts(e)
	fillAdd(m, "nas1", "/mnt/nas1")
	drive(m, key("enter"), key("enter")) // review -> apply & restart
	if s.st != mRun || !s.run.done || s.run.err != nil {
		t.Fatalf("state=%d done=%v err=%v\n%s", s.st, s.run.done, s.run.err, view(m))
	}
	if !e.f.Called("up -d --force-recreate api worker") {
		t.Errorf("%v", e.f.Joined())
	}
	v := view(m)
	if !strings.Contains(v, "挂载已应用") {
		t.Errorf("%s", v)
	}
	over, _ := os.ReadFile(filepath.Join(e.dir, "docker-compose.mounts.yml"))
	if !strings.Contains(string(over), "/mnt/nas1:/mnt/storage/nas1:ro") {
		t.Errorf("%s", over)
	}
	drive(m, key("x")) // back to list, reloaded
	if s.st != mList || len(s.cfg.Extra) != 1 {
		t.Errorf("list not refreshed: st=%d extras=%d", s.st, len(s.cfg.Extra))
	}
}

func TestMountsApplyFailureRollsBack(t *testing.T) {
	e := newEnv(t)
	before, _ := os.ReadFile(filepath.Join(e.dir, "mounts.json"))
	boom := &compose.ExitError{Result: compose.Result{ExitCode: 1, Stderr: "bind source path does not exist"}}
	e.f.Rules = append([]*compose.Rule{{Match: "up -d --force-recreate api worker", Result: compose.Result{ExitCode: 1}, Err: boom, Times: 1}}, e.f.Rules...)
	m, s := openMounts(e)
	fillAdd(m, "nas1", "/mnt/nas1")
	drive(m, key("enter"), key("y"))
	if !s.run.done || s.run.err == nil {
		t.Fatalf("expected failure: %+v\n%s", s.run, view(m))
	}
	v := view(m)
	if !strings.Contains(v, "回滚") || !strings.Contains(v, "失败") {
		t.Errorf("rollback notice:\n%s", v)
	}
	after, _ := os.ReadFile(filepath.Join(e.dir, "mounts.json"))
	if string(before) != string(after) {
		t.Errorf("mounts.json not restored:\n%s", after)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "docker-compose.mounts.yml")); err == nil {
		t.Error("override must be removed on rollback")
	}
	ups := 0
	for _, c := range e.f.Joined() {
		if strings.Contains(c, "up -d") {
			ups++
		}
	}
	if ups != 2 {
		t.Errorf("failed up + recovery up expected: %v", e.f.Joined())
	}
}

func TestMountsUnhealthyAfterRestartRollsBack(t *testing.T) {
	e := newEnv(t)
	e.f.Rules = nil
	e.f.On("ps", compose.Result{Stdout: `{"Service":"api","State":"running","Health":"unhealthy"}`}, nil)
	m, s := openMounts(e)
	drive(m, key("A")) // re-apply current config with restart
	if s.st != mReview {
		t.Fatalf("st=%d", s.st)
	}
	drive(m, key("enter"))
	if s.run.err == nil || !strings.Contains(view(m), "回滚") {
		t.Errorf("unhealthy api must trigger rollback:\n%s", view(m))
	}
}

func TestMountsArchiveChangeNeedsTypedAck(t *testing.T) {
	e := newEnv(t)
	e.prober["/srv/oa/archive"] = mounts.Probe{Exists: true, IsDir: true, Mode: 0o750, UID: 10001, GID: 10001, Empty: false}
	m, s := openMounts(e)
	drive(m, key("e")) // archive is the first row
	if s.kind != "core" || s.role != "archive" {
		t.Fatalf("kind=%s role=%s", s.kind, s.role)
	}
	for i := 0; i < 20; i++ {
		drive(m, tea.KeyMsg{Type: tea.KeyBackspace})
	}
	typeText(m, "/srv/new/archive")
	drive(m, key("enter"))
	if s.st != mReview || !s.as.NeedAck {
		t.Fatalf("st=%d ack=%v\n%s", s.st, s.as.NeedAck, view(m))
	}
	v := view(m)
	for _, w := range []string{"不会移动或删除", "/srv/oa/archive", "cp -a"} {
		if !strings.Contains(v, w) {
			t.Errorf("review missing %q:\n%s", w, v)
		}
	}
	drive(m, key("enter")) // confirming the review page is the acknowledgement (no typed word)
	if s.st != mRun || !s.run.done || s.run.err != nil {
		t.Fatalf("st=%d %+v", s.st, s.run)
	}
	b, _ := os.ReadFile(filepath.Join(e.dir, "mounts.json"))
	if !strings.Contains(string(b), "/srv/new/archive") {
		t.Errorf("%s", b)
	}
}

func TestMountsPgdataBlockedWhileRunning(t *testing.T) {
	e := newEnv(t)
	m, s := openMounts(e)
	drive(m, key("down"), key("down"), key("down"), key("e")) // pgdata row
	if s.role != "pgdata" {
		t.Fatalf("role %s", s.role)
	}
	for i := 0; i < 20; i++ {
		drive(m, tea.KeyMsg{Type: tea.KeyBackspace})
	}
	typeText(m, "/srv/newpg")
	if v := flat(view(m)); !strings.Contains(v, flat("oa-agentctl stop")) {
		t.Errorf("must explain how:\n%s", v)
	}
	drive(m, key("enter"))
	if s.st != mForm {
		t.Error("pgdata change must be blocked while running")
	}
}

func TestMountsRemove(t *testing.T) {
	e := newEnv(t)
	cfg := mounts.Config{Version: 1, Archive: mounts.Path{Host: "/srv/oa/archive"}, Spool: mounts.Path{Host: "/srv/oa/spool"}, App: mounts.Path{Host: "/srv/oa/app"}, PGData: mounts.Path{Host: "/srv/oa/pgdata"},
		Extra: []mounts.Extra{{Name: "nas1", Host: "/mnt/nas1", Container: "/mnt/storage/nas1", ReadOnly: true}}}
	mounts.Save(e.dir, cfg)
	m, s := openMounts(e)
	drive(m, key("x")) // core row: not removable
	if s.st != mList {
		t.Error("core mounts cannot be removed")
	}
	for i := 0; i < 4; i++ {
		drive(m, key("down"))
	}
	drive(m, key("x"))
	if s.st != mRemove || !strings.Contains(view(m), "主机上的数据不受影响") {
		t.Fatalf("st=%d\n%s", s.st, view(m))
	}
	drive(m, key("y"), key("w")) // review -> write only
	if s.run.err != nil {
		t.Fatal(s.run.err)
	}
	b, _ := os.ReadFile(filepath.Join(e.dir, "mounts.json"))
	if strings.Contains(string(b), "nas1") {
		t.Errorf("%s", b)
	}
	if e.f.Called("up") {
		t.Error("w must not restart")
	}
}

func TestCompletion(t *testing.T) {
	root := t.TempDir()
	if !strings.HasPrefix(filepath.ToSlash(root), "/") || runtime.GOOS == "windows" {
		t.Skip("POSIX paths needed")
	}
	for _, d := range []string{"alpha", "alpine", "beta", ".hidden"} {
		os.MkdirAll(filepath.Join(root, d), 0o755)
	}
	os.WriteFile(filepath.Join(root, "alfile"), nil, 0o644)
	c := completeDir(root + "/al")
	if len(c) != 2 || !strings.HasSuffix(c[0], "/alpha/") || !strings.HasSuffix(c[1], "/alpine/") {
		t.Errorf("only directories expected: %v", c)
	}
	if got := completeDir(root + "/b"); len(got) != 1 || !strings.HasSuffix(got[0], "/beta/") {
		t.Errorf("%v", got)
	}
	if got := completeDir(root + "/"); len(got) != 3 {
		t.Errorf("hidden dirs must be skipped: %v", got)
	}
	if got := completeDir(root + "/."); len(got) != 1 {
		t.Errorf("hidden on request: %v", got)
	}
	if completeDir("relative") != nil || completeDir(root+"/nope/x") != nil {
		t.Error("no completions expected")
	}
	cur, cands := nextCompletion(root+"/al", nil)
	if !strings.HasSuffix(cur, "/alp") || len(cands) != 2 {
		t.Errorf("common prefix first: %s", cur)
	}
	cur, cands = nextCompletion(cur, cands)
	if !strings.HasSuffix(cur, "/alpha/") {
		t.Errorf("then cycle: %s", cur)
	}
	cur, _ = nextCompletion(cur, cands)
	if !strings.HasSuffix(cur, "/alpine/") {
		t.Errorf("cycle: %s", cur)
	}
	if got := commonPrefix([]string{"/a/x/", "/a/y/"}); got != "/a/" {
		t.Errorf("%s", got)
	}
}

func TestServicesTypedConfirmAndStream(t *testing.T) {
	e := newEnv(t)
	e.f.On("stop", compose.Result{Stdout: "Container x Stopped\n"}, nil)
	m := sized(New(e.b))
	drive(m, key("4"), key("down"), key("enter")) // stop
	s := m.cur.(*services)
	if s.st != 1 || e.f.Called("compose stop") {
		t.Fatal("stop must ask for the typed word")
	}
	typeText(m, "stp")
	drive(m, key("enter"))
	if e.f.Called("compose stop") {
		t.Fatal("wrong word accepted")
	}
	for i := 0; i < 3; i++ {
		drive(m, tea.KeyMsg{Type: tea.KeyBackspace})
	}
	typeText(m, "STOP")
	drive(m, key("enter"))
	if !e.f.Called("compose stop") || !strings.Contains(view(m), "Stopped") || !strings.Contains(view(m), "完成") {
		t.Errorf("%v\n%s", e.f.Joined(), view(m))
	}
	drive(m, key("x"))
	drive(m, key("up"), key("up"), key("enter")) // start
	if !e.f.Called("compose up -d") {
		t.Errorf("%v", e.f.Joined())
	}
}

func TestLogsViewer(t *testing.T) {
	e := newEnv(t)
	e.f.On("logs", compose.Result{Stdout: "alpha one\nbeta two\nalpha three\n"}, nil)
	m := sized(New(e.b))
	drive(m, key("5"), key("down"), key("down"), key("enter")) // api
	if !e.f.Called("logs --no-color --tail 200 --follow api") {
		t.Errorf("%v", e.f.Joined())
	}
	v := view(m)
	for _, w := range []string{"alpha one", "beta two", "跟随"} {
		if !strings.Contains(v, w) {
			t.Errorf("missing %q:\n%s", w, v)
		}
	}
	drive(m, key("f"))
	if !strings.Contains(view(m), "已暂停") {
		t.Error("pause label")
	}
	drive(m, key("/"))
	typeText(m, "three")
	drive(m, key("enter"))
	l := m.cur.(*logsScreen)
	if l.term != "three" || l.follow {
		t.Errorf("search state: %q follow=%v", l.term, l.follow)
	}
	drive(m, key("esc"))
	if l.st != 0 {
		t.Error("esc returns to picker")
	}
}

func TestConfigScreen(t *testing.T) {
	e := newEnv(t)
	m := sized(New(e.b))
	drive(m, key("7"))
	v := view(m)
	if strings.Contains(v, "0123456789abcdef") || strings.Contains(v, "tokentoken") {
		t.Errorf("secrets visible:\n%s", v)
	}
	if !strings.Contains(v, "hidden") || !strings.Contains(v, "OA_PORT") {
		t.Errorf("masked view:\n%s", v)
	}
	c := m.cur.(*configScreen)
	for i, r := range c.rows {
		if r[0] == "OA_PORT" {
			c.cur = i
		}
	}
	drive(m, key("e"))
	for i := 0; i < 6; i++ {
		drive(m, tea.KeyMsg{Type: tea.KeyBackspace})
	}
	typeText(m, "99999")
	if !strings.Contains(view(m), "invalid value") {
		t.Errorf("live validation:\n%s", view(m))
	}
	drive(m, key("enter"))
	if c.st != cEdit {
		t.Error("invalid value must not be saved")
	}
	for i := 0; i < 5; i++ {
		drive(m, tea.KeyMsg{Type: tea.KeyBackspace})
	}
	typeText(m, "9090")
	drive(m, key("enter"))
	b, _ := os.ReadFile(filepath.Join(e.dir, ".env"))
	if !strings.Contains(string(b), "OA_PORT=9090") || !strings.Contains(view(m), "重启后生效") {
		t.Errorf("%s\n%s", b, view(m))
	}
	// secret keys are not editable
	for i, r := range c.rows {
		if r[0] == "OA_SECRET_KEY" {
			c.cur = i
		}
	}
	drive(m, key("e"))
	if c.st != cList || !strings.Contains(view(m), "不可通过界面修改") {
		t.Errorf("locked key:\n%s", view(m))
	}
}

func TestDomainSwitchWarnsAndConfirms(t *testing.T) {
	e := newEnv(t)
	os.WriteFile(filepath.Join(e.dir, "Caddyfile.template"), []byte("__EMAIL_LINE__\n__DOMAIN__ {}\n"), 0o644)
	e.f.Rules = nil
	e.f.On("ps", compose.Result{Stdout: psUp + "\n{\"Service\":\"caddy\",\"State\":\"running\"}"}, nil)
	m := sized(New(e.b))
	drive(m, key("7"), key("d"))
	typeText(m, "not a domain")
	drive(m, key("enter"))
	if !strings.Contains(view(m), "域名格式不正确") {
		t.Errorf("%s", view(m))
	}
	for i := 0; i < 12; i++ {
		drive(m, tea.KeyMsg{Type: tea.KeyBackspace})
	}
	typeText(m, "oa.example.com")
	drive(m, key("enter"))
	if v := view(m); !strings.Contains(v, "Passkey") || !strings.Contains(v, "DOMAIN") {
		t.Errorf("passkey warning + typed confirm expected:\n%s", v)
	}
	drive(m, key("enter"))
	if e.f.Called("up -d") {
		t.Fatal("applied without typed confirmation")
	}
	typeText(m, "DOMAIN")
	drive(m, key("enter"))
	if !e.f.Called("up -d --force-recreate --remove-orphans") {
		t.Errorf("%v", e.f.Joined())
	}
}

func TestBackupRestoreWizard(t *testing.T) {
	e := newEnv(t)
	os.MkdirAll(filepath.Join(e.dir, "backups"), 0o700)
	if m := sized(New(e.b)); true {
		drive(m, key("6"), key("down"), key("down"), key("enter"))
		if v := view(m); !strings.Contains(v, "没有找到") {
			t.Errorf("%s", v)
		}
	}
	os.WriteFile(filepath.Join(e.dir, "backups", "oa-agent-backup-20260101-000000.tar.gz"), []byte("x"), 0o600)
	m := sized(New(e.b))
	drive(m, key("6"), key("down"), key("down"), key("enter"), key("enter"))
	b := m.cur.(*backupScreen)
	if b.st != 2 || !strings.Contains(view(m), "RESTORE") {
		t.Fatalf("typed confirm expected:\n%s", view(m))
	}
	typeText(m, "RESTORE")
	drive(m, key("enter"))
	// the file is not a real backup so Restore fails validation, but it must have been attempted only after the word
	if b.st != 3 || !b.run.done || b.run.err == nil {
		t.Errorf("st=%d %+v", b.st, b.run)
	}
	if e.f.Called("DROP DATABASE") {
		t.Error("invalid backup must not reach the database")
	}
}

func TestDoctorScreen(t *testing.T) {
	e := newEnv(t)
	m := sized(New(e.b))
	drive(m, key("8"))
	v := view(m)
	if !strings.Contains(v, "docker") || !strings.Contains(v, "诊断完成") {
		t.Errorf("%s", v)
	}
	drive(m, key("down"), key("up"), key("r"))
}

func TestNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	InitColors()
	if s := styBad.Render("x"); strings.Contains(s, "\x1b") {
		t.Errorf("colour codes with NO_COLOR: %q", s)
	}
}

func TestStreamIgnoresStaleRuns(t *testing.T) {
	var r runView
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := r.start(ctx, "t", func(ctx context.Context, emit func(string)) error { emit("one"); return nil })
	msgs := []tea.Msg{cmd()}
	for len(msgs) > 0 {
		ok, c := r.update(msgs[0])
		msgs = msgs[1:]
		if ok && c != nil {
			msgs = append(msgs, c())
		}
	}
	if !r.done || len(r.lines) != 1 {
		t.Errorf("%+v", r)
	}
	if ok, _ := r.update(lineMsg{id: -5, s: "stale"}); ok {
		t.Error("stale message accepted")
	}
}

// flat joins wrapped lines so substring checks do not depend on the wrap position.
func flat(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "\n", " ")), "")
}

// Every screen's help bar must fit the 80-column minimum.
func TestHelpBarsFit80(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < len(menuItems); i++ {
		m := sized(New(e.b))
		drive(m, key(string(rune('1'+i))))
		if m.cur == nil {
			t.Fatalf("screen %d did not open", i)
		}
		if w := cells(m.cur.Help()); w > 76 {
			t.Errorf("%s help is %d cells wide: %s", m.cur.Title(), w, m.cur.Help())
		}
	}
	if w := cells(sHelpMenu); w > 76 {
		t.Errorf("menu help %d", w)
	}
}
