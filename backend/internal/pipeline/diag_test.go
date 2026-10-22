package pipeline

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"oa-agent/backend/internal/diag"
	"oa-agent/backend/internal/harness"
	"oa-agent/backend/internal/imports"
	"oa-agent/backend/internal/store"
)

func diagFor(t *testing.T, st *store.Store) {
	t.Helper()
	r := diag.New(diag.Options{FlushEvery: 20 * time.Millisecond, RatePerSec: 100000, Logf: func(string, ...any) {}})
	r.Attach(st.Pool)
	old := diag.SetDefault(r)
	t.Cleanup(func() { diag.SetDefault(old); r.Close() })
}

type panicAnalyzer struct {
	fakeAnalyzer
	panicOn string
}

func (p *panicAnalyzer) AnalyzeFile(ctx context.Context, in harness.FileInput, t []harness.Template, emit func(harness.Event)) (harness.Result, error) {
	if in.SourceName == p.panicOn {
		var m map[string]int
		m["boom"] = 1 // nil map write: a real runtime panic
	}
	return p.fakeAnalyzer.AnalyzeFile(ctx, in, t, emit)
}

func TestAnalyzerPanicFailsOnlyThatFile(t *testing.T) {
	st := testStore(t)
	diagFor(t, st)
	an := &panicAnalyzer{panicOn: "炸.pdf", fakeAnalyzer: fakeAnalyzer{results: map[string]harness.Result{
		"好.pdf": {Department: sp("采购部"), DocumentType: sp("合同"), DocumentDate: sp("2026-03-15")},
	}}}
	svc, spool, _ := newSvc(t, st, an)
	ctx := context.Background()
	b := importFiles(t, st, spool, map[string]string{"好.pdf": "SECRET-DOCUMENT-TEXT-OK", "炸.pdf": "SECRET-DOCUMENT-TEXT-BOOM"})
	job, _ := st.ClaimJob(ctx)
	svc.process(ctx, job) // would crash the test binary if the panic escaped
	got, _ := st.GetJob(ctx, b.Job.ID)
	if got.ProcessedFiles != 2 || got.FailureCount != 1 || got.SuccessCount != 1 {
		t.Fatalf("%+v", got)
	}
	files, _ := st.JobFiles(ctx, b.Job.ID)
	for _, f := range files {
		if f.SourceName == "炸.pdf" && (f.Error == nil || !strings.Contains(*f.Error, "内部错误，错误编号 ")) {
			t.Fatalf("file error must carry the report number: %v", f.Error)
		}
		if f.SourceName == "好.pdf" && f.Error != nil {
			t.Fatalf("the other file must be unaffected: %v", *f.Error)
		}
	}
	diag.Flush(5 * time.Second)
	list, total, err := diag.List(ctx, st.Pool, diag.Filter{Severity: "panic"})
	if err != nil || total != 1 {
		t.Fatalf("%d %v", total, err)
	}
	d, _ := diag.Get(ctx, st.Pool, list[0].ID)
	if d.Source != "pipeline" || d.Details["task_id"] != b.Job.ID || d.Details["file_id"] == nil || d.Details["ext"] != ".pdf" || d.Stack == "" {
		t.Fatalf("%+v", d)
	}
	raw := d.Message + d.Stack + sprintAny(d.Details)
	if strings.Contains(raw, "SECRET-DOCUMENT-TEXT") {
		t.Fatal("document content must never enter a report")
	}
}

func sprintAny(m map[string]any) string {
	var b strings.Builder
	for k, v := range m {
		b.WriteString(k)
		b.WriteString(sprint(v))
	}
	return b.String()
}

func sprint(v any) string { return strings.TrimSpace(strings.Join([]string{asString(v)}, "")) }

func asString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	default:
		return ""
	}
}

func TestLoopSurvivesPanicInProcess(t *testing.T) {
	st := testStore(t)
	diagFor(t, st)
	svc, spool, _ := newSvc(t, st, &fakeAnalyzer{results: map[string]harness.Result{"ok.pdf": {Department: sp("财务部"), DocumentType: sp("合同"), DocumentDate: sp("2026-01-01")}}})
	svc.cfg.Poll = 20 * time.Millisecond
	var first atomic.Bool
	processHook = func(j imports.Job) {
		if first.CompareAndSwap(false, true) {
			panic("injected fault in process")
		}
	}
	defer func() { processHook = nil }()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	b1 := importFiles(t, st, spool, map[string]string{"ok.pdf": "A"})
	go func() { svc.Run(ctx); close(done) }()
	wait := func(id string, want ...string) imports.Job {
		for i := 0; i < 300; i++ {
			j, _ := st.GetJob(context.Background(), id)
			for _, w := range want {
				if j.Status == w {
					return j
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		j, _ := st.GetJob(context.Background(), id)
		t.Fatalf("job %s stuck in %q", id, j.Status)
		return j
	}
	j1 := wait(b1.Job.ID, "failed")
	_ = j1
	b2 := importFiles(t, st, spool, map[string]string{"ok.pdf": "B"})
	wait(b2.Job.ID, "awaiting_review")
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run must return on shutdown")
	}
	diag.Flush(5 * time.Second)
	_, total, _ := diag.List(context.Background(), st.Pool, diag.Filter{Severity: "panic"})
	if total != 1 {
		t.Fatalf("panic reports: %d", total)
	}
}

func TestAnalyzeFailureClassification(t *testing.T) {
	st := testStore(t)
	diagFor(t, st)
	svc, _, _ := newSvc(t, st, &fakeAnalyzer{})
	job := imports.Job{ID: "11111111-1111-4111-8111-111111111111", LLMSource: "global", LLMModel: "m"}
	f := imports.File{ID: "22222222-2222-4222-8222-222222222222", SourceName: "机密文件.docx", SizeBytes: 123}
	// user errors: not reported
	for _, code := range []string{harness.CodeEncrypted, harness.CodeUnreadableFile, harness.CodeUnsupportedFormat, harness.CodeFileTooLarge} {
		svc.reportAnalyzeFailure(job, f, &harness.WorkerError{Code: code})
	}
	svc.reportAnalyzeFailure(job, f, context.Canceled)
	// internal: reported with ids and metadata
	svc.reportAnalyzeFailure(job, f, &harness.WorkerError{Code: harness.CodeWorkerUnavailable, Status: 503, Message: "SECRET-BODY-TEXT"})
	svc.reportAnalyzeFailure(job, f, errors.New("provider chat failed: "+(&harness.ProviderError{Status: 429, Msg: "rate limited"}).Error()))
	svc.reportAnalyzeFailure(job, f, &harness.ProviderError{Status: 401, Msg: "invalid api key"})
	diag.Flush(5 * time.Second)
	list, total, err := diag.List(context.Background(), st.Pool, diag.Filter{})
	if err != nil || total != 3 {
		t.Fatalf("total=%d err=%v %+v", total, err, list)
	}
	srcs := map[string]string{}
	for _, g := range list {
		srcs[g.Code] = g.Source
		d, _ := diag.Get(context.Background(), st.Pool, g.ID)
		if d.Details["task_id"] != job.ID || d.Details["file_id"] != f.ID || d.Details["size_bytes"] != float64(123) || d.Details["ext"] != ".docx" {
			t.Fatalf("details %v", d.Details)
		}
		if strings.Contains(d.Message+d.Stack, "SECRET-BODY-TEXT") || strings.Contains(sprintAny(d.Details), "机密文件") {
			t.Fatalf("leak: %+v", d)
		}
	}
	if srcs["WORKER_WORKER_UNAVAILABLE"] != "worker" || srcs["PROVIDER_HTTP_401"] != "llm" || srcs["ANALYZE_FAILED"] != "pipeline" {
		t.Fatalf("%v", srcs)
	}
}
