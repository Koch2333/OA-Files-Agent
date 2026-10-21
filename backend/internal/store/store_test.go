package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"oa-agent/backend/internal/db"
	"oa-agent/backend/internal/imports"
)

// testStore returns a Store on an isolated schema of OA_TEST_DATABASE_URL (skipped when unset).
func testStore(t *testing.T) *Store {
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
	schema := "oa_store_" + time.Now().Format("150405_000000000")
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
	return &Store{Pool: pool}
}

func newBatch(t *testing.T, key, fp string, names ...string) imports.Batch {
	t.Helper()
	id, _ := imports.NewID()
	now := time.Now().UTC()
	b := imports.Batch{Key: key, Fingerprint: fp, Job: imports.Job{ID: id, Status: "queued", TotalFiles: len(names), CreatedAt: now, UpdatedAt: now}, Files: []imports.File{}, Failures: []imports.Failure{}}
	for _, n := range names {
		fid, _ := imports.NewID()
		b.Files = append(b.Files, imports.File{ID: fid, TaskID: id, SourceName: n, SourcePath: "d/" + n, MIMEType: "application/pdf", SHA256: fid[:8] + "00000000000000000000000000000000000000000000000000000000", SizeBytes: 10, Evidence: []string{}, ArchiveStatus: "pending", CreatedAt: now, StoredPath: "/spool/" + n})
	}
	return b
}

func str(s string) *string { return &s }

func TestSaveIsIdempotentPerKeyAndRejectsDifferentContent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	first, replay, err := s.Save(ctx, newBatch(t, "k1", "fp-a", "a.pdf"))
	if err != nil || replay {
		t.Fatalf("first save: replay=%v err=%v", replay, err)
	}
	again, replay, err := s.Save(ctx, newBatch(t, "k1", "fp-a", "a.pdf"))
	if err != nil || !replay || again.Job.ID != first.Job.ID {
		t.Fatalf("replay must return the original task: replay=%v err=%v", replay, err)
	}
	if _, _, err = s.Save(ctx, newBatch(t, "k1", "fp-b", "b.pdf")); !errors.Is(err, ErrConflict) {
		t.Fatalf("different content under the same key must conflict, got %v", err)
	}
}

func TestSaveRecordsIntakeFailuresAsFileRows(t *testing.T) {
	s := testStore(t)
	b := newBatch(t, "", "", "ok.pdf")
	b.Job.TotalFiles = 2
	b.Failures = []imports.Failure{{Index: 1, SourcePath: "d/old.doc", Code: "UNSUPPORTED_FORMAT", Message: "请先转换", MIMEType: "application/octet-stream"}}
	saved, _, err := s.Save(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Job.ProcessedFiles != 1 || saved.Job.FailureCount != 1 {
		t.Fatalf("progress counters wrong: %+v", saved.Job)
	}
	var failed *imports.File
	for i := range saved.Files {
		if saved.Files[i].Error != nil {
			failed = &saved.Files[i]
		}
	}
	if failed == nil || failed.ArchiveStatus != "failed" || len(failed.SHA256) != 64 {
		t.Fatalf("failure row missing or malformed: %+v", failed)
	}
}

func seedPlan(t *testing.T, s *Store, review bool) (imports.Batch, Plan) {
	t.Helper()
	ctx := context.Background()
	b, _, err := s.Save(ctx, newBatch(t, "", "", "a.pdf", "b.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	var items []PlanItem
	for _, f := range b.Files {
		items = append(items, PlanItem{FileID: f.ID, OriginalName: f.SourceName, SuggestedName: "n_" + f.SourceName, SourcePath: f.SourcePath, DestinationPath: "财务部/2026/" + f.SourceName, Department: str("财务部"), DocumentType: str("合同"), DocumentDate: str("2026-03-15"), Warnings: []string{}, ReviewRequired: review && f.SourceName == "a.pdf"})
	}
	p, err := s.CreatePlan(ctx, b.Job.ID, nil, items)
	if err != nil {
		t.Fatal(err)
	}
	return b, p
}

func TestPlanStateMachine(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, p := seedPlan(t, s, true)

	if _, err := s.ApprovePlan(ctx, p.ID, p.Version, "u"); !errors.Is(err, ErrReviewRequired) {
		t.Fatalf("approve with review-required items must fail, got %v", err)
	}
	if _, _, _, err := s.BeginExecution(ctx, p.ID, "k"); !errors.Is(err, ErrState) {
		t.Fatalf("execute before approval must fail, got %v", err)
	}
	derive := func(it PlanItem) (string, string, []string, error) {
		return "x_" + it.OriginalName, "新/" + it.OriginalName, nil, nil
	}
	if _, err := s.PatchPlan(ctx, p.ID, p.Version+5, []PlanPatch{{FileID: p.Items[0].FileID, Department: str("人事部")}}, derive); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale version must conflict, got %v", err)
	}
	patched, err := s.PatchPlan(ctx, p.ID, p.Version, []PlanPatch{{FileID: p.Items[0].FileID, Department: str("人事部")}}, derive)
	if err != nil {
		t.Fatal(err)
	}
	if patched.Version != p.Version+1 || patched.Items[0].ReviewRequired || *patched.Items[0].Department != "人事部" || patched.Items[0].SuggestedName != "x_a.pdf" {
		t.Fatalf("patch result wrong: %+v", patched.Items[0])
	}
	if cleared, err := s.PatchPlan(ctx, p.ID, patched.Version, []PlanPatch{{FileID: p.Items[1].FileID, DocumentDate: str("")}}, derive); err != nil || !cleared.Items[1].ReviewRequired || cleared.Items[1].DocumentDate != nil {
		t.Fatalf("clearing a field must null it and require review: %v %+v", err, cleared.Items[1])
	}
	cur, _ := s.GetPlan(ctx, p.ID)
	if _, err := s.PatchPlan(ctx, p.ID, cur.Version, []PlanPatch{{FileID: p.Items[1].FileID, DocumentDate: str("2026-03-15")}}, derive); err != nil {
		t.Fatal(err)
	}
	cur, _ = s.GetPlan(ctx, p.ID)
	approved, err := s.ApprovePlan(ctx, p.ID, cur.Version, "tester")
	if err != nil || approved.Status != "approved" || approved.ApprovedBy == nil || *approved.ApprovedBy != "tester" {
		t.Fatalf("approve failed: %v %+v", err, approved)
	}
	if _, err := s.PatchPlan(ctx, p.ID, approved.Version, []PlanPatch{{FileID: p.Items[0].FileID, Department: str("x")}}, derive); !errors.Is(err, ErrState) {
		t.Fatalf("approved plans are locked, got %v", err)
	}
}

func TestExecutionIsIdempotentAndWritesLedgerOnce(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	b, p := seedPlan(t, s, false)
	p, err := s.ApprovePlan(ctx, p.ID, p.Version, "u")
	if err != nil {
		t.Fatal(err)
	}
	op, plan, replay, err := s.BeginExecution(ctx, p.ID, "key-1")
	if err != nil || replay || op.Status != "running" {
		t.Fatalf("begin: %v replay=%v op=%+v", err, replay, op)
	}
	if _, _, _, err := s.BeginExecution(ctx, p.ID, "key-2"); !errors.Is(err, ErrConflict) {
		t.Fatalf("a second key while executing must conflict, got %v", err)
	}
	if op2, _, _, err := s.BeginExecution(ctx, p.ID, "key-1"); err != nil || op2.ID != op.ID {
		t.Fatalf("same key must resume the same operation: %v", err)
	}
	var results []OpResult
	for _, it := range plan.Items {
		results = append(results, OpResult{FileID: it.FileID, FinalRel: it.DestinationPath, Status: "published", SHA256: "ab"})
	}
	done, err := s.FinishExecution(ctx, op, plan, results)
	if err != nil || done.Status != "executed" || done.OperationID == nil {
		t.Fatalf("finish: %v %+v", err, done)
	}
	if _, err := s.FinishExecution(ctx, op, plan, results); err != nil { // ledger insert is ON CONFLICT DO NOTHING
		t.Fatal(err)
	}
	rows, total, err := s.Ledger(ctx, Page{}, LedgerFilter{})
	if err != nil || total != 2 || len(rows) != 2 {
		t.Fatalf("ledger must hold exactly one row per file: total=%d err=%v", total, err)
	}
	if _, _, replay, err := s.BeginExecution(ctx, p.ID, "another"); err != nil || !replay {
		t.Fatalf("executed plan must replay: %v %v", replay, err)
	}
	job, _ := s.GetJob(ctx, b.Job.ID)
	if job.Status != "completed" {
		t.Fatalf("task status = %s", job.Status)
	}
	if err := s.MarkRolledBack(ctx, op); err != nil {
		t.Fatal(err)
	}
	if _, total, _ = s.Ledger(ctx, Page{}, LedgerFilter{}); total != 0 {
		t.Fatalf("rolled-back entries must leave the ledger, total=%d", total)
	}
}

func TestLedgerSearchTreatsWildcardsLiterally(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, p := seedPlan(t, s, false)
	p, _ = s.ApprovePlan(ctx, p.ID, p.Version, "u")
	op, plan, _, _ := s.BeginExecution(ctx, p.ID, "k")
	var res []OpResult
	for _, it := range plan.Items {
		res = append(res, OpResult{FileID: it.FileID, FinalRel: it.DestinationPath, Status: "published", SHA256: "ab"})
	}
	if _, err := s.FinishExecution(ctx, op, plan, res); err != nil {
		t.Fatal(err)
	}
	for q, want := range map[string]int{"a.pdf": 1, "A.PDF": 1, "财务": 2, "%": 0, "_": 0, "'; DROP TABLE ledger_entries; --": 0} {
		if _, total, err := s.Ledger(ctx, Page{}, LedgerFilter{Q: q}); err != nil || total != want {
			t.Errorf("q=%q total=%d want %d err=%v", q, total, want, err)
		}
	}
	if _, _, err := s.Ledger(ctx, Page{}, LedgerFilter{Sort: "executed_at; DROP TABLE x", Order: "asc"}); err != nil {
		t.Errorf("unknown sort column must fall back safely: %v", err)
	}
}

func TestClaimJobAndRecovery(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	b, _, _ := s.Save(ctx, newBatch(t, "", "", "a.pdf"))
	j, err := s.ClaimJob(ctx)
	if err != nil || j.ID != b.Job.ID || j.Status != "scanning" {
		t.Fatalf("claim: %v %+v", err, j)
	}
	if _, err := s.ClaimJob(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a claimed job must not be handed out twice, got %v", err)
	}
	n, err := s.RecoverJobs(ctx)
	if err != nil || n != 1 {
		t.Fatalf("recover: %d %v", n, err)
	}
	if j2, err := s.ClaimJob(ctx); err != nil || j2.ID != b.Job.ID {
		t.Fatalf("recovered job must be claimable again: %v", err)
	}
}

func TestEventsAreMonotonicPerTask(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a, _, _ := s.Save(ctx, newBatch(t, "", "", "a.pdf"))
	c, _, _ := s.Save(ctx, newBatch(t, "", "", "c.pdf"))
	for i := 0; i < 3; i++ {
		if err := s.AppendEvent(ctx, a.Job.ID, "p", "t", nil, "e", map[string]any{"i": i}); err != nil {
			t.Fatal(err)
		}
	}
	_ = s.AppendEvent(ctx, c.Job.ID, "p", "t", nil, "other", nil)
	evs, err := s.Events(ctx, a.Job.ID, 0, 10)
	if err != nil || len(evs) != 3 || evs[0].ID != 1 || evs[2].ID != 3 {
		t.Fatalf("events: %v %+v", err, evs)
	}
	if evs, _ = s.Events(ctx, a.Job.ID, 2, 10); len(evs) != 1 || evs[0].ID != 3 {
		t.Fatalf("resume after id 2 must return only id 3: %+v", evs)
	}
}

func TestCancelAndRetryRules(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	b, _, _ := s.Save(ctx, newBatch(t, "", "", "a.pdf"))
	if _, err := s.RetryJob(ctx, b.Job.ID); !errors.Is(err, ErrState) {
		t.Fatalf("retry of a queued job must be refused, got %v", err)
	}
	j, err := s.CancelJob(ctx, b.Job.ID)
	if err != nil || j.Status != "cancelled" {
		t.Fatalf("cancel queued: %v %+v", err, j)
	}
	if _, err := s.CancelJob(ctx, b.Job.ID); !errors.Is(err, ErrState) {
		t.Fatalf("cancelling twice must be refused, got %v", err)
	}
	j, err = s.RetryJob(ctx, b.Job.ID)
	if err != nil || j.Status != "queued" {
		t.Fatalf("retry cancelled: %v %+v", err, j)
	}
}

func TestMalformedIDsAreNotFoundNotErrors(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.GetJob(ctx, "not-a-uuid"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := s.GetPlan(ctx, "'; --"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := s.Template(ctx, ""); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}
