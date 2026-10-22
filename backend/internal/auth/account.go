package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"oa-agent/backend/internal/store"
)

var (
	errReauth = failReason(403, "FORBIDDEN", "该操作需要近期认证，请先重新输入密码或使用 Passkey 验证", "reauth_required")
	errNoMFA  = fail(409, "MFA_NOT_ENROLLED", "请先绑定 TOTP 或 Passkey")
)

func (s *Service) needFresh(id Identity) error {
	if !id.Fresh(s.Now()) {
		return errReauth
	}
	return nil
}

// reauth marks the session fresh again.
func (s *Service) markFresh(ctx context.Context, id Identity) {
	_ = s.Store.MarkSessionFresh(ctx, id.Session.ID, s.Now().Add(FreshWindow))
}

// Reauth verifies the password again and extends the "recent authentication" window.
func (s *Service) Reauth(ctx context.Context, c Client, id Identity, password string) error {
	if id.Local {
		return errLocalMode
	}
	if err := s.rateBoth(c, "user:"+strings.ToLower(id.User.Username)); err != nil {
		return err
	}
	if id.User.PasswordHash == "" || !id.Settings.AllowPasswordLogin || !VerifyPassword(id.User.PasswordHash, password) {
		if id.User.PasswordHash == "" {
			DummyVerify(password)
		}
		s.audit(ctx, id.User.Username, "auth.reauth_failed", id.User.ID, "重新认证失败")
		if e := s.countFailure(ctx, id.User, id.Settings, "密码"); e != nil {
			_, _ = s.Store.DeleteUserSessions(ctx, id.User.ID, "")
			return e
		}
		return errInvalidCredentials
	}
	s.markFresh(ctx, id)
	return nil
}

// ReauthPasskeyBegin / Finish give passkey-only accounts the same re-authentication.
func (s *Service) ReauthPasskeyBegin(ctx context.Context, c Client, id Identity) (WebAuthnOptions, error) {
	if id.Local {
		return WebAuthnOptions{}, errLocalMode
	}
	if err := s.rate(s.IPLimiter, "ip:"+c.IP); err != nil {
		return WebAuthnOptions{}, err
	}
	wa, err := s.webauthnFor(id.Settings)
	if err != nil {
		return WebAuthnOptions{}, err
	}
	w, creds, err := s.waUserFor(ctx, id.User)
	if err != nil {
		return WebAuthnOptions{}, err
	}
	if len(creds) == 0 {
		return WebAuthnOptions{}, fail(409, "NO_PASSKEY", "该账号没有 Passkey")
	}
	a, sd, err := wa.BeginLogin(w, webauthnUV())
	if err != nil {
		return WebAuthnOptions{}, errInternal
	}
	cid, err := s.storeChallenge(ctx, "webauthn_reauth", id.User.ID, sd, id.Session.ID)
	if err != nil {
		return WebAuthnOptions{}, err
	}
	return WebAuthnOptions{ChallengeID: cid, Options: a.Response}, nil
}

func (s *Service) ReauthPasskeyFinish(ctx context.Context, c Client, id Identity, challengeID string, credential []byte) error {
	if id.Local {
		return errLocalMode
	}
	if err := s.rate(s.IPLimiter, "ip:"+c.IP); err != nil {
		return err
	}
	ch, cd, err := s.takeChallenge(ctx, challengeID, "webauthn_reauth")
	if err != nil || ch.UserID != id.User.ID || !EqualHash(cd.Parent, id.Session.ID) {
		return errUnauthenticated
	}
	if err := s.assertion(ctx, id.Settings, id.User, cd, credential); err != nil {
		s.audit(ctx, id.User.Username, "auth.reauth_failed", id.User.ID, "Passkey 重新认证失败")
		return errUnauthenticated
	}
	s.markFresh(ctx, id)
	return nil
}

// ChangePassword changes the caller's own password and ends all their other sessions.
func (s *Service) ChangePassword(ctx context.Context, c Client, id Identity, current, next string) error {
	if id.Local {
		return errLocalMode
	}
	if err := s.rateBoth(c, "user:"+strings.ToLower(id.User.Username)); err != nil {
		return err
	}
	if id.User.PasswordHash == "" {
		return fail(409, "NO_PASSWORD", "该账号没有密码（仅 Passkey），请联系管理员设置")
	}
	if !VerifyPassword(id.User.PasswordHash, current) {
		s.audit(ctx, id.User.Username, "auth.reauth_failed", id.User.ID, "修改密码失败（当前密码错误）")
		if e := s.countFailure(ctx, id.User, id.Settings, "密码"); e != nil {
			_, _ = s.Store.DeleteUserSessions(ctx, id.User.ID, "")
			return e
		}
		return errInvalidCredentials
	}
	if msg := CheckPasswordPolicy(next, id.User.Username, id.Settings.PasswordMinLength); msg != "" {
		return fail(422, "WEAK_PASSWORD", msg)
	}
	if next == current {
		return fail(422, "WEAK_PASSWORD", "新密码不能与当前密码相同")
	}
	h, err := HashPassword(next)
	if err != nil {
		return errInternal
	}
	if err := s.Store.SetPassword(ctx, id.User.ID, h, false); err != nil {
		return errInternal
	}
	n, _ := s.Store.DeleteUserSessions(ctx, id.User.ID, id.Session.ID)
	s.markFresh(ctx, id)
	s.audit(ctx, id.User.Username, "auth.password_change", id.User.ID, fmt.Sprintf("修改密码，已退出其他 %d 个会话", n))
	return nil
}

// Security summarises the caller's sign-in methods.
type Security struct {
	HasPassword  bool
	TOTPEnabled  bool
	Passkeys     []store.WebAuthnCred
	RecoveryLeft int
	MFARequired  bool
}

func (s *Service) Security(ctx context.Context, id Identity) (Security, error) {
	if id.Local {
		return Security{Passkeys: []store.WebAuthnCred{}}, nil
	}
	creds, err := s.Store.Credentials(ctx, id.User.ID)
	if err != nil {
		return Security{}, errInternal
	}
	n, err := s.Store.RecoveryCodesRemaining(ctx, id.User.ID)
	if err != nil {
		return Security{}, errInternal
	}
	return Security{HasPassword: id.User.PasswordHash != "", TOTPEnabled: id.User.TOTPEnabled(), Passkeys: creds, RecoveryLeft: n, MFARequired: RequiresMFA(id.Settings, id.User.Role)}, nil
}

// TOTPEnrollment is returned by TOTPBegin (the secret is only ever shown here).
type TOTPEnrollment struct {
	ChallengeID string
	Secret      string
	URI         string
}

// TOTPBegin generates a pending secret (stored encrypted in a short-lived challenge until a code confirms it).
func (s *Service) TOTPBegin(ctx context.Context, id Identity) (TOTPEnrollment, error) {
	if id.Local {
		return TOTPEnrollment{}, errLocalMode
	}
	if id.User.TOTPEnabled() {
		return TOTPEnrollment{}, fail(409, "TOTP_ALREADY_ENABLED", "已绑定 TOTP，请先关闭后再重新绑定")
	}
	secret, uri, err := NewTOTPSecret(id.Settings.RPName, id.User.Username)
	if err != nil {
		return TOTPEnrollment{}, errInternal
	}
	enc, err := s.Box.Seal([]byte(secret))
	if err != nil {
		return TOTPEnrollment{}, errInternal
	}
	tok, err := NewToken()
	if err != nil {
		return TOTPEnrollment{}, errInternal
	}
	data := []byte(fmt.Sprintf(`{"secret_enc":%q}`, encodeB64(enc)))
	if err := s.Store.CreateChallenge(ctx, store.AuthChallenge{ID: HashToken(tok), Kind: "totp_enroll", UserID: id.User.ID, Data: data, ExpiresAt: s.Now().Add(ChallengeTTL)}); err != nil {
		return TOTPEnrollment{}, errInternal
	}
	return TOTPEnrollment{ChallengeID: tok, Secret: secret, URI: uri}, nil
}

// firstMFA reports whether the user has no second factor yet (their first enrolment returns recovery codes).
func (s *Service) firstMFA(ctx context.Context, id Identity) (bool, error) {
	creds, err := s.Store.Credentials(ctx, id.User.ID)
	if err != nil {
		return false, errInternal
	}
	return !userHasMFA(id.User, len(creds)), nil
}

func (s *Service) issueRecoveryCodes(ctx context.Context, userID string) ([]string, error) {
	plain, hashes, err := NewRecoveryCodes()
	if err != nil {
		return nil, errInternal
	}
	if err := s.Store.ReplaceRecoveryCodes(ctx, userID, hashes); err != nil {
		return nil, errInternal
	}
	return plain, nil
}

// afterEnrol promotes a restricted session once a second factor exists.
func (s *Service) afterEnrol(ctx context.Context, id Identity, mfa string) {
	if id.Session.Scope == "enroll_mfa" {
		_ = s.Store.PromoteSession(ctx, id.Session.ID, mfa)
	}
}

// TOTPConfirm activates the pending secret when the code is valid; the first MFA also yields recovery codes.
func (s *Service) TOTPConfirm(ctx context.Context, id Identity, challengeID, code string) ([]string, error) {
	if id.Local {
		return nil, errLocalMode
	}
	if err := s.rate(s.UserLimiter, "totp-confirm:"+id.User.ID); err != nil {
		return nil, err
	}
	hid := HashToken(challengeID)
	ch, err := s.Store.PeekChallenge(ctx, hid, "totp_enroll", s.Now())
	if err != nil || ch.UserID != id.User.ID {
		return nil, fail(422, "CHALLENGE_EXPIRED", "绑定已过期，请重新开始")
	}
	var d struct {
		SecretEnc string `json:"secret_enc"`
	}
	raw, derr := decodeB64(ch.Data, &d)
	if derr != nil {
		return nil, errInternal
	}
	secret, err := s.Box.Unseal(raw)
	if err != nil {
		return nil, errInternal
	}
	step, ok := VerifyTOTP(string(secret), code, s.Now(), 0)
	if !ok {
		_, _ = s.Store.BumpChallenge(ctx, hid, MaxMFAAttempts)
		return nil, fail(422, "INVALID_CODE", "验证码不正确，请检查手机时间后重试")
	}
	first, err := s.firstMFA(ctx, id)
	if err != nil {
		return nil, err
	}
	if won, err := s.Store.DeleteChallenge(ctx, hid); err != nil || !won {
		return nil, fail(422, "CHALLENGE_EXPIRED", "绑定已过期，请重新开始")
	}
	if err := s.Store.SetTOTP(ctx, id.User.ID, raw, s.Now(), step); err != nil {
		return nil, errInternal
	}
	var codes []string
	if first {
		if codes, err = s.issueRecoveryCodes(ctx, id.User.ID); err != nil {
			return nil, err
		}
	}
	s.afterEnrol(ctx, id, "totp")
	s.audit(ctx, id.User.Username, "totp.enable", id.User.ID, "绑定 TOTP")
	return codes, nil
}

// mfaCountAfter is how many second factors would remain.
func (s *Service) canRemoveMFA(ctx context.Context, id Identity, removingTOTP bool, removingPasskey bool) error {
	creds, err := s.Store.Credentials(ctx, id.User.ID)
	if err != nil {
		return errInternal
	}
	totp := id.User.TOTPEnabled()
	pk := len(creds)
	if removingTOTP {
		totp = false
	}
	if removingPasskey {
		pk--
	}
	if RequiresMFA(id.Settings, id.User.Role) && !totp && pk <= 0 {
		return fail(409, "MFA_REQUIRED_BY_POLICY", "安全策略要求至少保留一种第二因素")
	}
	return nil
}

// TOTPDisable removes TOTP (needs recent authentication).
func (s *Service) TOTPDisable(ctx context.Context, id Identity) error {
	if id.Local {
		return errLocalMode
	}
	if err := s.needFresh(id); err != nil {
		return err
	}
	if !id.User.TOTPEnabled() {
		return fail(409, "TOTP_NOT_ENABLED", "未绑定 TOTP")
	}
	if err := s.canRemoveMFA(ctx, id, true, false); err != nil {
		return err
	}
	if err := s.Store.ClearTOTP(ctx, id.User.ID); err != nil {
		return errInternal
	}
	s.dropCodesIfNoMFA(ctx, id.User.ID)
	s.audit(ctx, id.User.Username, "totp.disable", id.User.ID, "关闭 TOTP")
	return nil
}

func (s *Service) dropCodesIfNoMFA(ctx context.Context, userID string) {
	u, err := s.Store.AuthUserByID(ctx, userID)
	if err != nil {
		return
	}
	creds, err := s.Store.Credentials(ctx, userID)
	if err == nil && !userHasMFA(u, len(creds)) {
		_ = s.Store.ReplaceRecoveryCodes(ctx, userID, nil)
	}
}

// PasskeyRegisterBegin / Finish add a passkey to the caller.
func (s *Service) PasskeyRegisterBegin(ctx context.Context, id Identity) (WebAuthnOptions, error) {
	if id.Local {
		return WebAuthnOptions{}, errLocalMode
	}
	return s.beginRegistration(ctx, id.Settings, id.User, "webauthn_register", id.Session.ID)
}

func (s *Service) PasskeyRegisterFinish(ctx context.Context, id Identity, challengeID, name string, credential []byte) (store.WebAuthnCred, []string, error) {
	if id.Local {
		return store.WebAuthnCred{}, nil, errLocalMode
	}
	ch, cd, err := s.takeChallenge(ctx, challengeID, "webauthn_register")
	if err != nil || ch.UserID != id.User.ID || !EqualHash(cd.Parent, id.Session.ID) {
		return store.WebAuthnCred{}, nil, fail(401, "CHALLENGE_INVALID", "验证已过期，请重新开始")
	}
	first, err := s.firstMFA(ctx, id)
	if err != nil {
		return store.WebAuthnCred{}, nil, err
	}
	row, err := s.finishRegistration(ctx, id.Settings, id.User, cd, credential, name)
	if err != nil {
		return row, nil, err
	}
	if err := s.Store.AddCredential(ctx, row); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return row, nil, invalidPasskey("该 Passkey 已注册")
		}
		return row, nil, errInternal
	}
	var codes []string
	if first {
		if codes, err = s.issueRecoveryCodes(ctx, id.User.ID); err != nil {
			return row, nil, err
		}
	}
	s.afterEnrol(ctx, id, "passkey")
	s.audit(ctx, id.User.Username, "passkey.add", id.User.ID, "注册 Passkey「"+row.Name+"」")
	got, _ := s.Store.Credential(ctx, id.User.ID, row.ID)
	return got, codes, nil
}

func (s *Service) PasskeyRename(ctx context.Context, id Identity, credID []byte, name string) (store.WebAuthnCred, error) {
	if id.Local {
		return store.WebAuthnCred{}, errLocalMode
	}
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 64 {
		return store.WebAuthnCred{}, fail(400, "INVALID_ARGUMENT", "名称需要 1–64 个字符")
	}
	c, err := s.Store.RenameCredential(ctx, id.User.ID, credID, name)
	if errors.Is(err, store.ErrNotFound) {
		return c, fail(404, "NOT_FOUND", "Passkey 不存在")
	}
	if err != nil {
		return c, errInternal
	}
	s.audit(ctx, id.User.Username, "passkey.rename", id.User.ID, "重命名 Passkey 为「"+name+"」")
	return c, nil
}

func (s *Service) PasskeyDelete(ctx context.Context, id Identity, credID []byte) error {
	if id.Local {
		return errLocalMode
	}
	if err := s.needFresh(id); err != nil {
		return err
	}
	cur, err := s.Store.Credential(ctx, id.User.ID, credID)
	if errors.Is(err, store.ErrNotFound) {
		return fail(404, "NOT_FOUND", "Passkey 不存在")
	}
	if err != nil {
		return errInternal
	}
	creds, err := s.Store.Credentials(ctx, id.User.ID)
	if err != nil {
		return errInternal
	}
	if len(creds) == 1 && (id.User.PasswordHash == "" || !id.Settings.AllowPasswordLogin) {
		return fail(409, "LAST_LOGIN_METHOD", "不能删除最后一种登录方式")
	}
	if err := s.canRemoveMFA(ctx, id, false, true); err != nil {
		return err
	}
	if err := s.Store.DeleteCredential(ctx, id.User.ID, credID); err != nil {
		return errInternal
	}
	s.dropCodesIfNoMFA(ctx, id.User.ID)
	s.audit(ctx, id.User.Username, "passkey.delete", id.User.ID, "删除 Passkey「"+cur.Name+"」")
	return nil
}

// RegenerateRecoveryCodes replaces the recovery codes (old ones stop working); plaintext is returned once.
func (s *Service) RegenerateRecoveryCodes(ctx context.Context, id Identity) ([]string, error) {
	if id.Local {
		return nil, errLocalMode
	}
	if err := s.needFresh(id); err != nil {
		return nil, err
	}
	first, err := s.firstMFA(ctx, id)
	if err != nil {
		return nil, err
	}
	if first {
		return nil, errNoMFA
	}
	codes, err := s.issueRecoveryCodes(ctx, id.User.ID)
	if err != nil {
		return nil, err
	}
	s.audit(ctx, id.User.Username, "recovery.regenerate", id.User.ID, "重新生成恢复码")
	return codes, nil
}

// MySessions lists the caller's live sessions.
func (s *Service) MySessions(ctx context.Context, id Identity) ([]store.AuthSessionInfo, error) {
	if id.Local {
		return []store.AuthSessionInfo{}, nil
	}
	out, err := s.Store.ListSessions(ctx, id.User.ID, s.Now())
	if err != nil {
		return nil, errInternal
	}
	return out, nil
}

// RevokeMySession ends one of the caller's own sessions (404 for anyone else's).
func (s *Service) RevokeMySession(ctx context.Context, id Identity, sessionID string) error {
	if id.Local {
		return errLocalMode
	}
	si, err := s.Store.SessionByID(ctx, sessionID)
	if err != nil || si.UserID != id.User.ID {
		return fail(404, "NOT_FOUND", "会话不存在")
	}
	if _, err := s.Store.DeleteSession(ctx, sessionID); err != nil {
		return errInternal
	}
	s.audit(ctx, id.User.Username, "session.revoke", id.User.ID, "吊销会话")
	return nil
}
