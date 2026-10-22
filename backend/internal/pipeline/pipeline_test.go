package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"oa-agent/backend/internal/api"
	"oa-agent/backend/internal/db"
	"oa-agent/backend/internal/fileops"
	"oa-agent/backend/internal/fsman"
	"oa-agent/backend/internal/harness"
	"oa-agent/backend/internal/imports"
	"oa-agent/backend/internal/store"
)

func testStore(t *testing.T) *store.Store {
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
	schema := "oa_pipe_" + fmt.Sprintf("%d_%d", time.Now().UnixNano(), rand.Int63())
	q := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+q); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		admin.Exec(context.Background(), "DROP SCHEMA "+q+" CASCADE")
		admin.Close(context.Background())
	})
	_, _ = admin.Exec(ctx, "SET search_path TO "+q)
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

type fakeAnalyzer struct {
	results map[string]harness.Result
	errs    map[string]error
}

func (f *fakeAnalyzer) AnalyzeFile(_ context.Context, in harness.FileInput, _ []harness.Template, emit func(harness.Event)) (harness.Result, error) {
	emit(harness.Event{Phase: "analyze", Type: "started", Summary: "x"})
	if e := f.errs[in.SourceName]; e != nil {
		return harness.Result{}, e
	}
	return f.results[in.SourceName], nil
}

func sp(s string) *string { return &s }

// spool writes real files and returns a saved batch for them.
func importFiles(t *testing.T, st *store.Store, spool string, files map[string]string) imports.Batch {
	t.Helper()
	id, _ := imports.NewID()
	now := time.Now().UTC()
	b := imports.Batch{Job: imports.Job{ID: id, Status: "queued", TotalFiles: len(files), CreatedAt: now, UpdatedAt: now}, Files: []imports.File{}, Failures: []imports.Failure{}}
	for name, content := range files {
		fid, _ := imports.NewID()
		p := filepath.Join(spool, fid+".pdf")
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		h := sha256.Sum256([]byte(content))
		sum := hex.EncodeToString(h[:])
		b.Files = append(b.Files, imports.File{ID: fid, TaskID: id, SourceName: name, SourcePath: "in/" + name, MIMEType: "application/pdf", SHA256: sum, SizeBytes: int64(len(content)), Evidence: []string{}, ArchiveStatus: "pending", CreatedAt: now, StoredPath: p})
	}
	saved, _, err := st.Save(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

func newSvc(t *testing.T, st *store.Store, an Analyzer) (*Service, string, string) {
	t.Helper()
	spool, arch := t.TempDir(), t.TempDir()
	svc, err := New(Config{Store: st, Analyzer: an, ArchiveRoot: arch, SpoolRoot: spool, ProviderName: "test", Model: "m", ProviderOK: true})
	if err != nil {
		t.Fatal(err)
	}
	return svc, spool, arch
}

func TestJobLifecycleFromQueueToArchiveAndRollback(t *testing.T) {
	st := testStore(t)
	an := &fakeAnalyzer{
		results: map[string]harness.Result{
			"合同甲.pdf": {Department: sp("采购部"), DocumentType: sp("合同"), DocumentDate: sp("2026-03-15"), Evidence: []string{"文件名含“合同”→合同"}},
			"不明.pdf":  {ReviewRequired: true, Evidence: []string{}},
		},
		errs: map[string]error{"坏.pdf": &harness.WorkerError{Code: harness.CodeUnreadableFile}},
	}
	svc, spool, arch := newSvc(t, st, an)
	ctx := context.Background()
	b := importFiles(t, st, spool, map[string]string{"合同甲.pdf": "AAA", "不明.pdf": "BBB", "坏.pdf": "CCC"})
	job, err := st.ClaimJob(ctx)
	if err != nil {
		t.Fatal(err)
	}
	svc.process(ctx, job)

	job, _ = st.GetJob(ctx, b.Job.ID)
	if job.Status != "awaiting_review" || job.PlanID == nil || job.ProcessedFiles != 3 || job.FailureCount != 1 || job.SuccessCount != 2 {
		t.Fatalf("after processing: %+v", job)
	}
	files, _ := st.JobFiles(ctx, b.Job.ID)
	for _, f := range files {
		if f.SourceName == "坏.pdf" && (f.Error == nil || !strings.Contains(*f.Error, "损坏")) {
			t.Fatalf("worker error must become a readable file error, got %v", f.Error)
		}
	}
	plan, _ := st.GetPlan(ctx, *job.PlanID)
	if len(plan.Items) != 2 {
		t.Fatalf("failed files must not be planned: %d items", len(plan.Items))
	}
	var unknownID string
	for _, it := range plan.Items {
		if it.OriginalName == "不明.pdf" {
			unknownID = it.FileID
			if !it.ReviewRequired {
				t.Fatal("unidentified file must require review")
			}
		}
		if it.OriginalName == "合同甲.pdf" && it.DestinationPath != "采购部/2026/合同/2026-03-15_采购部_合同_合同甲.pdf" {
			t.Fatalf("destination = %s", it.DestinationPath)
		}
	}
	if _, err := svc.ExecutePlan(ctx, plan.ID, "k", "u"); !errors.Is(err, store.ErrState) {
		t.Fatalf("execute before approval must be refused, got %v", err)
	}
	if _, err := st.ApprovePlan(ctx, plan.ID, plan.Version, "u"); !errors.Is(err, store.ErrReviewRequired) {
		t.Fatalf("approve must be blocked until review items are resolved, got %v", err)
	}
	plan, err = st.PatchPlan(ctx, plan.ID, plan.Version, []store.PlanPatch{{FileID: unknownID, Department: sp("行政部"), DocumentType: sp("通知"), DocumentDate: sp("2026-04-01")}},
		func(it store.PlanItem) (string, string, []string, error) { return svc.Derive(ctx, it) })
	if err != nil {
		t.Fatal(err)
	}
	plan, err = st.ApprovePlan(ctx, plan.ID, plan.Version, "u")
	if err != nil {
		t.Fatal(err)
	}
	done, err := svc.ExecutePlan(ctx, plan.ID, "exec-1", "u")
	if err != nil || done.Status != "executed" || done.OperationID == nil {
		t.Fatalf("execute: %v %+v", err, done)
	}
	for _, it := range done.Items {
		got, err := os.ReadFile(filepath.Join(arch, filepath.FromSlash(it.DestinationPath)))
		if err != nil || len(got) != 3 {
			t.Fatalf("archived file missing at %s: %v", it.DestinationPath, err)
		}
	}
	if _, err := os.Stat(filepath.Join(spool, filepath.Base(files[0].StoredPath))); err != nil && files[0].Error == nil {
		t.Fatal("the imported original must be kept (copy, not move)")
	}
	again, err := svc.ExecutePlan(ctx, plan.ID, "exec-2", "u")
	if err != nil || again.Status != "executed" {
		t.Fatalf("re-execute must be a no-op: %v", err)
	}
	if _, total, _ := st.Ledger(ctx, store.Page{}, store.LedgerFilter{}); total != 2 {
		t.Fatalf("ledger rows = %d", total)
	}
	job, _ = st.GetJob(ctx, b.Job.ID)
	if job.Status != "partial_failure" {
		t.Fatalf("a task with a failed file ends partial_failure, got %s", job.Status)
	}

	if err := svc.Rollback(ctx, *done.OperationID, "u"); err != nil {
		t.Fatal(err)
	}
	for _, it := range done.Items {
		if _, err := os.Stat(filepath.Join(arch, filepath.FromSlash(it.DestinationPath))); !os.IsNotExist(err) {
			t.Fatalf("rollback must remove the published copy: %v", err)
		}
	}
	var ee *api.ExecError
	if err := svc.Rollback(ctx, *done.OperationID, "u"); !errors.As(err, &ee) || ee.Status != 409 {
		t.Fatalf("second rollback must be refused with 409, got %v", err)
	}
	if _, total, _ := st.Ledger(ctx, store.Page{}, store.LedgerFilter{}); total != 0 {
		t.Fatal("rolled-back entries must leave the ledger")
	}
}

func TestSecondImportOfSameFileFlagsDuplicateAndSuffixes(t *testing.T) {
	st := testStore(t)
	res := harness.Result{Department: sp("财务部"), DocumentType: sp("报表"), DocumentDate: sp("2026-01-31")}
	svc, spool, arch := newSvc(t, st, &fakeAnalyzer{results: map[string]harness.Result{"报表.pdf": res}})
	ctx := context.Background()
	run := func() store.Plan {
		b := importFiles(t, st, spool, map[string]string{"报表.pdf": "SAME"})
		j, _ := st.ClaimJob(ctx)
		svc.process(ctx, j)
		j, _ = st.GetJob(ctx, b.Job.ID)
		p, _ := st.GetPlan(ctx, *j.PlanID)
		p, _ = st.ApprovePlan(ctx, p.ID, p.Version, "u")
		if _, err := svc.ExecutePlan(ctx, p.ID, "k"+b.Job.ID, "u"); err != nil {
			t.Fatal(err)
		}
		return p
	}
	first := run()
	second := run()
	if first.Items[0].DestinationPath == second.Items[0].DestinationPath {
		t.Fatal("the second archive of the same name must not reuse the path")
	}
	entries, _ := os.ReadDir(filepath.Join(arch, "财务部", "2026", "报表"))
	if len(entries) != 2 {
		t.Fatalf("both copies must exist side by side (never overwrite), got %d", len(entries))
	}
	warned := false
	for _, w := range second.Items[0].Warnings {
		if strings.Contains(w, "内容相同") || strings.Contains(w, "已存在") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("second plan must warn about the existing/identical file: %v", second.Items[0].Warnings)
	}
}

func TestCancelRequestedStopsJob(t *testing.T) {
	st := testStore(t)
	svc, spool, _ := newSvc(t, st, &fakeAnalyzer{results: map[string]harness.Result{}})
	ctx := context.Background()
	b := importFiles(t, st, spool, map[string]string{"a.pdf": "A"})
	j, _ := st.ClaimJob(ctx)
	if _, err := st.CancelJob(ctx, b.Job.ID); err != nil {
		t.Fatal(err)
	}
	svc.process(ctx, j)
	got, _ := st.GetJob(ctx, b.Job.ID)
	if got.Status != "cancelled" || got.PlanID != nil {
		t.Fatalf("cancelled job must not produce a plan: %+v", got)
	}
}

func TestMissingSpoolFileBecomesFileError(t *testing.T) {
	st := testStore(t)
	svc, spool, _ := newSvc(t, st, &fakeAnalyzer{results: map[string]harness.Result{"a.pdf": {Department: sp("财务部"), DocumentType: sp("合同"), DocumentDate: sp("2026-01-01")}}})
	ctx := context.Background()
	b := importFiles(t, st, spool, map[string]string{"a.pdf": "A"})
	files, _ := st.JobFiles(ctx, b.Job.ID)
	os.Remove(files[0].StoredPath)
	j, _ := st.ClaimJob(ctx)
	svc.process(ctx, j)
	got, _ := st.GetJob(ctx, b.Job.ID)
	if got.Status != "failed" || got.FailureCount != 1 {
		t.Fatalf("lost spool file must fail the file and the task: %+v", got)
	}
}

func TestProposeCorrectionParsesOnlyKnownFields(t *testing.T) {
	st := testStore(t)
	svc, spool, _ := newSvc(t, st, &fakeAnalyzer{})
	ctx := context.Background()
	b := importFiles(t, st, spool, map[string]string{"a.pdf": "A"})
	fid := b.Files[0].ID
	c, err := svc.ProposeCorrection(ctx, fid, "应属人事部，类型是通知，日期 2026年5月6日")
	if err != nil || len(c.Changes) != 3 || c.Impact.Scope != "single_file" || c.Impact.RuleChanges {
		t.Fatalf("proposal: %v %+v", err, c)
	}
	var ue *api.UnderstandError
	if _, err := svc.ProposeCorrection(ctx, fid, "随便改改"); !errors.As(err, &ue) {
		t.Fatalf("unintelligible instruction must be rejected, got %v", err)
	}
	if _, err := svc.ProposeCorrection(ctx, fid, "日期改成 2026-02-30"); !errors.As(err, &ue) {
		t.Fatalf("impossible date must not be accepted, got %v", err)
	}
	f, _ := st.GetFile(ctx, fid)
	if f.Department != nil {
		t.Fatal("proposing must not change the file")
	}
}

func TestValidateRule(t *testing.T) {
	svc := &Service{}
	good := store.Rule{NamingPattern: "{date}_{department}_{original}", DirectoryPattern: "{department}/{year}/{type}"}
	if err := svc.ValidateRule(good); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []store.Rule{
		{NamingPattern: "{bogus}", DirectoryPattern: "{department}"},
		{NamingPattern: "{original}", DirectoryPattern: "../{department}"},
		{NamingPattern: "{original}", DirectoryPattern: "/abs/{department}"},
		{NamingPattern: "{original}", DirectoryPattern: `C:\x`},
		{NamingPattern: "", DirectoryPattern: "{department}"},
		{NamingPattern: "{original", DirectoryPattern: "{department}"},
	} {
		if err := svc.ValidateRule(bad); err == nil {
			t.Errorf("rule %+v should be rejected", bad)
		}
	}
}

func TestUserMessageNeverLeaksInternals(t *testing.T) {
	for err, want := range map[error]string{
		&harness.WorkerError{Code: harness.CodeEncrypted, Message: "secret detail"}: "加密",
		&harness.WorkerError{Code: harness.CodeUnreadableFile}:                      "损坏",
		&harness.WorkerError{Code: harness.CodeWorkerUnavailable}:                   "不可用",
		context.DeadlineExceeded: "超时",
		errors.New("pq: password authentication failed for user \"oa\" at 10.0.0.5"): "识别失败",
	} {
		got := userMessage(err)
		if !strings.Contains(got, want) || strings.Contains(got, "secret") || strings.Contains(got, "10.0.0.5") {
			t.Errorf("userMessage(%v) = %q", err, got)
		}
	}
}

type countingAnalyzer struct {
	fakeAnalyzer
	calls map[string]int
}

func (c *countingAnalyzer) AnalyzeFile(ctx context.Context, in harness.FileInput, t []harness.Template, emit func(harness.Event)) (harness.Result, error) {
	c.calls[in.SourceName]++
	return c.fakeAnalyzer.AnalyzeFile(ctx, in, t, emit)
}

// A crash mid-job leaves some files analysed; after RecoverJobs the job must continue without redoing them.
func TestRestartResumesWithoutReanalysingFinishedFiles(t *testing.T) {
	st := testStore(t)
	ok := harness.Result{Department: sp("财务部"), DocumentType: sp("合同"), DocumentDate: sp("2026-01-01")}
	an := &countingAnalyzer{fakeAnalyzer: fakeAnalyzer{results: map[string]harness.Result{"a.pdf": ok, "b.pdf": ok}}, calls: map[string]int{}}
	svc, spool, _ := newSvc(t, st, an)
	ctx := context.Background()
	b := importFiles(t, st, spool, map[string]string{"a.pdf": "A", "b.pdf": "B"})
	if _, err := st.ClaimJob(ctx); err != nil { // job is now "scanning" — as if the process died right here
		t.Fatal(err)
	}
	var done imports.File
	for _, f := range b.Files {
		if f.SourceName == "a.pdf" {
			done = f
		}
	}
	if err := st.SaveAnalysis(ctx, done.ID, store.AnalysisResult{Department: ok.Department, DocumentType: ok.DocumentType, DocumentDate: ok.DocumentDate, Evidence: []string{"e"}}); err != nil {
		t.Fatal(err)
	}
	if n, err := st.RecoverJobs(ctx); err != nil || n != 1 {
		t.Fatalf("recover: %d %v", n, err)
	}
	j, err := st.ClaimJob(ctx)
	if err != nil {
		t.Fatal(err)
	}
	svc.process(ctx, j)
	if an.calls["a.pdf"] != 0 || an.calls["b.pdf"] != 1 {
		t.Fatalf("only the unfinished file may be analysed again: %v", an.calls)
	}
	got, _ := st.GetJob(ctx, b.Job.ID)
	if got.Status != "awaiting_review" || got.ProcessedFiles != 2 || got.SuccessCount != 2 {
		t.Fatalf("after resume: %+v", got)
	}
	plan, _ := st.GetPlan(ctx, *got.PlanID)
	if len(plan.Items) != 2 {
		t.Fatalf("plan must cover both files, got %d", len(plan.Items))
	}
}

// Once a file was renamed/moved/deleted in the file manager, rolling back the original archive operation
// must be refused instead of silently skipping it and flagging the ledger wrongly.
func TestRollbackIsRefusedAfterFileManagerChanges(t *testing.T) {
	st := testStore(t)
	ok := harness.Result{Department: sp("财务部"), DocumentType: sp("合同"), DocumentDate: sp("2026-01-01")}
	svc, spool, arch := newSvc(t, st, &fakeAnalyzer{results: map[string]harness.Result{"a.pdf": ok, "b.pdf": ok}})
	ctx := context.Background()
	b := importFiles(t, st, spool, map[string]string{"a.pdf": "A", "b.pdf": "B"})
	j, _ := st.ClaimJob(ctx)
	svc.process(ctx, j)
	j, _ = st.GetJob(ctx, b.Job.ID)
	p, _ := st.GetPlan(ctx, *j.PlanID)
	p, _ = st.ApprovePlan(ctx, p.ID, p.Version, "u")
	done, err := svc.ExecutePlan(ctx, p.ID, "k", "u")
	if err != nil || done.OperationID == nil {
		t.Fatalf("execute: %v", err)
	}
	fm := &fileops.Service{Store: st, FS: &fsman.Manager{Root: arch}}
	first := done.Items[0].DestinationPath
	if _, _, err := fm.Move(ctx, first, strings.TrimSuffix(first, ".pdf")+"_改名.pdf", "u"); err != nil {
		t.Fatal(err)
	}
	var ee *api.ExecError
	if err := svc.Rollback(ctx, *done.OperationID, "u"); !errors.As(err, &ee) || ee.Code != "FILES_CHANGED" {
		t.Fatalf("rollback after a manual move must be refused, got %v", err)
	}
	for _, it := range done.Items[1:] {
		if _, err := os.Stat(filepath.Join(arch, filepath.FromSlash(it.DestinationPath))); err != nil {
			t.Fatal("a refused rollback must not delete anything")
		}
	}
	if _, total, _ := st.Ledger(ctx, store.Page{}, store.LedgerFilter{}); total != 2 {
		t.Fatalf("ledger must be untouched, rows=%d", total)
	}
}
