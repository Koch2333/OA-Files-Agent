package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"oa-agent/backend/internal/auth"
	"oa-agent/backend/internal/auth/softauth"
)

// actor is an independent browser: its own cookie jar and CSRF token.
type actor struct {
	c    *http.Client
	csrf string
}

var part8Bodies []string

func newActor() *actor {
	jar, _ := cookiejar.New(nil)
	return &actor{c: &http.Client{Jar: jar, Timeout: 30 * time.Second}}
}

type reply struct {
	Status int
	Code   string // error code, "" on success
	Data   map[string]any
	List   []any
	Raw    []byte
	Header http.Header
	Err    map[string]any
}

func (a *actor) do(method, path string, body any, hdr ...string) reply {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, *base+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if a.csrf != "" && method != "GET" {
		req.Header.Set("X-CSRF-Token", a.csrf)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		if hdr[i+1] == "" {
			req.Header.Del(hdr[i])
		} else {
			req.Header.Set(hdr[i], hdr[i+1])
		}
	}
	res, err := a.c.Do(req)
	if err != nil {
		fmt.Println("request failed:", err)
		return reply{}
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	recordCall(method, path, res.StatusCode, raw)
	part8Bodies = append(part8Bodies, string(raw))
	r := reply{Status: res.StatusCode, Raw: raw, Header: res.Header}
	var env struct {
		Data  json.RawMessage `json:"data"`
		Error map[string]any  `json:"error"`
	}
	if json.Unmarshal(raw, &env) == nil {
		r.Err = env.Error
		if env.Error != nil {
			r.Code, _ = env.Error["code"].(string)
		}
		_ = json.Unmarshal(env.Data, &r.Data)
		_ = json.Unmarshal(env.Data, &r.List)
	}
	if t, ok := r.Data["csrf_token"].(string); ok && t != "" {
		a.csrf = t
	}
	return r
}

func (r reply) s(k string) string { v, _ := r.Data[k].(string); return v }

func (r reply) reason() string {
	d, _ := r.Err["details"].(map[string]any)
	s, _ := d["reason"].(string)
	return s
}

const e2ePW = "Granite-Tulip-Orbit-4821"

// adminActor is a logged-in session of the e2e administrator, independent of the shared client.
func adminActor() *actor {
	a := newActor()
	r := a.do("POST", "/v1/auth/login", map[string]any{"username": *adminUserFlag, "password": *adminPassFlag})
	if r.Status != 200 {
		fmt.Println("admin login failed:", string(r.Raw))
	}
	return a
}

func mkUser(admin *actor, name, role string) *actor {
	r := admin.do("POST", "/v1/admin/users", map[string]any{"username": name, "display_name": name, "role": role, "credential": "password"})
	if r.Status != 201 {
		check("create user "+name, false, r.Status, string(r.Raw))
		return newActor()
	}
	tmp := r.s("temporary_password")
	a := newActor()
	a.do("POST", "/v1/auth/login", map[string]any{"username": name, "password": tmp})
	a.do("POST", "/v1/auth/password", map[string]any{"current_password": tmp, "new_password": e2ePW})
	b := newActor()
	b.do("POST", "/v1/auth/login", map[string]any{"username": name, "password": e2ePW})
	return b
}

func userID(admin *actor, name string) string {
	r := admin.do("GET", "/v1/admin/users?q="+url.QueryEscape(name)+"&page_size=50", nil)
	items, _ := r.Data["items"].([]any)
	for _, it := range items {
		m := it.(map[string]any)
		if strings.EqualFold(m["username"].(string), name) {
			return m["id"].(string)
		}
	}
	return ""
}

func part8() {
	fmt.Println("--- part 8: accounts, roles, sessions, MFA, audit")
	part8Bodies = nil
	suffix := fmt.Sprint(time.Now().Unix() % 100000)
	admin := adminActor()

	// wizard is idempotent / closed
	anon := newActor()
	r0 := anon.do("GET", "/v1/setup/status", nil)
	check("8 setup status exposes wizard_finished, server_version and api_level", r0.Status == 200 && r0.Data["wizard_finished"] == true && r0.Data["api_level"] != nil && r0.s("server_version") != "", string(r0.Raw))
	r := anon.do("POST", "/v1/setup/complete", map[string]any{"mode": "enterprise", "setup_token": "x", "admin": map[string]any{"username": "intruder", "display_name": "x", "password": e2ePW}})
	check("8 second /v1/setup/complete → 409 ALREADY_SETUP", r.Status == 409 && r.Code == "ALREADY_SETUP", r.Status, string(r.Raw))
	r = anon.do("POST", "/v1/setup/verify-token", map[string]any{"token": "x"})
	check("8 verify-token after setup → 409", r.Status == 409, r.Status)
	r = anon.do("GET", "/v1/setup/status", nil)
	check("8 setup status public: initialized + mode + allow_password_login", r.Status == 200 && r.Data["initialized"] == true && r.Data["mode"] == "enterprise" && r.Data["allow_password_login"] == true, string(r.Raw))
	passkeys := r.Data["passkey_available"] == true

	// login / logout / me / wrong password / cookie attributes
	a := newActor()
	r = a.do("POST", "/v1/auth/login", map[string]any{"username": *adminUserFlag, "password": "Definitely-Wrong-1"})
	check("8 wrong password → 401 INVALID_CREDENTIALS", r.Status == 401 && r.Code == "INVALID_CREDENTIALS", r.Status, string(r.Raw))
	r2 := a.do("POST", "/v1/auth/login", map[string]any{"username": "no-such-user-" + suffix, "password": "Definitely-Wrong-1"})
	check("8 unknown user answers like a wrong password", r2.Status == 401 && r2.Err["message"] == r.Err["message"], string(r2.Raw))
	req, _ := http.NewRequest("POST", *base+"/v1/auth/login", strings.NewReader(fmt.Sprintf(`{"username":%q,"password":%q}`, *adminUserFlag, *adminPassFlag)))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultTransport.RoundTrip(req)
	if err == nil {
		sc := strings.Join(res.Header.Values("Set-Cookie"), ";")
		res.Body.Close()
		check("8 session cookie is HttpOnly, SameSite=Strict, Path=/", strings.Contains(sc, "oa_session=") && strings.Contains(sc, "HttpOnly") && strings.Contains(sc, "SameSite=Strict") && strings.Contains(sc, "Path=/"), sc)
	}
	r = a.do("POST", "/v1/auth/login", map[string]any{"username": *adminUserFlag, "password": *adminPassFlag})
	check("8 login ok with csrf_token", r.Status == 200 && r.s("status") == "ok" && r.s("csrf_token") != "", string(r.Raw))
	r = a.do("GET", "/v1/auth/me", nil)
	check("8 /v1/auth/me", r.Status == 200 && r.Data["scope"] == "full" && r.Data["fresh"] == true && r.Data["user"].(map[string]any)["role"] == "admin", string(r.Raw))
	a.do("POST", "/v1/auth/logout", nil)
	r = a.do("GET", "/v1/auth/me", nil)
	check("8 after logout → 401 UNAUTHENTICATED", r.Status == 401 && r.Code == "UNAUTHENTICATED", r.Status)

	// CSRF
	c := adminActor()
	r = c.do("POST", "/v1/auth/reauth", map[string]any{"password": *adminPassFlag}, "X-CSRF-Token", "")
	check("8 write without CSRF token → 403 CSRF_INVALID", r.Status == 403 && r.Code == "CSRF_INVALID", r.Status, string(r.Raw))
	r = c.do("POST", "/v1/auth/reauth", map[string]any{"password": *adminPassFlag}, "X-CSRF-Token", "wrong")
	check("8 wrong CSRF token → 403", r.Status == 403 && r.Code == "CSRF_INVALID", r.Status)
	r = c.do("POST", "/v1/auth/reauth", map[string]any{"password": *adminPassFlag}, "Origin", "https://evil.example")
	check("8 cross-site Origin → 403 CROSS_ORIGIN", r.Status == 403 && r.Code == "CROSS_ORIGIN", r.Status, string(r.Raw))

	// unauthenticated access across the API
	for _, p := range []string{"/v1/tasks", "/v1/ledger", "/v1/files", "/v1/audit-logs", "/v1/providers", "/v1/fs?path=", "/v1/llm/config", "/v1/storage/connections", "/v1/admin/users", "/v1/tasks/00000000-0000-4000-8000-000000000001/events", "/v1/ledger/export"} {
		r := anon.do("GET", p, nil)
		check("8 anonymous GET "+p+" → 401", r.Status == 401 && r.Code == "UNAUTHENTICATED", r.Status)
	}
	for _, p := range []string{"/v1/imports", "/v1/oa-templates", "/v1/fs/move"} {
		r := anon.do("POST", p, map[string]any{})
		check("8 anonymous POST "+p+" → 401", r.Status == 401, r.Status)
	}
	r = anon.do("GET", "/healthz", nil)
	check("8 /healthz stays public", r.Status == 200)

	// roles on real routes
	viewer, operator := mkUser(admin, "e2e-viewer-"+suffix, "viewer"), mkUser(admin, "e2e-operator-"+suffix, "operator")
	type probe struct {
		method, path string
		body         any
		v, o, ad     int // expected status class: 200 = allowed (anything but 401/403), 403 = forbidden
	}
	probes := []probe{
		{"GET", "/v1/tasks", nil, 200, 200, 200}, {"GET", "/v1/ledger", nil, 200, 200, 200}, {"GET", "/v1/providers", nil, 200, 200, 200},
		{"GET", "/v1/oa-templates", nil, 200, 200, 200}, {"GET", "/v1/llm/config", nil, 403, 403, 200},
		{"POST", "/v1/oa-templates", map[string]any{}, 403, 200, 200}, {"POST", "/v1/archive-rules", map[string]any{}, 403, 200, 200},
		{"POST", "/v1/fs/folders", map[string]any{}, 403, 200, 200}, {"POST", "/v1/imports", nil, 403, 200, 200},
		{"GET", "/v1/audit-logs", nil, 403, 403, 200}, {"PUT", "/v1/llm/config", map[string]any{}, 403, 403, 200}, {"POST", "/v1/llm/test", map[string]any{}, 403, 403, 200},
		{"GET", "/v1/storage/connections", nil, 403, 200, 200}, {"POST", "/v1/storage/connections", map[string]any{}, 403, 403, 200}, {"GET", "/v1/storage/kinds", nil, 403, 403, 200},
		{"POST", "/v1/fs/reveal", map[string]any{}, 403, 403, 200},
		{"GET", "/v1/admin/users", nil, 403, 403, 200}, {"POST", "/v1/admin/users", map[string]any{}, 403, 403, 200}, {"GET", "/v1/admin/security-settings", nil, 403, 403, 200}, {"GET", "/v1/admin/sessions", nil, 403, 403, 200},
		{"PATCH", "/v1/admin/security-settings", map[string]any{}, 403, 403, 200},
	}
	for _, p := range probes {
		for role, who := range map[string]struct {
			a    *actor
			want int
		}{"viewer": {viewer, p.v}, "operator": {operator, p.o}, "admin": {admin, p.ad}} {
			r := who.a.do(p.method, p.path, p.body)
			okRole := r.Status != 401 && r.Status != 403
			if who.want == 403 {
				check(fmt.Sprintf("8 %s %s as %s → 403 FORBIDDEN", p.method, p.path, role), r.Status == 403 && r.Code == "FORBIDDEN", r.Status, string(r.Raw))
			} else {
				check(fmt.Sprintf("8 %s %s as %s is allowed", p.method, p.path, role), okRole, r.Status, string(r.Raw))
			}
		}
	}

	// sessions: list, revoke another one
	s1 := newActor()
	s1.do("POST", "/v1/auth/login", map[string]any{"username": "e2e-viewer-" + suffix, "password": e2ePW})
	r = viewer.do("GET", "/v1/auth/sessions", nil)
	cur, other := "", ""
	var others []string
	for _, it := range r.List {
		m := it.(map[string]any)
		if m["current"] == true {
			cur = m["id"].(string)
		} else {
			other = m["id"].(string)
			others = append(others, other)
		}
	}
	check("8 own sessions listed (current flagged)", cur != "" && other != "", string(r.Raw))
	ok204 := true
	for _, id := range others {
		ok204 = ok204 && viewer.do("DELETE", "/v1/auth/sessions/"+id, nil).Status == 204
	}
	check("8 revoke the other sessions (204)", ok204)
	check("8 revoked session is dead", s1.do("GET", "/v1/auth/me", nil).Status == 401)
	r = viewer.do("DELETE", "/v1/auth/sessions/"+other, nil)
	check("8 revoking twice → 404", r.Status == 404, r.Status)
	r = admin.do("GET", "/v1/admin/sessions", nil)
	check("8 admin sees all sessions", r.Status == 200 && len(r.List) >= 3, string(r.Raw))

	// TOTP enrolment, login, replay, recovery codes
	user := "e2e-totp-" + suffix
	tu := mkUser(admin, user, "operator")
	b := tu.do("POST", "/v1/auth/totp/begin", nil)
	secret := b.s("secret")
	check("8 TOTP begin gives secret + otpauth URI", b.Status == 200 && secret != "" && strings.HasPrefix(b.s("otpauth_uri"), "otpauth://totp/"), string(b.Raw))
	code, _ := auth.TOTPCode(secret, time.Now())
	r = tu.do("POST", "/v1/auth/totp/confirm", map[string]any{"challenge_id": b.s("challenge_id"), "code": "000001"})
	check("8 wrong confirmation code → 422", r.Status == 422, r.Status, string(r.Raw))
	r = tu.do("POST", "/v1/auth/totp/confirm", map[string]any{"challenge_id": b.s("challenge_id"), "code": code})
	codes, _ := r.Data["recovery_codes"].([]any)
	check("8 TOTP confirmed, 10 recovery codes returned once", r.Status == 200 && len(codes) == 10, string(r.Raw))
	login := newActor()
	r = login.do("POST", "/v1/auth/login", map[string]any{"username": user, "password": e2ePW})
	check("8 login now requires MFA", r.Status == 200 && r.s("status") == "mfa_required" && r.s("mfa_token") != "", string(r.Raw))
	tok := r.s("mfa_token")
	r = login.do("POST", "/v1/auth/mfa/totp", map[string]any{"mfa_token": tok, "code": code})
	check("8 TOTP code of the enrolment step is a replay → 401", r.Status == 401, r.Status, string(r.Raw))
	next, _ := auth.TOTPCode(secret, time.Now().Add(30*time.Second)) // inside the ±1 window, newer step
	r = login.do("POST", "/v1/auth/mfa/totp", map[string]any{"mfa_token": tok, "code": next})
	check("8 TOTP login (200, mfa=totp)", r.Status == 200 && r.s("status") == "ok", string(r.Raw))
	r = login.do("POST", "/v1/auth/mfa/totp", map[string]any{"mfa_token": tok, "code": next})
	check("8 mfa_token is single use → 401", r.Status == 401, r.Status)
	rc := newActor()
	tok2 := rc.do("POST", "/v1/auth/login", map[string]any{"username": user, "password": e2ePW}).s("mfa_token")
	r = rc.do("POST", "/v1/auth/mfa/recovery", map[string]any{"mfa_token": tok2, "code": codes[0].(string)})
	check("8 recovery code login (200)", r.Status == 200 && r.s("status") == "ok", string(r.Raw))
	rc2 := newActor()
	tok3 := rc2.do("POST", "/v1/auth/login", map[string]any{"username": user, "password": e2ePW}).s("mfa_token")
	r = rc2.do("POST", "/v1/auth/mfa/recovery", map[string]any{"mfa_token": tok3, "code": codes[0].(string)})
	check("8 recovery code works only once → 401", r.Status == 401, r.Status)
	r = rc.do("GET", "/v1/auth/security", nil)
	check("8 recovery codes remaining = 9", r.Data["recovery_codes_remaining"] == float64(9) && r.Data["totp_enabled"] == true, string(r.Raw))

	// lockout and admin unlock
	lockUser := "e2e-lock-" + suffix
	mkUser(admin, lockUser, "viewer")
	var last reply
	for i := 0; i < 5; i++ {
		last = newActor().do("POST", "/v1/auth/login", map[string]any{"username": lockUser, "password": "Wrong-Wrong-Wrong-1"})
	}
	check("8 fifth wrong password locks the account (423 ACCOUNT_LOCKED)", last.Status == 423 && last.Code == "ACCOUNT_LOCKED", last.Status, string(last.Raw))
	r = newActor().do("POST", "/v1/auth/login", map[string]any{"username": lockUser, "password": e2ePW})
	check("8 correct password while locked → 423", r.Status == 423, r.Status)
	lid := userID(admin, lockUser)
	r = admin.do("POST", "/v1/admin/users/"+lid+"/unlock", nil)
	check("8 admin unlock", r.Status == 200 && r.s("status") == "active", string(r.Raw))
	r = newActor().do("POST", "/v1/auth/login", map[string]any{"username": lockUser, "password": e2ePW})
	check("8 login after unlock", r.Status == 200 && r.s("status") == "ok", string(r.Raw))

	// rate limiting answers 429 with Retry-After
	got429 := false
	for i := 0; i < 80 && !got429; i++ {
		r := newActor().do("POST", "/v1/auth/login", map[string]any{"username": "rate-" + suffix, "password": "x"})
		if r.Status == 429 {
			got429 = r.Code == "RATE_LIMITED" && r.Header.Get("Retry-After") != ""
			break
		}
	}
	check("8 login flood → 429 RATE_LIMITED with Retry-After", got429)

	// admin user lifecycle
	lc := "e2e-life-" + suffix
	r = admin.do("POST", "/v1/admin/users", map[string]any{"username": lc, "display_name": "生命周期", "role": "viewer", "credential": "password"})
	tmp := r.s("temporary_password")
	check("8 create user returns one-time temporary password", r.Status == 201 && tmp != "", string(r.Raw))
	r = admin.do("POST", "/v1/admin/users", map[string]any{"username": lc, "role": "viewer", "credential": "password"})
	check("8 duplicate username → 409", r.Status == 409, r.Status)
	lcA := newActor()
	r = lcA.do("POST", "/v1/auth/login", map[string]any{"username": lc, "password": tmp})
	check("8 temp-password login flags must_change_password", r.Status == 200 && r.Data["user"].(map[string]any)["must_change_password"] == true, string(r.Raw))
	r = lcA.do("GET", "/v1/tasks", nil)
	check("8 must-change session is confined (403 reason must_change_password)", r.Status == 403 && r.reason() == "must_change_password", r.Status, string(r.Raw))
	r = lcA.do("POST", "/v1/auth/password", map[string]any{"current_password": tmp, "new_password": "short"})
	check("8 weak new password → 422", r.Status == 422, r.Status)
	r = lcA.do("POST", "/v1/auth/password", map[string]any{"current_password": tmp, "new_password": e2ePW})
	check("8 password change (204)", r.Status == 204, r.Status)
	check("8 after the change the session is usable", lcA.do("GET", "/v1/tasks", nil).Status == 200)
	lcid := userID(admin, lc)
	r = admin.do("POST", "/v1/admin/users/"+lcid+"/reset-password", nil)
	check("8 reset-password issues a new temporary password", r.Status == 200 && r.s("temporary_password") != "", string(r.Raw))
	check("8 reset-password ends the user's sessions", lcA.do("GET", "/v1/auth/me", nil).Status == 401)
	r = admin.do("POST", "/v1/admin/users/"+userID(admin, user)+"/reset-mfa", nil)
	check("8 reset-mfa clears TOTP and recovery codes", r.Status == 200 && r.Data["totp_enabled"] == false && r.Data["recovery_codes_remaining"] == float64(0), string(r.Raw))
	r = newActor().do("POST", "/v1/auth/login", map[string]any{"username": user, "password": e2ePW})
	check("8 after reset-mfa login needs no second factor", r.Status == 200 && r.s("status") == "ok", string(r.Raw))
	r = admin.do("PATCH", "/v1/admin/users/"+lcid, map[string]any{"role": "operator", "status": "disabled"})
	check("8 update role/status", r.Status == 200 && r.s("role") == "operator" && r.s("status") == "disabled", string(r.Raw))
	r = newActor().do("POST", "/v1/auth/login", map[string]any{"username": lc, "password": e2ePW})
	check("8 disabled user cannot log in", r.Status == 401, r.Status)
	// last administrator
	self := userID(admin, *adminUserFlag)
	r = admin.do("DELETE", "/v1/admin/users/"+self, nil)
	check("8 cannot delete yourself → 409", r.Status == 409, r.Status, string(r.Raw))
	r = admin.do("PATCH", "/v1/admin/users/"+self, map[string]any{"role": "viewer"})
	check("8 cannot demote the last administrator → 409 LAST_ADMIN", r.Status == 409 && r.Code == "LAST_ADMIN", r.Status, string(r.Raw))
	r = admin.do("PATCH", "/v1/admin/users/"+self, map[string]any{"status": "disabled"})
	check("8 cannot disable the last administrator → 409 LAST_ADMIN", r.Status == 409 && r.Code == "LAST_ADMIN", r.Status)
	r = admin.do("DELETE", "/v1/admin/users/"+lcid, nil)
	check("8 delete user (204)", r.Status == 204, r.Status)

	// policy: admins need MFA → enrolment-only session
	r = admin.do("PATCH", "/v1/admin/security-settings", map[string]any{"mfa_policy": "admins", "lockout_minutes": 15})
	check("8 security settings change", r.Status == 200 && r.Data["mfa_policy"] == "admins", string(r.Raw))
	adm2 := "e2e-admin2-" + suffix
	r = admin.do("POST", "/v1/admin/users", map[string]any{"username": adm2, "role": "admin", "credential": "password"})
	ap := newActor()
	ap.do("POST", "/v1/auth/login", map[string]any{"username": adm2, "password": r.s("temporary_password")})
	ap.do("POST", "/v1/auth/password", map[string]any{"current_password": r.s("temporary_password"), "new_password": e2ePW})
	ap2 := newActor()
	r = ap2.do("POST", "/v1/auth/login", map[string]any{"username": adm2, "password": e2ePW})
	check("8 admin without MFA gets status enroll_mfa", r.Status == 200 && r.s("status") == "enroll_mfa", string(r.Raw))
	r = ap2.do("GET", "/v1/admin/users", nil)
	check("8 enrolment session cannot use the API (403 reason enroll_mfa)", r.Status == 403 && r.reason() == "enroll_mfa", r.Status, string(r.Raw))
	// the admin's own session is not demoted, and policy off again for the rest of the run
	admin.do("PATCH", "/v1/admin/security-settings", map[string]any{"mfa_policy": "off"})

	// passkeys (software authenticator) when the server is reached as localhost
	if passkeys {
		pu := "e2e-pk-" + suffix
		pa := mkUser(admin, pu, "viewer")
		au := softauth.New()
		origin := *base
		begin := pa.do("POST", "/v1/auth/passkeys/begin", nil)
		opts, _ := json.Marshal(begin.Data["options"])
		cred, err := au.Create(opts, origin)
		check("8 passkey registration options accepted by the software authenticator", err == nil && begin.Status == 200, err, string(begin.Raw))
		r = pa.do("POST", "/v1/auth/passkeys/finish", map[string]any{"challenge_id": begin.s("challenge_id"), "name": "e2e", "credential": json.RawMessage(cred)})
		check("8 passkey registered (+recovery codes)", r.Status == 200 && len(r.Data["recovery_codes"].([]any)) == 10, string(r.Raw))
		pl := newActor()
		lb := pl.do("POST", "/v1/auth/passkey/login/begin", nil)
		lopts, _ := json.Marshal(lb.Data["options"])
		as, _ := au.Get(lopts, origin)
		r = pl.do("POST", "/v1/auth/passkey/login/finish", map[string]any{"challenge_id": lb.s("challenge_id"), "credential": json.RawMessage(as)})
		check("8 passwordless passkey login (200)", r.Status == 200 && r.s("status") == "ok" && r.Data["user"].(map[string]any)["username"] == pu, string(r.Raw))
		// invitation: one-time
		inv := admin.do("POST", "/v1/admin/users", map[string]any{"username": "e2e-inv-" + suffix, "role": "viewer", "credential": "passkey_invite"})
		tkn, _ := inv.Data["invite"].(map[string]any)["token"].(string)
		check("8 invite created (token + path)", inv.Status == 201 && tkn != "", string(inv.Raw))
		ia := newActor()
		check("8 invitation visible", ia.do("GET", "/v1/auth/invitations/"+tkn, nil).Status == 200)
		ib := ia.do("POST", "/v1/auth/invitations/"+tkn+"/passkey/begin", nil)
		iopts, _ := json.Marshal(ib.Data["options"])
		ic, _ := softauth.New().Create(iopts, origin)
		r = ia.do("POST", "/v1/auth/invitations/"+tkn+"/passkey/finish", map[string]any{"challenge_id": ib.s("challenge_id"), "credential": json.RawMessage(ic)})
		check("8 invitation accepted → logged in", r.Status == 200 && r.s("status") == "ok", string(r.Raw))
		check("8 invitation is one-time (404)", newActor().do("GET", "/v1/auth/invitations/"+tkn, nil).Status == 404)
	} else {
		fmt.Println("INFO  passkey part skipped: server not reached as localhost/HTTPS (run with -api http://localhost:8080)")
	}

	// audit and secret hygiene
	r = admin.do("GET", "/v1/audit-logs?page_size=200", nil)
	seen := map[string]bool{}
	items, _ := r.Data["items"].([]any)
	for _, it := range items {
		seen[it.(map[string]any)["action"].(string)] = true
	}
	for _, act := range []string{"setup.complete", "auth.login", "auth.login_failed", "auth.lockout", "user.create", "user.unlock", "user.reset_password", "user.reset_mfa", "user.update", "user.delete", "totp.enable", "auth.password_change", "settings.update", "session.revoke", "auth.recovery_used"} {
		check("8 audit log has "+act, seen[act])
	}
	all := strings.Join(part8Bodies, "\n")
	for _, bad := range []string{"password_hash", "$argon2", "totp_secret", "secret_enc", "code_hash", "webauthn_handle", "setup_token_hash"} {
		check("8 no response mentions "+bad, !strings.Contains(all, bad))
	}
	leak := false
	for _, e := range items {
		m := e.(map[string]any)
		s := fmt.Sprint(m["summary"], m["target"])
		if strings.Contains(s, e2ePW) || strings.Contains(s, *adminPassFlag) || strings.Contains(s, "$argon2") {
			leak = true
		}
	}
	check("8 audit entries carry no secrets", !leak)
}
