package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// Worker error codes (see docs/backend-design.md, Worker protocol).
const (
	CodeUnauthorized      = "UNAUTHORIZED"
	CodeInvalidRequest    = "INVALID_REQUEST"
	CodePathNotAllowed    = "PATH_NOT_ALLOWED"
	CodeFileNotFound      = "FILE_NOT_FOUND"
	CodeFileTooLarge      = "FILE_TOO_LARGE"
	CodeUnreadableFile    = "UNREADABLE_FILE"
	CodeEncrypted         = "ENCRYPTED"
	CodeUnsupportedFormat = "UNSUPPORTED_FORMAT"
	CodeOCRUnavailable    = "OCR_UNAVAILABLE"
	CodeExtractTimeout    = "EXTRACT_TIMEOUT"
	CodeWorkerUnavailable = "WORKER_UNAVAILABLE"
	CodeWorkerBadResponse = "WORKER_BAD_RESPONSE"
)

// WorkerError is a typed worker failure. Use errors.Is(err, ErrOCRUnavailable)
// etc. to test for a code, or errors.As to read Status.
type WorkerError struct {
	Code    string
	Status  int
	Message string
}

func (e *WorkerError) Error() string {
	return fmt.Sprintf("worker error %s (HTTP %d): %s", e.Code, e.Status, e.Message)
}

// Is matches on Code so sentinels below work with errors.Is.
func (e *WorkerError) Is(target error) bool {
	t, ok := target.(*WorkerError)
	return ok && t.Code == e.Code
}

var (
	ErrUnauthorized      = &WorkerError{Code: CodeUnauthorized}
	ErrInvalidRequest    = &WorkerError{Code: CodeInvalidRequest}
	ErrPathNotAllowed    = &WorkerError{Code: CodePathNotAllowed}
	ErrFileNotFound      = &WorkerError{Code: CodeFileNotFound}
	ErrFileTooLarge      = &WorkerError{Code: CodeFileTooLarge}
	ErrUnreadableFile    = &WorkerError{Code: CodeUnreadableFile}
	ErrEncrypted         = &WorkerError{Code: CodeEncrypted}
	ErrUnsupportedFormat = &WorkerError{Code: CodeUnsupportedFormat}
	ErrOCRUnavailable    = &WorkerError{Code: CodeOCRUnavailable}
	ErrExtractTimeout    = &WorkerError{Code: CodeExtractTimeout}
	ErrWorkerUnavailable = &WorkerError{Code: CodeWorkerUnavailable}
)

// WorkerExtractor talks to the loopback Python document worker.
type WorkerExtractor struct {
	endpoint string
	token    string
	http     *http.Client
	// MaxResponseBytes caps the response body (default 8 MiB).
	MaxResponseBytes int64
}

// NewWorkerExtractor validates that baseURL is a loopback HTTP origin and that
// token looks like a random secret. Redirects and environment proxies are
// disabled so the bearer token can never leave the loopback interface.
func NewWorkerExtractor(baseURL, token string) (*WorkerExtractor, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, errors.New("invalid worker URL")
	}
	ip, err := netip.ParseAddr(u.Hostname())
	if err != nil || !ip.IsLoopback() || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("worker URL must be a loopback HTTP origin")
	}
	bad := len(token) < 32
	for _, ch := range token {
		if ch < 33 || ch > 126 {
			bad = true
		}
	}
	if bad {
		return nil, errors.New("worker token must contain at least 32 printable ASCII characters")
	}
	return &WorkerExtractor{
		endpoint:         strings.TrimSuffix(baseURL, "/") + "/v1/extract",
		token:            token,
		MaxResponseBytes: 8 << 20,
		http: &http.Client{
			Timeout:       150 * time.Second,
			Transport:     &http.Transport{Proxy: nil, DisableKeepAlives: true},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

// SetTimeout overrides the overall HTTP timeout.
func (w *WorkerExtractor) SetTimeout(d time.Duration) { w.http.Timeout = d }

type workerEnvelope struct {
	Data  *ExtractResult `json:"data"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// Extract implements Extractor.
func (w *WorkerExtractor) Extract(ctx context.Context, req ExtractRequest) (ExtractResult, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return ExtractResult{}, err
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, w.endpoint, bytes.NewReader(body))
	if err != nil {
		return ExtractResult{}, err
	}
	hr.Header.Set("Authorization", "Bearer "+w.token)
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Accept", "application/json")
	res, err := w.http.Do(hr)
	if err != nil {
		if ctx.Err() != nil {
			return ExtractResult{}, ctx.Err()
		}
		// Do not wrap err: its text may contain the URL.
		return ExtractResult{}, &WorkerError{Code: CodeWorkerUnavailable, Message: "worker request failed"}
	}
	defer res.Body.Close()
	limit := w.MaxResponseBytes
	if limit <= 0 {
		limit = 8 << 20
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return ExtractResult{}, &WorkerError{Code: CodeWorkerUnavailable, Status: res.StatusCode, Message: "reading worker response failed"}
	}
	if int64(len(raw)) > limit {
		return ExtractResult{}, &WorkerError{Code: CodeWorkerBadResponse, Status: res.StatusCode, Message: "worker response exceeds size cap"}
	}
	var env workerEnvelope
	jerr := json.Unmarshal(raw, &env)
	if res.StatusCode != http.StatusOK {
		code := ""
		if jerr == nil && env.Error != nil {
			code = env.Error.Code
		}
		if code == "" {
			code = codeFromStatus(res.StatusCode)
		}
		return ExtractResult{}, &WorkerError{Code: code, Status: res.StatusCode, Message: safeMessage(code)}
	}
	if jerr != nil || env.Data == nil {
		return ExtractResult{}, &WorkerError{Code: CodeWorkerBadResponse, Status: res.StatusCode, Message: "malformed worker response"}
	}
	out := *env.Data
	if out.Pages == nil {
		out.Pages = []Page{}
	}
	if out.Warnings == nil {
		out.Warnings = []string{}
	}
	return out, nil
}

func codeFromStatus(s int) string {
	switch s {
	case 400:
		return CodeInvalidRequest
	case 401:
		return CodeUnauthorized
	case 403:
		return CodePathNotAllowed
	case 404:
		return CodeFileNotFound
	case 413:
		return CodeFileTooLarge
	case 422:
		return CodeUnreadableFile
	case 503:
		return CodeOCRUnavailable
	case 504:
		return CodeExtractTimeout
	}
	return CodeWorkerUnavailable
}

// safeMessage returns a fixed message per code so worker text (which could
// echo document content or paths) is never propagated.
func safeMessage(code string) string {
	switch code {
	case CodeUnauthorized:
		return "worker rejected credentials"
	case CodeInvalidRequest:
		return "worker rejected the request"
	case CodePathNotAllowed:
		return "path is outside the worker spool"
	case CodeFileNotFound:
		return "file not found"
	case CodeFileTooLarge:
		return "file too large"
	case CodeUnreadableFile:
		return "file is unreadable or corrupt"
	case CodeEncrypted:
		return "file is encrypted"
	case CodeUnsupportedFormat:
		return "unsupported format"
	case CodeOCRUnavailable:
		return "OCR is not available"
	case CodeExtractTimeout:
		return "extraction timed out"
	}
	return "worker error"
}
