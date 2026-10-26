package harness

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type stubProvider struct {
	reply string
	err   error
	got   []Message
	tools int
}

func (s *stubProvider) Chat(_ context.Context, m []Message, tools []ToolSpec) (Response, error) {
	s.got, s.tools = m, len(tools)
	return Response{Content: s.reply}, s.err
}

type failingExtractor struct{ code string }

func (f failingExtractor) Extract(context.Context, ExtractRequest) (ExtractResult, error) {
	return ExtractResult{}, &WorkerError{Code: f.code, Status: 422}
}

func TestReasonFromCode(t *testing.T) {
	for code, want := range map[string]string{CodeEncrypted: ReasonEncrypted, CodeUnreadableFile: ReasonDamaged, CodeUnsupportedFormat: ReasonUnsupported,
		CodeFileTooLarge: ReasonTooLarge, CodeExtractTimeout: ReasonTimeout, CodeOCRUnavailable: ReasonOCRUnavailable, CodeWorkerUnavailable: "", CodeFileNotFound: ""} {
		if got := ReasonFromCode(code); got != want {
			t.Errorf("%s: %q want %q", code, got, want)
		}
	}
}

func TestAgentReportsUnreadableContentAndKeepsTheLoopAlive(t *testing.T) {
	a := NewAgent(Heuristic{}, failingExtractor{CodeEncrypted}, Options{})
	res, err := a.AnalyzeFile(context.Background(), FileInput{FileID: "1", SourceName: "采购部_合同.pdf", StoredPath: "x"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.ContentStatus != ContentUnreadable || res.ContentReason != ReasonEncrypted || res.Basis != "" {
		t.Fatalf("%+v", res)
	}
}

func TestAnalyzeFilenameOnlyDeterministic(t *testing.T) {
	a := NewAgent(Heuristic{}, failingExtractor{CodeEncrypted}, Options{})
	res, err := a.AnalyzeFilenameOnly(context.Background(), FileInput{SourceName: "财务部_报销单_2026-05-20.docx"}, nil, ReasonEncrypted, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Basis != BasisFilenameOnly || res.ContentStatus != ContentUnreadable || !res.ReviewRequired || res.Trace == nil {
		t.Fatalf("%+v", res)
	}
	if res.Department == nil || *res.Department != "财务部" || res.DocumentDate == nil || *res.DocumentDate != "2026-05-20" {
		t.Fatalf("name evidence must be used: %+v", res)
	}
	if !strings.HasPrefix(res.Evidence[0], "内容无法读取（已加密），仅依据文件名判断") {
		t.Fatalf("explanation: %v", res.Evidence)
	}
	// nothing in the name: nothing invented
	res, _ = a.AnalyzeFilenameOnly(context.Background(), FileInput{SourceName: "a1b2c3.bin"}, nil, ReasonUnsupported, nil)
	if res.Department != nil || res.DocumentType != nil || res.DocumentDate != nil || !res.ReviewRequired {
		t.Fatalf("%+v", res)
	}
}

func TestAnalyzeFilenameOnlyModelSeesOnlyNameExtensionAndSize(t *testing.T) {
	p := &stubProvider{reply: `{"department":"人事部","document_type":"通知","document_date":"2030-01-01","evidence":["文件名像人事通知"],"review_required":false}`}
	a := NewAgent(p, failingExtractor{CodeEncrypted}, Options{})
	res, err := a.AnalyzeFilenameOnly(context.Background(), FileInput{SourceName: "入职安排.pdf", SizeBytes: 4321, StoredPath: "/secret/spool/path"}, nil, ReasonDamaged, nil)
	if err != nil {
		t.Fatal(err)
	}
	var sent strings.Builder
	for _, m := range p.got {
		sent.WriteString(m.Content)
	}
	if p.tools != 0 {
		t.Fatalf("no tools may be offered: %d", p.tools)
	}
	if !strings.Contains(sent.String(), "4321") || !strings.Contains(sent.String(), ".pdf") || strings.Contains(sent.String(), "/secret/spool/path") {
		t.Fatalf("prompt: %s", sent.String())
	}
	// the model may name a department/type from the NAME, but can never supply a date, and the result stays review-required
	if res.Department == nil || *res.Department != "人事部" || res.DocumentDate != nil || !res.ReviewRequired || res.Basis != BasisFilenameOnly {
		t.Fatalf("%+v", res)
	}
	found := false
	for _, e := range res.Evidence {
		found = found || strings.Contains(e, "仅依据文件名推断")
	}
	if !found {
		t.Fatalf("model-derived fields need provenance: %v", res.Evidence)
	}
}

func TestAnalyzeFilenameOnlyModelFailureDegradesToRules(t *testing.T) {
	a := NewAgent(&stubProvider{err: errors.New("boom")}, failingExtractor{CodeEncrypted}, Options{})
	res, err := a.AnalyzeFilenameOnly(context.Background(), FileInput{SourceName: "合同.pdf"}, nil, ReasonEncrypted, nil)
	if err != nil || !res.ReviewRequired || res.Basis != BasisFilenameOnly {
		t.Fatalf("%v %+v", err, res)
	}
	a = NewAgent(&stubProvider{reply: "not json"}, failingExtractor{CodeEncrypted}, Options{})
	if res, err = a.AnalyzeFilenameOnly(context.Background(), FileInput{SourceName: "x.pdf"}, nil, ReasonEncrypted, nil); err != nil || res.Department != nil {
		t.Fatalf("%v %+v", err, res)
	}
}
