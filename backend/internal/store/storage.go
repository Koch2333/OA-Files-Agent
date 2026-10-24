package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"oa-agent/backend/internal/sqldb"

	"oa-agent/backend/internal/imports"
)

// ---- storage connections ----

type LastTest struct {
	OK      bool      `json:"ok"`
	At      time.Time `json:"at"`
	Message string    `json:"message"`
}

type StorageConn struct {
	ID           string
	Name         string
	Kind         string
	Settings     map[string]string
	SecretsEnc   []byte
	BasePath     string
	Enabled      bool
	UseForImport bool
	Replicate    string
	AutoSince    *time.Time
	LastTest     *LastTest
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type ReplCounts struct {
	Queued  int `json:"queued"`
	Running int `json:"running"`
	Done    int `json:"done"`
	Skipped int `json:"skipped"`
	Failed  int `json:"failed"`
}

const connCols = `id::text, name, kind, settings, secrets_enc, base_path, enabled, use_for_import, replicate, auto_since, last_test, created_at, updated_at`

func scanConn(r sqldb.Row) (StorageConn, error) {
	var c StorageConn
	var settings, lt []byte
	err := r.Scan(&c.ID, &c.Name, &c.Kind, &settings, &c.SecretsEnc, &c.BasePath, &c.Enabled, &c.UseForImport, &c.Replicate, &c.AutoSince, &lt, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, sqldb.ErrNoRows) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, err
	}
	_ = json.Unmarshal(settings, &c.Settings)
	if c.Settings == nil {
		c.Settings = map[string]string{}
	}
	if len(lt) > 0 && string(lt) != "null" {
		var t LastTest
		if json.Unmarshal(lt, &t) == nil {
			c.LastTest = &t
		}
	}
	return c, nil
}

func (s *Store) isUniqueViolation(err error) bool {
	return err != nil && s.Dialect().IsUniqueViolation(err)
}

func (s *Store) ListConns(ctx context.Context) ([]StorageConn, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+connCols+` FROM storage_connections ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StorageConn{}
	for rows.Next() {
		c, err := scanConn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) GetConn(ctx context.Context, id string) (StorageConn, error) {
	if !validUUID(id) {
		return StorageConn{}, ErrNotFound
	}
	return scanConn(s.Pool.QueryRow(ctx, `SELECT `+connCols+` FROM storage_connections WHERE id=$1`, id))
}

func (s *Store) CreateConn(ctx context.Context, c StorageConn) (StorageConn, error) {
	id, err := imports.NewID()
	if err != nil {
		return c, err
	}
	st, _ := json.Marshal(c.Settings)
	var since *time.Time
	if c.Replicate == "auto" {
		n := time.Now().UTC()
		since = &n
	}
	if _, err = s.Pool.Exec(ctx, `INSERT INTO storage_connections(id,name,kind,settings,secrets_enc,base_path,enabled,use_for_import,replicate,auto_since) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		id, c.Name, c.Kind, sqldb.JSON(st), c.SecretsEnc, c.BasePath, c.Enabled, c.UseForImport, c.Replicate, since); err != nil {
		if s.isUniqueViolation(err) {
			return c, ErrConflict
		}
		return c, err
	}
	return scanConn(s.Pool.QueryRow(ctx, `SELECT `+connCols+` FROM storage_connections WHERE id=$1`, id))
}

// UpdateConn replaces the mutable fields. auto_since is (re)set when replicate turns to auto.
func (s *Store) UpdateConn(ctx context.Context, c StorageConn) (StorageConn, error) {
	st, _ := json.Marshal(c.Settings)
	if _, err := s.Pool.Exec(ctx, `UPDATE storage_connections SET name=$2, settings=$3, secrets_enc=$4, base_path=$5, enabled=$6, use_for_import=$7,
		auto_since = CASE WHEN $8='auto' AND replicate<>'auto' THEN now() WHEN $8<>'auto' THEN NULL ELSE auto_since END, replicate=$8, updated_at=now() WHERE id=$1`,
		c.ID, c.Name, sqldb.JSON(st), c.SecretsEnc, c.BasePath, c.Enabled, c.UseForImport, c.Replicate); err != nil {
		if s.isUniqueViolation(err) {
			return c, ErrConflict
		}
		return c, err
	}
	return scanConn(s.Pool.QueryRow(ctx, `SELECT `+connCols+` FROM storage_connections WHERE id=$1`, c.ID))
}

func (s *Store) DeleteConn(ctx context.Context, id string) error {
	if !validUUID(id) {
		return ErrNotFound
	}
	t, err := s.Pool.Exec(ctx, `DELETE FROM storage_connections WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if t.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SetLastTest(ctx context.Context, id string, t LastTest) error {
	b, _ := json.Marshal(t)
	_, err := s.Pool.Exec(ctx, `UPDATE storage_connections SET last_test=$2 WHERE id=$1`, id, sqldb.JSON(b))
	return err
}

func (s *Store) ConnCounts(ctx context.Context) (map[string]ReplCounts, error) {
	rows, err := s.Pool.Query(ctx, `SELECT connection_id::text, status, count(*) FROM replications GROUP BY connection_id, status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ReplCounts{}
	for rows.Next() {
		var id, st string
		var n int
		if err := rows.Scan(&id, &st, &n); err != nil {
			return nil, err
		}
		c := out[id]
		switch st {
		case "queued":
			c.Queued = n
		case "running":
			c.Running = n
		case "done":
			c.Done = n
		case "skipped":
			c.Skipped = n
		case "failed":
			c.Failed = n
		}
		out[id] = c
	}
	return out, rows.Err()
}

// ---- replications ----

type Replication struct {
	ID             string    `json:"id"`
	ConnectionID   string    `json:"connection_id"`
	ConnectionName string    `json:"connection_name"`
	LocalPath      string    `json:"local_path"`
	RemotePath     string    `json:"remote_path"`
	Status         string    `json:"status"`
	Attempts       int       `json:"attempts"`
	Error          *string   `json:"error"`
	SizeBytes      int64     `json:"size_bytes"`
	UpdatedAt      time.Time `json:"updated_at"`
	LedgerID       string    `json:"-"`
	SHA256         string    `json:"-"`
}

func likePrefix(p string) string {
	return sqldb.EscapeLike(p) + "%"
}

// EnqueueMissing queues every archived ledger entry (optionally below a path prefix and archived since a time)
// that has no replication row for the connection yet. Existing rows are never touched.
func (s *Store) EnqueueMissing(ctx context.Context, connID, prefix string, since *time.Time) (int64, error) {
	t, err := s.Pool.Exec(ctx, `INSERT INTO replications(id, connection_id, ledger_id, local_path, remote_path, size_bytes, sha256)
		SELECT gen_random_uuid(), $1::uuid, l.id, l.archive_path, l.archive_path, COALESCE(f.size_bytes,0), l.sha256
		FROM ledger_entries l JOIN file_records f ON f.id = l.file_id
		WHERE l.status='archived' AND ($2='' OR l.archive_path LIKE $3 ESCAPE '!') AND ($4::timestamptz IS NULL OR l.executed_at >= $4)
		AND NOT EXISTS (SELECT 1 FROM replications x WHERE x.connection_id=$1::uuid AND x.ledger_id=l.id)
		ON CONFLICT (connection_id, ledger_id) DO NOTHING`, connID, prefix, likePrefix(prefix), since)
	return t.RowsAffected(), err
}

// AutoConnections lists enabled connections whose replication policy is automatic.
func (s *Store) AutoConnections(ctx context.Context) ([]StorageConn, error) {
	all, err := s.ListConns(ctx)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, c := range all {
		if c.Enabled && c.Replicate == "auto" {
			out = append(out, c)
		}
	}
	return out, nil
}

// ClaimReplication takes the next due queued item of an enabled connection and marks it running.
func (s *Store) ClaimReplication(ctx context.Context) (Replication, error) {
	var r Replication
	err := s.Pool.InTx(ctx, func(tx sqldb.Tx) error {
		var id string
		if err := tx.QueryRow(ctx, `SELECT r.id FROM replications r JOIN storage_connections c ON c.id=r.connection_id
			WHERE r.status='queued' AND r.next_attempt_at <= now() AND c.enabled=true AND c.replicate<>'off'
			ORDER BY r.next_attempt_at LIMIT 1 FOR UPDATE OF r SKIP LOCKED`).Scan(&id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE replications SET status='running', attempts=attempts+1, updated_at=now() WHERE id=$1`, id); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT id::text, connection_id::text, ledger_id::text, local_path, remote_path, size_bytes, sha256, status, attempts, error, updated_at FROM replications WHERE id=$1`, id).
			Scan(&r.ID, &r.ConnectionID, &r.LedgerID, &r.LocalPath, &r.RemotePath, &r.SizeBytes, &r.SHA256, &r.Status, &r.Attempts, &r.Error, &r.UpdatedAt)
	})
	if errors.Is(err, sqldb.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// FinishReplication records the outcome. retryAt != nil re-queues the item for a later attempt.
func (s *Store) FinishReplication(ctx context.Context, id, status string, errMsg *string, remotePath string, retryAt *time.Time) error {
	if retryAt != nil {
		_, err := s.Pool.Exec(ctx, `UPDATE replications SET status='queued', error=$2, next_attempt_at=$3, updated_at=now() WHERE id=$1`, id, errMsg, *retryAt)
		return err
	}
	_, err := s.Pool.Exec(ctx, `UPDATE replications SET status=$2, error=$3, remote_path=CASE WHEN $4='' THEN remote_path ELSE $4 END, updated_at=now() WHERE id=$1`, id, status, errMsg, remotePath)
	return err
}

func (s *Store) RecoverReplications(ctx context.Context) (int64, error) {
	t, err := s.Pool.Exec(ctx, `UPDATE replications SET status='queued', next_attempt_at=$1, updated_at=now() WHERE status='running'`, time.Now().UTC().Add(-time.Second))
	return t.RowsAffected(), err
}

// LedgerForReplication returns the current archive path and status of a ledger entry (the file may have been
// moved or removed in the file manager since it was queued).
func (s *Store) LedgerForReplication(ctx context.Context, ledgerID string) (path, status string, err error) {
	err = s.Pool.QueryRow(ctx, `SELECT archive_path, status FROM ledger_entries WHERE id=$1`, ledgerID).Scan(&path, &status)
	if errors.Is(err, sqldb.ErrNoRows) {
		return "", "", ErrNotFound
	}
	return
}

func (s *Store) ListReplications(ctx context.Context, p Page, connID, status string) ([]Replication, int, error) {
	page, size := p.norm()
	var conds []string
	var args []any
	add := func(c string, v any) {
		args = append(args, v)
		conds = append(conds, strings.ReplaceAll(c, "$$", fmt.Sprintf("$%d", len(args))))
	}
	if connID != "" {
		add(`r.connection_id=$$::uuid`, connID)
	}
	if status != "" {
		add(`r.status=$$`, status)
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}
	var total int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM replications r `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, size, (page-1)*size)
	rows, err := s.Pool.Query(ctx, fmt.Sprintf(`SELECT r.id::text, r.connection_id::text, c.name, r.local_path, r.remote_path, r.status, r.attempts, r.error, r.size_bytes, r.updated_at
		FROM replications r JOIN storage_connections c ON c.id=r.connection_id %s ORDER BY r.updated_at DESC, r.id LIMIT $%d OFFSET $%d`, where, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Replication{}
	for rows.Next() {
		var r Replication
		if err := rows.Scan(&r.ID, &r.ConnectionID, &r.ConnectionName, &r.LocalPath, &r.RemotePath, &r.Status, &r.Attempts, &r.Error, &r.SizeBytes, &r.UpdatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

func (s *Store) GetReplication(ctx context.Context, id string) (Replication, error) {
	var r Replication
	if !validUUID(id) {
		return r, ErrNotFound
	}
	err := s.Pool.QueryRow(ctx, `SELECT r.id::text, r.connection_id::text, c.name, r.local_path, r.remote_path, r.status, r.attempts, r.error, r.size_bytes, r.updated_at
		FROM replications r JOIN storage_connections c ON c.id=r.connection_id WHERE r.id=$1`, id).
		Scan(&r.ID, &r.ConnectionID, &r.ConnectionName, &r.LocalPath, &r.RemotePath, &r.Status, &r.Attempts, &r.Error, &r.SizeBytes, &r.UpdatedAt)
	if errors.Is(err, sqldb.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// RetryReplication re-queues a failed item.
func (s *Store) RetryReplication(ctx context.Context, id string) (Replication, error) {
	r, err := s.GetReplication(ctx, id)
	if err != nil {
		return r, err
	}
	if r.Status != "failed" {
		return r, ErrState
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE replications SET status='queued', attempts=0, error=NULL, next_attempt_at=$2, updated_at=now() WHERE id=$1 AND status='failed'`, id, time.Now().UTC().Add(-time.Second)); err != nil {
		return r, err
	}
	return s.GetReplication(ctx, id)
}

// SetConnSecrets replaces only the sealed credentials (used when a provider rotates an OAuth refresh token). It does
// not touch updated_at, so cached providers stay valid.
func (s *Store) SetConnSecrets(ctx context.Context, id string, enc []byte) error {
	_, err := s.Pool.Exec(ctx, `UPDATE storage_connections SET secrets_enc=$2 WHERE id=$1`, id, enc)
	return err
}
