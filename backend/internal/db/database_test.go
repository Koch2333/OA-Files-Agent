package db

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestUnconfigured(t *testing.T) {
	if got := Check(context.Background(), ""); got != "not_configured" {
		t.Fatal(got)
	}
}

func TestInvalidConnectionDoesNotLeakSecret(t *testing.T) {
	_, err := Connect(context.Background(), "invalid-secret-url")
	if err == nil || err.Error() != "database connection failed" {
		t.Fatalf("unexpected error: %v", err)
	}
}

// Set OA_TEST_DATABASE_URL to a disposable PostgreSQL database. Each test uses an isolated schema.
func TestPostgresMigration(t *testing.T) {
	url := os.Getenv("OA_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("OA_TEST_DATABASE_URL not set; real PostgreSQL integration not run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	schema := "oa_test_" + time.Now().Format("20060102150405_000000000")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = conn.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	defer conn.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE")
	if _, err = conn.Exec(ctx, "SET search_path TO "+quoted); err != nil {
		t.Fatal(err)
	}
	path := "../../../migrations/001_initial.up.sql"
	for i := 0; i < 2; i++ {
		if err = Migrate(ctx, conn, path); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err = conn.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&count); err != nil || count != 1 {
		t.Fatalf("migration count %d, err %v", count, err)
	}
	if _, err = conn.Exec(ctx, "INSERT INTO import_jobs(id, total_files, processed_files) VALUES ('00000000-0000-0000-0000-000000000001', 0, 1)"); err == nil {
		t.Fatal("invalid progress was accepted")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	changed := filepath.Join(t.TempDir(), "001_changed.up.sql")
	if err = os.WriteFile(changed, append([]byte("BEGIN;\n-- changed\n"), raw[len("BEGIN;"):]...), 0600); err != nil {
		t.Fatal(err)
	}
	if err = Migrate(ctx, conn, changed); err == nil {
		t.Fatal("changed migration accepted")
	}
}

func TestMigrateDirAppliesAllVersionsAndIsIdempotent(t *testing.T) {
	url := os.Getenv("OA_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("OA_TEST_DATABASE_URL not set; real PostgreSQL integration not run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	schema := "oa_test_dir_" + time.Now().Format("20060102150405_000000000")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = conn.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	defer conn.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE")
	if _, err = conn.Exec(ctx, "SET search_path TO "+quoted); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = MigrateDir(ctx, conn, "../../../migrations"); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err = conn.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&n); err != nil || n != SchemaVersion {
		t.Fatalf("applied %d migrations, err %v", n, err)
	}
	if err = conn.QueryRow(ctx, "SELECT count(*) FROM archive_rules").Scan(&n); err != nil || n != 1 {
		t.Fatalf("default rule count %d, err %v", n, err)
	}
}
