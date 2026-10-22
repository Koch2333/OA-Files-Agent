package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func serveReq(h http.Handler, method, target, origin string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestGinPath(t *testing.T) {
	for in, want := range map[string]string{"/v1/files/{id}/preview": "/v1/files/:id/preview", "/healthz": "/healthz", "/v1/a/{id}": "/v1/a/:id"} {
		if got := ginPath(in); got != want {
			t.Fatalf("%s: %s != %s", in, got, want)
		}
	}
}

func TestPathValueReachesHandler(t *testing.T) {
	s := &Server{}
	s.eng = s.newEngine()
	var got string
	s.handle("/v1/x/{id}/y", map[string]http.HandlerFunc{"GET": func(w http.ResponseWriter, r *http.Request) { got = r.PathValue("id") }})
	s.handle("/v1/x/static", map[string]http.HandlerFunc{"GET": func(w http.ResponseWriter, r *http.Request) { got = "static" }})
	if rec := serveReq(s.eng, "GET", "/v1/x/abc/y", "", nil); rec.Code != 200 || got != "abc" {
		t.Fatalf("code=%d id=%q", rec.Code, got)
	}
	serveReq(s.eng, "GET", "/v1/x/static", "", nil)
	if got != "static" {
		t.Fatalf("static route lost to param route: %q", got)
	}
}

func TestMethodNotAllowedAndNotFound(t *testing.T) {
	h := NewServer(Deps{})
	rec := serveReq(h, "POST", "/healthz", "", nil)
	if rec.Code != 405 || !strings.Contains(rec.Header().Get("Allow"), "GET") || !strings.Contains(rec.Body.String(), "METHOD_NOT_ALLOWED") {
		t.Fatalf("405: %d %v %s", rec.Code, rec.Header(), rec.Body.String())
	}
	rec = serveReq(h, "GET", "/v1/nothing", "", nil)
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "NOT_FOUND") {
		t.Fatalf("404: %d %s", rec.Code, rec.Body.String())
	}
	if rec = serveReq(h, "HEAD", "/healthz", "", nil); rec.Code != 200 {
		t.Fatalf("HEAD falls back to GET: %d", rec.Code)
	}
}

func TestPanicBecomesJSON500(t *testing.T) {
	s := &Server{}
	s.eng = s.newEngine()
	s.handle("/boom", map[string]http.HandlerFunc{"GET": func(http.ResponseWriter, *http.Request) { panic("x") }})
	rec := serveReq(s.eng, "GET", "/boom", "", nil)
	if rec.Code != 500 || !strings.Contains(rec.Body.String(), "INTERNAL") || strings.Contains(rec.Body.String(), "goroutine") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestCORS(t *testing.T) {
	h := NewServer(Deps{CORSOrigins: []string{"http://localhost:5173"}})
	// preflight from an allowed origin
	rec := serveReq(h, "OPTIONS", "/healthz", "http://localhost:5173", map[string]string{"Access-Control-Request-Method": "POST", "Access-Control-Request-Headers": "content-type,idempotency-key"})
	if rec.Code != 204 || rec.Header().Get("Access-Control-Allow-Origin") != "http://localhost:5173" || !strings.Contains(strings.ToLower(rec.Header().Get("Access-Control-Allow-Headers")), "idempotency-key") {
		t.Fatalf("preflight: %d %v", rec.Code, rec.Header())
	}
	if rec.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Fatal("credentials must not be allowed")
	}
	// actual request exposes the request id
	rec = serveReq(h, "GET", "/healthz", "http://localhost:5173", nil)
	if rec.Header().Get("Access-Control-Allow-Origin") != "http://localhost:5173" || !strings.Contains(strings.ToLower(rec.Header().Get("Access-Control-Expose-Headers")), "x-request-id") {
		t.Fatalf("headers: %v", rec.Header())
	}
	// other origins get no CORS headers (and a preflight is refused)
	rec = serveReq(h, "GET", "/healthz", "https://evil.example", nil)
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("evil origin allowed: %v", rec.Header())
	}
	rec = serveReq(h, "OPTIONS", "/healthz", "https://evil.example", map[string]string{"Access-Control-Request-Method": "POST"})
	if rec.Code == 204 || rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("evil preflight: %d %v", rec.Code, rec.Header())
	}
	// no allow-list → no CORS headers at all
	rec = serveReq(NewServer(Deps{}), "GET", "/healthz", "http://localhost:5173", nil)
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("CORS must be off by default")
	}
}

func TestGuardHonoursAllowList(t *testing.T) {
	post := func(h http.Handler, origin string) int {
		req := httptest.NewRequest("POST", "http://127.0.0.1:8080/healthz", nil)
		req.Header.Set("Origin", origin)
		req.Header.Set("Sec-Fetch-Site", "same-site")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	closed := NewServer(Deps{})
	open := NewServer(Deps{CORSOrigins: []string{"http://localhost:5173"}})
	if c := post(closed, "http://localhost:5173"); c != 403 {
		t.Fatalf("without allow-list the cross-site write must be refused: %d", c)
	}
	if c := post(open, "http://localhost:5173"); c == 403 {
		t.Fatalf("allow-listed origin refused: %d", c)
	}
	if c := post(open, "https://evil.example"); c != 403 {
		t.Fatalf("other origin must stay refused: %d", c)
	}
}

func TestNormalizeOrigins(t *testing.T) {
	got, err := NormalizeOrigins([]string{" http://LocalHost:5173/ ", "", "http://localhost:5173", "https://oa.example.com"})
	if err != nil || len(got) != 2 || got[0] != "http://localhost:5173" {
		t.Fatalf("%v %v", got, err)
	}
	for _, bad := range []string{"*", "null", "http://*.example.com", "ftp://x", "http://a/path", "localhost:5173", "http://user@a"} {
		if _, err := NormalizeOrigins([]string{bad}); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
}
