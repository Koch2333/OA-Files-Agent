package api

import (
	"net/http"
	"strings"

	"oa-agent/backend/internal/auth"
	"oa-agent/backend/internal/store"
)

func (s *Server) adminUserRoutes() {
	s.handle("/v1/admin/users", map[string]http.HandlerFunc{"GET": s.adminListUsers, "POST": s.adminCreateUser})
	s.handle("/v1/admin/users/{id}", map[string]http.HandlerFunc{"GET": s.adminGetUser, "PATCH": s.adminUpdateUser, "DELETE": s.adminDeleteUser})
	s.handle("/v1/admin/users/{id}/reset-password", map[string]http.HandlerFunc{"POST": s.adminResetPassword})
	s.handle("/v1/admin/users/{id}/reset-mfa", map[string]http.HandlerFunc{"POST": s.adminResetMFA})
	s.handle("/v1/admin/users/{id}/unlock", map[string]http.HandlerFunc{"POST": s.adminUnlock})
	s.handle("/v1/admin/users/{id}/revoke-sessions", map[string]http.HandlerFunc{"POST": s.adminRevokeSessions})
	s.handle("/v1/admin/users/{id}/invite", map[string]http.HandlerFunc{"POST": s.adminInvite})
	s.handle("/v1/admin/sessions", map[string]http.HandlerFunc{"GET": s.adminSessions})
	s.handle("/v1/admin/sessions/{id}", map[string]http.HandlerFunc{"DELETE": s.adminRevokeSession})
	s.handle("/v1/admin/security-settings", map[string]http.HandlerFunc{"GET": s.adminGetSettings, "PATCH": s.adminPatchSettings})
}

func (s *Server) adminUserView(u store.AuthUserInfo) map[string]any {
	status := u.Status
	if status == "active" && u.LockedUntil != nil && s.d.Auth.Now().Before(*u.LockedUntil) {
		status = "locked"
	}
	return map[string]any{
		"id": u.ID, "username": u.Username, "display_name": u.DisplayName, "role": u.Role, "status": status,
		"has_password": u.PasswordHash != "", "totp_enabled": u.TOTPEnabled(), "passkey_count": u.PasskeyCount, "recovery_codes_remaining": u.RecoveryCodesRemaining,
		"must_change_password": u.MustChangePassword, "failed_logins": u.FailedLogins, "locked_until": rfc3339p(u.LockedUntil), "last_login_at": rfc3339p(u.LastLoginAt), "created_at": rfc3339(u.CreatedAt),
	}
}

func (s *Server) inviteView(i *auth.Invite) map[string]any {
	return map[string]any{"token": i.Token, "path": i.Path, "expires_at": i.ExpiresAt}
}

// actor returns the acting user's name and id for audit and self-protection.
func (s *Server) actor(r *http.Request) (string, string) {
	id, _ := identityOf(r)
	uid := id.User.ID
	if id.Local {
		uid = ""
	}
	return id.User.Username, uid
}

func (s *Server) adminListUsers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if role := q.Get("role"); role != "" && role != "admin" && role != "operator" && role != "viewer" {
		s.fail(w, r, 400, "INVALID_ARGUMENT", "role 取值无效", nil)
		return
	}
	if st := q.Get("status"); st != "" && st != "active" && st != "disabled" && st != "locked" {
		s.fail(w, r, 400, "INVALID_ARGUMENT", "status 取值无效", nil)
		return
	}
	page, size := pageParams(r)
	list, total, err := s.d.Store.ListAuthUsers(r.Context(), store.UserFilter{Q: strings.TrimSpace(q.Get("q")), Role: q.Get("role"), Status: q.Get("status"), Now: s.d.Auth.Now()}, store.Page{Page: page, Size: size})
	if err != nil {
		s.storeErr(w, r, err, "用户")
		return
	}
	items := make([]map[string]any, 0, len(list))
	for _, u := range list {
		items = append(items, s.adminUserView(u))
	}
	s.list(w, items, total, page, size)
}

func (s *Server) adminCreateUser(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
		Role        string `json:"role"`
		Credential  string `json:"credential"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	actor, _ := s.actor(r)
	c, err := s.d.Auth.CreateUser(r.Context(), actor, auth.NewUser{Username: in.Username, DisplayName: in.DisplayName, Role: in.Role, Credential: in.Credential})
	if err != nil {
		s.authFail(w, r, err)
		return
	}
	out := map[string]any{"user": s.adminUserView(c.User)}
	if c.Password != "" {
		out["temporary_password"] = c.Password
	}
	if c.Invite != nil {
		out["invite"] = s.inviteView(c.Invite)
	}
	s.ok(w, 201, out)
}

func (s *Server) adminGetUser(w http.ResponseWriter, r *http.Request) {
	u, err := s.d.Store.AuthUserInfoByID(r.Context(), r.PathValue("id"))
	if err != nil {
		s.storeErr(w, r, err, "用户")
		return
	}
	s.ok(w, 200, s.adminUserView(u))
}

func (s *Server) adminUpdateUser(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DisplayName *string `json:"display_name"`
		Role        *string `json:"role"`
		Status      *string `json:"status"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	actor, _ := s.actor(r)
	u, err := s.d.Auth.UpdateUser(r.Context(), actor, r.PathValue("id"), auth.UserUpdate{DisplayName: in.DisplayName, Role: in.Role, Status: in.Status})
	if err != nil {
		s.authFail(w, r, err)
		return
	}
	s.ok(w, 200, s.adminUserView(u))
}

func (s *Server) adminDeleteUser(w http.ResponseWriter, r *http.Request) {
	if !s.fresh(w, r) {
		return
	}
	actor, uid := s.actor(r)
	if err := s.d.Auth.DeleteUser(r.Context(), actor, uid, r.PathValue("id")); err != nil {
		s.authFail(w, r, err)
		return
	}
	w.WriteHeader(204)
}

func (s *Server) adminResetPassword(w http.ResponseWriter, r *http.Request) {
	if !s.fresh(w, r) {
		return
	}
	actor, _ := s.actor(r)
	pw, err := s.d.Auth.ResetPassword(r.Context(), actor, r.PathValue("id"))
	if err != nil {
		s.authFail(w, r, err)
		return
	}
	s.ok(w, 200, map[string]any{"temporary_password": pw})
}

func (s *Server) adminResetMFA(w http.ResponseWriter, r *http.Request) {
	if !s.fresh(w, r) {
		return
	}
	actor, _ := s.actor(r)
	u, err := s.d.Auth.ResetMFA(r.Context(), actor, r.PathValue("id"))
	if err != nil {
		s.authFail(w, r, err)
		return
	}
	s.ok(w, 200, s.adminUserView(u))
}

func (s *Server) adminUnlock(w http.ResponseWriter, r *http.Request) {
	actor, _ := s.actor(r)
	u, err := s.d.Auth.UnlockUser(r.Context(), actor, r.PathValue("id"))
	if err != nil {
		s.authFail(w, r, err)
		return
	}
	s.ok(w, 200, s.adminUserView(u))
}

func (s *Server) adminRevokeSessions(w http.ResponseWriter, r *http.Request) {
	actor, _ := s.actor(r)
	n, err := s.d.Auth.RevokeUserSessions(r.Context(), actor, r.PathValue("id"))
	if err != nil {
		s.authFail(w, r, err)
		return
	}
	s.ok(w, 200, map[string]any{"revoked": n})
}

func (s *Server) adminInvite(w http.ResponseWriter, r *http.Request) {
	actor, _ := s.actor(r)
	inv, err := s.d.Auth.CreateInvite(r.Context(), actor, r.PathValue("id"))
	if err != nil {
		s.authFail(w, r, err)
		return
	}
	s.ok(w, 201, s.inviteView(inv))
}

func (s *Server) adminSessions(w http.ResponseWriter, r *http.Request) {
	uid := r.URL.Query().Get("user_id")
	if uid != "" && !validUUIDString(uid) {
		s.fail(w, r, 400, "INVALID_ARGUMENT", "user_id 无效", nil)
		return
	}
	list, err := s.d.Auth.AdminSessions(r.Context(), uid)
	if err != nil {
		s.authFail(w, r, err)
		return
	}
	id, _ := identityOf(r)
	out := make([]map[string]any, 0, len(list))
	for _, si := range list {
		out = append(out, sessionInfoView(si, id.Session.ID))
	}
	s.ok(w, 200, out)
}

func validUUIDString(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if c != '-' {
				return false
			}
		case !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'):
			return false
		}
	}
	return true
}

func (s *Server) adminRevokeSession(w http.ResponseWriter, r *http.Request) {
	actor, _ := s.actor(r)
	if err := s.d.Auth.AdminRevokeSession(r.Context(), actor, r.PathValue("id")); err != nil {
		s.authFail(w, r, err)
		return
	}
	w.WriteHeader(204)
}

func settingsView(set store.AuthSettings) map[string]any {
	return map[string]any{
		"mode": set.Mode, "rp_id": set.RPID, "rp_name": set.RPName, "origins": set.Origins, "mfa_policy": set.MFAPolicy, "allow_password_login": set.AllowPasswordLogin,
		"session_idle_minutes": set.SessionIdleMinutes, "session_max_hours": set.SessionMaxHours, "password_min_length": set.PasswordMinLength,
		"lockout_threshold": set.LockoutThreshold, "lockout_minutes": set.LockoutMinutes, "passkey_available": auth.PasskeyAvailable(set),
	}
}

func (s *Server) adminGetSettings(w http.ResponseWriter, r *http.Request) {
	set, err := s.d.Store.AuthSettings(r.Context())
	if err != nil {
		s.storeErr(w, r, err, "设置")
		return
	}
	s.ok(w, 200, settingsView(set))
}

func (s *Server) adminPatchSettings(w http.ResponseWriter, r *http.Request) {
	if !s.fresh(w, r) {
		return
	}
	var in struct {
		RPID                *string   `json:"rp_id"`
		RPName              *string   `json:"rp_name"`
		Origins             *[]string `json:"origins"`
		MFAPolicy           *string   `json:"mfa_policy"`
		AllowPasswordLogin  *bool     `json:"allow_password_login"`
		SessionIdleMinutes  *int      `json:"session_idle_minutes"`
		SessionMaxHours     *int      `json:"session_max_hours"`
		PasswordMinLength   *int      `json:"password_min_length"`
		LockoutThreshold    *int      `json:"lockout_threshold"`
		LockoutMinutes      *int      `json:"lockout_minutes"`
		ConfirmPasskeyReset bool      `json:"confirm_passkey_reset"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	actor, _ := s.actor(r)
	set, err := s.d.Auth.UpdateSettings(r.Context(), actor, auth.SettingsPatch{RPID: in.RPID, RPName: in.RPName, Origins: in.Origins, MFAPolicy: in.MFAPolicy, AllowPasswordLogin: in.AllowPasswordLogin,
		SessionIdleMinutes: in.SessionIdleMinutes, SessionMaxHours: in.SessionMaxHours, PasswordMinLength: in.PasswordMinLength, LockoutThreshold: in.LockoutThreshold, LockoutMinutes: in.LockoutMinutes, ConfirmPasskeyReset: in.ConfirmPasskeyReset})
	if err != nil {
		s.authFail(w, r, err)
		return
	}
	s.ok(w, 200, settingsView(set))
}
