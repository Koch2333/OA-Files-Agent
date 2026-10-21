package harness

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTestOpenAI(t *testing.T, h http.HandlerFunc, mod func(*OpenAIConfig)) *OpenAICompatible {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	cfg := OpenAIConfig{BaseURL: srv.URL + "/v1", Model: "test-model", APIKey: "sk-test", BackoffBase: time.Millisecond, MaxBackoff: 5 * time.Millisecond}
	if mod != nil {
		mod(&cfg)
	}
	p, err := NewOpenAICompatible(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestOpenAIToolCallRoundTrip(t *testing.T) {
	var captured map[string]any
	var auth, path string
	p := newTestOpenAI(t, func(w http.ResponseWriter, r *http.Request) {
		auth, path = r.Header.Get("Authorization"), r.URL.Path
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"extract_text","arguments":"{\"max_chars\":500}"}}]}}],"usage":{"prompt_tokens":11,"completion_tokens":7}}`))
	}, nil)
	reg := NewDefaultRegistry(Options{})
	resp, err := p.Chat(context.Background(), []Message{
		{Role: RoleSystem, Content: "sys"},
		{Role: RoleUser, Content: "go"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c0", Name: "inspect_file", Arguments: json.RawMessage(`{}`)}}},
		{Role: RoleTool, ToolCallID: "c0", Name: "inspect_file", Content: `{"ok":true}`},
	}, reg.Specs())
	if err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer sk-test" || path != "/v1/chat/completions" {
		t.Errorf("auth=%q path=%q", auth, path)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "extract_text" || resp.ToolCalls[0].ID != "call_1" || string(resp.ToolCalls[0].Arguments) != `{"max_chars":500}` {
		t.Errorf("tool calls = %+v", resp.ToolCalls)
	}
	if resp.Usage.PromptTokens != 11 || resp.Usage.CompletionTokens != 7 {
		t.Errorf("usage = %+v", resp.Usage)
	}
	if captured["model"] != "test-model" || captured["tool_choice"] != "auto" {
		t.Errorf("request = %v", captured)
	}
	tools := captured["tools"].([]any)
	if len(tools) != len(reg.Specs()) || tools[0].(map[string]any)["type"] != "function" {
		t.Errorf("tools = %v", tools)
	}
	msgs := captured["messages"].([]any)
	asst := msgs[2].(map[string]any)
	tcs := asst["tool_calls"].([]any)
	if tcs[0].(map[string]any)["function"].(map[string]any)["arguments"] != "{}" {
		t.Errorf("assistant tool_calls not encoded as string args: %v", asst)
	}
	if msgs[3].(map[string]any)["tool_call_id"] != "c0" {
		t.Errorf("tool message = %v", msgs[3])
	}
}

func TestOpenAIRetry429Then5xxThenOK(t *testing.T) {
	var n int32
	p := newTestOpenAI(t, func(w http.ResponseWriter, r *http.Request) {
		switch atomic.AddInt32(&n, 1) {
		case 1:
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(429)
		case 2:
			w.WriteHeader(503)
		default:
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"evidence\":[]}"}}]}`))
		}
	}, nil)
	resp, err := p.Chat(context.Background(), []Message{{Role: RoleUser, Content: "x"}}, nil)
	if err != nil || resp.Content != `{"evidence":[]}` || n != 3 {
		t.Fatalf("resp=%+v err=%v attempts=%d", resp, err, n)
	}
}

func TestOpenAIRetryExhaustedAndNoRetryOn4xx(t *testing.T) {
	var n int32
	p := newTestOpenAI(t, func(w http.ResponseWriter, r *http.Request) { atomic.AddInt32(&n, 1); w.WriteHeader(429) }, func(c *OpenAIConfig) { c.MaxRetries = 2 })
	_, err := p.Chat(context.Background(), nil, nil)
	var pe *ProviderError
	if !errors.As(err, &pe) || pe.Status != 429 || n != 3 {
		t.Fatalf("err=%v attempts=%d", err, n)
	}
	n = 0
	p = newTestOpenAI(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&n, 1)
		w.WriteHeader(401)
		_, _ = w.Write([]byte("bad key sk-test"))
	}, nil)
	_, err = p.Chat(context.Background(), nil, nil)
	if !errors.As(err, &pe) || pe.Status != 401 || n != 1 || strings.Contains(err.Error(), "sk-test") {
		t.Fatalf("err=%v attempts=%d", err, n)
	}
}

func TestOpenAITimeoutAndCancel(t *testing.T) {
	p := newTestOpenAI(t, func(w http.ResponseWriter, r *http.Request) { time.Sleep(300 * time.Millisecond) }, func(c *OpenAIConfig) {
		c.Timeout = 30 * time.Millisecond
		c.MaxRetries = -1
	})
	if _, err := p.Chat(context.Background(), nil, nil); err == nil {
		t.Fatal("want timeout error")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Chat(ctx, nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenAIResponseCapAndConfig(t *testing.T) {
	p := newTestOpenAI(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(strings.Repeat("x", 5000))) }, func(c *OpenAIConfig) { c.MaxResponse = 1000 })
	if _, err := p.Chat(context.Background(), nil, nil); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("err = %v", err)
	}
	if _, err := NewOpenAICompatible(OpenAIConfig{BaseURL: "ftp://x", Model: "m"}); err == nil {
		t.Error("bad scheme accepted")
	}
	if _, err := NewOpenAICompatible(OpenAIConfig{BaseURL: "http://x"}); err == nil {
		t.Error("missing model accepted")
	}
	t.Setenv("OA_LLM_BASE_URL", "http://localhost:1/v1")
	t.Setenv("OA_LLM_MODEL", "m")
	t.Setenv("OA_LLM_API_KEY", "k")
	if c := OpenAIConfigFromEnv(); c.BaseURL == "" || c.Model != "m" || c.APIKey != "k" {
		t.Errorf("env config = %+v", c)
	}
}

// End to end: the agent drives an OpenAI-compatible server through tool calls.
func TestAgentWithOpenAIServer(t *testing.T) {
	var turn int32
	p := newTestOpenAI(t, func(w http.ResponseWriter, r *http.Request) {
		switch atomic.AddInt32(&turn, 1) {
		case 1:
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"","tool_calls":[{"id":"a","type":"function","function":{"name":"extract_text","arguments":"{}"}},{"id":"b","type":"function","function":{"name":"extract_metadata","arguments":""}}]}}]}`))
		default:
			b, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(b), `tool_call_id`) {
				t.Error("observations not sent back")
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"department\":\"财务部\",\"document_type\":\"申请\",\"document_date\":\"2026-03-15\",\"evidence\":[\"文件名含报销\"],\"review_required\":false}"}}]}`))
		}
	}, nil)
	ext := &fakeExtractor{text: longPad}
	res, err := NewAgent(p, ext, testOpts()).AnalyzeFile(context.Background(), fileIn("报销.docx", ""), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if deref(res.Department) != "财务部" || res.ReviewRequired || len(res.Trace) != 2 || !res.Trace[1].OK {
		t.Errorf("res = %+v", res)
	}
}
