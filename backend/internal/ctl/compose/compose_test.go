package compose

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"oa-agent/backend/internal/ctl/install"
)

func newInst(t *testing.T, env string) *install.Install {
	t.Helper()
	d := t.TempDir()
	os.WriteFile(filepath.Join(d, ".env"), []byte(env), 0o600)
	os.WriteFile(filepath.Join(d, "docker-compose.yml"), []byte("services: {}\n"), 0o644)
	inst, err := install.Load(d)
	if err != nil {
		t.Fatal(err)
	}
	return inst
}

const psND = `{"Service":"postgres","State":"running","Health":"healthy","Status":"Up 3 hours (healthy)","Image":"postgres:16-alpine","Publishers":[]}
{"Service":"api","State":"running","Health":"healthy","Status":"Up 2 hours (healthy)","Publishers":[{"URL":"0.0.0.0","TargetPort":8080,"PublishedPort":8080,"Protocol":"tcp"},{"URL":"::","TargetPort":8080,"PublishedPort":8080,"Protocol":"tcp"}]}
{"Service":"worker","State":"exited","Health":"","Status":"Exited (1) 5 minutes ago"}
`

func TestParsePS(t *testing.T) {
	svcs, err := ParsePS(psND)
	if err != nil || len(svcs) != 3 {
		t.Fatalf("%v %v", err, svcs)
	}
	if svcs[1].Ports != "0.0.0.0:8080->8080/tcp" {
		t.Errorf("ports dedupe: %q", svcs[1].Ports)
	}
	if svcs[0].Uptime() != "3 hours" || svcs[1].Uptime() != "2 hours" || svcs[2].Uptime() != "-" {
		t.Errorf("uptime: %q %q %q", svcs[0].Uptime(), svcs[1].Uptime(), svcs[2].Uptime())
	}
	if !svcs[0].OK() || svcs[2].OK() || svcs[2].Effective() != "exited" {
		t.Error("effective state")
	}
	arr := `[{"Service":"api","State":"running","Health":"","Status":"Up 1 minute"}]`
	s2, err := ParsePS(arr)
	if err != nil || len(s2) != 1 || !s2[0].OK() || s2[0].Uptime() != "1 minute" {
		t.Fatalf("array form: %v %+v", err, s2)
	}
	if s, err := ParsePS("  \n"); err != nil || s != nil {
		t.Error("empty output")
	}
	if _, err := ParsePS("not json"); err == nil {
		t.Error("garbage must error")
	}
}

func TestSpecUsesEnv(t *testing.T) {
	inst := newInst(t, "COMPOSE_FILE=docker-compose.yml:docker-compose.mounts.yml\nCOMPOSE_PROFILES=https\n")
	f := &Fake{}
	p := &Project{R: f, Inst: inst}
	p.Run(context.Background(), "ps")
	c := f.Calls[0]
	if c.Dir != inst.Dir || strings.Join(c.Args, " ") != "compose ps" {
		t.Errorf("%+v", c)
	}
	env := strings.Join(c.Env, "|")
	if !strings.Contains(env, "COMPOSE_FILE=docker-compose.yml:docker-compose.mounts.yml") || !strings.Contains(env, "COMPOSE_PROFILES=https") {
		t.Errorf("env %v", c.Env)
	}
	if strings.Contains(c.String(), "COMPOSE_") {
		t.Error("String() must not print env")
	}
}

func TestTableAddsMissing(t *testing.T) {
	inst := newInst(t, "COMPOSE_PROFILES=https\n")
	f := &Fake{}
	f.On("ps", Result{Stdout: psND}, nil)
	p := &Project{R: f, Inst: inst}
	tb, err := p.Table(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, s := range tb {
		names = append(names, s.Name+":"+s.Effective())
	}
	if strings.Join(names, ",") != "postgres:healthy,api:healthy,worker:exited,caddy:missing" {
		t.Errorf("%v", names)
	}
}

func TestWaitHealthy(t *testing.T) {
	inst := newInst(t, "")
	mk := func() (*Project, *Fake) {
		f := &Fake{}
		return &Project{R: f, Inst: inst, Poll: time.Millisecond}, f
	}
	// becomes healthy on the third poll
	p, f := mk()
	starting := `{"Service":"api","State":"running","Health":"starting"}`
	healthy := `{"Service":"api","State":"running","Health":"healthy"}`
	r1 := f.On("ps", Result{Stdout: starting}, nil)
	r1.Times = 2
	f.On("ps", Result{Stdout: healthy}, nil)
	if err := p.WaitHealthy(context.Background(), []string{"api"}, time.Second); err != nil {
		t.Fatal(err)
	}
	// unhealthy fails fast
	p, f = mk()
	f.On("ps", Result{Stdout: `{"Service":"api","State":"running","Health":"unhealthy"}`}, nil)
	if err := p.WaitHealthy(context.Background(), []string{"api"}, time.Minute); err == nil || !strings.Contains(err.Error(), "unhealthy") {
		t.Fatalf("got %v", err)
	}
	// never ready: timeout
	p, f = mk()
	f.On("ps", Result{Stdout: starting}, nil)
	if err := p.WaitHealthy(context.Background(), []string{"api"}, 30*time.Millisecond); err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("got %v", err)
	}
	// service absent counts as not ready
	p, f = mk()
	f.On("ps", Result{Stdout: ""}, nil)
	if err := p.WaitHealthy(context.Background(), []string{"api"}, 30*time.Millisecond); err == nil {
		t.Fatal("missing service must time out")
	}
}

func TestFakeStreamAndError(t *testing.T) {
	f := &Fake{}
	f.On("up", Result{Stdout: "a\nb\n", ExitCode: 1}, errors.New("boom"))
	var lines []string
	err := f.Stream(context.Background(), Spec{Args: []string{"compose", "up"}}, func(l string) { lines = append(lines, l) })
	if err == nil || len(lines) != 2 {
		t.Fatalf("%v %v", err, lines)
	}
}

func TestExitErrorMessage(t *testing.T) {
	e := &ExitError{Spec: Spec{Args: []string{"compose", "up"}}, Result: Result{Stderr: strings.Repeat("x", 1000), ExitCode: 2}}
	if len(e.Error()) > 700 {
		t.Error("message must be truncated")
	}
}
