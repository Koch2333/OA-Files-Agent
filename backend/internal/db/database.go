// Package db owns schema migrations and schema status for the supported engines (PostgreSQL, MySQL, SQL Server).
package db

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"oa-agent/backend/internal/sqldb"
)

// SchemaVersion is the newest migration this build expects.
const SchemaVersion = 10

// Migration is one numbered migration file.
type Migration struct {
	Version     int
	Name        string
	Path        string
	Checksum    string
	Steps       []string // executed one by one (PostgreSQL: a single step, the BEGIN/COMMIT body)
	Destructive bool
}

// Dir returns the directory that holds the migrations of a provider below the root migrations directory:
// PostgreSQL files stay in the root (their checksums are recorded in existing installations), the other engines
// have their own subdirectory.
func Dir(root string, p sqldb.Provider) string {
	if p == sqldb.Postgres {
		return root
	}
	return filepath.Join(root, string(p))
}

var destructiveRe = regexp.MustCompile(`(?i)\b(drop\s+(table|column|schema|database)|truncate\b)|--\s*oa:destructive`)

const stepSep = "--;;"

// Load reads and validates every NNN_*.up.sql of the provider, sorted by version. *.down.sql files are never read.
func Load(root string, p sqldb.Provider) ([]Migration, error) {
	dir := Dir(root, p)
	files, err := filepath.Glob(filepath.Join(dir, "*.up.sql"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	if len(files) == 0 {
		return nil, fmt.Errorf("no %s migrations found in %s", p, dir)
	}
	var out []Migration
	seen := map[int]bool{}
	for _, f := range files {
		v, err := versionOf(f)
		if err != nil {
			return nil, err
		}
		if seen[v] {
			return nil, fmt.Errorf("duplicate migration version %d", v)
		}
		seen[v] = true
		raw, err := os.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("read migration: %w", err)
		}
		m := Migration{Version: v, Name: filepath.Base(f), Path: f, Checksum: fmt.Sprintf("%x", sha256.Sum256(raw)), Destructive: destructiveRe.Match(raw)}
		text := strings.TrimSpace(string(raw))
		if p == sqldb.Postgres {
			if !strings.HasPrefix(text, "BEGIN;") || !strings.HasSuffix(text, "COMMIT;") {
				return nil, fmt.Errorf("migration %s must have a BEGIN/COMMIT wrapper", m.Name)
			}
			m.Steps = []string{strings.TrimSuffix(strings.TrimPrefix(text, "BEGIN;"), "COMMIT;")}
		} else {
			for _, s := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n"+stepSep+"\n") {
				if s = strings.TrimSpace(s); s != "" {
					m.Steps = append(m.Steps, s)
				}
			}
			if len(m.Steps) == 0 {
				return nil, fmt.Errorf("migration %s is empty", m.Name)
			}
		}
		out = append(out, m)
	}
	return out, nil
}

func versionOf(path string) (int, error) {
	base := filepath.Base(path)
	i := strings.IndexByte(base, '_')
	if i < 1 {
		return 0, fmt.Errorf("migration file %q needs a NNN_ prefix", base)
	}
	v, err := strconv.Atoi(base[:i])
	if err != nil || v < 1 {
		return 0, fmt.Errorf("migration file %q needs a numeric NNN_ prefix", base)
	}
	return v, nil
}

// Applied is one row of schema_migrations.
type Applied struct {
	Version   int       `json:"version"`
	Checksum  string    `json:"checksum"`
	AppliedAt time.Time `json:"applied_at"`
	State     string    `json:"state"` // applied | dirty
}

func ensureTracking(ctx context.Context, q sqldb.Queryer, p sqldb.Provider) error {
	var stmts []string
	switch p {
	case sqldb.Postgres:
		stmts = []string{`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, checksum TEXT NOT NULL, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`}
	case sqldb.MySQL:
		stmts = []string{
			`CREATE TABLE IF NOT EXISTS schema_migrations (version INT NOT NULL PRIMARY KEY, checksum VARCHAR(64) NOT NULL, applied_at DATETIME(6) NOT NULL DEFAULT (UTC_TIMESTAMP(6)), state VARCHAR(16) NOT NULL DEFAULT 'applied') ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
			`CREATE TABLE IF NOT EXISTS schema_migration_steps (version INT NOT NULL, step INT NOT NULL, applied_at DATETIME(6) NOT NULL DEFAULT (UTC_TIMESTAMP(6)), PRIMARY KEY (version, step)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		}
	case sqldb.MSSQL:
		stmts = []string{`IF OBJECT_ID('schema_migrations', 'U') IS NULL CREATE TABLE schema_migrations (version INT NOT NULL PRIMARY KEY, checksum NVARCHAR(64) NOT NULL, applied_at DATETIME2(6) NOT NULL DEFAULT SYSUTCDATETIME(), state NVARCHAR(16) NOT NULL DEFAULT 'applied')`}
	}
	for _, s := range stmts {
		if _, err := q.Exec(ctx, s); err != nil {
			return err
		}
	}
	return nil
}

// AppliedMigrations lists schema_migrations; a missing table yields an empty list.
func AppliedMigrations(ctx context.Context, d *sqldb.DB) ([]Applied, error) {
	q := `SELECT version, checksum, applied_at, state FROM schema_migrations ORDER BY version`
	if d.Provider() == sqldb.Postgres {
		q = `SELECT version, checksum, applied_at, 'applied' FROM schema_migrations ORDER BY version`
	}
	rows, err := d.Query(ctx, q)
	if err != nil {
		if tableMissing(ctx, d, "schema_migrations") {
			return nil, nil
		}
		return nil, err
	}
	defer rows.Close()
	var out []Applied
	for rows.Next() {
		var a Applied
		if err := rows.Scan(&a.Version, &a.Checksum, &a.AppliedAt, &a.State); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ListTables returns the base tables of the connection's current schema/database.
func ListTables(ctx context.Context, d *sqldb.DB) ([]string, error) {
	var q string
	switch d.Provider() {
	case sqldb.Postgres:
		q = `SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema() AND table_type = 'BASE TABLE' ORDER BY table_name`
	case sqldb.MySQL:
		q = `SELECT table_name FROM information_schema.tables WHERE table_schema = DATABASE() AND table_type = 'BASE TABLE' ORDER BY table_name`
	default:
		q = `SELECT table_name FROM information_schema.tables WHERE table_schema = SCHEMA_NAME() AND table_type = 'BASE TABLE' ORDER BY table_name`
	}
	rows, err := d.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, strings.ToLower(n))
	}
	return out, rows.Err()
}

func tableMissing(ctx context.Context, d *sqldb.DB, name string) bool {
	ts, err := ListTables(ctx, d)
	if err != nil {
		return false
	}
	for _, t := range ts {
		if t == name {
			return false
		}
	}
	return true
}

// OATables are the tables this application owns (every migration's tables plus the tracking tables).
var OATables = map[string]bool{
	"import_jobs": true, "file_records": true, "agent_events": true, "oa_templates": true, "archive_rules": true, "archive_plans": true,
	"archive_plan_items": true, "archive_operations": true, "ledger_entries": true, "audit_logs": true, "correction_proposals": true,
	"llm_settings": true, "storage_connections": true, "replications": true, "auth_settings": true, "users": true, "webauthn_credentials": true,
	"recovery_codes": true, "sessions": true, "auth_challenges": true, "invitations": true, "llm_personal": true, "ai_policy": true,
	"error_reports": true, "error_occurrences": true, "schema_migrations": true, "schema_migration_steps": true,
}

// Status describes the schema of the connected database.
type Status struct {
	State         string    `json:"state"` // ok | migration_required | dirty | empty | unavailable
	Version       int       `json:"version"`
	Latest        int       `json:"latest"`
	Dirty         bool      `json:"dirty"`
	Applied       []Applied `json:"applied"`
	OATables      int       `json:"oa_tables"`
	ForeignTables []string  `json:"foreign_tables"`
}

// SchemaStatus inspects the database without changing it.
func SchemaStatus(ctx context.Context, d *sqldb.DB) (Status, error) {
	st := Status{Latest: SchemaVersion, ForeignTables: []string{}, Applied: []Applied{}}
	if err := d.Ping(ctx); err != nil {
		st.State = "unavailable"
		return st, err
	}
	tables, err := ListTables(ctx, d)
	if err != nil {
		st.State = "unavailable"
		return st, err
	}
	for _, t := range tables {
		if OATables[t] {
			st.OATables++
		} else {
			st.ForeignTables = append(st.ForeignTables, t)
		}
	}
	ap, err := AppliedMigrations(ctx, d)
	if err != nil {
		st.State = "unavailable"
		return st, err
	}
	st.Applied = append(st.Applied, ap...)
	for _, a := range ap {
		if a.State == "dirty" {
			st.Dirty = true
		} else if a.Version > st.Version {
			st.Version = a.Version
		}
	}
	switch {
	case st.Dirty:
		st.State = "dirty"
	case st.OATables == 0:
		st.State = "empty"
	case st.Version >= SchemaVersion && coreTablesExist(ctx, d):
		st.State = "ok"
	default:
		st.State = "migration_required"
	}
	return st, nil
}

func coreTablesExist(ctx context.Context, d *sqldb.DB) bool {
	for _, t := range []string{"import_jobs", "file_records", "ledger_entries"} {
		var one int
		rows, err := d.Query(ctx, `SELECT 1 FROM `+t+` WHERE 1=0`)
		if err != nil {
			return false
		}
		rows.Close()
		_ = one
	}
	return true
}

// Check is the readiness probe used by /ready: ok, migration_required or unavailable.
func Check(ctx context.Context, d *sqldb.DB) string {
	if d == nil {
		return "not_configured"
	}
	st, err := SchemaStatus(ctx, d)
	if err != nil && st.State == "unavailable" {
		return "unavailable"
	}
	if st.State == "ok" {
		return "ok"
	}
	return "migration_required"
}

// ---- planning and applying ----

// Pending is a migration that still has to run.
type Pending struct {
	Migration
	Resume bool `json:"resume"` // a half-applied (dirty) migration of an engine without transactional DDL
}

// Plan is a dry run: what Apply would execute.
type Plan struct {
	Pending     []Pending
	Hash        string // binds the plan to the target and the exact DDL; used as the approval token payload
	Destructive bool
}

var ErrDirty = errors.New("a previous migration did not finish and the checksum no longer matches")

// MakePlan compares the migration files with schema_migrations.
func MakePlan(ctx context.Context, d *sqldb.DB, root string) (Plan, error) {
	ms, err := Load(root, d.Provider())
	if err != nil {
		return Plan{}, err
	}
	ap, err := AppliedMigrations(ctx, d)
	if err != nil {
		return Plan{}, err
	}
	have := map[int]Applied{}
	for _, a := range ap {
		have[a.Version] = a
	}
	var pl Plan
	h := sha256.New()
	fmt.Fprintf(h, "%s|%s|", d.Config().Fingerprint(), d.Provider())
	for _, m := range ms {
		a, ok := have[m.Version]
		if ok {
			if a.Checksum != m.Checksum {
				return Plan{}, fmt.Errorf("migration %d checksum mismatch; restore the applied migration file", m.Version)
			}
			if a.State == "applied" {
				continue
			}
		}
		pl.Pending = append(pl.Pending, Pending{Migration: m, Resume: ok})
		if m.Destructive {
			pl.Destructive = true
		}
		fmt.Fprintf(h, "%d:%s;", m.Version, m.Checksum)
	}
	pl.Hash = fmt.Sprintf("%x", h.Sum(nil))
	return pl, nil
}

// Options control Apply.
type Options struct {
	Dir              string // migrations root
	AllowDestructive bool   // explicit administrator approval for destructive steps
	Logf             func(string, ...any)
}

func (o Options) logf(f string, a ...any) {
	if o.Logf != nil {
		o.Logf(f, a...)
	}
}

// MySQL error numbers that mean "this DDL step already happened" when a dirty migration is resumed.
var mysqlAlready = map[uint16]bool{1050: true, 1060: true, 1061: true, 1091: true, 1826: true, 3822: true, 1068: true}

// Apply runs every pending migration under a server-side lock, so only one instance migrates at a time.
// PostgreSQL and SQL Server run each migration in one transaction (all or nothing); MySQL records every
// finished step, marks the migration dirty while it runs and can resume after a crash. Down migrations never run.
func Apply(ctx context.Context, d *sqldb.DB, o Options) error {
	p := d.Provider()
	lock, err := d.Begin(ctx)
	if err != nil {
		return err
	}
	defer lock.Rollback(context.Background())
	if err = lock.LockKey(ctx, "oa-migrate:"+d.Config().Fingerprint()); err != nil {
		return fmt.Errorf("migration lock: %w", err)
	}
	if err = ensureTracking(ctx, d, p); err != nil {
		return fmt.Errorf("migration bookkeeping: %w", err)
	}
	plan, err := MakePlan(ctx, d, o.Dir)
	if err != nil {
		return err
	}
	for _, pm := range plan.Pending {
		if pm.Destructive && !o.AllowDestructive {
			return fmt.Errorf("migration %s contains destructive statements and needs explicit administrator approval (allow-destructive)", pm.Name)
		}
	}
	for _, pm := range plan.Pending {
		o.logf("applying migration %s", pm.Name)
		var err error
		if p == sqldb.MySQL {
			err = applyStepwise(ctx, d, pm)
		} else {
			err = d.InTx(ctx, func(tx sqldb.Tx) error {
				for i, s := range pm.Steps {
					if _, err := tx.Exec(ctx, s); err != nil {
						return fmt.Errorf("apply migration %d step %d: %w", pm.Version, i+1, err)
					}
				}
				_, err := tx.Exec(ctx, `INSERT INTO schema_migrations(version, checksum) VALUES ($1, $2)`, pm.Version, pm.Checksum)
				return err
			})
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func applyStepwise(ctx context.Context, d *sqldb.DB, pm Pending) error {
	if !pm.Resume {
		if _, err := d.Exec(ctx, `INSERT INTO schema_migrations(version, checksum, state) VALUES ($1, $2, 'dirty')`, pm.Version, pm.Checksum); err != nil {
			return err
		}
	}
	done := map[int]bool{}
	rows, err := d.Query(ctx, `SELECT step FROM schema_migration_steps WHERE version=$1`, pm.Version)
	if err != nil {
		return err
	}
	for rows.Next() {
		var s int
		if err := rows.Scan(&s); err != nil {
			rows.Close()
			return err
		}
		done[s] = true
	}
	rows.Close()
	for i, s := range pm.Steps {
		if done[i] {
			continue
		}
		if _, err := d.Exec(ctx, s); err != nil {
			var me interface{ Error() string }
			_ = me
			if pm.Resume && mysqlAlreadyDone(err) {
				// the step ran before the crash but its marker was not written
			} else {
				return fmt.Errorf("apply migration %d step %d (the migration is marked dirty and resumes from this step on the next run): %w", pm.Version, i+1, err)
			}
		}
		if _, err := d.Exec(ctx, `INSERT INTO schema_migration_steps(version, step) VALUES ($1, $2)`, pm.Version, i); err != nil {
			return err
		}
	}
	_, err = d.Exec(ctx, `UPDATE schema_migrations SET state='applied', applied_at=now() WHERE version=$1`, pm.Version)
	return err
}

// ErrNeedsApproval is returned when schema changes are required but not approved.
var ErrNeedsApproval = errors.New("schema initialisation or migration needs explicit administrator approval")

// EnsureReadable verifies that schema_migrations (if present) can be read; used before planning.
func EnsureReadable(ctx context.Context, d *sqldb.DB) error {
	_, err := AppliedMigrations(ctx, d)
	return err
}
