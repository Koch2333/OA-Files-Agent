package diag

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned for an unknown report id.
var ErrNotFound = errors.New("error report not found")

// Retention limits.
const (
	MaxGroups      = 5000
	ResolvedMaxAge = 90 * 24 * time.Hour
)

type pgSink struct{ pool *pgxpool.Pool }

// Attach makes the default reporter persist to pool; queued and pending reports are written right away.
func Attach(pool *pgxpool.Pool) { Default().Attach(pool) }

// Attach makes r persist to pool.
func (r *Reporter) Attach(pool *pgxpool.Pool) {
	if pool == nil {
		return
	}
	r.setSink(&pgSink{pool})
}

const upsertSQL = `
INSERT INTO error_reports (id, fingerprint, source, severity, code, message, stack, details, request_id, username, count, first_seen, last_seen)
VALUES ($1,$2,$3,$4,$5,$6,$7,COALESCE($8::jsonb,'{}'::jsonb),$9,$10,$11,$12,$13)
ON CONFLICT (fingerprint) DO UPDATE SET
  count = error_reports.count + EXCLUDED.count,
  last_seen = GREATEST(error_reports.last_seen, EXCLUDED.last_seen),
  severity = EXCLUDED.severity,
  message = EXCLUDED.message,
  stack = CASE WHEN EXCLUDED.stack <> '' THEN EXCLUDED.stack ELSE error_reports.stack END,
  details = CASE WHEN EXCLUDED.details <> '{}'::jsonb THEN EXCLUDED.details ELSE error_reports.details END,
  request_id = EXCLUDED.request_id,
  username = EXCLUDED.username,
  resolved = false, resolved_at = NULL, resolved_by = ''`

func (p *pgSink) write(ctx context.Context, a *agg) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	var details any
	if len(a.rec.details) > 0 {
		details = string(a.rec.details)
	}
	if _, err = tx.Exec(ctx, upsertSQL, a.rec.id, a.rec.fp, a.rec.source, a.rec.severity, a.rec.code, a.rec.message, a.rec.stack, details,
		a.rec.requestID, a.rec.user, a.count, a.first, a.rec.at); err != nil {
		return err
	}
	for _, o := range a.occ {
		if _, err = tx.Exec(ctx, `INSERT INTO error_occurrences(report_id, at, request_id, username) VALUES ($1,$2,$3,$4)`, a.rec.id, o.at, o.requestID, o.user); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `DELETE FROM error_occurrences WHERE report_id=$1 AND id NOT IN (SELECT id FROM error_occurrences WHERE report_id=$1 ORDER BY at DESC, id DESC LIMIT $2)`, a.rec.id, maxOccurrence); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Group is the list view of one error report (contract schema ErrorReport).
type Group struct {
	ID        string    `json:"id"`
	Source    string    `json:"source"`
	Severity  string    `json:"severity"`
	Code      string    `json:"code"`
	Message   string    `json:"message"`
	Count     int       `json:"count"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	Resolved  bool      `json:"resolved"`
	RequestID string    `json:"request_id"`
}

// Occurrence is one recent occurrence.
type Occurrence struct {
	At        time.Time `json:"at"`
	RequestID string    `json:"request_id"`
	User      string    `json:"user"`
}

// GroupDetail is ErrorReportDetail.
type GroupDetail struct {
	Group
	Stack       string         `json:"stack"`
	Details     map[string]any `json:"details"`
	Occurrences []Occurrence   `json:"occurrences"`
}

// Filter selects reports for List.
type Filter struct {
	Source, Severity, Q string
	Resolved            *bool
	Page, Size          int
}

const groupCols = `id::text, source, severity, code, message, count, first_seen, last_seen, resolved, request_id`

func scanGroup(r pgx.Row) (Group, error) {
	var g Group
	err := r.Scan(&g.ID, &g.Source, &g.Severity, &g.Code, &g.Message, &g.Count, &g.FirstSeen, &g.LastSeen, &g.Resolved, &g.RequestID)
	return g, err
}

// List returns one page of reports, newest first.
func List(ctx context.Context, pool *pgxpool.Pool, f Filter) ([]Group, int, error) {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.Size < 1 || f.Size > 200 {
		f.Size = 20
	}
	where := ` WHERE ($1 = '' OR source = $1) AND ($2 = '' OR severity = $2) AND ($3::boolean IS NULL OR resolved = $3)
 AND ($4 = '' OR position(lower($4) in lower(message)) > 0 OR position(lower($4) in lower(code)) > 0 OR position(lower($4) in lower(request_id)) > 0
      OR starts_with(replace(id::text,'-',''), lower($4))
      OR EXISTS (SELECT 1 FROM error_occurrences o WHERE o.report_id = error_reports.id AND position(lower($4) in lower(o.request_id)) > 0))`
	var total int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM error_reports`+where, f.Source, f.Severity, f.Resolved, f.Q).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := pool.Query(ctx, `SELECT `+groupCols+` FROM error_reports`+where+` ORDER BY last_seen DESC, id LIMIT $5 OFFSET $6`, f.Source, f.Severity, f.Resolved, f.Q, f.Size, (f.Page-1)*f.Size)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Group{}
	for rows.Next() {
		g, err := scanGroup(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, g)
	}
	return out, total, rows.Err()
}

// Get returns one report with its recent occurrences.
func Get(ctx context.Context, pool *pgxpool.Pool, id string) (GroupDetail, error) {
	var d GroupDetail
	var raw []byte
	err := pool.QueryRow(ctx, `SELECT `+groupCols+`, stack, details FROM error_reports WHERE id::text = lower($1)`, id).Scan(
		&d.ID, &d.Source, &d.Severity, &d.Code, &d.Message, &d.Count, &d.FirstSeen, &d.LastSeen, &d.Resolved, &d.RequestID, &d.Stack, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, ErrNotFound
	}
	if err != nil {
		return d, err
	}
	d.Details = map[string]any{}
	_ = json.Unmarshal(raw, &d.Details)
	d.Occurrences = []Occurrence{}
	rows, err := pool.Query(ctx, `SELECT at, request_id, username FROM error_occurrences WHERE report_id::text = $1 ORDER BY at DESC, id DESC LIMIT $2`, d.ID, maxOccurrence)
	if err != nil {
		return d, err
	}
	defer rows.Close()
	for rows.Next() {
		var o Occurrence
		if err := rows.Scan(&o.At, &o.RequestID, &o.User); err != nil {
			return d, err
		}
		d.Occurrences = append(d.Occurrences, o)
	}
	return d, rows.Err()
}

// SetResolved marks a report resolved or open again.
func SetResolved(ctx context.Context, pool *pgxpool.Pool, id string, resolved bool, by string) (GroupDetail, error) {
	tag, err := pool.Exec(ctx, `UPDATE error_reports SET resolved=$2, resolved_at=CASE WHEN $2 THEN now() END, resolved_by=CASE WHEN $2 THEN $3 ELSE '' END WHERE id::text = lower($1)`, id, resolved, by)
	if err != nil {
		return GroupDetail{}, err
	}
	if tag.RowsAffected() == 0 {
		return GroupDetail{}, ErrNotFound
	}
	return Get(ctx, pool, id)
}

// Delete removes a report and its occurrences.
func Delete(ctx context.Context, pool *pgxpool.Pool, id string) error {
	tag, err := pool.Exec(ctx, `DELETE FROM error_reports WHERE id::text = lower($1)`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// OpenCount is the number of unresolved reports.
func OpenCount(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	var n int
	err := pool.QueryRow(ctx, `SELECT count(*) FROM error_reports WHERE NOT resolved`).Scan(&n)
	return n, err
}

// UncleanExitOpen reports whether the "last run ended uncleanly" report is still unresolved.
func UncleanExitOpen(ctx context.Context, pool *pgxpool.Pool) (bool, error) {
	var ok bool
	err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM error_reports WHERE source='supervisor' AND code=$1 AND NOT resolved)`, CodeUncleanExit).Scan(&ok)
	return ok, err
}

// Recent returns the n most recently seen reports with details (for the diagnostic bundle).
func Recent(ctx context.Context, pool *pgxpool.Pool, n int) ([]GroupDetail, error) {
	rows, err := pool.Query(ctx, `SELECT id::text FROM error_reports ORDER BY last_seen DESC LIMIT $1`, n)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	out := make([]GroupDetail, 0, len(ids))
	for _, id := range ids {
		d, err := Get(ctx, pool, id)
		if err != nil {
			continue
		}
		out = append(out, d)
	}
	return out, nil
}

// Retention deletes resolved groups older than 90 days, trims occurrences beyond 20 per group and keeps at most
// MaxGroups groups (oldest resolved first, then oldest overall). It returns the number of groups deleted.
func Retention(ctx context.Context, pool *pgxpool.Pool, now time.Time) (int, error) {
	total := 0
	tag, err := pool.Exec(ctx, `DELETE FROM error_reports WHERE resolved AND last_seen < $1`, now.Add(-ResolvedMaxAge))
	if err != nil {
		return 0, err
	}
	total += int(tag.RowsAffected())
	if _, err = pool.Exec(ctx, `DELETE FROM error_occurrences WHERE id IN (SELECT id FROM (SELECT id, row_number() OVER (PARTITION BY report_id ORDER BY at DESC, id DESC) rn FROM error_occurrences) x WHERE rn > $1)`, maxOccurrence); err != nil {
		return total, err
	}
	tag, err = pool.Exec(ctx, `DELETE FROM error_reports WHERE id IN (SELECT id FROM error_reports ORDER BY resolved DESC, last_seen ASC LIMIT GREATEST((SELECT count(*) FROM error_reports) - $1, 0))`, MaxGroups)
	if err != nil {
		return total, err
	}
	total += int(tag.RowsAffected())
	return total, nil
}
