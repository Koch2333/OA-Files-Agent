package harness

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testToken = "0123456789abcdef0123456789abcdef"

func TestNewWorkerExtractorValidation(t *testing.T) {
	tests := []struct {
		name, url, token string
		ok               bool
	}{
		{"loopback v4", "http://127.0.0.1:8765", testToken, true},
		{"loopback v6", "http://[::1]:8765/", testToken, true},
		{"remote host", "http://10.0.0.5:8765", testToken, false},
		{"hostname", "http://example.com", testToken, false},
		{"https", "https://127.0.0.1", testToken, false},
		{"userinfo", "http://u:p@127.0.0.1", testToken, false},
		{"path", "http://127.0.0.1/x", testToken, false},
		{"short token", "http://127.0.0.1:1", "short", false},
		{"token newline", "http://127.0.0.1:1", testToken[:31] + "\n", false},
	}
	for _, c := range tests {
		_, err := NewWorkerExtractor(c.url, c.token)
		if (err == nil) != c.ok {
			t.Errorf("%s: err = %v", c.name, err)
		}
	}
}

func workerServer(t *testing.T, h http.HandlerFunc) *WorkerExtractor {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	w, err := NewWorkerExtractor(srv.URL, testToken)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestWorkerExtractSuccess(t *testing.T) {
	var got ExtractRequest
	w := workerServer(t, func(rw http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/extract" || r.Header.Get("Authorization") != "Bearer "+testToken {
			rw.WriteHeader(401)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = rw.Write([]byte(`{"data":{"text":"你好","pages":[{"page":1,"text":"你好","source":"ocr","confidence":0.93}],"ocr_used":true,"truncated":false,"warnings":["w"],"metadata":{"title":null,"author":"a","created":"2026-03-15","modified":null}}}`))
	})
	res, err := w.Extract(context.Background(), ExtractRequest{Path: "/spool/1", Filename: "a.png", MIMEType: "image/png", OCR: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != "/spool/1" || !got.OCR || got.Filename != "a.png" {
		t.Errorf("request = %+v", got)
	}
	if res.Text != "你好" || !res.OCRUsed || len(res.Pages) != 1 || res.Pages[0].Confidence == nil || *res.Pages[0].Confidence != 0.93 || *res.Metadata.Created != "2026-03-15" {
		t.Errorf("result = %+v", res)
	}
}

func TestWorkerErrorMapping(t *testing.T) {
	tests := []struct {
		status int
		code   string
		want   *WorkerError
	}{
		{401, "UNAUTHORIZED", ErrUnauthorized},
		{403, "PATH_NOT_ALLOWED", ErrPathNotAllowed},
		{404, "FILE_NOT_FOUND", ErrFileNotFound},
		{413, "FILE_TOO_LARGE", ErrFileTooLarge},
		{422, "ENCRYPTED", ErrEncrypted},
		{422, "UNSUPPORTED_FORMAT", ErrUnsupportedFormat},
		{422, "UNREADABLE_FILE", ErrUnreadableFile},
		{503, "OCR_UNAVAILABLE", ErrOCRUnavailable},
		{504, "EXTRACT_TIMEOUT", ErrExtractTimeout},
		{400, "INVALID_REQUEST", ErrInvalidRequest},
	}
	for _, c := range tests {
		w := workerServer(t, func(rw http.ResponseWriter, r *http.Request) {
			rw.WriteHeader(c.status)
			_, _ = rw.Write([]byte(`{"error":{"code":"` + c.code + `","message":"/secret/path 机密正文"}}`))
		})
		_, err := w.Extract(context.Background(), ExtractRequest{})
		var we *WorkerError
		if !errors.Is(err, c.want) || !errors.As(err, &we) || we.Status != c.status {
			t.Errorf("%s: err = %v", c.code, err)
		}
		if err != nil && (strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "机密")) {
			t.Errorf("%s: worker message propagated: %v", c.code, err)
		}
	}
	// no JSON body: fall back to the status
	w := workerServer(t, func(rw http.ResponseWriter, r *http.Request) { rw.WriteHeader(503) })
	if _, err := w.Extract(context.Background(), ExtractRequest{}); !errors.Is(err, ErrOCRUnavailable) {
		t.Errorf("status fallback: %v", err)
	}
}

func TestWorkerSafetyLimits(t *testing.T) {
	// response cap
	w := workerServer(t, func(rw http.ResponseWriter, r *http.Request) { _, _ = rw.Write([]byte(strings.Repeat("a", 4096))) })
	w.MaxResponseBytes = 1024
	if _, err := w.Extract(context.Background(), ExtractRequest{}); err == nil || !strings.Contains(err.Error(), CodeWorkerBadResponse) {
		t.Errorf("cap: %v", err)
	}
	// redirects are not followed (token must not travel)
	hit := false
	other := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) { hit = true }))
	defer other.Close()
	w = workerServer(t, func(rw http.ResponseWriter, r *http.Request) {
		http.Redirect(rw, r, other.URL, http.StatusTemporaryRedirect)
	})
	if _, err := w.Extract(context.Background(), ExtractRequest{}); err == nil || hit {
		t.Errorf("redirect followed: err=%v hit=%v", err, hit)
	}
	// malformed 200
	w = workerServer(t, func(rw http.ResponseWriter, r *http.Request) { _, _ = rw.Write([]byte(`{"nope":1}`)) })
	if _, err := w.Extract(context.Background(), ExtractRequest{}); err == nil || !strings.Contains(err.Error(), CodeWorkerBadResponse) {
		t.Errorf("malformed: %v", err)
	}
	// timeout
	w = workerServer(t, func(rw http.ResponseWriter, r *http.Request) { time.Sleep(300 * time.Millisecond) })
	w.SetTimeout(30 * time.Millisecond)
	if _, err := w.Extract(context.Background(), ExtractRequest{}); !errors.Is(err, ErrWorkerUnavailable) {
		t.Errorf("timeout: %v", err)
	}
	// ctx cancel surfaces as ctx error
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := w.Extract(ctx, ExtractRequest{}); !errors.Is(err, context.Canceled) {
		t.Errorf("cancel: %v", err)
	}
}
