package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

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
}

type ctxKey struct{}

const maxJSONBody = 1 << 20

type Server struct {
	d   Deps
	mux *http.ServeMux
}

// New keeps the M0 signature: health/readiness/docs only.
func New(checks ...DependencyCheck) http.Handler { return NewServer(Deps{Checks: checks}) }

func NewServer(d Deps) http.Handler {
	s := &Server{d: d, mux: http.NewServeMux()}
	s.mux.HandleFunc("/", s.notFound)
	s.handle("/healthz", map[string]http.HandlerFunc{"GET": s.health})
	s.handle("/readyz", map[string]http.HandlerFunc{"GET": s.ready})
	s.handle("/docs", map[string]http.HandlerFunc{"GET": s.docs})
	s.handle("/openapi.yaml", map[string]http.HandlerFunc{"GET": s.spec})
	if d.Store != nil && d.Backend != nil && d.Imports != nil {
		s.business()
	}
	return http.HandlerFunc(s.serve)
}

func (s *Server) handle(pattern string, m map[string]http.HandlerFunc) {
	s.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
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
	})
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
	s.mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, rid)))
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
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	e := map[string]any{"code": code, "message": msg, "request_id": requestID(r)}
	if details != nil {
		e["details"] = details
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"error": e})
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
		log.Printf("internal error (request %s): %v", requestID(r), err)
		s.fail(w, r, 500, "INTERNAL", "内部错误", nil)
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

func actorOf(r *http.Request) string { return "本地用户" } // 认证占位：接入 SSO/RBAC 后替换

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
			go func(index int, check DependencyCheck) { results <- result{index, check(ctx)} }(i, s.d.Checks[i])
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
