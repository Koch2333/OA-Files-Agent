package llm

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// "Sign in with <platform>": an OAuth 2.0 authorization-code + PKCE (S256) flow that makes the provider create an
// API key for this app, which is then stored exactly like a pasted key (AES-GCM, never returned, never logged).
// Only providers that publish such a flow for third-party apps are listed in oauthFlows; see docs/LLM账号登录配置.md
// for the research (OpenRouter is the only one among the shipped presets). No client secret is involved.

// OAuthFlow describes one provider's PKCE flow.
type OAuthFlow struct {
	ProviderID   string // preset id
	Name         string
	AuthorizeURL string // browser page (https)
	ExchangeURL  string // POST {code, code_verifier, code_challenge_method} -> {key}
	KeyLabel     string
	// APIBase overrides the preset Base URL after connecting (tests with a fake provider only).
	APIBase string
	// Hosts are the only hosts the desktop shell may open for this flow (https only).
	Hosts []string
}

var oauthFlows = map[string]OAuthFlow{
	"openrouter": {
		ProviderID: "openrouter", Name: "OpenRouter",
		AuthorizeURL: "https://openrouter.ai/auth",
		ExchangeURL:  "https://openrouter.ai/api/v1/auth/keys",
		KeyLabel:     "OA Agent",
		Hosts:        []string{"openrouter.ai"},
	},
}

// OAuthProviders lists preset ids with an implemented login flow.
func OAuthProviders() []string {
	out := make([]string, 0, len(oauthFlows))
	for id := range oauthFlows {
		out = append(out, id)
	}
	return out
}

const (
	oauthTTL         = 10 * time.Minute
	oauthMaxPerUser  = 5
	oauthMaxTotal    = 500
	oauthExchangeCap = 20 * time.Second
)

// Statuses reported by OAuthStatus.
const (
	OAuthPending    = "pending"    // waiting for the user to finish in the browser
	OAuthConnecting = "connecting" // code received; exchanging, saving and testing
	OAuthConnected  = "connected"
	OAuthFailed     = "failed"
	OAuthDenied     = "denied"
	OAuthExpired    = "expired"
)

// OAuthStart is the answer to a start request.
type OAuthStart struct {
	AuthorizeURL string `json:"authorize_url"`
	State        string `json:"state"`
	ExpiresAt    string `json:"expires_at"`
}

// OAuthStatusView is polled by the UI.
type OAuthStatusView struct {
	Status   string      `json:"status"`
	Provider string      `json:"provider"`
	Scope    string      `json:"scope"`
	Message  string      `json:"message,omitempty"`
	Test     *TestResult `json:"test,omitempty"`
	Config   *ConfigView `json:"config,omitempty"`
}

type oauthRec struct {
	provider string
	scope    string // personal | global
	userID   string
	username string
	verifier string
	created  time.Time
	redirect bool // callback answers with a redirect to Settings (web) instead of a "you may close this" page
	status   string
	code     string
	message  string
	test     *TestResult
	cfg      *ConfigView
}

type oauthState struct {
	mu    sync.Mutex
	recs  map[[32]byte]*oauthRec // keyed by SHA-256(state): a leaked map key is not a usable state
	flows map[string]OAuthFlow
	http  *http.Client
	now   func() time.Time
}

func newOAuthState() *oauthState {
	flows := make(map[string]OAuthFlow, len(oauthFlows))
	for k, v := range oauthFlows {
		flows[k] = v
	}
	return &oauthState{recs: map[[32]byte]*oauthRec{}, flows: flows, now: time.Now,
		http: &http.Client{Timeout: oauthExchangeCap, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// SetOAuthEndpoints points a flow at another origin-compatible endpoint pair (tests with a fake provider, or a
// private mirror). Both URLs must be https unless they target a loopback host.
func (m *Manager) SetOAuthEndpoints(providerID, authorizeURL, exchangeURL string) error {
	f, ok := m.oa().flows[providerID]
	if !ok {
		return bad("OAUTH_UNSUPPORTED", "该服务商不支持登录授权")
	}
	var hosts []string
	for _, raw := range []string{authorizeURL, exchangeURL} {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || (u.Scheme != "https" && !(u.Scheme == "http" && isLoopbackHost(u.Hostname()))) {
			return bad("OAUTH_BAD_ENDPOINT", "授权地址必须是 https（本机测试除外）")
		}
		hosts = append(hosts, strings.ToLower(u.Host))
	}
	f.AuthorizeURL, f.ExchangeURL, f.Hosts = authorizeURL, exchangeURL, hosts[:1]
	o := m.oa()
	o.mu.Lock()
	o.flows[providerID] = f
	o.mu.Unlock()
	return nil
}

func isLoopbackHost(h string) bool {
	h = strings.ToLower(h)
	return h == "localhost" || h == "127.0.0.1" || h == "::1"
}

func (m *Manager) oa() *oauthState {
	m.oaOnce.Do(func() { m.oauth = newOAuthState() })
	return m.oauth
}

func randToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func hashState(s string) [32]byte { return sha256.Sum256([]byte(s)) }

// gc drops expired records; caller holds o.mu.
func (o *oauthState) gc() {
	now := o.now()
	for k, r := range o.recs {
		if now.Sub(r.created) > oauthTTL+time.Minute {
			delete(o.recs, k)
		}
	}
}

func (o *oauthState) expired(r *oauthRec) bool { return o.now().Sub(r.created) > oauthTTL }

// OAuthStartFlow creates a pending record and the provider's authorize URL. callbackURL is the absolute URL the
// provider redirects to (computed by the HTTP layer from OA_PUBLIC_URL or the loopback address).
func (m *Manager) OAuthStartFlow(ctx context.Context, sub Subject, provider, scope, callbackURL string, redirect bool) (OAuthStart, error) {
	o := m.oa()
	o.mu.Lock()
	f, ok := o.flows[provider]
	o.mu.Unlock()
	if !ok {
		return OAuthStart{}, bad("OAUTH_UNSUPPORTED", "该服务商没有可用的登录授权方式，请手动填写 API Key")
	}
	if err := m.oauthGate(ctx, sub, scope); err != nil {
		return OAuthStart{}, err
	}
	state, err := randToken(32)
	if err != nil {
		return OAuthStart{}, err
	}
	verifier, err := randToken(48) // 64 chars, within RFC 7636's 43..128
	if err != nil {
		return OAuthStart{}, err
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	o.mu.Lock()
	defer o.mu.Unlock()
	o.gc()
	if len(o.recs) >= oauthMaxTotal {
		return OAuthStart{}, bad("OAUTH_BUSY", "当前登录授权请求过多，请稍后再试")
	}
	n := 0
	for k, r := range o.recs {
		if r.userID == sub.ID {
			if r.status != OAuthConnecting || o.expired(r) { // pending ones are superseded, finished ones are dropped
				delete(o.recs, k)
				continue
			}
			n++
		}
	}
	if n >= oauthMaxPerUser {
		return OAuthStart{}, bad("OAUTH_BUSY", "进行中的登录授权过多，请稍后再试")
	}
	o.recs[hashState(state)] = &oauthRec{provider: provider, scope: scope, userID: sub.ID, username: sub.Username,
		verifier: verifier, created: o.now(), redirect: redirect, status: OAuthPending}

	au, err := url.Parse(f.AuthorizeURL)
	if err != nil {
		return OAuthStart{}, err
	}
	q := au.Query()
	q.Set("callback_url", callbackURL)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	if f.KeyLabel != "" {
		q.Set("key_label", f.KeyLabel)
	}
	au.RawQuery = q.Encode()
	return OAuthStart{AuthorizeURL: au.String(), State: state, ExpiresAt: o.now().Add(oauthTTL).UTC().Format(time.RFC3339)}, nil
}

// oauthGate checks that sub may configure the given scope.
func (m *Manager) oauthGate(ctx context.Context, sub Subject, scope string) error {
	switch scope {
	case "global":
		if sub.Role != "admin" {
			return &PolicyError{"FORBIDDEN", "只有管理员可以配置全局识别引擎"}
		}
		return nil
	case "personal":
		_, err := m.personalGate(ctx, sub)
		return err
	}
	return bad("INVALID_SCOPE", "scope 须为 personal 或 global")
}

// OAuthCallbackResult tells the HTTP layer how to answer the browser.
type OAuthCallbackResult struct {
	Redirect bool   // true: 302 back to Settings; false: static "you may close this window" page
	OK       bool   // the code was accepted (the exchange happens when the signed-in user polls the status)
	Reason   string // denied | expired | invalid | used (only when !OK)
}

// OAuthCallback records the authorization code for state. It needs no session (the browser comes back from another
// site and the session cookie is SameSite=Strict): the code is useless without the server-side PKCE verifier, and
// the exchange only runs when the user the flow was started for polls OAuthStatus with their own session.
func (m *Manager) OAuthCallback(state, code, errParam string) OAuthCallbackResult {
	o := m.oa()
	o.mu.Lock()
	defer o.mu.Unlock()
	o.gc()
	if state == "" {
		// Providers do not echo state on denial: nothing to bind to; the pending flow simply expires.
		reason := "invalid"
		if errParam != "" || code == "" {
			reason = "denied"
		}
		return OAuthCallbackResult{Redirect: true, Reason: reason}
	}
	r := o.recs[hashState(state)]
	if r == nil {
		return OAuthCallbackResult{Redirect: true, Reason: "invalid"}
	}
	res := OAuthCallbackResult{Redirect: r.redirect}
	switch {
	case o.expired(r):
		r.status, r.message = OAuthExpired, "登录授权已超时，请重新发起"
		res.Reason = "expired"
	case r.status != OAuthPending:
		res.Reason = "used" // replay: the state is single use
	case errParam != "":
		r.status, r.message = OAuthDenied, "你在服务商页面拒绝了授权"
		res.Reason = "denied"
	case code == "" || len(code) > 2048:
		r.status, r.message = OAuthFailed, "服务商没有返回授权码，请重试"
		res.Reason = "invalid"
	default:
		r.code, r.status = code, OAuthConnecting
		res.OK = true
	}
	return res
}

// OAuthStatusFor returns the flow's state for the user who started it (any other user gets "unknown").
func (m *Manager) OAuthStatusFor(sub Subject, state string) (OAuthStatusView, error) {
	o := m.oa()
	o.mu.Lock()
	defer o.mu.Unlock()
	r := o.recs[hashState(state)]
	if r == nil || r.userID != sub.ID {
		return OAuthStatusView{}, bad("OAUTH_UNKNOWN_STATE", "登录授权请求不存在或已过期，请重新发起")
	}
	if (r.status == OAuthPending || r.status == OAuthConnecting) && o.expired(r) {
		r.status, r.message = OAuthExpired, "登录授权已超时，请重新发起"
	}
	v := OAuthStatusView{Status: r.status, Provider: r.provider, Scope: r.scope, Message: r.message, Test: r.test, Config: r.cfg}
	if r.status == OAuthConnecting && r.code != "" {
		code, verifier := r.code, r.verifier
		r.code = "" // single use: the exchange is started exactly once
		flow := o.flows[r.provider]
		go m.oauthComplete(hashState(state), sub, flow, code, verifier)
	}
	return v, nil
}

func (m *Manager) oauthSet(key [32]byte, fn func(r *oauthRec)) {
	o := m.oa()
	o.mu.Lock()
	defer o.mu.Unlock()
	if r := o.recs[key]; r != nil {
		fn(r)
	}
}

func (m *Manager) oauthComplete(key [32]byte, sub Subject, flow OAuthFlow, code, verifier string) {
	defer func() {
		if v := recover(); v != nil {
			log.Printf("llm oauth: panic recovered in completion")
			m.oauthSet(key, func(r *oauthRec) { r.status, r.message = OAuthFailed, "内部错误，请重试" })
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var rec oauthRec
	m.oauthSet(key, func(r *oauthRec) { rec = *r })
	apiKey, err := m.oauthExchange(ctx, flow, code, verifier)
	if err != nil {
		msg := err.Error()
		m.oauthSet(key, func(r *oauthRec) { r.status, r.message = OAuthFailed, msg })
		return
	}
	cfg, serr := m.oauthStoreKey(ctx, sub, rec.scope, flow, apiKey)
	if serr != nil {
		log.Printf("llm oauth: saving the key failed (%s)", rec.provider)
		m.oauthSet(key, func(r *oauthRec) { r.status, r.message = OAuthFailed, "保存密钥失败，请重试" })
		return
	}
	// Run the usual connection test with the freshly stored key; a failing test does not undo the connection.
	var tr TestResult
	client := m.http
	if rec.scope == "personal" {
		client = m.personalHTTP()
	}
	if t, terr := m.runTest(ctx, client, cfg.BaseURL, apiKey, TestInput{BaseURL: cfg.BaseURL, Model: cfg.Model}); terr == nil {
		tr = t
	} else {
		tr = TestResult{Message: "连接测试未能运行"}
	}
	if m.Audit != nil {
		m.Audit(ctx, rec.username, "llm.oauth.connect", "llm:"+rec.username, "登录授权已连接："+rec.provider+" / "+rec.scope)
	}
	m.oauthSet(key, func(r *oauthRec) {
		r.status, r.cfg, r.test = OAuthConnected, &cfg, &tr
		if !tr.OK {
			r.message = "已获得密钥并保存，但连接测试未通过：" + tr.Message
		}
	})
}

// oauthExchange trades the code for the API key. Messages are user-facing and never contain the code, the
// verifier or the key.
func (m *Manager) oauthExchange(ctx context.Context, f OAuthFlow, code, verifier string) (string, error) {
	body, _ := json.Marshal(map[string]string{"code": code, "code_verifier": verifier, "code_challenge_method": "S256"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.ExchangeURL, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("授权地址无效")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := m.oa().http.Do(req)
	if err != nil {
		if ctx.Err() != nil || strings.Contains(strings.ToLower(err.Error()), "timeout") {
			return "", fmt.Errorf("连接 %s 超时，请稍后重试", f.Name)
		}
		return "", fmt.Errorf("无法连接 %s，请检查网络后重试", f.Name)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	switch {
	case res.StatusCode == 200:
	case res.StatusCode == 400:
		return "", fmt.Errorf("%s 拒绝了授权请求（参数不匹配），请重新发起", f.Name)
	case res.StatusCode == 401 || res.StatusCode == 403:
		return "", fmt.Errorf("授权码无效或已过期，请重新登录授权")
	case res.StatusCode == 429:
		return "", fmt.Errorf("%s 请求过于频繁，请稍后重试", f.Name)
	case res.StatusCode >= 500:
		return "", fmt.Errorf("%s 暂时不可用（HTTP %d），请稍后重试", f.Name, res.StatusCode)
	default:
		return "", fmt.Errorf("%s 返回了意外的状态（HTTP %d）", f.Name, res.StatusCode)
	}
	var out struct {
		Key string `json:"key"`
	}
	if json.Unmarshal(raw, &out) != nil || validKey(strings.TrimSpace(out.Key)) != nil {
		return "", fmt.Errorf("%s 没有返回有效的密钥", f.Name)
	}
	return strings.TrimSpace(out.Key), nil
}

// Key provenance travels inside the encrypted blob (no schema change): "\x00oa:oauth:<provider>\x00<key>". A pasted
// key can never start with a control character (validKey), so the marker is unambiguous.
const keyTagPrefix = "\x00oa:oauth:"

func tagKey(provider, key string) string { return keyTagPrefix + provider + "\x00" + key }

// splitKey returns the plain key and where it came from ("manual" or "oauth", with the provider id).
func splitKey(plain string) (key, source, provider string) {
	if strings.HasPrefix(plain, keyTagPrefix) {
		rest := plain[len(keyTagPrefix):]
		if i := strings.IndexByte(rest, 0); i >= 0 {
			return rest[i+1:], "oauth", rest[:i]
		}
	}
	return plain, "manual", ""
}

// oauthStoreKey saves the key and the provider's preset as the configuration of scope.
func (m *Manager) oauthStoreKey(ctx context.Context, sub Subject, scope string, f OAuthFlow, apiKey string) (ConfigView, error) {
	preset, ok := presetByID(f.ProviderID)
	if !ok {
		return ConfigView{}, bad("INVALID_PRESET", "未知的预设")
	}
	enc, err := m.box.Seal([]byte(tagKey(f.ProviderID, apiKey)))
	if err != nil {
		return ConfigView{}, err
	}
	next := func(cur Settings) Settings {
		base := preset.BaseURL
		if f.APIBase != "" {
			base = f.APIBase
		}
		s := Settings{Mode: ModeLLM, PresetID: preset.ID, BaseURL: base, Model: preset.DefaultModel, APIKeyEnc: enc}
		if cur.PresetID == preset.ID && cur.BaseURL != "" && validModel(cur.Model) == nil { // reconnecting keeps the chosen model
			s.BaseURL, s.Model, s.VisionModel, s.AllowVision = cur.BaseURL, cur.Model, cur.VisionModel, cur.AllowVision
		}
		return s
	}
	if scope == "global" {
		m.mu.Lock()
		defer m.mu.Unlock()
		s := next(m.set)
		if err := m.store.SaveLLM(ctx, s); err != nil {
			return ConfigView{}, err
		}
		s.UpdatedAt = time.Now().UTC()
		m.apply(s)
		return m.viewLocked(), nil
	}
	if _, err := m.personalGate(ctx, sub); err != nil {
		return ConfigView{}, err
	}
	m.pmu.Lock()
	defer m.pmu.Unlock()
	cur, err := m.loadPersonal(ctx, sub.ID)
	if err != nil {
		return ConfigView{}, err
	}
	if _, err := m.ai.SavePersonalLLM(ctx, sub.ID, next(cur.set)); err != nil {
		return ConfigView{}, err
	}
	p, err := m.loadPersonal(ctx, sub.ID)
	if err != nil {
		return ConfigView{}, err
	}
	return personalView(p), nil
}

// OAuthDisconnect deletes a key that was obtained through login (a manually pasted key is cleared with
// clear_api_key instead). The key stays valid on the provider's side until the user revokes it there.
func (m *Manager) OAuthDisconnect(ctx context.Context, sub Subject, scope string) (ConfigView, string, error) {
	clear := func(cur Settings, source, provider string) (Settings, error) {
		if len(cur.APIKeyEnc) == 0 || source != "oauth" {
			return Settings{}, &ValidationError{"NOT_OAUTH_KEY", "当前密钥不是通过登录授权获得的，请在表单中清除 API Key"}
		}
		cur.APIKeyEnc = nil
		return cur, nil
	}
	switch scope {
	case "global":
		if sub.Role != "admin" {
			return ConfigView{}, "", &PolicyError{"FORBIDDEN", "只有管理员可以配置全局识别引擎"}
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		provider := m.keyProv
		s, err := clear(m.set, m.keySrc, provider)
		if err != nil {
			return ConfigView{}, "", err
		}
		if err := m.store.SaveLLM(ctx, s); err != nil {
			return ConfigView{}, "", err
		}
		s.UpdatedAt = time.Now().UTC()
		m.apply(s)
		return m.viewLocked(), provider, nil
	case "personal":
		if m.ai == nil || sub.ID == "" {
			return ConfigView{}, "", &PolicyError{"PERSONAL_LLM_DISABLED", "管理员未开放个人大模型配置"}
		}
		m.pmu.Lock()
		defer m.pmu.Unlock()
		cur, err := m.loadPersonal(ctx, sub.ID)
		if err != nil {
			return ConfigView{}, "", err
		}
		s, err := clear(cur.set, cur.src, cur.prov)
		if err != nil {
			return ConfigView{}, "", err
		}
		if _, err := m.ai.SavePersonalLLM(ctx, sub.ID, s); err != nil {
			return ConfigView{}, "", err
		}
		p, err := m.loadPersonal(ctx, sub.ID)
		if err != nil {
			return ConfigView{}, "", err
		}
		return personalView(p), cur.prov, nil
	}
	return ConfigView{}, "", bad("INVALID_SCOPE", "scope 须为 personal 或 global")
}
