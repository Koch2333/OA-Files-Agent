package sqldb

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type pgBackend struct{ pool *pgxpool.Pool }

type pgRow struct{ r pgx.Row }

func (r pgRow) Scan(dest ...any) error {
	err := r.r.Scan(dest...)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNoRows
	}
	return err
}

func (b *pgBackend) exec(ctx context.Context, q string, args []any) (CommandTag, error) {
	t, err := b.pool.Exec(ctx, translate(Postgres, q).sql, args...)
	return CommandTag{t.RowsAffected()}, err
}

func (b *pgBackend) query(ctx context.Context, q string, args []any) (Rows, error) {
	r, err := b.pool.Query(ctx, translate(Postgres, q).sql, args...)
	if err != nil {
		return nil, err
	}
	return r, nil
}

func (b *pgBackend) queryRow(ctx context.Context, q string, args []any) Row {
	return pgRow{b.pool.QueryRow(ctx, translate(Postgres, q).sql, args...)}
}

func (b *pgBackend) begin(ctx context.Context) (Tx, error) {
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &pgTx{tx}, nil
}

func (b *pgBackend) ping(ctx context.Context) error { return b.pool.Ping(ctx) }
func (b *pgBackend) close()                         { b.pool.Close() }

type pgTx struct{ tx pgx.Tx }

func (t *pgTx) Exec(ctx context.Context, q string, args ...any) (CommandTag, error) {
	c, err := t.tx.Exec(ctx, translate(Postgres, q).sql, args...)
	return CommandTag{c.RowsAffected()}, err
}
func (t *pgTx) Query(ctx context.Context, q string, args ...any) (Rows, error) {
	r, err := t.tx.Query(ctx, translate(Postgres, q).sql, args...)
	if err != nil {
		return nil, err
	}
	return r, nil
}
func (t *pgTx) QueryRow(ctx context.Context, q string, args ...any) Row {
	return pgRow{t.tx.QueryRow(ctx, translate(Postgres, q).sql, args...)}
}
func (t *pgTx) Commit(ctx context.Context) error   { return t.tx.Commit(ctx) }
func (t *pgTx) Rollback(ctx context.Context) error { return t.tx.Rollback(ctx) }
func (t *pgTx) Dialect() Dialect                   { return pgDialect{} }
func (t *pgTx) LockKey(ctx context.Context, key string) error {
	_, err := t.tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, lockHash(key))
	return err
}
