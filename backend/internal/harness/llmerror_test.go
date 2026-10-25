package harness

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const okBody = `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`

func chatOnce(p *OpenAICompatible) (Response, error) {
	return p.Chat(context.Background(), []Message{{Role: RoleUser, Content: "SECRET-DOC-TEXT"}}, nil)
}

func TestLLM429RetryAfterThenSuccess(t *testing.T) {
	var n int32
	p := newTestOpenAI(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&n, 1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(429)
			return
		}
		_, _ = w.Write([]byte(okBody))
	}, nil)
	if _, err := chatOnce(p); err != nil || n != 2 {
		t.Fatalf("err=%v attempts=%d", err, n)
	}
}

func TestLLM500sThenSuccess(t *testing.T) {
	var n int32
	p := newTestOpenAI(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&n, 1) <= 2 {
			w.WriteHeader(503)
			return
		}
		_, _ = w.Write([]byte(okBody))
	}, nil)
	if _, err := chatOnce(p); err != nil || n != 3 {
		t.Fatalf("err=%v attempts=%d", err, n)
	}
}

func TestLLMExhaustedIsTyped(t *testing.T) {
	var n int32
	p := newTestOpenAI(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&n, 1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(429)
		_, _ = w.Write([]byte("sk-test SECRET-DOC-TEXT"))
	}, nil)
	_, err := chatOnce(p)
	pe, ok := AsProviderError(err)
	if !ok || pe.Code != LLMRateLimited || pe.Status != 429 || !pe.Retryable || pe.Attempts != 4 || n != 4 {
		t.Fatalf("%+v n=%d", pe, n)
	}
	msg := pe.UserMessage() + pe.Error()
	if strings.Contains(msg, "sk-test") || strings.Contains(msg, "SECRET") {
		t.Fatalf("leak: %s", msg)
	}
	if !strings.Contains(pe.UserMessage(), "429") || !strings.Contains(pe.UserMessage(), "自动重试 3 次") {
		t.Fatal(pe.UserMessage())
	}
}

func TestLLMNonRetryable(t *testing.T) {
	for status, code := range map[int]string{401: LLMAuth, 403: LLMAuth, 404: LLMNotFound, 400: LLMRejected} {
		var n int32
		p := newTestOpenAI(t, func(w http.ResponseWriter, r *http.Request) { atomic.AddInt32(&n, 1); w.WriteHeader(status) }, nil)
		_, err := chatOnce(p)
		pe, ok := AsProviderError(err)
		if !ok || pe.Code != code || pe.Retryable || n != 1 || pe.Attempts != 1 {
			t.Errorf("status %d: %+v n=%d", status, pe, n)
		}
	}
}

func TestLLMRetryAfterBeyondCapStopsAndReports(t *testing.T) {
	var n int32
	p := newTestOpenAI(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&n, 1)
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(429)
	}, nil)
	_, err := chatOnce(p)
	pe, _ := AsProviderError(err)
	if pe == nil || pe.RetryAfterSec != 120 || n != 1 || !pe.Retryable {
		t.Fatalf("%+v n=%d", pe, n)
	}
}

func TestLLMTimeoutClassified(t *testing.T) {
	p := newTestOpenAI(t, func(w http.ResponseWriter, r *http.Request) { time.Sleep(200 * time.Millisecond) }, func(c *OpenAIConfig) {
		c.Timeout = 20 * time.Millisecond
		c.MaxRetries = 1
	})
	_, err := chatOnce(p)
	pe, ok := AsProviderError(err)
	if !ok || pe.Code != LLMTimeout || !pe.Retryable || pe.Attempts != 2 {
		t.Fatalf("%+v", pe)
	}
}

func TestLLMTruncatedBody(t *testing.T) {
	var n int32
	p := newTestOpenAI(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&n, 1)
		w.Header().Set("Content-Length", "500")
		_, _ = w.Write([]byte(`{"choices":`))
	}, func(c *OpenAIConfig) { c.MaxRetries = 1 })
	_, err := chatOnce(p)
	pe, ok := AsProviderError(err)
	if !ok || pe.Code != LLMBadResponse || !pe.Retryable || n != 2 {
		t.Fatalf("%+v n=%d", pe, n)
	}
}

func TestLLMConnectionRefused(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	p, _ := NewOpenAICompatible(OpenAIConfig{BaseURL: url, Model: "m", MaxRetries: 1, BackoffBase: time.Millisecond, MaxBackoff: time.Millisecond})
	_, err := chatOnce(p)
	pe, ok := AsProviderError(err)
	if !ok || pe.Code != LLMUnreachable || !pe.Retryable || pe.Attempts != 2 || strings.Contains(pe.Error(), "127.0.0.1") {
		t.Fatalf("%+v", pe)
	}
}

func TestLLMTLSFailure(t *testing.T) {
	srv := httptest.NewTLSServer(http.NotFoundHandler()) // self-signed: default client rejects it
	defer srv.Close()
	p, _ := NewOpenAICompatible(OpenAIConfig{BaseURL: srv.URL, Model: "m", MaxRetries: -1})
	_, err := chatOnce(p)
	if pe, ok := AsProviderError(err); !ok || pe.Code != LLMUnreachable {
		t.Fatalf("%+v", pe)
	}
}

func TestLLMContextCancelStopsRetries(t *testing.T) {
	var n int32
	p := newTestOpenAI(t, func(w http.ResponseWriter, r *http.Request) { atomic.AddInt32(&n, 1); w.WriteHeader(500) }, func(c *OpenAIConfig) {
		c.BackoffBase = time.Second
		c.MaxBackoff = time.Second
	})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := p.Chat(ctx, []Message{{Role: RoleUser, Content: "x"}}, nil); err == nil || n > 2 {
		t.Fatalf("err=%v n=%d", err, n)
	}
}

func TestParseRetryAfterDate(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if got := parseRetryAfter(now.Add(30*time.Second).Format(http.TimeFormat), now); got != 30 {
		t.Fatal(got)
	}
	if parseRetryAfter("garbage", now) != 0 || parseRetryAfter("7", now) != 7 {
		t.Fatal("parse")
	}
}
