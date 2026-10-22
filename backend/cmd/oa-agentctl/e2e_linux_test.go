//go:build linux

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// shim is a fake `docker`: it logs every call and answers with canned output.
const shim = `#!/bin/sh
echo "$*" >> "$SHIM_LOG"
case "$*" in
  "version "*) echo 27.0.0 ;;
  "compose version"*) echo "Docker Compose version v2.29.0" ;;
  *"ps -a --format json"*)
    echo '{"Service":"postgres","State":"running","Health":"healthy","Status":"Up 5 minutes (healthy)"}'
    echo '{"Service":"api","State":"running","Health":"healthy","Status":"Up 5 minutes (healthy)","Publishers":[{"URL":"0.0.0.0","TargetPort":8080,"PublishedPort":8080,"Protocol":"tcp"}]}'
    echo '{"Service":"worker","State":"running","Status":"Up 5 minutes"}' ;;
  *"oa-admin list-users --json"*) echo '[{"username":"alice","role":"admin","status":"active","mfa":true,"locked":false}]' ;;
  *"oa-admin reset-password"*) echo "Temporary password: E2E-Temp-Pw-77" ;;
  *"config -q"*) ;;
  *"psql"*) echo 1 ;;
esac
exit 0
`

type e2eRig struct {
	t    *testing.T
	bin  string
	dir  string
	log  string
	path string
}

func buildBin(t *testing.T) string {
	if b := os.Getenv("OA_CTL_BIN"); b != "" {
		return b
	}
	out := filepath.Join(t.TempDir(), "oa-agentctl")
	cmd := exec.Command("go", "build", "-o", out, ".")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cannot build the binary here (set OA_CTL_BIN): %v\n%s", err, b)
	}
	return out
}

func newE2E(t *testing.T) *e2eRig {
	r := &e2eRig{t: t, bin: buildBin(t)}
	root := t.TempDir()
	r.dir = filepath.Join(root, "install")
	os.MkdirAll(r.dir, 0o755)
	bindir := filepath.Join(root, "bin")
	os.MkdirAll(bindir, 0o755)
	os.WriteFile(filepath.Join(bindir, "docker"), []byte(shim), 0o755)
	r.log = filepath.Join(root, "docker.log")
	r.path = bindir + ":" + os.Getenv("PATH")
	env := "COMPOSE_FILE=docker-compose.yml\nOA_PORT=8080\nOA_PUBLIC_URL=http://srv:8080\nOA_SECRET_KEY=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\nOA_WORKER_TOKEN=tokentokentokentokentokentokentoken12\nOA_ARCHIVE_DIR=" + root + "/archive\nOA_SPOOL_DIR=" + root + "/spool\nOA_APP_DIR=" + root + "/app\nOA_PG_DIR=" + root + "/pg\n"
	os.WriteFile(filepath.Join(r.dir, ".env"), []byte(env), 0o600)
	os.WriteFile(filepath.Join(r.dir, "docker-compose.yml"), []byte("services: {}\n"), 0o644)
	for _, d := range []string{"archive", "spool", "app", "pg", "nas"} {
		os.MkdirAll(filepath.Join(root, d), 0o755)
	}
	return r
}

func (r *e2eRig) run(args ...string) (string, string, int) {
	cmd := exec.Command(r.bin, append([]string{"--dir", r.dir}, args...)...)
	cmd.Env = append(os.Environ(), "PATH="+r.path, "SHIM_LOG="+r.log, "OA_AGENT_DIR=")
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		r.t.Fatal(err)
	}
	return so.String(), se.String(), code
}

func (r *e2eRig) calls() string {
	b, _ := os.ReadFile(r.log)
	return string(b)
}

func TestE2EBinary(t *testing.T) {
	r := newE2E(t)
	root := filepath.Dir(r.dir)

	out, _, code := r.run("status", "--json")
	if code != 0 || !strings.Contains(out, `"ok": true`) || !strings.Contains(out, "0.0.0.0:8080->8080/tcp") {
		t.Fatalf("status: %d %s", code, out)
	}
	if !strings.Contains(r.calls(), "compose ps -a --format json") {
		t.Errorf("docker calls: %s", r.calls())
	}

	// legacy layout: mounts apply creates mounts.json + override and syncs .env, unattended
	_, errOut, code := r.run("mounts", "apply")
	if code != 0 {
		t.Fatalf("mounts apply: %d %s", code, errOut)
	}
	over, _ := os.ReadFile(filepath.Join(r.dir, "docker-compose.mounts.yml"))
	if !strings.Contains(string(over), root+"/archive:/data/archive") || !strings.Contains(string(over), "generated — do not edit") {
		t.Errorf("override:\n%s", over)
	}
	if st, _ := os.Stat(filepath.Join(r.dir, ".env")); st.Mode().Perm() != 0o600 {
		t.Errorf(".env mode %v", st.Mode().Perm())
	}
	if st, _ := os.Stat(filepath.Join(r.dir, "docker-compose.mounts.yml")); st.Mode().Perm() != 0o644 {
		t.Errorf("override mode %v", st.Mode().Perm())
	}
	env, _ := os.ReadFile(filepath.Join(r.dir, ".env"))
	if !strings.Contains(string(env), "COMPOSE_FILE=docker-compose.yml:docker-compose.mounts.yml") {
		t.Errorf(".env:\n%s", env)
	}

	// add an external read-only directory
	_, errOut, code = r.run("mounts", "add", "nas1", root+"/nas", "--yes")
	if code != 0 {
		t.Fatalf("add: %d %s", code, errOut)
	}
	over, _ = os.ReadFile(filepath.Join(r.dir, "docker-compose.mounts.yml"))
	if !strings.Contains(string(over), root+"/nas:/mnt/storage/nas1:ro") || !strings.Contains(string(over), "OA_LOCAL_ROOTS: /mnt/storage") {
		t.Errorf("override:\n%s", over)
	}
	// dangerous path rejected with non-zero exit
	if _, _, code = r.run("mounts", "add", "bad", "/etc", "--yes"); code == 0 {
		t.Error("/etc must be rejected")
	}
	// restart goes through docker with api+worker together
	if _, _, code = r.run("mounts", "apply", "--restart"); code != 0 {
		t.Fatalf("apply --restart: %d", code)
	}
	if !strings.Contains(r.calls(), "compose up -d --force-recreate api worker") {
		t.Errorf("calls:\n%s", r.calls())
	}

	// users
	out, _, code = r.run("users", "reset-password", "alice", "--yes")
	if code != 0 || strings.Count(out, "E2E-Temp-Pw-77") != 1 {
		t.Fatalf("reset: %d %s", code, out)
	}
	if strings.Contains(r.calls(), "E2E-Temp-Pw-77") {
		t.Error("password in docker call log")
	}
	if _, _, code = r.run("users", "reset-password", "alice"); code != 4 {
		t.Errorf("without --yes must exit 4, got %d", code)
	}
	out, _, _ = r.run("users", "list", "--json")
	if !strings.Contains(out, "alice") {
		t.Errorf("%s", out)
	}

	// config masking
	out, _, _ = r.run("config", "show")
	if strings.Contains(out, "0123456789abcdef") {
		t.Errorf("secret leaked:\n%s", out)
	}
	if out, _, code = r.run("doctor"); code != 0 && !strings.Contains(out, "[FAIL]") {
		t.Errorf("doctor: %d\n%s", code, out)
	}
	if _, _, code = r.run("nonsense"); code != 2 {
		t.Errorf("unknown command exit %d", code)
	}
	// no TTY: the bare command refuses with a message
	if _, errOut, code = r.run(); code != 2 || !strings.Contains(errOut, "不是终端") {
		t.Errorf("no-tty: %d %s", code, errOut)
	}
	out, _, code = r.run("version")
	if code != 0 || !strings.Contains(out, "oa-agentctl") {
		t.Errorf("version: %d %s", code, out)
	}
}
