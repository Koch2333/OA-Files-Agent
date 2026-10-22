package auth

import (
	"context"
	"errors"
	"net"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"oa-agent/backend/internal/imports"
	"oa-agent/backend/internal/store"
)

var usernameRE = regexp.MustCompile(`^[A-Za-z0-9._@-]{3,64}$`)

var errAlreadySetup = fail(409, "ALREADY_SETUP", "系统已完成初始化")

// HostInfo describes how the browser reached the server (for relying-party suggestions and validation).
type HostInfo struct {
	Hostname string // without port
	HostPort string // as in the Host header (browser origin = scheme://HostPort)
	Scheme   string // http | https
	Loopback bool   // request came from the local machine (not through a proxy)
}

// effective applies OA_PUBLIC_URL, which overrides what the request claims.
func (s *Service) effective(h HostInfo) HostInfo {
	if s.PublicURL == "" {
		return h
	}
	u, err := url.Parse(s.PublicURL)
	if err != nil || u.Host == "" {
		return h
	}
	return HostInfo{Hostname: u.Hostname(), HostPort: u.Host, Scheme: u.Scheme, Loopback: h.Loopback}
}

func isIP(h string) bool { return net.ParseIP(strings.Trim(h, "[]")) != nil }

// Suggest returns RP ID / origin suggestions and whether passkeys can work for this access path.
func (s *Service) Suggest(h HostInfo) (rpID, origin string, passkey bool) {
	e := s.effective(h)
	if e.Hostname == "" {
		return "", "", false
	}
	origin = e.Scheme + "://" + e.HostPort
	secure := e.Scheme == "https" || (e.Scheme == "http" && isLocalhostName(e.Hostname))
	if isIP(e.Hostname) {
		return "", origin, false
	}
	return strings.ToLower(e.Hostname), origin, secure
}

// ValidateRP checks the relying-party identity structurally: RP ID is a domain name, every origin is an exact
// secure origin whose host is the RP ID or a subdomain of it.
func ValidateRP(rpID string, origins []string) error {
	if rpID == "" && len(origins) == 0 {
		return nil
	}
	if rpID == "" || len(origins) == 0 {
		return fail(422, "INVALID_ARGUMENT", "RP ID 与允许的来源必须同时设置")
	}
	if rpID != strings.ToLower(rpID) || strings.ContainsAny(rpID, "/:@ ") || isIP(rpID) || len(rpID) > 253 || !strings.Contains(rpID+".", ".") {
		return fail(422, "INVALID_ARGUMENT", "RP ID 必须是域名（不含协议、端口，不能是 IP 地址）")
	}
	for _, o := range origins {
		u, err := url.Parse(o)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil || strings.Contains(o, "*") || strings.HasSuffix(o, "/") {
			return fail(422, "INVALID_ARGUMENT", "来源必须是精确的 origin（如 https://oa.example.com），不能含路径或通配符："+o)
		}
		if !secureOrigin(o) {
			return fail(422, "INVALID_ARGUMENT", "Passkey 只能用于 HTTPS（或 localhost）来源："+o)
		}
		host := strings.ToLower(u.Hostname())
		if host != rpID && !strings.HasSuffix(host, "."+rpID) {
			return fail(422, "INVALID_ARGUMENT", "来源的域名必须等于 RP ID 或是其子域名："+o)
		}
	}
	return nil
}

// ValidateUsername enforces 3–64 characters of [A-Za-z0-9._@-].
func ValidateUsername(n string) error {
	if !usernameRE.MatchString(n) {
		return fail(422, "INVALID_ARGUMENT", "用户名需为 3–64 位字母、数字或 . _ @ -")
	}
	if strings.EqualFold(n, LocalUserName) {
		return fail(422, "INVALID_ARGUMENT", "该用户名为系统保留")
	}
	return nil
}

// EnsureSetupToken creates a fresh setup token (replacing any earlier one) while setup is pending; it returns "" when
// the system is already initialised. Only the hash is stored.
func (s *Service) EnsureSetupToken(ctx context.Context) (string, error) {
	set, err := s.Store.AuthSettings(ctx)
	if err != nil {
		return "", err
	}
	if set.SetupCompleted {
		return "", nil
	}
	tok, err := NewSetupToken()
	if err != nil {
		return "", err
	}
	if err := s.Store.SetSetupToken(ctx, HashToken(tok)); err != nil {
		if errors.Is(err, store.ErrAlreadySetup) {
			return "", nil
		}
		return "", err
	}
	s.invalidate()
	return tok, nil
}

func (s *Service) checkToken(stored, given string) bool {
	if stored == "" || given == "" {
		return false
	}
	return EqualHash(stored, HashToken(NormalizeSetupToken(given)))
}

// VerifySetupToken tells the wizard whether the token it was given is right (rate limited per address).
func (s *Service) VerifySetupToken(ctx context.Context, c Client, token string) error {
	if err := s.rate(s.SetupLimiter, "setup:"+c.IP); err != nil {
		return err
	}
	set, err := s.Store.AuthSettings(ctx)
	if err != nil {
		return errInternal
	}
	if set.SetupCompleted {
		return errAlreadySetup
	}
	if !s.checkToken(set.SetupTokenHash, token) {
		return fail(401, "INVALID_SETUP_TOKEN", "初始化令牌不正确")
	}
	return nil
}

type SetupAdmin struct{ Username, DisplayName, Password string }

type SetupRequest struct {
	Token              string
	Mode               string
	Admin              *SetupAdmin
	RPID, RPName       string
	Origins            []string
	MFAPolicy          string
	AllowPasswordLogin *bool
	SessionIdleMinutes *int
	SessionMaxHours    *int
	PasswordMinLength  *int
}

// CompleteSetup validates the request and atomically creates the administrator and policy. A request from the local
// machine may skip the token; a token that is supplied must always be correct.
func (s *Service) CompleteSetup(ctx context.Context, c Client, req SetupRequest, h HostInfo) (Outcome, error) {
	if err := s.rate(s.SetupLimiter, "setup:"+c.IP); err != nil {
		return Outcome{}, err
	}
	cur, err := s.Store.AuthSettings(ctx)
	if err != nil {
		return Outcome{}, errInternal
	}
	if cur.SetupCompleted {
		return Outcome{}, errAlreadySetup
	}
	if req.Mode != "enterprise" && req.Mode != "local" {
		return Outcome{}, fail(422, "INVALID_ARGUMENT", "mode 必须是 enterprise 或 local")
	}
	if req.Mode == "local" && !h.Loopback {
		return Outcome{}, fail(403, "LOCAL_ONLY", "local 模式只能由本机（回环地址）发起的向导选择")
	}
	needToken := !h.Loopback || req.Token != ""
	if needToken && !s.checkToken(cur.SetupTokenHash, req.Token) {
		return Outcome{}, fail(401, "INVALID_SETUP_TOKEN", "初始化令牌不正确")
	}
	set := cur
	set.Mode = req.Mode
	if req.RPName = strings.TrimSpace(req.RPName); req.RPName != "" {
		if utf8.RuneCountInString(req.RPName) > 64 {
			return Outcome{}, fail(422, "INVALID_ARGUMENT", "名称过长")
		}
		set.RPName = req.RPName
	}
	switch req.MFAPolicy {
	case "":
	case "off", "admins", "all":
		set.MFAPolicy = req.MFAPolicy
	default:
		return Outcome{}, fail(422, "INVALID_ARGUMENT", "mfa_policy 取值无效")
	}
	if req.AllowPasswordLogin != nil {
		set.AllowPasswordLogin = *req.AllowPasswordLogin
	}
	for _, p := range []struct {
		v      *int
		dst    *int
		lo, hi int
		name   string
	}{
		{req.SessionIdleMinutes, &set.SessionIdleMinutes, 5, 1440, "session_idle_minutes"},
		{req.SessionMaxHours, &set.SessionMaxHours, 1, 720, "session_max_hours"},
		{req.PasswordMinLength, &set.PasswordMinLength, 8, 128, "password_min_length"},
	} {
		if p.v != nil {
			if *p.v < p.lo || *p.v > p.hi {
				return Outcome{}, fail(422, "INVALID_ARGUMENT", p.name+" 超出范围")
			}
			*p.dst = *p.v
		}
	}
	// Relying party: explicit values are validated against the address the wizard is opened on; empty values fall back to
	// the suggestion when passkeys can work at all.
	rpID, origin, pk := s.Suggest(h)
	set.RPID, set.Origins = strings.ToLower(strings.TrimSpace(req.RPID)), req.Origins
	if set.RPID == "" && len(set.Origins) == 0 && pk && req.Mode == "enterprise" {
		set.RPID, set.Origins = rpID, []string{origin}
	}
	if err := ValidateRP(set.RPID, set.Origins); err != nil {
		return Outcome{}, err
	}
	if set.RPID != "" {
		e := s.effective(h)
		if e.Hostname != "" && !isIP(e.Hostname) && e.Hostname != set.RPID && !strings.HasSuffix(strings.ToLower(e.Hostname), "."+set.RPID) {
			return Outcome{}, fail(422, "INVALID_ARGUMENT", "RP ID 必须是当前访问域名（"+e.Hostname+"）或其上级域名")
		}
	}
	in := store.SetupInput{Settings: set}
	if needToken {
		in.VerifyToken = func(stored string) bool { return s.checkToken(stored, req.Token) }
	}
	var admin store.AuthUser
	if req.Mode == "enterprise" {
		a := req.Admin
		if a == nil {
			return Outcome{}, fail(422, "INVALID_ARGUMENT", "enterprise 模式需要创建管理员")
		}
		if err := ValidateUsername(a.Username); err != nil {
			return Outcome{}, err
		}
		if !set.AllowPasswordLogin {
			return Outcome{}, fail(422, "INVALID_ARGUMENT", "初始化时必须允许密码登录；绑定 Passkey 后可在安全设置中关闭")
		}
		if msg := CheckPasswordPolicy(a.Password, a.Username, set.PasswordMinLength); msg != "" {
			return Outcome{}, fail(422, "WEAK_PASSWORD", msg)
		}
		hash, err := HashPassword(a.Password)
		if err != nil {
			return Outcome{}, errInternal
		}
		handle, err := randomHandle()
		if err != nil {
			return Outcome{}, errInternal
		}
		id, err := imports.NewID()
		if err != nil {
			return Outcome{}, errInternal
		}
		name := strings.TrimSpace(a.DisplayName)
		if name == "" {
			name = a.Username
		}
		if utf8.RuneCountInString(name) > 64 {
			return Outcome{}, fail(422, "INVALID_ARGUMENT", "显示名过长")
		}
		admin = store.AuthUser{ID: id, Username: a.Username, DisplayName: name, Role: "admin", Status: "active", PasswordHash: hash, WebAuthnHandle: handle}
		in.Admin = &admin
	}
	if err := s.Store.CompleteSetup(ctx, in); err != nil {
		switch {
		case errors.Is(err, store.ErrAlreadySetup):
			return Outcome{}, errAlreadySetup
		case errors.Is(err, store.ErrBadSetupToken):
			return Outcome{}, fail(401, "INVALID_SETUP_TOKEN", "初始化令牌不正确")
		case errors.Is(err, store.ErrUserExists):
			return Outcome{}, fail(409, "ALREADY_SETUP", "系统已完成初始化")
		}
		return Outcome{}, errInternal
	}
	s.invalidate()
	if req.Mode == "local" {
		s.audit(ctx, LocalUserName, "setup.complete", "-", "完成初始化（本机 local 模式）")
		return Outcome{Status: "ok", User: LocalIdentity(set, s.Now()).User}, nil
	}
	s.audit(ctx, admin.Username, "setup.complete", admin.ID, "完成初始化（enterprise 模式），创建管理员 "+admin.Username)
	scope := "full"
	if RequiresMFA(set, "admin") {
		scope = "enroll_mfa"
	}
	return s.finishLogin(ctx, c, admin, "none", scope, set)
}

// FinishWizard closes the setup wizard: the administrator must be fully signed in, and when the policy demands a second
// factor they must have enrolled one. Idempotent; audited as setup.finished the first time.
func (s *Service) FinishWizard(ctx context.Context, id Identity) (store.AuthSettings, error) {
	set, err := s.Store.AuthSettings(ctx)
	if err != nil {
		return set, errInternal
	}
	if set.WizardFinished || id.Local {
		return set, nil
	}
	if id.User.Role != "admin" {
		return set, fail(403, "FORBIDDEN", "当前角色没有权限执行此操作")
	}
	if RequiresMFA(set, id.User.Role) {
		creds, err := s.Store.Credentials(ctx, id.User.ID)
		if err != nil {
			return set, errInternal
		}
		if !userHasMFA(id.User, len(creds)) {
			return set, fail(409, "MFA_REQUIRED_BY_POLICY", "安全策略要求先绑定第二因素（TOTP 或 Passkey）")
		}
	}
	if err := s.Store.FinishWizard(ctx); err != nil {
		return set, errInternal
	}
	s.invalidate()
	s.audit(ctx, id.User.Username, "setup.finished", id.User.ID, "完成初始化向导")
	set.WizardFinished = true
	return set, nil
}
