package api

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"strings"

	"oa-agent/backend/internal/llm"
)

// OAuthLLM is the "sign in with the platform" part of llm.Manager (PKCE flow that provisions an API key).
type OAuthLLM interface {
	OAuthStartFlow(ctx context.Context, sub llm.Subject, provider, scope, callbackURL string, redirect bool) (llm.OAuthStart, error)
	OAuthCallback(state, code, errParam string) llm.OAuthCallbackResult
	OAuthStatusFor(sub llm.Subject, state string) (llm.OAuthStatusView, error)
	OAuthDisconnect(ctx context.Context, sub llm.Subject, scope string) (llm.ConfigView, string, error)
}

func (s *Server) llmOAuthRoutes() {
	s.handle("/v1/llm/oauth/start", map[string]http.HandlerFunc{"POST": s.oauthStart})
	s.handle("/v1/llm/oauth/status", map[string]http.HandlerFunc{"GET": s.oauthStatus})
	s.handle("/v1/llm/oauth/callback", map[string]http.HandlerFunc{"GET": s.oauthCallback})
	s.handle("/v1/llm/oauth/disconnect", map[string]http.HandlerFunc{"POST": s.oauthDisconnect})
}

// oauthCallbackURL is where the provider sends the browser back: the loopback address in local (desktop) mode,
// otherwise OA_PUBLIC_URL (the Host header of a remote request is never trusted for this).
func (s *Server) oauthCallbackURL(r *http.Request) (string, bool) {
	const path = "/v1/llm/oauth/callback"
	local := s.d.Auth == nil
	if s.d.Auth != nil {
		if set, err := s.d.Auth.Settings(r.Context()); err == nil && set.Mode == "local" {
			local = true
		}
	}
	if local {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		host = strings.Trim(host, "[]")
		if ip := net.ParseIP(host); host == "localhost" || (ip != nil && ip.IsLoopback()) {
			return "http://" + r.Host + path, true
		}
	}
	if u, err := url.Parse(s.d.PublicURL); err == nil && s.d.PublicURL != "" && u.Host != "" && (u.Scheme == "https" || u.Scheme == "http") {
		return strings.TrimRight(s.d.PublicURL, "/") + path, true
	}
	return "", false
}

func (s *Server) oauthStart(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Provider string `json:"provider"`
		Scope    string `json:"scope"`
		Client   string `json:"client"` // web (default): the callback redirects back to Settings; desktop: a "close this window" page
	}
	if !s.decode(w, r, &in) {
		return
	}
	if !s.personalRate(w, r) {
		return
	}
	cb, ok := s.oauthCallbackURL(r)
	if !ok {
		s.fail(w, r, 422, "OAUTH_PUBLIC_URL_REQUIRED", "服务器未设置对外访问地址（OA_PUBLIC_URL），无法接收登录授权回调；请管理员设置后重试，或手动填写 API Key", nil)
		return
	}
	sub := subjectOf(r)
	st, err := s.d.LLM.OAuthStartFlow(r.Context(), sub, in.Provider, in.Scope, cb, in.Client != "desktop")
	if err != nil {
		s.aiErr(w, r, err)
		return
	}
	// No secrets here: the state is not logged either.
	s.d.Store.Audit(r.Context(), actorOf(r), "llm.oauth.start", "llm:"+sub.Username, "发起登录授权："+in.Provider+" / "+in.Scope)
	s.ok(w, 200, st)
}

func (s *Server) oauthStatus(w http.ResponseWriter, r *http.Request) {
	v, err := s.d.LLM.OAuthStatusFor(subjectOf(r), r.URL.Query().Get("state"))
	if err != nil {
		s.aiErr(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.ok(w, 200, v)
}

const oauthDonePage = `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>OA Agent</title>
<style>body{font-family:system-ui,"Microsoft YaHei",sans-serif;display:grid;place-items:center;min-height:100vh;margin:0;background:#f7f9fc;color:#1b1f24}main{max-width:26rem;padding:2rem;text-align:center}h1{font-size:1.25rem}p{color:#4a5360;line-height:1.6}</style></head>
<body><main><h1>%TITLE%</h1><p>%BODY%</p></main></body></html>`

// oauthCallback is public (the session cookie is SameSite=Strict, so it is absent on the redirect back from the
// provider): it only records the code for an unguessable single-use state. Exchange, storage and the connection
// test happen when the user who started the flow polls /status with their own session.
func (s *Server) oauthCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	q := r.URL.Query()
	res := s.d.LLM.OAuthCallback(q.Get("state"), q.Get("code"), q.Get("error"))
	flag := "ok"
	if !res.OK {
		flag = res.Reason
		if flag == "" {
			flag = "error"
		}
	}
	if res.Redirect {
		http.Redirect(w, r, "/settings?llm_oauth="+flag, http.StatusFound)
		return
	}
	title, body := "授权已完成", "请回到 OA Agent 窗口，连接会自动完成。可以关闭此页面。"
	if !res.OK {
		title, body = "授权未完成", "授权被拒绝、已过期或已被使用。请回到 OA Agent 窗口重新发起。"
	}
	page := strings.NewReplacer("%TITLE%", title, "%BODY%", body).Replace(oauthDonePage)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	_, _ = w.Write([]byte(page))
}

func (s *Server) oauthDisconnect(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Scope string `json:"scope"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	sub := subjectOf(r)
	v, provider, err := s.d.LLM.OAuthDisconnect(r.Context(), sub, in.Scope)
	if err != nil {
		s.aiErr(w, r, err)
		return
	}
	s.d.Store.Audit(r.Context(), actorOf(r), "llm.oauth.disconnect", "llm:"+sub.Username, "断开登录授权："+provider+" / "+in.Scope)
	s.ok(w, 200, v)
}
