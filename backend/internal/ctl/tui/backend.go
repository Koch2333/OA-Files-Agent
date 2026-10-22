package tui

import (
	"context"
	"path/filepath"
	"sort"
	"strings"

	"oa-agent/backend/internal/ctl/apply"
	"oa-agent/backend/internal/ctl/cli"
	"oa-agent/backend/internal/ctl/compose"
	"oa-agent/backend/internal/ctl/doctor"
	"oa-agent/backend/internal/ctl/envfile"
	"oa-agent/backend/internal/ctl/mounts"
	"oa-agent/backend/internal/ctl/oaadmin"
	"oa-agent/backend/internal/ctl/ops"
)

// Backend is the TUI's view of the installation. The screens only call these methods,
// which in turn use the same logic packages as the command line.
type Backend struct {
	S      *cli.Session
	Ctx    context.Context
	HostIP string
	// ClockSync overrides the NTP check (tests); nil uses timedatectl.
	ClockSync func() string
}

func (b *Backend) admin() *oaadmin.Client { return &oaadmin.Client{P: b.S.P} }

func (b *Backend) Dir() string { return b.S.Inst.Dir }

func (b *Backend) Services(ctx context.Context) ([]compose.Service, error) { return b.S.P.Table(ctx) }

// SetupStatus returns the first line of `oa-admin status`, which reports whether setup is complete.
func (b *Backend) SetupStatus(ctx context.Context) string {
	res, err := b.admin().Run(ctx, "status")
	if err != nil {
		return "未知（oa-admin 不可用）"
	}
	for _, l := range strings.Split(res.Stdout+res.Stderr, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return "未知"
}

func (b *Backend) URL() string { return b.S.Inst.URL() }

// DiskRow is one mount's usage for the dashboard.
type DiskRow struct {
	Label       string
	Host        string
	Free, Total uint64
	Level       string
}

func (b *Backend) Disks() []DiskRow {
	cfg, _, err := mounts.Load(b.S.Inst.Dir, b.S.Inst.Env)
	if err != nil {
		return nil
	}
	var out []DiskRow
	for _, r := range mounts.CheckAll(cfg, b.S.Prober) {
		lv := "ok"
		for _, is := range r.Issues {
			if is.Sev == mounts.Error {
				lv = "error"
			} else if is.Sev == mounts.Warn && lv == "ok" {
				lv = "warn"
			}
		}
		out = append(out, DiskRow{r.Entry.Label(), r.Entry.Host, r.Probe.FreeBytes, r.Probe.TotalBytes, lv})
	}
	return out
}

func (b *Backend) Users(ctx context.Context) ([]oaadmin.User, error) { return b.admin().ListUsers(ctx) }
func (b *Backend) ResetPassword(ctx context.Context, u string) (oaadmin.Secret, error) {
	return b.admin().ResetPassword(ctx, u)
}
func (b *Backend) CreateAdmin(ctx context.Context, u string) (oaadmin.Secret, error) {
	return b.admin().CreateAdmin(ctx, u)
}
func (b *Backend) UserAction(ctx context.Context, sub, u string) (string, error) {
	return b.admin().Simple(ctx, sub, u)
}

func (b *Backend) Mounts() (mounts.Config, bool, error) {
	return mounts.Load(b.S.Inst.Dir, b.S.Inst.Env)
}

func (b *Backend) Assess(ctx context.Context, prev, next mounts.Config) ops.Assessment {
	return b.S.Env.AssessMounts(ctx, prev, next)
}

func (b *Backend) Probe(path string) mounts.Probe {
	p, _ := b.S.Prober.Probe(path)
	return p
}

// ApplyMounts writes the new layout; with restart it recreates api+worker and rolls back on failure.
func (b *Backend) ApplyMounts(ctx context.Context, prev, next mounts.Config, restart bool, fixes []mounts.Fix, emit func(string)) error {
	if len(fixes) > 0 && b.S.Env.Getuid != nil && b.S.Env.Getuid() == 0 {
		if err := ops.RunFixes(fixes, emit); err != nil {
			return err
		}
	}
	_, err := apply.Apply(ctx, b.S.P, prev, next, apply.Options{Restart: restart}, emit)
	return err
}

func (b *Backend) ServiceAction(ctx context.Context, action string, emit func(string)) error {
	switch action {
	case "start":
		return b.S.Env.Start(ctx, emit)
	case "stop":
		return b.S.Env.Stop(ctx, emit)
	case "restart":
		return b.S.Env.Restart(ctx, "", emit)
	case "restart-app":
		return b.S.Env.Restart(ctx, "api", emit)
	case "upgrade":
		return b.S.Env.Upgrade(ctx, ops.UpgradeOptions{}, emit)
	}
	return nil
}

func (b *Backend) Logs(ctx context.Context, svc string, tail int, emit func(string)) error {
	args := []string{"logs", "--no-color", "--tail", itoa(tail), "--follow"}
	if svc != "" {
		args = append(args, svc)
	}
	return b.S.P.Stream(ctx, emit, args...)
}

func (b *Backend) Backup(ctx context.Context, skipArchive bool, emit func(string)) error {
	_, err := b.S.Env.Backup(ctx, ops.BackupOptions{SkipArchive: skipArchive}, emit)
	return err
}

func (b *Backend) Restore(ctx context.Context, file string, emit func(string)) error {
	return b.S.Env.Restore(ctx, file, emit)
}

// BackupFiles lists backups newest first.
func (b *Backend) BackupFiles() []string {
	m, _ := filepath.Glob(filepath.Join(b.S.Inst.Dir, "backups", "oa-agent-backup-*.tar.gz"))
	sort.Sort(sort.Reverse(sort.StringSlice(m)))
	return m
}

// ConfigRows returns the masked .env assignments (key, display value).
func (b *Backend) ConfigRows() [][2]string {
	var out [][2]string
	for _, kv := range b.S.Inst.Env.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		if envfile.IsSecret(k) && v != "" {
			v = "******** (hidden)"
		}
		out = append(out, [2]string{k, v})
	}
	return out
}

func (b *Backend) ConfigSet(k, v string) error { return b.S.Env.ConfigSet(k, v) }

func (b *Backend) PlanDomain(fqdn string) (ops.DomainPlan, error) { return b.S.Env.PlanDomain(fqdn) }

func (b *Backend) ApplyDomain(ctx context.Context, fqdn string, emit func(string)) error {
	return b.S.Env.ApplyDomain(ctx, fqdn, b.HostIP, false, emit)
}

func (b *Backend) Doctor(ctx context.Context) []doctor.Result {
	return doctor.Run(ctx, doctor.Env{Inst: b.S.Inst, P: b.S.P, Prober: b.S.Prober, ClockSync: b.clock()})
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var s []byte
	for ; n > 0; n /= 10 {
		s = append([]byte{byte('0' + n%10)}, s...)
	}
	return string(s)
}

func (b *Backend) clock() func() string {
	if b.ClockSync != nil {
		return b.ClockSync
	}
	return doctor.DefaultClockSync
}
