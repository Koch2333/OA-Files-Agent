package dbadmin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"oa-agent/backend/internal/db"
	"oa-agent/backend/internal/dbconfig"
	"oa-agent/backend/internal/secrets"
	"oa-agent/backend/internal/sqldb"
	"oa-agent/backend/internal/store"
)

var (
	ErrReadOnly     = errors.New("the database configuration is read-only")
	ErrInvalid      = errors.New("invalid database settings")
	ErrNoPending    = errors.New("there is no pending database configuration")
	ErrBadToken     = errors.New("the confirmation token is missing, expired or does not match the previewed DDL")
	ErrNotValidated = errors.New("validation failed")
)

// ConnRequest is what the admin API accepts. The password is write-only: an empty value with UseSaved reuses the
// stored one (pending first, then active); it is never echoed back.
type ConnRequest struct {
	Provider      string `json:"provider"`
	Host          string `json:"host"`
	Port          int    `json:"port"`
	Name          string `json:"name"`
	User          string `json:"user"`
	Password      string `json:"password,omitempty"`
	UseSaved      bool   `json:"use_saved_password,omitempty"`
	Schema        string `json:"schema,omitempty"`
	TLSMode       string `json:"tls_mode,omitempty"`
	CAPEM         string `json:"ca_pem,omitempty"`
	ServerName    string `json:"server_name,omitempty"`
	ClientCertPEM string `json:"client_cert_pem,omitempty"`
	ClientKeyPEM  string `json:"client_key_pem,omitempty"`
}

// Service is the admin workflow. All methods are safe for concurrent use.
type Service struct {
	DataDir       string
	MigrationsDir string
	Box           *secrets.Box
	Resolved      dbconfig.Resolved
	Store         *store.Store // the running database (may be nil)
	Audit         func(ctx context.Context, actor, action, target, summary string)
	Now           func() time.Time
	// Hooks lets the cross-database migration workflow add its own checks (maintenance mode etc.).
	direct *sqldb.Config // set by SetDirect (target "direct")
	mu     sync.Mutex
	tokens map[string]initToken
}

type initToken struct {
	hash    string
	target  string
	expires time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) audit(ctx context.Context, actor, action, target, summary string) {
	if s.Audit != nil {
		s.Audit(ctx, actor, action, target, summary)
	}
}

func (s *Service) readOnly() error {
	if s.Resolved.ReadOnlyReason != "" {
		return fmt.Errorf("%w: %s", ErrReadOnly, s.Resolved.ReadOnlyReason)
	}
	return nil
}

// ---- views (no secrets) ----

type ConnView struct {
	Provider    string `json:"provider"`
	Label       string `json:"label"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Name        string `json:"name"`
	User        string `json:"user"`
	Schema      string `json:"schema,omitempty"`
	TLSMode     string `json:"tls_mode"`
	HasCA       bool   `json:"has_ca"`
	HasClient   bool   `json:"has_client_cert"`
	ServerName  string `json:"server_name,omitempty"`
	HasPassword bool   `json:"has_password"`
}

func viewOf(c sqldb.Config) ConnView {
	h, p, n, u := c.Host, c.Port, c.Name, c.User
	if c.URL != "" {
		if f, err := sqldb.FromURL(c.URL); err == nil {
			h, p, n, u = f.Host, f.Port, f.Name, f.User
		}
	}
	tm := c.TLSMode
	if tm == "" {
		tm = sqldb.TLSDisable
	}
	return ConnView{Provider: string(c.Provider), Label: c.Provider.Label(), Host: h, Port: p, Name: n, User: u, Schema: c.Schema, TLSMode: tm,
		HasCA: c.CAPEM != "", HasClient: c.ClientCertPEM != "", ServerName: c.ServerName, HasPassword: c.Password != "" || c.URL != ""}
}

type PendingView struct {
	ConnView
	SavedAt            time.Time  `json:"saved_at"`
	SavedBy            string     `json:"saved_by"`
	Validated          bool       `json:"validated"`
	Approved           bool       `json:"approved"`
	ApprovedAt         *time.Time `json:"approved_at,omitempty"`
	ApprovedBy         string     `json:"approved_by,omitempty"`
	CurrentHasData     bool       `json:"current_has_data"`
	DataMigrated       bool       `json:"data_migrated"`
	NeedsInitialize    bool       `json:"needs_initialize"`
	NeedsDataMigrate   bool       `json:"needs_data_migration"`
	SchemaState        string     `json:"schema_state,omitempty"`
	WillApplyOnRestart bool       `json:"will_apply_on_restart"`
	Blockers           []string   `json:"blockers"`
}

type ApplyView struct {
	At      time.Time `json:"at"`
	Applied bool      `json:"applied"`
	Reason  string    `json:"reason"`
}

type StatusView struct {
	Configured     bool         `json:"configured"`
	Source         string       `json:"source"`
	ReadOnly       bool         `json:"read_only"`
	ReadOnlyReason string       `json:"read_only_reason,omitempty"`
	Active         *ActiveView  `json:"active,omitempty"`
	Pending        *PendingView `json:"pending,omitempty"`
	LastApply      *ApplyView   `json:"last_apply,omitempty"`
	RestartNeeded  bool         `json:"restart_needed"`
	Warnings       []string     `json:"warnings"`
}

type ActiveView struct {
	ConnView
	Connected     bool    `json:"connected"`
	ServerVersion string  `json:"server_version"`
	LatencyMS     int64   `json:"latency_ms"`
	Migration     MigView `json:"migration"`
	Isolation     string  `json:"isolation"`
	Checks        []Check `json:"checks"`
	Embedded      bool    `json:"embedded"`
}

type MigView struct {
	State   string `json:"state"`
	Version int    `json:"version"`
	Latest  int    `json:"latest"`
	Dirty   bool   `json:"dirty"`
}

func migView(st db.Status) MigView {
	return MigView{State: st.State, Version: st.Version, Latest: st.Latest, Dirty: st.Dirty}
}

// Status reports the running database and any pending configuration.
func (s *Service) Status(ctx context.Context) (StatusView, error) {
	v := StatusView{Configured: s.Resolved.Configured(), Source: string(s.Resolved.Source), ReadOnly: s.Resolved.ReadOnlyReason != "", ReadOnlyReason: s.Resolved.ReadOnlyReason, Warnings: []string{}}
	if s.Store != nil {
		cfg := s.Store.Pool.Config()
		rep := Inspect(ctx, s.Store.Pool)
		cv := viewOf(cfg)
		if cfg.Schema == "" && cfg.Provider == sqldb.Postgres {
			cv.Schema = "public"
		}
		v.Active = &ActiveView{ConnView: cv, Connected: rep.LatencyMS >= 0 && len(rep.Checks) > 0 && rep.Checks[0].Status == Pass, ServerVersion: rep.ServerVersion, LatencyMS: rep.LatencyMS,
			Migration: migView(rep.Schema), Isolation: rep.Isolation, Checks: rep.Checks, Embedded: s.Resolved.Source == dbconfig.SourceEmbedded}
		if rep.Isolation == IsoNotIsolated {
			v.Warnings = append(v.Warnings, "当前数据库的隔离性无法保证（目标 schema 中有非 OA Agent 的表）。现有安装继续运行，但建议迁移到专用数据库/schema。")
		}
		for _, c := range rep.Checks {
			if c.Status == Warn && c.ID == "least_privilege" {
				v.Warnings = append(v.Warnings, c.Detail)
			}
		}
	}
	f, err := dbconfig.Load(s.DataDir)
	if err != nil {
		return v, err
	}
	if f.LastApply != nil {
		v.LastApply = &ApplyView{At: f.LastApply.At, Applied: f.LastApply.Applied, Reason: f.LastApply.Reason}
	}
	if f.Pending != nil {
		pv := s.pendingView(ctx, *f.Pending)
		v.Pending = &pv
		v.RestartNeeded = pv.WillApplyOnRestart
	}
	return v, nil
}

func (s *Service) pendingView(ctx context.Context, p dbconfig.Pending) PendingView {
	pv := PendingView{SavedAt: p.SavedAt, SavedBy: p.SavedBy, Validated: p.Validated, Approved: p.Approved, ApprovedAt: p.ApprovedAt, ApprovedBy: p.ApprovedBy,
		CurrentHasData: p.CurrentHasData, DataMigrated: p.DataMigrated, Blockers: []string{}}
	cfg, err := p.Entry.Config(s.Box)
	if err != nil {
		pv.Blockers = append(pv.Blockers, err.Error())
		return pv
	}
	pv.ConnView = viewOf(cfg)
	pv.HasPassword = p.Entry.HasPassword()
	blockers, state := s.gates(ctx, p, cfg)
	pv.SchemaState = state
	pv.NeedsInitialize = state == "empty" || state == "migration_required"
	pv.NeedsDataMigrate = p.CurrentHasData && !p.DataMigrated && state == "ok"
	pv.Blockers = blockers
	pv.WillApplyOnRestart = len(blockers) == 0
	return pv
}

// gates lists everything that currently keeps a pending configuration from being applied.
func (s *Service) gates(ctx context.Context, p dbconfig.Pending, cfg sqldb.Config) (blockers []string, schemaState string) {
	blockers = []string{}
	if !p.Validated {
		blockers = append(blockers, "尚未通过校验")
	}
	if !p.Approved {
		blockers = append(blockers, "管理员尚未批准切换")
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	d, err := sqldb.Open(cctx, cfg)
	if err != nil {
		return append(blockers, "无法连接目标数据库："+err.Error()), "unavailable"
	}
	defer d.Close()
	rep := Inspect(cctx, d)
	for _, c := range rep.Checks {
		if c.Status == Fail {
			blockers = append(blockers, c.Title+"："+c.Detail)
		}
	}
	schemaState = rep.Schema.State
	switch schemaState {
	case "empty":
		blockers = append(blockers, "目标库尚未初始化：请先执行初始化（先预览 DDL，再由管理员批准）")
	case "migration_required":
		blockers = append(blockers, "目标库表结构版本落后，需要先批准升级")
	case "dirty":
		blockers = append(blockers, "目标库上次迁移未完成，需要先恢复")
	}
	if schemaState == "ok" && p.CurrentHasData && !p.DataMigrated {
		var n int
		_ = d.QueryRow(cctx, `SELECT (SELECT count(*) FROM users) + (SELECT count(*) FROM import_jobs) + (SELECT count(*) FROM ledger_entries)`).Scan(&n)
		if n == 0 {
			blockers = append(blockers, "当前安装已有数据，而目标库是空的：不会把现有安装指向空库。请使用“数据迁移”流程复制数据后再切换。")
		}
	}
	return blockers, schemaState
}

// ---- requests ----

func (s *Service) savedEntry() (*dbconfig.Entry, *dbconfig.Entry) {
	f, err := dbconfig.Load(s.DataDir)
	if err != nil {
		return nil, nil
	}
	var pe *dbconfig.Entry
	if f.Pending != nil {
		pe = &f.Pending.Entry
	}
	return pe, f.Active
}

// ToConfig validates a request and resolves the saved password when asked to.
func (s *Service) ToConfig(r ConnRequest) (sqldb.Config, error) {
	p, err := sqldb.ParseProvider(r.Provider)
	if err != nil {
		return sqldb.Config{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	c := sqldb.Config{Provider: p, Host: strings.TrimSpace(r.Host), Port: r.Port, Name: strings.TrimSpace(r.Name), User: strings.TrimSpace(r.User), Password: r.Password,
		Schema: strings.TrimSpace(r.Schema), TLSMode: r.TLSMode, CAPEM: strings.TrimSpace(r.CAPEM), ServerName: strings.TrimSpace(r.ServerName),
		ClientCertPEM: strings.TrimSpace(r.ClientCertPEM), ClientKeyPEM: strings.TrimSpace(r.ClientKeyPEM)}
	if c.Port == 0 {
		c.Port = p.DefaultPort()
	}
	if c.Password == "" && r.UseSaved {
		pe, ae := s.savedEntry()
		for _, e := range []*dbconfig.Entry{pe, ae} {
			if e != nil && e.HasPassword() && e.Provider == string(p) && strings.EqualFold(e.Host, c.Host) && e.User == c.User {
				sc, err := e.Config(s.Box)
				if err != nil {
					return c, fmt.Errorf("%w: %v", ErrInvalid, err)
				}
				c.Password = sc.Password
				if c.ClientKeyPEM == "" && c.ClientCertPEM != "" {
					c.ClientKeyPEM = sc.ClientKeyPEM
				}
				break
			}
		}
		if c.Password == "" {
			return c, fmt.Errorf("%w: no saved password for this host and user; enter the password", ErrInvalid)
		}
	}
	c = c.Normalize()
	if err := c.Validate(); err != nil {
		return c, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return c, nil
}

// TestResult is the outcome of a connection test.
type TestResult struct {
	OK            bool    `json:"ok"`
	Provider      string  `json:"provider"`
	ServerVersion string  `json:"server_version"`
	LatencyMS     int64   `json:"latency_ms"`
	TLS           string  `json:"tls_mode"`
	Message       string  `json:"message"`
	Migration     MigView `json:"migration"`
}

func (s *Service) open(ctx context.Context, c sqldb.Config) (*sqldb.DB, error) {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return sqldb.Open(cctx, c)
}

// Test connects with the given settings and reports provider, version, latency and migration status.
func (s *Service) Test(ctx context.Context, r ConnRequest, actor string) (TestResult, error) {
	c, err := s.ToConfig(r)
	if err != nil {
		return TestResult{}, err
	}
	res := TestResult{Provider: string(c.Provider), TLS: viewOf(c).TLSMode}
	t0 := time.Now()
	d, err := s.open(ctx, c)
	if err != nil {
		res.Message = err.Error()
		s.audit(ctx, actor, "database.test", c.Provider.Label(), "failed: "+targetSummary(c))
		return res, nil
	}
	defer d.Close()
	res.LatencyMS = time.Since(t0).Milliseconds()
	rep := Inspect(ctx, d)
	res.OK, res.ServerVersion, res.Migration = true, rep.ServerVersion, migView(rep.Schema)
	res.Message = "连接成功"
	s.audit(ctx, actor, "database.test", c.Provider.Label(), "ok: "+targetSummary(c))
	return res, nil
}

func targetSummary(c sqldb.Config) string {
	return fmt.Sprintf("%s@%s/%s", c.User, c.Address(), c.Name)
}

// Validate runs the permission and isolation checklist.
func (s *Service) Validate(ctx context.Context, r ConnRequest, actor string) (Report, error) {
	c, err := s.ToConfig(r)
	if err != nil {
		return Report{}, err
	}
	d, err := s.open(ctx, c)
	if err != nil {
		rep := Report{Provider: string(c.Provider), Isolation: IsoUnknown, Checks: []Check{{"connect", "连接数据库", Fail, err.Error()}}}
		return rep, nil
	}
	defer d.Close()
	rep := Inspect(ctx, d)
	s.audit(ctx, actor, "database.validate", c.Provider.Label(), fmt.Sprintf("ok=%v isolation=%s %s", rep.OK, rep.Isolation, targetSummary(c)))
	return rep, nil
}

func (s *Service) currentHasData(ctx context.Context) bool {
	if s.Store == nil {
		return false
	}
	var n int
	if err := s.Store.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM users) + (SELECT count(*) FROM import_jobs) + (SELECT count(*) FROM ledger_entries)`).Scan(&n); err != nil {
		return true // unknown: assume the installation has data (safe side)
	}
	return n > 0
}

// SavePending stores the configuration as pending. It never switches the running database. Approval requires
// confirm=true and a passing validation.
func (s *Service) SavePending(ctx context.Context, r ConnRequest, confirm bool, actor string) (StatusView, error) {
	if err := s.readOnly(); err != nil {
		return StatusView{}, err
	}
	c, err := s.ToConfig(r)
	if err != nil {
		return StatusView{}, err
	}
	d, err := s.open(ctx, c)
	if err != nil {
		return StatusView{}, fmt.Errorf("%w: cannot connect: %v", ErrNotValidated, err)
	}
	rep := Inspect(ctx, d)
	d.Close()
	if !rep.OK {
		return StatusView{}, &ValidationError{Report: rep}
	}
	entry, err := dbconfig.EntryFromConfig(s.Box, c)
	if err != nil {
		return StatusView{}, err
	}
	now := s.now().UTC()
	p := dbconfig.Pending{Entry: entry, SavedAt: now, SavedBy: actor, Validated: true, CurrentHasData: s.currentHasData(ctx)}
	if confirm {
		p.Approved, p.ApprovedAt, p.ApprovedBy = true, &now, actor
	}
	f, err := dbconfig.Load(s.DataDir)
	if err != nil {
		return StatusView{}, err
	}
	f.Pending = &p
	if err = dbconfig.Save(s.DataDir, f); err != nil {
		return StatusView{}, err
	}
	s.audit(ctx, actor, "database.config.save", c.Provider.Label(), fmt.Sprintf("pending saved approved=%v has_data=%v %s", confirm, p.CurrentHasData, targetSummary(c)))
	return s.Status(ctx)
}

// ValidationError carries the failed checklist.
type ValidationError struct{ Report Report }

func (e *ValidationError) Error() string {
	var fails []string
	for _, c := range e.Report.Checks {
		if c.Status == Fail {
			fails = append(fails, c.Title+"："+c.Detail)
		}
	}
	return "validation failed: " + strings.Join(fails, "; ")
}

// DiscardPending removes the pending configuration.
func (s *Service) DiscardPending(ctx context.Context, actor string) error {
	if err := s.readOnly(); err != nil {
		return err
	}
	f, err := dbconfig.Load(s.DataDir)
	if err != nil {
		return err
	}
	if f.Pending == nil {
		return ErrNoPending
	}
	f.Pending = nil
	if err = dbconfig.Save(s.DataDir, f); err != nil {
		return err
	}
	s.audit(ctx, actor, "database.config.discard", "pending", "pending database configuration discarded")
	return nil
}

// ---- initialisation (dry run + approval) ----

type StepPreview struct {
	Version     int      `json:"version"`
	Name        string   `json:"name"`
	Statements  []string `json:"statements"`
	Destructive bool     `json:"destructive"`
	Resume      bool     `json:"resume"`
}

type InitPreview struct {
	Target        string        `json:"target"`
	Provider      string        `json:"provider"`
	State         string        `json:"state"`
	Steps         []StepPreview `json:"steps"`
	Statements    int           `json:"statement_count"`
	Destructive   bool          `json:"destructive"`
	Token         string        `json:"confirmation_token"`
	ExpiresAt     time.Time     `json:"expires_at"`
	Checks        []Check       `json:"checks"`
	Transactional bool          `json:"transactional_ddl"`
}

// SetDirect makes the target "direct" refer to a configuration supplied by the caller (the oa-admin db-check / db-init
// commands run before any deployment exists, so there is neither a saved pending entry nor a running database).
func (s *Service) SetDirect(c sqldb.Config) { s.direct = &c }

func (s *Service) targetConfig(target string) (sqldb.Config, bool, error) {
	switch target {
	case "direct":
		if s.direct == nil {
			return sqldb.Config{}, false, errors.New("no direct database configuration")
		}
		return *s.direct, false, nil
	case "", "pending":
		f, err := dbconfig.Load(s.DataDir)
		if err != nil {
			return sqldb.Config{}, false, err
		}
		if f.Pending == nil {
			return sqldb.Config{}, false, ErrNoPending
		}
		c, err := f.Pending.Config(s.Box)
		return c, false, err
	case "active":
		if s.Store == nil {
			return sqldb.Config{}, true, errors.New("no active database")
		}
		return s.Store.Pool.Config(), true, nil
	}
	return sqldb.Config{}, false, fmt.Errorf("%w: target must be pending, active or direct", ErrInvalid)
}

func (s *Service) targetDB(ctx context.Context, target string) (*sqldb.DB, bool, error) {
	c, active, err := s.targetConfig(target)
	if err != nil {
		return nil, false, err
	}
	if active {
		return s.Store.Pool, true, nil
	}
	d, err := s.open(ctx, c)
	return d, false, err
}

// PlanInit is the dry run: it lists the DDL that would run and returns a confirmation token bound to that exact plan.
func (s *Service) PlanInit(ctx context.Context, target, actor string) (InitPreview, error) {
	d, active, err := s.targetDB(ctx, target)
	if err != nil {
		return InitPreview{}, err
	}
	if !active {
		defer d.Close()
	}
	rep := Inspect(ctx, d)
	pv := InitPreview{Target: normTarget(target), Provider: string(d.Provider()), State: rep.Schema.State, Checks: rep.Checks, Steps: []StepPreview{}, Transactional: d.Provider() != sqldb.MySQL}
	if err := db.EnsureReadable(ctx, d); err != nil {
		return pv, err
	}
	pl, err := db.MakePlan(ctx, d, s.MigrationsDir)
	if err != nil {
		return pv, err
	}
	for _, p := range pl.Pending {
		sp := StepPreview{Version: p.Version, Name: p.Name, Destructive: p.Destructive, Resume: p.Resume}
		for _, st := range p.Steps {
			if d.Provider() == sqldb.Postgres {
				sp.Statements = append(sp.Statements, sqldb.SplitStatements(st)...)
			} else {
				sp.Statements = append(sp.Statements, st)
			}
		}
		pv.Statements += len(sp.Statements)
		pv.Steps = append(pv.Steps, sp)
	}
	pv.Destructive = pl.Destructive
	tok := make([]byte, 24)
	if _, err = rand.Read(tok); err != nil {
		return pv, err
	}
	pv.Token = hex.EncodeToString(tok)
	pv.ExpiresAt = s.now().Add(15 * time.Minute)
	s.mu.Lock()
	if s.tokens == nil {
		s.tokens = map[string]initToken{}
	}
	for k, t := range s.tokens {
		if t.expires.Before(s.now()) {
			delete(s.tokens, k)
		}
	}
	s.tokens[pv.Token] = initToken{hash: pl.Hash, target: normTarget(target), expires: pv.ExpiresAt}
	s.mu.Unlock()
	s.audit(ctx, actor, "database.initialize.preview", pv.Provider, fmt.Sprintf("target=%s state=%s migrations=%d statements=%d", pv.Target, pv.State, len(pv.Steps), pv.Statements))
	return pv, nil
}

func normTarget(t string) string {
	if t == "" {
		return "pending"
	}
	return t
}

// Initialize applies the previewed migrations after the administrator confirmed them with the token.
func (s *Service) Initialize(ctx context.Context, target, token, actor string) (InitPreview, error) {
	if err := s.readOnly(); err != nil && normTarget(target) == "pending" {
		return InitPreview{}, err
	}
	s.mu.Lock()
	t, ok := s.tokens[token]
	if ok {
		delete(s.tokens, token) // single use
	}
	s.mu.Unlock()
	if !ok || t.expires.Before(s.now()) || t.target != normTarget(target) {
		return InitPreview{}, ErrBadToken
	}
	d, active, err := s.targetDB(ctx, target)
	if err != nil {
		return InitPreview{}, err
	}
	if !active {
		defer d.Close()
	}
	pl, err := db.MakePlan(ctx, d, s.MigrationsDir)
	if err != nil {
		return InitPreview{}, err
	}
	if pl.Hash != t.hash {
		return InitPreview{}, ErrBadToken
	}
	rep := Inspect(ctx, d)
	if !rep.OK {
		return InitPreview{}, &ValidationError{Report: rep}
	}
	if rep.Isolation == IsoNotIsolated {
		return InitPreview{}, &ValidationError{Report: rep}
	}
	s.audit(ctx, actor, "database.initialize.start", string(d.Provider()), fmt.Sprintf("target=%s migrations=%d", normTarget(target), len(pl.Pending)))
	if err = db.Apply(ctx, d, db.Options{Dir: s.MigrationsDir}); err != nil {
		s.audit(ctx, actor, "database.initialize.failed", string(d.Provider()), "target="+normTarget(target)+": "+sqldb.SafeError(err, d.Config()))
		return InitPreview{}, err
	}
	s.audit(ctx, actor, "database.initialize.done", string(d.Provider()), fmt.Sprintf("target=%s migrations=%d", normTarget(target), len(pl.Pending)))
	st, _ := db.SchemaStatus(ctx, d)
	return InitPreview{Target: normTarget(target), Provider: string(d.Provider()), State: st.State, Steps: []StepPreview{}}, nil
}

// ---- start-up promotion ----

// Promote applies the pending configuration at start-up when every gate is green; otherwise the active
// configuration is kept and the reason is recorded. It never changes anything when the environment configures the
// database. The returned bool says whether the active configuration changed.
func Promote(ctx context.Context, dir, migrations string, box *secrets.Box, logf func(string, ...any)) bool {
	f, err := dbconfig.Load(dir)
	if err != nil || f.Pending == nil {
		return false
	}
	svc := &Service{DataDir: dir, MigrationsDir: migrations, Box: box}
	cfg, err := f.Pending.Config(box)
	record := func(applied bool, reason string) {
		f.LastApply = &dbconfig.ApplyRecord{At: time.Now().UTC(), Applied: applied, Reason: reason}
		if err := dbconfig.Save(dir, f); err != nil {
			logf("database.json: %v", err)
		}
	}
	if err != nil {
		record(false, err.Error())
		logf("pending database configuration ignored: %v", err)
		return false
	}
	blockers, _ := svc.gates(ctx, *f.Pending, cfg)
	if len(blockers) > 0 {
		reason := strings.Join(blockers, "; ")
		record(false, reason)
		logf("pending database configuration NOT applied (the current database stays in use): %s", reason)
		return false
	}
	entry := f.Pending.Entry
	f.Active, f.Pending = &entry, nil
	record(true, "pending configuration applied at start-up")
	logf("database configuration switched to %s (%s)", cfg.Provider.Label(), targetSummary(cfg))
	return true
}
