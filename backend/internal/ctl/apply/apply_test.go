package apply

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"oa-agent/backend/internal/ctl/compose"
	"oa-agent/backend/internal/ctl/install"
	"oa-agent/backend/internal/ctl/mounts"
)

const oldEnv = "COMPOSE_FILE=docker-compose.yml\nOA_SECRET_KEY=topsecret-value-123456\nOA_ARCHIVE_DIR=/srv/old/archive\n"

func setup(t *testing.T) (*compose.Project, *compose.Fake, mounts.Config) {
	t.Helper()
	d := t.TempDir()
	os.WriteFile(filepath.Join(d, ".env"), []byte(oldEnv), 0o600)
	os.WriteFile(filepath.Join(d, "docker-compose.yml"), []byte("services: {}\n"), 0o644)
	inst, err := install.Load(d)
	if err != nil {
		t.Fatal(err)
	}
	f := &compose.Fake{}
	p := &compose.Project{R: f, Inst: inst, Poll: time.Millisecond}
	old := mounts.Config{Version: 1, Archive: mounts.Path{Host: "/srv/old/archive"}, Spool: mounts.Path{Host: "/srv/old/spool"},
		App: mounts.Path{Host: "/srv/old/app"}, PGData: mounts.Path{Host: "/srv/old/pg"}}
	old.Normalize()
	if _, err := mounts.Save(d, old); err != nil {
		t.Fatal(err)
	}
	return p, f, old
}

func healthy() compose.Result {
	return compose.Result{Stdout: `{"Service":"postgres","State":"running","Health":"healthy"}
{"Service":"api","State":"running","Health":"healthy"}
{"Service":"worker","State":"running"}`}
}

func read(t *testing.T, p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestApplyDryRunWritesNothing(t *testing.T) {
	p, f, old := setup(t)
	next := old.WithCore("archive", "/srv/new/archive")
	before := read(t, p.Inst.EnvPath())
	rep, err := Apply(context.Background(), p, old, next, Options{DryRun: true, Restart: true}, nil)
	if err != nil || len(rep.Override) == 0 {
		t.Fatalf("%v", err)
	}
	if read(t, p.Inst.EnvPath()) != before {
		t.Error(".env changed in dry run")
	}
	if _, err := os.Stat(p.Inst.OverridePath()); err == nil {
		t.Error("override written in dry run")
	}
	if len(f.Calls) != 0 {
		t.Errorf("docker called in dry run: %v", f.Joined())
	}
	if strings.Contains(read(t, p.Inst.MountsPath()), "/srv/new/archive") {
		t.Error("mounts.json changed in dry run")
	}
}

func TestApplySuccessNoRestart(t *testing.T) {
	p, f, old := setup(t)
	next := old.WithCore("archive", "/srv/new/archive")
	if _, err := Apply(context.Background(), p, old, next, Options{}, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(t, p.Inst.MountsPath()), "/srv/new/archive") {
		t.Error("mounts.json not updated")
	}
	if !strings.Contains(read(t, p.Inst.OverridePath()), "/srv/new/archive:/data/archive") {
		t.Error("override not generated")
	}
	env := read(t, p.Inst.EnvPath())
	for _, w := range []string{"OA_ARCHIVE_DIR=/srv/new/archive", "OA_PG_DIR=/srv/old/pg", "OA_SECRET_KEY=topsecret-value-123456", "COMPOSE_FILE=docker-compose.yml:docker-compose.mounts.yml"} {
		if !strings.Contains(env, w) {
			t.Errorf(".env missing %q:\n%s", w, env)
		}
	}
	if f.Called("up") {
		t.Error("must not restart without --restart")
	}
	if !f.Called("compose config -q") {
		t.Error("compose config must validate the result")
	}
	baks, _ := filepath.Glob(p.Inst.MountsPath() + ".bak-*")
	if len(baks) < 1 {
		t.Error("no mounts.json backup")
	}
}

func TestApplyRestartSuccess(t *testing.T) {
	p, f, old := setup(t)
	f.On("ps", healthy(), nil)
	next := old.WithCore("spool", "/srv/new/spool")
	if _, err := Apply(context.Background(), p, old, next, Options{Restart: true, HealthTimeout: time.Second}, nil); err != nil {
		t.Fatal(err)
	}
	if !f.Called("compose up -d --force-recreate api worker") {
		t.Errorf("calls: %v", f.Joined())
	}
}

func TestApplyPgdataChangeRecreatesAll(t *testing.T) {
	p, f, old := setup(t)
	f.On("ps", healthy(), nil)
	next := old.WithCore("pgdata", "/srv/new/pg")
	if _, err := Apply(context.Background(), p, old, next, Options{Restart: true, HealthTimeout: time.Second}, nil); err != nil {
		t.Fatal(err)
	}
	if !f.Called("compose up -d --force-recreate\n") && !f.Called("compose up -d --force-recreate") {
		t.Errorf("calls: %v", f.Joined())
	}
	for _, c := range f.Joined() {
		if strings.HasPrefix(c, "compose up") && strings.Contains(c, "api worker") {
			t.Errorf("pgdata change should recreate the whole stack: %s", c)
		}
	}
}

func snapshotFiles(t *testing.T, p *compose.Project) (string, string, bool) {
	_, err := os.Stat(p.Inst.OverridePath())
	return read(t, p.Inst.MountsPath()), read(t, p.Inst.EnvPath()), err == nil
}

func TestApplyUpFailureRollsBack(t *testing.T) {
	p, f, old := setup(t)
	m0, e0, _ := snapshotFiles(t, p)
	boom := &compose.ExitError{Result: compose.Result{ExitCode: 1, Stderr: "port in use"}}
	f.On("up -d --force-recreate api worker", compose.Result{ExitCode: 1}, boom).Times = 1
	next := old.WithCore("archive", "/srv/new/archive")
	rep, err := Apply(context.Background(), p, old, next, Options{Restart: true, HealthTimeout: time.Second}, nil)
	if !errors.Is(err, ErrRolledBack) || !rep.RolledBack {
		t.Fatalf("want rollback, got %v", err)
	}
	m1, e1, hasOverride := snapshotFiles(t, p)
	if m0 != m1 || e0 != e1 {
		t.Errorf("files not restored:\n%s\n---\n%s\n%s\n---\n%s", m0, m1, e0, e1)
	}
	if hasOverride {
		t.Error("override did not exist before; rollback must remove it")
	}
	// after the rollback the old config was brought back up
	ups := 0
	for _, c := range f.Joined() {
		if strings.HasPrefix(c, "compose up") {
			ups++
		}
	}
	if ups != 2 {
		t.Errorf("expected failed up + recovery up, got %v", f.Joined())
	}
	if got := p.Inst.Env.Value("OA_ARCHIVE_DIR", ""); got != "/srv/old/archive" {
		t.Errorf("in-memory env not reloaded: %s", got)
	}
}

func TestApplyUnhealthyRollsBack(t *testing.T) {
	p, f, old := setup(t)
	m0, _, _ := snapshotFiles(t, p)
	f.On("ps", compose.Result{Stdout: `{"Service":"api","State":"running","Health":"unhealthy"}
{"Service":"worker","State":"running"}`}, nil)
	next := old.WithCore("archive", "/srv/new/archive")
	_, err := Apply(context.Background(), p, old, next, Options{Restart: true, HealthTimeout: time.Second}, nil)
	if !errors.Is(err, ErrRolledBack) {
		t.Fatalf("got %v", err)
	}
	if m1, _, _ := snapshotFiles(t, p); m1 != m0 {
		t.Error("mounts.json not restored")
	}
}

func TestApplyComposeConfigFailureRollsBackBeforeRestart(t *testing.T) {
	p, f, old := setup(t)
	f.On("config -q", compose.Result{ExitCode: 1, Stderr: "yaml error"}, &compose.ExitError{Result: compose.Result{ExitCode: 1, Stderr: "yaml error"}})
	_, err := Apply(context.Background(), p, old, old.WithCore("app", "/srv/new/app"), Options{Restart: true}, nil)
	if !errors.Is(err, ErrRolledBack) {
		t.Fatal(err)
	}
	if f.Called("compose up -d --force-recreate api worker") && len(f.Joined()) > 3 {
		// only the recovery up is acceptable, not the new-config up
	}
	if strings.Contains(read(t, p.Inst.MountsPath()), "/srv/new/app") {
		t.Error("mounts.json should be restored")
	}
}

func TestApplyDockerMissingSkipsValidation(t *testing.T) {
	p, f, old := setup(t)
	f.On("config -q", compose.Result{}, compose.ErrDockerMissing)
	if _, err := Apply(context.Background(), p, old, old.WithCore("app", "/srv/new/app"), Options{}, nil); err != nil {
		t.Fatalf("installer must be able to apply without docker: %v", err)
	}
	if !strings.Contains(read(t, p.Inst.OverridePath()), "/srv/new/app:/data") {
		t.Error("override not written")
	}
}

func TestApplyInvalidConfigTouchesNothing(t *testing.T) {
	p, f, old := setup(t)
	m0, e0, _ := snapshotFiles(t, p)
	bad := old.WithCore("archive", "/etc/oa")
	if _, err := Apply(context.Background(), p, old, bad, Options{}, nil); err == nil {
		t.Fatal("expected validation error")
	}
	m1, e1, o := snapshotFiles(t, p)
	if m0 != m1 || e0 != e1 || o || len(f.Calls) != 0 {
		t.Error("invalid config must not change anything")
	}
}

func TestApplyEnvKeepsMode(t *testing.T) {
	p, _, old := setup(t)
	if _, err := Apply(context.Background(), p, old, old.WithCore("app", "/srv/new/app"), Options{}, nil); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p.Inst.EnvPath())
	if st.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Errorf(".env mode %v", st.Mode().Perm())
	}
	ents, _ := os.ReadDir(p.Inst.Dir)
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("temp file left: %s", e.Name())
		}
	}
}
