package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"oa-agent/backend/internal/auth"
	"oa-agent/backend/internal/auth/softauth"
)

// enrollTOTP enrols TOTP for the signed-in client; it returns the secret and recovery codes.
func enrollTOTP(t *testing.T, e *authEnv, c *tclient) (string, []string) {
	t.Helper()
	b := c.must("POST", "/v1/auth/totp/begin", nil, 200)
	secret := b.str("secret")
	if !strings.HasPrefix(b.str("otpauth_uri"), "otpauth://totp/") || b.Data["digits"] != float64(6) || b.Data["period"] != float64(30) || b.Data["algorithm"] != "SHA1" {
		t.Fatalf("%s", b.Raw)
	}
	code, _ := auth.TOTPCode(secret, e.clock.Now())
	assertCode(t, c.do("POST", "/v1/auth/totp/confirm", map[string]any{"challenge_id": b.str("challenge_id"), "code": "000000"}), 422, "INVALID_CODE")
	r := c.must("POST", "/v1/auth/totp/confirm", map[string]any{"challenge_id": b.str("challenge_id"), "code": code}, 200)
	var codes []string
	for _, x := range r.Data["recovery_codes"].([]any) {
		codes = append(codes, x.(string))
	}
	return secret, codes
}

func TestTOTPEnrolmentLoginReplayAndRecovery(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)
	e := newAuthEnv(t)
	admin := e.setup(map[string]any{"mfa_policy": "admins"})
	secret, codes := enrollTOTP(t, e, admin)
	if len(codes) != 10 {
		t.Fatalf("recovery codes: %d", len(codes))
	}
	me := admin.must("GET", "/v1/auth/me", nil, 200)
	if me.Data["scope"] != "full" || me.Data["mfa"] != "totp" {
		t.Fatalf("enrolment must lift the restriction: %v", me.Data)
	}
	if sec := admin.must("GET", "/v1/auth/security", nil, 200); sec.Data["totp_enabled"] != true || sec.Data["recovery_codes_remaining"] != float64(10) || sec.Data["mfa_required_by_policy"] != true {
		t.Fatalf("%s", sec.Raw)
	}
	// the secret is never shown again
	if contains(admin.must("GET", "/v1/auth/security", nil, 200).Raw, secret) {
		t.Fatal("TOTP secret leaked")
	}
	var enc []byte
	_ = e.st.Pool.QueryRow(t.Context(), `SELECT totp_secret_enc FROM users WHERE username='admin'`).Scan(&enc)
	if len(enc) == 0 || bytes.Contains(enc, []byte(secret)) {
		t.Fatal("secret must be stored encrypted")
	}
	// cannot enrol twice
	assertCode(t, admin.must0("POST", "/v1/auth/totp/begin"), 409, "TOTP_ALREADY_ENABLED")
	admin.must("POST", "/v1/auth/logout", nil, 204)

	// login → mfa_required (no session yet)
	c := e.client()
	lr := c.must("POST", "/v1/auth/login", map[string]any{"username": "admin", "password": adminPW}, 200)
	if lr.str("status") != "mfa_required" || lr.str("mfa_token") == "" || c.cookie != "" || len(lr.Header.Values("Set-Cookie")) != 0 {
		t.Fatalf("%s", lr.Raw)
	}
	methods := lr.Data["methods"].([]any)
	if len(methods) != 2 || methods[0] != "totp" || methods[1] != "recovery" {
		t.Fatalf("methods %v", methods)
	}
	tok := lr.str("mfa_token")
	assertCode(t, c.do("POST", "/v1/auth/mfa/totp", map[string]any{"mfa_token": tok, "code": "123456"}), 401, "INVALID_CREDENTIALS")
	assertCode(t, c.do("POST", "/v1/auth/mfa/totp", map[string]any{"mfa_token": "bogus", "code": "123456"}), 401, "MFA_TOKEN_INVALID")
	// the code used while enrolling is a replay; the next step works
	used, _ := auth.TOTPCode(secret, e.clock.Now())
	assertCode(t, c.do("POST", "/v1/auth/mfa/totp", map[string]any{"mfa_token": tok, "code": used}), 401, "INVALID_CREDENTIALS")
	e.clock.Advance(31 * time.Second)
	next, _ := auth.TOTPCode(secret, e.clock.Now())
	ok := c.must("POST", "/v1/auth/mfa/totp", map[string]any{"mfa_token": tok, "code": next}, 200)
	if ok.str("status") != "ok" || c.cookie == "" || ok.str("csrf_token") == "" {
		t.Fatalf("%s", ok.Raw)
	}
	if c.must("GET", "/v1/auth/me", nil, 200).Data["mfa"] != "totp" {
		t.Fatal("session mfa")
	}
	// mfa_token is one-time; the accepted code cannot be replayed on a new login
	assertCode(t, e.client().do("POST", "/v1/auth/mfa/totp", map[string]any{"mfa_token": tok, "code": next}), 401, "MFA_TOKEN_INVALID")
	c2 := e.client()
	t2 := c2.must("POST", "/v1/auth/login", map[string]any{"username": "admin", "password": adminPW}, 200).str("mfa_token")
	assertCode(t, c2.do("POST", "/v1/auth/mfa/totp", map[string]any{"mfa_token": t2, "code": next}), 401, "INVALID_CREDENTIALS")
	// ±1 window (previous step is older than the last used one → refused; the next step is fine)
	e.clock.Advance(30 * time.Second)
	later, _ := auth.TOTPCode(secret, e.clock.Now())
	c2.must("POST", "/v1/auth/mfa/totp", map[string]any{"mfa_token": t2, "code": later}, 200)

	// recovery codes: one-time
	c3 := e.client()
	t3 := c3.must("POST", "/v1/auth/login", map[string]any{"username": "admin", "password": adminPW}, 200).str("mfa_token")
	c3.must("POST", "/v1/auth/mfa/recovery", map[string]any{"mfa_token": t3, "code": strings.ToLower(codes[0])}, 200)
	if c3.must("GET", "/v1/auth/me", nil, 200).Data["mfa"] != "recovery" || c3.must("GET", "/v1/auth/security", nil, 200).Data["recovery_codes_remaining"] != float64(9) {
		t.Fatal("recovery login")
	}
	c4 := e.client()
	t4 := c4.must("POST", "/v1/auth/login", map[string]any{"username": "admin", "password": adminPW}, 200).str("mfa_token")
	assertCode(t, c4.do("POST", "/v1/auth/mfa/recovery", map[string]any{"mfa_token": t4, "code": codes[0]}), 401, "INVALID_CREDENTIALS")
	c4.must("POST", "/v1/auth/mfa/recovery", map[string]any{"mfa_token": t4, "code": codes[1]}, 200)

	// regenerating needs recent auth and kills the old codes
	e.clock.Advance(11 * time.Minute)
	r := c3.do("POST", "/v1/auth/recovery-codes", nil)
	if r.Code != 403 || r.code() != "FORBIDDEN" || r.Err["details"].(map[string]any)["reason"] != "reauth_required" {
		t.Fatalf("%s", r.Raw)
	}
	assertCode(t, c3.do("POST", "/v1/auth/reauth", map[string]any{"password": "wrong-Password-1"}), 401, "INVALID_CREDENTIALS")
	if me := c3.must("POST", "/v1/auth/reauth", map[string]any{"password": adminPW}, 200); me.Data["fresh"] != true {
		t.Fatal("reauth must refresh")
	}
	fresh := c3.must("POST", "/v1/auth/recovery-codes", nil, 200).Data["recovery_codes"].([]any)
	if len(fresh) != 10 {
		t.Fatal("10 new codes")
	}
	c5 := e.client()
	t5 := c5.must("POST", "/v1/auth/login", map[string]any{"username": "admin", "password": adminPW}, 200).str("mfa_token")
	assertCode(t, c5.do("POST", "/v1/auth/mfa/recovery", map[string]any{"mfa_token": t5, "code": codes[2]}), 401, "INVALID_CREDENTIALS")
	// the last MFA cannot be removed while the policy requires one
	r = c3.do("DELETE", "/v1/auth/totp", nil)
	assertCode(t, r, 409, "MFA_REQUIRED_BY_POLICY")
	// five wrong codes lock the account and burn the token
	c6 := e.client()
	t6 := c6.must("POST", "/v1/auth/login", map[string]any{"username": "admin", "password": adminPW}, 200).str("mfa_token")
	locked := false
	for i := 0; i < 5; i++ {
		if c6.do("POST", "/v1/auth/mfa/totp", map[string]any{"mfa_token": t6, "code": "111111"}).code() == "ACCOUNT_LOCKED" {
			locked = true
		}
	}
	if !locked {
		t.Fatal("repeated wrong codes must lock the account")
	}
	good, _ := auth.TOTPCode(secret, e.clock.Now().Add(time.Minute))
	if r := c6.do("POST", "/v1/auth/mfa/totp", map[string]any{"mfa_token": t6, "code": good}); r.Code == 200 {
		t.Fatal("locked account must not log in")
	}
	acts := e.auditActions()
	for _, a := range []string{"totp.enable", "auth.mfa_failed", "auth.recovery_used", "recovery.regenerate", "auth.lockout"} {
		if acts[a] == 0 {
			t.Errorf("audit missing %s", a)
		}
	}
	// nothing secret ended up in the log
	for _, s := range append(append(codes, secret, adminPW, admin.cookie, admin.csrf, c.cookie, c3.cookie), fresh[0].(string)) {
		if s != "" && strings.Contains(logs.String(), s) {
			t.Fatalf("secret material in the log: %q", s)
		}
	}
}

func (c *tclient) must0(method, path string) resp { return c.do(method, path, nil) }

// passkey helpers
func optionsJSON(t *testing.T, r resp) ([]byte, string) {
	t.Helper()
	var d struct {
		Data struct {
			ChallengeID string          `json:"challenge_id"`
			Options     json.RawMessage `json:"options"`
		} `json:"data"`
	}
	if err := json.Unmarshal(r.Raw, &d); err != nil || d.Data.ChallengeID == "" || len(d.Data.Options) == 0 {
		t.Fatalf("options: %s", r.Raw)
	}
	return d.Data.Options, d.Data.ChallengeID
}

func passkeySetup(t *testing.T) (*authEnv, *tclient) {
	e := newAuthEnv(t)
	admin := e.setup(map[string]any{"mfa_policy": "admins", "rp_id": "localhost", "origins": []string{baseURL}})
	return e, admin
}

func registerPasskey(t *testing.T, c *tclient, a *softauth.Authenticator, name string) resp {
	t.Helper()
	opts, id := optionsJSON(t, c.must("POST", "/v1/auth/passkeys/begin", nil, 200))
	cred, err := a.Create(opts, baseURL)
	if err != nil {
		t.Fatal(err)
	}
	return c.must("POST", "/v1/auth/passkeys/finish", map[string]any{"challenge_id": id, "name": name, "credential": json.RawMessage(cred)}, 200)
}

func TestPasskeyRegistrationAndPasswordlessLogin(t *testing.T) {
	e, admin := passkeySetup(t)
	// options follow the design: discoverable + user verification required + no attestation, random 64-byte handle
	opts, id := optionsJSON(t, admin.must("POST", "/v1/auth/passkeys/begin", nil, 200))
	var o struct {
		Attestation string `json:"attestation"`
		RP          struct{ ID string }
		User        struct{ ID, Name string }
		Selection   struct{ ResidentKey, UserVerification string } `json:"authenticatorSelection"`
	}
	_ = json.Unmarshal(opts, &o)
	h, _ := base64.RawURLEncoding.DecodeString(o.User.ID)
	if o.Attestation != "none" || o.RP.ID != "localhost" || o.Selection.ResidentKey != "required" || o.Selection.UserVerification != "required" || len(h) != 64 || o.User.Name != "admin" || strings.Contains(string(h), "admin") {
		t.Fatalf("options: %s", opts)
	}
	// an authenticator that skips user verification is refused
	weak := softauth.New()
	weak.NoUV = true
	cred, _ := weak.Create(opts, baseURL)
	assertCode(t, admin.do("POST", "/v1/auth/passkeys/finish", map[string]any{"challenge_id": id, "credential": json.RawMessage(cred)}), 422, "INVALID_CREDENTIAL")
	// wrong origin and replayed challenge
	a := softauth.New()
	opts, id = optionsJSON(t, admin.must("POST", "/v1/auth/passkeys/begin", nil, 200))
	cred, _ = a.Create(opts, "https://evil.example")
	assertCode(t, admin.do("POST", "/v1/auth/passkeys/finish", map[string]any{"challenge_id": id, "credential": json.RawMessage(cred)}), 422, "INVALID_CREDENTIAL")
	r := registerPasskey(t, admin, a, "笔记本")
	if len(r.Data["recovery_codes"].([]any)) != 10 || r.Data["passkey"].(map[string]any)["name"] != "笔记本" || r.Data["me"].(map[string]any)["scope"] != "full" {
		t.Fatalf("%s", r.Raw)
	}
	// already registered authenticators are excluded
	opts, _ = optionsJSON(t, admin.must("POST", "/v1/auth/passkeys/begin", nil, 200))
	if _, err := a.Create(opts, baseURL); err == nil {
		t.Fatal("duplicate registration must be excluded")
	}
	pid := r.Data["passkey"].(map[string]any)["id"].(string)
	if rn := admin.must("PATCH", "/v1/auth/passkeys/"+pid, map[string]any{"name": "新名字"}, 200); rn.str("name") != "新名字" {
		t.Fatal("rename")
	}
	assertCode(t, admin.do("PATCH", "/v1/auth/passkeys/AAAA", map[string]any{"name": "x"}), 404, "NOT_FOUND")
	admin.must("POST", "/v1/auth/logout", nil, 204)

	// passwordless: no username, UV, session mfa=passkey
	c := e.client()
	opts, id = optionsJSON(t, c.must("POST", "/v1/auth/passkey/login/begin", nil, 200))
	assertion, err := a.Get(opts, baseURL)
	if err != nil {
		t.Fatal(err)
	}
	// tampered signature
	var tam map[string]any
	_ = json.Unmarshal(assertion, &tam)
	tam["response"].(map[string]any)["signature"] = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 70))
	bad, _ := json.Marshal(tam)
	assertCode(t, c.do("POST", "/v1/auth/passkey/login/finish", map[string]any{"challenge_id": id, "credential": json.RawMessage(bad)}), 401, "INVALID_CREDENTIALS")
	// the challenge was burnt by the failed attempt
	assertCode(t, c.do("POST", "/v1/auth/passkey/login/finish", map[string]any{"challenge_id": id, "credential": json.RawMessage(assertion)}), 401, "INVALID_CREDENTIALS")
	opts, id = optionsJSON(t, c.must("POST", "/v1/auth/passkey/login/begin", nil, 200))
	assertion, _ = a.Get(opts, baseURL)
	lr := c.must("POST", "/v1/auth/passkey/login/finish", map[string]any{"challenge_id": id, "credential": json.RawMessage(assertion)}, 200)
	if lr.str("status") != "ok" || c.cookie == "" || lr.Data["user"].(map[string]any)["username"] != "admin" {
		t.Fatalf("%s", lr.Raw)
	}
	if me := c.must("GET", "/v1/auth/me", nil, 200); me.Data["mfa"] != "passkey" || me.Data["scope"] != "full" {
		t.Fatalf("%v", me.Data)
	}
	// replayed assertion (challenge consumed)
	assertCode(t, e.client().do("POST", "/v1/auth/passkey/login/finish", map[string]any{"challenge_id": id, "credential": json.RawMessage(assertion)}), 401, "INVALID_CREDENTIALS")
	// counters are tracked; a stuck counter looks like a clone and is refused
	sec := c.must("GET", "/v1/auth/security", nil, 200)
	if sec.Data["passkeys"].([]any)[0].(map[string]any)["last_used_at"] == nil {
		t.Fatal("last_used_at")
	}
	a.Fixed = true
	cc := e.client()
	opts, id = optionsJSON(t, cc.must("POST", "/v1/auth/passkey/login/begin", nil, 200))
	assertion, _ = a.Get(opts, baseURL)
	assertCode(t, cc.do("POST", "/v1/auth/passkey/login/finish", map[string]any{"challenge_id": id, "credential": json.RawMessage(assertion)}), 401, "INVALID_CREDENTIALS")
	if e.auditActions()["passkey.clone_suspected"] != 1 {
		t.Fatal("clone detection not audited")
	}
	// disabled users cannot use a passkey either
	a.Fixed = false
	me := c.must("GET", "/v1/auth/me", nil, 200).Data["user"].(map[string]any)
	e.st.Pool.Exec(t.Context(), `UPDATE users SET status='disabled' WHERE id=$1`, me["id"])
	a.Counter += 5
	dc := e.client()
	opts, id = optionsJSON(t, dc.must("POST", "/v1/auth/passkey/login/begin", nil, 200))
	assertion, _ = a.Get(opts, baseURL)
	assertCode(t, dc.do("POST", "/v1/auth/passkey/login/finish", map[string]any{"challenge_id": id, "credential": json.RawMessage(assertion)}), 401, "INVALID_CREDENTIALS")
}

func TestPasskeyAsSecondFactorAndDeletionRules(t *testing.T) {
	e, admin := passkeySetup(t)
	a := softauth.New()
	registerPasskey(t, admin, a, "key1")
	admin.must("POST", "/v1/auth/logout", nil, 204)
	c := e.client()
	lr := c.must("POST", "/v1/auth/login", map[string]any{"username": "admin", "password": adminPW}, 200)
	if lr.str("status") != "mfa_required" {
		t.Fatal(string(lr.Raw))
	}
	if m := lr.Data["methods"].([]any); m[0] != "passkey" {
		t.Fatalf("%v", m)
	}
	tok := lr.str("mfa_token")
	opts, id := optionsJSON(t, c.must("POST", "/v1/auth/mfa/passkey/begin", map[string]any{"mfa_token": tok}, 200))
	assertion, _ := a.Get(opts, baseURL)
	// a challenge from one mfa_token cannot finish another login
	other := e.client()
	tok2 := other.must("POST", "/v1/auth/login", map[string]any{"username": "admin", "password": adminPW}, 200).str("mfa_token")
	assertCode(t, other.do("POST", "/v1/auth/mfa/passkey/finish", map[string]any{"mfa_token": tok2, "challenge_id": id, "credential": json.RawMessage(assertion)}), 401, "MFA_TOKEN_INVALID")
	opts, id = optionsJSON(t, c.must("POST", "/v1/auth/mfa/passkey/begin", map[string]any{"mfa_token": tok}, 200))
	assertion, _ = a.Get(opts, baseURL)
	ok := c.must("POST", "/v1/auth/mfa/passkey/finish", map[string]any{"mfa_token": tok, "challenge_id": id, "credential": json.RawMessage(assertion)}, 200)
	if ok.str("status") != "ok" || c.must("GET", "/v1/auth/me", nil, 200).Data["mfa"] != "passkey" {
		t.Fatalf("%s", ok.Raw)
	}
	assertCode(t, c.do("POST", "/v1/auth/mfa/passkey/finish", map[string]any{"mfa_token": tok, "challenge_id": id, "credential": json.RawMessage(assertion)}), 401, "MFA_TOKEN_INVALID")
	// deleting needs recent auth; policy keeps the last MFA
	pid := c.must("GET", "/v1/auth/security", nil, 200).Data["passkeys"].([]any)[0].(map[string]any)["id"].(string)
	e.clock.Advance(11 * time.Minute)
	r := c.do("DELETE", "/v1/auth/passkeys/"+pid, nil)
	if r.Code != 403 || r.Err["details"].(map[string]any)["reason"] != "reauth_required" {
		t.Fatalf("%s", r.Raw)
	}
	// passkey re-authentication
	opts, id = optionsJSON(t, c.must("POST", "/v1/auth/reauth/passkey/begin", nil, 200))
	assertion, _ = a.Get(opts, baseURL)
	if me := c.must("POST", "/v1/auth/reauth/passkey/finish", map[string]any{"challenge_id": id, "credential": json.RawMessage(assertion)}, 200); me.Data["fresh"] != true {
		t.Fatal("fresh")
	}
	assertCode(t, c.do("DELETE", "/v1/auth/passkeys/"+pid, nil), 409, "MFA_REQUIRED_BY_POLICY")
	// with TOTP as well, the passkey may go
	enrollTOTP(t, e, c)
	c.must("DELETE", "/v1/auth/passkeys/"+pid, nil, 204)
	assertCode(t, c.do("DELETE", "/v1/auth/passkeys/"+pid, nil), 404, "NOT_FOUND")
}

func TestInvitationFlow(t *testing.T) {
	e, admin := passkeySetup(t)
	registerPasskey(t, admin, softauth.New(), "admin-key") // admin now has MFA (policy admins)
	admin.must("POST", "/v1/setup/finish", nil, 200)
	mk := func(name string) (string, string) {
		r := admin.must("POST", "/v1/admin/users", map[string]any{"username": name, "display_name": name, "role": "operator", "credential": "passkey_invite"}, 201)
		inv := r.Data["invite"].(map[string]any)
		if r.Data["temporary_password"] != nil || r.Data["user"].(map[string]any)["has_password"] != false || !strings.HasPrefix(inv["path"].(string), "/invite/"+inv["token"].(string)) {
			t.Fatalf("%s", r.Raw)
		}
		return inv["token"].(string), r.Data["user"].(map[string]any)["id"].(string)
	}
	tok, uid := mk("ivan")
	anon := e.client()
	if r := anon.must("GET", "/v1/auth/invitations/"+tok, nil, 200); r.str("username") != "ivan" || r.Data["passkey_available"] != true {
		t.Fatalf("%s", r.Raw)
	}
	assertCode(t, anon.do("GET", "/v1/auth/invitations/not-a-token", nil), 404, "NOT_FOUND")
	// the stored value is a hash
	var n int
	_ = e.st.Pool.QueryRow(t.Context(), `SELECT count(*) FROM invitations WHERE token_hash=$1`, tok).Scan(&n)
	if n != 0 {
		t.Fatal("invitation token stored in plaintext")
	}
	a := softauth.New()
	opts, id := optionsJSON(t, anon.must("POST", "/v1/auth/invitations/"+tok+"/passkey/begin", nil, 200))
	cred, _ := a.Create(opts, baseURL)
	lr := anon.must("POST", "/v1/auth/invitations/"+tok+"/passkey/finish", map[string]any{"challenge_id": id, "name": "ivan-phone", "credential": json.RawMessage(cred)}, 200)
	if lr.str("status") != "ok" || anon.cookie == "" || lr.Data["user"].(map[string]any)["username"] != "ivan" {
		t.Fatalf("%s", lr.Raw)
	}
	// one-time
	assertCode(t, e.client().do("GET", "/v1/auth/invitations/"+tok, nil), 404, "NOT_FOUND")
	assertCode(t, e.client().do("POST", "/v1/auth/invitations/"+tok+"/passkey/begin", nil), 404, "NOT_FOUND")
	assertCode(t, e.client().do("POST", "/v1/auth/invitations/"+tok+"/passkey/finish", map[string]any{"challenge_id": id, "credential": json.RawMessage(cred)}), 404, "NOT_FOUND")
	// a new invitation replaces an older one; expiry after 24 hours
	t1, _ := mk("jane")
	t2 := admin.must("POST", "/v1/admin/users/"+func() string { u, _ := e.st.AuthUserByName(t.Context(), "jane"); return u.ID }()+"/invite", nil, 201).Data["token"].(string)
	assertCode(t, e.client().do("GET", "/v1/auth/invitations/"+t1, nil), 404, "NOT_FOUND")
	e.clock.Advance(25 * time.Hour)
	assertCode(t, e.client().do("GET", "/v1/auth/invitations/"+t2, nil), 404, "NOT_FOUND")
	_ = uid
	if e.auditActions()["invite.accept"] != 1 {
		t.Fatal("audit")
	}
}

func newSoft() *softauth.Authenticator { return softauth.New() }
