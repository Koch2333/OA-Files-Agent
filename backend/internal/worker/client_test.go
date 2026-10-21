package worker

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestConfiguration(t *testing.T) {
	for _, url := range []string{"https://127.0.0.1:8090", "http://example.com", "http://127.0.0.1@evil.example", "http://127.0.0.1/a", "http://127.0.0.1?secret=1"} {
		if _, err := New(url, strings.Repeat("a", 32)); err == nil {
			t.Fatalf("accepted %s", url)
		}
	}
	if _, err := New("http://127.0.0.1:8090", "short"); err == nil {
		t.Fatal("accepted short token")
	}
	c, err := New("http://127.0.0.1:8090", "")
	if err != nil || c.Check(context.Background()) != "not_configured" {
		t.Fatal("missing token must be unconfigured")
	}
}

func TestHealthResponses(t *testing.T) {
	for _, tc := range []struct {
		name       string
		code       int
		body, want string
	}{
		{"healthy skeleton", 200, `{"data":{"status":"ok","extraction_available":false}}`, "ok"},
		{"auth", 401, "", "unauthorized"},
		{"redirect", 302, "", "unavailable"},
		{"crash", 500, "", "unavailable"},
		{"malformed", 200, "oops", "invalid_response"},
		{"missing capability", 200, `{"data":{"status":"ok"}}`, "invalid_response"},
		{"oversized", 200, strings.Repeat("x", 4097), "invalid_response"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			token := strings.Repeat("a", 32)
			c, err := New("http://127.0.0.1:8090", token)
			if err != nil {
				t.Fatal(err)
			}
			c.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
				if r.Header.Get("Authorization") != "Bearer "+token || r.URL.Path != "/healthz" {
					t.Error("incorrect worker request")
				}
				return &http.Response{StatusCode: tc.code, Body: io.NopCloser(strings.NewReader(tc.body)), Header: make(http.Header)}, nil
			})
			if got := c.Check(context.Background()); got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestCancellation(t *testing.T) {
	c, _ := New("http://127.0.0.1:8090", strings.Repeat("a", 32))
	c.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if got := c.Check(ctx); got != "timeout" {
		t.Fatal(got)
	}
}

// Optional cross-process check against the actual Python worker, not a fake health endpoint.
func TestPythonWorkerIntegration(t *testing.T) {
	python := os.Getenv("OA_TEST_PYTHON")
	if python == "" {
		t.Skip("set OA_TEST_PYTHON to run the real Python worker integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, python, "-u", "-c", "from server import create_server; s=create_server(0); print(s.server_port, flush=True); s.serve_forever()")
	dir, err := filepath.Abs("../../../workers/document-ai")
	if err != nil {
		t.Fatal(err)
	}
	cmd.Dir = dir
	token := strings.Repeat("integration-test-", 3)
	cmd.Env = append(os.Environ(), "OA_WORKER_TOKEN="+token)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); cmd.Wait() }()
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		t.Fatal("worker did not announce port")
	}
	c, err := New("http://127.0.0.1:"+scanner.Text(), token)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Check(ctx); got != "ok" {
		t.Fatalf("Python health: %s", got)
	}
	c.token = strings.Repeat("wrong-", 6)
	if got := c.Check(ctx); got != "unauthorized" {
		t.Fatalf("Python auth: %s", got)
	}
}
