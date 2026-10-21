package harness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestToolInputValidation(t *testing.T) {
	o := testOpts()
	o.CheckDuplicates = func(context.Context, FileInput) ([]DuplicateRef, error) { return nil, nil }
	reg := NewDefaultRegistry(o)
	s := newSession(fileIn("a.docx", ""), nil, &fakeExtractor{text: longPad}, o.withDefaults())
	tests := []struct {
		name string
		tool string
		args string
		code string // "" = success
	}{
		{"unknown tool", "run_shell", `{}`, ToolUnknown},
		{"write tool absent", "write_file", `{"path":"/etc/passwd"}`, ToolUnknown},
		{"path arg rejected", "extract_text", `{"path":"/etc/passwd"}`, ToolInvalidInput},
		{"extra arg on inspect", "inspect_file", `{"path":"/x"}`, ToolInvalidInput},
		{"max_chars wrong type", "extract_text", `{"max_chars":"lots"}`, ToolInvalidInput},
		{"max_chars too small", "extract_text", `{"max_chars":1}`, ToolInvalidInput},
		{"max_chars too big", "extract_text", `{"max_chars":99999999}`, ToolInvalidInput},
		{"not an object", "extract_text", `[1,2]`, ToolInvalidInput},
		{"trailing garbage", "inspect_file", `{} {}`, ToolInvalidInput},
		{"empty args ok", "inspect_file", ``, ""},
		{"null args ok", "inspect_file", `null`, ""},
		{"validate_plan type", "validate_plan", `{"department":5}`, ToolInvalidInput},
		{"validate_plan date too long", "validate_plan", `{"document_date":"2026-03-15T00:00:00"}`, ToolInvalidInput},
		{"validate_plan ok", "validate_plan", `{"department":"财务部","evidence":["a"]}`, ""},
		{"metadata before extract", "extract_metadata", `{}`, ToolFailed},
		{"check_duplicates ok", "check_duplicates", `{}`, ""},
		{"oversized args", "extract_text", `{"max_chars":100,"x":"` + strings.Repeat("a", 20000) + `"}`, ToolInvalidInput},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			out, err := reg.Execute(context.Background(), s, c.tool, json.RawMessage(c.args))
			if c.code == "" {
				if err != nil {
					t.Fatalf("unexpected error %v", err)
				}
				if !json.Valid([]byte(out)) {
					t.Fatalf("invalid output %q", out)
				}
				return
			}
			var te *ToolError
			if !errors.As(err, &te) || te.Code != c.code {
				t.Fatalf("err = %v, want code %s", err, c.code)
			}
		})
	}
}

func TestRegistryHasNoWriteOrShell(t *testing.T) {
	reg := NewDefaultRegistry(Options{CheckDuplicates: func(context.Context, FileInput) ([]DuplicateRef, error) { return nil, nil }})
	want := "check_duplicates,detect_template,extract_metadata,extract_text,inspect_file,run_ocr,validate_plan"
	if got := strings.Join(reg.Names(), ","); got != want {
		t.Fatalf("tools = %s", got)
	}
	for _, sp := range reg.Specs() {
		if sp.Parameters["type"] != "object" || sp.Parameters["additionalProperties"] != false {
			t.Errorf("%s schema not closed", sp.Name)
		}
	}
	// check_duplicates is absent unless a checker is injected.
	if strings.Contains(strings.Join(NewDefaultRegistry(Options{}).Names(), ","), "check_duplicates") {
		t.Error("check_duplicates registered without checker")
	}
}

func TestOutputSchemaEnforced(t *testing.T) {
	reg := NewRegistry()
	reg.Register(Tool{Name: "bad", InputSchema: objSchema(map[string]any{}), OutputSchema: objSchema(map[string]any{"n": map[string]any{"type": "integer"}}, "n"),
		Timeout: time.Second, Run: func(context.Context, *Session, map[string]any) (any, error) { return map[string]any{"n": "x"}, nil }})
	reg.Register(Tool{Name: "panics", InputSchema: objSchema(map[string]any{}), OutputSchema: objSchema(map[string]any{}),
		Timeout: time.Second, Run: func(context.Context, *Session, map[string]any) (any, error) { panic("x") }})
	reg.Register(Tool{Name: "slow", InputSchema: objSchema(map[string]any{}), OutputSchema: objSchema(map[string]any{}),
		Timeout: 20 * time.Millisecond, Run: func(ctx context.Context, _ *Session, _ map[string]any) (any, error) {
			time.Sleep(300 * time.Millisecond)
			return map[string]any{}, nil
		}})
	for name, code := range map[string]string{"bad": ToolOutputInvalid, "panics": ToolFailed, "slow": ToolTimeout} {
		_, err := reg.Execute(context.Background(), nil, name, nil)
		var te *ToolError
		if !errors.As(err, &te) || te.Code != code {
			t.Errorf("%s: err = %v, want %s", name, err, code)
		}
	}
}

func TestWorkerErrorMapsToToolCode(t *testing.T) {
	ext := &fakeExtractor{err: &WorkerError{Code: CodeOCRUnavailable, Status: 503, Message: "OCR is not available"}}
	reg := NewDefaultRegistry(testOpts())
	s := newSession(fileIn("a.png", "image/png"), nil, ext, testOpts().withDefaults())
	_, err := reg.Execute(context.Background(), s, "run_ocr", nil)
	var te *ToolError
	if !errors.As(err, &te) || te.Code != CodeOCRUnavailable {
		t.Fatalf("err = %v", err)
	}
}

func TestCheckDuplicatesInjected(t *testing.T) {
	o := testOpts()
	o.CheckDuplicates = func(_ context.Context, f FileInput) ([]DuplicateRef, error) {
		return []DuplicateRef{{FileID: "other", Name: "same.pdf"}}, nil
	}
	reg := NewDefaultRegistry(o)
	out, err := reg.Execute(context.Background(), newSession(fileIn("a.pdf", ""), nil, &fakeExtractor{}, o.withDefaults()), "check_duplicates", nil)
	if err != nil || !strings.Contains(out, `"count":1`) {
		t.Fatalf("out=%s err=%v", out, err)
	}
	o.CheckDuplicates = func(context.Context, FileInput) ([]DuplicateRef, error) {
		return nil, errors.New("db password=hunter2")
	}
	reg = NewDefaultRegistry(o)
	_, err = reg.Execute(context.Background(), newSession(fileIn("a.pdf", ""), nil, &fakeExtractor{}, o.withDefaults()), "check_duplicates", nil)
	if err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("internal error leaked: %v", err)
	}
}

func TestExtractTextWrapsAndTruncates(t *testing.T) {
	o := testOpts()
	o.MaxTextChars = 200
	reg := NewDefaultRegistry(o)
	s := newSession(fileIn("a.docx", ""), nil, &fakeExtractor{text: strings.Repeat("文", 1000)}, o.withDefaults())
	out, err := reg.Execute(context.Background(), s, "extract_text", nil)
	if err != nil {
		t.Fatal(err)
	}
	var r struct {
		Text       string
		Chars      int
		ShownChars int `json:"shown_chars"`
	}
	_ = json.Unmarshal([]byte(out), &r)
	if r.Chars != 1000 || r.ShownChars != 200 || !strings.HasPrefix(r.Text, "<<<DOCUMENT_DATA") || !strings.Contains(r.Text, "<<<END_DOCUMENT_DATA id="+s.nonce) {
		t.Errorf("unexpected: %+v", r)
	}
}
