package api

import (
	"strings"
	"testing"
	"time"
)

func userID(t *testing.T, e *authEnv, name string) string {
	t.Helper()
	u, err := e.st.AuthUserByName(t.Context(), name)
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

func TestAdminOperationsReauthResetsAndLastAdmin(t *testing.T) {
	e := newAuthEnv(t)
	admin := e.setup(nil)
	adminID := userID(t, e, "admin")
	r := admin.must("POST", "/v1/admin/users", map[string]any{"username": "bobby", "role": "operator", "credential": "password"}, 201)
	tmp, bid := r.str("temporary_password"), r.Data["user"].(map[string]any)["id"].(string)
	bob, _ := e.login("bobby", tmp)
	if g := bob.do("GET", "/v1/auth/sessions", nil); g.Code != 403 || g.Err["details"].(map[string]any)["reason"] != "must_change_password" {
		t.Fatalf("%s", g.Raw)
	}
	assertCode(t, bob.do("POST", "/v1/auth/password", map[string]any{"current_password": tmp, "new_password": "short"}), 422, "WEAK_PASSWORD")
	assertCode(t, bob.do("POST", "/v1/auth/password", map[string]any{"current_password": "nope-Nope-12345", "new_password": adminPW}), 401, "INVALID_CREDENTIALS")
	assertCode(t, bob.do("POST", "/v1/auth/password", map[string]any{"current_password": tmp, "new_password": tmp}), 422, "WEAK_PASSWORD")
	other, _ := e.login("bobby", tmp) // a second session dies when the password changes
	bob.must("POST", "/v1/auth/password", map[string]any{"current_password": tmp, "new_password": adminPW}, 204)
	assertCode(t, other.do("GET", "/v1/auth/me", nil), 401, "UNAUTHENTICATED")
	if me := bob.must("GET", "/v1/auth/me", nil, 200); me.Data["user"].(map[string]any)["must_change_password"] != false {
		t.Fatal("flag not cleared")
	}
	bob.must("GET", "/v1/auth/sessions", nil, 200)
	if _, lr := e.login("bobby", tmp); lr.Code != 401 {
		t.Fatal("old password still works")
	}

	// reset password: new one-time password, sessions revoked, must change again
	rp := admin.must("POST", "/v1/admin/users/"+bid+"/reset-password", nil, 200).str("temporary_password")
	if rp == "" || rp == tmp {
		t.Fatal("reset password")
	}
	assertCode(t, bob.do("GET", "/v1/auth/me", nil), 401, "UNAUTHENTICATED")
	if c, lr := e.login("bobby", rp); lr.Code != 200 || lr.Data["user"].(map[string]any)["must_change_password"] != true {
		t.Fatalf("%s %v", lr.Raw, c)
	}
	// reset MFA clears TOTP and ends sessions
	bob2 := e.user(admin, "carla", "viewer")
	enrollTOTP(t, e, bob2)
	cid := userID(t, e, "carla")
	au := admin.must("POST", "/v1/admin/users/"+cid+"/reset-mfa", nil, 200)
	if au.Data["totp_enabled"] != false || au.Data["recovery_codes_remaining"] != float64(0) || au.Data["passkey_count"] != float64(0) {
		t.Fatalf("%s", au.Raw)
	}
	assertCode(t, bob2.do("GET", "/v1/auth/me", nil), 401, "UNAUTHENTICATED")
	if n := admin.must("POST", "/v1/admin/users/"+cid+"/revoke-sessions", nil, 200).Data["revoked"]; n != float64(0) {
		t.Fatalf("revoked %v", n)
	}
	assertCode(t, admin.do("POST", "/v1/admin/users/00000000-0000-4000-8000-000000000009/unlock", nil), 404, "NOT_FOUND")
	assertCode(t, admin.do("GET", "/v1/admin/users/not-a-uuid", nil), 404, "NOT_FOUND")

	// last administrator protection
	assertCode(t, admin.do("DELETE", "/v1/admin/users/"+adminID, nil), 409, "CANNOT_DELETE_SELF")
	assertCode(t, admin.do("PATCH", "/v1/admin/users/"+adminID, map[string]any{"role": "viewer"}), 409, "LAST_ADMIN")
	assertCode(t, admin.do("PATCH", "/v1/admin/users/"+adminID, map[string]any{"status": "disabled"}), 409, "LAST_ADMIN")
	second := e.user(admin, "root2", "admin")
	second.must("DELETE", "/v1/admin/users/"+adminID, nil, 204) // with another admin around the first may go
	assertCode(t, admin.do("GET", "/v1/auth/me", nil), 401, "UNAUTHENTICATED")
	assertCode(t, second.do("PATCH", "/v1/admin/users/"+userID(t, e, "root2"), map[string]any{"role": "viewer"}), 409, "LAST_ADMIN")
	assertCode(t, second.do("DELETE", "/v1/admin/users/"+userID(t, e, "root2"), nil), 409, "CANNOT_DELETE_SELF")
	acts := e.auditActions()
	for _, a := range []string{"user.create", "user.reset_password", "user.reset_mfa", "user.delete", "auth.password_change", "session.revoke_all"} {
		if acts[a] == 0 {
			t.Errorf("audit missing %s", a)
		}
	}
}

func TestAdminReauthWindow(t *testing.T) {
	e := newAuthEnv(t)
	admin := e.setup(nil)
	victim := e.user(admin, "dave", "viewer")
	_ = victim
	id := userID(t, e, "dave")
	e.clock.Advance(11 * time.Minute)
	for _, tc := range [][3]string{{"POST", "/v1/admin/users/" + id + "/reset-password", ""}, {"POST", "/v1/admin/users/" + id + "/reset-mfa", ""}, {"DELETE", "/v1/admin/users/" + id, ""}, {"PATCH", "/v1/admin/security-settings", "x"}} {
		var body any
		if tc[2] != "" {
			body = map[string]any{"lockout_minutes": 7}
		}
		r := admin.do(tc[0], tc[1], body)
		if r.Code != 403 || r.code() != "FORBIDDEN" || r.Err["details"].(map[string]any)["reason"] != "reauth_required" {
			t.Errorf("%s %s: %d %s", tc[0], tc[1], r.Code, r.Raw)
		}
	}
	if me := admin.must("GET", "/v1/auth/me", nil, 200); me.Data["fresh"] != false {
		t.Fatal("fresh must be false")
	}
	admin.must("POST", "/v1/auth/reauth", map[string]any{"password": adminPW}, 200)
	admin.must("PATCH", "/v1/admin/security-settings", map[string]any{"lockout_minutes": 7}, 200)
}

func TestSecuritySettings(t *testing.T) {
	e := newAuthEnv(t)
	admin := e.setup(map[string]any{"rp_id": "localhost", "origins": []string{baseURL}})
	s := admin.must("GET", "/v1/admin/security-settings", nil, 200)
	if s.Data["mode"] != "enterprise" || s.Data["rp_id"] != "localhost" || s.Data["passkey_available"] != true || s.Data["lockout_threshold"] != float64(5) || s.Data["session_max_hours"] != float64(12) {
		t.Fatalf("%s", s.Raw)
	}
	if r := admin.must("PATCH", "/v1/admin/security-settings", map[string]any{"mfa_policy": "all", "session_idle_minutes": 30, "lockout_threshold": 3, "password_min_length": 14}, 200); r.Data["mfa_policy"] != "all" || r.Data["session_idle_minutes"] != float64(30) || r.Data["password_min_length"] != float64(14) {
		t.Fatalf("%s", r.Raw)
	}
	// ranges
	assertCode(t, admin.do("PATCH", "/v1/admin/security-settings", map[string]any{"lockout_threshold": 1}), 422, "INVALID_ARGUMENT")
	assertCode(t, admin.do("PATCH", "/v1/admin/security-settings", map[string]any{"mfa_policy": "sometimes"}), 422, "INVALID_ARGUMENT")
	assertCode(t, admin.do("PATCH", "/v1/admin/security-settings", map[string]any{"bogus": 1}), 400, "INVALID_JSON")
	// the new minimum length applies to password changes
	assertCode(t, admin.do("POST", "/v1/auth/password", map[string]any{"current_password": adminPW, "new_password": "Short-Pass-1x"}), 422, "WEAK_PASSWORD")
	// passkey reset must be confirmed once passkeys exist
	registerPasskeyFor(t, e, admin)
	assertCode(t, admin.do("PATCH", "/v1/admin/security-settings", map[string]any{"rp_id": "example.org", "origins": []string{"https://example.org"}}), 422, "PASSKEY_RESET_REQUIRED")
	assertCode(t, admin.do("PATCH", "/v1/admin/security-settings", map[string]any{"rp_id": "example.org", "origins": []string{"https://evil.com"}, "confirm_passkey_reset": true}), 422, "INVALID_ARGUMENT")
	// password login cannot be turned off while accounts depend on it
	e.user(admin, "frank", "viewer")
	assertCode(t, admin.do("PATCH", "/v1/admin/security-settings", map[string]any{"allow_password_login": false}), 409, "LOGIN_METHOD_IN_USE")
	r := admin.must("PATCH", "/v1/admin/security-settings", map[string]any{"rp_id": "example.org", "origins": []string{"https://example.org"}, "confirm_passkey_reset": true}, 200)
	if r.Data["rp_id"] != "example.org" {
		t.Fatalf("%s", r.Raw)
	}
	if n, _ := e.st.CountPasskeys(t.Context()); n != 0 {
		t.Fatal("changing the RP ID must drop the passkeys")
	}
	if e.auditActions()["settings.update"] < 2 {
		t.Fatal("settings changes not audited")
	}
	// the public status reports what the login page needs
	if st := e.client().must("GET", "/v1/setup/status", nil, 200); st.Data["allow_password_login"] != true || st.Data["initialized"] != true {
		t.Fatalf("%s", st.Raw)
	}
}

func registerPasskeyFor(t *testing.T, e *authEnv, c *tclient) {
	t.Helper()
	registerPasskey(t, c, newSoft(), "k")
}

func TestNoSecretsInResponses(t *testing.T) {
	e := newAuthEnv(t)
	admin := e.setup(nil)
	r := admin.must("POST", "/v1/admin/users", map[string]any{"username": "erin", "role": "viewer", "credential": "password"}, 201)
	tmp := r.str("temporary_password")
	erin := e.client()
	erin.must("POST", "/v1/auth/login", map[string]any{"username": "erin", "password": tmp}, 200)
	erin.must("POST", "/v1/auth/password", map[string]any{"current_password": tmp, "new_password": adminPW}, 204)
	secret, codes := enrollTOTP(t, e, erin)
	for _, p := range []string{"/v1/admin/users", "/v1/admin/users/" + userID(t, e, "erin"), "/v1/admin/sessions", "/v1/admin/security-settings", "/v1/auth/me", "/v1/auth/security", "/v1/auth/sessions"} {
		admin.do("GET", p, nil)
		erin.do("GET", p, nil)
	}
	var all strings.Builder
	for _, b := range append(*admin.bodies, *erin.bodies...) {
		all.Write(b)
		all.WriteByte('\n')
	}
	text := all.String()
	for _, bad := range []string{"password_hash", "$argon2", "totp_secret", "secret_enc", "code_hash", "webauthn_handle", "public_key", "setup_token_hash"} {
		if strings.Contains(text, bad) {
			t.Errorf("responses mention %q", bad)
		}
	}
	// the one-time values appear exactly where they are issued: temp password once, recovery codes once, TOTP secret once
	if strings.Count(text, tmp) != 1 || strings.Count(text, secret) != 2 { // secret + otpauth_uri of the single begin response
		t.Errorf("one-time secrets repeated: tmp=%d secret=%d", strings.Count(text, tmp), strings.Count(text, secret))
	}
	for _, c := range codes {
		if strings.Count(text, c) != 1 {
			t.Errorf("recovery code shown %d times", strings.Count(text, c))
		}
	}
	if strings.Contains(text, admin.cookie) || strings.Contains(text, erin.cookie) {
		t.Error("session cookie value appears in a response body")
	}
	// cookie values are not stored either
	var n int
	_ = e.st.Pool.QueryRow(t.Context(), `SELECT count(*) FROM sessions WHERE id=$1 OR id=$2`, admin.cookie, erin.cookie).Scan(&n)
	if n != 0 {
		t.Error("session ids must be hashes")
	}
}
