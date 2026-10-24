package sqldb

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// sqlBackend serves MySQL and SQL Server through database/sql.
type sqlBackend struct {
	db *sql.DB
	p  Provider
	d  Dialect
}

type sqlRows struct{ r *sql.Rows }

func (r sqlRows) Next() bool          { return r.r.Next() }
func (r sqlRows) Scan(d ...any) error { return r.r.Scan(d...) }
func (r sqlRows) Close()              { _ = r.r.Close() }
func (r sqlRows) Err() error          { return r.r.Err() }

func convertArg(a any) (any, error) {
	switch v := a.(type) {
	case time.Time:
		return v.UTC(), nil
	case *time.Time:
		if v == nil {
			return nil, nil
		}
		return v.UTC(), nil
	case json.RawMessage:
		if v == nil {
			return nil, nil
		}
		return string(v), nil
	case []string:
		return nil, fmt.Errorf("sqldb: pass list columns through Dialect.StringsArg")
	}
	return a, nil
}

func (b *sqlBackend) prep(q string, args []any) (string, []any, error) {
	t := translate(b.p, q)
	args, err := t.expand(args)
	if err != nil {
		return "", nil, err
	}
	out := make([]any, len(args))
	for i, a := range args {
		if out[i], err = convertArg(a); err != nil {
			return "", nil, err
		}
	}
	return t.sql, out, nil
}

type execQ interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func doExec(ctx context.Context, e execQ, b *sqlBackend, q string, args []any) (CommandTag, error) {
	s, a, err := b.prep(q, args)
	if err != nil {
		return CommandTag{}, err
	}
	r, err := e.ExecContext(ctx, s, a...)
	if err != nil {
		return CommandTag{}, err
	}
	n, _ := r.RowsAffected()
	return CommandTag{n}, nil
}

func doQuery(ctx context.Context, e execQ, b *sqlBackend, q string, args []any) (Rows, error) {
	s, a, err := b.prep(q, args)
	if err != nil {
		return nil, err
	}
	r, err := e.QueryContext(ctx, s, a...)
	if err != nil {
		return nil, err
	}
	return sqlRows{r}, nil
}

type errRow struct{ err error }

func (r errRow) Scan(...any) error { return r.err }

func doQueryRow(ctx context.Context, e execQ, b *sqlBackend, q string, args []any) Row {
	s, a, err := b.prep(q, args)
	if err != nil {
		return errRow{err}
	}
	return e.QueryRowContext(ctx, s, a...)
}

func (b *sqlBackend) exec(ctx context.Context, q string, args []any) (CommandTag, error) {
	return doExec(ctx, b.db, b, q, args)
}
func (b *sqlBackend) query(ctx context.Context, q string, args []any) (Rows, error) {
	return doQuery(ctx, b.db, b, q, args)
}
func (b *sqlBackend) queryRow(ctx context.Context, q string, args []any) Row {
	return doQueryRow(ctx, b.db, b, q, args)
}
func (b *sqlBackend) ping(ctx context.Context) error { return b.db.PingContext(ctx) }
func (b *sqlBackend) close()                         { _ = b.db.Close() }

func (b *sqlBackend) begin(ctx context.Context) (Tx, error) {
	if b.p == MySQL {
		// Manual transaction on a dedicated connection: GET_LOCK locks are session scoped and must be released
		// after COMMIT on that same connection.
		conn, err := b.db.Conn(ctx)
		if err != nil {
			return nil, err
		}
		if _, err = conn.ExecContext(ctx, "START TRANSACTION"); err != nil {
			_ = conn.Close()
			return nil, err
		}
		return &sqlTx{b: b, q: conn, conn: conn}, nil
	}
	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &sqlTx{b: b, q: tx, tx: tx}, nil
}

type sqlTx struct {
	b     *sqlBackend
	q     execQ
	tx    *sql.Tx
	conn  *sql.Conn
	locks []string
	done  bool
}

func (t *sqlTx) Exec(ctx context.Context, q string, args ...any) (CommandTag, error) {
	return doExec(ctx, t.q, t.b, q, args)
}
func (t *sqlTx) Query(ctx context.Context, q string, args ...any) (Rows, error) {
	return doQuery(ctx, t.q, t.b, q, args)
}
func (t *sqlTx) QueryRow(ctx context.Context, q string, args ...any) Row {
	return doQueryRow(ctx, t.q, t.b, q, args)
}
func (t *sqlTx) Dialect() Dialect { return t.b.d }

func (t *sqlTx) LockKey(ctx context.Context, key string) error {
	name := lockName(key)
	switch t.b.p {
	case MySQL:
		var got sql.NullInt64
		if err := t.conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, 30)", name).Scan(&got); err != nil {
			return errLock("acquire lock", err)
		}
		if !got.Valid || got.Int64 != 1 {
			return fmt.Errorf("acquire lock: timed out")
		}
		t.locks = append(t.locks, name)
	case MSSQL:
		var rc int
		err := t.tx.QueryRowContext(ctx, `DECLARE @r INT; EXEC @r = sp_getapplock @Resource=@p1, @LockMode='Exclusive', @LockOwner='Transaction', @LockTimeout=30000; SELECT @r`, name).Scan(&rc)
		if err != nil {
			return errLock("acquire lock", err)
		}
		if rc < 0 {
			return fmt.Errorf("acquire lock: sp_getapplock returned %d", rc)
		}
	}
	return nil
}

func (t *sqlTx) finish(ctx context.Context, stmt string) error {
	if t.done {
		return sql.ErrTxDone
	}
	t.done = true
	if t.tx != nil {
		if stmt == "COMMIT" {
			return t.tx.Commit()
		}
		return t.tx.Rollback()
	}
	_, err := t.conn.ExecContext(ctx, stmt)
	bad := err != nil
	for _, name := range t.locks {
		if _, e := t.conn.ExecContext(context.Background(), "SELECT RELEASE_LOCK(?)", name); e != nil {
			bad = true
		}
	}
	if bad {
		_ = t.conn.Raw(func(any) error { return errBadConn })
	}
	_ = t.conn.Close()
	return err
}

func (t *sqlTx) Commit(ctx context.Context) error { return t.finish(ctx, "COMMIT") }
func (t *sqlTx) Rollback(ctx context.Context) error {
	if t.done {
		return nil
	}
	return t.finish(ctx, "ROLLBACK")
}
