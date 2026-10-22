package oaadmin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"oa-agent/backend/internal/ctl/compose"
	"oa-agent/backend/internal/ctl/install"
)

func TestParseUsersJSON(t *testing.T) {
	cases := []struct {
		name, in string
		want     []User
	}{
		{"array", `[{"username":"alice","role":"admin","status":"active","mfa":true,"locked":false},{"username":"bob","role":"user","disabled":true,"totp_enabled":false,"locked_until":"2026-01-01T00:00:00Z"}]`,
			[]User{{"alice", "admin", "active", true, false}, {"bob", "user", "disabled", false, true}}},
		{"wrapped", `{"users":[{"name":"carol","role":"auditor","active":false,"mfa_enabled":"yes"}]}`,
			[]User{{"carol", "auditor", "disabled", true, false}}},
		{"skip nameless", `[{"role":"x"},{"user":"dave"}]`, []User{{"dave", "", "active", false, false}}},
		{"null locked_until", `[{"username":"e","locked_until":null}]`, []User{{"e", "", "active", false, false}}},
	}
	for _, c := range cases {
		got, err := ParseUsersJSON(c.in)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if len(got) != len(c.want) {
			t.Errorf("%s: %+v", c.name, got)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s[%d]: %+v want %+v", c.name, i, got[i], c.want[i])
			}
		}
	}
	for _, bad := range []string{"", "not json", `{"foo":1}`, `[1,2]`} {
		if _, err := ParseUsersJSON(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}

func TestParseUsersTable(t *testing.T) {
	tbl := `USERNAME   ROLE      STATUS     MFA   LOCKED
--------   ----      ------     ---   ------
alice      admin     active     yes   no
bob        user      disabled   no    yes
`
	us, err := ParseUsersTable(tbl)
	if err != nil || len(us) != 2 {
		t.Fatalf("%v %+v", err, us)
	}
	if us[0] != (User{"alice", "admin", "active", true, false}) || us[1] != (User{"bob", "user", "disabled", false, true}) {
		t.Errorf("%+v", us)
	}
	pipes := "| user | role | status | mfa |\n|------|------|--------|-----|\n| zed | admin | active | true |\n"
	us, err = ParseUsersTable(pipes)
	if err != nil || len(us) != 1 || us[0].Username != "zed" || !us[0].MFA {
		t.Fatalf("pipes: %v %+v", err, us)
	}
	zh := "用户名  角色  状态  MFA  锁定\n张三  admin  active  是  否\n"
	us, err = ParseUsersTable(zh)
	if err != nil || len(us) != 1 || us[0].Username != "张三" || !us[0].MFA {
		t.Fatalf("zh: %v %+v", err, us)
	}
	for _, bad := range []string{"", "no table here\njust text", "a  b  c\n1  2  3"} {
		if _, err := ParseUsersTable(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}

func TestParseSecret(t *testing.T) {
	cases := []struct{ in, pw string }{
		{"Temporary password: Abc123xyz\nmust change on login", "Abc123xyz"},
		{"user alice\ntemp password = Q!w2e3\n", "Q!w2e3"},
		{"临时密码：Zz9988\n", "Zz9988"},
		{"New password: s3cret-value", "s3cret-value"},
		{"nothing useful", ""},
	}
	for _, c := range cases {
		s := ParseSecret(c.in)
		if s.Password != c.pw {
			t.Errorf("%q: got %q", c.in, s.Password)
		}
		if c.pw != "" && strings.Contains(s.Output, c.pw) {
			t.Errorf("redacted output leaks the password: %q", s.Output)
		}
	}
}

func TestValidUsername(t *testing.T) {
	for _, u := range []string{"alice", "a.b-c_d", "x@y.com", "A1"} {
		if ValidUsername(u) != nil {
			t.Errorf("%s rejected", u)
		}
	}
	for _, u := range []string{"", "-rf", "a b", "a;b", "$(x)", "a\nb", ".x", strings.Repeat("a", 65), "--help"} {
		if ValidUsername(u) == nil {
			t.Errorf("%q accepted", u)
		}
	}
}

func client(t *testing.T) (*Client, *compose.Fake) {
	d := t.TempDir()
	os.WriteFile(filepath.Join(d, ".env"), []byte("OA_PORT=8080\n"), 0o600)
	os.WriteFile(filepath.Join(d, "docker-compose.yml"), []byte("x"), 0o644)
	inst, err := install.Load(d)
	if err != nil {
		t.Fatal(err)
	}
	f := &compose.Fake{}
	return &Client{P: &compose.Project{R: f, Inst: inst}}, f
}

func TestExecVsRun(t *testing.T) {
	c, f := client(t)
	f.On("ps", compose.Result{Stdout: `{"Service":"api","State":"running","Health":"healthy"}`}, nil)
	f.On("reset-mfa", compose.Result{Stdout: "ok"}, nil)
	if _, err := c.Simple(context.Background(), "reset-mfa", "alice"); err != nil {
		t.Fatal(err)
	}
	if !f.Called("compose exec -T api oa-admin reset-mfa alice") {
		t.Errorf("running api must use exec: %v", f.Joined())
	}
	c, f = client(t)
	f.On("ps", compose.Result{Stdout: `{"Service":"api","State":"exited"}`}, nil)
	c.Simple(context.Background(), "unlock", "bob")
	if !f.Called("compose run --rm -T api oa-admin unlock bob") {
		t.Errorf("stopped api must use run --rm: %v", f.Joined())
	}
}

func TestListUsersFallsBackToTable(t *testing.T) {
	c, f := client(t)
	f.On("ps", compose.Result{Stdout: `{"Service":"api","State":"running"}`}, nil)
	f.On("list-users --json", compose.Result{Stderr: "unknown flag --json", ExitCode: 2}, &compose.ExitError{Result: compose.Result{ExitCode: 2}})
	f.On("list-users", compose.Result{Stdout: "USERNAME  ROLE\nalice  admin\n"}, nil)
	us, err := c.ListUsers(context.Background())
	if err != nil || len(us) != 1 || us[0].Username != "alice" {
		t.Fatalf("%v %+v", err, us)
	}
}

func TestResetPasswordErrors(t *testing.T) {
	c, f := client(t)
	f.On("ps", compose.Result{Stdout: `{"Service":"api","State":"running"}`}, nil)
	if _, err := c.ResetPassword(context.Background(), "bad name"); err == nil {
		t.Error("invalid username must be rejected before running anything")
	}
	if f.Called("oa-admin") {
		t.Error("nothing should have run")
	}
	f.On("reset-password", compose.Result{Stderr: "user not found, password: leaked1", ExitCode: 1}, &compose.ExitError{Result: compose.Result{Stderr: "user not found, password: leaked1", ExitCode: 1}})
	_, err := c.ResetPassword(context.Background(), "ghost")
	if err == nil || strings.Contains(err.Error(), "leaked1") {
		t.Errorf("error must be redacted: %v", err)
	}
	var ee *compose.ExitError
	if errors.As(err, &ee) {
		t.Log("wrapped error exposes ExitError; message already redacted")
	}
}
