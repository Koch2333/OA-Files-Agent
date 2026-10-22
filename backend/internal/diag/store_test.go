package diag

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"strings"
	"testing"
	"time"

	"oa-agent/backend/internal/dbtest"
)

func pgReporter(t *testing.T) (*Reporter, *dbPool) {
	t.Helper()
	st := dbtest.New(t)
	r := newTestReporter(t, Options{RatePerSec: 100000})
	r.Attach(st.Pool)
	return r, &dbPool{st.Pool}
}

type dbPool struct{ Pool *pgxpool.Pool }

func rep(t *testing.T, r *Reporter, e Entry) string {
	t.Helper()
	id := r.Report(context.Background(), e)
	if !r.Flush(5 * time.Second) {
		t.Fatal("flush timeout")
	}
	return id
}

func TestGroupingReopenOccurrencesAndSearch(t *testing.T) {
	r, p := pgReporter(t)
	ctx := context.Background()
	id := rep(t, r, Entry{Source: "api", Code: "INTERNAL", Message: "open /a/b/1.docx: denied task 1", RequestID: "req-1", User: "alice", Stack: "s1", Details: map[string]any{"route": "/v1/x", "password": "CANARY"}})
	id2 := rep(t, r, Entry{Source: "api", Code: "INTERNAL", Message: "open /c/d/2.docx: denied task 22", RequestID: "req-2", User: "bob"})
	if id != id2 {
		t.Fatal("same bug must group")
	}
	d, err := Get(ctx, p.Pool, id)
	if err != nil {
		t.Fatal(err)
	}
	if d.Count != 2 || d.Resolved || d.Stack != "s1" || len(d.Occurrences) != 2 || d.Occurrences[0].RequestID != "req-2" || d.RequestID != "req-2" || false {
		t.Fatalf("%+v", d)
	}
	if strings.Contains(sprint(d.Details), "CANARY") || d.Details["route"] != "/v1/x" {
		t.Fatalf("details %v", d.Details)
	}
	// resolve then recur → re-open
	d, err = SetResolved(ctx, p.Pool, id, true, "admin")
	if err != nil || !d.Resolved {
		t.Fatalf("%v %+v", err, d)
	}
	if n, _ := OpenCount(ctx, p.Pool); n != 0 {
		t.Fatalf("open=%d", n)
	}
	rep(t, r, Entry{Source: "api", Code: "INTERNAL", Message: "open /e/3.docx: denied task 3", RequestID: "req-3"})
	d, _ = Get(ctx, p.Pool, id)
	if d.Resolved || d.Count != 3 {
		t.Fatalf("recurrence must re-open: %+v", d)
	}
	// another group + filters + q
	rep(t, r, Entry{Source: "pipeline", Severity: "panic", Code: "PANIC", Message: "boom", RequestID: "zzz-9"})
	tr := true
	for _, c := range []struct {
		f    Filter
		want int
	}{
		{Filter{}, 2}, {Filter{Source: "pipeline"}, 1}, {Filter{Severity: "panic"}, 1}, {Filter{Resolved: &tr}, 0},
		{Filter{Q: "DENIED"}, 1}, {Filter{Q: "zzz"}, 1}, {Filter{Q: "req-1"}, 1}, {Filter{Q: ShortID(id)}, 1}, {Filter{Q: "nothing-like-this"}, 0}, {Filter{Q: "%"}, 0},
	} {
		items, total, err := List(ctx, p.Pool, c.f)
		if err != nil || total != c.want || len(items) != c.want {
			t.Errorf("%+v: got %d/%d err=%v want %d", c.f, len(items), total, err, c.want)
		}
	}
	items, total, _ := List(ctx, p.Pool, Filter{Page: 2, Size: 1})
	if total != 2 || len(items) != 1 {
		t.Fatalf("paging %d %d", total, len(items))
	}
	if _, err := Get(ctx, p.Pool, "00000000-0000-0000-0000-000000000000"); err != ErrNotFound {
		t.Fatal(err)
	}
	if _, err := Get(ctx, p.Pool, "not-a-uuid"); err != ErrNotFound {
		t.Fatal(err)
	}
	if err := Delete(ctx, p.Pool, id); err != nil {
		t.Fatal(err)
	}
	if err := Delete(ctx, p.Pool, id); err != ErrNotFound {
		t.Fatal(err)
	}
	var occ int
	p.Pool.QueryRow(ctx, `SELECT count(*) FROM error_occurrences WHERE report_id::text=$1`, id).Scan(&occ)
	if occ != 0 {
		t.Fatal("occurrences must cascade")
	}
}

func TestOccurrencesBounded(t *testing.T) {
	r, p := pgReporter(t)
	ctx := context.Background()
	var id string
	for round := 0; round < 3; round++ {
		for i := 0; i < 15; i++ {
			id = r.Report(ctx, Entry{Source: "api", Code: "X", Message: "same", RequestID: "r"})
		}
		r.Flush(5 * time.Second)
	}
	d, _ := Get(ctx, p.Pool, id)
	if d.Count != 45 || len(d.Occurrences) != 20 {
		t.Fatalf("count=%d occ=%d", d.Count, len(d.Occurrences))
	}
}

func TestRetention(t *testing.T) {
	r, p := pgReporter(t)
	ctx := context.Background()
	oldRes := rep(t, r, Entry{Source: "api", Code: "OLDRES", Message: "old resolved"})
	oldOpen := rep(t, r, Entry{Source: "api", Code: "OLDOPEN", Message: "old open"})
	fresh := rep(t, r, Entry{Source: "api", Code: "FRESH", Message: "fresh resolved"})
	SetResolved(ctx, p.Pool, oldRes, true, "a")
	SetResolved(ctx, p.Pool, fresh, true, "a")
	p.Pool.Exec(ctx, `UPDATE error_reports SET last_seen = now() - interval '100 days' WHERE id::text IN ($1,$2)`, oldRes, oldOpen)
	n, err := Retention(ctx, p.Pool, time.Now())
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if _, err := Get(ctx, p.Pool, oldRes); err != ErrNotFound {
		t.Fatal("old resolved must be deleted")
	}
	if _, err := Get(ctx, p.Pool, oldOpen); err != nil {
		t.Fatal("unresolved must stay however old")
	}
	if _, err := Get(ctx, p.Pool, fresh); err != nil {
		t.Fatal("recent resolved must stay")
	}
	// occurrence trimming
	for i := 0; i < 30; i++ {
		p.Pool.Exec(ctx, `INSERT INTO error_occurrences(report_id, at) VALUES ($1, now() - make_interval(mins => $2::int))`, fresh, i)
	}
	Retention(ctx, p.Pool, time.Now())
	d, _ := Get(ctx, p.Pool, fresh)
	var raw int
	p.Pool.QueryRow(ctx, `SELECT count(*) FROM error_occurrences WHERE report_id::text=$1`, fresh).Scan(&raw)
	if raw != 20 || len(d.Occurrences) != 20 {
		t.Fatalf("occurrences %d", raw)
	}
	// group cap: shrink by inserting rows directly (cap is 5000)
	p.Pool.Exec(ctx, `INSERT INTO error_reports(id, fingerprint, source, severity, message, first_seen, last_seen, resolved)
	  SELECT gen_random_uuid(), 'bulk' || g, 'api', 'error', 'bulk', now(), now() - make_interval(secs => g), g % 2 = 0 FROM generate_series(1, 5100) g`)
	n, err = Retention(ctx, p.Pool, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var total int
	p.Pool.QueryRow(ctx, `SELECT count(*) FROM error_reports`).Scan(&total)
	if total != MaxGroups || n < 100 {
		t.Fatalf("total=%d deleted=%d", total, n)
	}
	var openLeft int
	p.Pool.QueryRow(ctx, `SELECT count(*) FROM error_reports WHERE NOT resolved`).Scan(&openLeft)
	if openLeft < 2500 {
		t.Fatalf("resolved groups go first, open=%d", openLeft)
	}
}

func TestUncleanExitOpen(t *testing.T) {
	r, p := pgReporter(t)
	ctx := context.Background()
	if ok, _ := UncleanExitOpen(ctx, p.Pool); ok {
		t.Fatal("none yet")
	}
	old := SetDefault(r)
	defer SetDefault(old)
	id := ReportPrevRun(ctx, PrevRun{Unclean: true, Info: RunInfo{PID: 5}})
	r.Flush(5 * time.Second)
	if ok, _ := UncleanExitOpen(ctx, p.Pool); !ok {
		t.Fatal("must be open")
	}
	SetResolved(ctx, p.Pool, id, true, "admin")
	if ok, _ := UncleanExitOpen(ctx, p.Pool); ok {
		t.Fatal("resolved by admin")
	}
}

func TestPoisonRecordDoesNotBlockOthers(t *testing.T) {
	r, p := pgReporter(t)
	ctx := context.Background()
	rep(t, r, Entry{Source: "api", Code: "A", Message: "nul \x00 byte and bad \xff utf8"})
	rep(t, r, Entry{Source: "api", Code: "B", Message: "ok"})
	if _, total, _ := List(ctx, p.Pool, Filter{}); total != 2 {
		t.Fatalf("total %d", total)
	}
}
