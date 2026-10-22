package auth

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"oa-agent/backend/internal/store"
)

var errPasskeyUnavailable = fail(409, "PASSKEY_UNAVAILABLE", "Passkey 不可用：需要 HTTPS（或 localhost）并在安全设置中配置 RP ID 与来源")

// waUser adapts a stored user to the WebAuthn library.
type waUser struct {
	u     store.AuthUser
	creds []webauthn.Credential
}

func (w waUser) WebAuthnID() []byte                         { return w.u.WebAuthnHandle }
func (w waUser) WebAuthnName() string                       { return w.u.Username }
func (w waUser) WebAuthnCredentials() []webauthn.Credential { return w.creds }
func (w waUser) WebAuthnDisplayName() string {
	if w.u.DisplayName != "" {
		return w.u.DisplayName
	}
	return w.u.Username
}

func toWebAuthnCred(c store.WebAuthnCred) webauthn.Credential {
	flags := protocol.FlagUserPresent | protocol.FlagUserVerified
	if c.BackupEligible {
		flags |= protocol.FlagBackupEligible
	}
	if c.BackupState {
		flags |= protocol.FlagBackupState
	}
	tr := make([]protocol.AuthenticatorTransport, 0, len(c.Transports))
	for _, t := range c.Transports {
		tr = append(tr, protocol.AuthenticatorTransport(t))
	}
	return webauthn.Credential{
		ID: c.ID, PublicKey: c.PublicKey, Transport: tr, Flags: webauthn.NewCredentialFlags(flags),
		Authenticator: webauthn.Authenticator{AAGUID: c.AAGUID, SignCount: c.SignCount, CloneWarning: c.CloneWarning},
	}
}

func (s *Service) waUserFor(ctx context.Context, u store.AuthUser) (waUser, []store.WebAuthnCred, error) {
	creds, err := s.Store.Credentials(ctx, u.ID)
	if err != nil {
		return waUser{}, nil, errInternal
	}
	w := waUser{u: u}
	for _, c := range creds {
		w.creds = append(w.creds, toWebAuthnCred(c))
	}
	return w, creds, nil
}

// webauthnFor builds the relying party from the stored settings.
func (s *Service) webauthnFor(set store.AuthSettings) (*webauthn.WebAuthn, error) {
	if !PasskeyAvailable(set) {
		return nil, errPasskeyUnavailable
	}
	yes := true
	w, err := webauthn.New(&webauthn.Config{
		RPID: set.RPID, RPDisplayName: set.RPName, RPOrigins: set.Origins,
		AttestationPreference: protocol.PreferNoAttestation,
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			RequireResidentKey: &yes, ResidentKey: protocol.ResidentKeyRequirementRequired, UserVerification: protocol.VerificationRequired,
		},
		Timeouts: webauthn.TimeoutsConfig{
			Login:        webauthn.TimeoutConfig{Enforce: true, Timeout: ChallengeTTL, TimeoutUVD: ChallengeTTL},
			Registration: webauthn.TimeoutConfig{Enforce: true, Timeout: ChallengeTTL, TimeoutUVD: ChallengeTTL},
		},
	})
	if err != nil {
		return nil, fail(409, "PASSKEY_UNAVAILABLE", "Passkey 配置无效，请在安全设置中检查 RP ID 与来源")
	}
	return w, nil
}

// challengeData is what is stored with a WebAuthn challenge.
type challengeData struct {
	Session  webauthn.SessionData `json:"session"`
	Parent   string               `json:"parent,omitempty"` // hash of the mfa token / invitation token this ceremony belongs to
	Username string               `json:"username,omitempty"`
}

func (s *Service) storeChallenge(ctx context.Context, kind, userID string, sd *webauthn.SessionData, parent string) (string, error) {
	tok, err := NewToken()
	if err != nil {
		return "", errInternal
	}
	b, err := json.Marshal(challengeData{Session: *sd, Parent: parent})
	if err != nil {
		return "", errInternal
	}
	if err := s.Store.CreateChallenge(ctx, store.AuthChallenge{ID: HashToken(tok), Kind: kind, UserID: userID, Data: b, ExpiresAt: s.Now().Add(ChallengeTTL)}); err != nil {
		return "", errInternal
	}
	return tok, nil
}

func (s *Service) takeChallenge(ctx context.Context, id, kind string) (store.AuthChallenge, challengeData, error) {
	var cd challengeData
	ch, err := s.Store.ConsumeChallenge(ctx, HashToken(id), kind, s.Now())
	if err != nil {
		return ch, cd, fail(401, "CHALLENGE_INVALID", "验证已过期，请重新开始")
	}
	if err := json.Unmarshal(ch.Data, &cd); err != nil {
		return ch, cd, errInternal
	}
	return ch, cd, nil
}

// WebAuthnOptions is the begin-step result: options in the standard JSON form plus the one-time challenge id.
type WebAuthnOptions struct {
	ChallengeID string
	Options     any
}

// beginRegistration creates registration options for u (existing credentials are excluded).
func (s *Service) beginRegistration(ctx context.Context, set store.AuthSettings, u store.AuthUser, kind, parent string) (WebAuthnOptions, error) {
	wa, err := s.webauthnFor(set)
	if err != nil {
		return WebAuthnOptions{}, err
	}
	w, creds, err := s.waUserFor(ctx, u)
	if err != nil {
		return WebAuthnOptions{}, err
	}
	if len(creds) >= MaxPasskeys {
		return WebAuthnOptions{}, fail(409, "TOO_MANY_PASSKEYS", fmt.Sprintf("最多可注册 %d 个 Passkey", MaxPasskeys))
	}
	creation, sd, err := wa.BeginRegistration(w, webauthn.WithExclusions(webauthn.Credentials(w.creds).CredentialDescriptors()))
	if err != nil {
		return WebAuthnOptions{}, errInternal
	}
	id, err := s.storeChallenge(ctx, kind, u.ID, sd, parent)
	if err != nil {
		return WebAuthnOptions{}, err
	}
	return WebAuthnOptions{ChallengeID: id, Options: creation.Response}, nil
}

func invalidPasskey(what string) *Error {
	return fail(422, "INVALID_CREDENTIAL", what)
}

// finishRegistration verifies the attestation and converts it into a store row.
func (s *Service) finishRegistration(ctx context.Context, set store.AuthSettings, u store.AuthUser, cd challengeData, credential []byte, name string) (store.WebAuthnCred, error) {
	wa, err := s.webauthnFor(set)
	if err != nil {
		return store.WebAuthnCred{}, err
	}
	w, _, err := s.waUserFor(ctx, u)
	if err != nil {
		return store.WebAuthnCred{}, err
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(credential)
	if err != nil {
		return store.WebAuthnCred{}, invalidPasskey("Passkey 注册数据无效")
	}
	cred, err := wa.CreateCredential(w, cd.Session, parsed)
	if err != nil {
		return store.WebAuthnCred{}, invalidPasskey("Passkey 验证未通过，请重试")
	}
	if !cred.Flags.UserVerified {
		return store.WebAuthnCred{}, invalidPasskey("需要用户验证（PIN / 生物识别）的 Passkey")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Passkey " + s.Now().Local().Format("2006-01-02")
	}
	if r := []rune(name); len(r) > 64 {
		name = string(r[:64])
	}
	tr := make([]string, 0, len(cred.Transport))
	for _, t := range cred.Transport {
		tr = append(tr, string(t))
	}
	return store.WebAuthnCred{ID: cred.ID, UserID: u.ID, Name: name, PublicKey: cred.PublicKey, SignCount: cred.Authenticator.SignCount, AAGUID: cred.Authenticator.AAGUID, Transports: tr, BackupEligible: cred.Flags.BackupEligible, BackupState: cred.Flags.BackupState}, nil
}

// assertion validates an assertion for a known user and persists counters. A suspected clone is rejected.
func (s *Service) assertion(ctx context.Context, set store.AuthSettings, u store.AuthUser, cd challengeData, credential []byte) error {
	wa, err := s.webauthnFor(set)
	if err != nil {
		return err
	}
	w, _, err := s.waUserFor(ctx, u)
	if err != nil {
		return err
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(credential)
	if err != nil {
		return errInvalidCredentials
	}
	cred, err := wa.ValidateLogin(w, cd.Session, parsed)
	if err != nil {
		return errInvalidCredentials
	}
	return s.afterAssertion(ctx, u, cred)
}

func (s *Service) afterAssertion(ctx context.Context, u store.AuthUser, cred *webauthn.Credential) error {
	if cred.Authenticator.CloneWarning {
		_ = s.Store.TouchCredential(ctx, cred.ID, cred.Authenticator.SignCount, true, cred.Flags.BackupState, s.Now())
		s.audit(ctx, u.Username, "passkey.clone_suspected", u.ID, "Passkey 计数器未递增，疑似被克隆，已拒绝本次验证")
		return fail(401, "INVALID_CREDENTIALS", "Passkey 验证失败")
	}
	_ = s.Store.TouchCredential(ctx, cred.ID, cred.Authenticator.SignCount, false, cred.Flags.BackupState, s.Now())
	return nil
}

// MFAPasskeyBegin starts a passkey assertion as second factor.
func (s *Service) MFAPasskeyBegin(ctx context.Context, c Client, mfaToken string) (WebAuthnOptions, error) {
	ch, u, set, err := s.mfaContext(ctx, c, mfaToken)
	if err != nil {
		return WebAuthnOptions{}, err
	}
	wa, err := s.webauthnFor(set)
	if err != nil {
		return WebAuthnOptions{}, err
	}
	w, creds, err := s.waUserFor(ctx, u)
	if err != nil {
		return WebAuthnOptions{}, err
	}
	if len(creds) == 0 {
		return WebAuthnOptions{}, errMFAToken
	}
	assertion, sd, err := wa.BeginLogin(w, webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return WebAuthnOptions{}, errInternal
	}
	id, err := s.storeChallenge(ctx, "webauthn_mfa", u.ID, sd, ch.ID)
	if err != nil {
		return WebAuthnOptions{}, err
	}
	return WebAuthnOptions{ChallengeID: id, Options: assertion.Response}, nil
}

// MFAPasskeyFinish verifies the assertion and completes login.
func (s *Service) MFAPasskeyFinish(ctx context.Context, c Client, mfaToken, challengeID string, credential []byte) (Outcome, error) {
	ch, u, set, err := s.mfaContext(ctx, c, mfaToken)
	if err != nil {
		return Outcome{}, err
	}
	wch, cd, err := s.takeChallenge(ctx, challengeID, "webauthn_mfa")
	if err != nil || wch.UserID != u.ID || !EqualHash(cd.Parent, ch.ID) {
		return Outcome{}, errMFAToken
	}
	if err := s.assertion(ctx, set, u, cd, credential); err != nil {
		var ae *Error
		if errors.As(err, &ae) && ae.Code != "INVALID_CREDENTIALS" {
			return Outcome{}, err
		}
		return Outcome{}, s.mfaFailed(ctx, ch, u, set, "Passkey")
	}
	return s.mfaDone(ctx, c, ch, u, set, "passkey")
}

// PasskeyLoginBegin starts a discoverable (usernameless) login.
func (s *Service) PasskeyLoginBegin(ctx context.Context, c Client) (WebAuthnOptions, error) {
	set, err := s.Settings(ctx)
	if err != nil {
		return WebAuthnOptions{}, errInternal
	}
	if set.Mode == "local" {
		return WebAuthnOptions{}, errLocalMode
	}
	if err := s.rate(s.IPLimiter, "ip:"+c.IP); err != nil {
		return WebAuthnOptions{}, err
	}
	wa, err := s.webauthnFor(set)
	if err != nil {
		return WebAuthnOptions{}, err
	}
	assertion, sd, err := wa.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return WebAuthnOptions{}, errInternal
	}
	id, err := s.storeChallenge(ctx, "webauthn_login", "", sd, "")
	if err != nil {
		return WebAuthnOptions{}, err
	}
	return WebAuthnOptions{ChallengeID: id, Options: assertion.Response}, nil
}

// PasskeyLoginFinish verifies a discoverable assertion; the credential's owner is logged in (mfa=passkey).
func (s *Service) PasskeyLoginFinish(ctx context.Context, c Client, challengeID string, credential []byte) (Outcome, error) {
	set, err := s.Settings(ctx)
	if err != nil {
		return Outcome{}, errInternal
	}
	if set.Mode == "local" {
		return Outcome{}, errLocalMode
	}
	if err := s.rate(s.IPLimiter, "ip:"+c.IP); err != nil {
		return Outcome{}, err
	}
	_, cd, err := s.takeChallenge(ctx, challengeID, "webauthn_login")
	if err != nil {
		return Outcome{}, errInvalidCredentials
	}
	wa, err := s.webauthnFor(set)
	if err != nil {
		return Outcome{}, err
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(credential)
	if err != nil {
		return Outcome{}, errInvalidCredentials
	}
	var found store.AuthUser
	user, cred, err := wa.ValidatePasskeyLogin(func(rawID, handle []byte) (webauthn.User, error) {
		u, err := s.Store.AuthUserByHandle(ctx, handle)
		if err != nil {
			return nil, err
		}
		found = u
		w, _, err := s.waUserFor(ctx, u)
		return w, err
	}, cd.Session, parsed)
	if err != nil || user == nil {
		s.audit(ctx, "-", "auth.login_failed", "-", "Passkey 登录失败")
		return Outcome{}, errInvalidCredentials
	}
	u := found
	if u.LockedUntil != nil && s.Now().Before(*u.LockedUntil) {
		return Outcome{}, lockedErr(*u.LockedUntil)
	}
	if u.Status != "active" {
		s.audit(ctx, u.Username, "auth.login_failed", u.ID, "Passkey 登录失败（账号已停用）")
		return Outcome{}, errInvalidCredentials
	}
	if err := s.afterAssertion(ctx, u, cred); err != nil {
		return Outcome{}, err
	}
	return s.finishLogin(ctx, c, u, "passkey", "full", set)
}

// randomHandle is the 64-byte WebAuthn user handle (never derived from the username).
func randomHandle() ([]byte, error) {
	b := make([]byte, 64)
	_, err := rand.Read(b)
	return b, err
}

func webauthnUV() webauthn.LoginOption {
	return webauthn.WithUserVerification(protocol.VerificationRequired)
}
