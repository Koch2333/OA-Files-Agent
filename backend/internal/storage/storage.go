// Package storage is the service layer of the optional storage connections: connection management with encrypted
// credentials, connection tests (incl. SSH host-key trust on first use), remote browsing, import from remote and
// replication of archived files (add-only: never overwrite, never delete).
package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"path"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"oa-agent/backend/internal/cloud"
	"oa-agent/backend/internal/fsman"
	"oa-agent/backend/internal/imports"
	"oa-agent/backend/internal/secrets"
	"oa-agent/backend/internal/store"
)

type Config struct {
	Store   *store.Store
	Box     *secrets.Box
	Imports *imports.Service
	Archive *fsman.Manager // the local archive root (source of replication)
	Workers int
}

type Service struct {
	cfg   Config
	mu    sync.Mutex
	cache map[string]*cachedProv // by connection id
	connM map[string]*sync.Mutex // one active transfer per connection
}

type cachedProv struct {
	p       cloud.Provider
	updated time.Time // connection.updated_at the provider was built from
	last    time.Time
}

func New(cfg Config) *Service {
	if cfg.Workers < 1 {
		cfg.Workers = 2
	}
	return &Service{cfg: cfg, cache: map[string]*cachedProv{}, connM: map[string]*sync.Mutex{}}
}

// ValidationError is a user-correctable problem (HTTP 422).
type ValidationError struct{ Code, Msg string }

func (e *ValidationError) Error() string { return e.Msg }
func bad(code, f string, a ...any) error { return &ValidationError{code, fmt.Sprintf(f, a...)} }

// UpstreamError is a failure reaching the remote storage (HTTP 502), worded for users.
type UpstreamError struct{ Msg string }

func (e *UpstreamError) Error() string { return e.Msg }

var ErrNameTaken = errors.New("name already used")

// ---- views ----

type View struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Kind         string            `json:"kind"`
	Enabled      bool              `json:"enabled"`
	BasePath     string            `json:"base_path"`
	UseForImport bool              `json:"use_for_import"`
	Replicate    string            `json:"replicate"`
	Settings     map[string]string `json:"settings"`
	SecretsSet   map[string]bool   `json:"secrets_set"`
	LastTest     *store.LastTest   `json:"last_test"`
	Replication  store.ReplCounts  `json:"replication"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
}

func (s *Service) secretsOf(c store.StorageConn) (map[string]string, error) {
	out := map[string]string{}
	if len(c.SecretsEnc) == 0 {
		return out, nil
	}
	raw, err := s.cfg.Box.Unseal(c.SecretsEnc)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) seal(m map[string]string) ([]byte, error) {
	if len(m) == 0 {
		return nil, nil
	}
	b, _ := json.Marshal(m)
	return s.cfg.Box.Seal(b)
}

func (s *Service) view(c store.StorageConn, counts store.ReplCounts) View {
	set := map[string]bool{}
	if sec, err := s.secretsOf(c); err == nil {
		for k := range sec {
			set[k] = true
		}
	} else if k, ok := cloud.KindByID(c.Kind); ok {
		// undecryptable (secret key changed): say the secrets are missing so the user re-enters them
		_ = k
	}
	return View{ID: c.ID, Name: c.Name, Kind: c.Kind, Enabled: c.Enabled, BasePath: c.BasePath, UseForImport: c.UseForImport, Replicate: c.Replicate,
		Settings: c.Settings, SecretsSet: set, LastTest: c.LastTest, Replication: counts, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt}
}

func (s *Service) Kinds() []cloud.Kind { return cloud.Kinds() }

func (s *Service) List(ctx context.Context) ([]View, error) {
	cs, err := s.cfg.Store.ListConns(ctx)
	if err != nil {
		return nil, err
	}
	counts, err := s.cfg.Store.ConnCounts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]View, 0, len(cs))
	for _, c := range cs {
		out = append(out, s.view(c, counts[c.ID]))
	}
	return out, nil
}

func (s *Service) Get(ctx context.Context, id string) (View, error) {
	c, err := s.cfg.Store.GetConn(ctx, id)
	if err != nil {
		return View{}, err
	}
	counts, _ := s.cfg.Store.ConnCounts(ctx)
	return s.view(c, counts[c.ID]), nil
}

// ---- create / update / delete ----

type Input struct {
	Name         string            `json:"name"`
	Kind         string            `json:"kind"`
	Settings     map[string]string `json:"settings"`
	Secrets      map[string]string `json:"secrets"`
	BasePath     string            `json:"base_path"`
	Enabled      *bool             `json:"enabled"`
	UseForImport *bool             `json:"use_for_import"`
	Replicate    string            `json:"replicate"`
}

type Patch struct {
	Name         *string           `json:"name"`
	Settings     map[string]string `json:"settings"`
	Secrets      map[string]string `json:"secrets"`
	ClearSecrets []string          `json:"clear_secrets"`
	BasePath     *string           `json:"base_path"`
	Enabled      *bool             `json:"enabled"`
	UseForImport *bool             `json:"use_for_import"`
	Replicate    *string           `json:"replicate"`
}

func cleanName(n string) (string, error) {
	n = strings.TrimSpace(n)
	if c := utf8.RuneCountInString(n); c < 1 || c > 80 {
		return "", bad("INVALID_NAME", "名称需为 1–80 个字符")
	}
	return n, nil
}

// CleanBase normalises the user's start folder ("/a/b/" → "a/b") and validates it.
func CleanBase(p string) (string, error) {
	p = strings.Trim(strings.TrimSpace(p), "/")
	return cloud.CleanRel(p)
}

func checkReplicate(r string) error {
	switch r {
	case "off", "manual", "auto":
		return nil
	}
	return bad("INVALID_REPLICATE", "同步策略须为 off / manual / auto")
}

func boolOr(p *bool, d bool) bool {
	if p == nil {
		return d
	}
	return *p
}

func (s *Service) Create(ctx context.Context, in Input, actor string) (View, error) {
	name, err := cleanName(in.Name)
	if err != nil {
		return View{}, err
	}
	vals, sec, err := cloud.Validate(in.Kind, in.Settings, in.Secrets, nil)
	if err != nil {
		return View{}, translate(err)
	}
	base, err := CleanBase(in.BasePath)
	if err != nil {
		return View{}, translate(err)
	}
	if in.Replicate == "" {
		in.Replicate = "off"
	}
	if err := checkReplicate(in.Replicate); err != nil {
		return View{}, err
	}
	enc, err := s.seal(sec)
	if err != nil {
		return View{}, err
	}
	c, err := s.cfg.Store.CreateConn(ctx, store.StorageConn{Name: name, Kind: in.Kind, Settings: vals, SecretsEnc: enc, BasePath: base,
		Enabled: boolOr(in.Enabled, true), UseForImport: boolOr(in.UseForImport, true), Replicate: in.Replicate})
	if errors.Is(err, store.ErrConflict) {
		return View{}, ErrNameTaken
	}
	if err != nil {
		return View{}, err
	}
	s.cfg.Store.Audit(ctx, actor, "storage.create", c.ID, "新建存储连接「"+c.Name+"」（"+c.Kind+"）")
	return s.view(c, store.ReplCounts{}), nil
}

func translate(err error) error {
	var ve *cloud.ValidationError
	if errors.As(err, &ve) {
		return &ValidationError{ve.Code, ve.Msg}
	}
	return err
}

// targetKeys identify WHERE credentials are sent; changing one without re-entering the credentials is refused so a
// saved credential can never be redirected to another server by an edit.
var targetKeys = []string{"host", "port", "endpoint", "url", "account", "container", "bucket", "share", "space_id", "region", "tls"}

func targetOf(v map[string]string) string {
	var b strings.Builder
	for _, k := range targetKeys {
		b.WriteString(strings.ToLower(strings.TrimSpace(v[k])))
		b.WriteByte('|')
	}
	return b.String()
}

func (s *Service) Update(ctx context.Context, id string, p Patch, actor string) (View, error) {
	cur, err := s.cfg.Store.GetConn(ctx, id)
	if err != nil {
		return View{}, err
	}
	oldSec, serr := s.secretsOf(cur)
	if serr != nil {
		oldSec = map[string]string{} // undecryptable: the user must re-enter credentials
	}
	vals := map[string]string{}
	for k, v := range cur.Settings {
		vals[k] = v
	}
	for k, v := range p.Settings {
		if strings.TrimSpace(v) == "" {
			delete(vals, k)
		} else {
			vals[k] = v
		}
	}
	sec := map[string]string{}
	for k, v := range oldSec {
		sec[k] = v
	}
	for _, k := range p.ClearSecrets {
		delete(sec, k)
	}
	changedSecrets := len(p.ClearSecrets) > 0
	for k, v := range p.Secrets {
		if strings.TrimSpace(v) != "" {
			sec[k] = v
			changedSecrets = true
		}
	}
	kind, _ := cloud.KindByID(cur.Kind)
	hasSecretFields := false
	for _, f := range kind.Fields {
		if f.Secret {
			hasSecretFields = true
		}
	}
	if hasSecretFields && len(oldSec) > 0 && targetOf(vals) != targetOf(cur.Settings) && len(p.Secrets) == 0 {
		return View{}, bad("SECRETS_REQUIRED", "更换了服务器地址或目标，请重新输入凭据（已保存的凭据不会发送到新的地址）")
	}
	have := map[string]bool{}
	for k := range sec {
		have[k] = true
	}
	cleanV, cleanS, err := cloud.Validate(cur.Kind, vals, sec, have)
	if err != nil {
		return View{}, translate(err)
	}
	next := cur
	next.Settings = cleanV
	if p.Name != nil {
		if next.Name, err = cleanName(*p.Name); err != nil {
			return View{}, err
		}
	}
	if p.BasePath != nil {
		if next.BasePath, err = CleanBase(*p.BasePath); err != nil {
			return View{}, translate(err)
		}
	}
	if p.Enabled != nil {
		next.Enabled = *p.Enabled
	}
	if p.UseForImport != nil {
		next.UseForImport = *p.UseForImport
	}
	if p.Replicate != nil {
		if err := checkReplicate(*p.Replicate); err != nil {
			return View{}, err
		}
		next.Replicate = *p.Replicate
	}
	if changedSecrets || serr != nil {
		if next.SecretsEnc, err = s.seal(cleanS); err != nil {
			return View{}, err
		}
	}
	out, err := s.cfg.Store.UpdateConn(ctx, next)
	if errors.Is(err, store.ErrConflict) {
		return View{}, ErrNameTaken
	}
	if err != nil {
		return View{}, err
	}
	s.drop(id)
	s.cfg.Store.Audit(ctx, actor, "storage.update", id, "修改存储连接「"+out.Name+"」")
	counts, _ := s.cfg.Store.ConnCounts(ctx)
	return s.view(out, counts[id]), nil
}

func (s *Service) Delete(ctx context.Context, id, actor string) error {
	c, err := s.cfg.Store.GetConn(ctx, id)
	if err != nil {
		return err
	}
	if err := s.cfg.Store.DeleteConn(ctx, id); err != nil {
		return err
	}
	s.drop(id)
	s.cfg.Store.Audit(ctx, actor, "storage.delete", id, "删除存储连接「"+c.Name+"」（远端文件未改动）")
	return nil
}

// ---- providers ----

func (s *Service) connLock(id string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.connM[id]
	if m == nil {
		m = &sync.Mutex{}
		s.connM[id] = m
	}
	return m
}

func (s *Service) drop(id string) {
	s.mu.Lock()
	cp := s.cache[id]
	delete(s.cache, id)
	s.mu.Unlock()
	if cp != nil {
		_ = cp.p.Close()
	}
}

func (s *Service) settingsOf(c store.StorageConn) (cloud.Settings, error) {
	sec, err := s.secretsOf(c)
	if err != nil {
		return cloud.Settings{}, bad("SECRETS_UNAVAILABLE", "已保存的凭据无法解密（本机密钥已变化），请编辑连接并重新输入凭据")
	}
	set := cloud.Settings{Values: c.Settings, Secrets: sec, BasePath: c.BasePath}
	set.Persist = func(key, value string) { s.persistSecret(c.ID, key, value) }
	return set, nil
}

// provider returns a cached connected provider for a saved connection (rebuilt when the connection changed).
func (s *Service) provider(ctx context.Context, c store.StorageConn) (cloud.Provider, error) {
	s.mu.Lock()
	if cp := s.cache[c.ID]; cp != nil && cp.updated.Equal(c.UpdatedAt) {
		cp.last = time.Now()
		p := cp.p
		s.mu.Unlock()
		return p, nil
	}
	s.mu.Unlock()
	set, err := s.settingsOf(c)
	if err != nil {
		return nil, err
	}
	p, err := cloud.Open(ctx, c.Kind, set)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if old := s.cache[c.ID]; old != nil {
		_ = old.p.Close()
	}
	s.cache[c.ID] = &cachedProv{p: p, updated: c.UpdatedAt, last: time.Now()}
	s.mu.Unlock()
	return p, nil
}

// evict forgets a provider after an error so the next use reconnects.
func (s *Service) evict(id string) { s.drop(id) }

func (s *Service) reap() {
	s.mu.Lock()
	var stale []cloud.Provider
	for id, cp := range s.cache {
		if time.Since(cp.last) > 2*time.Minute {
			stale = append(stale, cp.p)
			delete(s.cache, id)
		}
	}
	s.mu.Unlock()
	for _, p := range stale {
		_ = p.Close()
	}
}

// ---- tests ----

type HostKeyInfo struct {
	Fingerprint string `json:"fingerprint"`
	Changed     bool   `json:"changed"`
}

type TestInput struct {
	Kind          string            `json:"kind"`
	Settings      map[string]string `json:"settings"`
	Secrets       map[string]string `json:"secrets"`
	BasePath      string            `json:"base_path"`
	ConnectionID  string            `json:"connection_id"`
	CheckWrite    bool              `json:"check_write"`
	AcceptHostKey string            `json:"accept_host_key"`
}

type TestResult struct {
	OK        bool         `json:"ok"`
	LatencyMS int64        `json:"latency_ms"`
	Steps     []cloud.Step `json:"steps"`
	HostKey   *HostKeyInfo `json:"host_key"`
}

func marker() string {
	id, _ := imports.NewID()
	return ".oa-agent-write-test-" + id[:8]
}

// runTest connects with the given settings and probes the storage. A SSH host-key problem is returned as HostKey
// (not as a failed step) so the UI can ask the user to confirm the fingerprint.
func (s *Service) runTest(ctx context.Context, kind string, set cloud.Settings, checkWrite bool, acceptKey string) TestResult {
	cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	start := time.Now()
	if acceptKey != "" && set.Values["host_key"] == "" {
		vals := map[string]string{}
		for k, v := range set.Values {
			vals[k] = v
		}
		vals["host_key"] = acceptKey
		set.Values = vals
	}
	p, err := cloud.Open(cctx, kind, set)
	if err != nil {
		var hk *cloud.HostKeyError
		if errors.As(err, &hk) {
			return TestResult{OK: false, LatencyMS: time.Since(start).Milliseconds(), Steps: []cloud.Step{{Name: "连接", OK: false, Message: cloud.Describe(err)}}, HostKey: &HostKeyInfo{Fingerprint: hk.Fingerprint, Changed: hk.Changed}}
		}
		msg := cloud.Describe(err)
		var ve *cloud.ValidationError
		if errors.As(err, &ve) {
			msg = ve.Msg
		}
		return TestResult{OK: false, LatencyMS: time.Since(start).Milliseconds(), Steps: []cloud.Step{{Name: "连接", OK: false, Message: msg}}}
	}
	defer p.Close()
	rep := cloud.Probe(cctx, p, checkWrite, marker())
	steps := append([]cloud.Step{{Name: "连接", OK: true, Message: "已连接"}}, rep.Steps...)
	return TestResult{OK: rep.OK, LatencyMS: time.Since(start).Milliseconds(), Steps: steps}
}

func summarize(r TestResult) string {
	for _, st := range r.Steps {
		if !st.OK {
			return st.Name + "：" + st.Message
		}
	}
	return "连接正常"
}

// TestDraft tests unsaved parameters. With ConnectionID, credentials left blank are taken from that saved
// connection, but only when the target server is unchanged.
func (s *Service) TestDraft(ctx context.Context, in TestInput, actor string) (TestResult, error) {
	var saved map[string]string
	var savedConn *store.StorageConn
	if in.ConnectionID != "" {
		c, err := s.cfg.Store.GetConn(ctx, in.ConnectionID)
		if err != nil {
			return TestResult{}, err
		}
		savedConn = &c
		if c.Kind != in.Kind {
			return TestResult{}, bad("INVALID_KIND", "存储类型与已保存的连接不一致")
		}
		if sec, err := s.secretsOf(c); err == nil && targetOf(c.Settings) == targetOf(in.Settings) {
			saved = sec
		}
	}
	have := map[string]bool{}
	merged := map[string]string{}
	for k, v := range saved {
		merged[k] = v
		have[k] = true
	}
	for k, v := range in.Secrets {
		if strings.TrimSpace(v) != "" {
			merged[k] = v
			have[k] = true
		}
	}
	vals, sec, err := cloud.Validate(in.Kind, in.Settings, merged, have)
	if err != nil {
		return TestResult{}, translate(err)
	}
	base, err := CleanBase(in.BasePath)
	if err != nil {
		return TestResult{}, translate(err)
	}
	res := s.runTest(ctx, in.Kind, cloud.Settings{Values: vals, Secrets: sec, BasePath: base}, in.CheckWrite, in.AcceptHostKey)
	if savedConn != nil {
		_ = s.cfg.Store.SetLastTest(ctx, savedConn.ID, store.LastTest{OK: res.OK, At: time.Now().UTC(), Message: summarize(res)})
	}
	s.cfg.Store.Audit(ctx, actor, "storage.test", in.Kind, "测试存储连接（未保存的参数）："+summarize(res))
	return res, nil
}

// TestSaved tests a stored connection. A confirmed SSH fingerprint is pinned into the connection.
func (s *Service) TestSaved(ctx context.Context, id string, checkWrite bool, acceptHostKey, actor string) (TestResult, error) {
	c, err := s.cfg.Store.GetConn(ctx, id)
	if err != nil {
		return TestResult{}, err
	}
	set, err := s.settingsOf(c)
	if err != nil {
		return TestResult{}, err
	}
	res := s.runTest(ctx, c.Kind, set, checkWrite, acceptHostKey)
	if acceptHostKey != "" && res.OK && c.Settings["host_key"] == "" {
		vals := map[string]string{}
		for k, v := range c.Settings {
			vals[k] = v
		}
		vals["host_key"] = acceptHostKey
		c.Settings = vals
		if _, uerr := s.cfg.Store.UpdateConn(ctx, c); uerr == nil {
			s.drop(id)
			s.cfg.Store.Audit(ctx, actor, "storage.hostkey", id, "固定 SSH 主机密钥 "+acceptHostKey)
		}
	}
	_ = s.cfg.Store.SetLastTest(ctx, id, store.LastTest{OK: res.OK, At: time.Now().UTC(), Message: summarize(res)})
	s.cfg.Store.Audit(ctx, actor, "storage.test", id, "测试存储连接「"+c.Name+"」："+summarize(res))
	return res, nil
}

// ---- browse ----

type Listing struct {
	Path      string        `json:"path"`
	Parent    *string       `json:"parent"`
	Entries   []cloud.Entry `json:"entries"`
	Truncated bool          `json:"truncated"`
}

const maxList = 5000

func parentOf(rel string) *string {
	if rel == "" {
		return nil
	}
	p := path.Dir(rel)
	if p == "." {
		p = ""
	}
	return &p
}

func (s *Service) Browse(ctx context.Context, id, rel string) (Listing, error) {
	rel, err := cloud.CleanRel(rel)
	if err != nil {
		return Listing{}, translate(err)
	}
	c, err := s.cfg.Store.GetConn(ctx, id)
	if err != nil {
		return Listing{}, err
	}
	if !c.Enabled {
		return Listing{}, bad("DISABLED", "该连接已停用")
	}
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	p, err := s.provider(cctx, c)
	if err != nil {
		return Listing{}, s.upstream(id, err)
	}
	ents, err := p.List(cctx, rel)
	if err != nil {
		return Listing{}, s.upstream(id, err)
	}
	l := Listing{Path: rel, Parent: parentOf(rel), Entries: ents}
	if len(l.Entries) > maxList {
		l.Entries, l.Truncated = l.Entries[:maxList], true
	}
	cloud.SortEntries(l.Entries)
	for i := range l.Entries {
		if l.Entries[i].Path == "" {
			l.Entries[i].Path = cloud.Join(rel, l.Entries[i].Name)
		}
	}
	return l, nil
}

// upstream converts a provider error into a user-facing error and drops the cached connection.
func (s *Service) upstream(id string, err error) error {
	s.evict(id)
	if errors.Is(err, cloud.ErrNotFound) {
		return store.ErrNotFound
	}
	var ve *ValidationError
	if errors.As(err, &ve) {
		return err
	}
	var cve *cloud.ValidationError
	if errors.As(err, &cve) {
		return &ValidationError{cve.Code, cve.Msg}
	}
	var hk *cloud.HostKeyError
	if errors.As(err, &hk) {
		return &UpstreamError{"SSH 主机密钥尚未确认或已变化：请先在连接设置中重新测试并核对指纹"}
	}
	log.Printf("storage: remote error on connection %s: %v", id, err)
	if h, ok := err.(interface{ Hint() string }); ok && h.Hint() != "" {
		return &UpstreamError{h.Hint()}
	}
	return &UpstreamError{cloud.Describe(err)}
}

// ---- import from remote ----

const maxWalkDepth = 8

// ImportRemote expands the selected paths (folders recursively), pulls the files into the spool and creates a task.
func (s *Service) ImportRemote(ctx context.Context, connID string, items []string, templateID, key, actor string) (imports.Batch, error) {
	if len(items) == 0 || len(items) > 100 {
		return imports.Batch{}, bad("INVALID_ITEMS", "请选择 1–100 个文件或文件夹")
	}
	c, err := s.cfg.Store.GetConn(ctx, connID)
	if err != nil {
		return imports.Batch{}, err
	}
	if !c.Enabled || !c.UseForImport {
		return imports.Batch{}, bad("NOT_FOR_IMPORT", "该连接未启用或未允许用于导入")
	}
	if templateID != "" {
		t, err := s.cfg.Store.Template(ctx, templateID)
		if err != nil || !t.Enabled {
			return imports.Batch{}, bad("INVALID_TEMPLATE", "指定的 OA 模板不存在或已停用")
		}
	}
	lock := s.connLock(connID)
	lock.Lock()
	defer lock.Unlock()
	p, err := s.provider(ctx, c)
	if err != nil {
		return imports.Batch{}, s.upstream(connID, err)
	}
	var files []imports.RemoteFile
	seen := map[string]bool{}
	var walk func(dir, prefix string, depth int) error
	walk = func(dir, prefix string, depth int) error {
		ents, err := p.List(ctx, dir)
		if err != nil {
			return err
		}
		cloud.SortEntries(ents)
		for _, e := range ents {
			ep := e.Path
			if ep == "" {
				ep = cloud.Join(dir, e.Name)
			}
			if e.IsDir {
				if depth >= maxWalkDepth {
					continue
				}
				if err := walk(ep, cloud.Join(prefix, e.Name), depth+1); err != nil {
					return err
				}
			} else {
				add(&files, seen, p, ep, cloud.Join(prefix, e.Name), e.Size)
			}
			if len(files) > imports.MaxFiles {
				return nil
			}
		}
		return nil
	}
	for _, raw := range items {
		rel, err := cloud.CleanRel(strings.Trim(raw, "/"))
		if err != nil || rel == "" {
			return imports.Batch{}, bad("INVALID_PATH", "所选路径无效")
		}
		ent, err := p.Stat(ctx, rel)
		if err != nil {
			return imports.Batch{}, s.upstream(connID, err)
		}
		if ent.IsDir {
			if err := walk(rel, path.Base(rel), 1); err != nil {
				return imports.Batch{}, s.upstream(connID, err)
			}
		} else {
			add(&files, seen, p, rel, path.Base(rel), ent.Size)
		}
		if len(files) > imports.MaxFiles {
			return imports.Batch{}, &imports.Error{Status: 413, Code: "TOO_MANY_FILES", Message: "所选内容超过每批 100 个文件的上限，请分批导入"}
		}
	}
	b, err := s.cfg.Imports.ImportRemote(ctx, files, key, templateID, connID, func(e error) string { return cloud.Describe(e) })
	if err != nil {
		s.evict(connID)
		return b, err
	}
	s.cfg.Store.Audit(ctx, actor, "storage.import", connID, fmt.Sprintf("从「%s」导入 %d 个文件（%d 个被拒绝）", c.Name, b.Job.TotalFiles, b.Job.FailureCount))
	return b, nil
}

func add(files *[]imports.RemoteFile, seen map[string]bool, p cloud.Provider, remote, rel string, size int64) {
	if seen[remote] {
		return
	}
	seen[remote] = true
	*files = append(*files, imports.RemoteFile{RelPath: rel, Size: size, Open: func(ctx context.Context) (io.ReadCloser, error) {
		r, _, err := p.Open(ctx, remote)
		return r, err
	}})
}

// persistSecret rewrites one credential of a saved connection (OAuth refresh tokens rotate on use).
func (s *Service) persistSecret(id, key, value string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := s.cfg.Store.GetConn(ctx, id)
	if err != nil {
		return
	}
	sec, err := s.secretsOf(c)
	if err != nil {
		return
	}
	sec[key] = value
	enc, err := s.seal(sec)
	if err != nil {
		return
	}
	if err := s.cfg.Store.SetConnSecrets(ctx, id, enc); err != nil {
		log.Printf("storage: cannot save rotated credential for connection %s: %v", id, err)
	}
}
