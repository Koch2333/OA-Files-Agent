package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"oa-agent/backend/internal/auth"
	"oa-agent/backend/internal/dbtest"
	"oa-agent/backend/internal/secrets"
	"oa-agent/backend/internal/store"
)

func init() { auth.Argon2Params.Memory, auth.Argon2Params.Time = 1024, 1 } // fast tests

const (
	baseURL = "http://localhost:8080"
	adminPW = "Correct-Horse-9-Battery"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type authEnv struct {
	t     *testing.T
	st    *store.Store
	svc   *auth.Service
	srv   *Server
	h     http.Handler
	clock *fakeClock
}

// newAuthEnv builds a server with only the auth routes on an isolated schema of the test database.
func newAuthEnv(t *testing.T) *authEnv {
	t.Helper()
	st := dbtest.New(t)
	box, err := secrets.Open("", filepath.Join(t.TempDir(), "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	svc := auth.New(st, box)
	clock := &fakeClock{t: time.Now()}
	svc.SetClock(clock.Now)
	srv := newServer(Deps{Store: st, Auth: svc})
	return &authEnv{t: t, st: st, svc: svc, srv: srv, h: http.HandlerFunc(srv.serve), clock: clock}
}

type resp struct {
	Code   int
	Header http.Header
	Raw    []byte
	Data   map[string]any
	Any    any
	Err    map[string]any
}

func (r resp) code() string {
	if r.Err == nil {
		return ""
	}
	s, _ := r.Err["code"].(string)
	return s
}

func (r resp) str(k string) string { s, _ := r.Data[k].(string); return s }

type tclient struct {
	env    *authEnv
	cookie string
	csrf   string
	remote string
	host   string
	hdr    map[string]string
	tls    bool
	bodies *[][]byte
}

func (e *authEnv) client() *tclient {
	return &tclient{env: e, remote: "192.0.2.10:5555", host: "localhost:8080", hdr: map[string]string{}, bodies: &[][]byte{}}
}

func (c *tclient) clone() *tclient { n := *c; n.hdr = map[string]string{}; return &n }

func (c *tclient) do(method, path string, body any) resp {
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	scheme := "http"
	if c.tls {
		scheme = "https"
	}
	req := httptest.NewRequest(method, scheme+"://"+c.host+path, rd)
	req.RemoteAddr = c.remote
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.cookie != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: c.cookie})
	}
	if c.csrf != "" && method != "GET" && method != "HEAD" {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	for k, v := range c.hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	c.env.h.ServeHTTP(rec, req)
	out := resp{Code: rec.Code, Header: rec.Header(), Raw: rec.Body.Bytes()}
	*c.bodies = append(*c.bodies, out.Raw)
	var env struct {
		Data  json.RawMessage `json:"data"`
		Error map[string]any  `json:"error"`
	}
	if json.Unmarshal(out.Raw, &env) == nil {
		out.Err = env.Error
		_ = json.Unmarshal(env.Data, &out.Data)
		_ = json.Unmarshal(env.Data, &out.Any)
	}
	for _, sc := range rec.Result().Cookies() {
		if sc.Name == sessionCookie {
			if sc.Value == "" || sc.MaxAge < 0 {
				c.cookie, c.csrf = "", ""
			} else {
				c.cookie = sc.Value
			}
		}
	}
	if t, ok := out.Data["csrf_token"].(string); ok && t != "" {
		c.csrf = t
	}
	return out
}

func (c *tclient) must(method, path string, body any, want int) resp {
	c.env.t.Helper()
	r := c.do(method, path, body)
	if r.Code != want {
		c.env.t.Fatalf("%s %s: status %d (want %d): %s", method, path, r.Code, want, r.Raw)
	}
	return r
}

// setup completes the wizard (enterprise mode, MFA policy off unless the extra fields say otherwise) and returns the admin client.
func (e *authEnv) setup(extra map[string]any) *tclient {
	e.t.Helper()
	tok, err := e.svc.EnsureSetupToken(context.Background())
	if err != nil || tok == "" {
		e.t.Fatalf("setup token: %q %v", tok, err)
	}
	body := map[string]any{"setup_token": tok, "mode": "enterprise", "mfa_policy": "off",
		"admin": map[string]any{"username": "admin", "display_name": "管理员", "password": adminPW}}
	for k, v := range extra {
		body[k] = v
	}
	c := e.client()
	c.must("POST", "/v1/setup/complete", body, 201)
	if me := c.do("GET", "/v1/auth/me", nil); me.Data["scope"] == "full" {
		c.must("POST", "/v1/setup/finish", nil, 200) // the wizard gate: tests that are not about the wizard finish it
	}
	return c
}

// user creates a user through the admin API and returns a logged-in client (temporary password already replaced).
func (e *authEnv) user(admin *tclient, name, role string) *tclient {
	e.t.Helper()
	r := admin.must("POST", "/v1/admin/users", map[string]any{"username": name, "display_name": name, "role": role, "credential": "password"}, 201)
	tmp, _ := r.Data["temporary_password"].(string)
	c := e.client()
	c.must("POST", "/v1/auth/login", map[string]any{"username": name, "password": tmp}, 200)
	c.must("POST", "/v1/auth/password", map[string]any{"current_password": tmp, "new_password": adminPW}, 204)
	c.cookie, c.csrf = "", ""
	c.must("POST", "/v1/auth/login", map[string]any{"username": name, "password": adminPW}, 200)
	return c
}

func (e *authEnv) login(name, pw string) (*tclient, resp) {
	c := e.client()
	return c, c.do("POST", "/v1/auth/login", map[string]any{"username": name, "password": pw})
}

func (e *authEnv) auditActions() map[string]int {
	list, _, err := e.st.AuditLogs(context.Background(), store.Page{Page: 1, Size: 200})
	if err != nil {
		e.t.Fatal(err)
	}
	out := map[string]int{}
	for _, a := range list {
		out[a.Action]++
	}
	return out
}

func assertCode(t *testing.T, r resp, status int, code string) {
	t.Helper()
	if r.Code != status || r.code() != code {
		t.Fatalf("got %d %q (%s), want %d %q", r.Code, r.code(), r.Raw, status, code)
	}
}

func contains(b []byte, s string) bool { return strings.Contains(string(b), s) }
