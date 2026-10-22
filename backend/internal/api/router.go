package api

import (
	"fmt"
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"

	"oa-agent/backend/internal/diag"
)

// newEngine builds the Gin engine used as the router. Handlers stay plain net/http handlers (path parameters are
// exposed through Request.PathValue), so Gin only replaces routing, panic recovery and CORS.
func (s *Server) newEngine() *gin.Engine {
	gin.SetMode(gin.ReleaseMode) // Gin's own banner/route dump is noise; request logging lives in debuglog.go
	e := gin.New()
	e.RedirectTrailingSlash = false
	e.RedirectFixedPath = false
	e.HandleMethodNotAllowed = false // s.handle answers 405 itself with the contract's error body
	e.UseRawPath = false
	e.Use(s.recovery())
	if len(s.d.CORSOrigins) > 0 {
		e.Use(corsMiddleware(s.d.CORSOrigins))
	}
	e.Use(s.authMiddleware())
	e.NoRoute(func(c *gin.Context) { s.notFound(c.Writer, c.Request) })
	return e
}

func (s *Server) recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				details := s.recordPanic(c, v)
				if !c.Writer.Written() {
					s.writeFail(c.Writer, c.Request, 500, "INTERNAL", "服务内部错误", details)
				}
				c.Abort()
			}
		}()
		c.Next()
	}
}

// ginPath converts a net/http pattern ("/v1/files/{id}") to Gin syntax ("/v1/files/:id").
func ginPath(pattern string) string {
	var b strings.Builder
	for i := 0; i < len(pattern); i++ {
		if pattern[i] == '{' {
			j := strings.IndexByte(pattern[i:], '}')
			if j > 0 {
				b.WriteByte(':')
				b.WriteString(pattern[i+1 : i+j])
				i += j
				continue
			}
		}
		b.WriteByte(pattern[i])
	}
	return b.String()
}

// wrap adapts a net/http handler, copying Gin path parameters into Request.PathValue.
func wrap(h http.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		for _, p := range c.Params {
			c.Request.SetPathValue(p.Key, p.Value)
		}
		c.Request.SetPathValue("_route", c.FullPath()) // read by routeOf (error reports group by route pattern)
		h(c.Writer, c.Request)
	}
}

// NormalizeOrigins validates a CORS allow-list: exact origins only (scheme://host[:port]); "*" and "null" are refused
// because the API has no per-user authentication and must not be readable from arbitrary pages.
func NormalizeOrigins(in []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, raw := range in {
		o := strings.TrimRight(strings.TrimSpace(raw), "/")
		if o == "" {
			continue
		}
		u, err := url.Parse(o)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || strings.Contains(o, "*") {
			return nil, &originError{raw}
		}
		o = u.Scheme + "://" + strings.ToLower(u.Host)
		if !seen[o] {
			seen[o] = true
			out = append(out, o)
		}
	}
	return out, nil
}

type originError struct{ v string }

func (e *originError) Error() string {
	return "invalid CORS origin " + e.v + " (use exact origins such as http://localhost:5173; wildcards are not allowed)"
}

func corsMiddleware(origins []string) gin.HandlerFunc {
	return cors.New(cors.Config{
		AllowOrigins:     origins,
		AllowMethods:     []string{"GET", "HEAD", "POST", "PATCH", "PUT", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Content-Type", "Accept", "Idempotency-Key", "Last-Event-ID", "Range", "If-None-Match", "X-CSRF-Token"},
		ExposeHeaders:    []string{"X-Request-ID", "Content-Disposition", "Content-Range", "Content-Length", "ETag", "Retry-After"},
		AllowCredentials: false,
		MaxAge:           10 * time.Minute,
	})
}

// corsAllowed reports whether the request's Origin is on the allow-list (used by the cross-site guard).
func (s *Server) corsAllowed(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return false
	}
	for _, a := range s.d.CORSOrigins {
		if strings.EqualFold(a, o) {
			return true
		}
	}
	return false
}

// recordPanic stores a handler panic as a severity=panic report (request id, route, user, stack; never the body) and
// returns the error details for the 500 response.
func (s *Server) recordPanic(c *gin.Context, v any) map[string]any {
	r := c.Request
	route := c.FullPath()
	if route == "" {
		route = r.URL.Path
	}
	user := ""
	if id, ok := identityOf(r); ok {
		user = id.User.Username
	}
	id := diag.Report(r.Context(), diag.Entry{Source: "api", Severity: "panic", Code: "PANIC", RequestID: requestID(r), User: user,
		Message: fmt.Sprintf("panic in %s %s: %v", r.Method, route, v), Stack: string(debug.Stack()),
		Details: map[string]any{"route": route, "method": r.Method}})
	d := map[string]any{}
	if id != "" {
		d["report_id"] = id
	}
	return d
}
