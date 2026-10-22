package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"oa-agent/backend/internal/auth"
	"oa-agent/backend/internal/store"
)

func (s *Server) authRoutes() {
	s.handle("/v1/auth/login", map[string]http.HandlerFunc{"POST": s.login})
	s.handle("/v1/auth/logout", map[string]http.HandlerFunc{"POST": s.logout})
	s.handle("/v1/auth/me", map[string]http.HandlerFunc{"GET": s.me})
	s.handle("/v1/auth/mfa/totp", map[string]http.HandlerFunc{"POST": s.mfaTotp})
	s.handle("/v1/auth/mfa/recovery", map[string]http.HandlerFunc{"POST": s.mfaRecovery})
	s.handle("/v1/auth/mfa/passkey/begin", map[string]http.HandlerFunc{"POST": s.mfaPasskeyBegin})
	s.handle("/v1/auth/mfa/passkey/finish", map[string]http.HandlerFunc{"POST": s.mfaPasskeyFinish})
	s.handle("/v1/auth/passkey/login/begin", map[string]http.HandlerFunc{"POST": s.passkeyLoginBegin})
	s.handle("/v1/auth/passkey/login/finish", map[string]http.HandlerFunc{"POST": s.passkeyLoginFinish})
	s.handle("/v1/auth/reauth", map[string]http.HandlerFunc{"POST": s.reauth})
	s.handle("/v1/auth/reauth/passkey/begin", map[string]http.HandlerFunc{"POST": s.reauthPasskeyBegin})
	s.handle("/v1/auth/reauth/passkey/finish", map[string]http.HandlerFunc{"POST": s.reauthPasskeyFinish})
	s.handle("/v1/auth/password", map[string]http.HandlerFunc{"POST": s.changePassword})
	s.handle("/v1/auth/security", map[string]http.HandlerFunc{"GET": s.mySecurity})
	s.handle("/v1/auth/totp/begin", map[string]http.HandlerFunc{"POST": s.totpBegin})
	s.handle("/v1/auth/totp/confirm", map[string]http.HandlerFunc{"POST": s.totpConfirm})
	s.handle("/v1/auth/totp", map[string]http.HandlerFunc{"DELETE": s.totpDisable})
	s.handle("/v1/auth/passkeys/begin", map[string]http.HandlerFunc{"POST": s.passkeyRegisterBegin})
	s.handle("/v1/auth/passkeys/finish", map[string]http.HandlerFunc{"POST": s.passkeyRegisterFinish})
	s.handle("/v1/auth/passkeys/{id}", map[string]http.HandlerFunc{"PATCH": s.passkeyRename, "DELETE": s.passkeyDelete})
	s.handle("/v1/auth/recovery-codes", map[string]http.HandlerFunc{"POST": s.recoveryCodes})
	s.handle("/v1/auth/sessions", map[string]http.HandlerFunc{"GET": s.mySessions})
	s.handle("/v1/auth/sessions/{id}", map[string]http.HandlerFunc{"DELETE": s.revokeMySession})
	s.handle("/v1/auth/invitations/{token}", map[string]http.HandlerFunc{"GET": s.invitation})
	s.handle("/v1/auth/invitations/{token}/passkey/begin", map[string]http.HandlerFunc{"POST": s.invitationPasskeyBegin})
	s.handle("/v1/auth/invitations/{token}/passkey/finish", map[string]http.HandlerFunc{"POST": s.invitationPasskeyFinish})
}

// authFail answers with the status and code the auth service chose; unexpected errors become a generic 500.
func (s *Server) authFail(w http.ResponseWriter, r *http.Request, err error) {
	var ae *auth.Error
	if !errors.As(err, &ae) {
		s.storeErr(w, r, err, "")
		return
	}
	if ae.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int((ae.RetryAfter+time.Second-1)/time.Second)))
	}
	s.fail(w, r, ae.Status, ae.Code, ae.Message, ae.Details)
}

func (s *Server) client(r *http.Request) auth.Client {
	return auth.Client{IP: s.clientIP(r), UserAgent: r.UserAgent()}
}

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", Expires: expires, MaxAge: int(time.Until(expires).Seconds()), HttpOnly: true, Secure: s.isSecure(r), SameSite: http.SameSiteStrictMode})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, Expires: time.Unix(0, 0), HttpOnly: true, Secure: s.isSecure(r), SameSite: http.SameSiteStrictMode})
}

// --- views

func sessionUser(u store.AuthUser) map[string]any {
	return map[string]any{"id": u.ID, "username": u.Username, "display_name": u.DisplayName, "role": u.Role, "must_change_password": u.MustChangePassword}
}

func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func rfc3339p(t *time.Time) any {
	if t == nil {
		return nil
	}
	return rfc3339(*t)
}

func policyView(set store.AuthSettings) map[string]any {
	return map[string]any{"mode": set.Mode, "mfa_policy": set.MFAPolicy, "allow_password_login": set.AllowPasswordLogin, "passkey_available": auth.PasskeyAvailable(set), "password_min_length": set.PasswordMinLength}
}

func meView(id auth.Identity, now time.Time) map[string]any {
	csrf := ""
	if !id.Local {
		csrf = auth.CSRFToken(id.Token)
	}
	return map[string]any{
		"user": sessionUser(id.User), "csrf_token": csrf, "scope": id.Session.Scope, "mfa": id.Session.MFA, "fresh": id.Fresh(now),
		"session_expires_at": rfc3339(id.SessionExpiry()), "policy": policyView(id.Settings),
	}
}

func passkeyView(c store.WebAuthnCred) map[string]any {
	return map[string]any{"id": base64.RawURLEncoding.EncodeToString(c.ID), "name": c.Name, "created_at": rfc3339(c.CreatedAt), "last_used_at": rfc3339p(c.LastUsedAt),
		"backup_eligible": c.BackupEligible, "backup_state": c.BackupState, "transports": c.Transports}
}

func sessionInfoView(si store.AuthSessionInfo, current string) map[string]any {
	return map[string]any{"id": si.ID, "user_id": si.UserID, "username": si.Username, "created_at": rfc3339(si.CreatedAt), "last_seen_at": rfc3339(si.LastSeenAt),
		"expires_at": rfc3339(si.ExpiresAt), "ip": si.IP, "user_agent": si.UserAgent, "mfa": si.MFA, "current": si.ID == current}
}

// writeOutcome sends a login result; full and restricted sessions set the cookie (and retire the old one).
func (s *Server) writeOutcome(w http.ResponseWriter, r *http.Request, status int, o auth.Outcome) {
	if o.Status == "mfa_required" {
		s.ok(w, status, map[string]any{"status": "mfa_required", "mfa_token": o.MFAToken, "methods": o.Methods})
		return
	}
	if old, err := r.Cookie(sessionCookie); err == nil && old.Value != "" && old.Value != o.SessionToken {
		_, _ = s.d.Auth.Store.DeleteSession(r.Context(), auth.HashToken(old.Value))
	}
	body := map[string]any{"status": o.Status, "user": sessionUser(o.User)}
	if o.SessionToken != "" {
		s.setSessionCookie(w, r, o.SessionToken, o.Session.ExpiresAt)
		body["csrf_token"] = auth.CSRFToken(o.SessionToken)
		body["session_expires_at"] = rfc3339(o.Expires)
	}
	s.ok(w, status, body)
}

func (s *Server) identity(w http.ResponseWriter, r *http.Request) (auth.Identity, bool) {
	id, ok := identityOf(r)
	if !ok {
		s.fail(w, r, 401, "UNAUTHENTICATED", "请先登录", nil)
	}
	return id, ok
}

func (s *Server) webAuthnOptions(w http.ResponseWriter, o auth.WebAuthnOptions) {
	s.ok(w, 200, map[string]any{"challenge_id": o.ChallengeID, "options": o.Options})
}

// --- login

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var in struct{ Username, Password string }
	if !s.decode(w, r, &in) {
		return
	}
	o, err := s.d.Auth.Login(r.Context(), s.client(r), in.Username, in.Password)
	if err != nil {
		s.authFail(w, r, err)
		return
	}
	s.writeOutcome(w, r, 200, o)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	id, ok := s.identity(w, r)
	if !ok {
		return
	}
	s.d.Auth.Logout(r.Context(), id)
	s.clearSessionCookie(w, r)
	w.WriteHeader(204)
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	id, ok := s.identity(w, r)
	if !ok {
		return
	}
	s.ok(w, 200, meView(id, s.d.Auth.Now()))
}

func (s *Server) mfaTotp(w http.ResponseWriter, r *http.Request) {
	var in struct {
		MFAToken string `json:"mfa_token"`
		Code     string `json:"code"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	o, err := s.d.Auth.MFATOTP(r.Context(), s.client(r), in.MFAToken, in.Code)
	if err != nil {
		s.authFail(w, r, err)
		return
	}
	s.writeOutcome(w, r, 200, o)
}

func (s *Server) mfaRecovery(w http.ResponseWriter, r *http.Request) {
	var in struct {
		MFAToken string `json:"mfa_token"`
		Code     string `json:"code"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	o, err := s.d.Auth.MFARecovery(r.Context(), s.client(r), in.MFAToken, in.Code)
	if err != nil {
		s.authFail(w, r, err)
		return
	}
	s.writeOutcome(w, r, 200, o)
}

func (s *Server) mfaPasskeyBegin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		MFAToken string `json:"mfa_token"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	o, err := s.d.Auth.MFAPasskeyBegin(r.Context(), s.client(r), in.MFAToken)
	if err != nil {
		s.authFail(w, r, err)
		return
	}
	s.webAuthnOptions(w, o)
}

type credentialBody struct {
	MFAToken    string          `json:"mfa_token"`
	ChallengeID string          `json:"challenge_id"`
	Name        string          `json:"name"`
	Credential  json.RawMessage `json:"credential"`
}

func (s *Server) mfaPasskeyFinish(w http.ResponseWriter, r *http.Request) {
	var in credentialBody
	if !s.decode(w, r, &in) {
		return
	}
	o, err := s.d.Auth.MFAPasskeyFinish(r.Context(), s.client(r), in.MFAToken, in.ChallengeID, in.Credential)
	if err != nil {
		s.authFail(w, r, err)
		return
	}
	s.writeOutcome(w, r, 200, o)
}

func (s *Server) passkeyLoginBegin(w http.ResponseWriter, r *http.Request) {
	o, err := s.d.Auth.PasskeyLoginBegin(r.Context(), s.client(r))
	if err != nil {
		s.authFail(w, r, err)
		return
	}
	s.webAuthnOptions(w, o)
}

func (s *Server) passkeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	var in credentialBody
	if !s.decode(w, r, &in) {
		return
	}
	o, err := s.d.Auth.PasskeyLoginFinish(r.Context(), s.client(r), in.ChallengeID, in.Credential)
	if err != nil {
		s.authFail(w, r, err)
		return
	}
	s.writeOutcome(w, r, 200, o)
}

// --- re-authentication and password

// meAfter reloads the identity (the session may have changed) for responses that echo /me.
func (s *Server) meAfter(r *http.Request, id auth.Identity) map[string]any {
	if !id.Local {
		if sess, _, err := s.d.Auth.Store.SessionWithUser(r.Context(), id.Session.ID); err == nil {
			id.Session = sess
		}
	}
	return meView(id, s.d.Auth.Now())
}

func (s *Server) reauth(w http.ResponseWriter, r *http.Request) {
	id, ok := s.identity(w, r)
	if !ok {
		return
	}
	var in struct{ Password string }
	if !s.decode(w, r, &in) {
		return
	}
	if err := s.d.Auth.Reauth(r.Context(), s.client(r), id, in.Password); err != nil {
		s.authFail(w, r, err)
		return
	}
	s.ok(w, 200, s.meAfter(r, id))
}

func (s *Server) reauthPasskeyBegin(w http.ResponseWriter, r *http.Request) {
	id, ok := s.identity(w, r)
	if !ok {
		return
	}
	o, err := s.d.Auth.ReauthPasskeyBegin(r.Context(), s.client(r), id)
	if err != nil {
		s.authFail(w, r, err)
		return
	}
	s.webAuthnOptions(w, o)
}

func (s *Server) reauthPasskeyFinish(w http.ResponseWriter, r *http.Request) {
	id, ok := s.identity(w, r)
	if !ok {
		return
	}
	var in credentialBody
	if !s.decode(w, r, &in) {
		return
	}
	if err := s.d.Auth.ReauthPasskeyFinish(r.Context(), s.client(r), id, in.ChallengeID, in.Credential); err != nil {
		s.authFail(w, r, err)
		return
	}
	s.ok(w, 200, s.meAfter(r, id))
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	id, ok := s.identity(w, r)
	if !ok {
		return
	}
	var in struct {
		Current string `json:"current_password"`
		New     string `json:"new_password"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if err := s.d.Auth.ChangePassword(r.Context(), s.client(r), id, in.Current, in.New); err != nil {
		s.authFail(w, r, err)
		return
	}
	w.WriteHeader(204)
}

func (s *Server) mySecurity(w http.ResponseWriter, r *http.Request) {
	id, ok := s.identity(w, r)
	if !ok {
		return
	}
	sec, err := s.d.Auth.Security(r.Context(), id)
	if err != nil {
		s.authFail(w, r, err)
		return
	}
	pks := make([]map[string]any, 0, len(sec.Passkeys))
	for _, c := range sec.Passkeys {
		pks = append(pks, passkeyView(c))
	}
	s.ok(w, 200, map[string]any{"has_password": sec.HasPassword, "totp_enabled": sec.TOTPEnabled, "passkeys": pks, "recovery_codes_remaining": sec.RecoveryLeft, "mfa_required_by_policy": sec.MFARequired})
}

// --- second-factor enrolment

func (s *Server) totpBegin(w http.ResponseWriter, r *http.Request) {
	id, ok := s.identity(w, r)
	if !ok {
		return
	}
	e, err := s.d.Auth.TOTPBegin(r.Context(), id)
	if err != nil {
		s.authFail(w, r, err)
		return
	}
	s.ok(w, 200, map[string]any{"challenge_id": e.ChallengeID, "secret": e.Secret, "otpauth_uri": e.URI, "digits": auth.TOTPDigits, "period": auth.TOTPPeriod, "algorithm": "SHA1"})
}

func (s *Server) totpConfirm(w http.ResponseWriter, r *http.Request) {
	id, ok := s.identity(w, r)
	if !ok {
		return
	}
	var in struct {
		ChallengeID string `json:"challenge_id"`
		Code        string `json:"code"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	codes, err := s.d.Auth.TOTPConfirm(r.Context(), id, in.ChallengeID, in.Code)
	if err != nil {
		s.authFail(w, r, err)
		return
	}
	if codes == nil {
		codes = []string{}
	}
	s.ok(w, 200, map[string]any{"recovery_codes": codes, "me": s.meAfter(r, id)})
}

func (s *Server) totpDisable(w http.ResponseWriter, r *http.Request) {
	id, ok := s.identity(w, r)
	if !ok {
		return
	}
	if err := s.d.Auth.TOTPDisable(r.Context(), id); err != nil {
		s.authFail(w, r, err)
		return
	}
	w.WriteHeader(204)
}

func (s *Server) passkeyRegisterBegin(w http.ResponseWriter, r *http.Request) {
	id, ok := s.identity(w, r)
	if !ok {
		return
	}
	o, err := s.d.Auth.PasskeyRegisterBegin(r.Context(), id)
	if err != nil {
		s.authFail(w, r, err)
		return
	}
	s.webAuthnOptions(w, o)
}

func (s *Server) passkeyRegisterFinish(w http.ResponseWriter, r *http.Request) {
	id, ok := s.identity(w, r)
	if !ok {
		return
	}
	var in credentialBody
	if !s.decode(w, r, &in) {
		return
	}
	pk, codes, err := s.d.Auth.PasskeyRegisterFinish(r.Context(), id, in.ChallengeID, in.Name, in.Credential)
	if err != nil {
		s.authFail(w, r, err)
		return
	}
	if codes == nil {
		codes = []string{}
	}
	s.ok(w, 200, map[string]any{"recovery_codes": codes, "passkey": passkeyView(pk), "me": s.meAfter(r, id)})
}

func credID(r *http.Request) []byte {
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(r.PathValue("id"), "="))
	if err != nil {
		return nil
	}
	return b
}

func (s *Server) passkeyRename(w http.ResponseWriter, r *http.Request) {
	id, ok := s.identity(w, r)
	if !ok {
		return
	}
	var in struct{ Name string }
	if !s.decode(w, r, &in) {
		return
	}
	c, err := s.d.Auth.PasskeyRename(r.Context(), id, credID(r), in.Name)
	if err != nil {
		s.authFail(w, r, err)
		return
	}
	s.ok(w, 200, passkeyView(c))
}

func (s *Server) passkeyDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := s.identity(w, r)
	if !ok {
		return
	}
	if err := s.d.Auth.PasskeyDelete(r.Context(), id, credID(r)); err != nil {
		s.authFail(w, r, err)
		return
	}
	w.WriteHeader(204)
}

func (s *Server) recoveryCodes(w http.ResponseWriter, r *http.Request) {
	id, ok := s.identity(w, r)
	if !ok {
		return
	}
	codes, err := s.d.Auth.RegenerateRecoveryCodes(r.Context(), id)
	if err != nil {
		s.authFail(w, r, err)
		return
	}
	s.ok(w, 200, map[string]any{"recovery_codes": codes})
}

// --- sessions

func (s *Server) mySessions(w http.ResponseWriter, r *http.Request) {
	id, ok := s.identity(w, r)
	if !ok {
		return
	}
	list, err := s.d.Auth.MySessions(r.Context(), id)
	if err != nil {
		s.authFail(w, r, err)
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, si := range list {
		out = append(out, sessionInfoView(si, id.Session.ID))
	}
	s.ok(w, 200, out)
}

func (s *Server) revokeMySession(w http.ResponseWriter, r *http.Request) {
	id, ok := s.identity(w, r)
	if !ok {
		return
	}
	if err := s.d.Auth.RevokeMySession(r.Context(), id, r.PathValue("id")); err != nil {
		s.authFail(w, r, err)
		return
	}
	if r.PathValue("id") == id.Session.ID {
		s.clearSessionCookie(w, r)
	}
	w.WriteHeader(204)
}

// --- invitations

func (s *Server) invitation(w http.ResponseWriter, r *http.Request) {
	v, err := s.d.Auth.Invitation(r.Context(), s.client(r), r.PathValue("token"))
	if err != nil {
		s.authFail(w, r, err)
		return
	}
	s.ok(w, 200, map[string]any{"username": v.Username, "display_name": v.DisplayName, "expires_at": v.ExpiresAt, "passkey_available": v.PasskeyAvailable})
}

func (s *Server) invitationPasskeyBegin(w http.ResponseWriter, r *http.Request) {
	o, err := s.d.Auth.InvitationPasskeyBegin(r.Context(), s.client(r), r.PathValue("token"))
	if err != nil {
		s.authFail(w, r, err)
		return
	}
	s.webAuthnOptions(w, o)
}

func (s *Server) invitationPasskeyFinish(w http.ResponseWriter, r *http.Request) {
	var in credentialBody
	if !s.decode(w, r, &in) {
		return
	}
	o, err := s.d.Auth.InvitationPasskeyFinish(r.Context(), s.client(r), r.PathValue("token"), in.ChallengeID, in.Name, in.Credential)
	if err != nil {
		s.authFail(w, r, err)
		return
	}
	s.writeOutcome(w, r, 200, o)
}
