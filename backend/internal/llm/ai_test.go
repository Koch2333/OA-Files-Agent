package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"oa-agent/backend/internal/harness"
	"oa-agent/backend/internal/store"
)

// aiMemStore is an in-memory AIStore (plus the global row) for unit tests.
type aiMemStore struct {
	memStore
	mu       sync.Mutex
	policy   store.AIPolicy
	personal map[string]Settings
}

func newAIStore() *aiMemStore {
	return &aiMemStore{policy: store.AIPolicy{AllowAutoArchive: true}, personal: map[string]Settings{}}
}

func (s *aiMemStore) LoadAIPolicy(context.Context) (store.AIPolicy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.policy, nil
}
func (s *aiMemStore) SaveAIPolicy(_ context.Context, p store.AIPolicy) (store.AIPolicy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	p.UpdatedAt = &now
	s.policy = p
	return p, nil
}
func (s *aiMemStore) LoadPersonalLLM(_ context.Context, id string) (Settings, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.personal[id]
	return v, ok, nil
}
func (s *aiMemStore) SavePersonalLLM(_ context.Context, id string, v Settings) (time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v.UpdatedAt = time.Now()
	s.personal[id] = v
	return v.UpdatedAt, nil
}
func (s *aiMemStore) DeletePersonalLLM(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.personal, id)
	return nil
}

func newAIMgr(t *testing.T) (*Manager, *aiMemStore) {
	t.Helper()
	st := newAIStore()
	m, err := New(context.Background(), st, newBox(t), Seed{})
	if err != nil {
		t.Fatal(err)
	}
	return m, st
}

var (
	admin = Subject{ID: "u-admin", Username: "root", Role: "admin"}
	opA   = Subject{ID: "u-a", Username: "alice", Role: "operator"}
	opB   = Subject{ID: "u-b", Username: "bob", Role: "operator"}
	view  = Subject{ID: "u-v", Username: "vic", Role: "viewer"}
)

func setPolicy(t *testing.T, m *Manager, personal, private, auto bool) {
	t.Helper()
	if _, err := m.UpdatePolicy(context.Background(), PolicyPatch{AllowPersonalLLM: &personal, AllowPersonalPrivateHosts: &private, AllowAutoArchive: &auto}); err != nil {
		t.Fatal(err)
	}
}

func saveGlobalLLM(t *testing.T, m *Manager, base, model, key string) {
	t.Helper()
	if _, err := m.Save(context.Background(), Input{Mode: ModeLLM, BaseURL: base, Model: model, APIKey: key}); err != nil {
		t.Fatal(err)
	}
}

func TestPolicyDefaultsAndPatch(t *testing.T) {
	m, _ := newAIMgr(t)
	p, err := m.Policy(context.Background())
	if err != nil || p.AllowPersonalLLM || p.AllowPersonalPrivateHosts || !p.AllowAutoArchive {
		t.Fatalf("defaults: %+v %v", p, err)
	}
	f := true
	p, err = m.UpdatePolicy(context.Background(), PolicyPatch{AllowPersonalLLM: &f})
	if err != nil || !p.AllowPersonalLLM || p.AllowPersonalPrivateHosts || !p.AllowAutoArchive || p.UpdatedAt == nil {
		t.Fatalf("patch must change only the given field: %+v %v", p, err)
	}
}

func TestPersonalBaseURLRules(t *testing.T) {
	m, _ := newAIMgr(t)
	ctx := context.Background()
	for _, raw := range []string{"http://api.example.com/v1", "https://127.0.0.1/v1", "https://10.1.2.3/v1", "https://192.168.0.5:8443/v1",
		"https://localhost/v1", "https://intranet/v1", "https://llm.corp/v1", "https://[::1]/v1", "https://100.64.0.1/v1", "http://10.0.0.1:8000/v1"} {
		if _, err := m.normalizePersonalBase(ctx, raw, false); err == nil {
			t.Errorf("%s must be refused while private hosts are not allowed", raw)
		}
	}
	if b, err := m.normalizePersonalBase(ctx, "https://8.8.8.8/v1/", false); err != nil || b != "https://8.8.8.8/v1" {
		t.Errorf("public https IP literal: %q %v", b, err)
	}
	// with the policy open, the global rules apply: http only for private/local, never link-local
	for _, raw := range []string{"http://10.0.0.1:8000/v1", "http://127.0.0.1:1/v1", "https://llm.corp/v1"} {
		if _, err := m.normalizePersonalBase(ctx, raw, true); err != nil {
			t.Errorf("%s must be allowed when private hosts are allowed: %v", raw, err)
		}
	}
	for _, raw := range []string{"http://169.254.169.254/latest", "https://169.254.169.254/v1", "https://[fe80::1]/v1", "http://api.example.com/v1", "https://0.0.0.0/v1"} {
		for _, allow := range []bool{false, true} {
			if _, err := m.normalizePersonalBase(ctx, raw, allow); err == nil {
				t.Errorf("%s must always be refused (allowPrivate=%v)", raw, allow)
			}
		}
	}
}

func TestNonPublicIP(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "10.0.0.1", "172.16.0.1", "192.168.1.1", "169.254.169.254", "100.64.0.1", "0.0.0.0", "::1", "fe80::1", "fd00::1", "::ffff:10.0.0.1", "224.0.0.1", "198.18.0.1", "240.0.0.1"} {
		if !NonPublicIP(netip.MustParseAddr(s)) {
			t.Errorf("%s must be non-public", s)
		}
	}
	for _, s := range []string{"8.8.8.8", "1.1.1.1", "93.184.216.34", "2606:4700:4700::1111"} {
		if NonPublicIP(netip.MustParseAddr(s)) {
			t.Errorf("%s is public", s)
		}
	}
}

func TestPersonalConfigGateKeyHandlingAndRoles(t *testing.T) {
	m, st := newAIMgr(t)
	ctx := context.Background()
	fake := newFake(t, "sk-personal-0001")
	base := fake.srv.URL + "/v1" // 127.0.0.1 over http
	in := Input{Mode: ModeLLM, BaseURL: base, Model: "qwen-plus", APIKey: "sk-personal-0001"}

	var pe *PolicyError
	if _, err := m.PersonalConfig(ctx, opA); !errors.As(err, &pe) || pe.Code != "PERSONAL_LLM_DISABLED" {
		t.Fatalf("policy off must refuse: %v", err)
	}
	setPolicy(t, m, true, false, true)
	if _, err := m.PersonalConfig(ctx, view); !errors.As(err, &pe) {
		t.Fatalf("a viewer never has personal configs: %v", err)
	}
	var ve *ValidationError
	if _, err := m.SavePersonal(ctx, opA, in); !errors.As(err, &ve) || ve.Code != "INVALID_BASE_URL" {
		t.Fatalf("a private host must be refused by default: %v", err)
	}
	setPolicy(t, m, true, true, true)
	v, err := m.SavePersonal(ctx, opA, in)
	if err != nil || !v.APIKeySet || v.APIKeyHint != "••••0001" || v.State != "ok" {
		t.Fatalf("save: %+v %v", v, err)
	}
	if raw := st.personal[opA.ID]; len(raw.APIKeyEnc) == 0 || strings.Contains(string(raw.APIKeyEnc), "sk-personal") {
		t.Fatal("the personal key must be stored encrypted")
	}
	// an empty api_key keeps the saved key while the base URL is unchanged; another URL needs a new key
	if v, err = m.SavePersonal(ctx, opA, Input{Mode: ModeLLM, BaseURL: base, Model: "deepseek-chat"}); err != nil || !v.APIKeySet || v.Model != "deepseek-chat" {
		t.Fatalf("keep key: %+v %v", v, err)
	}
	if _, err = m.SavePersonal(ctx, opA, Input{Mode: ModeLLM, BaseURL: "http://127.0.0.1:9/v1", Model: "m", PresetID: "openai"}); !errors.As(err, &ve) || ve.Code != "KEY_REQUIRED" {
		t.Fatalf("key must not follow to another host: %v", err)
	}
	// configurations are per user
	if other, _ := m.PersonalConfig(ctx, opB); other.APIKeySet || other.State != "unconfigured" {
		t.Fatalf("bob must not see alice's config: %+v", other)
	}
	// test + models work through the personal client
	res, err := m.TestPersonal(ctx, opA, TestInput{BaseURL: base, Model: "qwen-plus"})
	if err != nil || !res.OK {
		t.Fatalf("personal test: %+v %v", res, err)
	}
	if models, err := m.ModelsPersonal(ctx, opA, base, ""); err != nil || len(models) != 2 {
		t.Fatalf("personal models: %v %v", models, err)
	}
	if err := m.DeletePersonal(ctx, opA); err != nil {
		t.Fatal(err)
	}
	if v, _ := m.PersonalConfig(ctx, opA); v.APIKeySet {
		t.Fatal("deleted")
	}
}

func TestPersonalClientRefusesPrivateAndMetadataAddressesAtConnectTime(t *testing.T) {
	m, _ := newAIMgr(t)
	fake := newFake(t, "")
	ctx := context.Background()
	client := m.personalHTTP()
	m.allowPrivate.Store(false)
	// a host NAME that resolves to loopback passes validation-by-name tricks but dies at dial time (DNS rebinding safe)
	local := strings.Replace(fake.srv.URL, "127.0.0.1", "localhost", 1)
	if _, err := listModels(ctx, client, local+"/v1", ""); err == nil {
		t.Fatal("dial to loopback must be refused while private hosts are not allowed")
	}
	if fake.calls.Load() != 0 {
		t.Fatal("nothing may have reached the server")
	}
	if _, err := listModels(ctx, client, "http://169.254.169.254/v1", ""); err == nil {
		t.Fatal("metadata address must be refused")
	}
	m.allowPrivate.Store(true)
	if _, err := listModels(ctx, client, fake.srv.URL+"/v1", ""); err != nil {
		t.Fatalf("loopback allowed by policy: %v", err)
	}
	if _, err := listModels(ctx, client, "http://169.254.169.254/v1", ""); err == nil {
		t.Fatal("metadata address must stay refused even when private hosts are allowed")
	}
}

func TestPersonalClientDoesNotFollowRedirects(t *testing.T) {
	m, _ := newAIMgr(t)
	m.allowPrivate.Store(true)
	target := newFake(t, "")
	redir := httpTestRedirect(t, target.srv.URL+"/v1/models")
	if _, err := listModels(context.Background(), m.personalHTTP(), redir+"/v1", ""); err == nil {
		t.Fatal("a redirect answer must be an error, not followed")
	}
	if target.calls.Load() != 0 {
		t.Fatal("redirect target was contacted")
	}
}

func TestResolveAndStatusMatrix(t *testing.T) {
	m, _ := newAIMgr(t)
	ctx := context.Background()
	gfake := newFake(t, "sk-global-00001")
	pfake := newFake(t, "sk-personal-0001")

	// nothing configured
	st, err := m.UserStatus(ctx, opA)
	if err != nil || st.Configured || st.Source != "none" || st.Mode != "heuristic" || st.SetupHint != "contact_admin" || st.AutoArchiveAllowed || st.AutoAckVersion != AutoAckVersion || st.AutoAckText == "" {
		t.Fatalf("fresh install, operator: %+v %v", st, err)
	}
	if st, _ = m.UserStatus(ctx, admin); st.SetupHint != "admin_global" {
		t.Fatalf("admin hint: %+v", st)
	}
	setPolicy(t, m, true, true, true)
	if st, _ = m.UserStatus(ctx, opA); st.SetupHint != "personal_or_admin" || !st.PersonalAllowed {
		t.Fatalf("operator hint with personal allowed: %+v", st)
	}
	if st, _ = m.UserStatus(ctx, view); st.SetupHint != "contact_admin" || st.PersonalAllowed {
		t.Fatalf("viewer: %+v", st)
	}

	// global engine only
	saveGlobalLLM(t, m, gfake.srv.URL+"/v1", "g-model", "sk-global-00001")
	st, _ = m.UserStatus(ctx, opA)
	if !st.Configured || st.Source != "global" || st.Model != "g-model" || !st.GlobalConfigured || st.PersonalConfigured || st.SetupHint != "none" || !st.AutoArchiveAllowed {
		t.Fatalf("global: %+v", st)
	}
	if strings.Contains(st.ProviderLabel, "127.0.0.1") || strings.Contains(st.ProviderLabel, "http") {
		t.Fatalf("provider label must be a name only: %q", st.ProviderLabel)
	}
	if st, _ = m.UserStatus(ctx, view); st.AutoArchiveAllowed {
		t.Fatal("viewers can never use automatic archiving")
	}

	// personal config wins for its owner while the policy allows it
	if _, err := m.SavePersonal(ctx, opA, Input{Mode: ModeLLM, BaseURL: pfake.srv.URL + "/v1", Model: "p-model", APIKey: "sk-personal-0001"}); err != nil {
		t.Fatal(err)
	}
	st, _ = m.UserStatus(ctx, opA)
	if st.Source != "personal" || st.Model != "p-model" || !st.PersonalConfigured {
		t.Fatalf("personal first: %+v", st)
	}
	if st, _ = m.UserStatus(ctx, opB); st.Source != "global" {
		t.Fatalf("bob has no personal config: %+v", st)
	}
	// policy off: personal configs stay but stop being used
	setPolicy(t, m, false, true, true)
	st, _ = m.UserStatus(ctx, opA)
	if st.Source != "global" || st.PersonalAllowed || !st.PersonalConfigured {
		t.Fatalf("policy off must fall back to global: %+v", st)
	}
	// explicit sources
	var pe *PolicyError
	if _, err := m.Resolve(ctx, opA, "personal"); !errors.As(err, &pe) || pe.Code != "PERSONAL_LLM_DISABLED" {
		t.Fatalf("explicit personal with policy off: %v", err)
	}
	setPolicy(t, m, true, true, true)
	if r, err := m.Resolve(ctx, opA, "personal"); err != nil || r.Source != "personal" {
		t.Fatalf("explicit personal: %+v %v", r, err)
	}
	var ve *ValidationError
	if _, err := m.Resolve(ctx, opB, "personal"); !errors.As(err, &ve) || ve.Code != "LLM_REQUIRED" {
		t.Fatalf("personal without config: %v", err)
	}
	if r, _ := m.Resolve(ctx, opA, "global"); r.Source != "global" {
		t.Fatalf("explicit global: %+v", r)
	}
	// auto archive switched off by the administrator
	setPolicy(t, m, true, true, false)
	if st, _ = m.UserStatus(ctx, opA); st.AutoArchiveAllowed {
		t.Fatal("policy off must disable automatic archiving in the status")
	}
	// a global engine in heuristic mode is not "configured"
	if _, err := m.Save(ctx, Input{Mode: ModeHeuristic}); err != nil {
		t.Fatal(err)
	}
	if r, _ := m.Resolve(ctx, opB, "auto"); r.Source != "none" {
		t.Fatalf("heuristic global: %+v", r)
	}
}

// TestEachTaskUsesItsOwnConfiguration is the isolation guarantee: two users with different personal configs plus the
// global engine, all running at the same instant, each talk only to their own server.
func TestEachTaskUsesItsOwnConfiguration(t *testing.T) {
	m, _ := newAIMgr(t)
	ctx := context.Background()
	gf, af, bf := newFake(t, "sk-global-00001"), newFake(t, "sk-alice-000001"), newFake(t, "sk-bob-00000001")
	setPolicy(t, m, true, true, true)
	saveGlobalLLM(t, m, gf.srv.URL+"/v1", "g-model", "sk-global-00001")
	if _, err := m.SavePersonal(ctx, opA, Input{Mode: ModeLLM, BaseURL: af.srv.URL + "/v1", Model: "alice-model", APIKey: "sk-alice-000001"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SavePersonal(ctx, opB, Input{Mode: ModeLLM, BaseURL: bf.srv.URL + "/v1", Model: "bob-model", APIKey: "sk-bob-00000001"}); err != nil {
		t.Fatal(err)
	}
	type job struct{ source, owner, model string }
	jobs := []job{{"personal", opA.ID, "alice-model"}, {"personal", opB.ID, "bob-model"}, {"global", opA.ID, "g-model"}, {"personal", opA.ID, "alice-model"}}
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, len(jobs))
	for _, j := range jobs {
		wg.Add(1)
		go func(j job) {
			defer wg.Done()
			tm, err := m.TaskProvider(ctx, j.source, j.owner)
			if err != nil {
				errs <- err
				return
			}
			if tm.Model != j.model {
				errs <- errors.New("wrong model " + tm.Model)
				return
			}
			<-start
			_, err = tm.Provider.Chat(ctx, []harness.Message{{Role: harness.RoleUser, Content: "hi"}}, nil)
			errs <- err
		}(j)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if af.calls.Load() != 2 || bf.calls.Load() != 1 || gf.calls.Load() != 1 {
		t.Fatalf("each task must hit only its own server: alice=%d bob=%d global=%d", af.calls.Load(), bf.calls.Load(), gf.calls.Load())
	}
	if af.lastModel.Load() != "alice-model" || bf.lastModel.Load() != "bob-model" || gf.lastModel.Load() != "g-model" {
		t.Fatal("each server must see its own model name")
	}
	// the global hot-swap keeps working for global tasks
	gf2 := newFake(t, "sk-global-00002")
	saveGlobalLLM(t, m, gf2.srv.URL+"/v1", "g2-model", "sk-global-00002")
	tm, err := m.TaskProvider(ctx, "global", opA.ID)
	if err != nil || tm.Model != "g2-model" {
		t.Fatalf("global follows the hot-swap: %+v %v", tm, err)
	}
	if _, err = tm.Provider.Chat(ctx, []harness.Message{{Role: harness.RoleUser, Content: "hi"}}, nil); err != nil || gf2.calls.Load() != 1 {
		t.Fatalf("hot-swapped global: %v %d", err, gf2.calls.Load())
	}
}

func TestTaskProviderFailsInsteadOfFallingBack(t *testing.T) {
	m, _ := newAIMgr(t)
	ctx := context.Background()
	pf := newFake(t, "sk-alice-000001")
	setPolicy(t, m, true, true, true)
	var tme *TaskModelError
	// none is always the offline engine
	if tm, err := m.TaskProvider(ctx, "none", ""); err != nil || tm.Provider == nil {
		t.Fatalf("none: %v", err)
	}
	// global source while the global engine is offline
	if _, err := m.TaskProvider(ctx, "global", opA.ID); !errors.As(err, &tme) {
		t.Fatalf("global without an engine must fail: %v", err)
	}
	// personal: deleted
	if _, err := m.TaskProvider(ctx, "personal", opA.ID); !errors.As(err, &tme) || !strings.Contains(tme.Msg, "删除") {
		t.Fatalf("missing personal config must fail clearly: %v", err)
	}
	if _, err := m.SavePersonal(ctx, opA, Input{Mode: ModeLLM, BaseURL: pf.srv.URL + "/v1", Model: "alice-model", APIKey: "sk-alice-000001"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.TaskProvider(ctx, "personal", opA.ID); err != nil {
		t.Fatal(err)
	}
	// personal: policy switched off
	setPolicy(t, m, false, true, true)
	if _, err := m.TaskProvider(ctx, "personal", opA.ID); !errors.As(err, &tme) {
		t.Fatalf("policy off must stop personal tasks: %v", err)
	}
	setPolicy(t, m, true, true, true)
	// personal: switched to the offline mode
	if _, err := m.SavePersonal(ctx, opA, Input{Mode: ModeHeuristic}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.TaskProvider(ctx, "personal", opA.ID); !errors.As(err, &tme) {
		t.Fatalf("a non-LLM personal config must fail: %v", err)
	}
	if pf.calls.Load() != 0 {
		t.Fatal("no model call may have happened")
	}
}

func TestPersonalProviderRefusesPrivateHostWhenPolicyTightenedAfterSaving(t *testing.T) {
	m, _ := newAIMgr(t)
	ctx := context.Background()
	pf := newFake(t, "sk-alice-000001")
	setPolicy(t, m, true, true, true)
	if _, err := m.SavePersonal(ctx, opA, Input{Mode: ModeLLM, BaseURL: pf.srv.URL + "/v1", Model: "alice-model", APIKey: "sk-alice-000001"}); err != nil {
		t.Fatal(err)
	}
	setPolicy(t, m, true, false, true) // the administrator closes private hosts later
	tm, err := m.TaskProvider(ctx, "personal", opA.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tm.Provider.Chat(ctx, []harness.Message{{Role: harness.RoleUser, Content: "hi"}}, nil); err == nil || pf.calls.Load() != 0 {
		t.Fatalf("the dial-time check must stop the call: err=%v calls=%d", err, pf.calls.Load())
	}
}

func TestAutoAckTextStatesTheRisksPlainly(t *testing.T) {
	for _, must := range []string{"大语言模型", "不是由人工", "可能出错", "承担责任", "不会被删除", "撤销", "不会被自动归档"} {
		if !strings.Contains(AutoAckText, must) {
			t.Errorf("risk text must contain %q", must)
		}
	}
	if AutoAckVersion == "" {
		t.Fatal("version")
	}
}

var _ = http.StatusOK

func httpTestRedirect(t *testing.T, to string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, to, http.StatusFound) }))
	t.Cleanup(srv.Close)
	return srv.URL
}
