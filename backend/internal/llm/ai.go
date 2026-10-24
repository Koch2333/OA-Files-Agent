package llm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"

	"oa-agent/backend/internal/cloud"
	"oa-agent/backend/internal/harness"
	"oa-agent/backend/internal/imports"
	"oa-agent/backend/internal/store"
)

// AutoAckVersion identifies the wording of AutoAckText. Change both together: a task can only be created in fully
// automatic mode with an acknowledgement of exactly this version.
const AutoAckVersion = "2026-10-v1"

// AutoAckText is the legally relevant risk notice for fully automatic archiving. The server owns the wording; the
// client must show it verbatim and the user must tick the box before the request is accepted.
const AutoAckText = `【全自动归档风险提示】
1. 全自动模式下，文件所属部门、文件类型、文件日期、规范文件名和归档位置，全部由大语言模型（AI）判断并直接执行，不是由人工判断，也不经过人工审批。
2. 大语言模型可能出错：它可能误读文档、把文件归入错误的部门、类型、日期或目录，且这类错误不一定能被系统发现。
3. 由此产生的后果，由贵单位及本次操作人承担责任。
4. 原始文件不会被删除；每一次自动归档操作都记录在“操作历史”中，可随时恢复（撤销）。
5. 模型无法确定的文件（信息不足、字段缺失、与已归档文件内容重复等）不会被自动归档，会保留在待审核的计划中，等待人工处理。
勾选“我已阅读并理解上述风险，愿意承担相应责任”后，才能使用全自动模式。`

// AIStore is the persistence the AI features need beyond the global configuration (implemented by *store.Store).
// A Store without it simply has no personal configurations and the default policy.
type AIStore interface {
	LoadAIPolicy(context.Context) (store.AIPolicy, error)
	SaveAIPolicy(context.Context, store.AIPolicy) (store.AIPolicy, error)
	LoadPersonalLLM(ctx context.Context, userID string) (Settings, bool, error)
	SavePersonalLLM(ctx context.Context, userID string, s Settings) (time.Time, error)
	DeletePersonalLLM(ctx context.Context, userID string) error
}

// Policy is the administrator policy (see store.AIPolicy).
type Policy = store.AIPolicy

// PolicyPatch changes policy fields; nil leaves a field unchanged.
type PolicyPatch struct {
	AllowPersonalLLM          *bool `json:"allow_personal_llm"`
	AllowPersonalPrivateHosts *bool `json:"allow_personal_private_hosts"`
	AllowAutoArchive          *bool `json:"allow_auto_archive"`
}

// Subject is the signed-in user a request acts for.
type Subject struct{ ID, Username, Role string }

func (s Subject) operatorPlus() bool { return s.Role == "operator" || s.Role == "admin" }

// PolicyError is a refusal by the administrator policy (HTTP 403).
type PolicyError struct{ Code, Msg string }

func (e *PolicyError) Error() string { return e.Msg }

// TaskModelError means the model a task was bound to cannot be used any more (personal key deleted, policy switched
// off, global engine disabled). The pipeline fails the file with Msg; it never silently falls back to another model.
type TaskModelError struct{ Msg string }

func (e *TaskModelError) Error() string { return e.Msg }

// Resolution is the outcome of choosing a model for a user.
type Resolution struct {
	Source        string // none | global | personal
	Mode          string // heuristic | llm
	Model         string
	ProviderLabel string
}

// UserStatus is GET /v1/llm/status.
type UserStatus struct {
	Configured         bool   `json:"configured"`
	Source             string `json:"source"`
	Mode               string `json:"mode"`
	Model              string `json:"model"`
	ProviderLabel      string `json:"provider_label"`
	GlobalConfigured   bool   `json:"global_configured"`
	PersonalConfigured bool   `json:"personal_configured"`
	PersonalAllowed    bool   `json:"personal_allowed"`
	AutoArchiveAllowed bool   `json:"auto_archive_allowed"`
	AllowPrivateHosts  bool   `json:"allow_personal_private_hosts"`
	AutoAckVersion     string `json:"auto_ack_version"`
	AutoAckText        string `json:"auto_ack_text"`
	SetupHint          string `json:"setup_hint"`
}

// TaskModel is the model a task runs with.
type TaskModel struct {
	Provider harness.Provider
	// Vision is non-nil only for personal configurations (they bring their own vision model; it may return nil).
	Vision func() harness.VisionTranscriber
	Model  string
}

// personalLimiterSeed keeps the personal HTTP client (one per manager) lazily built.
type personalState struct {
	once   sync.Once
	client *http.Client
}

func (m *Manager) personalHTTP() *http.Client {
	m.pers.once.Do(func() {
		d := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip, err := netip.ParseAddr(host)
			if err != nil {
				return err
			}
			ip = ip.Unmap()
			// link-local (cloud metadata), unspecified and multicast: always; private/loopback: unless the policy allows it
			if cloud.BlockedIP(ip) || (!m.allowPrivate.Load() && NonPublicIP(ip)) {
				return fmt.Errorf("address %s is not allowed", ip)
			}
			return nil
		}}
		tr := &http.Transport{Proxy: nil, DialContext: d.DialContext, TLSHandshakeTimeout: 10 * time.Second, IdleConnTimeout: 60 * time.Second, MaxIdleConns: 20, ForceAttemptHTTP2: true}
		m.pers.client = &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	})
	return m.pers.client
}

var nonPublicPrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, p := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "64:ff9b::/96", "2001:db8::/32"} {
		out = append(out, netip.MustParsePrefix(p))
	}
	return out
}()

// NonPublicIP reports addresses that are not routable on the public internet (loopback, private, link-local, CGNAT,
// documentation and reserved ranges, unspecified, multicast).
func NonPublicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsInterfaceLocalMulticast() {
		return true
	}
	for _, p := range nonPublicPrefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// normalizePersonalBase applies the personal Base URL rules: the global rules, and unless the administrator allows
// private hosts, https to a public host only. Host names are resolved once here (best effort); the dial-time check
// in personalHTTP is what actually enforces the rule.
func (m *Manager) normalizePersonalBase(ctx context.Context, raw string, allowPrivate bool) (string, error) {
	base, err := NormalizeBaseURL(raw)
	if err != nil {
		return "", err
	}
	if allowPrivate {
		return base, nil
	}
	u, _ := url.Parse(base)
	deny := func() error {
		return bad("INVALID_BASE_URL", "个人 API 必须使用 https 公网地址（管理员未开放内网/本机地址）")
	}
	if u.Scheme != "https" {
		return "", deny()
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if ip, perr := netip.ParseAddr(host); perr == nil {
		if NonPublicIP(ip) {
			return "", deny()
		}
		return base, nil
	}
	if host == "localhost" || !strings.Contains(host, ".") {
		return "", deny()
	}
	for _, suf := range []string{".localhost", ".local", ".internal", ".lan", ".localdomain", ".home", ".corp", ".intranet", ".home.arpa"} {
		if strings.HasSuffix(host, suf) {
			return "", deny()
		}
	}
	lctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if addrs, lerr := net.DefaultResolver.LookupNetIP(lctx, "ip", host); lerr == nil {
		for _, a := range addrs {
			if NonPublicIP(a) {
				return "", deny()
			}
		}
	}
	return base, nil
}

// ---- policy ----

// Policy returns the administrator policy (defaults when the store has none).
func (m *Manager) Policy(ctx context.Context) (Policy, error) {
	if m.ai == nil {
		return Policy{AllowAutoArchive: true}, nil
	}
	p, err := m.ai.LoadAIPolicy(ctx)
	if err == nil {
		m.allowPrivate.Store(p.AllowPersonalPrivateHosts)
	}
	return p, err
}

// UpdatePolicy applies a patch and returns the new policy.
func (m *Manager) UpdatePolicy(ctx context.Context, patch PolicyPatch) (Policy, error) {
	cur, err := m.Policy(ctx)
	if err != nil {
		return cur, err
	}
	if m.ai == nil {
		return cur, errors.New("policy store not available")
	}
	if patch.AllowPersonalLLM != nil {
		cur.AllowPersonalLLM = *patch.AllowPersonalLLM
	}
	if patch.AllowPersonalPrivateHosts != nil {
		cur.AllowPersonalPrivateHosts = *patch.AllowPersonalPrivateHosts
	}
	if patch.AllowAutoArchive != nil {
		cur.AllowAutoArchive = *patch.AllowAutoArchive
	}
	p, err := m.ai.SaveAIPolicy(ctx, cur)
	if err == nil {
		m.allowPrivate.Store(p.AllowPersonalPrivateHosts)
	}
	return p, err
}

// ---- resolution ----

func (m *Manager) presetLabel(presetID string) string {
	if p, ok := presetByID(presetID); ok && p.Name != "" {
		return p.Name
	}
	return "自定义（OpenAI 兼容）"
}

// globalState reports whether the global engine is a usable LLM and which one.
func (m *Manager) globalState() (usable bool, model, label string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.set.Mode == ModeLLM && m.state == "ok" {
		return true, m.set.Model, m.presetLabel(m.set.PresetID)
	}
	return false, "", ""
}

// personalLoaded is a user's stored personal configuration with its decrypted key (memory only).
type personalLoaded struct {
	set    Settings
	key    string
	src    string // manual | oauth
	prov   string
	state  string // ok | unconfigured | unavailable
	exists bool
}

func (p personalLoaded) usable() bool { return p.exists && p.set.Mode == ModeLLM && p.state == "ok" }

func (m *Manager) loadPersonal(ctx context.Context, userID string) (personalLoaded, error) {
	if m.ai == nil || userID == "" {
		return personalLoaded{state: "unconfigured"}, nil
	}
	s, ok, err := m.ai.LoadPersonalLLM(ctx, userID)
	if err != nil {
		return personalLoaded{}, err
	}
	if !ok {
		return personalLoaded{state: "unconfigured"}, nil
	}
	p := personalLoaded{set: s, exists: true, state: "ok"}
	if len(s.APIKeyEnc) > 0 {
		if k, uerr := m.box.Unseal(s.APIKeyEnc); uerr == nil {
			p.key, p.src, p.prov = splitKey(string(k))
		} else {
			p.state = "unavailable"
		}
	}
	if s.Mode == ModeLLM && p.state == "ok" {
		if pr, known := presetByID(s.PresetID); p.key == "" && known && pr.NeedsAPIKey {
			p.state = "unconfigured"
		}
	}
	return p, nil
}

// Resolve chooses the model for sub's next task. requested is auto|global|personal (empty = auto).
func (m *Manager) Resolve(ctx context.Context, sub Subject, requested string) (Resolution, error) {
	pol, err := m.Policy(ctx)
	if err != nil {
		return Resolution{}, err
	}
	pers, err := m.loadPersonal(ctx, sub.ID)
	if err != nil {
		return Resolution{}, err
	}
	allowed := pol.AllowPersonalLLM && sub.operatorPlus()
	gOK, gModel, gLabel := m.globalState()
	personal := Resolution{Source: imports.SourcePersonal, Mode: ModeLLM, Model: pers.set.Model, ProviderLabel: m.presetLabel(pers.set.PresetID)}
	global := Resolution{Source: imports.SourceGlobal, Mode: ModeLLM, Model: gModel, ProviderLabel: gLabel}
	none := Resolution{Source: imports.SourceNone, Mode: ModeHeuristic, ProviderLabel: "离线规则"}
	switch requested {
	case "", "auto":
		switch {
		case allowed && pers.usable():
			return personal, nil
		case gOK:
			return global, nil
		}
		return none, nil
	case imports.SourceGlobal:
		if gOK {
			return global, nil
		}
		return none, nil
	case imports.SourcePersonal:
		if !allowed {
			return Resolution{}, &PolicyError{"PERSONAL_LLM_DISABLED", "管理员未开放个人大模型配置"}
		}
		if !pers.usable() {
			return Resolution{}, bad("LLM_REQUIRED", "您还没有配置可用的个人大模型")
		}
		return personal, nil
	}
	return Resolution{}, bad("INVALID_LLM_SOURCE", "llm_source 取值无效")
}

// UserStatus computes GET /v1/llm/status for sub. It never contains keys or base URLs.
func (m *Manager) UserStatus(ctx context.Context, sub Subject) (UserStatus, error) {
	pol, err := m.Policy(ctx)
	if err != nil {
		return UserStatus{}, err
	}
	res, err := m.Resolve(ctx, sub, "auto")
	if err != nil {
		return UserStatus{}, err
	}
	pers, err := m.loadPersonal(ctx, sub.ID)
	if err != nil {
		return UserStatus{}, err
	}
	gOK, _, _ := m.globalState()
	st := UserStatus{Configured: res.Source != imports.SourceNone, Source: res.Source, Mode: res.Mode, Model: res.Model, ProviderLabel: res.ProviderLabel,
		GlobalConfigured: gOK, PersonalConfigured: pers.usable(), PersonalAllowed: pol.AllowPersonalLLM && sub.operatorPlus(),
		AllowPrivateHosts: pol.AllowPersonalPrivateHosts, AutoAckVersion: AutoAckVersion, AutoAckText: AutoAckText}
	st.AutoArchiveAllowed = pol.AllowAutoArchive && st.Configured && sub.operatorPlus()
	switch {
	case st.Configured:
		st.SetupHint = "none"
	case sub.Role == "admin":
		st.SetupHint = "admin_global"
	case st.PersonalAllowed:
		st.SetupHint = "personal_or_admin"
	default:
		st.SetupHint = "contact_admin"
	}
	return st, nil
}

// TaskProvider returns the model a task must run with, from the columns stored on the task (background workers have
// no HTTP user). A personal configuration is rebuilt from the owner's stored row for every job, so it always follows
// that user's latest saved version; the global engine keeps following hot-swaps. There is no fallback between sources.
func (m *Manager) TaskProvider(ctx context.Context, source, owner string) (TaskModel, error) {
	switch source {
	case imports.SourceNone, "":
		return TaskModel{Provider: harness.Heuristic{}}, nil
	case imports.SourceGlobal:
		ok, model, _ := m.globalState()
		if !ok {
			return TaskModel{}, &TaskModelError{"全局大模型当前未启用或不可用，任务无法使用其创建时选定的模型"}
		}
		return TaskModel{Provider: m.sw, Model: model}, nil
	case imports.SourcePersonal:
		pol, err := m.Policy(ctx)
		if err != nil {
			return TaskModel{}, err
		}
		if !pol.AllowPersonalLLM {
			return TaskModel{}, &TaskModelError{"管理员已关闭个人大模型配置，任务无法使用创建者的个人配置"}
		}
		p, err := m.loadPersonal(ctx, owner)
		if err != nil {
			return TaskModel{}, err
		}
		if !p.exists {
			return TaskModel{}, &TaskModelError{"创建者的个人大模型配置已被删除，任务无法继续"}
		}
		if !p.usable() {
			return TaskModel{}, &TaskModelError{"创建者的个人大模型配置不可用（模式、密钥或解密状态异常），任务无法继续"}
		}
		client := m.personalHTTP()
		prov, err := buildWith(client, p.set.BaseURL, p.set.Model, p.key, 0, m.Timeout)
		if err != nil {
			return TaskModel{}, &TaskModelError{"创建者的个人大模型配置无效，任务无法继续"}
		}
		tm := TaskModel{Provider: prov, Model: p.set.Model}
		set, key := p.set, p.key
		tm.Vision = func() harness.VisionTranscriber {
			if !set.AllowVision || set.VisionModel == "" {
				return nil
			}
			return newTranscriber(client, set.BaseURL, set.VisionModel, key, 60*time.Second)
		}
		return tm, nil
	}
	return TaskModel{}, &TaskModelError{"任务的模型来源无效"}
}

// ---- personal configuration ----

func (m *Manager) personalGate(ctx context.Context, sub Subject) (Policy, error) {
	pol, err := m.Policy(ctx)
	if err != nil {
		return pol, err
	}
	if m.ai == nil || !pol.AllowPersonalLLM || !sub.operatorPlus() || sub.ID == "" {
		return pol, &PolicyError{"PERSONAL_LLM_DISABLED", "管理员未开放个人大模型配置"}
	}
	return pol, nil
}

func personalView(p personalLoaded) ConfigView {
	if !p.exists {
		return ConfigView{Mode: ModeHeuristic, PresetID: "deepseek", State: "unconfigured"}
	}
	v := ConfigView{Mode: p.set.Mode, PresetID: p.set.PresetID, BaseURL: p.set.BaseURL, Model: p.set.Model, VisionModel: p.set.VisionModel, AllowVision: p.set.AllowVision, APIKeySet: len(p.set.APIKeyEnc) > 0, State: p.state}
	if p.key != "" {
		v.APIKeyHint = KeyHint(p.key)
		v.KeySource, v.KeyProvider = p.src, p.prov
	} else if v.APIKeySet {
		v.APIKeyHint = "••••"
	}
	if !p.set.UpdatedAt.IsZero() {
		t := p.set.UpdatedAt
		v.UpdatedAt = &t
	}
	return v
}

// PersonalConfig returns the user's own configuration (never the key).
func (m *Manager) PersonalConfig(ctx context.Context, sub Subject) (ConfigView, error) {
	if _, err := m.personalGate(ctx, sub); err != nil {
		return ConfigView{}, err
	}
	p, err := m.loadPersonal(ctx, sub.ID)
	if err != nil {
		return ConfigView{}, err
	}
	return personalView(p), nil
}

// SavePersonal validates and stores the user's configuration (API key encrypted).
func (m *Manager) SavePersonal(ctx context.Context, sub Subject, in Input) (ConfigView, error) {
	pol, err := m.personalGate(ctx, sub)
	if err != nil {
		return ConfigView{}, err
	}
	m.pmu.Lock()
	defer m.pmu.Unlock()
	cur, err := m.loadPersonal(ctx, sub.ID)
	if err != nil {
		return ConfigView{}, err
	}
	norm := func(raw string) (string, error) {
		return m.normalizePersonalBase(ctx, raw, pol.AllowPersonalPrivateHosts)
	}
	next, err := m.buildSettings(in, cur.set, cur.key, norm)
	if err != nil {
		return ConfigView{}, err
	}
	if _, err := m.ai.SavePersonalLLM(ctx, sub.ID, next); err != nil {
		return ConfigView{}, err
	}
	p, err := m.loadPersonal(ctx, sub.ID)
	if err != nil {
		return ConfigView{}, err
	}
	return personalView(p), nil
}

// DeletePersonal removes the user's configuration (also allowed while the policy is off: nobody should be unable to
// delete their own stored key). Tasks that were bound to it fail with a clear message.
func (m *Manager) DeletePersonal(ctx context.Context, sub Subject) error {
	if m.ai == nil || sub.ID == "" {
		return &PolicyError{"PERSONAL_LLM_DISABLED", "管理员未开放个人大模型配置"}
	}
	return m.ai.DeletePersonalLLM(ctx, sub.ID)
}

func (m *Manager) personalCall(ctx context.Context, sub Subject, baseRaw, explicitKey string) (base, key string, err error) {
	pol, err := m.personalGate(ctx, sub)
	if err != nil {
		return "", "", err
	}
	base, err = m.normalizePersonalBase(ctx, baseRaw, pol.AllowPersonalPrivateHosts)
	if err != nil {
		return "", "", err
	}
	explicitKey = strings.TrimSpace(explicitKey)
	if explicitKey != "" {
		if err = validKey(explicitKey); err != nil {
			return "", "", err
		}
	}
	cur, err := m.loadPersonal(ctx, sub.ID)
	if err != nil {
		return "", "", err
	}
	return base, keyForSaved(cur.set, cur.key, base, explicitKey), nil
}

// TestPersonal probes a personal endpoint with the connect-time-checked client; nothing is saved.
func (m *Manager) TestPersonal(ctx context.Context, sub Subject, in TestInput) (TestResult, error) {
	base, key, err := m.personalCall(ctx, sub, in.BaseURL, in.APIKey)
	if err != nil {
		return TestResult{}, err
	}
	return m.runTest(ctx, m.personalHTTP(), base, key, in)
}

// ModelsPersonal lists models of a personal endpoint.
func (m *Manager) ModelsPersonal(ctx context.Context, sub Subject, baseRaw, apiKey string) ([]string, error) {
	base, key, err := m.personalCall(ctx, sub, baseRaw, apiKey)
	if err != nil {
		return nil, err
	}
	return listModels(ctx, m.personalHTTP(), base, key)
}

var _ AIStore = (*store.Store)(nil)
