package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"oa-agent/backend/internal/auth"
	"oa-agent/backend/internal/diag"
	"oa-agent/backend/internal/imports"
	"oa-agent/backend/internal/store"
)

type DependencyCheck func(context.Context) string

// TemplateTestResult is the outcome of running a template against a sample file.
type TemplateTestResult struct {
	Matched      bool     `json:"matched"`
	Department   *string  `json:"department"`
	DocumentType *string  `json:"document_type"`
	DocumentDate *string  `json:"document_date"`
	Evidence     []string `json:"evidence"`
}

type ProviderStatus struct {
	Name  string `json:"name"`
	Model string `json:"model"`
	State string `json:"state"`
}

// Backend is the business logic the HTTP layer delegates to (implemented by pipeline.Service).
type Backend interface {
	Derive(ctx context.Context, it store.PlanItem) (name, dest string, warnings []string, err error)
	ExecutePlan(ctx context.Context, planID, key, actor string) (store.Plan, error)
	Rollback(ctx context.Context, opID, actor string) error
	TestTemplate(ctx context.Context, t store.Template, filename string, content io.Reader) (TemplateTestResult, error)
	ProposeCorrection(ctx context.Context, fileID, instruction string) (store.Correction, error)
	ValidateRule(r store.Rule) error
	Providers() []ProviderStatus
}

type Deps struct {
	Checks  []DependencyCheck // database, then worker
	Store   *store.Store
	Imports *imports.Service
	Backend Backend
	// StaticDir, when set, serves the built frontend (SPA fallback to index.html) for non-API GET requests.
	StaticDir string
	// Files enables the file-manager routes (/v1/fs/*).
	Files FileManager
	// LLM enables the recognition-engine settings routes (/v1/llm/*).
	LLM LLMManager
	// Preview enables the in-app document preview routes.
	Preview Previewer
	// Storage enables the optional storage-connection routes (/v1/storage/*, /v1/imports/remote).
	Storage StorageManager
	// StrictLocal rejects requests whose Host header is not a loopback name (DNS-rebinding defence); set when
	// the server listens on a loopback address. AllowedHosts adds extra accepted host names.
	StrictLocal  bool
	AllowedHosts []string
	// Auth enables login, sessions, roles, the setup wizard and the admin-user API. nil means no authentication at all
	// (unit tests only; cmd/oa-agent always wires it when a database is configured).
	Auth *auth.Service
	// TrustedProxy (OA_TRUSTED_PROXY=1) makes X-Forwarded-For / X-Forwarded-Proto count for client address and Secure cookies.
	TrustedProxy bool
	// PublicURL (OA_PUBLIC_URL) is the externally reachable origin; the LLM login callback is built from it outside local mode.
	PublicURL string
	// CORSOrigins is the exact-origin allow-list for browser pages served from another origin (e.g. the Vite dev server).
	// Empty means same-origin only: no CORS headers are sent. Validate with NormalizeOrigins.
	CORSOrigins []string
	// Diag carries process facts for the diagnostics routes (/v1/admin/system, the diagnostic bundle).
	Diag DiagInfo
	// DBAdmin enables the database administration routes (/v1/admin/database*).
	DBAdmin DatabaseAdmin
}

type ctxKey struct{}

const maxJSONBody = 1 << 20

type Server struct {
	d      Deps
	eng    *gin.Engine
	routes map[string]map[string]bool // Gin pattern → registered methods (authz and the route-table test)

	clientMu  sync.Mutex
	clientLim *diag.Limiter
}

// New keeps the M0 signature: health/readiness/docs only.
func New(checks ...DependencyCheck) http.Handler { return NewServer(Deps{Checks: checks}) }

func NewServer(d Deps) http.Handler {
	s := newServer(d)
	return http.HandlerFunc(debugRequests(s.serve))
}

func newServer(d Deps) *Server {
	s := &Server{d: d, routes: map[string]map[string]bool{}}
	s.eng = s.newEngine()
	s.handle("/healthz", map[string]http.HandlerFunc{"GET": s.health})
	s.handle("/readyz", map[string]http.HandlerFunc{"GET": s.ready})
	s.handle("/docs", map[string]http.HandlerFunc{"GET": s.docs})
	s.handle("/openapi.yaml", map[string]http.HandlerFunc{"GET": s.spec})
	if d.Store != nil && d.Backend != nil && d.Imports != nil {
		s.business()
	}
	if d.Files != nil {
		s.fsRoutes()
	}
	if d.LLM != nil && d.Store != nil {
		s.llmRoutes()
	}
	if d.Preview != nil {
		s.previewRoutes()
	}
	if d.Storage != nil && d.Store != nil {
		s.storageRoutes()
	}
	if d.Auth != nil && d.Store != nil {
		s.setupRoutes()
		s.authRoutes()
		s.adminUserRoutes()
		s.diagRoutes()
		s.faultRoutes()
		if d.DBAdmin != nil {
			s.databaseRoutes()
		}
	}
	return s
}

func (s *Server) handle(pattern string, m map[string]http.HandlerFunc) {
	gp := ginPath(pattern)
	if s.routes == nil {
		s.routes = map[string]map[string]bool{}
	}
	if s.routes[gp] == nil {
		s.routes[gp] = map[string]bool{}
	}
	for method := range m {
		s.routes[gp][method] = true
	}
	s.eng.Any(ginPath(pattern), wrap(func(w http.ResponseWriter, r *http.Request) {
		h, ok := m[r.Method]
		if !ok && r.Method == http.MethodHead {
			h, ok = m[http.MethodGet]
		}
		if !ok {
			allow := make([]string, 0, len(m))
			for k := range m {
				allow = append(allow, k)
			}
			sortStrings(allow)
			w.Header().Set("Allow", strings.Join(allow, ", "))
			s.fail(w, r, 405, "METHOD_NOT_ALLOWED", "仅支持 "+strings.Join(allow, "/"), nil)
			return
		}
		h(w, r)
	}))
}

func sortStrings(a []string) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		http.Error(w, "request ID unavailable", 500)
		return
	}
	rid := hex.EncodeToString(id[:])
	w.Header().Set("X-Request-ID", rid)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, rid))
	if !s.guard(w, r) {
		return
	}
	s.eng.ServeHTTP(w, r)
}

func requestID(r *http.Request) string { v, _ := r.Context().Value(ctxKey{}).(string); return v }

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	if s.d.StaticDir != "" && (r.Method == http.MethodGet || r.Method == http.MethodHead) && !strings.HasPrefix(r.URL.Path, "/v1/") {
		s.static(w, r)
		return
	}
	s.fail(w, r, 404, "NOT_FOUND", "接口不存在或尚未实现", nil)
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, status int, code, msg string, details map[string]any) {
	if status >= 500 && code != "SETUP_REQUIRED" && code != "NOT_SUPPORTED" {
		details = s.recordServerError(r, status, code, msg, "", details)
	}
	s.writeFail(w, r, status, code, msg, details)
}

func (s *Server) writeFail(w http.ResponseWriter, r *http.Request, status int, code, msg string, details map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	e := map[string]any{"code": code, "message": msg, "request_id": requestID(r)}
	if details != nil {
		e["details"] = details
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"error": e})
}

// internal answers 500 INTERNAL and records the underlying error (redacted, with the route) as an error report; the
// response carries the request id and, in details, the report id.
func (s *Server) internal(w http.ResponseWriter, r *http.Request, public string, err error) {
	cause := ""
	if err != nil {
		cause = err.Error()
	}
	s.writeFail(w, r, 500, "INTERNAL", public, s.recordServerError(r, 500, "INTERNAL", public, cause, nil))
}

// routeOf is the matched route pattern ("/v1/files/:id"), or the raw path when no route matched.
func routeOf(r *http.Request) string {
	if v := r.PathValue("_route"); v != "" {
		return v
	}
	return r.URL.Path
}

// recordServerError stores a 5xx as an error report (grouped per route + code + message) and returns details with the
// report id added. Reports are coalesced and rate limited inside diag, so a failing hot path cannot flood the database.
func (s *Server) recordServerError(r *http.Request, status int, code, msg, cause string, details map[string]any) map[string]any {
	source := "api"
	switch code {
	case "UPSTREAM_ERROR":
		source = "storage"
		if strings.HasPrefix(r.URL.Path, "/v1/llm") {
			source = "llm"
		}
	case "PREVIEW_UNAVAILABLE":
		source = "worker"
	}
	text := fmt.Sprintf("%s %s → %d %s: %s", r.Method, routeOf(r), status, code, msg)
	if cause != "" {
		text += ": " + cause
	}
	user := ""
	if id, ok := identityOf(r); ok {
		user = id.User.Username
	}
	id := diag.Report(r.Context(), diag.Entry{Source: source, Code: code, Message: text, RequestID: requestID(r), User: user,
		Details: map[string]any{"route": routeOf(r), "method": r.Method, "status": status}})
	out := make(map[string]any, len(details)+1)
	for k, v := range details {
		out[k] = v
	}
	if id != "" {
		out["report_id"] = id
	}
	return out
}

func (s *Server) ok(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

func (s *Server) list(w http.ResponseWriter, items any, total, page, size int) {
	s.ok(w, 200, map[string]any{"items": items, "total": total, "page": page, "page_size": size})
}

// storeErr maps repository errors to contract error responses.
func (s *Server) storeErr(w http.ResponseWriter, r *http.Request, err error, what string) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.fail(w, r, 404, "NOT_FOUND", what+"不存在", nil)
	case errors.Is(err, store.ErrConflict):
		s.fail(w, r, 409, "VERSION_CONFLICT", "已被他人修改或存在冲突，请刷新后重试", nil)
	case errors.Is(err, store.ErrReviewRequired):
		s.fail(w, r, 409, "REVIEW_REQUIRED", "仍有待人工确认的条目，请先修订", nil)
	case errors.Is(err, store.ErrState):
		s.fail(w, r, 409, "INVALID_STATE", "当前状态不允许此操作", nil)
	case errors.Is(err, context.Canceled):
		s.fail(w, r, 499, "CANCELED", "请求已取消", nil)
	default:
		s.internal(w, r, "内部错误", err)
	}
}

func (s *Server) decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			s.fail(w, r, 413, "BODY_TOO_LARGE", "请求体过大", nil)
		} else {
			s.fail(w, r, 400, "INVALID_JSON", "请求体不是有效的 JSON 或含未知字段", nil)
		}
		return false
	}
	return true
}

func pageParams(r *http.Request) (int, int) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	size, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 20
	}
	if size > 200 {
		size = 200
	}
	return page, size
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	s.ok(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) docs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(docsPage)
}

func (s *Server) spec(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	_, _ = w.Write(openAPI)
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	type result struct {
		index  int
		status string
	}
	results := make(chan result, 2)
	states := []string{"not_configured", "not_configured"}
	pending := 0
	for i := 0; i < 2 && i < len(s.d.Checks); i++ {
		if s.d.Checks[i] != nil {
			pending++
			states[i] = "timeout"
			go func(index int, check DependencyCheck) {
				st := "error"
				diag.Safe("api.ready_check", func() { st = check(ctx) })
				results <- result{index, st}
			}(i, s.d.Checks[i])
		}
	}
	for pending > 0 {
		select {
		case res := <-results:
			states[res.index] = res.status
			pending--
		case <-ctx.Done():
			pending = 0
		}
	}
	status, code := "not_ready", http.StatusServiceUnavailable
	if states[0] == "ok" && states[1] == "ok" {
		status, code = "ready", http.StatusOK
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"status": status, "dependencies": map[string]string{"database": states[0], "worker": states[1]}}})
}
