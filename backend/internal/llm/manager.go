package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"oa-agent/backend/internal/harness"
	"oa-agent/backend/internal/secrets"
	"oa-agent/backend/internal/store"
)

type Settings = store.LLMSettings

// Store persists the single configuration row (implemented by *store.Store).
type Store interface {
	LoadLLM(context.Context) (Settings, bool, error)
	SaveLLM(context.Context, Settings) error
}

const (
	ModeHeuristic = "heuristic"
	ModeLLM       = "llm"
)

// Input is a save request. APIKey "" keeps the saved key (only while the Base URL is unchanged).
type Input struct {
	Mode        string `json:"mode"`
	PresetID    string `json:"preset_id"`
	BaseURL     string `json:"base_url"`
	Model       string `json:"model"`
	APIKey      string `json:"api_key"`
	ClearAPIKey bool   `json:"clear_api_key"`
	// KeyLabel is an optional name the user gave the key at the provider (status line only; kept inside the sealed blob
	// next to the key, and only together with a new APIKey).
	KeyLabel string `json:"key_label"`
	// Optional vision fallback for poor OCR (opt-in: page images are sent to the provider).
	VisionModel string `json:"vision_model"`
	AllowVision bool   `json:"allow_vision"`
}

type TestInput struct {
	BaseURL     string `json:"base_url"`
	Model       string `json:"model"`
	APIKey      string `json:"api_key"`
	VisionModel string `json:"vision_model"`
}

type ConfigView struct {
	Mode       string `json:"mode"`
	PresetID   string `json:"preset_id"`
	BaseURL    string `json:"base_url"`
	Model      string `json:"model"`
	APIKeySet  bool   `json:"api_key_set"`
	APIKeyHint string `json:"api_key_hint"`
	// KeySource is "manual" (pasted) or "oauth" (obtained by signing in with the platform); KeyProvider names the platform.
	KeySource   string `json:"key_source,omitempty"`
	KeyProvider string `json:"key_provider,omitempty"`
	// KeyLabel is the key's name at the provider (login-obtained keys only, when the provider reports it).
	KeyLabel    string     `json:"key_label,omitempty"`
	VisionModel string     `json:"vision_model"`
	AllowVision bool       `json:"allow_vision"`
	State       string     `json:"state"`
	UpdatedAt   *time.Time `json:"updated_at"`
}

type TestResult struct {
	OK          bool   `json:"ok"`
	LatencyMS   int64  `json:"latency_ms"`
	Message     string `json:"message"`
	ToolCalling *bool  `json:"tool_calling"`
	VisionOK    *bool  `json:"vision_ok"`
	// Failure details (zero when OK): HTTP status from the provider, whether trying again can help, Retry-After seconds.
	Status            int  `json:"status,omitempty"`
	Retryable         bool `json:"retryable,omitempty"`
	RetryAfterSeconds int  `json:"retry_after_seconds,omitempty"`
}

// UpstreamError is a failure talking to the model provider (HTTP 502), already worded for users.
type UpstreamError struct{ Msg string }

func (e *UpstreamError) Error() string { return e.Msg }

type Manager struct {
	store Store
	box   *secrets.Box
	sw    *Switch
	mu    sync.Mutex
	set   Settings
	key   string // decrypted, memory only
	state string
	http  *http.Client
	ai    AIStore // nil: no personal configurations, default policy
	// Audit records connect events (optional; never receives secrets).
	Audit    func(ctx context.Context, actor, action, target, summary string)
	oaOnce   sync.Once
	oauth    *oauthState
	keySrc   string // manual | oauth (global key provenance)
	keyProv  string
	keyLabel string
	pers     personalState
	pmu      sync.Mutex // serialises personal saves
	// allowPrivate mirrors policy.allow_personal_private_hosts for the dial-time check of the personal client.
	allowPrivate atomic.Bool
	// Timeout per model call (OA_LLM_TIMEOUT_SECONDS); tests use a shorter fixed value.
	Timeout time.Duration
}

// Seed is the optional initial configuration (from OA_LLM_* environment variables), used only when nothing is saved.
type Seed struct {
	Provider, BaseURL, Model, APIKey string
	Timeout                          time.Duration
}

func New(ctx context.Context, st Store, box *secrets.Box, seed Seed) (*Manager, error) {
	m := &Manager{store: st, box: box, sw: NewSwitch(harness.Heuristic{}), Timeout: seed.Timeout,
		http: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	if a, ok := st.(AIStore); ok {
		m.ai = a
		if p, err := a.LoadAIPolicy(ctx); err == nil {
			m.allowPrivate.Store(p.AllowPersonalPrivateHosts)
		}
	}
	cur, ok, err := st.LoadLLM(ctx)
	if err != nil {
		return nil, err
	}
	if !ok {
		cur = Settings{Mode: ModeHeuristic, PresetID: "deepseek"}
		if strings.EqualFold(seed.Provider, "openai") || (seed.BaseURL != "" && seed.Model != "") {
			if base, verr := NormalizeBaseURL(seed.BaseURL); verr == nil && validModel(seed.Model) == nil {
				cur = Settings{Mode: ModeLLM, PresetID: presetForURL(base), BaseURL: base, Model: strings.TrimSpace(seed.Model)}
				if seed.APIKey != "" && validKey(seed.APIKey) == nil {
					if enc, serr := box.Seal([]byte(seed.APIKey)); serr == nil {
						cur.APIKeyEnc = enc
					}
				}
				log.Printf("recognition engine seeded from environment: %s / %s", cur.PresetID, cur.Model)
			} else {
				log.Printf("OA_LLM_* environment ignored: %v", verr)
			}
		}
		if err := st.SaveLLM(ctx, cur); err != nil {
			return nil, err
		}
	}
	m.apply(cur)
	return m, nil
}

// sameEndpoint compares base URLs ignoring a trailing slash and a trailing /v1 (providers such as DeepSeek accept both).
func sameEndpoint(a, b string) bool {
	n := func(s string) string {
		return strings.TrimSuffix(strings.ToLower(strings.TrimRight(strings.TrimSpace(s), "/")), "/v1")
	}
	return a != "" && b != "" && n(a) == n(b)
}

func presetForURL(base string) string {
	for _, p := range Presets() {
		if sameEndpoint(p.BaseURL, base) {
			return p.ID
		}
	}
	return "custom"
}

// apply decrypts the key, builds the provider and swaps it in. Caller holds no lock requirement at init.
func (m *Manager) apply(s Settings) {
	m.set, m.key, m.state = s, "", "ok"
	m.keySrc, m.keyProv, m.keyLabel = "", "", ""
	if len(s.APIKeyEnc) > 0 {
		if k, err := m.box.Unseal(s.APIKeyEnc); err == nil {
			m.key, m.keySrc, m.keyProv, m.keyLabel = splitKeyLabel(string(k))
		} else {
			m.state = "unavailable"
			log.Printf("saved API key cannot be decrypted (secret key changed?): re-enter it in Settings")
		}
	}
	if s.Mode != ModeLLM {
		m.sw.Set(harness.Heuristic{})
		if m.state == "unavailable" {
			m.state = "ok" // offline mode does not need the key
		}
		return
	}
	p, known := presetByID(s.PresetID)
	if m.key == "" && (known && p.NeedsAPIKey) {
		m.sw.Set(harness.Heuristic{}) // a key-requiring provider without a usable key must not run: fall back to offline rules
		if m.state == "ok" {
			m.state = "unconfigured"
		}
		return
	}
	prov, err := m.build(s.BaseURL, s.Model, m.key, 0, m.Timeout)
	if err != nil {
		m.sw.Set(harness.Heuristic{})
		m.state = "unavailable"
		return
	}
	m.sw.Set(prov)
}

func (m *Manager) build(base, model, key string, retries int, timeout time.Duration) (harness.Provider, error) {
	return buildWith(m.http, base, model, key, retries, timeout)
}

func buildWith(client *http.Client, base, model, key string, retries int, timeout time.Duration) (harness.Provider, error) {
	return harness.NewOpenAICompatible(harness.OpenAIConfig{BaseURL: base, Model: model, APIKey: key, Timeout: timeout, MaxRetries: retries, HTTPClient: client})
}

// Provider is the stable provider handed to the agent; it follows saved configuration changes.
func (m *Manager) Provider() harness.Provider { return m.sw }

func (m *Manager) Presets() []Preset { return Presets() }

func (m *Manager) viewLocked() ConfigView {
	v := ConfigView{Mode: m.set.Mode, PresetID: m.set.PresetID, BaseURL: m.set.BaseURL, Model: m.set.Model, VisionModel: m.set.VisionModel, AllowVision: m.set.AllowVision, APIKeySet: len(m.set.APIKeyEnc) > 0, State: m.state}
	if m.key != "" {
		v.APIKeyHint = KeyHint(m.key)
		v.KeySource, v.KeyProvider, v.KeyLabel = m.keySrc, m.keyProv, m.keyLabel
	} else if v.APIKeySet {
		v.APIKeyHint = "••••"
	}
	if !m.set.UpdatedAt.IsZero() {
		t := m.set.UpdatedAt
		v.UpdatedAt = &t
	}
	return v
}

func (m *Manager) Config() ConfigView {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.viewLocked()
}

// Status describes the active engine for the legacy /v1/providers listing.
func (m *Manager) Status() (name, model, state string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.set.Mode == ModeLLM && m.state == "ok" {
		if p, ok := presetByID(m.set.PresetID); ok {
			return p.Name, m.set.Model, "ok"
		}
		return "openai-compatible", m.set.Model, "ok"
	}
	if m.set.Mode == ModeLLM {
		return "heuristic", "rule-based（离线，非 LLM；大模型未生效）", m.state
	}
	return "heuristic", "rule-based（离线，非 LLM）", "ok"
}

// keyFor chooses the key for a request to base: an explicit key wins; the saved key is only reused when the
// Base URL is unchanged, so switching provider never leaks one vendor's key to another.
func (m *Manager) keyFor(base, explicit string) string {
	if explicit != "" {
		return explicit
	}
	if m.key != "" && strings.EqualFold(strings.TrimRight(m.set.BaseURL, "/"), strings.TrimRight(base, "/")) {
		return m.key
	}
	return ""
}

func (m *Manager) Save(ctx context.Context, in Input) (ConfigView, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	next, err := m.buildSettings(in, m.set, m.key, NormalizeBaseURL)
	if err != nil {
		return ConfigView{}, err
	}
	if err := m.store.SaveLLM(ctx, next); err != nil {
		return ConfigView{}, err
	}
	next.UpdatedAt = time.Now().UTC()
	m.apply(next)
	return m.viewLocked(), nil
}

// keyForSaved: an explicit key wins; the saved key is only reused while the Base URL is unchanged.
func keyForSaved(cur Settings, curKey, base, explicit string) string {
	if explicit != "" {
		return explicit
	}
	if curKey != "" && strings.EqualFold(strings.TrimRight(cur.BaseURL, "/"), strings.TrimRight(base, "/")) {
		return curKey
	}
	return ""
}

// buildSettings validates a save request against the current (global or personal) configuration and returns the
// settings to store. norm normalises the Base URL (the global and personal rules differ).
func (m *Manager) buildSettings(in Input, cur Settings, curKey string, norm func(string) (string, error)) (Settings, error) {
	next := Settings{Mode: in.Mode, PresetID: strings.TrimSpace(in.PresetID)}
	switch in.Mode {
	case ModeHeuristic:
		// keep the last LLM fields so the form is pre-filled when the user switches back
		next.PresetID, next.BaseURL, next.Model, next.APIKeyEnc = cur.PresetID, cur.BaseURL, cur.Model, cur.APIKeyEnc
		if in.ClearAPIKey {
			next.APIKeyEnc = nil // "remove key": switch to offline rules and forget the saved key
		}
		next.VisionModel, next.AllowVision = cur.VisionModel, cur.AllowVision
		if next.PresetID == "" {
			next.PresetID = "deepseek"
		}
	case ModeLLM:
		base, err := norm(in.BaseURL)
		if err != nil {
			return Settings{}, err
		}
		if err := validModel(in.Model); err != nil {
			return Settings{}, err
		}
		if next.PresetID == "" {
			next.PresetID = presetForURL(base)
		}
		preset, known := presetByID(next.PresetID)
		if !known {
			return Settings{}, bad("INVALID_PRESET", "未知的预设")
		}
		kept := false
		key := ""
		switch {
		case in.APIKey != "":
			key = strings.TrimSpace(in.APIKey)
			if err := validKey(key); err != nil {
				return Settings{}, err
			}
		case in.ClearAPIKey:
		default:
			key = keyForSaved(cur, curKey, base, "")
			kept = key != ""
		}
		if key == "" && preset.NeedsAPIKey {
			if cur.BaseURL != "" && !strings.EqualFold(strings.TrimRight(cur.BaseURL, "/"), base) && len(cur.APIKeyEnc) > 0 {
				return Settings{}, bad("KEY_REQUIRED", "已更换接口地址，请重新输入该服务商的 API Key（不会沿用旧服务商的密钥）")
			}
			return Settings{}, bad("KEY_REQUIRED", "该服务商需要 API Key")
		}
		next.BaseURL, next.Model = base, strings.TrimSpace(in.Model)
		next.VisionModel, next.AllowVision = strings.TrimSpace(in.VisionModel), in.AllowVision
		if next.VisionModel != "" {
			if err := validModel(next.VisionModel); err != nil {
				return Settings{}, bad("INVALID_VISION_MODEL", "视觉模型名称只能包含字母、数字及 . _ : / @ + -")
			}
		}
		if next.AllowVision && next.VisionModel == "" {
			return Settings{}, bad("VISION_MODEL_REQUIRED", "开启视觉识别需要填写视觉模型名称")
		}
		if kept {
			next.APIKeyEnc = cur.APIKeyEnc // unchanged key: keep the sealed blob (and its provenance tag)
		} else if key != "" {
			enc, err := m.box.Seal([]byte(tagManualLabel(in.KeyLabel, key)))
			if err != nil {
				return Settings{}, err
			}
			next.APIKeyEnc = enc
		}
	default:
		return Settings{}, bad("INVALID_MODE", "mode 须为 heuristic 或 llm")
	}
	return next, nil
}

func (m *Manager) resolveCall(baseRaw, explicitKey string) (base, key string, err error) {
	base, err = NormalizeBaseURL(baseRaw)
	if err != nil {
		return "", "", err
	}
	explicitKey = strings.TrimSpace(explicitKey)
	if explicitKey != "" {
		if err = validKey(explicitKey); err != nil {
			return "", "", err
		}
	}
	m.mu.Lock()
	key = m.keyFor(base, explicitKey)
	m.mu.Unlock()
	return base, key, nil
}

func describe(err error) string {
	var pe *harness.ProviderError
	if errors.As(err, &pe) {
		if pe.Attempts > 1 && pe.Retryable {
			return pe.UserMessage()
		}
		switch {
		case pe.Status == 401 || pe.Status == 403:
			return "密钥无效或没有权限（HTTP " + fmt.Sprint(pe.Status) + "）"
		case pe.Status == 404:
			return "接口或模型不存在（HTTP 404）：请检查 Base URL 与模型名称"
		case pe.Status == 400 || pe.Status == 422:
			return fmt.Sprintf("服务商拒绝了请求（HTTP %d）：模型名称可能不正确或不受支持", pe.Status)
		case pe.Status == 429:
			return "请求过于频繁或额度不足（HTTP 429）"
		case pe.Status >= 500:
			return fmt.Sprintf("服务商暂时不可用（HTTP %d）", pe.Status)
		case pe.Status == 0:
			return "无法连接服务商：请检查 Base URL 与网络"
		}
		return fmt.Sprintf("请求失败（HTTP %d）", pe.Status)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "连接超时"
	}
	return "请求失败"
}

var echoTool = harness.ToolSpec{Name: "echo", Description: "Echo the given text back.", Parameters: map[string]any{
	"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}, "required": []string{"text"}, "additionalProperties": false}}

// Test sends one tiny request (and one tool-calling probe) with the submitted parameters; nothing is saved.
func (m *Manager) Test(ctx context.Context, in TestInput) (TestResult, error) {
	base, key, err := m.resolveCall(in.BaseURL, in.APIKey)
	if err != nil {
		return TestResult{}, err
	}
	return m.runTest(ctx, m.http, base, key, in)
}

// runTest probes a normalised endpoint with the given HTTP client (global: the shared client; personal: the
// connect-time-checked one).
func (m *Manager) runTest(ctx context.Context, client *http.Client, base, key string, in TestInput) (TestResult, error) {
	if err := validModel(in.Model); err != nil {
		return TestResult{}, err
	}
	if vm := strings.TrimSpace(in.VisionModel); vm != "" {
		if err := validModel(vm); err != nil {
			return TestResult{}, bad("INVALID_VISION_MODEL", "视觉模型名称只能包含字母、数字及 . _ : / @ + -")
		}
	}
	prov, err := buildWith(client, base, strings.TrimSpace(in.Model), key, -1, 25*time.Second)
	if err != nil {
		return TestResult{}, bad("INVALID_BASE_URL", "Base URL 或模型无效")
	}
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	start := time.Now()
	_, err = prov.Chat(cctx, []harness.Message{{Role: harness.RoleSystem, Content: "Reply with the single word: ok"}, {Role: harness.RoleUser, Content: "ping"}}, nil)
	res := TestResult{LatencyMS: time.Since(start).Milliseconds()}
	if err != nil {
		res.Message = describe(err)
		if pe, ok := harness.AsProviderError(err); ok {
			res.Status, res.Retryable, res.RetryAfterSeconds = pe.Status, pe.Retryable, pe.RetryAfterSec
		} else if errors.Is(err, context.DeadlineExceeded) {
			res.Retryable = true
		}
		return res, nil
	}
	res.OK = true
	res.Message = "连接成功"
	resp, terr := prov.Chat(cctx, []harness.Message{{Role: harness.RoleSystem, Content: "You must call the echo tool with text \"ok\"."}, {Role: harness.RoleUser, Content: "call it now"}}, []harness.ToolSpec{echoTool})
	tc := terr == nil && len(resp.ToolCalls) > 0
	if terr == nil || errors.As(terr, new(*harness.ProviderError)) {
		res.ToolCalling = &tc
	}
	if res.ToolCalling != nil && !*res.ToolCalling {
		res.Message = "连接成功，但未检测到工具调用能力：识别流程依赖工具调用，效果可能受影响，建议换用支持函数调用的模型"
	}
	if vm := strings.TrimSpace(in.VisionModel); vm != "" {
		ok, msg := testVision(cctx, client, base, vm, key)
		res.VisionOK = &ok
		if !ok {
			res.Message += "；" + msg
		}
	}
	return res, nil
}

// Models lists model ids from the provider's /models endpoint.
func (m *Manager) Models(ctx context.Context, baseRaw, apiKey string) ([]string, error) {
	base, key, err := m.resolveCall(baseRaw, apiKey)
	if err != nil {
		return nil, err
	}
	return listModels(ctx, m.http, base, key)
}

func listModels(ctx context.Context, client *http.Client, base, key string) ([]string, error) {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return nil, bad("INVALID_BASE_URL", "Base URL 无效")
	}
	req.Header.Set("Accept", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, &UpstreamError{"无法连接服务商：请检查 Base URL 与网络"}
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, &UpstreamError{describe(&harness.ProviderError{Status: res.StatusCode})}
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if err != nil {
		return nil, &UpstreamError{"读取模型列表失败"}
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return nil, &UpstreamError{"该服务商的 /models 返回格式不是 OpenAI 兼容格式，请手动填写模型名称"}
	}
	seen := map[string]bool{}
	out := []string{}
	for _, d := range body.Data {
		if d.ID != "" && modelRe.MatchString(d.ID) && !seen[d.ID] {
			seen[d.ID] = true
			out = append(out, d.ID)
		}
	}
	sort.Strings(out)
	if len(out) > 500 {
		out = out[:500]
	}
	return out, nil
}
