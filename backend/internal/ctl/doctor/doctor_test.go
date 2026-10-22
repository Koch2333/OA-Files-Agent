package doctor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"oa-agent/backend/internal/ctl/compose"
	"oa-agent/backend/internal/ctl/install"
	"oa-agent/backend/internal/ctl/mounts"
)

type okProber struct{}

func (okProber) Probe(p string) (mounts.Probe, error) {
	uid := 10001
	if strings.Contains(p, "pg") {
		uid = 70
	}
	return mounts.Probe{Exists: true, IsDir: true, Mode: 0o750, UID: uid, GID: uid, FreeBytes: 500 << 30, TotalBytes: 900 << 30, FSType: "ext4", Empty: true}, nil
}

func rig(t *testing.T, env string) (Env, *compose.Fake) {
	d := t.TempDir()
	os.WriteFile(filepath.Join(d, ".env"), []byte(env), 0o600)
	os.WriteFile(filepath.Join(d, "docker-compose.yml"), []byte("x"), 0o644)
	inst, err := install.Load(d)
	if err != nil {
		t.Fatal(err)
	}
	f := &compose.Fake{}
	return Env{Inst: inst, P: &compose.Project{R: f, Inst: inst}, Prober: okProber{},
		EnvMode: func(string) (os.FileMode, error) { return 0o600, nil }, ClockSync: func() string { return "yes" }}, f
}

const goodEnv = "OA_ARCHIVE_DIR=/srv/a\nOA_SPOOL_DIR=/srv/s\nOA_APP_DIR=/srv/ap\nOA_PG_DIR=/srv/pgdata\nOA_SECRET_KEY=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\nOA_WORKER_TOKEN=0123456789abcdef0123456789abcdef\nOA_PORT=8080\nOA_DOMAIN=x.example.com\nCOMPOSE_FILE=docker-compose.yml:docker-compose.https.yml\n"

const psAll = `{"Service":"postgres","State":"running","Health":"healthy"}
{"Service":"api","State":"running","Health":"healthy","Publishers":[{"URL":"0.0.0.0","TargetPort":8080,"PublishedPort":8080,"Protocol":"tcp"}]}
{"Service":"worker","State":"running"}
{"Service":"caddy","State":"running","Health":"healthy"}`

func find(rs []Result, name string) *Result {
	for i := range rs {
		if rs[i].Name == name {
			return &rs[i]
		}
	}
	return nil
}

func TestAllGood(t *testing.T) {
	e, f := rig(t, goodEnv)
	f.On("ps", compose.Result{Stdout: psAll}, nil)
	rs := Run(context.Background(), e)
	fails, warns := Counts(rs)
	if fails != 0 {
		for _, r := range rs {
			t.Logf("%s %s %s", r.Status, r.Name, r.Detail)
		}
		t.Fatalf("fails=%d", fails)
	}
	if warns != 1 || find(rs, "mounts.json") == nil { // default layout, no mounts.json
		t.Errorf("warns=%d", warns)
	}
}

func TestDockerDown(t *testing.T) {
	e, f := rig(t, goodEnv)
	f.On("version", compose.Result{ExitCode: 1}, compose.ErrDockerMissing)
	rs := Run(context.Background(), e)
	if len(rs) != 1 || rs[0].Level != Fail {
		t.Fatalf("%+v", rs)
	}
}

func TestProblems(t *testing.T) {
	e, f := rig(t, "OA_ARCHIVE_DIR=/srv/a\nOA_SPOOL_DIR=/srv/s\nOA_APP_DIR=/srv/ap\nOA_PG_DIR=/srv/pgdata\nOA_SECRET_KEY=short\nOA_WORKER_TOKEN=x\nOA_PORT=9999\n")
	e.EnvMode = func(string) (os.FileMode, error) { return 0o644, nil }
	e.ClockSync = func() string { return "no" }
	f.On("psql", compose.Result{ExitCode: 1}, &compose.ExitError{Result: compose.Result{ExitCode: 1}})
	f.On("ps", compose.Result{Stdout: `{"Service":"postgres","State":"running","Health":"healthy"}
{"Service":"api","State":"exited"}`}, nil)
	rs := Run(context.Background(), e)
	expect := map[string]Level{
		"env-mode": Warn, "secret-key": Fail, "worker-token": Fail, "service:api": Fail, "service:worker": Fail,
		"port": Fail, "database": Fail, "https": Warn, "clock": Warn, "service:postgres": OK,
	}
	for n, l := range expect {
		r := find(rs, n)
		if r == nil || r.Level != l {
			t.Errorf("%s: %+v want %v", n, r, l)
		}
	}
	if r := find(rs, "env-mode"); r != nil && !strings.Contains(r.Fix, "chmod 600") {
		t.Error("fix command missing")
	}
	for _, r := range rs {
		if strings.Contains(r.Detail, "short") && r.Name == "secret-key" {
			t.Error("secret value echoed")
		}
	}
}

func TestOverrideDrift(t *testing.T) {
	e, f := rig(t, goodEnv)
	f.On("ps", compose.Result{Stdout: psAll}, nil)
	cfg := mounts.Config{Version: 1, Archive: mounts.Path{Host: "/srv/a"}, Spool: mounts.Path{Host: "/srv/s"}, App: mounts.Path{Host: "/srv/ap"}, PGData: mounts.Path{Host: "/srv/pgdata"}}
	mounts.Save(e.Inst.Dir, cfg)
	rs := Run(context.Background(), e)
	if r := find(rs, "override"); r == nil || r.Level != Warn {
		t.Errorf("missing override should warn: %+v", r)
	}
	if r := find(rs, "compose-file"); r == nil || r.Level != Fail {
		t.Errorf("COMPOSE_FILE without override should fail: %+v", r)
	}
	over, _ := mounts.Render(cfg, mounts.Options{})
	os.WriteFile(e.Inst.OverridePath(), over, 0o644)
	e.Inst.Env.Set("COMPOSE_FILE", "docker-compose.yml:docker-compose.mounts.yml")
	rs = Run(context.Background(), e)
	if r := find(rs, "override"); r == nil || r.Level != OK {
		t.Errorf("in sync: %+v", r)
	}
}
