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

const defaultRenderBytes = 4 << 20

// WorkerExtractor talks to the loopback Python document worker.
type WorkerExtractor struct {
	endpoint       string
	token          string
	http           *http.Client
	renderEndpoint string
	// MaxRenderBytes caps the render response body (default 4 MiB: a base64
	// image of up to 2.5 MB encoded plus the JSON envelope).
	MaxRenderBytes int64
	// MaxResponseBytes caps the extract response body (default 8 MiB).
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
		renderEndpoint:   strings.TrimSuffix(baseURL, "/") + "/v1/render",
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

type workerError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type workerEnvelope struct {
	Data  *ExtractResult `json:"data"`
	Error *workerError   `json:"error"`
}

type renderEnvelope struct {
	Data  *RenderResult `json:"data"`
	Error *workerError  `json:"error"`
}

// post sends one authenticated JSON request and returns the raw response
// body and status. Transport failures and oversize bodies are typed
// *WorkerError values (or the context error); worker text is never propagated.
func (w *WorkerExtractor) post(ctx context.Context, endpoint string, req any, limit int64) ([]byte, int, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, 0, err
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	hr.Header.Set("Authorization", "Bearer "+w.token)
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Accept", "application/json")
	res, err := w.http.Do(hr)
	if err != nil {
		if ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		// Do not wrap err: its text may contain the URL.
		return nil, 0, &WorkerError{Code: CodeWorkerUnavailable, Message: "worker request failed"}
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, 0, &WorkerError{Code: CodeWorkerUnavailable, Status: res.StatusCode, Message: "reading worker response failed"}
	}
	if int64(len(raw)) > limit {
		return nil, 0, &WorkerError{Code: CodeWorkerBadResponse, Status: res.StatusCode, Message: "worker response exceeds size cap"}
	}
	return raw, res.StatusCode, nil
}

// statusError maps a non-200 response to a typed error.
func statusError(status int, we *workerError) error {
	code := ""
	if we != nil {
		code = we.Code
	}
	if code == "" {
		code = codeFromStatus(status)
	}
	return &WorkerError{Code: code, Status: status, Message: safeMessage(code)}
}

// Extract implements Extractor.
func (w *WorkerExtractor) Extract(ctx context.Context, req ExtractRequest) (ExtractResult, error) {
	limit := w.MaxResponseBytes
	if limit <= 0 {
		limit = 8 << 20
	}
	raw, status, err := w.post(ctx, w.endpoint, req, limit)
	if err != nil {
		return ExtractResult{}, err
	}
	var env workerEnvelope
	jerr := json.Unmarshal(raw, &env)
	if status != http.StatusOK {
		if jerr != nil {
			env.Error = nil
		}
		return ExtractResult{}, statusError(status, env.Error)
	}
	if jerr != nil || env.Data == nil {
		return ExtractResult{}, &WorkerError{Code: CodeWorkerBadResponse, Status: status, Message: "malformed worker response"}
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

// Render implements PageRenderer (POST /v1/render). The response carries a
// base64 image of up to ~2.5 MB encoded, so it has its own, larger body cap.
func (w *WorkerExtractor) Render(ctx context.Context, req RenderRequest) (RenderResult, error) {
	limit := w.MaxRenderBytes
	if limit <= 0 {
		limit = defaultRenderBytes
	}
	raw, status, err := w.post(ctx, w.renderEndpoint, struct {
		Path    string `json:"path"`
		Page    int    `json:"page"`
		MaxSide int    `json:"max_side"`
	}{req.Path, req.Page, req.MaxSide}, limit)
	if err != nil {
		return RenderResult{}, err
	}
	var env renderEnvelope
	jerr := json.Unmarshal(raw, &env)
	if status != http.StatusOK {
		if jerr != nil {
			env.Error = nil
		}
		return RenderResult{}, statusError(status, env.Error)
	}
	if jerr != nil || env.Data == nil || env.Data.ImageBase64 == "" {
		return RenderResult{}, &WorkerError{Code: CodeWorkerBadResponse, Status: status, Message: "malformed worker response"}
	}
	return *env.Data, nil
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
