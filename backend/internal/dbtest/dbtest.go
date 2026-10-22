// Package dbtest gives integration tests an isolated, fully migrated schema on OA_TEST_DATABASE_URL.
package dbtest

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"oa-agent/backend/internal/db"
	"oa-agent/backend/internal/store"
)

// New skips the test when no database is configured.
func New(t *testing.T) *store.Store {
	t.Helper()
	url := os.Getenv("OA_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("OA_TEST_DATABASE_URL not set; real PostgreSQL integration not run")
	}
	ctx := context.Background()
	admin, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	schema := "oa_t_" + fmt.Sprintf("%d_%d", time.Now().UnixNano(), rand.Int63())
	q := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+q); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		admin.Exec(context.Background(), "DROP SCHEMA "+q+" CASCADE")
		admin.Close(context.Background())
	})
	if _, err = admin.Exec(ctx, "SET search_path TO "+q); err != nil {
		t.Fatal(err)
	}
	if err = db.MigrateDir(ctx, admin, filepath.Join("..", "..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	cfg, _ := pgxpool.ParseConfig(url)
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return &store.Store{Pool: pool}
}
