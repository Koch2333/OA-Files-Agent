package db_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"oa-agent/backend/internal/db"
	"oa-agent/backend/internal/dbtest/dbiso"
	"oa-agent/backend/internal/sqldb"
)

const root = "../../../migrations"

func TestUnconfigured(t *testing.T) {
	if got := db.Check(context.Background(), nil); got != "not_configured" {
		t.Fatal(got)
	}
}

func TestInvalidConnectionDoesNotLeakSecret(t *testing.T) {
	_, err := sqldb.OpenURL(context.Background(), "postgres://oa:TopSecretPw@127.0.0.1:1/x?sslmode=disable&connect_timeout=1")
	if err == nil || strings.Contains(err.Error(), "TopSecretPw") {
		t.Fatalf("unexpected error: %v", err)
	}
	_, err = sqldb.OpenURL(context.Background(), "mysql://oa:TopSecretPw@127.0.0.1:1/x")
	if err == nil || strings.Contains(err.Error(), "TopSecretPw") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadAllDialects(t *testing.T) {
	for _, p := range []sqldb.Provider{sqldb.Postgres, sqldb.MySQL, sqldb.MSSQL} {
		ms, err := db.Load(root, p)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if len(ms) != db.SchemaVersion || ms[len(ms)-1].Version != db.SchemaVersion {
			t.Fatalf("%s: %d migrations, want %d", p, len(ms), db.SchemaVersion)
		}
		for _, m := range ms {
			if m.Destructive {
				t.Errorf("%s %s is flagged destructive", p, m.Name)
			}
		}
	}
}

func TestMigrateAppliesAllVersionsAndIsIdempotent(t *testing.T) {
	pool, _ := dbiso.Empty(t)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := db.Apply(ctx, pool, db.Options{Dir: root}); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&n); err != nil || n != db.SchemaVersion {
		t.Fatalf("applied %d migrations, err %v", n, err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM archive_rules").Scan(&n); err != nil || n != 1 {
		t.Fatalf("default rule count %d, err %v", n, err)
	}
	st, err := db.SchemaStatus(ctx, pool)
	if err != nil || st.State != "ok" || st.Version != db.SchemaVersion || len(st.ForeignTables) != 0 {
		t.Fatalf("status %+v err %v", st, err)
	}
	if db.Check(ctx, pool) != "ok" {
		t.Fatal("check")
	}
	// constraints are enforced on every engine
	if _, err = pool.Exec(ctx, "INSERT INTO import_jobs(id, total_files, processed_files) VALUES ('00000000-0000-0000-0000-000000000001', 0, 1)"); err == nil {
		t.Fatal("invalid progress was accepted")
	}
}

func TestEmptyStatusAndPlan(t *testing.T) {
	pool, _ := dbiso.Empty(t)
	ctx := context.Background()
	st, err := db.SchemaStatus(ctx, pool)
	if err != nil || st.State != "empty" {
		t.Fatalf("status %+v err %v", st, err)
	}
	pl, err := db.MakePlan(ctx, pool, root)
	if err != nil || len(pl.Pending) != db.SchemaVersion || pl.Hash == "" || pl.Destructive {
		t.Fatalf("plan %+v err %v", pl, err)
	}
	if db.Check(ctx, pool) != "migration_required" {
		t.Fatal("check")
	}
}

func copyDir(t *testing.T, p sqldb.Provider) string {
	t.Helper()
	dst := t.TempDir()
	src := db.Dir(root, p)
	files, _ := filepath.Glob(filepath.Join(src, "*.up.sql"))
	out := db.Dir(dst, p)
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		b, _ := os.ReadFile(f)
		if err := os.WriteFile(filepath.Join(out, filepath.Base(f)), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dst
}

func TestChangedMigrationIsRejected(t *testing.T) {
	pool, _ := dbiso.Empty(t)
	ctx := context.Background()
	if err := db.Apply(ctx, pool, db.Options{Dir: root}); err != nil {
		t.Fatal(err)
	}
	dir := copyDir(t, pool.Provider())
	f := filepath.Join(db.Dir(dir, pool.Provider()), "001_initial.up.sql")
	b, _ := os.ReadFile(f)
	os.WriteFile(f, []byte(strings.Replace(string(b), "\n", "\n-- tampered\n", 1)), 0o600)
	if err := db.Apply(ctx, pool, db.Options{Dir: dir}); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("changed migration accepted: %v", err)
	}
}

func TestDestructiveNeedsApproval(t *testing.T) {
	pool, _ := dbiso.Empty(t)
	ctx := context.Background()
	if err := db.Apply(ctx, pool, db.Options{Dir: root}); err != nil {
		t.Fatal(err)
	}
	dir := copyDir(t, pool.Provider())
	body := "DROP TABLE ai_policy"
	if pool.Provider() == sqldb.Postgres {
		body = "BEGIN;\nDROP TABLE ai_policy;\nCOMMIT;"
	}
	os.WriteFile(filepath.Join(db.Dir(dir, pool.Provider()), "011_drop.up.sql"), []byte(body), 0o600)
	err := db.Apply(ctx, pool, db.Options{Dir: dir})
	if err == nil || !strings.Contains(err.Error(), "destructive") {
		t.Fatalf("destructive migration ran without approval: %v", err)
	}
	var n int
	if e := pool.QueryRow(ctx, "SELECT count(*) FROM ai_policy").Scan(&n); e != nil {
		t.Fatalf("table must still exist: %v", e)
	}
	if err = db.Apply(ctx, pool, db.Options{Dir: dir, AllowDestructive: true}); err != nil {
		t.Fatal(err)
	}
	if e := pool.QueryRow(ctx, "SELECT count(*) FROM ai_policy").Scan(&n); e == nil {
		t.Fatal("approved destructive migration did not run")
	}
}

// A failing step leaves nothing behind on PostgreSQL and SQL Server (transactional DDL); on MySQL the migration is
// marked dirty and the next run resumes at the failed step.
func TestFailedStepRecovery(t *testing.T) {
	pool, _ := dbiso.Empty(t)
	ctx := context.Background()
	if err := db.Apply(ctx, pool, db.Options{Dir: root}); err != nil {
		t.Fatal(err)
	}
	p := pool.Provider()
	dir := copyDir(t, p)
	good := "CREATE TABLE zz_one (id INT NOT NULL PRIMARY KEY)"
	bad := "CREATE TABLE zz_bad (id INT NOT NULL PRIMARY KEY, x INT, FOREIGN KEY (x) REFERENCES no_such_table(id))"
	fixed := "CREATE TABLE zz_two (id INT NOT NULL PRIMARY KEY)"
	var broken, repaired string
	switch p {
	case sqldb.Postgres:
		broken = "BEGIN;\n" + good + ";\n" + bad + ";\nCOMMIT;"
	case sqldb.MySQL:
		broken = good + " ENGINE=InnoDB\n--;;\n" + bad
	default:
		broken = good + "\n--;;\n" + bad
	}
	_ = repaired
	name := "011_fail.up.sql"
	path := filepath.Join(db.Dir(dir, p), name)
	os.WriteFile(path, []byte(broken), 0o600)
	err := db.Apply(ctx, pool, db.Options{Dir: dir})
	if err == nil {
		t.Fatal("failing migration succeeded")
	}
	tables, _ := db.ListTables(ctx, pool)
	has := func(n string) bool {
		for _, x := range tables {
			if x == n {
				return true
			}
		}
		return false
	}
	st, _ := db.SchemaStatus(ctx, pool)
	switch p {
	case sqldb.MySQL:
		if !has("zz_one") || has("zz_bad") {
			t.Fatalf("mysql partial state wrong: %v", tables)
		}
		if st.State != "dirty" {
			t.Fatalf("mysql must be dirty, got %s", st.State)
		}
		// editing the migration now is refused (checksum differs) ...
		os.WriteFile(path, []byte(broken+"\n"), 0o600)
		if err = db.Apply(ctx, pool, db.Options{Dir: dir}); err == nil {
			t.Fatal("dirty migration with a different checksum must be refused")
		}
		// ... and the identical file resumes: step 1 is already recorded, the failing step fails again.
		os.WriteFile(path, []byte(broken), 0o600)
		if err = db.Apply(ctx, pool, db.Options{Dir: dir}); err == nil {
			t.Fatal("still-broken migration succeeded")
		}
	default:
		if has("zz_one") || has("zz_bad") {
			t.Fatalf("%s left partial DDL behind: %v", p, tables)
		}
		if st.Version != db.SchemaVersion {
			t.Fatalf("version moved to %d", st.Version)
		}
	}
	_ = fixed
}

func TestMySQLResumeAfterCrashBetweenStepAndMarker(t *testing.T) {
	pool, _ := dbiso.Empty(t)
	if pool.Provider() != sqldb.MySQL {
		t.Skip("MySQL only: step markers")
	}
	ctx := context.Background()
	if err := db.Apply(ctx, pool, db.Options{Dir: root}); err != nil {
		t.Fatal(err)
	}
	dir := copyDir(t, sqldb.MySQL)
	path := filepath.Join(db.Dir(dir, sqldb.MySQL), "011_two.up.sql")
	body := "CREATE TABLE zz_a (id INT NOT NULL PRIMARY KEY) ENGINE=InnoDB\n--;;\nALTER TABLE zz_a ADD COLUMN b INT NULL\n--;;\nCREATE INDEX zz_a_b ON zz_a(b)"
	os.WriteFile(path, []byte(body), 0o600)
	ms, _ := db.Load(dir, sqldb.MySQL)
	m := ms[len(ms)-1]
	// simulate a crash after step 2 ran but before its marker: dirty row, marker only for step 0, steps 0 and 1 applied
	pool.Exec(ctx, `INSERT INTO schema_migrations(version, checksum, state) VALUES ($1,$2,'dirty')`, m.Version, m.Checksum)
	pool.Exec(ctx, "CREATE TABLE zz_a (id INT NOT NULL PRIMARY KEY) ENGINE=InnoDB")
	pool.Exec(ctx, `INSERT INTO schema_migration_steps(version, step) VALUES ($1, 0)`, m.Version)
	pool.Exec(ctx, "ALTER TABLE zz_a ADD COLUMN b INT NULL")
	if err := db.Apply(ctx, pool, db.Options{Dir: dir}); err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	st, _ := db.SchemaStatus(ctx, pool)
	if st.State != "ok" && st.Version != 11 {
		t.Fatalf("status %+v", st)
	}
	if st.Dirty {
		t.Fatal("still dirty")
	}
}

func TestConcurrentMigratorsApplyOnce(t *testing.T) {
	pool, _ := dbiso.Empty(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make([]error, 6)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = db.Apply(ctx, pool, db.Options{Dir: root})
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("migrator %d: %v", i, err)
		}
	}
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&n); err != nil || n != db.SchemaVersion {
		t.Fatalf("applied %d migrations, err %v", n, err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM archive_rules").Scan(&n); err != nil || n != 1 {
		t.Fatalf("default rule inserted %d times, err %v", n, err)
	}
}
