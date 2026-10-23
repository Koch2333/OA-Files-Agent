package ops

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"oa-agent/backend/internal/ctl/compose"
	"oa-agent/backend/internal/ctl/install"
	"oa-agent/backend/internal/ctl/mounts"
)

type rig struct {
	e       Env
	f       *compose.Fake
	dir     string
	archive string
	app     string
	lines   []string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "install")
	os.MkdirAll(dir, 0o755)
	// host paths in mounts.json are POSIX strings; on Windows the temp dir has a drive letter, so these tests run on Linux
	r := &rig{dir: dir, archive: filepath.ToSlash(filepath.Join(root, "archive")), app: filepath.ToSlash(filepath.Join(root, "app"))}
	os.MkdirAll(r.archive, 0o755)
	os.MkdirAll(r.app, 0o755)
	os.WriteFile(filepath.Join(dir, ".env"), []byte("OA_SECRET_KEY=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\nOA_VERSION=1.0\nCOMPOSE_FILE=docker-compose.yml\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte("x"), 0o644)
	cfg := mounts.Config{Version: 1, Archive: mounts.Path{Host: r.archive}, Spool: mounts.Path{Host: filepath.ToSlash(filepath.Join(root, "spool"))},
		App: mounts.Path{Host: r.app}, PGData: mounts.Path{Host: filepath.ToSlash(filepath.Join(root, "pg"))}}
	if _, err := mounts.Save(dir, cfg); err != nil {
		t.Fatal(err)
	}
	inst, err := install.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	r.f = &compose.Fake{}
	p := &compose.Project{R: r.f, Inst: inst, Poll: time.Millisecond}
	r.e = Env{P: p, Timeout: time.Second, Prober: mounts.OSProber{}, Getuid: func() int { return 1000 },
		Now: func() time.Time { return time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC) }}
	return r
}

func (r *rig) pr(s string) { r.lines = append(r.lines, s) }

const allHealthy = `{"Service":"postgres","State":"running","Health":"healthy"}
{"Service":"api","State":"running","Health":"healthy"}
{"Service":"worker","State":"running"}`

func TestBackupRestoreRoundTrip(t *testing.T) {
	r := newRig(t)
	os.WriteFile(filepath.Join(r.app, "secret.key"), []byte("KEYDATA"), 0o600)
	os.MkdirAll(filepath.Join(r.archive, "2026", "01"), 0o755)
	os.WriteFile(filepath.Join(r.archive, "2026", "01", "doc.pdf"), []byte("PDFBYTES"), 0o644)
	r.f.On("ps", compose.Result{Stdout: allHealthy}, nil)
	r.f.On("pg_dump", compose.Result{Stdout: "PGDUMPDATA"}, nil)
	file, err := r.e.Backup(context.Background(), BackupOptions{}, r.pr)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(file) != "oa-agent-backup-20260506-070809.tar.gz" {
		t.Errorf("name %s", file)
	}
	if st, _ := os.Stat(file); st.Mode().Perm() != 0o600 && os.PathSeparator == '/' {
		t.Errorf("backup mode %v", st.Mode().Perm())
	}
	// layout must match the bash tool
	names := tarNames(t, file)
	for _, want := range []string{"./db.dump", "./env", "./app/secret.key", "./archive/2026/01/doc.pdf", "./MANIFEST"} {
		if !contains(names, want) {
			t.Errorf("missing %s in %v", want, names)
		}
	}
	if err := ValidateBackup(file); err != nil {
		t.Fatal(err)
	}

	// destroy and restore
	os.RemoveAll(filepath.Join(r.archive, "2026"))
	os.WriteFile(filepath.Join(r.archive, "newer.txt"), []byte("n"), 0o644)
	os.WriteFile(filepath.Join(r.app, "secret.key"), []byte("OTHER"), 0o600)
	if err := r.e.Restore(context.Background(), file, r.pr); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(r.archive, "2026", "01", "doc.pdf")); string(b) != "PDFBYTES" {
		t.Errorf("archive not restored: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(r.app, "secret.key")); string(b) != "KEYDATA" {
		t.Errorf("secret.key not restored: %q", b)
	}
	// the previous archive is kept, not deleted
	old := r.archive + ".pre-restore-20260506-070809"
	if b, err := os.ReadFile(filepath.Join(old, "newer.txt")); err != nil || string(b) != "n" {
		t.Errorf("old archive not preserved: %v", err)
	}
	for _, want := range []string{"DROP DATABASE", "CREATE DATABASE", "pg_restore", "compose stop worker api"} {
		if !r.f.Called(want) {
			t.Errorf("missing call %q: %v", want, r.f.Joined())
		}
	}
}

func TestBackupNeedsPostgres(t *testing.T) {
	r := newRig(t)
	r.f.On("ps", compose.Result{Stdout: `{"Service":"postgres","State":"exited"}`}, nil)
	if _, err := r.e.Backup(context.Background(), BackupOptions{}, r.pr); err == nil {
		t.Fatal("expected error")
	}
	ents, _ := os.ReadDir(filepath.Join(r.dir, "backups"))
	if len(ents) != 0 {
		t.Errorf("no files expected, got %v", ents)
	}
}

func TestBackupEmptyDumpFails(t *testing.T) {
	r := newRig(t)
	r.f.On("ps", compose.Result{Stdout: allHealthy}, nil)
	if _, err := r.e.Backup(context.Background(), BackupOptions{}, r.pr); err == nil {
		t.Fatal("empty pg_dump must fail")
	}
	ents, _ := os.ReadDir(filepath.Join(r.dir, "backups"))
	if len(ents) != 0 {
		t.Errorf("leftovers: %v", ents)
	}
}

func TestBackupSkipArchive(t *testing.T) {
	r := newRig(t)
	os.WriteFile(filepath.Join(r.archive, "f"), []byte("x"), 0o644)
	r.f.On("ps", compose.Result{Stdout: allHealthy}, nil)
	r.f.On("pg_dump", compose.Result{Stdout: "D"}, nil)
	file, err := r.e.Backup(context.Background(), BackupOptions{SkipArchive: true, OutDir: filepath.Join(r.dir, "out")}, r.pr)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range tarNames(t, file) {
		if strings.HasPrefix(n, "./archive") {
			t.Errorf("archive included: %s", n)
		}
	}
}

func writeTar(t *testing.T, path string, entries map[string]string, extra func(*tar.Writer)) {
	f, _ := os.Create(path)
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for n, c := range entries {
		tw.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: int64(len(c)), Typeflag: tar.TypeReg})
		tw.Write([]byte(c))
	}
	if extra != nil {
		extra(tw)
	}
	tw.Close()
	gz.Close()
	f.Close()
}

func TestRestoreRejectsPathTraversalAndLinks(t *testing.T) {
	r := newRig(t)
	r.f.On("ps", compose.Result{Stdout: allHealthy}, nil)
	evil := filepath.Join(r.dir, "evil.tar.gz")
	writeTar(t, evil, map[string]string{"./db.dump": "D", "./archive/../../escape.txt": "pwn", "../outside.txt": "pwn"}, func(tw *tar.Writer) {
		tw.WriteHeader(&tar.Header{Name: "./archive/link", Linkname: "/etc/passwd", Typeflag: tar.TypeSymlink})
	})
	if err := r.e.Restore(context.Background(), evil, r.pr); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Dir(r.dir)
	for _, p := range []string{filepath.Join(parent, "escape.txt"), filepath.Join(parent, "outside.txt"), filepath.Join(filepath.Dir(parent), "outside.txt")} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("traversal escaped to %s", p)
		}
	}
	if _, err := os.Lstat(filepath.Join(r.archive, "link")); err == nil {
		t.Error("symlink was recreated")
	}
}

func TestValidateBackupRejects(t *testing.T) {
	d := t.TempDir()
	bad := filepath.Join(d, "x.tar.gz")
	writeTar(t, bad, map[string]string{"./other": "x"}, nil)
	if ValidateBackup(bad) == nil {
		t.Error("no db.dump")
	}
	os.WriteFile(filepath.Join(d, "junk"), []byte("junk"), 0o644)
	if ValidateBackup(filepath.Join(d, "junk")) == nil {
		t.Error("not gzip")
	}
	if ValidateBackup(filepath.Join(d, "missing")) == nil {
		t.Error("missing file")
	}
}

func TestRestoreAdoptsBackupSecretKey(t *testing.T) {
	r := newRig(t)
	r.f.On("ps", compose.Result{Stdout: allHealthy}, nil)
	f := filepath.Join(r.dir, "b.tar.gz")
	writeTar(t, f, map[string]string{"./db.dump": "D", "./env": "OA_SECRET_KEY=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\n"}, nil)
	if err := r.e.Restore(context.Background(), f, r.pr); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(r.dir, ".env"))
	if !strings.Contains(string(b), "OA_SECRET_KEY=bbbb") {
		t.Errorf("key not adopted:\n%s", b)
	}
	for _, l := range r.lines {
		if strings.Contains(l, "bbbbbbbb") {
			t.Errorf("secret printed in progress: %s", l)
		}
	}
}

func TestUpgradeFlow(t *testing.T) {
	r := newRig(t)
	r.f.On("ps", compose.Result{Stdout: allHealthy}, nil)
	r.f.On("pg_dump", compose.Result{Stdout: "D"}, nil)
	if err := r.e.Upgrade(context.Background(), UpgradeOptions{Version: "1.1"}, r.pr); err != nil {
		t.Fatal(err)
	}
	calls := strings.Join(r.f.Joined(), "\n")
	iBackup, iPull, iUp := strings.Index(calls, "pg_dump"), strings.Index(calls, "compose pull"), strings.Index(calls, "up -d --force-recreate api worker")
	if !(iBackup >= 0 && iBackup < iPull && iPull < iUp) {
		t.Errorf("order wrong:\n%s", calls)
	}
	if b, _ := os.ReadFile(filepath.Join(r.dir, ".env")); !strings.Contains(string(b), "OA_VERSION=1.1") {
		t.Error("version not set")
	}
	if err := r.e.Upgrade(context.Background(), UpgradeOptions{Version: "bad version", NoBackup: true}, r.pr); err == nil {
		t.Error("invalid version accepted")
	}
}

func TestUpgradeBuildMode(t *testing.T) {
	r := newRig(t)
	r.e.P.Inst.Env.Set("COMPOSE_FILE", "docker-compose.yml:docker-compose.build.yml")
	r.f.On("ps", compose.Result{Stdout: allHealthy}, nil)
	if err := r.e.Upgrade(context.Background(), UpgradeOptions{NoBackup: true}, r.pr); err != nil {
		t.Fatal(err)
	}
	if !r.f.Called("compose build --pull") || r.f.Called("compose pull") {
		t.Errorf("%v", r.f.Joined())
	}
}

func TestUpgradeFailureHint(t *testing.T) {
	r := newRig(t)
	r.f.On("ps", compose.Result{Stdout: `{"Service":"api","State":"running","Health":"unhealthy"}`}, nil)
	err := r.e.Upgrade(context.Background(), UpgradeOptions{NoBackup: true, Version: "2.0"}, r.pr)
	if err == nil || !strings.Contains(err.Error(), "--tag 1.0") {
		t.Errorf("rollback hint should name the old version: %v", err)
	}
}

func TestConfigSetAndDomain(t *testing.T) {
	r := newRig(t)
	if err := r.e.ConfigSet("OA_PORT", "9000"); err != nil {
		t.Fatal(err)
	}
	if err := r.e.ConfigSet("OA_SECRET_KEY", "x"); err == nil {
		t.Error("secret key must not be settable")
	}
	if r.e.P.Inst.Env.Value("OA_PORT", "") != "9000" {
		t.Error("not applied")
	}
	if _, err := r.e.PlanDomain("not a domain"); err == nil {
		t.Error("invalid domain")
	}
	os.WriteFile(filepath.Join(r.dir, "Caddyfile.template"), []byte("{\n__EMAIL_LINE__\n}\n__DOMAIN__ {\n}\n"), 0o644)
	r.e.P.Inst.Env.Set("OA_DOMAIN", "old.example.com")
	plan, _ := r.e.PlanDomain("oa.example.com")
	if !plan.InvalidatesKeys {
		t.Error("domain change must warn about passkeys")
	}
	r.f.On("ps", compose.Result{Stdout: allHealthy + "\n{\"Service\":\"caddy\",\"State\":\"running\"}"}, nil)
	if err := r.e.ApplyDomain(context.Background(), "oa.example.com", "10.0.0.1", false, r.pr); err != nil {
		t.Fatal(err)
	}
	cf, _ := os.ReadFile(filepath.Join(r.dir, "Caddyfile"))
	if !strings.Contains(string(cf), "oa.example.com {") {
		t.Errorf("caddyfile: %s", cf)
	}
	env, _ := os.ReadFile(filepath.Join(r.dir, ".env"))
	for _, w := range []string{"OA_DOMAIN=oa.example.com", "OA_BIND_ADDR=127.0.0.1", "OA_TRUSTED_PROXY=1", "OA_PUBLIC_URL=https://oa.example.com", "docker-compose.https.yml"} {
		if !strings.Contains(string(env), w) {
			t.Errorf("env missing %s:\n%s", w, env)
		}
	}
	if !r.f.Called("up -d --force-recreate --remove-orphans") {
		t.Error("stack not recreated")
	}
}

func TestRestartKeepsApiWorkerTogether(t *testing.T) {
	r := newRig(t)
	r.f.On("ps", compose.Result{Stdout: allHealthy}, nil)
	if err := r.e.Restart(context.Background(), "worker", r.pr); err != nil {
		t.Fatal(err)
	}
	if !r.f.Called("up -d --force-recreate api worker") {
		t.Errorf("%v", r.f.Joined())
	}
	if err := r.e.Restart(context.Background(), "bogus; rm -rf", r.pr); err == nil {
		t.Error("unknown service accepted")
	}
}

func TestSetupTokenFromLogs(t *testing.T) {
	r := newRig(t)
	r.f.On("logs", compose.Result{Stdout: "api | SETUP TOKEN: OLDTOKEN\napi | SETUP TOKEN: NEWTOKEN123\n"}, nil)
	tok, err := r.e.SetupToken(context.Background())
	if err != nil || tok != "NEWTOKEN123" {
		t.Fatalf("%q %v", tok, err)
	}
}

func tarNames(t *testing.T, file string) []string {
	f, _ := os.Open(file)
	defer f.Close()
	gz, _ := gzip.NewReader(f)
	tr := tar.NewReader(gz)
	var out []string
	for {
		h, err := tr.Next()
		if err != nil {
			return out
		}
		out = append(out, h.Name)
	}
}

// ------------------------------------------------------------------ registry upgrades

func sha(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// stubBundle returns a Fetch hook that writes a deploy-image bundle (with a correct or wrong checksum) into dest.
func stubBundle(t *testing.T, goodSum bool, images *[]string) func(context.Context, string, string) error {
	return func(_ context.Context, image, dest string) error {
		*images = append(*images, image)
		os.MkdirAll(filepath.Join(dest, "bin"), 0o755)
		bin := []byte("GO-BINARY-" + image)
		files := map[string]string{
			"docker-compose.yml": "NEW-COMPOSE", "docker-compose.https.yml": "NEW-HTTPS", "docker-compose.build.yml": "NEW-BUILD",
			"Caddyfile.template": "NEW-CADDY", ".env.example": "NEW-EXAMPLE",
			"oa-agentctl.sh": "#!/bin/bash\nOA_DEFAULT_DIR='/opt/oa-agent'\necho hi\n",
			"bin/oa-agentctl-linux-" + runtime.GOARCH: string(bin),
		}
		for n, c := range files {
			if err := os.WriteFile(filepath.Join(dest, n), []byte(c), 0o644); err != nil {
				return err
			}
		}
		sum := sha(bin)
		if !goodSum {
			sum = strings.Repeat("0", 64)
		}
		return os.WriteFile(filepath.Join(dest, "bin", "SHA256SUMS"), []byte(sum+"  oa-agentctl-linux-"+runtime.GOARCH+"\n"), 0o644)
	}
}

func registryRig(t *testing.T) (*rig, *[]string, string) {
	r := newRig(t)
	r.e.P.Inst.Env.Set("OA_IMAGE_REGISTRY", "reg.example.com/ns")
	r.e.P.Inst.Env.Set("COMPOSE_FILE", "docker-compose.yml:docker-compose.mounts.yml")
	r.e.P.Inst.Env.Save(r.e.P.Inst.EnvPath())
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "oa-agentctl"), []byte("OLD-GO"), 0o755)
	os.WriteFile(filepath.Join(bin, "oa-agentctl.sh"), []byte("OLD-SH"), 0o755)
	r.e.BinDir = bin
	var images []string
	r.e.Fetch = stubBundle(t, true, &images)
	r.f.On("ps", compose.Result{Stdout: allHealthy}, nil)
	return r, &images, bin
}

func TestUpgradeRegistryNewTag(t *testing.T) {
	r, images, bin := registryRig(t)
	os.WriteFile(filepath.Join(r.dir, "mounts.json.keep"), []byte("m"), 0o600)
	os.WriteFile(filepath.Join(r.dir, "docker-compose.mounts.yml"), []byte("MOUNTS-OVERRIDE"), 0o644)
	envBefore, _ := os.ReadFile(filepath.Join(r.dir, ".env"))
	if err := r.e.Upgrade(context.Background(), UpgradeOptions{Version: "sha-abc1234", NoBackup: true}, r.pr); err != nil {
		t.Fatal(err)
	}
	calls := strings.Join(r.f.Joined(), "\n")
	if r.f.Called("compose build") || !r.f.Called("compose pull") || !r.f.Called("up -d --force-recreate api worker") {
		t.Errorf("registry mode must pull and recreate api+worker, never build:\n%s", calls)
	}
	if got, want := *images, []string{"reg.example.com/ns/oa-agent-deploy:sha-abc1234"}; len(got) != 1 || got[0] != want[0] {
		t.Errorf("deploy image: %v", got)
	}
	read := func(p string) string { b, _ := os.ReadFile(p); return string(b) }
	if read(filepath.Join(r.dir, "docker-compose.yml")) != "NEW-COMPOSE" || read(filepath.Join(r.dir, "docker-compose.https.yml")) != "NEW-HTTPS" {
		t.Error("compose files not refreshed")
	}
	if read(filepath.Join(r.dir, "docker-compose.mounts.yml")) != "MOUNTS-OVERRIDE" {
		t.Error("generated mounts override was clobbered")
	}
	env := read(filepath.Join(r.dir, ".env"))
	if !strings.Contains(env, "OA_VERSION=sha-abc1234") || !strings.Contains(env, "OA_SECRET_KEY=aaaaaaaa") {
		t.Errorf(".env: %s", env)
	}
	if !strings.Contains(string(envBefore), "OA_SECRET_KEY=aaaa") {
		t.Fatal("rig")
	}
	if got := read(filepath.Join(bin, "oa-agentctl")); got != "GO-BINARY-reg.example.com/ns/oa-agent-deploy:sha-abc1234" {
		t.Errorf("binary not replaced: %q", got)
	}
	if sh := read(filepath.Join(bin, "oa-agentctl.sh")); !strings.Contains(sh, "OA_DEFAULT_DIR='"+r.dir+"'") {
		t.Errorf("script default dir not rewritten: %s", sh)
	}
}

func TestUpgradeRegistrySameTagSkipsRefresh(t *testing.T) {
	r, images, _ := registryRig(t) // OA_VERSION=1.0, pinned tag, unchanged
	if err := r.e.Upgrade(context.Background(), UpgradeOptions{NoBackup: true}, r.pr); err != nil {
		t.Fatal(err)
	}
	if len(*images) != 0 {
		t.Errorf("same pinned tag must not refresh management files: %v", *images)
	}
	r.e.P.Inst.Env.Set("OA_VERSION", "latest")
	if err := r.e.Upgrade(context.Background(), UpgradeOptions{NoBackup: true}, r.pr); err != nil || len(*images) != 1 {
		t.Errorf("moving tag latest must refresh: %v %v", err, *images)
	}
}

func TestUpgradeRegistryChecksumMismatchAborts(t *testing.T) {
	r, _, bin := registryRig(t)
	var images []string
	r.e.Fetch = stubBundle(t, false, &images)
	err := r.e.Upgrade(context.Background(), UpgradeOptions{Version: "2.0", NoBackup: true}, r.pr)
	if err == nil || !errors.Is(err, ErrChecksum) {
		t.Fatalf("want checksum error, got %v", err)
	}
	if r.f.Called("up -d") {
		t.Error("services must not be recreated after a checksum failure")
	}
	if b, _ := os.ReadFile(filepath.Join(bin, "oa-agentctl")); string(b) != "OLD-GO" {
		t.Error("binary replaced despite bad checksum")
	}
	if b, _ := os.ReadFile(filepath.Join(r.dir, "docker-compose.yml")); string(b) != "x" {
		t.Error("compose file replaced despite bad checksum")
	}
}

func TestUpgradeRegistryFetchFailureIsWarning(t *testing.T) {
	r, _, _ := registryRig(t)
	r.e.Fetch = func(context.Context, string, string) error { return errors.New("manifest unknown") }
	if err := r.e.Upgrade(context.Background(), UpgradeOptions{Version: "2.0", NoBackup: true}, r.pr); err != nil {
		t.Fatalf("a missing deploy image must not block the upgrade: %v", err)
	}
	if !strings.Contains(strings.Join(r.lines, "\n"), "警告") || !r.f.Called("up -d --force-recreate api worker") {
		t.Errorf("lines=%v calls=%v", r.lines, r.f.Joined())
	}
}

func TestUpgradePullFailureNamesTag(t *testing.T) {
	r, _, _ := registryRig(t)
	r.f.On("compose pull", compose.Result{ExitCode: 1, Stderr: "manifest unknown"}, errors.New("exit 1"))
	err := r.e.Upgrade(context.Background(), UpgradeOptions{Version: "nope", NoBackup: true}, r.pr)
	if err == nil || !strings.Contains(err.Error(), `"nope"`) || r.f.Called("up -d") {
		t.Errorf("err=%v", err)
	}
}

func TestFetchBundleDockerCalls(t *testing.T) {
	r, _, _ := registryRig(t)
	r.e.Fetch = nil
	r.f.On("create", compose.Result{Stdout: "abc123\n"}, nil)
	dest := t.TempDir()
	if err := r.e.fetchBundle(context.Background(), r.pr, "reg.example.com/ns/oa-agent-deploy:t", dest); err != nil {
		t.Fatal(err)
	}
	want := []string{"pull reg.example.com/ns/oa-agent-deploy:t", "create reg.example.com/ns/oa-agent-deploy:t", "cp abc123:/deploy/. " + dest, "rm -f abc123"}
	got := r.f.Joined()
	if len(got) != len(want) {
		t.Fatalf("%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("call %d: %q want %q", i, got[i], want[i])
		}
	}
}

func TestUpgradeBuildModeKeepsOldBehaviour(t *testing.T) {
	r := newRig(t)
	r.e.P.Inst.Env.Set("COMPOSE_FILE", "docker-compose.yml:docker-compose.build.yml")
	r.e.P.Inst.Env.Set("OA_IMAGE_REGISTRY", "reg.example.com/ns")
	r.e.Fetch = func(context.Context, string, string) error {
		t.Error("build mode must not fetch the deploy image")
		return nil
	}
	r.f.On("ps", compose.Result{Stdout: allHealthy}, nil)
	if err := r.e.Upgrade(context.Background(), UpgradeOptions{Version: "9.9", NoBackup: true}, r.pr); err != nil {
		t.Fatal(err)
	}
	if !r.f.Called("up -d --remove-orphans") || r.f.Called("force-recreate") {
		t.Errorf("%v", r.f.Joined())
	}
}
