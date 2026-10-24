package api

import (
	"net/http"
	"sort"
	"strings"
	"testing"

	"oa-agent/backend/internal/auth"
	"oa-agent/backend/internal/imports"
	"oa-agent/backend/internal/store"
)

type stubBackend struct{ Backend }
type stubFiles struct{ FileManager }
type stubLLM struct{ LLMManager }
type stubPreview struct{ Previewer }
type stubStorage struct{ StorageManager }
type stubDBAdmin struct{ DatabaseAdmin }

// fullyWired registers every optional route group; handlers are never called.
func fullyWired(extra Deps) *Server {
	d := extra
	d.Store, d.Backend, d.Imports = extra.Store, stubBackend{}, &imports.Service{}
	d.Files, d.LLM, d.Preview, d.Storage, d.DBAdmin = stubFiles{}, stubLLM{}, stubPreview{}, stubStorage{}, stubDBAdmin{}
	if d.Store == nil {
		d.Store = &store.Store{}
	}
	return newServer(d)
}

func TestEveryRouteHasAnAccessRule(t *testing.T) {
	e := fullyWiredAuth(t, false)
	n := 0
	for pattern, methods := range e.routes {
		for m := range methods {
			n++
			if _, ok := routeAccess[m+" "+pattern]; !ok {
				t.Errorf("route %s %s has no entry in routeAccess (default deny): add it to authz.go", m, pattern)
			}
		}
	}
	for key := range routeAccess {
		sp := strings.IndexByte(key, ' ')
		if !e.routes[key[sp+1:]][key[:sp]] {
			t.Errorf("routeAccess entry %q matches no registered route", key)
		}
	}
	if n < 100 {
		t.Fatalf("only %d routes registered; the server is not fully wired", n)
	}
}

func fullyWiredAuth(t *testing.T, db bool) *Server {
	if !db {
		return fullyWired(Deps{Auth: auth.New(&store.Store{}, nil)}) // route registration only
	}
	e := newAuthEnv(t)
	return fullyWired(Deps{Store: e.st, Auth: e.svc})
}

// stubServer serves every route of the fully wired server with a trivial 200 handler, behind the real middleware.
func stubServer(t *testing.T, e *authEnv, keepReal ...string) {
	t.Helper()
	wired := fullyWired(Deps{Store: e.st, Auth: e.svc})
	// the auth service is set so the auth routes register too
	ts := &Server{d: Deps{Auth: e.svc, Store: e.st}}
	ts.eng = ts.newEngine()
	if len(keepReal) > 0 { // the real setup and auth handlers (for tests of the wizard gate)
		ts.setupRoutes()
		ts.authRoutes()
	}
	ok := func(w http.ResponseWriter, r *http.Request) { ts.ok(w, 200, map[string]any{"ok": true}) }
	for pattern, methods := range wired.routes {
		if ts.routes[pattern] != nil {
			continue
		}
		m := map[string]http.HandlerFunc{}
		for method := range methods {
			m[method] = ok
		}
		ts.handle(pattern, m)
	}
	e.srv, e.h = ts, http.HandlerFunc(ts.serve)
}

func concrete(pattern string) string {
	parts := strings.Split(pattern, "/")
	for i, p := range parts {
		if strings.HasPrefix(p, ":") {
			parts[i] = "00000000-0000-4000-8000-000000000001"
		}
	}
	return strings.Join(parts, "/")
}

func TestRoleMatrixOverEveryRoute(t *testing.T) {
	e := newAuthEnv(t)
	admin := e.setup(nil)
	viewer, operator := e.user(admin, "vera", "viewer"), e.user(admin, "olga", "operator")
	stubServer(t, e)
	anon := e.client()
	clients := map[string]*tclient{"viewer": viewer, "operator": operator, "admin": admin}
	rank := map[string]int{"viewer": 1, "operator": 2, "admin": 3}
	var keys []string
	for k := range routeAccess {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		sp := strings.IndexByte(key, ' ')
		method, pattern := key[:sp], key[sp+1:]
		path := concrete(pattern)
		need := routeAccess[key]
		// anonymous
		r := anon.do(method, path, nil)
		if need == accessPublic {
			if r.Code != 200 {
				t.Errorf("%s %s anonymous: %d", method, path, r.Code)
			}
		} else if r.Code != 401 || r.code() != "UNAUTHENTICATED" {
			t.Errorf("%s %s anonymous: want 401, got %d %s", method, path, r.Code, r.Raw)
		}
		for role, c := range clients {
			r := c.do(method, path, nil)
			allowed := need == accessPublic || rank[role] >= int(need)
			switch {
			case allowed && r.Code != 200:
				t.Errorf("%s %s as %s: want 200, got %d %s", method, path, role, r.Code, r.Raw)
			case !allowed && (r.Code != 403 || r.code() != "FORBIDDEN"):
				t.Errorf("%s %s as %s: want 403 FORBIDDEN, got %d %s", method, path, role, r.Code, r.Raw)
			}
		}
		// writes without the CSRF token are refused for signed-in users on protected routes
		if need != accessPublic && method != "GET" {
			saved := admin.csrf
			admin.csrf = ""
			if r := admin.do(method, path, nil); r.Code != 403 || r.code() != "CSRF_INVALID" {
				t.Errorf("%s %s without CSRF: %d %s", method, path, r.Code, r.Raw)
			}
			admin.csrf = saved
		}
	}
	// unknown API paths are protected too (no information for anonymous callers)
	assertCode(t, anon.do("GET", "/v1/definitely/not/here", nil), 401, "UNAUTHENTICATED")
	if r := admin.do("GET", "/v1/definitely/not/here", nil); r.Code != 404 {
		t.Fatalf("%d", r.Code)
	}
	// dashboard reads must work for viewers and operators
	for _, p := range []string{"/v1/providers", "/v1/tasks", "/v1/ledger", "/readyz"} {
		for _, c := range []*tclient{viewer, operator} {
			if r := c.do("GET", p, nil); r.Code != 200 {
				t.Errorf("GET %s: %d", p, r.Code)
			}
		}
	}
}

func TestRestrictedSessionsAndTemporaryPasswords(t *testing.T) {
	e := newAuthEnv(t)
	admin := e.setup(map[string]any{"mfa_policy": "admins"})
	if admin.do("GET", "/v1/auth/me", nil).Data["scope"] != "enroll_mfa" {
		t.Fatal("admin without MFA must get an enrolment session under policy admins")
	}
	stubServer(t, e)
	for _, k := range []string{"GET /v1/admin/users", "POST /v1/auth/reauth", "GET /v1/auth/sessions", "GET /v1/tasks", "DELETE /v1/auth/totp"} {
		sp := strings.IndexByte(k, ' ')
		r := admin.do(k[:sp], k[sp+1:], nil)
		if r.Code != 403 || r.Err["details"].(map[string]any)["reason"] != "enroll_mfa" {
			t.Errorf("%s with scope enroll_mfa: %d %s", k, r.Code, r.Raw)
		}
	}
	for _, k := range []string{"GET /v1/auth/me", "GET /v1/auth/security", "POST /v1/auth/totp/begin", "POST /v1/auth/passkeys/begin", "POST /v1/auth/logout"} {
		sp := strings.IndexByte(k, ' ')
		if r := admin.do(k[:sp], k[sp+1:], nil); r.Code != 200 {
			t.Errorf("%s with scope enroll_mfa: %d %s", k, r.Code, r.Raw)
		}
	}
	// a temporary password confines the session to the password-change screen
	e2 := newAuthEnv(t)
	adm := e2.setup(nil)
	r := adm.must("POST", "/v1/admin/users", map[string]any{"username": "tempuser", "role": "viewer", "credential": "password"}, 201)
	c, lr := e2.login("tempuser", r.str("temporary_password"))
	if lr.Code != 200 || lr.Data["user"].(map[string]any)["must_change_password"] != true {
		t.Fatalf("%s", lr.Raw)
	}
	stubServer(t, e2)
	gated := c.do("GET", "/v1/tasks", nil)
	if gated.Code != 403 || gated.Err["details"].(map[string]any)["reason"] != "must_change_password" {
		t.Fatalf("%d %s", gated.Code, gated.Raw)
	}
	c.must("GET", "/v1/auth/me", nil, 200)
}
