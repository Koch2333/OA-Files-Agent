package db

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// SchemaVersion is the newest migration this build expects.
const SchemaVersion = 2

// Connect never includes the connection string in errors returned to callers.
func Connect(ctx context.Context, url string) (*pgx.Conn, error) {
	c, err := pgx.Connect(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("database connection failed")
	}
	return c, nil
}

// Migrate applies one migration file atomically. The version is the numeric filename prefix.
// A transaction lock serializes concurrent startup.
func Migrate(ctx context.Context, conn *pgx.Conn, path string) error {
	version, err := versionOf(path)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read migration: %w", err)
	}
	sql := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(sql, "BEGIN;") || !strings.HasSuffix(sql, "COMMIT;") {
		return fmt.Errorf("migration must have BEGIN/COMMIT wrapper")
	}
	sql = strings.TrimSuffix(strings.TrimPrefix(sql, "BEGIN;"), "COMMIT;")
	checksum := fmt.Sprintf("%x", sha256.Sum256(raw))
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(728194601)"); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, checksum TEXT NOT NULL, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	var stored string
	err = tx.QueryRow(ctx, "SELECT checksum FROM schema_migrations WHERE version = $1", version).Scan(&stored)
	if err == nil {
		if stored != checksum {
			return fmt.Errorf("migration %d checksum mismatch; restore the applied migration", version)
		}
		return tx.Commit(ctx)
	}
	if err != pgx.ErrNoRows {
		return err
	}
	if _, err = tx.Exec(ctx, sql); err != nil {
		return fmt.Errorf("apply migration %d: %w", version, err)
	}
	if _, err = tx.Exec(ctx, "INSERT INTO schema_migrations(version, checksum) VALUES ($1, $2)", version, checksum); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// MigrateDir applies every NNN_*.up.sql in version order; already-applied files are verified by checksum.
func MigrateDir(ctx context.Context, conn *pgx.Conn, dir string) error {
	files, err := filepath.Glob(filepath.Join(dir, "*.up.sql"))
	if err != nil {
		return err
	}
	sort.Strings(files)
	if len(files) == 0 {
		return fmt.Errorf("no migrations found in %s", dir)
	}
	for _, f := range files {
		if err := Migrate(ctx, conn, f); err != nil {
			return err
		}
	}
	return nil
}

func versionOf(path string) (int, error) {
	base := filepath.Base(path)
	i := strings.IndexByte(base, '_')
	if i < 1 {
		return 0, fmt.Errorf("migration file %q needs a NNN_ prefix", base)
	}
	v, err := strconv.Atoi(base[:i])
	if err != nil || v < 1 {
		return 0, fmt.Errorf("migration file %q needs a numeric NNN_ prefix", base)
	}
	return v, nil
}

func Check(ctx context.Context, url string) string {
	if url == "" {
		return "not_configured"
	}
	conn, err := Connect(ctx, url)
	if err != nil {
		return "unavailable"
	}
	defer conn.Close(context.Background())
	var ready bool
	err = conn.QueryRow(ctx, `SELECT COALESCE((SELECT max(version) FROM schema_migrations), 0) >= $1 AND to_regclass('import_jobs') IS NOT NULL AND to_regclass('file_records') IS NOT NULL AND to_regclass('ledger_entries') IS NOT NULL`, SchemaVersion).Scan(&ready)
	if err != nil || !ready {
		return "migration_required"
	}
	return "ok"
}
