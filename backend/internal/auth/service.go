package auth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"oa-agent/backend/internal/secrets"
	"oa-agent/backend/internal/store"
)

const (
	ChallengeTTL   = 5 * time.Minute
	FreshWindow    = 10 * time.Minute
	TouchEvery     = time.Minute
	InviteTTL      = 24 * time.Hour
	MaxMFAAttempts = 5
	MaxPasskeys    = 10
	LocalUserName  = "local"
	CLIActor       = "oa-admin(cli)"
	settingsTTL    = 3 * time.Second
)

// Error is a failure with the HTTP status and contract error code the API layer should answer with.
type Error struct {
	Status     int
	Code       string
	Message    string
	RetryAfter time.Duration
	Details    map[string]any
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func fail(status int, code, msg string) *Error {
	return &Error{Status: status, Code: code, Message: msg}
}

func failReason(status int, code, msg, reason string) *Error {
	return &Error{Status: status, Code: code, Message: msg, Details: map[string]any{"reason": reason}}
}

var (
	errInvalidCredentials = fail(401, "INVALID_CREDENTIALS", "用户名或密码不正确")
	errMFAToken           = fail(401, "MFA_TOKEN_INVALID", "验证已过期，请重新登录")
	errLocalMode          = fail(409, "LOCAL_MODE", "当前为本机（local）模式，不提供该功能")
	errInternal           = fail(500, "INTERNAL", "内部错误")
)

// Client is what the service needs to know about the caller (for sessions and rate limits).
type Client struct{ IP, UserAgent string }

// Service implements the account system on top of the store.
type Service struct {
	Store *store.Store
	Box   *secrets.Box
	Now   Clock
	// IPLimiter and UserLimiter throttle login-like endpoints per client address and per username / token.
	IPLimiter   *Limiter
	UserLimiter *Limiter
	// SetupLimiter throttles the public setup endpoints per address (token guessing).
	SetupLimiter *Limiter
	// PublicURL (OA_PUBLIC_URL) pre-fills the relying-party suggestions.
	PublicURL string
	// Logf receives non-sensitive operational messages (never secrets).
	Logf func(format string, args ...any)

	ghosts   ghostLocks
	mu       sync.Mutex
	cached   store.AuthSettings
	cachedAt time.Time
}

func New(st *store.Store, box *secrets.Box) *Service {
	now := Clock(time.Now)
	return &Service{
		Store: st, Box: box, Now: now,
		IPLimiter:    NewLimiter(60, time.Second, now),
		UserLimiter:  NewLimiter(12, 10*time.Second, now),
		SetupLimiter: NewLimiter(10, 6*time.Second, now),
		Logf:         func(string, ...any) {},
	}
}

// SetClock replaces the clock of the service and its limiters (tests).
func (s *Service) SetClock(c Clock) {
	s.Now = c
	for _, l := range []*Limiter{s.IPLimiter, s.UserLimiter, s.SetupLimiter} {
		if l != nil {
			l.Now = c
		}
	}
}

func (s *Service) audit(ctx context.Context, actor, action, target, summary string) {
	s.Store.Audit(ctx, actor, action, target, summary)
}

// Settings returns the stored policy; reads are cached for a few seconds (and refreshed on every local write).
func (s *Service) Settings(ctx context.Context) (store.AuthSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.cachedAt.IsZero() && time.Since(s.cachedAt) < settingsTTL {
		return s.cached, nil
	}
	a, err := s.Store.AuthSettings(ctx)
	if err != nil {
		return a, err
	}
	s.cached, s.cachedAt = a, time.Now()
	return a, nil
}

func (s *Service) invalidate() {
	s.mu.Lock()
	s.cachedAt = time.Time{}
	s.mu.Unlock()
}

// PasskeyAvailable reports whether WebAuthn can work with these settings: an RP ID and origins that are secure contexts.
func PasskeyAvailable(a store.AuthSettings) bool {
	if a.RPID == "" || len(a.Origins) == 0 {
		return false
	}
	for _, o := range a.Origins {
		if !secureOrigin(o) {
			return false
		}
	}
	return true
}

// secureOrigin: https, or http on localhost (browsers treat it as a secure context).
func secureOrigin(o string) bool {
	u, err := url.Parse(o)
	if err != nil || u.Host == "" {
		return false
	}
	return u.Scheme == "https" || (u.Scheme == "http" && isLocalhostName(u.Hostname()))
}

func isLocalhostName(h string) bool {
	h = strings.ToLower(h)
	return h == "localhost" || strings.HasSuffix(h, ".localhost")
}

// RequiresMFA reports whether the policy demands a second factor for this role.
func RequiresMFA(a store.AuthSettings, role string) bool {
	switch a.MFAPolicy {
	case "all":
		return true
	case "admins":
		return role == "admin"
	}
	return false
}

func userHasMFA(u store.AuthUser, passkeys int) bool { return u.TOTPEnabled() || passkeys > 0 }

// Identity is an authenticated caller.
type Identity struct {
	User     store.AuthUser
	Session  store.AuthSession
	Token    string // raw cookie value ("" in local mode)
	Local    bool
	Settings store.AuthSettings
}

// LocalIdentity is the built-in user of local mode.
func LocalIdentity(set store.AuthSettings, now time.Time) Identity {
	u := store.AuthUser{ID: "local", Username: LocalUserName, DisplayName: "本机用户", Role: "admin", Status: "active"}
	return Identity{User: u, Local: true, Settings: set, Session: store.AuthSession{ID: "local", UserID: "local", Scope: "full", MFA: "local", CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(24 * time.Hour), FreshUntil: now.Add(24 * time.Hour)}}
}

func (id Identity) Fresh(now time.Time) bool { return id.Local || now.Before(id.Session.FreshUntil) }

var errUnauthenticated = fail(401, "UNAUTHENTICATED", "请先登录")

// Authenticate resolves a session cookie value. Idle and absolute expiry are enforced here; last_seen is refreshed at
// most once per TouchEvery.
func (s *Service) Authenticate(ctx context.Context, token string, set store.AuthSettings) (Identity, error) {
	if token == "" || len(token) > 128 {
		return Identity{}, errUnauthenticated
	}
	sess, u, err := s.Store.SessionWithUser(ctx, HashToken(token))
	if errors.Is(err, store.ErrNotFound) {
		return Identity{}, errUnauthenticated
	}
	if err != nil {
		return Identity{}, errInternal
	}
	now := s.Now()
	abs := sess.CreatedAt.Add(time.Duration(set.SessionMaxHours) * time.Hour)
	if sess.ExpiresAt.Before(abs) {
		abs = sess.ExpiresAt
	}
	idle := sess.LastSeenAt.Add(time.Duration(set.SessionIdleMinutes) * time.Minute)
	if !now.Before(abs) || !now.Before(idle) || u.Status != "active" {
		_, _ = s.Store.DeleteSession(ctx, sess.ID)
		return Identity{}, errUnauthenticated
	}
	if now.Sub(sess.LastSeenAt) >= TouchEvery {
		_ = s.Store.TouchSession(ctx, sess.ID, now)
		sess.LastSeenAt = now
	}
	return Identity{User: u, Session: sess, Token: token, Settings: set}, nil
}

// SessionExpiry is the effective end of a session (absolute limit, or idle limit if sooner).
func (id Identity) SessionExpiry() time.Time {
	abs := id.Session.CreatedAt.Add(time.Duration(id.Settings.SessionMaxHours) * time.Hour)
	if id.Session.ExpiresAt.Before(abs) {
		abs = id.Session.ExpiresAt
	}
	idle := id.Session.LastSeenAt.Add(time.Duration(id.Settings.SessionIdleMinutes) * time.Minute)
	if idle.Before(abs) {
		return idle
	}
	return abs
}

// Outcome is the result of a login step.
type Outcome struct {
	Status       string // ok | mfa_required | enroll_mfa
	User         store.AuthUser
	SessionToken string
	Session      store.AuthSession
	MFAToken     string
	Methods      []string
	Expires      time.Time
}

func (s *Service) newSession(ctx context.Context, u store.AuthUser, c Client, mfa, scope string, set store.AuthSettings) (Outcome, error) {
	tok, err := NewToken()
	if err != nil {
		return Outcome{}, errInternal
	}
	now := s.Now()
	ua := c.UserAgent
	if len(ua) > 300 {
		ua = ua[:300]
	}
	sess := store.AuthSession{ID: HashToken(tok), UserID: u.ID, CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Duration(set.SessionMaxHours) * time.Hour), FreshUntil: now.Add(FreshWindow), IP: c.IP, UserAgent: ua, MFA: mfa, Scope: scope}
	if err := s.Store.CreateSession(ctx, sess); err != nil {
		return Outcome{}, errInternal
	}
	st := "ok"
	if scope == "enroll_mfa" {
		st = "enroll_mfa"
	}
	idle := now.Add(time.Duration(set.SessionIdleMinutes) * time.Minute)
	exp := sess.ExpiresAt
	if idle.Before(exp) {
		exp = idle
	}
	return Outcome{Status: st, User: u, SessionToken: tok, Session: sess, Expires: exp}, nil
}

func (s *Service) rate(l *Limiter, key string) error {
	if ok, wait := l.Allow(key); !ok {
		e := fail(429, "RATE_LIMITED", "请求过于频繁，请稍后再试")
		e.RetryAfter = wait
		return e
	}
	return nil
}

// rateBoth applies the per-address and the per-subject bucket.
func (s *Service) rateBoth(c Client, subject string) error {
	if err := s.rate(s.IPLimiter, "ip:"+c.IP); err != nil {
		return err
	}
	if subject != "" {
		return s.rate(s.UserLimiter, subject)
	}
	return nil
}

func lockedErr(until time.Time) *Error {
	e := fail(423, "ACCOUNT_LOCKED", "账号已被临时锁定，请稍后再试或联系管理员解锁")
	e.Details = map[string]any{"locked_until": until.UTC().Format(time.RFC3339)}
	return e
}

func (s *Service) lockDuration(set store.AuthSettings) time.Duration {
	return time.Duration(set.LockoutMinutes) * time.Minute
}

// countFailure records a failed secret check for a real user and reports a resulting lock.
func (s *Service) countFailure(ctx context.Context, u store.AuthUser, set store.AuthSettings, what string) *Error {
	locked, until, err := s.Store.RecordLoginFailure(ctx, u.ID, set.LockoutThreshold, s.lockDuration(set), s.Now())
	if err == nil && locked && until != nil {
		s.audit(ctx, u.Username, "auth.lockout", u.ID, fmt.Sprintf("连续失败达到阈值（%s），账号锁定至 %s", what, until.Local().Format("15:04:05")))
		return lockedErr(*until)
	}
	return nil
}

func normalizeUsername(n string) string { return strings.TrimSpace(n) }

// Login checks username and password (first factor).
func (s *Service) Login(ctx context.Context, c Client, username, password string) (Outcome, error) {
	set, err := s.Settings(ctx)
	if err != nil {
		return Outcome{}, errInternal
	}
	if set.Mode == "local" {
		return Outcome{}, errLocalMode
	}
	username = normalizeUsername(username)
	if len(username) > 64 || len(password) > 4096 {
		DummyVerify(password)
		return Outcome{}, errInvalidCredentials
	}
	if err := s.rateBoth(c, "user:"+strings.ToLower(username)); err != nil {
		return Outcome{}, err
	}
	now := s.Now()
	u, err := s.Store.AuthUserByName(ctx, username)
	if errors.Is(err, store.ErrNotFound) {
		if until, locked := s.ghosts.locked(strings.ToLower(username), now); locked {
			return Outcome{}, lockedErr(until)
		}
		DummyVerify(password)
		s.audit(ctx, "-", "auth.login_failed", "-", "登录失败（用户名或密码错误）")
		if until, locked := s.ghosts.fail(strings.ToLower(username), set.LockoutThreshold, s.lockDuration(set), now); locked {
			return Outcome{}, lockedErr(until)
		}
		return Outcome{}, errInvalidCredentials
	}
	if err != nil {
		return Outcome{}, errInternal
	}
	if u.LockedUntil != nil && now.Before(*u.LockedUntil) {
		return Outcome{}, lockedErr(*u.LockedUntil)
	}
	okPw := false
	if u.PasswordHash != "" && set.AllowPasswordLogin {
		okPw = VerifyPassword(u.PasswordHash, password)
	} else {
		DummyVerify(password)
	}
	if !okPw {
		s.audit(ctx, u.Username, "auth.login_failed", u.ID, "登录失败（用户名或密码错误）")
		if e := s.countFailure(ctx, u, set, "密码"); e != nil {
			return Outcome{}, e
		}
		return Outcome{}, errInvalidCredentials
	}
	if u.Status != "active" {
		s.audit(ctx, u.Username, "auth.login_failed", u.ID, "登录失败（账号已停用）")
		return Outcome{}, errInvalidCredentials
	}
	creds, err := s.Store.Credentials(ctx, u.ID)
	if err != nil {
		return Outcome{}, errInternal
	}
	if userHasMFA(u, len(creds)) {
		methods := []string{}
		if u.TOTPEnabled() {
			methods = append(methods, "totp")
		}
		if len(creds) > 0 && PasskeyAvailable(set) {
			methods = append(methods, "passkey")
		}
		if n, _ := s.Store.RecoveryCodesRemaining(ctx, u.ID); n > 0 {
			methods = append(methods, "recovery")
		}
		tok, err := NewToken()
		if err != nil {
			return Outcome{}, errInternal
		}
		if err := s.Store.CreateChallenge(ctx, store.AuthChallenge{ID: HashToken(tok), Kind: "login_mfa", UserID: u.ID, ExpiresAt: now.Add(ChallengeTTL)}); err != nil {
			return Outcome{}, errInternal
		}
		return Outcome{Status: "mfa_required", User: u, MFAToken: tok, Methods: methods}, nil
	}
	scope := "full"
	if RequiresMFA(set, u.Role) {
		scope = "enroll_mfa"
	}
	return s.finishLogin(ctx, c, u, "none", scope, set)
}

// finishLogin clears failure counters, rotates into a fresh session and audits the success.
func (s *Service) finishLogin(ctx context.Context, c Client, u store.AuthUser, mfa, scope string, set store.AuthSettings) (Outcome, error) {
	out, err := s.newSession(ctx, u, c, mfa, scope, set)
	if err != nil {
		return out, err
	}
	_ = s.Store.RecordLoginSuccess(ctx, u.ID, s.Now())
	s.UserLimiterReset(u.Username)
	summary := "登录成功"
	if mfa != "none" {
		summary += "（" + mfa + "）"
	}
	if scope == "enroll_mfa" {
		summary += "，需先绑定第二因素"
	}
	s.audit(ctx, u.Username, "auth.login", u.ID, summary)
	return out, nil
}

func (s *Service) UserLimiterReset(username string) {
	s.UserLimiter.Reset("user:" + strings.ToLower(username))
}

// mfaContext validates an mfa_token and returns its user.
func (s *Service) mfaContext(ctx context.Context, c Client, token string) (store.AuthChallenge, store.AuthUser, store.AuthSettings, error) {
	var zc store.AuthChallenge
	var zu store.AuthUser
	set, err := s.Settings(ctx)
	if err != nil {
		return zc, zu, set, errInternal
	}
	if set.Mode == "local" {
		return zc, zu, set, errLocalMode
	}
	if err := s.rateBoth(c, "mfa:"+HashToken(token)); err != nil {
		return zc, zu, set, err
	}
	ch, err := s.Store.PeekChallenge(ctx, HashToken(token), "login_mfa", s.Now())
	if err != nil {
		return zc, zu, set, errMFAToken
	}
	u, err := s.Store.AuthUserByID(ctx, ch.UserID)
	if err != nil || u.Status != "active" {
		return zc, zu, set, errMFAToken
	}
	if u.LockedUntil != nil && s.Now().Before(*u.LockedUntil) {
		return zc, zu, set, lockedErr(*u.LockedUntil)
	}
	return ch, u, set, nil
}

// mfaFailed counts a wrong second factor against the token (max attempts) and the account (lockout).
func (s *Service) mfaFailed(ctx context.Context, ch store.AuthChallenge, u store.AuthUser, set store.AuthSettings, what string) error {
	_, _ = s.Store.BumpChallenge(ctx, ch.ID, MaxMFAAttempts)
	s.audit(ctx, u.Username, "auth.mfa_failed", u.ID, "第二因素验证失败（"+what+"）")
	if e := s.countFailure(ctx, u, set, what); e != nil {
		_, _ = s.Store.DeleteChallenge(ctx, ch.ID)
		return e
	}
	return errInvalidCredentials
}

func (s *Service) mfaDone(ctx context.Context, c Client, ch store.AuthChallenge, u store.AuthUser, set store.AuthSettings, mfa string) (Outcome, error) {
	ok, err := s.Store.DeleteChallenge(ctx, ch.ID) // one-time: only one concurrent request wins
	if err != nil {
		return Outcome{}, errInternal
	}
	if !ok {
		return Outcome{}, errMFAToken
	}
	return s.finishLogin(ctx, c, u, mfa, "full", set)
}

// MFATOTP completes login with a TOTP code.
func (s *Service) MFATOTP(ctx context.Context, c Client, token, code string) (Outcome, error) {
	ch, u, set, err := s.mfaContext(ctx, c, token)
	if err != nil {
		return Outcome{}, err
	}
	if !u.TOTPEnabled() {
		return Outcome{}, s.mfaFailed(ctx, ch, u, set, "TOTP")
	}
	secret, err := s.Box.Unseal(u.TOTPSecretEnc)
	if err != nil {
		return Outcome{}, errInternal
	}
	step, ok := VerifyTOTP(string(secret), code, s.Now(), u.TOTPLastStep)
	if ok {
		if adv, err := s.Store.AdvanceTOTPStep(ctx, u.ID, step); err != nil {
			return Outcome{}, errInternal
		} else if !adv {
			ok = false // replayed concurrently
		}
	}
	if !ok {
		return Outcome{}, s.mfaFailed(ctx, ch, u, set, "TOTP")
	}
	return s.mfaDone(ctx, c, ch, u, set, "totp")
}

// MFARecovery completes login with a one-time recovery code.
func (s *Service) MFARecovery(ctx context.Context, c Client, token, code string) (Outcome, error) {
	ch, u, set, err := s.mfaContext(ctx, c, token)
	if err != nil {
		return Outcome{}, err
	}
	ok, err := s.Store.ConsumeRecoveryCode(ctx, u.ID, HashRecoveryCode(code), s.Now())
	if err != nil {
		return Outcome{}, errInternal
	}
	if !ok {
		return Outcome{}, s.mfaFailed(ctx, ch, u, set, "恢复码")
	}
	n, _ := s.Store.RecoveryCodesRemaining(ctx, u.ID)
	s.audit(ctx, u.Username, "auth.recovery_used", u.ID, fmt.Sprintf("使用恢复码登录，剩余 %d 个", n))
	return s.mfaDone(ctx, c, ch, u, set, "recovery")
}

// Logout ends one session.
func (s *Service) Logout(ctx context.Context, id Identity) {
	if id.Local {
		return
	}
	_, _ = s.Store.DeleteSession(ctx, id.Session.ID)
	s.audit(ctx, id.User.Username, "auth.logout", id.User.ID, "退出登录")
}

// loopbackHost reports whether a host string (no port) is a loopback address or localhost.
func loopbackHost(h string) bool {
	if isLocalhostName(h) {
		return true
	}
	ip := net.ParseIP(strings.Trim(h, "[]"))
	return ip != nil && ip.IsLoopback()
}
