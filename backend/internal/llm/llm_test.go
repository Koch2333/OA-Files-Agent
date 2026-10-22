package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"oa-agent/backend/internal/harness"
	"oa-agent/backend/internal/secrets"
)

type memStore struct {
	s  Settings
	ok bool
}

func (m *memStore) LoadLLM(context.Context) (Settings, bool, error) { return m.s, m.ok, nil }
func (m *memStore) SaveLLM(_ context.Context, s Settings) error     { m.s, m.ok = s, true; return nil }

func newBox(t *testing.T) *secrets.Box {
	t.Helper()
	b, err := secrets.Open("", filepath.Join(t.TempDir(), "k"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newMgr(t *testing.T, seed Seed) (*Manager, *memStore) {
	t.Helper()
	st := &memStore{}
	m, err := New(context.Background(), st, newBox(t), seed)
	if err != nil {
		t.Fatal(err)
	}
	return m, st
}

// fakeLLM is an OpenAI-compatible server. Behaviour is switchable per test.
type fakeLLM struct {
	srv        *httptest.Server
	wantKey    string
	supportsTc atomic.Bool
	calls      atomic.Int32
	lastModel  atomic.Value
}

func newFake(t *testing.T, key string) *fakeLLM {
	f := &fakeLLM{wantKey: key}
	f.supportsTc.Store(true)
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if key != "" && r.Header.Get("Authorization") != "Bearer "+key {
			http.Error(w, `{"error":"bad key"}`, 401)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/models"):
			w.Write([]byte(`{"data":[{"id":"qwen-plus"},{"id":"deepseek-chat"},{"id":"bad id with spaces"},{"id":"deepseek-chat"}]}`))
		case strings.HasSuffix(r.URL.Path, "/chat/completions"):
			f.calls.Add(1)
			var body struct {
				Model string           `json:"model"`
				Tools []map[string]any `json:"tools"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			f.lastModel.Store(body.Model)
			if body.Model == "missing-model" {
				http.Error(w, `{"error":"no such model"}`, 404)
				return
			}
			if len(body.Tools) > 0 && f.supportsTc.Load() {
				w.Write([]byte(`{"choices":[{"message":{"content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"echo","arguments":"{\"text\":\"ok\"}"}}]}}]}`))
				return
			}
			w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func TestNormalizeBaseURL(t *testing.T) {
	good := map[string]string{
		"https://api.deepseek.com/v1":                       "https://api.deepseek.com/v1",
		"https://api.deepseek.com/v1/":                      "https://api.deepseek.com/v1",
		"  https://api.deepseek.com  ":                      "https://api.deepseek.com",
		"http://127.0.0.1:11434/v1":                         "http://127.0.0.1:11434/v1",
		"http://localhost:8000/v1":                          "http://localhost:8000/v1",
		"http://192.168.1.20:1234/v1":                       "http://192.168.1.20:1234/v1",
		"http://10.0.0.5/v1":                                "http://10.0.0.5/v1",
		"https://dashscope.aliyuncs.com/compatible-mode/v1": "https://dashscope.aliyuncs.com/compatible-mode/v1",
	}
	for in, want := range good {
		if got, err := NormalizeBaseURL(in); err != nil || got != want {
			t.Errorf("NormalizeBaseURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{
		"", "api.deepseek.com", "ftp://x/v1", "file:///etc/passwd", "javascript:alert(1)",
		"http://api.deepseek.com/v1", "http://8.8.8.8/v1", // key would travel in clear over the internet
		"https://user:pw@api.example.com/v1", "https://api.example.com/v1?x=1", "https://api.example.com/v1#f",
		"http://169.254.169.254/latest/meta-data", "https://169.254.169.254/", "https://[fe80::1]/", "http://0.0.0.0:80/",
		"https://api.example.com/v1\nHost: evil", "https://exa mple.com", strings.Repeat("a", 301),
	} {
		if got, err := NormalizeBaseURL(in); err == nil {
			t.Errorf("NormalizeBaseURL(%q) = %q, want an error", in, got)
		} else if _, ok := err.(*ValidationError); !ok {
			t.Errorf("error for %q is not a ValidationError: %T", in, err)
		}
	}
}

func TestPresetsDefaultToDeepSeekThenQwenAndStaySelfConsistent(t *testing.T) {
	ps := Presets()
	if ps[0].ID != "deepseek" || ps[1].ID != "qwen" {
		t.Fatalf("DeepSeek must lead, Qwen second: %s, %s", ps[0].ID, ps[1].ID)
	}
	seen := map[string]bool{}
	for _, p := range ps {
		if seen[p.ID] {
			t.Errorf("duplicate preset %s", p.ID)
		}
		seen[p.ID] = true
		if p.ID == "custom" {
			continue
		}
		if _, err := NormalizeBaseURL(p.BaseURL); err != nil {
			t.Errorf("%s base URL invalid: %v", p.ID, err)
		}
		found := len(p.Models) == 0 // presets without suggestions (OpenAI: use the live list) have no default either
		for _, m := range p.Models {
			if validModel(m.ID) != nil {
				t.Errorf("%s model %q invalid", p.ID, m.ID)
			}
			if m.ID == p.DefaultModel {
				found = true
			}
		}
		if !found {
			t.Errorf("%s default model %q is not in its model list", p.ID, p.DefaultModel)
		}
	}
	if !ps[0].NeedsAPIKey || ps[3].ID != "ollama" || ps[3].NeedsAPIKey {
		t.Error("key requirements wrong")
	}
}

func TestFreshInstallIsOfflineWithDeepSeekPreselected(t *testing.T) {
	m, st := newMgr(t, Seed{})
	c := m.Config()
	if c.Mode != ModeHeuristic || c.PresetID != "deepseek" || c.State != "ok" || c.APIKeySet || !st.ok {
		t.Fatalf("fresh config: %+v", c)
	}
	if _, ok := m.Provider().(*Switch); !ok {
		t.Fatal("agent must receive the switchable provider")
	}
	name, model, state := m.Status()
	if name != "heuristic" || state != "ok" || !strings.Contains(model, "离线") {
		t.Fatalf("status: %s %s %s", name, model, state)
	}
}

func TestSaveLLMModeNeverReturnsKeyAndHotSwapsProvider(t *testing.T) {
	f := newFake(t, "sk-test-12345678")
	m, st := newMgr(t, Seed{})
	ctx := context.Background()
	cfg, err := m.Save(ctx, Input{Mode: ModeLLM, PresetID: "custom", BaseURL: f.srv.URL + "/v1", Model: "my-model", APIKey: "sk-test-12345678"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(cfg)
	if strings.Contains(string(raw), "12345678") || !cfg.APIKeySet || cfg.APIKeyHint != "••••5678" || cfg.State != "ok" || cfg.Mode != ModeLLM {
		t.Fatalf("view must carry only a hint: %s", raw)
	}
	if strings.Contains(string(st.s.APIKeyEnc), "sk-test") || len(st.s.APIKeyEnc) == 0 {
		t.Fatal("key must be stored encrypted")
	}
	// the very next model call goes to the configured endpoint with the configured model — no restart
	resp, err := m.Provider().Chat(ctx, []harness.Message{{Role: harness.RoleUser, Content: "hi"}}, nil)
	if err != nil || resp.Content != "ok" || f.lastModel.Load() != "my-model" {
		t.Fatalf("hot swap: %v %+v model=%v", err, resp, f.lastModel.Load())
	}
	// switching back to offline rules takes effect immediately and keeps the form data
	cfg, err = m.Save(ctx, Input{Mode: ModeHeuristic})
	if err != nil || cfg.Mode != ModeHeuristic || cfg.Model != "my-model" || !cfg.APIKeySet {
		t.Fatalf("back to offline: %v %+v", err, cfg)
	}
	before := f.calls.Load()
	if _, err := m.Provider().Chat(ctx, []harness.Message{{Role: harness.RoleSystem, Content: "x"}, {Role: harness.RoleUser, Content: "y"}}, nil); err != nil {
		t.Log("heuristic chat result ignored:", err)
	}
	if f.calls.Load() != before {
		t.Fatal("offline mode must not call the model server")
	}
}

func TestSaveValidationAndKeyRules(t *testing.T) {
	m, _ := newMgr(t, Seed{})
	ctx := context.Background()
	code := func(err error) string {
		var ve *ValidationError
		if errors.As(err, &ve) {
			return ve.Code
		}
		return "other:" + err.Error()
	}
	for name, tc := range map[string]struct {
		in   Input
		want string
	}{
		"bad mode":             {Input{Mode: "x"}, "INVALID_MODE"},
		"missing url":          {Input{Mode: ModeLLM, PresetID: "deepseek", Model: "deepseek-chat", APIKey: "sk-12345678"}, "INVALID_BASE_URL"},
		"missing model":        {Input{Mode: ModeLLM, PresetID: "deepseek", BaseURL: "https://api.deepseek.com/v1", APIKey: "sk-12345678"}, "MODEL_REQUIRED"},
		"bad model chars":      {Input{Mode: ModeLLM, PresetID: "deepseek", BaseURL: "https://api.deepseek.com/v1", Model: "a b", APIKey: "sk-12345678"}, "INVALID_MODEL"},
		"key required":         {Input{Mode: ModeLLM, PresetID: "deepseek", BaseURL: "https://api.deepseek.com/v1", Model: "deepseek-chat"}, "KEY_REQUIRED"},
		"key with newline":     {Input{Mode: ModeLLM, PresetID: "deepseek", BaseURL: "https://api.deepseek.com/v1", Model: "deepseek-chat", APIKey: "sk-1234\n5678"}, "INVALID_API_KEY"},
		"key with chinese":     {Input{Mode: ModeLLM, PresetID: "deepseek", BaseURL: "https://api.deepseek.com/v1", Model: "deepseek-chat", APIKey: "密钥密钥密钥密钥密钥"}, "INVALID_API_KEY"},
		"unknown preset":       {Input{Mode: ModeLLM, PresetID: "nope", BaseURL: "https://x.example.com/v1", Model: "m", APIKey: "sk-12345678"}, "INVALID_PRESET"},
		"http to the internet": {Input{Mode: ModeLLM, PresetID: "custom", BaseURL: "http://api.example.com/v1", Model: "m"}, "INVALID_BASE_URL"},
	} {
		if _, err := m.Save(ctx, tc.in); code(err) != tc.want {
			t.Errorf("%s: got %v, want %s", name, err, tc.want)
		}
	}
	if m.Config().Mode != ModeHeuristic {
		t.Fatal("a rejected save must not change the active engine")
	}
	// keyless providers (Ollama / custom) work without a key
	if cfg, err := m.Save(ctx, Input{Mode: ModeLLM, PresetID: "ollama", BaseURL: "http://127.0.0.1:11434/v1", Model: "qwen2.5:7b"}); err != nil || cfg.State != "ok" || cfg.APIKeySet {
		t.Fatalf("ollama: %v %+v", err, cfg)
	}
}

func TestSavedKeyIsNeverSentToADifferentBaseURL(t *testing.T) {
	a := newFake(t, "sk-for-vendor-A")
	b := newFake(t, "") // would accept anything: we assert what it receives
	var gotAuth atomic.Value
	b.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"data":[]}`))
	})
	m, _ := newMgr(t, Seed{})
	ctx := context.Background()
	if _, err := m.Save(ctx, Input{Mode: ModeLLM, PresetID: "custom", BaseURL: a.srv.URL + "/v1", Model: "m", APIKey: "sk-for-vendor-A"}); err != nil {
		t.Fatal(err)
	}
	// test against vendor B without typing a key: the saved key must not be reused
	gotAuth.Store("")
	if _, err := m.Test(ctx, TestInput{BaseURL: b.srv.URL + "/v1", Model: "m"}); err != nil {
		t.Fatal(err)
	}
	if v := gotAuth.Load(); v != "" {
		t.Fatalf("saved key leaked to another base URL: %v", v)
	}
	gotAuth.Store("")
	if _, err := m.Models(ctx, b.srv.URL+"/v1", ""); err != nil {
		t.Logf("models error ignored: %v", err)
	}
	if v := gotAuth.Load(); v != "" {
		t.Fatalf("saved key leaked on /models: %v", v)
	}
	// saving a key-requiring preset for a new URL without a new key is refused, not silently reusing the old key
	_, err := m.Save(ctx, Input{Mode: ModeLLM, PresetID: "qwen", BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1", Model: "qwen-plus"})
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Code != "KEY_REQUIRED" || !strings.Contains(ve.Msg, "重新输入") {
		t.Fatalf("changing provider must demand a new key: %v", err)
	}
	// same URL → the saved key is reused (user only changes the model)
	cfg, err := m.Save(ctx, Input{Mode: ModeLLM, PresetID: "custom", BaseURL: a.srv.URL + "/v1/", Model: "m2"})
	if err != nil || !cfg.APIKeySet || cfg.Model != "m2" {
		t.Fatalf("model change must keep the key: %v %+v", err, cfg)
	}
	if cfg, err = m.Save(ctx, Input{Mode: ModeLLM, PresetID: "custom", BaseURL: a.srv.URL + "/v1", Model: "m2", ClearAPIKey: true}); err != nil || cfg.APIKeySet {
		t.Fatalf("clear key: %v %+v", err, cfg)
	}
}

func TestConnectionTestReportsReadableOutcomes(t *testing.T) {
	f := newFake(t, "sk-good-1234567")
	m, _ := newMgr(t, Seed{})
	ctx := context.Background()
	res, err := m.Test(ctx, TestInput{BaseURL: f.srv.URL + "/v1", Model: "qwen-plus", APIKey: "sk-good-1234567"})
	if err != nil || !res.OK || res.ToolCalling == nil || !*res.ToolCalling || res.Message != "连接成功" {
		t.Fatalf("good: %v %+v", err, res)
	}
	f.supportsTc.Store(false)
	res, _ = m.Test(ctx, TestInput{BaseURL: f.srv.URL + "/v1", Model: "qwen-plus", APIKey: "sk-good-1234567"})
	if !res.OK || res.ToolCalling == nil || *res.ToolCalling || !strings.Contains(res.Message, "工具调用") {
		t.Fatalf("no tool calling must be flagged: %+v", res)
	}
	res, _ = m.Test(ctx, TestInput{BaseURL: f.srv.URL + "/v1", Model: "qwen-plus", APIKey: "sk-wrong-1234567"})
	if res.OK || !strings.Contains(res.Message, "密钥无效") || strings.Contains(res.Message, "sk-wrong") {
		t.Fatalf("401: %+v", res)
	}
	res, _ = m.Test(ctx, TestInput{BaseURL: f.srv.URL + "/v1", Model: "missing-model", APIKey: "sk-good-1234567"})
	if res.OK || !strings.Contains(res.Message, "不存在") {
		t.Fatalf("404: %+v", res)
	}
	f.srv.Close()
	res, _ = m.Test(ctx, TestInput{BaseURL: f.srv.URL + "/v1", Model: "qwen-plus", APIKey: "sk-good-1234567"})
	if res.OK || !strings.Contains(res.Message, "无法连接") {
		t.Fatalf("unreachable: %+v", res)
	}
	if _, err = m.Test(ctx, TestInput{BaseURL: "http://evil.example.com/v1", Model: "m"}); err == nil {
		t.Fatal("unsafe base URLs are rejected before any request is made")
	}
}

func TestModelListing(t *testing.T) {
	f := newFake(t, "sk-good-1234567")
	m, _ := newMgr(t, Seed{})
	got, err := m.Models(context.Background(), f.srv.URL+"/v1", "sk-good-1234567")
	if err != nil || strings.Join(got, ",") != "deepseek-chat,qwen-plus" {
		t.Fatalf("models (sorted, de-duplicated, invalid ids dropped): %v %v", got, err)
	}
	var ue *UpstreamError
	if _, err = m.Models(context.Background(), f.srv.URL+"/v1", "sk-wrong-1234567"); !errors.As(err, &ue) || !strings.Contains(ue.Msg, "密钥无效") {
		t.Fatalf("bad key: %v", err)
	}
}

func TestSeedFromEnvironmentOnlyOnce(t *testing.T) {
	f := newFake(t, "sk-env-12345678")
	st := &memStore{}
	box := newBox(t)
	m, err := New(context.Background(), st, box, Seed{Provider: "openai", BaseURL: f.srv.URL + "/v1", Model: "env-model", APIKey: "sk-env-12345678"})
	if err != nil {
		t.Fatal(err)
	}
	if c := m.Config(); c.Mode != ModeLLM || c.Model != "env-model" || !c.APIKeySet || c.PresetID != "custom" {
		t.Fatalf("seeded: %+v", c)
	}
	// later starts ignore the environment: the saved configuration rules
	m2, err := New(context.Background(), st, box, Seed{Provider: "openai", BaseURL: "https://api.deepseek.com/v1", Model: "other"})
	if err != nil {
		t.Fatal(err)
	}
	if c := m2.Config(); c.Model != "env-model" {
		t.Fatalf("environment must not override saved settings: %+v", c)
	}
	// DeepSeek URL from the environment is recognised as the preset
	st3 := &memStore{}
	m3, _ := New(context.Background(), st3, box, Seed{BaseURL: "https://api.deepseek.com/v1/", Model: "deepseek-chat", APIKey: "sk-env-12345678"})
	if c := m3.Config(); c.PresetID != "deepseek" || c.State != "ok" {
		t.Fatalf("preset detection: %+v", c)
	}
	// an invalid environment configuration must not break start-up
	if m4, err := New(context.Background(), &memStore{}, box, Seed{Provider: "openai", BaseURL: "ftp://x", Model: "m"}); err != nil || m4.Config().Mode != ModeHeuristic {
		t.Fatalf("invalid env: %v", err)
	}
}

func TestUndecryptableKeyFallsBackToOfflineAndReportsIt(t *testing.T) {
	f := newFake(t, "sk-good-1234567")
	st := &memStore{}
	m, _ := New(context.Background(), st, newBox(t), Seed{})
	if _, err := m.Save(context.Background(), Input{Mode: ModeLLM, PresetID: "custom", BaseURL: f.srv.URL + "/v1", Model: "m", APIKey: "sk-good-1234567"}); err != nil {
		t.Fatal(err)
	}
	// same database, different secret key (e.g. restored on another machine)
	m2, err := New(context.Background(), st, newBox(t), Seed{})
	if err != nil {
		t.Fatal(err)
	}
	c := m2.Config()
	if c.State != "unavailable" || !c.APIKeySet {
		t.Fatalf("state must say the stored key is unusable: %+v", c)
	}
	before := f.calls.Load()
	m2.Provider().Chat(context.Background(), []harness.Message{{Role: harness.RoleUser, Content: "x"}}, nil)
	if f.calls.Load() != before {
		t.Fatal("must not call the provider with a missing key")
	}
	if _, _, state := m2.Status(); state == "ok" {
		t.Fatal("status must not claim the LLM is active")
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	target := newFake(t, "")
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.srv.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer redir.Close()
	m, _ := newMgr(t, Seed{})
	res, _ := m.Test(context.Background(), TestInput{BaseURL: redir.URL + "/v1", Model: "m"})
	if res.OK || target.calls.Load() != 0 {
		t.Fatalf("a redirecting endpoint must not be followed: %+v calls=%d", res, target.calls.Load())
	}
}

func TestKeyHint(t *testing.T) {
	for in, want := range map[string]string{"": "", "short": "••••", "sk-abcdefgh1234": "••••1234"} {
		if got := KeyHint(in); got != want {
			t.Errorf("KeyHint(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEveryPresetIsVerifiedAndExplained(t *testing.T) {
	for _, p := range Presets() {
		if len(p.CheckedAt) != 10 || p.CheckedAt[4] != '-' || p.CheckedAt[7] != '-' {
			t.Errorf("%s: checked_at must be YYYY-MM-DD, got %q", p.ID, p.CheckedAt)
		}
		if strings.TrimSpace(p.Note) == "" {
			t.Errorf("%s: a provider hint (note) is required", p.ID)
		}
	}
	by := map[string]Preset{}
	for _, p := range Presets() {
		by[p.ID] = p
	}
	if len(by["openai"].Models) != 0 {
		t.Error("OpenAI has no hard-coded suggestions: model names change too fast, the live list is authoritative")
	}
	if _, ok := by["qwen-intl"]; ok {
		t.Error("the unverifiable qwen-intl preset was removed")
	}
	if by["deepseek"].DefaultModel != "deepseek-flash" || by["qwen"].DefaultModel != "qwen3.7-plus" {
		t.Errorf("defaults: %q %q", by["deepseek"].DefaultModel, by["qwen"].DefaultModel)
	}
}

func TestAdministratorCanOverrideTheCatalogWithoutARebuild(t *testing.T) {
	orig := Presets()
	t.Cleanup(func() { catalogMu.Lock(); catalog = orig; catalogMu.Unlock() })
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	good := write("ok.json", `[{"id":"acme","name":"Acme 网关","description":"d","base_url":"https://llm.acme.example/v1","models":[{"id":"acme-large","label":"acme-large"}],"default_model":"acme-large","needs_api_key":true,"docs_url":"","vision_models":[],"checked_at":"2027-01-05","note":"n"}]`)
	if err := LoadOverride(good); err != nil {
		t.Fatal(err)
	}
	ps := Presets()
	if ps[0].ID != "acme" || ps[len(ps)-1].ID != "custom" {
		t.Fatalf("override applied and a custom entry appended: %+v", ps)
	}
	if _, ok := presetByID("acme"); !ok {
		t.Fatal("presetByID must follow the override")
	}
	for name, body := range map[string]string{
		"dup":      `[{"id":"a","name":"x","models":[],"vision_models":[]},{"id":"a","name":"y","models":[],"vision_models":[]}]`,
		"badid":    `[{"id":"Bad ID","name":"x"}]`,
		"badurl":   `[{"id":"a","name":"x","base_url":"http://api.example.com/v1"}]`,
		"badmodel": `[{"id":"a","name":"x","models":[{"id":"has space","label":"x"}]}]`,
		"empty":    `[]`,
		"notjson":  `nope`,
		"noname":   `[{"id":"a","name":" "}]`,
	} {
		before := Presets()
		if err := LoadOverride(write(name+".json", body)); err == nil {
			t.Errorf("%s: invalid catalogs must be rejected", name)
		}
		if after := Presets(); len(after) != len(before) || after[0].ID != before[0].ID {
			t.Errorf("%s: a rejected override must leave the active catalog untouched", name)
		}
	}
	if err := LoadOverride(filepath.Join(dir, "missing.json")); err == nil {
		t.Error("missing file must be an error")
	}
}
