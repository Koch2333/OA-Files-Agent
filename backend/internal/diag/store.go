package diag

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"oa-agent/backend/internal/sqldb"
)

// ErrNotFound is returned for an unknown report id.
var ErrNotFound = errors.New("error report not found")

// Retention limits.
const (
	MaxGroups      = 5000
	ResolvedMaxAge = 90 * 24 * time.Hour
)

type dbSink struct{ pool *sqldb.DB }

// Attach makes the default reporter persist to pool; queued and pending reports are written right away.
func Attach(pool *sqldb.DB) { Default().Attach(pool) }

// Attach makes r persist to pool.
func (r *Reporter) Attach(pool *sqldb.DB) {
	if pool == nil {
		return
	}
	r.setSink(&dbSink{pool})
}

// write upserts the group (portable: read the row under a lock, then UPDATE or INSERT) and appends occurrences.
// A concurrent first insert of the same group surfaces as a unique violation and is retried once as an update.
func (p *dbSink) write(ctx context.Context, a *agg) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		err = p.pool.InTx(ctx, func(tx sqldb.Tx) error { return p.writeTx(ctx, tx, a) })
		if err == nil || !p.pool.Dialect().IsUniqueViolation(err) {
			return err
		}
	}
	return err
}

func (p *dbSink) writeTx(ctx context.Context, tx sqldb.Tx, a *agg) error {
	var details any
	if len(a.rec.details) > 0 {
		details = string(a.rec.details)
	}
	var count int
	var lastSeen time.Time
	var stack string
	var oldDetails []byte
	err := tx.QueryRow(ctx, `SELECT count, last_seen, stack, details FROM error_reports WHERE id=$1 FOR UPDATE`, a.rec.id).Scan(&count, &lastSeen, &stack, &oldDetails)
	switch {
	case errors.Is(err, sqldb.ErrNoRows):
		if details == nil {
			details = "{}"
		}
		if _, err = tx.Exec(ctx, `INSERT INTO error_reports (id, fingerprint, source, severity, code, message, stack, details, request_id, username, count, first_seen, last_seen)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, a.rec.id, a.rec.fp, a.rec.source, a.rec.severity, a.rec.code, a.rec.message, a.rec.stack, details,
			a.rec.requestID, a.rec.user, a.count, a.first, a.rec.at); err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		if a.rec.at.After(lastSeen) {
			lastSeen = a.rec.at
		}
		if a.rec.stack != "" {
			stack = a.rec.stack
		}
		det := string(oldDetails)
		if details != nil && details.(string) != "{}" {
			det = details.(string)
		}
		if det == "" {
			det = "{}"
		}
		if _, err = tx.Exec(ctx, `UPDATE error_reports SET count=$2, last_seen=$3, severity=$4, message=$5, stack=$6, details=$7, request_id=$8, username=$9,
			resolved=false, resolved_at=NULL, resolved_by='' WHERE id=$1`, a.rec.id, count+a.count, lastSeen, a.rec.severity, a.rec.message, stack, det, a.rec.requestID, a.rec.user); err != nil {
			return err
		}
	}
	for _, o := range a.occ {
		if _, err = tx.Exec(ctx, `INSERT INTO error_occurrences(report_id, at, request_id, username) VALUES ($1,$2,$3,$4)`, a.rec.id, o.at, o.requestID, o.user); err != nil {
			return err
		}
	}
	return trimOccurrences(ctx, tx, a.rec.id)
}

// trimOccurrences keeps the newest maxOccurrence rows of one group.
func trimOccurrences(ctx context.Context, q sqldb.Queryer, reportID string) error {
	rows, err := q.Query(ctx, `SELECT id FROM error_occurrences WHERE report_id=$1 ORDER BY at DESC, id DESC LIMIT 100000 OFFSET $2`, reportID, maxOccurrence)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	return deleteOccurrences(ctx, q, ids)
}

func deleteOccurrences(ctx context.Context, q sqldb.Queryer, ids []int64) error {
	for len(ids) > 0 {
		n := min(len(ids), 400)
		ph := make([]string, n)
		args := make([]any, n)
		for i := 0; i < n; i++ {
			ph[i] = "$" + strconv.Itoa(i+1)
			args[i] = ids[i]
		}
		if _, err := q.Exec(ctx, `DELETE FROM error_occurrences WHERE id IN (`+strings.Join(ph, ",")+`)`, args...); err != nil {
			return err
		}
		ids = ids[n:]
	}
	return nil
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

func scanGroup(r sqldb.Row) (Group, error) {
	var g Group
	err := r.Scan(&g.ID, &g.Source, &g.Severity, &g.Code, &g.Message, &g.Count, &g.FirstSeen, &g.LastSeen, &g.Resolved, &g.RequestID)
	return g, err
}

// List returns one page of reports, newest first.
func List(ctx context.Context, pool *sqldb.DB, f Filter) ([]Group, int, error) {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.Size < 1 || f.Size > 200 {
		f.Size = 20
	}
	var conds []string
	var args []any
	add := func(c string, v ...any) {
		for _, x := range v {
			args = append(args, x)
			c = strings.Replace(c, "?", "$"+strconv.Itoa(len(args)), 1)
		}
		conds = append(conds, c)
	}
	if f.Source != "" {
		add(`source = ?`, f.Source)
	}
	if f.Severity != "" {
		add(`severity = ?`, f.Severity)
	}
	if f.Resolved != nil {
		add(`resolved = ?`, *f.Resolved)
	}
	if f.Q != "" {
		like := "%" + sqldb.EscapeLike(f.Q) + "%"
		prefix := sqldb.EscapeLike(strings.ToLower(f.Q)) + "%"
		args = append(args, like, prefix)
		a, b := "$"+strconv.Itoa(len(args)-1), "$"+strconv.Itoa(len(args))
		conds = append(conds, `(message ILIKE `+a+` OR code ILIKE `+a+` OR request_id ILIKE `+a+`
      OR replace(id::text,'-','') LIKE `+b+` ESCAPE '!'
      OR EXISTS (SELECT 1 FROM error_occurrences o WHERE o.report_id = error_reports.id AND o.request_id ILIKE `+a+`))`)
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	var total int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM error_reports`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, f.Size, (f.Page-1)*f.Size)
	rows, err := pool.Query(ctx, `SELECT `+groupCols+` FROM error_reports`+where+` ORDER BY last_seen DESC, id LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...)
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
func Get(ctx context.Context, pool *sqldb.DB, id string) (GroupDetail, error) {
	var d GroupDetail
	var raw []byte
	err := pool.QueryRow(ctx, `SELECT `+groupCols+`, stack, details FROM error_reports WHERE id::text = lower($1)`, id).Scan(
		&d.ID, &d.Source, &d.Severity, &d.Code, &d.Message, &d.Count, &d.FirstSeen, &d.LastSeen, &d.Resolved, &d.RequestID, &d.Stack, &raw)
	if errors.Is(err, sqldb.ErrNoRows) {
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
func SetResolved(ctx context.Context, pool *sqldb.DB, id string, resolved bool, by string) (GroupDetail, error) {
	tag, err := pool.Exec(ctx, `UPDATE error_reports SET resolved=$2, resolved_at=CASE WHEN $2=true THEN now() END, resolved_by=CASE WHEN $2=true THEN $3 ELSE '' END WHERE id::text = lower($1)`, id, resolved, by)
	if err != nil {
		return GroupDetail{}, err
	}
	if tag.RowsAffected() == 0 {
		return GroupDetail{}, ErrNotFound
	}
	return Get(ctx, pool, id)
}

// Delete removes a report and its occurrences.
func Delete(ctx context.Context, pool *sqldb.DB, id string) error {
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
func OpenCount(ctx context.Context, pool *sqldb.DB) (int, error) {
	var n int
	err := pool.QueryRow(ctx, `SELECT count(*) FROM error_reports WHERE resolved=false`).Scan(&n)
	return n, err
}

// UncleanExitOpen reports whether the "last run ended uncleanly" report is still unresolved.
func UncleanExitOpen(ctx context.Context, pool *sqldb.DB) (bool, error) {
	var ok bool
	err := pool.QueryRow(ctx, `SELECT CASE WHEN EXISTS (SELECT 1 FROM error_reports WHERE source='supervisor' AND code=$1 AND resolved=false) THEN true ELSE false END`, CodeUncleanExit).Scan(&ok)
	return ok, err
}

// Recent returns the n most recently seen reports with details (for the diagnostic bundle).
func Recent(ctx context.Context, pool *sqldb.DB, n int) ([]GroupDetail, error) {
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
func Retention(ctx context.Context, pool *sqldb.DB, now time.Time) (int, error) {
	total := 0
	tag, err := pool.Exec(ctx, `DELETE FROM error_reports WHERE resolved=true AND last_seen < $1`, now.Add(-ResolvedMaxAge))
	if err != nil {
		return 0, err
	}
	total += int(tag.RowsAffected())
	gr, err := pool.Query(ctx, `SELECT report_id FROM error_occurrences GROUP BY report_id HAVING count(*) > $1`, maxOccurrence)
	if err != nil {
		return total, err
	}
	var over []string
	for gr.Next() {
		var id string
		if err := gr.Scan(&id); err != nil {
			gr.Close()
			return total, err
		}
		over = append(over, id)
	}
	gr.Close()
	for _, id := range over {
		if err = trimOccurrences(ctx, pool, id); err != nil {
			return total, err
		}
	}
	var groups int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM error_reports`).Scan(&groups); err != nil {
		return total, err
	}
	if groups > MaxGroups {
		xr, err := pool.Query(ctx, `SELECT id FROM error_reports ORDER BY resolved DESC, last_seen ASC LIMIT $1`, groups-MaxGroups)
		if err != nil {
			return total, err
		}
		var drop []string
		for xr.Next() {
			var id string
			if err := xr.Scan(&id); err != nil {
				xr.Close()
				return total, err
			}
			drop = append(drop, id)
		}
		xr.Close()
		for _, id := range drop {
			if _, err = pool.Exec(ctx, `DELETE FROM error_reports WHERE id=$1`, id); err != nil {
				return total, err
			}
			total++
		}
	}
	return total, nil
}
