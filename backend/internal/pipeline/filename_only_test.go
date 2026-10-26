package pipeline

import (
	"context"
	"strings"
	"testing"

	"oa-agent/backend/internal/harness"
	"oa-agent/backend/internal/llm"
	"oa-agent/backend/internal/store"
)

// scriptedExtractor is the worker double: per file name it fails with a worker code or returns a result.
type scriptedExtractor struct {
	errs    map[string]string
	results map[string]harness.ExtractResult
	calls   map[string]int
}

func (s *scriptedExtractor) Extract(_ context.Context, req harness.ExtractRequest) (harness.ExtractResult, error) {
	if s.calls == nil {
		s.calls = map[string]int{}
	}
	s.calls[req.Filename]++
	if code := s.errs[req.Filename]; code != "" {
		return harness.ExtractResult{}, &harness.WorkerError{Code: code, Status: 422, Message: "x"}
	}
	r := s.results[req.Filename]
	if r.Pages == nil {
		r.Pages = []harness.Page{}
	}
	return r, nil
}

func realAgentSvc(t *testing.T, st *store.Store, ext harness.Extractor, models ModelResolver) (*Service, string) {
	t.Helper()
	agent := harness.NewAgent(harness.Heuristic{}, ext, harness.Options{})
	svc, spool, _ := newAISvc(t, st, agent, models)
	return svc, spool
}

func planItems(t *testing.T, st *store.Store, taskID string) map[string]store.PlanItem {
	t.Helper()
	job, _ := st.GetJob(context.Background(), taskID)
	if job.PlanID == nil {
		t.Fatalf("no plan: %+v", job)
	}
	plan, _ := st.GetPlan(context.Background(), *job.PlanID)
	out := map[string]store.PlanItem{}
	for _, it := range plan.Items {
		out[it.OriginalName] = it
	}
	return out
}

func TestUnreadableFilesAreClassifiedFromTheNameOnlyAndNeverFail(t *testing.T) {
	st := testStore(t)
	ext := &scriptedExtractor{
		errs: map[string]string{
			"采购部_设备采购合同_2026-03-15.pdf": harness.CodeEncrypted,
			"未知.dat":  harness.CodeUnsupportedFormat,
			"报表.xlsx": harness.CodeUnreadableFile,
			"巨大.pdf":  harness.CodeFileTooLarge,
			"超时.pdf":  harness.CodeExtractTimeout,
		},
		results: map[string]harness.ExtractResult{
			// OCR ran and found nothing
			"空白扫描.png": {OCRUsed: true, ContentStatus: "unreadable", ContentReason: "ocr_empty"},
		},
	}
	svc, spool := realAgentSvc(t, st, ext, nil)
	names := map[string]string{"采购部_设备采购合同_2026-03-15.pdf": "AAA", "未知.dat": "BBB", "报表.xlsx": "CCC", "巨大.pdf": "DDD", "超时.pdf": "EEE", "空白扫描.png": "FFF", "empty.bin": ""}
	b := importFiles(t, st, spool, names)
	runJob(t, st, svc)

	ctx := context.Background()
	job, _ := st.GetJob(ctx, b.Job.ID)
	if job.Status != "awaiting_review" || job.FailureCount != 0 || job.SuccessCount != 7 {
		t.Fatalf("no unreadable file may become a failed item: %+v", job)
	}
	if ext.calls["empty.bin"] != 0 {
		t.Fatal("an empty file must not even be sent to the worker")
	}
	files, _ := st.JobFiles(ctx, b.Job.ID)
	reasons := map[string]string{}
	for _, f := range files {
		if f.Error != nil || f.Content == nil || f.Content.Status != "unreadable" || f.Content.Basis != "filename_only" || f.Content.Reason == nil || !f.ReviewRequired {
			t.Fatalf("%s: %+v err=%v", f.SourceName, f.Content, f.Error)
		}
		reasons[f.SourceName] = *f.Content.Reason
		if len(f.Evidence) == 0 || !strings.HasPrefix(f.Evidence[0], "内容无法读取（") || !strings.Contains(f.Evidence[0], "仅依据文件名判断") {
			t.Fatalf("%s: explanation missing: %v", f.SourceName, f.Evidence)
		}
	}
	want := map[string]string{"采购部_设备采购合同_2026-03-15.pdf": "encrypted", "未知.dat": "unsupported", "报表.xlsx": "damaged", "巨大.pdf": "too_large", "超时.pdf": "timeout", "空白扫描.png": "ocr_empty", "empty.bin": "empty"}
	for n, r := range want {
		if reasons[n] != r {
			t.Errorf("%s: reason %q want %q", n, reasons[n], r)
		}
	}
	items := planItems(t, st, b.Job.ID)
	if len(items) != 7 {
		t.Fatalf("all seven must be planned: %d", len(items))
	}
	for n, it := range items {
		if it.Basis != "filename_only" || it.BasisNote == nil || !it.ReviewRequired {
			t.Errorf("%s: plan item must be flagged: %+v", n, it)
		}
	}
	named := items["采购部_设备采购合同_2026-03-15.pdf"]
	// what the NAME says is used: department, type and the date written in the name
	if named.Department == nil || *named.Department != "采购部" || named.DocumentType == nil || *named.DocumentType != "合同" || named.DocumentDate == nil || *named.DocumentDate != "2026-03-15" || len(named.FallbackFields) != 0 {
		t.Fatalf("filename-only classification: %+v", named)
	}
	if !named.ReviewRequired {
		t.Fatal("filename-only items are never approved automatically")
	}
	// nothing in the name -> placeholders from the plan layer, never a fabricated date (the file mtime is not a document date)
	hash := items["未知.dat"]
	if hash.DocumentDate != nil || hash.Department == nil || *hash.Department != "其他" || len(hash.FallbackFields) != 2 {
		t.Fatalf("name without information: %+v", hash)
	}
	// approval is blocked until a person resolves every one of them
	plan, _ := st.GetPlan(ctx, *job.PlanID)
	if _, err := st.ApprovePlan(ctx, plan.ID, plan.Version, "u"); err == nil {
		t.Fatal("a plan of filename-only items must not be approvable as is")
	}
}

// extractThenGiveUp is a model that tries extract_text once and then answers with nothing determined.
type extractThenGiveUp struct{ turn int }

func (p *extractThenGiveUp) Chat(context.Context, []harness.Message, []harness.ToolSpec) (harness.Response, error) {
	p.turn++
	if p.turn == 1 {
		return harness.Response{ToolCalls: []harness.ToolCall{{ID: "c1", Name: "extract_text", Arguments: []byte("{}")}}}, nil
	}
	return harness.Response{Content: `{"department":null,"document_type":null,"document_date":null,"evidence":[],"review_required":true}`}, nil
}

type scriptedModels struct{}

func (scriptedModels) TaskProvider(context.Context, string, string) (llm.TaskModel, error) {
	return llm.TaskModel{Provider: &extractThenGiveUp{}, Model: "g-model"}, nil
}

func TestFilenameOnlyItemsAreNeverArchivedByAutomaticMode(t *testing.T) {
	st := testStore(t)
	ext := &scriptedExtractor{
		errs: map[string]string{"采购部_合同_2026-03-15.pdf": harness.CodeEncrypted},
		results: map[string]harness.ExtractResult{
			"读得出.pdf": {Text: "采购部 合同 2026年3月15日", Pages: []harness.Page{{Page: 1, Text: "采购部", Source: "native"}}, ContentStatus: "ok"},
		},
	}
	svc, spool := realAgentSvc(t, st, ext, scriptedModels{})
	b := importAI(t, st, spool, map[string]string{"采购部_合同_2026-03-15.pdf": "AAA"}, autoJob("g-model"))
	runJob(t, st, svc)
	ctx := context.Background()
	job, _ := st.GetJob(ctx, b.Job.ID)
	if job.Status != "awaiting_review" || job.PlanID == nil {
		t.Fatalf("full-automatic mode must leave a filename-only item for a human: %+v", job)
	}
	if _, total, _ := st.Ledger(ctx, store.Page{}, store.LedgerFilter{}); total != 0 {
		t.Fatalf("nothing may be archived automatically: ledger rows = %d", total)
	}
	it := planItems(t, st, b.Job.ID)["采购部_合同_2026-03-15.pdf"]
	if !it.ReviewRequired || it.Basis != "filename_only" || it.Department == nil || *it.Department != "采购部" {
		t.Fatalf("%+v", it)
	}
}

func TestFilenameOnlyFallbackWithoutAHarnessAgent(t *testing.T) {
	// an Analyzer that does not implement the filename analysis still yields a flagged, review-required item
	st := testStore(t)
	an := &fakeAnalyzer{errs: map[string]error{"加密.pdf": &harness.WorkerError{Code: harness.CodeEncrypted}}}
	svc, spool, _ := newSvc(t, st, an)
	b := importFiles(t, st, spool, map[string]string{"加密.pdf": "AAA"})
	runJob(t, st, svc)
	files, _ := st.JobFiles(context.Background(), b.Job.ID)
	f := files[0]
	if f.Error != nil || f.Content == nil || *f.Content.Reason != "encrypted" || !f.ReviewRequired || f.Department != nil {
		t.Fatalf("%+v %+v", f, f.Content)
	}
}

func TestReadableFilesReportContentAndFormatMismatch(t *testing.T) {
	st := testStore(t)
	ext := &scriptedExtractor{results: map[string]harness.ExtractResult{
		"网页导出.xls": {Text: "采购部 设备采购合同", Pages: []harness.Page{{Page: 1, Text: "采购部 设备采购合同", Source: "native"}}, ContentStatus: "ok", DetectedFormat: "html", ExtensionMismatch: true},
		"很长.csv":   {Text: "a,b", Pages: []harness.Page{{Page: 1, Text: "a,b", Source: "native"}}, ContentStatus: "partial", ContentReason: "capped"},
	}}
	svc, spool := realAgentSvc(t, st, ext, nil)
	b := importFiles(t, st, spool, map[string]string{"网页导出.xls": "AAA", "很长.csv": "BBB"})
	runJob(t, st, svc)
	files, _ := st.JobFiles(context.Background(), b.Job.ID)
	for _, f := range files {
		if f.Content == nil || f.Content.Basis != "content" {
			t.Fatalf("%s: %+v", f.SourceName, f.Content)
		}
		switch f.SourceName {
		case "网页导出.xls":
			if f.Content.Status != "ok" || f.Content.FormatMismatch == nil || *f.Content.FormatMismatch != "html" {
				t.Fatalf("mismatch must be reported: %+v", f.Content)
			}
		case "很长.csv":
			if f.Content.Status != "partial" || f.Content.Reason == nil || *f.Content.Reason != "capped" {
				t.Fatalf("partial must be reported: %+v", f.Content)
			}
		}
	}
	for n, it := range planItems(t, st, b.Job.ID) {
		if it.Basis != "content" {
			t.Errorf("%s: %+v", n, it)
		}
	}
}
