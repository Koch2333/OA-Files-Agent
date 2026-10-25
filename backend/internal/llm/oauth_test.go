package llm

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

const fakeOAuthKey = "sk-or-v1-FAKEKEY0123456789abcdef"

// fakeProvider implements the OpenRouter-style exchange endpoint plus an OpenAI-compatible API.
type fakeProvider struct {
	*fakeLLM
	ex      *httptest.Server
	mu      sync.Mutex
	codes   map[string]string // code -> challenge
	status  int               // forced exchange status (0 = normal)
	exCalls int
}

func newFakeProvider(t *testing.T) *fakeProvider {
	fp := &fakeProvider{fakeLLM: newFake(t, fakeOAuthKey), codes: map[string]string{}}
	fp.ex = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fp.mu.Lock()
		defer fp.mu.Unlock()
		fp.exCalls++
		if fp.status != 0 {
			http.Error(w, "boom", fp.status)
			return
		}
		var b struct{ Code, CodeVerifier, CodeChallengeMethod string }
		var raw map[string]string
		json.NewDecoder(r.Body).Decode(&raw)
		b.Code, b.CodeVerifier, b.CodeChallengeMethod = raw["code"], raw["code_verifier"], raw["code_challenge_method"]
		ch, ok := fp.codes[b.Code]
		sum := sha256.Sum256([]byte(b.CodeVerifier))
		if !ok || b.CodeChallengeMethod != "S256" || base64.RawURLEncoding.EncodeToString(sum[:]) != ch {
			http.Error(w, `{"error":"invalid"}`, 403)
			return
		}
		delete(fp.codes, b.Code) // single use
		w.Write([]byte(`{"key":"` + fakeOAuthKey + `"}`))
	}))
	t.Cleanup(fp.ex.Close)
	return fp
}

func oauthMgr(t *testing.T) (*Manager, *aiMemStore, *fakeProvider, *[]string) {
	m, st := newAIMgr(t)
	setPolicy(t, m, true, true, true)
	fp := newFakeProvider(t)
	if err := m.SetOAuthEndpoints("openrouter", fp.ex.URL+"/auth", fp.ex.URL+"/keys"); err != nil {
		t.Fatal(err)
	}
	o := m.oa()
	f := o.flows["openrouter"]
	f.APIBase = fp.srv.URL + "/v1"
	o.flows["openrouter"] = f
	var audit []string
	var amu sync.Mutex
	m.Audit = func(_ context.Context, actor, action, target, summary string) {
		amu.Lock()
		audit = append(audit, actor+"|"+action+"|"+target+"|"+summary)
		amu.Unlock()
	}
	return m, st, fp, &audit
}

// browser plays the user's browser: it reads the authorize URL, "approves" and returns the callback params.
func (fp *fakeProvider) approve(t *testing.T, authURL string) (state, code string) {
	u, _ := url.Parse(authURL)
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("callback_url") == "" || q.Get("state") == "" {
		t.Fatalf("authorize URL lacks PKCE params: %s", authURL)
	}
	code = "code-" + q.Get("state")[:8]
	fp.mu.Lock()
	fp.codes[code] = q.Get("code_challenge")
	fp.mu.Unlock()
	return q.Get("state"), code
}

func waitStatus(t *testing.T, m *Manager, sub Subject, state string) OAuthStatusView {
	t.Helper()
	for i := 0; i < 200; i++ {
		v, err := m.OAuthStatusFor(sub, state)
		if err != nil {
			t.Fatal(err)
		}
		if v.Status != OAuthPending && v.Status != OAuthConnecting {
			return v
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("flow did not finish")
	return OAuthStatusView{}
}

func TestOAuthRoundTripGlobalStoresKeyLikeManual(t *testing.T) {
	m, st, fp, audit := oauthMgr(t)
	ctx := context.Background()
	s, err := m.OAuthStartFlow(ctx, admin, "openrouter", "global", "http://127.0.0.1:8080/v1/llm/oauth/callback", false)
	if err != nil {
		t.Fatal(err)
	}
	state, code := fp.approve(t, s.AuthorizeURL)
	if state != s.State {
		t.Fatal("state mismatch")
	}
	if !m.OAuthCallback(state, code, "").OK {
		t.Fatal("callback refused")
	}
	v := waitStatus(t, m, admin, state)
	if v.Status != OAuthConnected || v.Test == nil || !v.Test.OK || v.Config == nil {
		t.Fatalf("not connected: %+v", v)
	}
	cfg := m.Config()
	if cfg.Mode != ModeLLM || cfg.PresetID != "openrouter" || !cfg.APIKeySet || cfg.KeySource != "oauth" || cfg.KeyProvider != "openrouter" {
		t.Fatalf("config: %+v", cfg)
	}
	// stored encrypted, never in any view
	if strings.Contains(string(st.s.APIKeyEnc), fakeOAuthKey) {
		t.Fatal("key stored in clear")
	}
	for _, obj := range []any{cfg, v, s} {
		b, _ := json.Marshal(obj)
		if strings.Contains(string(b), fakeOAuthKey) || strings.Contains(string(b), "code_verifier") {
			t.Fatalf("secret leaked in %s", b)
		}
	}
	if m.key != fakeOAuthKey {
		t.Fatal("active key must be the plain key (tag stripped)")
	}
	if cfg.KeyLabel != "OA Agent" || cfg.APIKeyHint != "••••cdef" {
		t.Fatalf("label/hint: %+v", cfg)
	}
	// the label also survives an unrelated save and never carries the key
	defer func() {
		if c := m.Config(); c.KeyLabel != "" {
			t.Fatalf("a pasted key must drop the login label: %+v", c)
		}
	}()
	// provenance survives an unrelated save (model change without a new key)
	if _, err := m.Save(ctx, Input{Mode: ModeLLM, PresetID: "openrouter", BaseURL: cfg.BaseURL, Model: "other/model"}); err != nil {
		t.Fatal(err)
	}
	if c := m.Config(); c.KeySource != "oauth" || c.Model != "other/model" || c.KeyLabel != "OA Agent" {
		t.Fatalf("after save: %+v", c)
	}
	if len(*audit) == 0 || strings.Contains(strings.Join(*audit, "\n"), fakeOAuthKey) {
		t.Fatalf("audit: %v", *audit)
	}
	// a pasted key afterwards is "manual"
	if _, err := m.Save(ctx, Input{Mode: ModeLLM, PresetID: "openrouter", BaseURL: cfg.BaseURL, Model: "x/y", APIKey: "manual-key-12345"}); err != nil {
		t.Fatal(err)
	}
	if c := m.Config(); c.KeySource != "manual" {
		t.Fatalf("manual: %+v", c)
	}
	// disconnect refuses manual keys
	if _, _, err := m.OAuthDisconnect(ctx, admin, "global"); err == nil {
		t.Fatal("disconnect of a manual key must be refused")
	}
}

func TestOAuthPersonalAndDisconnect(t *testing.T) {
	m, st, fp, _ := oauthMgr(t)
	ctx := context.Background()
	s, err := m.OAuthStartFlow(ctx, opA, "openrouter", "personal", "http://x/cb", true)
	if err != nil {
		t.Fatal(err)
	}
	state, code := fp.approve(t, s.AuthorizeURL)
	m.OAuthCallback(state, code, "")
	if v := waitStatus(t, m, opA, state); v.Status != OAuthConnected {
		t.Fatalf("%+v", v)
	}
	pc, err := m.PersonalConfig(ctx, opA)
	if err != nil || pc.KeySource != "oauth" || !pc.APIKeySet {
		t.Fatalf("%+v %v", pc, err)
	}
	if g := m.Config(); g.APIKeySet {
		t.Fatal("personal login must not touch the global config")
	}
	if _, _, err := m.OAuthDisconnect(ctx, opA, "global"); err == nil {
		t.Fatal("operator must not disconnect the global key")
	}
	if _, prov, err := m.OAuthDisconnect(ctx, opA, "personal"); err != nil || prov != "openrouter" {
		t.Fatalf("%v %s", err, prov)
	}
	if pc, _ := m.PersonalConfig(ctx, opA); pc.APIKeySet {
		t.Fatal("key must be gone")
	}
	if len(st.personal["u-a"].APIKeyEnc) != 0 {
		t.Fatal("sealed key must be deleted")
	}
}

func TestOAuthGates(t *testing.T) {
	m, _, _, _ := oauthMgr(t)
	ctx := context.Background()
	if _, err := m.OAuthStartFlow(ctx, opA, "openrouter", "global", "http://x/cb", true); err == nil {
		t.Error("operator must not start a global flow")
	}
	if _, err := m.OAuthStartFlow(ctx, view, "openrouter", "personal", "http://x/cb", true); err == nil {
		t.Error("viewer must not start a personal flow")
	}
	if _, err := m.OAuthStartFlow(ctx, admin, "deepseek", "global", "http://x/cb", true); err == nil {
		t.Error("provider without a flow must be refused")
	}
	if _, err := m.OAuthStartFlow(ctx, admin, "openrouter", "other", "http://x/cb", true); err == nil {
		t.Error("bad scope")
	}
	setPolicy(t, m, false, false, true)
	if _, err := m.OAuthStartFlow(ctx, opA, "openrouter", "personal", "http://x/cb", true); err == nil {
		t.Error("personal flow while the policy is off")
	}
}

func TestOAuthReplayWrongUserExpiryDenied(t *testing.T) {
	m, _, fp, _ := oauthMgr(t)
	ctx := context.Background()
	s, _ := m.OAuthStartFlow(ctx, opA, "openrouter", "personal", "http://x/cb", true)
	state, code := fp.approve(t, s.AuthorizeURL)
	if _, err := m.OAuthStatusFor(opB, state); err == nil {
		t.Fatal("another user must not read the flow")
	}
	if r := m.OAuthCallback("unknown-state", code, ""); r.OK || r.Reason != "invalid" {
		t.Fatalf("unknown state: %+v", r)
	}
	if !m.OAuthCallback(state, code, "").OK {
		t.Fatal("first callback")
	}
	if r := m.OAuthCallback(state, code, ""); r.OK || r.Reason != "used" {
		t.Fatalf("replay must be refused: %+v", r)
	}
	if v := waitStatus(t, m, opA, state); v.Status != OAuthConnected {
		t.Fatalf("%+v", v)
	}
	fp.mu.Lock()
	n := fp.exCalls
	fp.mu.Unlock()
	if n != 1 {
		t.Fatalf("exchange must run exactly once, ran %d", n)
	}
	// denied (with state, and without: providers drop state on denial)
	s2, _ := m.OAuthStartFlow(ctx, opA, "openrouter", "personal", "http://x/cb", true)
	if r := m.OAuthCallback(s2.State, "", "access_denied"); r.OK || r.Reason != "denied" {
		t.Fatalf("%+v", r)
	}
	if v, _ := m.OAuthStatusFor(opA, s2.State); v.Status != OAuthDenied || v.Message == "" {
		t.Fatalf("%+v", v)
	}
	if r := m.OAuthCallback("", "", ""); r.OK || r.Reason != "denied" {
		t.Fatalf("%+v", r)
	}
	// expiry
	s3, _ := m.OAuthStartFlow(ctx, opA, "openrouter", "personal", "http://x/cb", true)
	m.oa().now = func() time.Time { return time.Now().Add(oauthTTL + 30*time.Second) }
	if r := m.OAuthCallback(s3.State, "c", ""); r.OK || r.Reason != "expired" {
		t.Fatalf("%+v", r)
	}
	if v, _ := m.OAuthStatusFor(opA, s3.State); v.Status != OAuthExpired {
		t.Fatalf("%+v", v)
	}
}

func TestOAuthWrongVerifierAndProviderErrors(t *testing.T) {
	m, _, fp, _ := oauthMgr(t)
	ctx := context.Background()
	// a code issued for another challenge fails the PKCE check at the provider
	s, _ := m.OAuthStartFlow(ctx, admin, "openrouter", "global", "http://x/cb", true)
	state := s.State
	fp.mu.Lock()
	fp.codes["stolen"] = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	fp.mu.Unlock()
	m.OAuthCallback(state, "stolen", "")
	v := waitStatus(t, m, admin, state)
	if v.Status != OAuthFailed || !strings.Contains(v.Message, "授权码") || m.Config().APIKeySet {
		t.Fatalf("%+v", v)
	}
	for _, code := range []int{500, 503, 429, 400} {
		fp.mu.Lock()
		fp.status = code
		fp.mu.Unlock()
		s, _ := m.OAuthStartFlow(ctx, admin, "openrouter", "global", "http://x/cb", true)
		m.OAuthCallback(s.State, "c", "")
		v := waitStatus(t, m, admin, s.State)
		if v.Status != OAuthFailed || v.Message == "" || strings.Contains(v.Message, "boom") {
			t.Fatalf("status %d: %+v", code, v)
		}
	}
	// provider unreachable
	fp.ex.Close()
	s, _ = m.OAuthStartFlow(ctx, admin, "openrouter", "global", "http://x/cb", true)
	m.OAuthCallback(s.State, "c", "")
	if v := waitStatus(t, m, admin, s.State); v.Status != OAuthFailed || !strings.Contains(v.Message, "无法连接") {
		t.Fatalf("%+v", v)
	}
}

func TestOAuthLimitsAndTagging(t *testing.T) {
	m, _, _, _ := oauthMgr(t)
	ctx := context.Background()
	for i := 0; i < oauthMaxPerUser+3; i++ {
		_, err := m.OAuthStartFlow(ctx, admin, "openrouter", "global", "http://x/cb", true)
		if err != nil {
			t.Fatalf("a repeated start supersedes the previous pending one: %v", err)
		}
	}
	k, src, prov := splitKey(tagKey("openrouter", "abc"))
	if k != "abc" || src != "oauth" || prov != "openrouter" {
		t.Fatal(k, src, prov)
	}
	if k, src, _ := splitKey("plain-key"); k != "plain-key" || src != "manual" {
		t.Fatal("plain")
	}
	if err := validKey(tagKey("openrouter", "abc")); err == nil {
		t.Fatal("a pasted key must never be able to carry the provenance tag")
	}
	if err := m.SetOAuthEndpoints("openrouter", "http://evil.example/auth", "https://x/keys"); err == nil {
		t.Fatal("plain http to a public host must be refused")
	}
	found := false
	for _, p := range m.Presets() {
		if p.ID == "openrouter" && p.OAuthLogin && p.KeyURL != "" {
			found = true
		}
		if p.ID == "deepseek" && p.OAuthLogin {
			t.Fatal("deepseek has no login flow")
		}
	}
	if !found {
		t.Fatal("openrouter preset missing")
	}
}

func TestOAuthKeyLabelFallbackAndSanitising(t *testing.T) {
	for _, tc := range []struct {
		mode  int32
		label string
	}{{1, ""}, {2, "OA Agent"}} {
		m, _, fp, _ := oauthMgr(t)
		fp.keyInfo.Store(tc.mode)
		ctx := context.Background()
		s, _ := m.OAuthStartFlow(ctx, admin, "openrouter", "global", "http://x/cb", false)
		state, code := fp.approve(t, s.AuthorizeURL)
		m.OAuthCallback(state, code, "")
		v := waitStatus(t, m, admin, state)
		if v.Status != OAuthConnected {
			t.Fatalf("a failing key-info call must not fail the flow: %+v", v)
		}
		c := m.Config()
		if c.KeyLabel != tc.label || c.APIKeyHint == "" || !c.APIKeySet {
			t.Fatalf("mode %d: %+v", tc.mode, c)
		}
		b, _ := json.Marshal([]any{c, v})
		if strings.Contains(string(b), fakeOAuthKey) {
			t.Fatalf("key leaked: %s", b)
		}
	}
}

func TestKeyLabelTagRoundTrip(t *testing.T) {
	k, src, prov, label := splitKeyLabel(tagKeyLabel("openrouter", "My key", "abc"))
	if k != "abc" || src != "oauth" || prov != "openrouter" || label != "My key" {
		t.Fatal(k, src, prov, label)
	}
	// an old blob without a label still parses
	if k, _, prov, label := splitKeyLabel(tagKey("openrouter", "abc")); k != "abc" || prov != "openrouter" || label != "" {
		t.Fatal("legacy blob", k, prov, label)
	}
	if got := cleanKeyLabel("a\x00b\x01c\n" + strings.Repeat("x", 200)); strings.ContainsAny(got, "\x00\x01\n") || len([]rune(got)) > 80 {
		t.Fatalf("not sanitised: %q", got)
	}
}

func TestManualKeyLabelTagRoundTrip(t *testing.T) {
	k, src, prov, label := splitKeyLabel(tagManualLabel("  OA Agent\n", "sk-abc"))
	if k != "sk-abc" || src != "manual" || prov != "" || label != "OA Agent" {
		t.Fatal(k, src, prov, label)
	}
	if got := tagManualLabel("", "sk-abc"); got != "sk-abc" {
		t.Fatalf("empty label must not tag: %q", got)
	}
	if got := tagManualLabel("\x00\x01\n", "sk-abc"); got != "sk-abc" {
		t.Fatalf("control-only label must not tag: %q", got)
	}
	if k, src, _, label := splitKeyLabel("sk-plain"); k != "sk-plain" || src != "manual" || label != "" {
		t.Fatal("plain key", k, src, label)
	}
}
