package oauthhttp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"oa-agent/backend/internal/cloud"
)

func TestEndpointOnlyLocal(t *testing.T) {
	for in, want := range map[string]string{
		"": "def", "https://evil.example.com": "def", "http://169.254.169.254": "def", "ftp://127.0.0.1": "def",
		"http://127.0.0.1:8080/": "http://127.0.0.1:8080", "http://localhost:1": "http://localhost:1", "http://10.1.2.3": "http://10.1.2.3",
	} {
		if got := Endpoint("def", in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestRetryAfterAndGiveUp(t *testing.T) {
	BackoffBase = time.Millisecond
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(503)
		w.Write([]byte("secret body"))
	}))
	defer srv.Close()
	d := &Doer{Name: "x", HC: NewClient()}
	_, err := d.Do(context.Background(), "op", func(ctx context.Context) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
	})
	if err == nil || n.Load() != MaxAttempts || strings.Contains(err.Error(), "secret body") {
		t.Fatalf("err=%v attempts=%d", err, n.Load())
	}
	var se *StatusError
	if !errors.As(err, &se) || se.Status != 503 {
		t.Fatalf("status error: %v", err)
	}
}

func TestSafePath(t *testing.T) {
	if p, err := SafePath("a/b", "c"); err != nil || p != "a/b/c" {
		t.Fatal(p, err)
	}
	if _, err := SafePath("a", "../c"); err == nil {
		t.Fatal("escape accepted")
	}
	_ = cloud.ErrAuth
}

func TestAuthorizationNotForwardedAcrossHosts(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("Authorization leaked to another host")
		}
	}))
	defer other.Close()
	// 127.0.0.1 vs localhost count as different hosts
	otherURL := strings.Replace(other.URL, "127.0.0.1", "localhost", 1)
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, otherURL, http.StatusFound)
	}))
	defer first.Close()
	d := &Doer{Name: "x", HC: NewClient(), Token: func(context.Context, string) (string, error) { return "tok", nil }}
	resp, err := d.Do(context.Background(), "op", func(ctx context.Context) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, "GET", first.URL, nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	Drain(resp)
}
