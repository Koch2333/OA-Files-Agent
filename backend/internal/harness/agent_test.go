package harness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// scriptProvider replays canned responses and records what it was shown.
type scriptProvider struct {
	mu        sync.Mutex
	responses []Response
	err       error
	calls     int
	seenMsgs  [][]Message
	seenTools [][]ToolSpec
}

func (p *scriptProvider) Chat(ctx context.Context, m []Message, tools []ToolSpec) (Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seenMsgs = append(p.seenMsgs, append([]Message(nil), m...))
	p.seenTools = append(p.seenTools, tools)
	if p.err != nil {
		return Response{}, p.err
	}
	i := p.calls
	p.calls++
	if i >= len(p.responses) {
		i = len(p.responses) - 1 // repeat the last response forever
	}
	return p.responses[i], nil
}

func tc(name, args string) ToolCall { return ToolCall{Name: name, Arguments: json.RawMessage(args)} }

func TestLoopMaxTurns(t *testing.T) {
	p := &scriptProvider{responses: []Response{{ToolCalls: []ToolCall{tc("inspect_file", `{}`)}}}}
	o := testOpts()
	o.MaxTurns = 3
	a := NewAgent(p, &fakeExtractor{}, o)
	res, err := a.AnalyzeFile(context.Background(), fileIn("a.docx", ""), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.calls != 3 {
		t.Errorf("provider calls = %d, want 3", p.calls)
	}
	if !res.ReviewRequired || res.Department != nil || !hasWarn(res, "agent_limit:max_turns") {
		t.Errorf("unexpected result %+v", res)
	}
}

func TestLoopFailureCap(t *testing.T) {
	p := &scriptProvider{responses: []Response{{ToolCalls: []ToolCall{tc("run_shell", `{"cmd":"rm -rf /"}`)}}}}
	o := testOpts()
	o.MaxFailures = 2
	a := NewAgent(p, &fakeExtractor{}, o)
	res, err := a.AnalyzeFile(context.Background(), fileIn("a.docx", ""), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.calls != 2 || !hasWarn(res, "agent_limit:max_failures") {
		t.Errorf("calls=%d warnings=%v", p.calls, res.Warnings)
	}
	if len(res.Trace) != 2 || res.Trace[0].ErrorCode != ToolUnknown {
		t.Errorf("trace = %+v", res.Trace)
	}
}

func TestLoopContextBudget(t *testing.T) {
	p := &scriptProvider{responses: []Response{{ToolCalls: []ToolCall{tc("inspect_file", `{}`)}}}}
	o := testOpts()
	o.MaxContextChars = 100 // the system prompt alone exceeds this
	a := NewAgent(p, &fakeExtractor{}, o)
	res, _ := a.AnalyzeFile(context.Background(), fileIn("a.docx", ""), nil, nil)
	if p.calls != 0 || !hasWarn(res, "agent_limit:context_budget") {
		t.Errorf("calls=%d warnings=%v", p.calls, res.Warnings)
	}
}

func TestLoopTokenBudget(t *testing.T) {
	p := &scriptProvider{responses: []Response{{ToolCalls: []ToolCall{tc("inspect_file", `{}`)}, Usage: Usage{PromptTokens: 600, CompletionTokens: 600}}}}
	o := testOpts()
	o.MaxTokens = 1000
	a := NewAgent(p, &fakeExtractor{}, o)
	res, _ := a.AnalyzeFile(context.Background(), fileIn("a.docx", ""), nil, nil)
	if p.calls != 1 || !hasWarn(res, "agent_limit:token_budget") {
		t.Errorf("calls=%d warnings=%v", p.calls, res.Warnings)
	}
}

func TestLoopBadFinalFormat(t *testing.T) {
	p := &scriptProvider{responses: []Response{{Content: "I think it is finance"}}}
	o := testOpts()
	o.MaxFailures = 2
	res, err := NewAgent(p, &fakeExtractor{}, o).AnalyzeFile(context.Background(), fileIn("a.docx", ""), nil, nil)
	if err != nil || !hasWarn(res, "agent_limit:max_failures") {
		t.Fatalf("err=%v res=%+v", err, res)
	}
}

func TestLoopContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a := NewAgent(Heuristic{}, &fakeExtractor{}, testOpts())
	if _, err := a.AnalyzeFile(ctx, fileIn("a.docx", ""), nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	// cancellation during a slow tool
	ctx2, cancel2 := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel2()
	_, err := NewAgent(Heuristic{}, &fakeExtractor{delay: 5 * time.Second}, testOpts()).AnalyzeFile(ctx2, fileIn("a.docx", ""), nil, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}

func TestLoopToolTimeoutIsObservation(t *testing.T) {
	o := testOpts()
	o.ToolTimeout = 30 * time.Millisecond
	ext := &fakeExtractor{delay: 2 * time.Second}
	p := &scriptProvider{responses: []Response{
		{ToolCalls: []ToolCall{tc("extract_text", `{}`)}},
		{Content: `{"department":null,"document_type":null,"document_date":null,"evidence":[],"review_required":true}`},
	}}
	res, err := NewAgent(p, ext, o).AnalyzeFile(context.Background(), fileIn("a.docx", ""), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Trace) != 1 || res.Trace[0].ErrorCode != ToolTimeout {
		t.Fatalf("trace = %+v", res.Trace)
	}
	last := p.seenMsgs[1][len(p.seenMsgs[1])-1]
	if last.Role != RoleTool || !strings.Contains(last.Content, `"timeout"`) {
		t.Errorf("observation not fed back: %+v", last)
	}
}

func TestProviderErrorReturned(t *testing.T) {
	p := &scriptProvider{err: errors.New("boom")}
	_, err := NewAgent(p, &fakeExtractor{}, testOpts()).AnalyzeFile(context.Background(), fileIn("a.docx", ""), nil, nil)
	if err == nil {
		t.Fatal("want error")
	}
}

func TestFinalValidation(t *testing.T) {
	tests := []struct {
		name   string
		answer string
		strict bool
		dept   string
		typ    string
		date   string
		review bool
	}{
		{"valid", `{"department":" 财务部 ","document_type":"申请","document_date":"2026-03-15","evidence":["x"],"review_required":false}`, false, "财务部", "申请", "2026-03-15", false},
		{"fenced json", "```json\n" + `{"department":"财务部","document_type":"申请","document_date":"2026-03-15","evidence":["x"],"review_required":false}` + "\n```", false, "财务部", "申请", "2026-03-15", false},
		{"bad calendar date", `{"department":"财务部","document_type":"申请","document_date":"2026-02-30","evidence":["x"],"review_required":false}`, false, "财务部", "申请", "", true},
		{"wrong format date", `{"department":"财务部","document_type":"申请","document_date":"2026年3月15日","evidence":["x"],"review_required":false}`, false, "财务部", "申请", "", true},
		{"far future", `{"department":"财务部","document_type":"申请","document_date":"2099-01-01","evidence":["x"],"review_required":false}`, false, "财务部", "申请", "", true},
		{"no evidence", `{"department":"财务部","document_type":"申请","document_date":"2026-03-15","evidence":[],"review_required":false}`, false, "", "", "", true},
		{"synonym normalised", `{"department":"会计","document_type":"报销","document_date":"2026-03-15","evidence":["x"],"review_required":false}`, true, "财务部", "申请", "2026-03-15", false},
		{"strict vocab rejects", `{"department":"火星部","document_type":"申请","document_date":"2026-03-15","evidence":["x"],"review_required":false}`, true, "", "申请", "2026-03-15", true},
		{"lenient vocab accepts", `{"department":"火星部","document_type":"申请","document_date":"2026-03-15","evidence":["x"],"review_required":false}`, false, "火星部", "申请", "2026-03-15", false},
		{"length cap", `{"department":"` + strings.Repeat("长", 500) + `","document_type":null,"document_date":null,"evidence":["x"],"review_required":false}`, false, strings.Repeat("长", maxFieldRunes), "", "", true},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			o := testOpts()
			o.StrictVocabulary = c.strict
			p := &scriptProvider{responses: []Response{{Content: c.answer}}}
			res, err := NewAgent(p, &fakeExtractor{}, o).AnalyzeFile(context.Background(), fileIn("a.docx", ""), nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			got := [3]string{strings.Trim(deref(res.Department), "<nil>"), strings.Trim(deref(res.DocumentType), "<nil>"), strings.Trim(deref(res.DocumentDate), "<nil>")}
			if got != [3]string{c.dept, c.typ, c.date} || res.ReviewRequired != c.review {
				t.Errorf("got %v review=%v warnings=%v", got, res.ReviewRequired, res.Warnings)
			}
		})
	}
}

func hasWarn(r Result, w string) bool {
	for _, x := range r.Warnings {
		if x == w {
			return true
		}
	}
	return false
}

func TestTemplateMatching(t *testing.T) {
	tpls := []Template{
		{ID: "t-off", Name: "停用", Enabled: false, Priority: 100, FilenameKeywords: []string{"报销"}, Department: "行政部"},
		{ID: "t-low", Name: "低优先级", Enabled: true, Priority: 1, FilenameKeywords: []string{"报销"}, Department: "行政部", DocumentType: "通知"},
		{ID: "t-high", Name: "报销模板", Enabled: true, Priority: 10, FilenameKeywords: []string{"报销"}, Department: "财务部", DocumentType: "申请", DateSource: "filename"},
		{ID: "t-text", Name: "正文模板", Enabled: true, Priority: 5, TextKeywords: []string{"差旅"}, Department: "人事部"},
	}
	ext := &fakeExtractor{text: longPad + "日期：2026年1月1日"}
	res, err := NewAgent(Heuristic{}, ext, testOpts()).AnalyzeFile(context.Background(), fileIn("报销单_20260315.pdf", "application/pdf"), tpls, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.TemplateID == nil || *res.TemplateID != "t-high" {
		t.Fatalf("template = %v", deref(res.TemplateID))
	}
	if deref(res.Department) != "财务部" || deref(res.DocumentType) != "申请" {
		t.Errorf("seed not applied: %v %v", deref(res.Department), deref(res.DocumentType))
	}
	// DateSource=filename beats the body date, without a conflict.
	if deref(res.DocumentDate) != "2026-03-15" || res.ReviewRequired {
		t.Errorf("date = %v review=%v", deref(res.DocumentDate), res.ReviewRequired)
	}
	if !strings.Contains(strings.Join(res.Evidence, "\n"), "模板") {
		t.Errorf("template evidence missing: %v", res.Evidence)
	}

	// text keyword match
	res, _ = NewAgent(Heuristic{}, &fakeExtractor{text: longPad + "差旅费用说明"}, testOpts()).AnalyzeFile(context.Background(), fileIn("a.docx", ""), tpls, nil)
	if res.TemplateID == nil || *res.TemplateID != "t-text" {
		t.Errorf("text template = %v", deref(res.TemplateID))
	}
	// none matches
	res, _ = NewAgent(Heuristic{}, &fakeExtractor{text: longPad}, testOpts()).AnalyzeFile(context.Background(), fileIn("a.docx", ""), tpls, nil)
	if res.TemplateID != nil {
		t.Errorf("unexpected template %v", *res.TemplateID)
	}
}

func TestTemplateDateSourceMetadata(t *testing.T) {
	tpls := []Template{{ID: "m", Name: "元数据", Enabled: true, Priority: 1, FilenameKeywords: []string{"scan"}, Department: "行政部", DocumentType: "通知", DateSource: "file_metadata"}}
	created := "2026-03-10T08:00:00Z"
	ext := &fakeExtractor{text: longPad, meta: FileMetadata{Created: &created}}
	res, _ := NewAgent(Heuristic{}, ext, testOpts()).AnalyzeFile(context.Background(), fileIn("scan.pdf", ""), tpls, nil)
	if deref(res.DocumentDate) != "2026-03-10" {
		t.Fatalf("date = %v", deref(res.DocumentDate))
	}
	// Without the template, metadata is never used as the document date.
	res, _ = NewAgent(Heuristic{}, ext, testOpts()).AnalyzeFile(context.Background(), fileIn("scan.pdf", ""), nil, nil)
	if res.DocumentDate != nil {
		t.Fatalf("metadata date leaked: %v", *res.DocumentDate)
	}
}

func TestTemplateConflictFlagsReview(t *testing.T) {
	tpls := []Template{{ID: "t", Name: "T", Enabled: true, Priority: 1, FilenameKeywords: []string{"单"}, Department: "财务部", DocumentType: "申请"}}
	ext := &fakeExtractor{text: "发文部门：人事部\n" + longPad + "日期：2026-03-15"}
	res, _ := NewAgent(Heuristic{}, ext, testOpts()).AnalyzeFile(context.Background(), fileIn("单据.docx", ""), tpls, nil)
	if !res.ReviewRequired {
		t.Error("template/text disagreement must require review")
	}
}
