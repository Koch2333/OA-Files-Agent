package main

import (
	"bytes"
	"strings"
	"testing"

	"oa-agent/backend/internal/dbadmin"
	"oa-agent/backend/internal/dbtest/dbiso"
	"oa-agent/backend/internal/sqldb"
)

func envMap(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func runDBT(t *testing.T, cmd string, args []string, stdin string, env map[string]string) (int, string, string) {
	t.Helper()
	var out, errw bytes.Buffer
	rc := runDB(cmd, args, strings.NewReader(stdin), &out, &errw, envMap(env))
	return rc, out.String(), errw.String()
}

func TestDBCommandsNeedSettings(t *testing.T) {
	rc, _, e := runDBT(t, "db-check", nil, "", map[string]string{})
	if rc != 2 || !strings.Contains(e, "no database settings") {
		t.Fatalf("rc=%d stderr=%q", rc, e)
	}
	env := map[string]string{"OA_DB_PROVIDER": "postgres", "OA_DB_HOST": "db.example.com", "OA_DB_NAME": "oa", "OA_DB_USER": "oa"}
	if rc, _, e = runDBT(t, "db-check", nil, "", env); rc != 2 || !strings.Contains(e, "no database password") {
		t.Fatalf("missing password: rc=%d stderr=%q", rc, e)
	}
	if rc, _, e = runDBT(t, "db-check", []string{"--password-stdin"}, "\n", env); rc != 2 || !strings.Contains(e, "empty") {
		t.Fatalf("empty stdin password: rc=%d stderr=%q", rc, e)
	}
	if rc, _, e = runDBT(t, "db-check", []string{"bogus"}, "", env); rc != 2 {
		t.Fatalf("stray argument: rc=%d stderr=%q", rc, e)
	}
	env["OA_DB_PROVIDER"] = "oracle"
	if rc, o, _ := runDBT(t, "db-check", []string{"--password-stdin"}, "pw\n", env); rc != 2 || !strings.Contains(o, "RESULT\tfail") {
		t.Fatalf("unknown provider: rc=%d out=%q", rc, o)
	}
}

func TestDBStdinPasswordOverridesFileAndDSN(t *testing.T) {
	env := map[string]string{"OA_DB_PROVIDER": "postgres", "OA_DB_HOST": "h", "OA_DB_NAME": "oa", "OA_DB_USER": "u",
		"OA_DB_PASSWORD": "from-env", "OA_DB_PASSWORD_FILE": "/nonexistent", "OA_DB_DSN": "postgres://x:y@z/db"}
	g := dbEnv(envMap(env), "from-stdin", true)
	if g("OA_DB_PASSWORD") != "from-stdin" || g("OA_DB_PASSWORD_FILE") != "" || g("OA_DB_DSN") != "" || g("OA_DB_HOST") != "h" {
		t.Fatal("stdin password must replace the file/DSN settings")
	}
	if dbEnv(envMap(env), "", false)("OA_DB_PASSWORD") != "from-env" {
		t.Fatal("without --password-stdin the environment is used unchanged")
	}
}

func TestDBUnreachableServerFailsWithoutLeakingThePassword(t *testing.T) {
	env := map[string]string{"OA_DB_PROVIDER": "postgres", "OA_DB_HOST": "127.0.0.1", "OA_DB_PORT": "1", "OA_DB_NAME": "oa", "OA_DB_USER": "oa", "OA_DB_TLS_MODE": "disable"}
	rc, out, _ := runDBT(t, "db-check", []string{"--password-stdin"}, "S3cretPw!\n", env)
	if rc != 1 || !strings.Contains(out, "CHECK\tfail\tconnect") || !strings.Contains(out, "RESULT\tfail") {
		t.Fatalf("rc=%d out=%q", rc, out)
	}
	if strings.Contains(out, "S3cretPw!") {
		t.Fatal("the password leaked into the output")
	}
}

func TestFingerprintBindsTheExactDDL(t *testing.T) {
	// db-init cannot apply a plan the administrator did not see: any change in the statements changes the fingerprint
	mk := func(st ...string) dbadmin.InitPreview {
		return dbadmin.InitPreview{Steps: []dbadmin.StepPreview{{Version: 1, Name: "x", Statements: st}}}
	}
	a, b := fingerprint(mk("CREATE TABLE a (x int)")), fingerprint(mk("CREATE TABLE a (x int)", "DROP TABLE a"))
	if a == b || len(a) != 24 || a != fingerprint(mk("CREATE TABLE a (x int)")) {
		t.Fatalf("fingerprints %q %q", a, b)
	}
}

// ---- against a real server (OA_TEST_DATABASE_URL etc.; skipped otherwise) ----

func dbEnvFor(cfg sqldb.Config) map[string]string {
	m := map[string]string{"OA_DB_PROVIDER": string(cfg.Provider), "OA_DB_HOST": cfg.Host, "OA_DB_NAME": cfg.Name, "OA_DB_USER": cfg.User,
		"OA_DB_SCHEMA": cfg.Schema, "OA_DB_TLS_MODE": "disable", "OA_MIGRATIONS_DIR": dbiso.MigrationsDir()}
	if cfg.Port != 0 {
		m["OA_DB_PORT"] = itoa(cfg.Port)
	}
	return m
}

func itoa(n int) string {
	var b [12]byte
	i := len(b)
	for n > 0 || i == len(b) {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func TestDBCheckAndInitAgainstARealServer(t *testing.T) {
	d, cfg := dbiso.Empty(t)
	_ = d
	env := dbEnvFor(cfg)
	pw := cfg.Password + "\n"

	rc, out, _ := runDBT(t, "db-check", []string{"--password-stdin"}, pw, env)
	if rc != 0 || !strings.Contains(out, "CHECK\tpass\tconnect") || !strings.Contains(out, "INFO\tschema\tempty") || !strings.Contains(out, "RESULT\tok") {
		t.Fatalf("db-check on an empty target: rc=%d\n%s", rc, out)
	}
	// a CI password may equal the user or database name ("postgres"), which the report prints legitimately
	scrubbed := out
	for _, public := range []string{cfg.User, cfg.Name, cfg.Host, cfg.Schema, string(cfg.Provider)} {
		if public != "" {
			scrubbed = strings.ReplaceAll(scrubbed, public, "")
		}
	}
	if cfg.Password != "" && strings.Contains(scrubbed, cfg.Password) {
		t.Fatal("password leaked")
	}
	// preview changes nothing and prints a fingerprint
	rc, out, _ = runDBT(t, "db-init", []string{"--password-stdin", "--show-ddl"}, pw, env)
	if rc != 0 || !strings.Contains(out, "PLAN\t") || !strings.Contains(out, "DDL\t") || !strings.Contains(out, "preview only") {
		t.Fatalf("preview: rc=%d\n%s", rc, out)
	}
	fp := ""
	for _, f := range strings.Split(out, "\t") {
		if strings.HasPrefix(f, "fingerprint=") {
			fp = strings.TrimSpace(strings.SplitN(strings.TrimPrefix(f, "fingerprint="), "\n", 2)[0])
		}
	}
	if len(fp) != 24 {
		t.Fatalf("fingerprint not found in %q", out)
	}
	if rc, out, _ = runDBT(t, "db-check", []string{"--password-stdin"}, pw, env); !strings.Contains(out, "INFO\tschema\tempty") {
		t.Fatalf("preview must not create anything: %s", out)
	}
	// applying needs the matching fingerprint
	if rc, out, _ = runDBT(t, "db-init", []string{"--password-stdin", "--apply", "--plan", "deadbeef"}, pw, env); rc != 1 || !strings.Contains(out, "fingerprint mismatch") {
		t.Fatalf("wrong plan: rc=%d\n%s", rc, out)
	}
	if rc, out, _ = runDBT(t, "db-init", []string{"--password-stdin", "--apply", "--plan", fp}, pw, env); rc != 0 || !strings.Contains(out, "applied") {
		t.Fatalf("apply: rc=%d\n%s", rc, out)
	}
	rc, out, _ = runDBT(t, "db-check", []string{"--password-stdin"}, pw, env)
	if rc != 0 || !strings.Contains(out, "INFO\tschema\tok") {
		t.Fatalf("db-check after init: rc=%d\n%s", rc, out)
	}
	if rc, out, _ = runDBT(t, "db-init", []string{"--password-stdin"}, pw, env); rc != 0 || !strings.Contains(out, "already current") {
		t.Fatalf("second init: rc=%d\n%s", rc, out)
	}
	// a wrong password is a connection failure that does not echo the password
	rc, out, _ = runDBT(t, "db-check", []string{"--password-stdin"}, "definitely-wrong-pw\n", env)
	if rc != 1 || !strings.Contains(out, "RESULT\tfail") || strings.Contains(out, "definitely-wrong-pw") {
		t.Fatalf("wrong password: rc=%d\n%s", rc, out)
	}
}
