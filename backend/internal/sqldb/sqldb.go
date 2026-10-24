// Package sqldb is the single door between the application and its relational database. It exposes the small
// pgx-shaped surface the store layer already used (Exec/Query/QueryRow/Begin, ErrNoRows) over three engines:
// PostgreSQL (native pgx pool), MySQL 8+ and SQL Server 2019+ (database/sql). SQL is written once in a restricted
// PostgreSQL-flavoured subset and rewritten per dialect (see translate.go).
package sqldb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type Provider string

const (
	Postgres Provider = "postgres"
	MySQL    Provider = "mysql"
	MSSQL    Provider = "mssql"
)

// ParseProvider accepts the spellings people actually use.
func ParseProvider(s string) (Provider, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "postgres", "postgresql", "pg", "pgsql":
		return Postgres, nil
	case "mysql":
		return MySQL, nil
	case "mssql", "sqlserver", "sql-server", "sqlsrv":
		return MSSQL, nil
	}
	return "", fmt.Errorf("unsupported database provider %q (use postgres, mysql or mssql)", s)
}

func (p Provider) Label() string {
	switch p {
	case Postgres:
		return "PostgreSQL"
	case MySQL:
		return "MySQL"
	case MSSQL:
		return "SQL Server"
	}
	return string(p)
}

func (p Provider) DefaultPort() int {
	switch p {
	case Postgres:
		return 5432
	case MySQL:
		return 3306
	case MSSQL:
		return 1433
	}
	return 0
}

// ErrNoRows is returned by Row.Scan when the query produced no row.
var ErrNoRows = sql.ErrNoRows

type Row interface{ Scan(dest ...any) error }

type Rows interface {
	Next() bool
	Scan(dest ...any) error
	Close()
	Err() error
}

// CommandTag reports the rows a statement matched (matched, not merely changed, on every engine).
type CommandTag struct{ n int64 }

func (c CommandTag) RowsAffected() int64 { return c.n }

type Queryer interface {
	Exec(ctx context.Context, sql string, args ...any) (CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) Row
}

type Tx interface {
	Queryer
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
	// Dialect of the connection this transaction runs on.
	Dialect() Dialect
	// LockKey takes an exclusive, transaction-scoped application lock named key (released at commit/rollback).
	LockKey(ctx context.Context, key string) error
}

// ErrEmpty is returned for an empty configuration.
var ErrEmpty = errors.New("database is not configured")

type backend interface {
	exec(ctx context.Context, q string, args []any) (CommandTag, error)
	query(ctx context.Context, q string, args []any) (Rows, error)
	queryRow(ctx context.Context, q string, args []any) Row
	begin(ctx context.Context) (Tx, error)
	ping(ctx context.Context) error
	close()
}

// DB is a connection pool for one of the supported engines.
type DB struct {
	b       backend
	dialect Dialect
	cfg     Config
}

func (db *DB) Provider() Provider { return db.dialect.Provider() }
func (db *DB) Dialect() Dialect   { return db.dialect }

// Config returns the settings the pool was opened with (including the password: callers must never expose it).
func (db *DB) Config() Config { return db.cfg }

func (db *DB) Exec(ctx context.Context, q string, args ...any) (CommandTag, error) {
	return db.b.exec(ctx, q, args)
}
func (db *DB) Query(ctx context.Context, q string, args ...any) (Rows, error) {
	return db.b.query(ctx, q, args)
}
func (db *DB) QueryRow(ctx context.Context, q string, args ...any) Row {
	return db.b.queryRow(ctx, q, args)
}
func (db *DB) Begin(ctx context.Context) (Tx, error) { return db.b.begin(ctx) }
func (db *DB) Ping(ctx context.Context) error        { return db.b.ping(ctx) }
func (db *DB) Close()                                { db.b.close() }

const maxTxAttempts = 3

// InTx runs fn in a transaction: it commits when fn returns nil, rolls back on error or panic (the panic is
// re-raised) and retries the whole function only for errors the dialect classifies as transient (deadlock victim,
// serialization failure). fn must therefore be safe to run again.
func (db *DB) InTx(ctx context.Context, fn func(Tx) error) (err error) {
	for attempt := 1; ; attempt++ {
		err = db.inTxOnce(ctx, fn)
		if err == nil || attempt >= maxTxAttempts || !db.dialect.IsRetryable(err) || ctx.Err() != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(time.Duration(attempt) * 25 * time.Millisecond):
		}
	}
}

func (db *DB) inTxOnce(ctx context.Context, fn func(Tx) error) (err error) {
	tx, err := db.b.begin(ctx)
	if err != nil {
		return err
	}
	done := false
	defer func() {
		if r := recover(); r != nil {
			_ = tx.Rollback(context.Background())
			panic(r)
		}
		if !done {
			_ = tx.Rollback(context.Background())
		}
	}()
	if err = fn(tx); err != nil {
		return err
	}
	done = true
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	return nil
}

// JSON converts an encoded JSON document into a statement argument every engine accepts (text, never binary).
// A nil/empty document becomes SQL NULL.
func JSON(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return string(b)
}

// JSONOr is JSON with a default document for empty input.
func JSONOr(b []byte, def string) string {
	if len(b) == 0 {
		return def
	}
	return string(b)
}
