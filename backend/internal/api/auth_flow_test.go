package api

import (
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"oa-agent/backend/internal/auth"
	"oa-agent/backend/internal/store"
)

func TestSetupGateBlocksEverythingElseUntilInitialised(t *testing.T) {
	e := newAuthEnv(t)
	c := e.client()
	for _, tc := range [][2]string{{"GET", "/v1/auth/me"}, {"POST", "/v1/auth/login"}, {"GET", "/v1/admin/users"}, {"GET", "/v1/anything-else"}, {"POST", "/v1/auth/mfa/totp"}} {
		assertCode(t, c.do(tc[0], tc[1], nil), 503, "SETUP_REQUIRED")
	}
	if r := c.do("GET", "/healthz", nil); r.Code != 200 {
		t.Fatalf("healthz: %d", r.Code)
	}
	r := c.must("GET", "/v1/setup/status", nil, 200)
	if r.Data["initialized"] != false || r.Data["loopback"] != false || r.Data["needs_token"] != true || r.Data["mode"] != nil {
		t.Fatalf("%v", r.Data)
	}
	if r.Data["rp_id_suggestion"] != "localhost" || r.Data["origin_suggestion"] != "http://localhost:8080" || r.Data["passkey_available"] != true {
		t.Fatalf("suggestions: %v", r.Data)
	}
	if len(r.Data["checks"].([]any)) < 3 {
		t.Fatal("checks missing")
	}
	// a server reached by IP address over plain http cannot do passkeys
	ip := e.client()
	ip.host = "10.1.2.3:8080"
	if r := ip.must("GET", "/v1/setup/status", nil, 200); r.Data["passkey_available"] != false || r.Data["https"] != false {
		t.Fatalf("%v", r.Data)
	}
	// behind a trusted proxy that terminates TLS
	e.srv.d.TrustedProxy = true
	px := e.client()
	px.host, px.hdr = "oa.example.com", map[string]string{"X-Forwarded-Proto": "https"}
	if r := px.must("GET", "/v1/setup/status", nil, 200); r.Data["passkey_available"] != true || r.Data["https"] != true || r.Data["rp_id_suggestion"] != "oa.example.com" || r.Data["origin_suggestion"] != "https://oa.example.com" {
		t.Fatalf("%v", r.Data)
	}
}

func TestSetupTokenAndValidation(t *testing.T) {
	e := newAuthEnv(t)
	e.svc.SetupLimiter = auth.NewLimiter(1000, time.Second, e.clock.Now)
	tok, _ := e.svc.EnsureSetupToken(t.Context())
	tok2, _ := e.svc.EnsureSetupToken(t.Context()) // every start issues a fresh token
	if tok == tok2 || tok == "" {
		t.Fatal("token must be regenerated")
	}
	c := e.client()
	assertCode(t, c.do("POST", "/v1/setup/verify-token", map[string]any{"token": tok}), 401, "INVALID_SETUP_TOKEN") // the old one is dead
	assertCode(t, c.do("POST", "/v1/setup/verify-token", map[string]any{"token": "nope"}), 401, "INVALID_SETUP_TOKEN")
	if r := c.must("POST", "/v1/setup/verify-token", map[string]any{"token": strings.ToLower(tok2[:6]) + "-" + tok2[6:]}, 200); r.Data["valid"] != true {
		t.Fatal("token should tolerate case and dashes")
	}
	admin := map[string]any{"username": "admin", "display_name": "A", "password": adminPW}
	post := func(body map[string]any) resp { return c.do("POST", "/v1/setup/complete", body) }
	assertCode(t, post(map[string]any{"mode": "enterprise", "admin": admin}), 401, "INVALID_SETUP_TOKEN")                       // remote caller without a token
	assertCode(t, post(map[string]any{"mode": "enterprise", "setup_token": "bad", "admin": admin}), 401, "INVALID_SETUP_TOKEN") // wrong token
	assertCode(t, post(map[string]any{"mode": "local", "setup_token": tok2}), 403, "LOCAL_ONLY")                                // local mode only from the machine itself
	weak := map[string]any{"username": "admin", "display_name": "A", "password": "short"}
	assertCode(t, post(map[string]any{"mode": "enterprise", "setup_token": tok2, "admin": weak}), 422, "WEAK_PASSWORD")
	assertCode(t, post(map[string]any{"mode": "enterprise", "setup_token": tok2}), 422, "INVALID_ARGUMENT")
	assertCode(t, post(map[string]any{"mode": "enterprise", "setup_token": tok2, "admin": admin, "rp_id": "evil.com", "origins": []string{"https://evil.com"}}), 422, "INVALID_ARGUMENT")
	assertCode(t, post(map[string]any{"mode": "enterprise", "setup_token": tok2, "admin": admin, "allow_password_login": false}), 422, "INVALID_ARGUMENT")
	assertCode(t, post(map[string]any{"mode": "enterprise", "setup_token": tok2, "admin": admin, "session_idle_minutes": 1}), 422, "INVALID_ARGUMENT")
	assertCode(t, post(map[string]any{"mode": "enterprise", "setup_token": tok2, "admin": map[string]any{"username": "x y", "display_name": "", "password": adminPW}}), 422, "INVALID_ARGUMENT")
	if _, err := e.st.AuthUserByName(t.Context(), "admin"); err == nil {
		t.Fatal("a rejected setup must not create anyone")
	}
	r := post(map[string]any{"mode": "enterprise", "setup_token": tok2, "admin": admin, "mfa_policy": "off"})
	if r.Code != 201 || r.str("status") != "ok" || r.str("csrf_token") == "" || c.cookie == "" {
		t.Fatalf("%d %s", r.Code, r.Raw)
	}
	// afterwards the wizard is closed
	assertCode(t, post(map[string]any{"mode": "enterprise", "setup_token": tok2, "admin": admin}), 409, "ALREADY_SETUP")
	assertCode(t, c.do("POST", "/v1/setup/verify-token", map[string]any{"token": tok2}), 409, "ALREADY_SETUP")
	if r := c.must("GET", "/v1/setup/status", nil, 200); r.Data["initialized"] != true || r.Data["mode"] != "enterprise" {
		t.Fatalf("%v", r.Data)
	}
	if tok3, _ := e.svc.EnsureSetupToken(t.Context()); tok3 != "" {
		t.Fatal("no token once initialised")
	}
	if e.auditActions()["setup.complete"] != 1 {
		t.Fatal("setup not audited")
	}
}

func TestSetupConcurrentSingleWinner(t *testing.T) {
	e := newAuthEnv(t)
	tok, _ := e.svc.EnsureSetupToken(t.Context())
	var created, conflict atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := e.client()
			c.remote = "192.0.2." + string(rune('1'+i)) + ":1"
			r := c.do("POST", "/v1/setup/complete", map[string]any{"setup_token": tok, "mode": "enterprise", "mfa_policy": "off",
				"admin": map[string]any{"username": "admin" + string(rune('a'+i)), "display_name": "x", "password": adminPW}})
			switch {
			case r.Code == 201:
				created.Add(1)
			case r.Code == 409 && r.code() == "ALREADY_SETUP":
				conflict.Add(1)
			default:
				t.Errorf("unexpected %d %s", r.Code, r.Raw)
			}
		}(i)
	}
	wg.Wait()
	if created.Load() != 1 || conflict.Load() != 7 {
		t.Fatalf("created=%d conflict=%d", created.Load(), conflict.Load())
	}
	if _, total, _ := e.st.ListAuthUsers(t.Context(), store.UserFilter{Now: time.Now()}, store.Page{}); total != 1 {
		t.Fatalf("%d users", total)
	}
}

func TestLocalMode(t *testing.T) {
	e := newAuthEnv(t)
	e.svc.EnsureSetupToken(t.Context())
	proxied := e.client()
	proxied.remote = "127.0.0.1:4000"
	proxied.hdr["X-Forwarded-For"] = "203.0.113.9"
	assertCode(t, proxied.do("POST", "/v1/setup/complete", map[string]any{"mode": "local"}), 403, "LOCAL_ONLY") // a proxied request only looks local
	remote := e.client()
	assertCode(t, remote.do("POST", "/v1/setup/complete", map[string]any{"mode": "local"}), 403, "LOCAL_ONLY")
	c := e.client()
	c.remote = "127.0.0.1:4000"
	if r := c.must("GET", "/v1/setup/status", nil, 200); r.Data["loopback"] != true {
		t.Fatalf("%v", r.Data)
	}
	r := c.must("POST", "/v1/setup/complete", map[string]any{"mode": "local"}, 201) // loopback needs no token
	if r.str("status") != "ok" || c.cookie != "" {
		t.Fatalf("local mode has no sessions: %s", r.Raw)
	}
	me := c.must("GET", "/v1/auth/me", nil, 200)
	u := me.Data["user"].(map[string]any)
	if u["username"] != "local" || u["role"] != "admin" || me.Data["mfa"] != "local" || me.Data["scope"] != "full" {
		t.Fatalf("%v", me.Data)
	}
	c.must("GET", "/v1/admin/security-settings", nil, 200)
	c.must("POST", "/v1/admin/users", map[string]any{"username": "bob", "role": "viewer", "credential": "password"}, 201) // no CSRF needed: no cookie session
	assertCode(t, c.do("POST", "/v1/auth/login", map[string]any{"username": "x", "password": "y"}), 409, "LOCAL_MODE")
	assertCode(t, c.do("POST", "/v1/auth/totp/begin", nil), 409, "LOCAL_MODE")
	assertCode(t, c.do("POST", "/v1/auth/passkey/login/begin", nil), 409, "LOCAL_MODE")
	if r := c.must("GET", "/v1/auth/sessions", nil, 200); len(r.Any.([]any)) != 0 {
		t.Fatal("no sessions in local mode")
	}
	c.must("POST", "/v1/auth/logout", nil, 204)
	// the open door is only for this machine
	assertCode(t, remote.do("GET", "/v1/auth/me", nil), 403, "FORBIDDEN")
	assertCode(t, proxied.do("GET", "/v1/auth/me", nil), 403, "FORBIDDEN")
	// a trusted proxy hands the real client address over
	e.srv.d.TrustedProxy = true
	proxied.hdr["X-Forwarded-For"] = "127.0.0.1"
	proxied.must("GET", "/v1/auth/me", nil, 200)
	if got := actorOfLocal(); got != "local" {
		t.Fatal(got)
	}
}

func actorOfLocal() string {
	req, _ := http.NewRequest("GET", "/", nil)
	return actorOf(req) // auth not wired → "local"
}

func TestLoginLockoutAndUnknownUsersLookAlike(t *testing.T) {
	e := newAuthEnv(t)
	admin := e.setup(nil)
	e.user(admin, "carol", "viewer")
	wrong := func(name string) resp {
		_, r := e.login(name, "Wrong-Password-1")
		return r
	}
	known, unknown := wrong("carol"), wrong("nobody-here")
	if known.Code != 401 || unknown.Code != 401 || known.code() != "INVALID_CREDENTIALS" || known.Err["message"] != unknown.Err["message"] {
		t.Fatalf("known/unknown must be indistinguishable: %s vs %s", known.Raw, unknown.Raw)
	}
	for i := 0; i < 3; i++ {
		wrong("carol")
		wrong("nobody-here")
	}
	// fifth failure locks (the real and the made-up account behave the same)
	assertCode(t, wrong("carol"), 423, "ACCOUNT_LOCKED")
	assertCode(t, wrong("nobody-here"), 423, "ACCOUNT_LOCKED")
	// a correct password does not get through while locked
	_, r := e.login("carol", adminPW)
	assertCode(t, r, 423, "ACCOUNT_LOCKED")
	// the lock expires with the (injected) clock
	e.clock.Advance(16 * time.Minute)
	c, r := e.login("carol", adminPW)
	if r.Code != 200 || c.cookie == "" {
		t.Fatalf("after the lockout window: %d %s", r.Code, r.Raw)
	}
	// admin unlock
	for i := 0; i < 5; i++ {
		wrong("carol")
	}
	_, r = e.login("carol", adminPW)
	assertCode(t, r, 423, "ACCOUNT_LOCKED")
	list := admin.must("GET", "/v1/admin/users?status=locked", nil, 200)
	items := list.Data["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["status"] != "locked" {
		t.Fatalf("%s", list.Raw)
	}
	id := items[0].(map[string]any)["id"].(string)
	if r := admin.must("POST", "/v1/admin/users/"+id+"/unlock", nil, 200); r.str("status") != "active" {
		t.Fatalf("%s", r.Raw)
	}
	if _, r = e.login("carol", adminPW); r.Code != 200 {
		t.Fatalf("after unlock: %s", r.Raw)
	}
	// successful login resets the failure counter
	for i := 0; i < 4; i++ {
		wrong("carol")
	}
	e.login("carol", adminPW)
	for i := 0; i < 4; i++ {
		if r := wrong("carol"); r.Code != 401 {
			t.Fatalf("counter was not reset: %d", r.Code)
		}
	}
	// disabled accounts cannot log in and look like wrong credentials
	admin.must("PATCH", "/v1/admin/users/"+id, map[string]any{"status": "disabled"}, 200)
	_, r = e.login("carol", adminPW)
	assertCode(t, r, 401, "INVALID_CREDENTIALS")
	acts := e.auditActions()
	for _, a := range []string{"auth.login", "auth.login_failed", "auth.lockout", "user.unlock", "user.update"} {
		if acts[a] == 0 {
			t.Errorf("audit missing %s", a)
		}
	}
}

func TestRateLimitReturns429WithRetryAfter(t *testing.T) {
	e := newAuthEnv(t)
	e.setup(nil)
	e.svc.IPLimiter = auth.NewLimiter(4, time.Hour, e.clock.Now)
	c := e.client()
	for i := 0; i < 4; i++ {
		if r := c.do("POST", "/v1/auth/login", map[string]any{"username": "a" + string(rune('a'+i)), "password": "x"}); r.Code != 401 {
			t.Fatalf("%d", r.Code)
		}
	}
	r := c.do("POST", "/v1/auth/login", map[string]any{"username": "zz", "password": "x"})
	assertCode(t, r, 429, "RATE_LIMITED")
	if ra := r.Header.Get("Retry-After"); ra == "" || ra == "0" {
		t.Fatalf("Retry-After: %q", ra)
	}
	// per-username bucket (distinct addresses, same account)
	e.svc.IPLimiter = auth.NewLimiter(1000, time.Second, e.clock.Now)
	e.svc.UserLimiter = auth.NewLimiter(3, time.Hour, e.clock.Now)
	for i := 0; i < 3; i++ {
		c := e.client()
		c.remote = "198.51.100." + string(rune('1'+i)) + ":9"
		c.do("POST", "/v1/auth/login", map[string]any{"username": "admin", "password": "x"})
	}
	other := e.client()
	other.remote = "198.51.100.77:9"
	assertCode(t, other.do("POST", "/v1/auth/login", map[string]any{"username": "ADMIN", "password": adminPW}), 429, "RATE_LIMITED")
}

func TestCookieAttributesAndSecureFlag(t *testing.T) {
	e := newAuthEnv(t)
	e.setup(nil)
	login := func(c *tclient) string {
		req := c.rawLogin()
		return req
	}
	plain := login(e.client())
	if !strings.Contains(plain, "oa_session=") || !strings.Contains(plain, "HttpOnly") || !strings.Contains(plain, "SameSite=Strict") || !strings.Contains(plain, "Path=/") || strings.Contains(plain, "Secure") {
		t.Fatalf("plain http cookie: %s", plain)
	}
	tls := e.client()
	tls.tls = true
	if s := login(tls); !strings.Contains(s, "Secure") || !strings.Contains(s, "HttpOnly") || !strings.Contains(s, "SameSite=Strict") {
		t.Fatalf("https cookie: %s", s)
	}
	// forwarded proto is ignored unless the proxy is trusted
	spoof := e.client()
	spoof.hdr["X-Forwarded-Proto"] = "https"
	if s := login(spoof); strings.Contains(s, "Secure") {
		t.Fatalf("untrusted X-Forwarded-Proto honoured: %s", s)
	}
	e.srv.d.TrustedProxy = true
	if s := login(spoof); !strings.Contains(s, "Secure") {
		t.Fatalf("trusted proxy: %s", s)
	}
	// the client address used for sessions/limits
	px := e.client()
	px.hdr["X-Forwarded-For"] = "203.0.113.5, 10.0.0.1"
	px.must("POST", "/v1/auth/login", map[string]any{"username": "admin", "password": adminPW}, 200)
	sessions := px.must("GET", "/v1/auth/sessions", nil, 200)
	found := false
	for _, s := range sessions.Any.([]any) {
		if s.(map[string]any)["ip"] == "203.0.113.5" && s.(map[string]any)["current"] == true {
			found = true
		}
	}
	if !found {
		t.Fatalf("X-Forwarded-For first hop not used: %s", sessions.Raw)
	}
	e.srv.d.TrustedProxy = false
	plainIP := e.client()
	plainIP.hdr["X-Forwarded-For"] = "203.0.113.5"
	plainIP.must("POST", "/v1/auth/login", map[string]any{"username": "admin", "password": adminPW}, 200)
	for _, s := range plainIP.must("GET", "/v1/auth/sessions", nil, 200).Any.([]any) {
		if s.(map[string]any)["current"] == true && s.(map[string]any)["ip"] != "192.0.2.10" {
			t.Fatalf("X-Forwarded-For trusted without OA_TRUSTED_PROXY: %v", s)
		}
	}
}

// rawLogin performs a login and returns the Set-Cookie header.
func (c *tclient) rawLogin() string {
	r := c.do("POST", "/v1/auth/login", map[string]any{"username": "admin", "password": adminPW})
	if r.Code != 200 {
		c.env.t.Fatalf("login: %s", r.Raw)
	}
	return strings.Join(r.Header.Values("Set-Cookie"), "; ")
}

func TestCSRFAndSameOriginGuard(t *testing.T) {
	e := newAuthEnv(t)
	admin := e.setup(nil)
	good := admin.csrf
	admin.csrf = ""
	assertCode(t, admin.do("POST", "/v1/auth/reauth", map[string]any{"password": adminPW}), 403, "CSRF_INVALID")
	admin.csrf = "x" + good
	assertCode(t, admin.do("POST", "/v1/auth/reauth", map[string]any{"password": adminPW}), 403, "CSRF_INVALID")
	assertCode(t, admin.do("PATCH", "/v1/admin/security-settings", map[string]any{"lockout_minutes": 5}), 403, "CSRF_INVALID")
	assertCode(t, admin.do("DELETE", "/v1/auth/sessions/abc", nil), 403, "CSRF_INVALID")
	admin.must("GET", "/v1/auth/me", nil, 200) // reads need no token
	admin.csrf = good
	admin.must("POST", "/v1/auth/reauth", map[string]any{"password": adminPW}, 200)
	// another session's token does not work on this one
	other := e.client()
	other.must("POST", "/v1/auth/login", map[string]any{"username": "admin", "password": adminPW}, 200)
	admin.csrf = other.csrf
	assertCode(t, admin.do("POST", "/v1/auth/reauth", map[string]any{"password": adminPW}), 403, "CSRF_INVALID")
	admin.csrf = good
	// /me re-issues the same token
	if me := admin.must("GET", "/v1/auth/me", nil, 200); me.str("csrf_token") != good {
		t.Fatal("csrf token must be stable per session")
	}
	// cross-site writes stay blocked by the existing guard
	admin.hdr["Origin"] = "https://evil.example"
	assertCode(t, admin.do("POST", "/v1/auth/reauth", map[string]any{"password": adminPW}), 403, "CROSS_ORIGIN")
}

func TestSessionExpiryIdleAbsoluteAndSliding(t *testing.T) {
	e := newAuthEnv(t)
	admin := e.setup(map[string]any{"session_idle_minutes": 5, "session_max_hours": 1})
	// sliding: activity every 4 minutes keeps the session alive past the idle limit
	for i := 0; i < 5; i++ {
		e.clock.Advance(4 * time.Minute)
		admin.must("GET", "/v1/auth/me", nil, 200)
	}
	// idle timeout
	e.clock.Advance(6 * time.Minute)
	assertCode(t, admin.do("GET", "/v1/auth/me", nil), 401, "UNAUTHENTICATED")
	// the expired session is gone for good and the cookie was cleared
	if admin.cookie != "" {
		t.Fatal("stale cookie should be cleared")
	}
	// absolute timeout: constant activity still ends at max_hours (1h)
	c, r := e.login("admin", adminPW)
	if r.Code != 200 {
		t.Fatal(string(r.Raw))
	}
	expired := false
	for i := 0; i < 20; i++ {
		e.clock.Advance(4 * time.Minute)
		if c.do("GET", "/v1/auth/me", nil).Code == 401 {
			expired = true
			if i < 13 {
				t.Fatalf("expired too early (%d min)", (i+1)*4)
			}
			break
		}
	}
	if !expired {
		t.Fatal("absolute session limit not enforced")
	}
	// last_seen is written at most once a minute
	c2, _ := e.login("admin", adminPW)
	sess := func() time.Time {
		list, _ := e.st.ListSessions(t.Context(), "", e.clock.Now())
		for _, s := range list {
			if s.ID == auth.HashToken(c2.cookie) {
				return s.LastSeenAt
			}
		}
		t.Fatal("session not found")
		return time.Time{}
	}
	before := sess()
	e.clock.Advance(20 * time.Second)
	c2.must("GET", "/v1/auth/me", nil, 200)
	if !sess().Equal(before) {
		t.Fatal("last_seen updated more than once a minute")
	}
	e.clock.Advance(50 * time.Second)
	c2.must("GET", "/v1/auth/me", nil, 200)
	if !sess().After(before) {
		t.Fatal("last_seen not refreshed after a minute")
	}
	// an unusable cookie is just unauthenticated
	bad := e.client()
	bad.cookie = "not-a-session"
	assertCode(t, bad.do("GET", "/v1/auth/me", nil), 401, "UNAUTHENTICATED")
}

func TestLogoutSessionListAndRevoke(t *testing.T) {
	e := newAuthEnv(t)
	admin := e.setup(nil)
	second := e.client()
	second.must("POST", "/v1/auth/login", map[string]any{"username": "admin", "password": adminPW}, 200)
	list := admin.must("GET", "/v1/auth/sessions", nil, 200).Any.([]any)
	if len(list) != 2 {
		t.Fatalf("%d sessions", len(list))
	}
	var other string
	for _, s := range list {
		m := s.(map[string]any)
		if m["current"] != true {
			other = m["id"].(string)
		}
		if m["username"] != "admin" || m["user_agent"] == nil || m["mfa"] != "none" {
			t.Fatalf("%v", m)
		}
	}
	// the session id is a hash, never the cookie value
	if contains(admin.Raw0(), admin.cookie) {
		t.Fatal("cookie value leaked in the session list")
	}
	admin.must("DELETE", "/v1/auth/sessions/"+other, nil, 204)
	assertCode(t, second.do("GET", "/v1/auth/me", nil), 401, "UNAUTHENTICATED")
	assertCode(t, admin.do("DELETE", "/v1/auth/sessions/"+other, nil), 404, "NOT_FOUND")
	// somebody else's session cannot be revoked through the self-service route
	carol := e.user(admin, "carol", "viewer")
	cs := ""
	for _, s := range carol.must("GET", "/v1/auth/sessions", nil, 200).Any.([]any) {
		if m := s.(map[string]any); m["current"] == true {
			cs = m["id"].(string)
		}
	}
	assertCode(t, admin.do("DELETE", "/v1/auth/sessions/"+cs, nil), 404, "NOT_FOUND")
	adminList := admin.must("GET", "/v1/admin/sessions", nil, 200).Any.([]any)
	if len(adminList) < 2 {
		t.Fatal("admin must see every session")
	}
	admin.must("DELETE", "/v1/admin/sessions/"+cs, nil, 204)
	assertCode(t, carol.do("GET", "/v1/auth/me", nil), 401, "UNAUTHENTICATED")
	assertCode(t, admin.do("DELETE", "/v1/admin/sessions/"+cs, nil), 404, "NOT_FOUND")
	admin.must("POST", "/v1/auth/logout", nil, 204)
	if admin.cookie != "" {
		t.Fatal("logout must clear the cookie")
	}
	assertCode(t, admin.do("GET", "/v1/auth/me", nil), 401, "UNAUTHENTICATED")
}

// Raw0 returns the most recent response body.
func (c *tclient) Raw0() []byte { b := *c.bodies; return b[len(b)-1] }
