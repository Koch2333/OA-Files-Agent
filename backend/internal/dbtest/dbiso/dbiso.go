// Package dbiso gives integration tests an isolated, fully migrated database on a real server. The engine is
// chosen by OA_TEST_PROVIDER (postgres | mysql | mssql); when unset, the first of OA_TEST_DATABASE_URL (PostgreSQL),
// OA_TEST_MYSQL_URL, OA_TEST_MSSQL_URL that is set is used. Every test gets its own PostgreSQL schema or its own
// MySQL / SQL Server database, dropped at cleanup.
package dbiso

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"oa-agent/backend/internal/db"
	"oa-agent/backend/internal/sqldb"
)

// Provider reports which engine the tests run against ("" when none is configured).
func Provider() sqldb.Provider {
	if p := os.Getenv("OA_TEST_PROVIDER"); p != "" {
		if pr, err := sqldb.ParseProvider(p); err == nil {
			return pr
		}
	}
	switch {
	case os.Getenv("OA_TEST_DATABASE_URL") != "":
		return sqldb.Postgres
	case os.Getenv("OA_TEST_MYSQL_URL") != "":
		return sqldb.MySQL
	case os.Getenv("OA_TEST_MSSQL_URL") != "":
		return sqldb.MSSQL
	}
	return ""
}

func adminURL(p sqldb.Provider) string {
	switch p {
	case sqldb.Postgres:
		return os.Getenv("OA_TEST_DATABASE_URL")
	case sqldb.MySQL:
		return os.Getenv("OA_TEST_MYSQL_URL")
	case sqldb.MSSQL:
		return os.Getenv("OA_TEST_MSSQL_URL")
	}
	return ""
}

// MigrationsDir is the repository's migrations root.
func MigrationsDir() string { return filepath.Join("..", "..", "..", "migrations") }

func name() string { return fmt.Sprintf("oa_t_%d_%d", time.Now().UnixNano()%1e12, rand.Int63()%1e6) }

// Admin returns the configured provider and an admin connection to the server (skips the test when none).
func Admin(t *testing.T) (sqldb.Provider, sqldb.Config, *sqldb.DB) {
	t.Helper()
	p := Provider()
	url := adminURL(p)
	if p == "" || url == "" {
		t.Skip("no test database configured (OA_TEST_DATABASE_URL / OA_TEST_MYSQL_URL / OA_TEST_MSSQL_URL); real database integration not run")
	}
	cfg, err := sqldb.FromURL(url)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := sqldb.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	return p, cfg, admin
}

// Empty creates an isolated, unmigrated database/schema and returns a pool bound to it and its config.
func Empty(t *testing.T) (*sqldb.DB, sqldb.Config) {
	t.Helper()
	p, cfg, admin := Admin(t)
	ctx := context.Background()
	n := name()
	ic := cfg
	switch p {
	case sqldb.Postgres:
		if _, err := admin.Exec(ctx, `CREATE SCHEMA `+n); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { admin.Exec(context.Background(), `DROP SCHEMA `+n+` CASCADE`) })
		ic.Schema = n
	case sqldb.MySQL:
		if _, err := admin.Exec(ctx, "CREATE DATABASE `"+n+"` CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { admin.Exec(context.Background(), "DROP DATABASE `"+n+"`") })
		ic.URL, ic.Name = "", n
	case sqldb.MSSQL:
		if _, err := admin.Exec(ctx, `CREATE DATABASE `+n); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			admin.Exec(context.Background(), `ALTER DATABASE `+n+` SET SINGLE_USER WITH ROLLBACK IMMEDIATE`)
			admin.Exec(context.Background(), `DROP DATABASE `+n)
		})
		ic.URL, ic.Name = "", n
	}
	ic.MaxConns = 20
	pool, err := sqldb.Open(ctx, ic)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool, ic
}

// NewDB is Empty plus all migrations.
func NewDB(t *testing.T) *sqldb.DB {
	t.Helper()
	pool, _ := Empty(t)
	if err := db.Apply(context.Background(), pool, db.Options{Dir: MigrationsDir()}); err != nil {
		t.Fatal(err)
	}
	return pool
}
