package api

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"oa-agent/backend/internal/auth"
	"oa-agent/backend/internal/buildinfo"
	"oa-agent/backend/internal/diag"
)

func (s *Server) setupRoutes() {
	s.handle("/v1/setup/status", map[string]http.HandlerFunc{"GET": s.setupStatus})
	s.handle("/v1/setup/verify-token", map[string]http.HandlerFunc{"POST": s.setupVerifyToken})
	s.handle("/v1/setup/complete", map[string]http.HandlerFunc{"POST": s.setupComplete})
	s.handle("/v1/setup/finish", map[string]http.HandlerFunc{"POST": s.setupFinish})
}

// hostInfo describes how the caller reached the server: hostname, scheme and whether it is a genuine local request.
func (s *Server) hostInfo(r *http.Request) auth.HostInfo {
	hp := r.Host
	if s.d.TrustedProxy {
		if fh := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Host"), ",")[0]); fh != "" {
			hp = fh
		}
	}
	host := hp
	if h, _, err := net.SplitHostPort(hp); err == nil {
		host = h
	}
	scheme := "http"
	if s.isSecure(r) {
		scheme = "https"
	}
	return auth.HostInfo{Hostname: strings.ToLower(strings.Trim(host, "[]")), HostPort: hp, Scheme: scheme, Loopback: s.fromLoopback(r)}
}

// dependencyStates runs the database and worker checks concurrently with the readiness timeout.
func (s *Server) dependencyStates(ctx context.Context) (db, worker string) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	states := []string{"not_configured", "not_configured"}
	var wg sync.WaitGroup
	for i := 0; i < 2 && i < len(s.d.Checks); i++ {
		if s.d.Checks[i] == nil {
			continue
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			states[i] = "error"
			diag.Safe("api.dependency_check", func() { states[i] = s.d.Checks[i](ctx) })
		}(i)
	}
	wg.Wait()
	return states[0], states[1]
}

func (s *Server) setupStatus(w http.ResponseWriter, r *http.Request) {
	set, err := s.d.Auth.Settings(r.Context())
	if err != nil {
		s.fail(w, r, 500, "INTERNAL", "内部错误", nil)
		return
	}
	h := s.hostInfo(r)
	rp, origin, pk := s.d.Auth.Suggest(h)
	httpsOK := strings.HasPrefix(origin, "https://")
	db, worker := s.dependencyStates(r.Context())
	check := func(name string, ok bool, good, bad string) map[string]any {
		m := bad
		if ok {
			m = good
		}
		return map[string]any{"name": name, "ok": ok, "message": m}
	}
	passkeyMsg := "可使用 Passkey"
	if !pk {
		passkeyMsg = "Passkey 需要 HTTPS（或 localhost）和域名访问；当前可只使用 TOTP"
	}
	data := map[string]any{
		"initialized": set.SetupCompleted, "wizard_finished": set.SetupCompleted && set.WizardFinished, "server_version": buildinfo.Version, "api_level": APILevel, "needs_token": !h.Loopback, "loopback": h.Loopback, "https": httpsOK, "passkey_available": pk,
		"rp_id_suggestion": rp, "origin_suggestion": origin, "min_password_length": set.PasswordMinLength,
		"checks": []map[string]any{
			check("database", db == "ok", "数据库连接正常", "数据库不可用（"+db+"）"),
			check("worker", worker == "ok", "文档解析 Worker 正常", "文档解析 Worker 不可用（"+worker+"），导入的文件将无法识别"),
			check("https", httpsOK, "当前使用 HTTPS", "当前未使用 HTTPS：会话 Cookie 不带 Secure 标记，Passkey 不可用（localhost 除外）"),
			check("passkey", pk, passkeyMsg, passkeyMsg),
		},
	}
	if set.SetupCompleted {
		data["mode"] = set.Mode
		data["allow_password_login"] = set.AllowPasswordLogin
	}
	s.ok(w, 200, data)
}

func (s *Server) setupVerifyToken(w http.ResponseWriter, r *http.Request) {
	var in struct{ Token string }
	if !s.decode(w, r, &in) {
		return
	}
	if err := s.d.Auth.VerifySetupToken(r.Context(), s.client(r), in.Token); err != nil {
		s.authFail(w, r, err)
		return
	}
	s.ok(w, 200, map[string]any{"valid": true})
}

func (s *Server) setupComplete(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SetupToken string `json:"setup_token"`
		Mode       string `json:"mode"`
		Admin      *struct {
			Username    string `json:"username"`
			DisplayName string `json:"display_name"`
			Password    string `json:"password"`
		} `json:"admin"`
		RPID               string   `json:"rp_id"`
		RPName             string   `json:"rp_name"`
		Origins            []string `json:"origins"`
		MFAPolicy          string   `json:"mfa_policy"`
		AllowPasswordLogin *bool    `json:"allow_password_login"`
		SessionIdleMinutes *int     `json:"session_idle_minutes"`
		SessionMaxHours    *int     `json:"session_max_hours"`
		PasswordMinLength  *int     `json:"password_min_length"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	req := auth.SetupRequest{Token: in.SetupToken, Mode: in.Mode, RPID: in.RPID, RPName: in.RPName, Origins: in.Origins, MFAPolicy: in.MFAPolicy,
		AllowPasswordLogin: in.AllowPasswordLogin, SessionIdleMinutes: in.SessionIdleMinutes, SessionMaxHours: in.SessionMaxHours, PasswordMinLength: in.PasswordMinLength}
	if in.Admin != nil {
		req.Admin = &auth.SetupAdmin{Username: in.Admin.Username, DisplayName: in.Admin.DisplayName, Password: in.Admin.Password}
	}
	o, err := s.d.Auth.CompleteSetup(r.Context(), s.client(r), req, s.hostInfo(r))
	if err != nil {
		s.authFail(w, r, err)
		return
	}
	s.writeOutcome(w, r, 201, o)
}

// APILevel is the contract generation this backend implements. The frontend carries the same number
// (REQUIRED_API_LEVEL in frontend/src/api/apiLevel.ts) and shows a banner when they differ. Bump it in BOTH places
// whenever the frontend starts to depend on a new endpoint or field.
const APILevel = 5

func (s *Server) setupFinish(w http.ResponseWriter, r *http.Request) {
	id, ok := s.identity(w, r)
	if !ok {
		return
	}
	if _, err := s.d.Auth.FinishWizard(r.Context(), id); err != nil {
		s.authFail(w, r, err)
		return
	}
	s.setupStatus(w, r)
}

// wizardAllowed lists the API routes that stay open while the setup wizard is unfinished (the wizard configures the
// global LLM and the administrator's second factor; everything else waits).
func wizardAllowed(path string) bool {
	switch {
	case strings.HasPrefix(path, "/v1/auth/"), strings.HasPrefix(path, "/v1/setup/"), strings.HasPrefix(path, "/v1/llm/"):
		return true
	}
	return path == "/v1/admin/ai-policy" || path == "/v1/client-errors"
}
