package api

import (
	"context"
	"net"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"oa-agent/backend/internal/auth"
)

// access is the minimum role a route needs. accessPublic routes need no login; everything not listed in
// routeAccess is denied (admin only), and TestEveryRouteHasAnAccessRule fails until it is listed.
type access int

const (
	accessPublic access = iota
	accessViewer
	accessOperator
	accessAdmin
)

var roleRank = map[string]int{"viewer": 1, "operator": 2, "admin": 3}

func (a access) rank() int { return int(a) } // viewer=1, operator=2, admin=3 line up with roleRank

const (
	pub = accessPublic
	viw = accessViewer
	opr = accessOperator
	adm = accessAdmin
)

// routeAccess is THE route → minimum-role table (key: "METHOD /net/http/pattern"). Default deny.
var routeAccess = normalizeAccess(map[string]access{
	// system and static
	"GET /healthz": pub, "GET /readyz": pub, "GET /docs": pub, "GET /openapi.yaml": pub,
	// setup wizard
	"GET /v1/setup/status": pub, "POST /v1/setup/verify-token": pub, "POST /v1/setup/complete": pub, "POST /v1/setup/finish": adm,
	// login (public)
	"POST /v1/auth/login": pub, "POST /v1/auth/mfa/totp": pub, "POST /v1/auth/mfa/recovery": pub,
	"POST /v1/auth/mfa/passkey/begin": pub, "POST /v1/auth/mfa/passkey/finish": pub,
	"POST /v1/auth/passkey/login/begin": pub, "POST /v1/auth/passkey/login/finish": pub,
	"GET /v1/auth/invitations/{token}": pub, "POST /v1/auth/invitations/{token}/passkey/begin": pub, "POST /v1/auth/invitations/{token}/passkey/finish": pub,
	// own account (any signed-in user)
	"POST /v1/auth/logout": viw, "GET /v1/auth/me": viw, "POST /v1/auth/reauth": viw,
	"POST /v1/auth/reauth/passkey/begin": viw, "POST /v1/auth/reauth/passkey/finish": viw,
	"POST /v1/auth/password": viw, "GET /v1/auth/security": viw,
	"POST /v1/auth/totp/begin": viw, "POST /v1/auth/totp/confirm": viw, "DELETE /v1/auth/totp": viw,
	"POST /v1/auth/passkeys/begin": viw, "POST /v1/auth/passkeys/finish": viw,
	"PATCH /v1/auth/passkeys/{id}": viw, "DELETE /v1/auth/passkeys/{id}": viw,
	"POST /v1/auth/recovery-codes": viw, "GET /v1/auth/sessions": viw, "DELETE /v1/auth/sessions/{id}": viw,
	// administration of users, sessions and security settings
	"GET /v1/admin/users": adm, "POST /v1/admin/users": adm,
	"GET /v1/admin/users/{id}": adm, "PATCH /v1/admin/users/{id}": adm, "DELETE /v1/admin/users/{id}": adm,
	"POST /v1/admin/users/{id}/reset-password": adm, "POST /v1/admin/users/{id}/reset-mfa": adm, "POST /v1/admin/users/{id}/unlock": adm,
	"POST /v1/admin/users/{id}/revoke-sessions": adm, "POST /v1/admin/users/{id}/invite": adm,
	"GET /v1/admin/sessions": adm, "DELETE /v1/admin/sessions/{id}": adm,
	"GET /v1/admin/security-settings": adm, "PATCH /v1/admin/security-settings": adm,
	// diagnostics: system status, error reports and the diagnostic bundle are admin only; any signed-in user may report a client error
	"GET /v1/admin/system": adm, "GET /v1/admin/error-reports": adm, "GET /v1/admin/error-reports/{id}": adm,
	"PATCH /v1/admin/error-reports/{id}": adm, "DELETE /v1/admin/error-reports/{id}": adm, "GET /v1/admin/diagnostics/bundle": adm,
	"POST /v1/client-errors": viw,
	// database administration (admin only; changing settings and initialising also need a fresh sign-in)
	"GET /v1/admin/database": adm, "PUT /v1/admin/database": adm, "DELETE /v1/admin/database/pending": adm,
	"POST /v1/admin/database/test": adm, "POST /v1/admin/database/validate-permissions": adm, "POST /v1/admin/database/initialize": adm,
	// archive workflow: reading = viewer, changing = operator
	"POST /v1/imports": opr, "GET /v1/tasks": viw, "GET /v1/tasks/{id}": viw, "GET /v1/tasks/{id}/events": viw,
	"POST /v1/tasks/{id}/cancel": opr, "POST /v1/tasks/{id}/retry": opr,
	"GET /v1/files": viw, "GET /v1/files/{id}": viw, "POST /v1/files/{id}/correction-proposals": opr,
	"POST /v1/correction-proposals/{id}/approve": opr,
	"GET /v1/archive-plans/{id}":                 viw, "PATCH /v1/archive-plans/{id}": opr,
	"POST /v1/archive-plans/{id}/approve": opr, "POST /v1/archive-plans/{id}/execute": opr,
	"POST /v1/archive-operations/{id}/rollback": opr,
	"GET /v1/ledger": viw, "GET /v1/ledger/export": viw,
	"GET /v1/oa-templates": viw, "POST /v1/oa-templates": opr,
	"GET /v1/oa-templates/{id}": viw, "PATCH /v1/oa-templates/{id}": opr, "DELETE /v1/oa-templates/{id}": opr, "POST /v1/oa-templates/{id}/test": opr,
	"GET /v1/archive-rules": viw, "POST /v1/archive-rules": opr,
	"GET /v1/audit-logs": adm, "GET /v1/providers": viw,
	// file manager and preview
	"GET /v1/fs": viw, "GET /v1/fs/content": viw, "POST /v1/fs/folders": opr, "POST /v1/fs/move": opr,
	"GET /v1/fs/trash": viw, "POST /v1/fs/trash": opr, "POST /v1/fs/trash/{id}/restore": opr, "POST /v1/fs/reveal": adm,
	"GET /v1/files/{id}/preview": viw, "GET /v1/files/{id}/preview/page": viw, "GET /v1/fs/preview": viw, "GET /v1/fs/preview/page": viw,
	// recognition engine: reading presets/config = viewer, everything else = admin
	"GET /v1/llm/presets": viw, "GET /v1/llm/config": adm, "PUT /v1/llm/config": adm, "POST /v1/llm/test": adm, "POST /v1/llm/models": adm,
	// AI workbench: status = any signed-in role; personal configuration = operator and up; policy = admin
	"GET /v1/llm/status":   viw,
	"GET /v1/llm/personal": opr, "PUT /v1/llm/personal": opr, "DELETE /v1/llm/personal": opr,
	"POST /v1/llm/personal/test": opr, "POST /v1/llm/personal/models": opr,
	// "sign in with the platform": start/status/disconnect = operator and up (scope=global additionally needs admin, checked in the handler); the callback is public (SameSite=Strict cookie is absent on the redirect back) and only records a code for a single-use state
	"POST /v1/llm/oauth/start": opr, "GET /v1/llm/oauth/status": opr, "POST /v1/llm/oauth/disconnect": opr, "GET /v1/llm/oauth/callback": pub,
	"GET /v1/admin/ai-policy": adm, "PATCH /v1/admin/ai-policy": adm,
	// storage connections: admin; listing and browsing are open to operators because remote import needs them
	"GET /v1/storage/kinds": adm, "GET /v1/storage/connections": opr, "POST /v1/storage/connections": adm, "POST /v1/storage/connections/test": adm,
	"GET /v1/storage/connections/{id}": adm, "PATCH /v1/storage/connections/{id}": adm, "DELETE /v1/storage/connections/{id}": adm,
	"POST /v1/storage/connections/{id}/test": adm, "GET /v1/storage/connections/{id}/browse": opr, "POST /v1/storage/connections/{id}/replicate": adm,
	"GET /v1/storage/replications": adm, "POST /v1/storage/replications/{id}/retry": adm,
	"POST /v1/imports/remote": opr,
	// unbounded imports: resumable chunked uploads, batch commit, local-path import (the latter additionally requires local mode + loopback, checked in the handler)
	"GET /v1/imports/config": opr, "POST /v1/imports/uploads": opr,
	"GET /v1/imports/uploads/{id}": opr, "PUT /v1/imports/uploads/{id}": opr, "DELETE /v1/imports/uploads/{id}": opr,
	"POST /v1/imports/uploads/{id}/complete": opr,
	"GET /v1/imports/batches/{id}": opr, "DELETE /v1/imports/batches/{id}": opr, "POST /v1/imports/batches/{id}/commit": opr,
	"POST /v1/imports/from-paths": opr,
})

func normalizeAccess(in map[string]access) map[string]access {
	out := make(map[string]access, len(in))
	for k, v := range in {
		i := strings.IndexByte(k, ' ')
		out[k[:i]+" "+ginPath(k[i+1:])] = v
	}
	return out
}

// enrollAllowed are the only routes a restricted (scope=enroll_mfa) session may call; passwordChangeAllowed the ones a
// session with a temporary password may call.
var enrollAllowed = normalizeKeys("GET /v1/auth/me", "POST /v1/auth/logout", "GET /v1/auth/security", "POST /v1/auth/password",
	"POST /v1/auth/totp/begin", "POST /v1/auth/totp/confirm", "POST /v1/auth/passkeys/begin", "POST /v1/auth/passkeys/finish")

var passwordChangeAllowed = normalizeKeys("GET /v1/auth/me", "POST /v1/auth/logout", "POST /v1/auth/password", "GET /v1/auth/security")

func normalizeKeys(keys ...string) map[string]bool {
	out := map[string]bool{}
	for _, k := range keys {
		i := strings.IndexByte(k, ' ')
		out[k[:i]+" "+ginPath(k[i+1:])] = true
	}
	return out
}

type identKey struct{}

func withIdentity(r *http.Request, id auth.Identity) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), identKey{}, id))
}

func identityOf(r *http.Request) (auth.Identity, bool) {
	id, ok := r.Context().Value(identKey{}).(auth.Identity)
	return id, ok
}

// actorOf names the acting user for audit logs and ownership: the signed-in username, "local" in local mode, and
// "local" when authentication is not wired at all (unit tests of other packages).
func actorOf(r *http.Request) string {
	if id, ok := identityOf(r); ok {
		return id.User.Username
	}
	return auth.LocalUserName
}

const sessionCookie = "oa_session"

// authMiddleware is the Gin-level chain: setup gate → authn (cookie session) → scope/password gates → CSRF → authz.
func (s *Server) authMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		a := s.d.Auth
		if a == nil {
			c.Next()
			return
		}
		r, w := c.Request, c.Writer
		path := r.URL.Path
		isAPI := strings.HasPrefix(path, "/v1/")
		pattern := c.FullPath()
		method := r.Method
		if method == http.MethodHead {
			method = http.MethodGet
		}
		need := adm // unknown routes: default deny
		if pattern != "" {
			if acc, ok := routeAccess[method+" "+pattern]; ok {
				need = acc
			} else if _, registered := s.routes[pattern][method]; !registered {
				c.Next() // a method the route does not serve: the handler answers 405 without side effects
				return
			}
		} else if !isAPI {
			c.Next() // static frontend / unknown non-API path
			return
		}
		if isAPI && (strings.HasPrefix(path, "/v1/auth/") || strings.HasPrefix(path, "/v1/admin/") || strings.HasPrefix(path, "/v1/setup/")) {
			w.Header().Set("Cache-Control", "no-store")
		}
		set, err := a.Settings(r.Context())
		if err != nil {
			s.fail(w, r, 500, "INTERNAL", "内部错误", nil)
			c.Abort()
			return
		}
		if !set.SetupCompleted && isAPI && !strings.HasPrefix(path, "/v1/setup/") {
			s.fail(w, r, 503, "SETUP_REQUIRED", "系统尚未初始化，请先完成初始化向导", nil)
			c.Abort()
			return
		}
		if need == pub {
			c.Next()
			return
		}
		if set.Mode == "local" {
			if !s.fromLoopback(r) {
				s.fail(w, r, 403, "FORBIDDEN", "local 模式仅允许本机访问", nil)
				c.Abort()
				return
			}
			c.Request = withIdentity(r, auth.LocalIdentity(set, a.Now()))
			c.Next()
			return
		}
		ck, err := r.Cookie(sessionCookie)
		var token string
		if err == nil {
			token = ck.Value
		}
		id, aerr := a.Authenticate(r.Context(), token, set)
		if aerr != nil {
			if token != "" {
				s.clearSessionCookie(w, r)
			}
			s.authFail(w, r, aerr)
			c.Abort()
			return
		}
		key := method + " " + pattern
		if id.Session.Scope == "enroll_mfa" && !enrollAllowed[key] {
			s.fail(w, r, 403, "FORBIDDEN", "需要先绑定第二因素（TOTP 或 Passkey）", map[string]any{"reason": "enroll_mfa"})
			c.Abort()
			return
		}
		if id.User.MustChangePassword && !passwordChangeAllowed[key] {
			s.fail(w, r, 403, "FORBIDDEN", "需要先修改临时密码", map[string]any{"reason": "must_change_password"})
			c.Abort()
			return
		}
		if isAPI && set.SetupCompleted && !set.WizardFinished && !wizardAllowed(path) {
			s.fail(w, r, 409, "WIZARD_REQUIRED", "请先完成初始化向导", nil)
			c.Abort()
			return
		}
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			if !auth.CheckCSRF(id.Token, r.Header.Get("X-CSRF-Token")) {
				s.fail(w, r, 403, "CSRF_INVALID", "CSRF 校验失败，请刷新页面后重试", nil)
				c.Abort()
				return
			}
		}
		if roleRank[id.User.Role] < need.rank() {
			s.fail(w, r, 403, "FORBIDDEN", "当前角色没有权限执行此操作", nil)
			c.Abort()
			return
		}
		c.Request = withIdentity(r, id)
		c.Next()
	}
}

// clientIP is the connecting address, or the first X-Forwarded-For hop when a trusted proxy is configured.
func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if s.d.TrustedProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			first := strings.TrimSpace(strings.Split(xff, ",")[0])
			if net.ParseIP(first) != nil {
				return first
			}
		}
	}
	return host
}

// fromLoopback: the request really comes from this machine. A proxy hop (forwarding headers) disqualifies it unless a
// trusted proxy is configured, in which case the forwarded client address decides.
func (s *Server) fromLoopback(r *http.Request) bool {
	if !isLoopbackRequest(r) {
		return false
	}
	if s.d.TrustedProxy {
		ip := net.ParseIP(s.clientIP(r))
		return ip != nil && ip.IsLoopback()
	}
	for _, h := range []string{"X-Forwarded-For", "Forwarded", "X-Real-IP", "X-Forwarded-Host"} {
		if r.Header.Get(h) != "" {
			return false
		}
	}
	return true
}

// isSecure: TLS, or an HTTPS-terminating trusted proxy.
func (s *Server) isSecure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return s.d.TrustedProxy && strings.EqualFold(strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]), "https")
}

// fresh enforces the "recent authentication" window: 403 FORBIDDEN with details.reason=reauth_required.
func (s *Server) fresh(w http.ResponseWriter, r *http.Request) bool {
	id, ok := identityOf(r)
	if ok && id.Fresh(s.d.Auth.Now()) {
		return true
	}
	s.fail(w, r, 403, "FORBIDDEN", "该操作需要近期认证，请先重新输入密码或使用 Passkey 验证", map[string]any{"reason": "reauth_required"})
	return false
}
