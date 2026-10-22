package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"oa-agent/backend/internal/imports"
	"oa-agent/backend/internal/store"
)

var validRoles = map[string]bool{"admin": true, "operator": true, "viewer": true}

func (s *Service) mapUserErr(err error) error {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return fail(404, "NOT_FOUND", "用户不存在")
	case errors.Is(err, store.ErrLastAdmin):
		return fail(409, "LAST_ADMIN", "不能停用、降级或删除最后一个启用的管理员")
	case errors.Is(err, store.ErrUserExists):
		return fail(409, "USER_EXISTS", "用户名已存在")
	}
	return errInternal
}

// Invite is a freshly created invitation (the token exists in plaintext only in this value).
type Invite struct {
	Token     string
	Path      string
	ExpiresAt string
}

type CreatedUser struct {
	User     store.AuthUserInfo
	Password string
	Invite   *Invite
}

type NewUser struct {
	Username, DisplayName, Role, Credential string
	// BreakGlass (oa-admin only) creates a password user even when password login is disabled in the policy.
	BreakGlass bool
}

func (s *Service) createInvite(ctx context.Context, userID string) (*Invite, error) {
	tok, err := NewToken()
	if err != nil {
		return nil, errInternal
	}
	exp := s.Now().Add(InviteTTL)
	if err := s.Store.CreateInvitation(ctx, userID, HashToken(tok), exp); err != nil {
		return nil, errInternal
	}
	return &Invite{Token: tok, Path: "/invite/" + tok, ExpiresAt: exp.UTC().Format("2006-01-02T15:04:05Z07:00")}, nil
}

// CreateUser adds a user with a one-time temporary password or a passkey invitation.
func (s *Service) CreateUser(ctx context.Context, actor string, in NewUser) (CreatedUser, error) {
	set, err := s.Settings(ctx)
	if err != nil {
		return CreatedUser{}, errInternal
	}
	if err := ValidateUsername(in.Username); err != nil {
		return CreatedUser{}, err
	}
	if !validRoles[in.Role] {
		return CreatedUser{}, fail(422, "INVALID_ARGUMENT", "角色取值无效")
	}
	name := strings.TrimSpace(in.DisplayName)
	if name == "" {
		name = in.Username
	}
	if utf8.RuneCountInString(name) > 64 {
		return CreatedUser{}, fail(422, "INVALID_ARGUMENT", "显示名过长")
	}
	handle, err := randomHandle()
	if err != nil {
		return CreatedUser{}, errInternal
	}
	id, err := imports.NewID()
	if err != nil {
		return CreatedUser{}, errInternal
	}
	u := store.AuthUser{ID: id, Username: in.Username, DisplayName: name, Role: in.Role, Status: "active", WebAuthnHandle: handle}
	out := CreatedUser{}
	switch in.Credential {
	case "password":
		if !set.AllowPasswordLogin && !in.BreakGlass {
			return out, fail(409, "PASSWORD_LOGIN_DISABLED", "已禁用密码登录，请创建 Passkey 邀请")
		}
		pw, err := TempPassword()
		if err != nil {
			return out, errInternal
		}
		if u.PasswordHash, err = HashPassword(pw); err != nil {
			return out, errInternal
		}
		u.MustChangePassword = true
		out.Password = pw
	case "passkey_invite":
		if !PasskeyAvailable(set) {
			return out, errPasskeyUnavailable
		}
	default:
		return out, fail(422, "INVALID_ARGUMENT", "credential 取值无效")
	}
	if err := s.Store.CreateAuthUser(ctx, u); err != nil {
		return CreatedUser{}, s.mapUserErr(err)
	}
	if in.Credential == "passkey_invite" {
		if out.Invite, err = s.createInvite(ctx, u.ID); err != nil {
			return out, err
		}
	}
	info, err := s.Store.AuthUserInfoByID(ctx, u.ID)
	if err != nil {
		return out, errInternal
	}
	out.User = info
	s.audit(ctx, actor, "user.create", u.ID, fmt.Sprintf("创建用户 %s（%s，%s）", u.Username, u.Role, in.Credential))
	return out, nil
}

type UserUpdate struct {
	DisplayName *string
	Role        *string
	Status      *string
}

// UpdateUser edits display name, role or status; the last active administrator is protected.
func (s *Service) UpdateUser(ctx context.Context, actor, id string, in UserUpdate) (store.AuthUserInfo, error) {
	p := store.UserPatch{}
	if in.DisplayName != nil {
		n := strings.TrimSpace(*in.DisplayName)
		if utf8.RuneCountInString(n) > 64 {
			return store.AuthUserInfo{}, fail(400, "INVALID_ARGUMENT", "显示名过长")
		}
		p.DisplayName = &n
	}
	if in.Role != nil {
		if !validRoles[*in.Role] {
			return store.AuthUserInfo{}, fail(400, "INVALID_ARGUMENT", "角色取值无效")
		}
		p.Role = in.Role
	}
	if in.Status != nil {
		if *in.Status != "active" && *in.Status != "disabled" {
			return store.AuthUserInfo{}, fail(400, "INVALID_ARGUMENT", "状态取值无效")
		}
		p.Status = in.Status
	}
	before, err := s.Store.AuthUserByID(ctx, id)
	if err != nil {
		return store.AuthUserInfo{}, s.mapUserErr(err)
	}
	u, err := s.Store.UpdateAuthUser(ctx, id, p)
	if err != nil {
		return store.AuthUserInfo{}, s.mapUserErr(err)
	}
	var ch []string
	if before.Role != u.Role {
		ch = append(ch, "角色 "+before.Role+"→"+u.Role)
	}
	if before.Status != u.Status {
		ch = append(ch, "状态 "+before.Status+"→"+u.Status)
	}
	if before.DisplayName != u.DisplayName {
		ch = append(ch, "显示名")
	}
	s.audit(ctx, actor, "user.update", u.ID, "修改用户 "+u.Username+"："+strings.Join(ch, "，"))
	return s.Store.AuthUserInfoByID(ctx, id)
}

func (s *Service) DeleteUser(ctx context.Context, actor, actorUserID, id string) error {
	if actorUserID != "" && actorUserID == id {
		return fail(409, "CANNOT_DELETE_SELF", "不能删除自己")
	}
	u, err := s.Store.AuthUserByID(ctx, id)
	if err != nil {
		return s.mapUserErr(err)
	}
	if err := s.Store.DeleteAuthUser(ctx, id); err != nil {
		return s.mapUserErr(err)
	}
	s.audit(ctx, actor, "user.delete", id, "删除用户 "+u.Username)
	return nil
}

// ResetPassword issues a new one-time temporary password and ends the user's sessions.
func (s *Service) ResetPassword(ctx context.Context, actor, id string) (string, error) {
	u, err := s.Store.AuthUserByID(ctx, id)
	if err != nil {
		return "", s.mapUserErr(err)
	}
	pw, err := TempPassword()
	if err != nil {
		return "", errInternal
	}
	h, err := HashPassword(pw)
	if err != nil {
		return "", errInternal
	}
	if err := s.Store.SetPassword(ctx, id, h, true); err != nil {
		return "", s.mapUserErr(err)
	}
	_, _ = s.Store.DeleteUserSessions(ctx, id, "")
	s.audit(ctx, actor, "user.reset_password", id, "重置用户 "+u.Username+" 的密码并吊销其全部会话")
	return pw, nil
}

func (s *Service) ResetMFA(ctx context.Context, actor, id string) (store.AuthUserInfo, error) {
	u, err := s.Store.AuthUserByID(ctx, id)
	if err != nil {
		return store.AuthUserInfo{}, s.mapUserErr(err)
	}
	if err := s.Store.ClearMFA(ctx, id); err != nil {
		return store.AuthUserInfo{}, errInternal
	}
	s.audit(ctx, actor, "user.reset_mfa", id, "重置用户 "+u.Username+" 的 TOTP、Passkey 与恢复码")
	return s.Store.AuthUserInfoByID(ctx, id)
}

func (s *Service) UnlockUser(ctx context.Context, actor, id string) (store.AuthUserInfo, error) {
	u, err := s.Store.AuthUserByID(ctx, id)
	if err != nil {
		return store.AuthUserInfo{}, s.mapUserErr(err)
	}
	if err := s.Store.UnlockAuthUser(ctx, id); err != nil {
		return store.AuthUserInfo{}, s.mapUserErr(err)
	}
	s.ghosts.clear(strings.ToLower(u.Username))
	s.UserLimiterReset(u.Username)
	s.audit(ctx, actor, "user.unlock", id, "解锁用户 "+u.Username)
	return s.Store.AuthUserInfoByID(ctx, id)
}

func (s *Service) RevokeUserSessions(ctx context.Context, actor, id string) (int, error) {
	u, err := s.Store.AuthUserByID(ctx, id)
	if err != nil {
		return 0, s.mapUserErr(err)
	}
	n, err := s.Store.DeleteUserSessions(ctx, id, "")
	if err != nil {
		return 0, errInternal
	}
	s.audit(ctx, actor, "session.revoke_all", id, fmt.Sprintf("吊销用户 %s 的全部会话（%d 个）", u.Username, n))
	return n, nil
}

func (s *Service) AdminRevokeSession(ctx context.Context, actor, sessionID string) error {
	si, err := s.Store.SessionByID(ctx, sessionID)
	if err != nil {
		return fail(404, "NOT_FOUND", "会话不存在")
	}
	if _, err := s.Store.DeleteSession(ctx, sessionID); err != nil {
		return errInternal
	}
	s.audit(ctx, actor, "session.revoke", si.UserID, "吊销用户 "+si.Username+" 的会话")
	return nil
}

func (s *Service) AdminSessions(ctx context.Context, userID string) ([]store.AuthSessionInfo, error) {
	out, err := s.Store.ListSessions(ctx, userID, s.Now())
	if err != nil {
		return nil, errInternal
	}
	return out, nil
}

// CreateInvite issues a new 24-hour passkey invitation (older ones stop working).
func (s *Service) CreateInvite(ctx context.Context, actor, id string) (*Invite, error) {
	set, err := s.Settings(ctx)
	if err != nil {
		return nil, errInternal
	}
	u, err := s.Store.AuthUserByID(ctx, id)
	if err != nil {
		return nil, s.mapUserErr(err)
	}
	if u.Status != "active" {
		return nil, fail(409, "INVALID_STATE", "用户已停用")
	}
	if !PasskeyAvailable(set) {
		return nil, errPasskeyUnavailable
	}
	inv, err := s.createInvite(ctx, id)
	if err != nil {
		return nil, err
	}
	s.audit(ctx, actor, "user.invite", id, "为用户 "+u.Username+" 生成 Passkey 邀请")
	return inv, nil
}

// --- invitations (public side)

var errInviteNotFound = fail(404, "NOT_FOUND", "邀请不存在或已失效")

func (s *Service) liveInvitation(ctx context.Context, token string) (store.AuthUser, store.Invitation, error) {
	if len(token) > 128 {
		return store.AuthUser{}, store.Invitation{}, errInviteNotFound
	}
	u, inv, err := s.Store.InvitationUser(ctx, HashToken(token), s.Now())
	if err != nil || u.Status != "active" {
		return u, inv, errInviteNotFound
	}
	return u, inv, nil
}

type InvitationView struct {
	Username, DisplayName string
	ExpiresAt             string
	PasskeyAvailable      bool
}

func (s *Service) Invitation(ctx context.Context, c Client, token string) (InvitationView, error) {
	if err := s.rate(s.IPLimiter, "ip:"+c.IP); err != nil {
		return InvitationView{}, err
	}
	set, err := s.Settings(ctx)
	if err != nil {
		return InvitationView{}, errInternal
	}
	u, inv, err := s.liveInvitation(ctx, token)
	if err != nil {
		return InvitationView{}, err
	}
	return InvitationView{Username: u.Username, DisplayName: u.DisplayName, ExpiresAt: inv.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z07:00"), PasskeyAvailable: PasskeyAvailable(set)}, nil
}

func (s *Service) InvitationPasskeyBegin(ctx context.Context, c Client, token string) (WebAuthnOptions, error) {
	if err := s.rate(s.IPLimiter, "ip:"+c.IP); err != nil {
		return WebAuthnOptions{}, err
	}
	set, err := s.Settings(ctx)
	if err != nil {
		return WebAuthnOptions{}, errInternal
	}
	u, _, err := s.liveInvitation(ctx, token)
	if err != nil {
		return WebAuthnOptions{}, err
	}
	return s.beginRegistration(ctx, set, u, "webauthn_invite", HashToken(token))
}

// InvitationPasskeyFinish registers the first passkey, burns the invitation and logs the user in.
func (s *Service) InvitationPasskeyFinish(ctx context.Context, c Client, token, challengeID, name string, credential []byte) (Outcome, error) {
	if err := s.rate(s.IPLimiter, "ip:"+c.IP); err != nil {
		return Outcome{}, err
	}
	set, err := s.Settings(ctx)
	if err != nil {
		return Outcome{}, errInternal
	}
	u, _, err := s.liveInvitation(ctx, token)
	if err != nil {
		return Outcome{}, err
	}
	ch, cd, err := s.takeChallenge(ctx, challengeID, "webauthn_invite")
	if err != nil || ch.UserID != u.ID || !EqualHash(cd.Parent, HashToken(token)) {
		return Outcome{}, fail(422, "CHALLENGE_EXPIRED", "验证已过期，请重新开始")
	}
	row, err := s.finishRegistration(ctx, set, u, cd, credential, name)
	if err != nil {
		return Outcome{}, err
	}
	u2, err := s.Store.AcceptInvitation(ctx, HashToken(token), s.Now(), row)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Outcome{}, errInviteNotFound
		}
		return Outcome{}, errInternal
	}
	s.audit(ctx, u2.Username, "invite.accept", u2.ID, "接受邀请并注册 Passkey")
	// A passkey is a full multi-factor login; recovery codes are issued from the account page.
	return s.finishLogin(ctx, c, u2, "passkey", "full", set)
}

// --- security settings

type SettingsPatch struct {
	RPID, RPName        *string
	Origins             *[]string
	MFAPolicy           *string
	AllowPasswordLogin  *bool
	SessionIdleMinutes  *int
	SessionMaxHours     *int
	PasswordMinLength   *int
	LockoutThreshold    *int
	LockoutMinutes      *int
	ConfirmPasskeyReset bool
}

func inRange(v *int, lo, hi int, name string) error {
	if v != nil && (*v < lo || *v > hi) {
		return fail(422, "INVALID_ARGUMENT", name+" 超出范围")
	}
	return nil
}

// UpdateSettings applies a security-settings change with the consistency rules of the design.
func (s *Service) UpdateSettings(ctx context.Context, actor string, p SettingsPatch) (store.AuthSettings, error) {
	cur, err := s.Store.AuthSettings(ctx)
	if err != nil {
		return cur, errInternal
	}
	next := cur
	var changed []string
	for _, r := range []error{
		inRange(p.SessionIdleMinutes, 5, 1440, "session_idle_minutes"), inRange(p.SessionMaxHours, 1, 720, "session_max_hours"),
		inRange(p.PasswordMinLength, 8, 128, "password_min_length"), inRange(p.LockoutThreshold, 3, 20, "lockout_threshold"), inRange(p.LockoutMinutes, 1, 1440, "lockout_minutes"),
	} {
		if r != nil {
			return cur, r
		}
	}
	setInt := func(v *int, dst *int, name string) {
		if v != nil && *v != *dst {
			*dst = *v
			changed = append(changed, name)
		}
	}
	setInt(p.SessionIdleMinutes, &next.SessionIdleMinutes, "session_idle_minutes")
	setInt(p.SessionMaxHours, &next.SessionMaxHours, "session_max_hours")
	setInt(p.PasswordMinLength, &next.PasswordMinLength, "password_min_length")
	setInt(p.LockoutThreshold, &next.LockoutThreshold, "lockout_threshold")
	setInt(p.LockoutMinutes, &next.LockoutMinutes, "lockout_minutes")
	if p.RPName != nil {
		n := strings.TrimSpace(*p.RPName)
		if n == "" || utf8.RuneCountInString(n) > 64 {
			return cur, fail(422, "INVALID_ARGUMENT", "rp_name 需要 1–64 个字符")
		}
		if n != next.RPName {
			next.RPName = n
			changed = append(changed, "rp_name")
		}
	}
	if p.MFAPolicy != nil {
		switch *p.MFAPolicy {
		case "off", "admins", "all":
		default:
			return cur, fail(422, "INVALID_ARGUMENT", "mfa_policy 取值无效")
		}
		if *p.MFAPolicy != next.MFAPolicy {
			next.MFAPolicy = *p.MFAPolicy
			changed = append(changed, "mfa_policy")
		}
	}
	rpChanged, originsChanged := false, false
	if p.RPID != nil && strings.ToLower(strings.TrimSpace(*p.RPID)) != cur.RPID {
		next.RPID = strings.ToLower(strings.TrimSpace(*p.RPID))
		rpChanged = true
		changed = append(changed, "rp_id")
	}
	if p.Origins != nil {
		o := make([]string, 0, len(*p.Origins))
		for _, x := range *p.Origins {
			if x = strings.TrimSpace(x); x != "" {
				o = append(o, x)
			}
		}
		if strings.Join(o, "\n") != strings.Join(cur.Origins, "\n") {
			next.Origins = o
			originsChanged = true
			changed = append(changed, "origins")
		}
	}
	if rpChanged || originsChanged {
		if err := ValidateRP(next.RPID, next.Origins); err != nil {
			return cur, err
		}
		n, err := s.Store.CountPasskeys(ctx)
		if err != nil {
			return cur, errInternal
		}
		if n > 0 && !p.ConfirmPasskeyReset {
			e := fail(422, "PASSKEY_RESET_REQUIRED", "修改 RP ID / 来源会使已注册的 Passkey 失效，请确认后重试（confirm_passkey_reset=true）")
			e.Details = map[string]any{"passkeys": n}
			return cur, e
		}
	}
	if p.AllowPasswordLogin != nil && *p.AllowPasswordLogin != next.AllowPasswordLogin {
		next.AllowPasswordLogin = *p.AllowPasswordLogin
		changed = append(changed, "allow_password_login")
	}
	if cur.AllowPasswordLogin && !next.AllowPasswordLogin {
		if !PasskeyAvailable(next) {
			return cur, errPasskeyUnavailable
		}
		n, err := s.Store.CountUsersWithoutPasskey(ctx)
		if err != nil {
			return cur, errInternal
		}
		if n > 0 && !(rpChanged && p.ConfirmPasskeyReset) {
			e := fail(409, "LOGIN_METHOD_IN_USE", fmt.Sprintf("仍有 %d 个启用的账号没有 Passkey，禁用密码登录会使他们无法登录", n))
			e.Details = map[string]any{"users_without_passkey": n}
			return cur, e
		}
		if rpChanged {
			return cur, fail(409, "LOGIN_METHOD_IN_USE", "修改 RP ID 会使所有 Passkey 失效，不能同时禁用密码登录")
		}
	}
	if len(changed) == 0 {
		return cur, nil
	}
	if rpChanged {
		if err := s.Store.DeleteAllCredentials(ctx); err != nil {
			return cur, errInternal
		}
	}
	if err := s.Store.SaveAuthSettings(ctx, next); err != nil {
		return cur, errInternal
	}
	s.invalidate()
	summary := "修改安全设置：" + strings.Join(changed, "、")
	if rpChanged {
		summary += "（已使全部 Passkey 失效）"
	}
	s.audit(ctx, actor, "settings.update", "auth", summary)
	return s.Store.AuthSettings(ctx)
}
