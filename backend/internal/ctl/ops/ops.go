// Package ops holds the multi-step stack operations shared by the CLI and the TUI.
// Every function reports progress through a callback and never prints by itself.
package ops

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"oa-agent/backend/internal/ctl/compose"
	"oa-agent/backend/internal/ctl/envfile"
	"oa-agent/backend/internal/ctl/fsutil"
	"oa-agent/backend/internal/ctl/install"
	"oa-agent/backend/internal/ctl/mounts"
	"oa-agent/backend/internal/ctl/oaadmin"
)

// Progress receives human-readable progress lines.
type Progress func(string)

func (p Progress) say(f string, a ...any) {
	if p != nil {
		p(fmt.Sprintf(f, a...))
	}
}

// Env bundles what operations need.
type Env struct {
	P        *compose.Project
	Getuid   func() int
	Timeout  time.Duration // health wait; default 10 minutes
	Prober   mounts.Prober
	Now      func() time.Time
	Stdin    io.Reader
	StderrTo io.Writer
}

func (e Env) timeout() time.Duration {
	if e.Timeout > 0 {
		return e.Timeout
	}
	return 10 * time.Minute
}

func (e Env) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e Env) isRoot() bool { return e.Getuid != nil && e.Getuid() == 0 }

// MountsConfig loads the effective mounts layout of the install.
func (e Env) MountsConfig() (mounts.Config, error) {
	c, _, err := mounts.Load(e.P.Inst.Dir, e.P.Inst.Env)
	return c, err
}

func (e Env) stream(ctx context.Context, pr Progress, args ...string) error {
	return e.P.Stream(ctx, func(l string) { pr.say("  %s", l) }, args...)
}

// FixOwnership repairs top-level owner/mode of core mounts when running as root (like the bash owner_fix).
func (e Env) FixOwnership(pr Progress) {
	if !e.isRoot() || e.Prober == nil {
		return
	}
	cfg, err := e.MountsConfig()
	if err != nil {
		return
	}
	for _, r := range mounts.CheckAll(cfg, e.Prober) {
		for _, f := range r.Fixes {
			if err := mounts.RunFix(f); err != nil {
				pr.say("无法修复 %s：%v", f.Path, err)
			} else {
				pr.say("已执行：%s", f.Command())
			}
		}
	}
}

// Start brings the stack up and waits for health.
func (e Env) Start(ctx context.Context, pr Progress) error {
	e.FixOwnership(pr)
	pr.say("docker compose up -d")
	if err := e.stream(ctx, pr, "up", "-d"); err != nil {
		return err
	}
	return e.wait(ctx, pr, e.P.Inst.Services())
}

func (e Env) wait(ctx context.Context, pr Progress, svcs []string) error {
	pr.say("等待服务健康：%s", strings.Join(svcs, " "))
	if err := e.P.WaitHealthy(ctx, svcs, e.timeout()); err != nil {
		return err
	}
	pr.say("全部服务正常")
	return nil
}

// Stop stops all services (containers are kept).
func (e Env) Stop(ctx context.Context, pr Progress) error {
	pr.say("docker compose stop")
	return e.stream(ctx, pr, "stop")
}

// Restart restarts everything or one service. api and worker always move together because the
// worker shares the api's network namespace.
func (e Env) Restart(ctx context.Context, svc string, pr Progress) error {
	switch svc {
	case "", "api", "worker":
		if err := e.stream(ctx, pr, "up", "-d", "--force-recreate", "api", "worker"); err != nil {
			return err
		}
		if svc == "" {
			args := []string{"restart", "postgres"}
			if e.P.Inst.HTTPS() {
				args = append(args, "caddy")
			}
			if err := e.stream(ctx, pr, args...); err != nil {
				return err
			}
		}
	default:
		if !validService(svc) {
			return fmt.Errorf("未知服务 %q", svc)
		}
		if err := e.stream(ctx, pr, "restart", svc); err != nil {
			return err
		}
	}
	return e.wait(ctx, pr, e.P.Inst.Services())
}

func validService(s string) bool {
	switch s {
	case "postgres", "api", "worker", "caddy":
		return true
	}
	return false
}

// ValidService is the exported check used by the CLI for log targets.
func ValidService(s string) bool { return validService(s) }

// SetupToken finds the first-run token in the api log or asks oa-admin.
var tokenRe = regexp.MustCompile(`SETUP TOKEN: *([A-Za-z0-9_-]+)`)

func (e Env) SetupToken(ctx context.Context) (string, error) {
	if res, err := e.P.Run(ctx, "logs", "--no-color", "api"); err == nil {
		if all := tokenRe.FindAllStringSubmatch(res.Stdout, -1); len(all) > 0 {
			return all[len(all)-1][1], nil
		}
	}
	c := oaadmin.Client{P: e.P}
	res, err := c.Run(ctx, "setup-token")
	if err != nil {
		return "", err
	}
	out := strings.TrimSpace(res.Stdout + res.Stderr)
	if out == "" {
		return "", errors.New("没有可用的初始化令牌（可能已完成初始化）")
	}
	return out, nil
}

// ConfigSet validates and writes one whitelisted key.
func (e Env) ConfigSet(key, val string) error {
	if err := envfile.ValidateConfig(key, val); err != nil {
		return err
	}
	inst := e.P.Inst
	if _, err := fsutil.Backup(inst.EnvPath(), 5); err != nil {
		return err
	}
	inst.Env.Set(key, val)
	return inst.Env.Save(inst.EnvPath())
}

// DomainPlan is what switching the domain would do.
type DomainPlan struct {
	FQDN            string // empty = back to HTTP
	OldDomain       string
	InvalidatesKeys bool // registered passkeys are bound to the old domain
}

// PlanDomain validates a domain switch without changing anything.
func (e Env) PlanDomain(fqdn string) (DomainPlan, error) {
	old := e.P.Inst.Env.Value("OA_DOMAIN", "")
	if fqdn != "" && !envfile.ValidDomain.MatchString(fqdn) {
		return DomainPlan{}, fmt.Errorf("域名格式不正确：%s", fqdn)
	}
	return DomainPlan{FQDN: fqdn, OldDomain: old, InvalidatesKeys: old != "" && old != fqdn}, nil
}

// ApplyDomain updates .env, renders the Caddyfile and (unless noStart) recreates the stack.
func (e Env) ApplyDomain(ctx context.Context, fqdn string, hostIP string, noStart bool, pr Progress) error {
	inst := e.P.Inst
	if _, err := fsutil.Backup(inst.EnvPath(), 5); err != nil {
		return err
	}
	if fqdn == "" {
		inst.Env.Set("COMPOSE_FILE", install.SetHTTPSLayer(inst.ComposeFile(), false))
		inst.Env.Set("COMPOSE_PROFILES", "")
		inst.Env.Set("OA_DOMAIN", "")
		inst.Env.Set("OA_BIND_ADDR", "0.0.0.0")
		inst.Env.Set("OA_TRUSTED_PROXY", "0")
		inst.Env.Set("OA_PUBLIC_URL", "http://"+hostIP+":"+inst.Port())
	} else {
		tpl, err := os.ReadFile(filepath.Join(inst.Dir, "Caddyfile.template"))
		if err != nil {
			return fmt.Errorf("缺少 Caddyfile.template：%w", err)
		}
		email := inst.Env.Value("OA_ACME_EMAIL", "")
		line := ""
		if email != "" {
			line = "email " + email
		}
		cf := strings.NewReplacer("__DOMAIN__", fqdn, "__EMAIL_LINE__", line).Replace(string(tpl))
		if err := fsutil.WriteAtomic(filepath.Join(inst.Dir, "Caddyfile"), []byte(cf), 0o644); err != nil {
			return err
		}
		inst.Env.Set("COMPOSE_FILE", install.SetHTTPSLayer(inst.ComposeFile(), true))
		inst.Env.Set("OA_DOMAIN", fqdn)
		inst.Env.Set("OA_BIND_ADDR", "127.0.0.1")
		inst.Env.Set("OA_TRUSTED_PROXY", "1")
		inst.Env.Set("OA_PUBLIC_URL", "https://"+fqdn)
	}
	if err := inst.Env.Save(inst.EnvPath()); err != nil {
		return err
	}
	pr.say("配置已更新")
	if noStart {
		return nil
	}
	if err := e.stream(ctx, pr, "up", "-d", "--force-recreate", "--remove-orphans"); err != nil {
		return err
	}
	return e.wait(ctx, pr, inst.Services())
}

// ------------------------------------------------------------------ backup / restore

// BackupOptions controls Backup.
type BackupOptions struct {
	OutDir      string // default <install>/backups
	SkipArchive bool
}

// Backup writes a tar.gz compatible with the bash tool: ./db.dump, ./env, ./app/secret.key, ./archive/..., ./MANIFEST.
func (e Env) Backup(ctx context.Context, o BackupOptions, pr Progress) (string, error) {
	inst := e.P.Inst
	out := o.OutDir
	if out == "" {
		out = filepath.Join(inst.Dir, "backups")
	}
	if err := os.MkdirAll(out, 0o700); err != nil {
		return "", err
	}
	running, err := e.P.Running(ctx)
	if err != nil {
		return "", err
	}
	if !contains(running, "postgres") {
		return "", errors.New("postgres 未运行；先执行 oa-agentctl start")
	}
	cfg, err := e.MountsConfig()
	if err != nil {
		return "", err
	}
	stamp := e.now().Format("20060102-150405")
	dump, err := os.CreateTemp(out, ".db-dump-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(dump.Name())
	_ = dump.Chmod(0o600)
	pr.say("导出数据库 …")
	var stderr strings.Builder
	spec := e.P.Spec("exec", "-T", "postgres", "pg_dump", "-U", "oa", "-d", "oa", "-Fc")
	if err := e.P.R.Attach(ctx, spec, nil, dump, &stderr); err != nil {
		dump.Close()
		return "", fmt.Errorf("pg_dump 失败：%w %s", err, strings.TrimSpace(stderr.String()))
	}
	if err := dump.Close(); err != nil {
		return "", err
	}
	if st, err := os.Stat(dump.Name()); err != nil || st.Size() == 0 {
		return "", errors.New("pg_dump 输出为空")
	}

	file := filepath.Join(out, "oa-agent-backup-"+stamp+".tar.gz")
	tmp := file + ".part"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	ok := false
	defer func() {
		if !ok {
			f.Close()
			os.Remove(tmp)
		}
	}()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	pr.say("打包文件 …")
	if err := addFile(tw, "./db.dump", dump.Name(), 0o600); err != nil {
		return "", err
	}
	if err := addFile(tw, "./env", inst.EnvPath(), 0o600); err != nil {
		return "", err
	}
	if k := filepath.Join(cfg.App.Host, "secret.key"); fileExists(k) {
		if err := addFile(tw, "./app/secret.key", k, 0o600); err != nil {
			return "", err
		}
	}
	if !o.SkipArchive && dirExists(cfg.Archive.Host) {
		if err := addTree(tw, "./archive", cfg.Archive.Host); err != nil {
			return "", err
		}
	}
	manifest := fmt.Sprintf("oa-agent backup\ncreated=%s\nversion=%s\nskip_archive=%v\n", e.now().UTC().Format(time.RFC3339), inst.Env.Value("OA_VERSION", ""), o.SkipArchive)
	if err := addBytes(tw, "./MANIFEST", []byte(manifest), 0o644); err != nil {
		return "", err
	}
	if err := tw.Close(); err != nil {
		return "", err
	}
	if err := gz.Close(); err != nil {
		return "", err
	}
	if err := f.Sync(); err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, file); err != nil {
		return "", err
	}
	ok = true
	pr.say("备份完成：%s", file)
	pr.say("注意：备份含加密密钥与数据库口令，请像密码一样保管。")
	return file, nil
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
func fileExists(p string) bool { st, err := os.Stat(p); return err == nil && st.Mode().IsRegular() }
func dirExists(p string) bool  { st, err := os.Stat(p); return err == nil && st.IsDir() }

func addBytes(tw *tar.Writer, name string, b []byte, mode int64) error {
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(b)), ModTime: fsutil.Now(), Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	_, err := tw.Write(b)
	return err
}

func addFile(tw *tar.Writer, name, src string, mode int64) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: st.Size(), ModTime: st.ModTime(), Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	return err
}

// addTree adds regular files and directories (symlinks are skipped: a restore must never recreate links).
func addTree(tw *tar.Writer, prefix, root string) error {
	return filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		name := prefix
		if rel != "." {
			name = prefix + "/" + filepath.ToSlash(rel)
		}
		switch {
		case d.IsDir():
			return tw.WriteHeader(&tar.Header{Name: name + "/", Mode: 0o750, Typeflag: tar.TypeDir, ModTime: fsutil.Now()})
		case d.Type().IsRegular():
			return addFile(tw, name, p, 0o640)
		}
		return nil
	})
}

// extractTar unpacks a backup. Entries under ./archive/ go to archiveDir, the rest to stage.
// Names that escape their root, links and device nodes are rejected.
func extractTar(file, stage, archiveDir string) (hasArchive bool, err error) {
	f, err := os.Open(file)
	if err != nil {
		return false, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return false, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return hasArchive, nil
		}
		if err != nil {
			return hasArchive, err
		}
		name := path.Clean("/" + strings.TrimPrefix(h.Name, "./"))[1:] // clean, no leading slash, no ..
		if name == "" || name == "." {
			continue
		}
		root, rel := stage, name
		if name == "archive" || strings.HasPrefix(name, "archive/") {
			root, rel = archiveDir, strings.TrimPrefix(strings.TrimPrefix(name, "archive"), "/")
			hasArchive = true
		}
		dst := filepath.Join(root, filepath.FromSlash(rel))
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dst, 0o750); err != nil {
				return hasArchive, err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
				return hasArchive, err
			}
			w, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(h.Mode)&0o777|0o200)
			if err != nil {
				return hasArchive, err
			}
			if _, err := io.Copy(w, tr); err != nil {
				w.Close()
				return hasArchive, err
			}
			if err := w.Close(); err != nil {
				return hasArchive, err
			}
		default: // symlinks, hardlinks, devices: ignored on purpose
		}
	}
}

// ValidateBackup checks that the tarball has a database dump.
func ValidateBackup(file string) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("不是有效的 gzip 文件：%w", err)
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err != nil {
			return errors.New("不是有效的 OA 备份（缺少 db.dump）")
		}
		if strings.TrimPrefix(h.Name, "./") == "db.dump" {
			return nil
		}
	}
}

// Restore overwrites the database and archive from a backup. The previous archive directory is
// renamed to archive.pre-restore-<time>, never deleted. The caller must have confirmed.
func (e Env) Restore(ctx context.Context, file string, pr Progress) error {
	if err := ValidateBackup(file); err != nil {
		return err
	}
	inst := e.P.Inst
	cfg, err := e.MountsConfig()
	if err != nil {
		return err
	}
	stamp := e.now().Format("20060102-150405")
	stage, err := os.MkdirTemp(inst.Dir, ".restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	archiveParent := filepath.Dir(cfg.Archive.Host)
	newArchive := filepath.Join(archiveParent, ".archive.restore-"+stamp)
	hasArchive, err := extractTar(file, stage, newArchive)
	if err != nil {
		os.RemoveAll(newArchive)
		return fmt.Errorf("解包失败：%w", err)
	}
	if hasArchive {
		_ = os.MkdirAll(newArchive, 0o750)
	}
	dump := filepath.Join(stage, "db.dump")
	if st, err := os.Stat(dump); err != nil || st.Size() == 0 {
		os.RemoveAll(newArchive)
		return errors.New("db.dump 为空")
	}
	pr.say("停止 api / worker …")
	_ = e.stream(ctx, pr, "stop", "worker", "api")
	if err := e.stream(ctx, pr, "up", "-d", "postgres"); err != nil {
		return err
	}
	if err := e.P.WaitHealthy(ctx, []string{"postgres"}, 3*time.Minute); err != nil {
		return err
	}
	pr.say("还原数据库 …")
	psql := func(sql string) error {
		_, err := e.P.Run(ctx, "exec", "-T", "postgres", "psql", "-U", "oa", "-d", "postgres", "-v", "ON_ERROR_STOP=1", "-c", sql)
		return err
	}
	if err := psql("DROP DATABASE IF EXISTS oa WITH (FORCE)"); err != nil {
		return err
	}
	if err := psql("CREATE DATABASE oa OWNER oa"); err != nil {
		return err
	}
	df, err := os.Open(dump)
	if err != nil {
		return err
	}
	var se strings.Builder
	err = e.P.R.Attach(ctx, e.P.Spec("exec", "-T", "postgres", "pg_restore", "-U", "oa", "-d", "oa", "--no-owner", "--exit-on-error"), df, io.Discard, &se)
	df.Close()
	if err != nil {
		return fmt.Errorf("pg_restore 失败：%w %s", err, strings.TrimSpace(se.String()))
	}
	if hasArchive {
		pr.say("还原归档文件 …")
		if dirExists(cfg.Archive.Host) {
			old := cfg.Archive.Host + ".pre-restore-" + stamp
			if err := os.Rename(cfg.Archive.Host, old); err != nil {
				return fmt.Errorf("无法保留旧归档目录：%w", err)
			}
			pr.say("旧归档目录已改名为 %s", old)
		}
		if err := os.Rename(newArchive, cfg.Archive.Host); err != nil {
			return err
		}
	} else {
		pr.say("备份不含归档文件，保留当前归档目录。")
	}
	if b, err := os.ReadFile(filepath.Join(stage, "app", "secret.key")); err == nil {
		if err := os.MkdirAll(cfg.App.Host, 0o750); err == nil {
			_ = fsutil.WriteAtomic(filepath.Join(cfg.App.Host, "secret.key"), b, 0o600)
		}
	}
	if b, err := os.ReadFile(filepath.Join(stage, "env")); err == nil {
		old := envfile.Parse(b).Value("OA_SECRET_KEY", "")
		if old != "" && old != inst.Env.Value("OA_SECRET_KEY", "") {
			pr.say("备份中的 OA_SECRET_KEY 与当前不同：采用备份中的密钥（否则已保存的凭据无法解密）。")
			inst.Env.Set("OA_SECRET_KEY", old)
			if err := inst.Env.Save(inst.EnvPath()); err != nil {
				return err
			}
		}
	}
	e.FixOwnership(pr)
	if err := e.stream(ctx, pr, "up", "-d"); err != nil {
		return err
	}
	if err := e.wait(ctx, pr, inst.Services()); err != nil {
		return err
	}
	pr.say("还原完成")
	return nil
}

// ------------------------------------------------------------------ upgrade

// UpgradeOptions controls Upgrade.
type UpgradeOptions struct {
	Version  string
	NoBackup bool
}

// Upgrade backs up, pulls (or builds) and restarts. DB migrations are never reverted automatically.
func (e Env) Upgrade(ctx context.Context, o UpgradeOptions, pr Progress) error {
	inst := e.P.Inst
	old := inst.Env.Value("OA_VERSION", "")
	if o.Version != "" {
		if err := envfile.ValidateConfig("OA_VERSION", o.Version); err != nil {
			return err
		}
	}
	if !o.NoBackup {
		pr.say("升级前先备份 …")
		if _, err := e.Backup(ctx, BackupOptions{}, pr); err != nil {
			return fmt.Errorf("升级前备份失败（可用 --no-backup 跳过）：%w", err)
		}
	}
	if o.Version != "" {
		if err := e.ConfigSet("OA_VERSION", o.Version); err != nil {
			return err
		}
	}
	if strings.Contains(inst.ComposeFile(), "docker-compose.build.yml") {
		pr.say("从源码构建镜像 …")
		if err := e.stream(ctx, pr, "build", "--pull"); err != nil {
			return err
		}
	} else {
		pr.say("拉取镜像 …")
		if err := e.stream(ctx, pr, "pull"); err != nil {
			return err
		}
	}
	e.FixOwnership(pr)
	if err := e.stream(ctx, pr, "up", "-d", "--remove-orphans"); err != nil {
		return err
	}
	if err := e.wait(ctx, pr, inst.Services()); err != nil {
		hint := old
		if hint == "" {
			hint = "<旧版本>"
		}
		return fmt.Errorf("升级后服务未就绪：%w。回滚办法：oa-agentctl config set OA_VERSION %s；oa-agentctl restore <升级前备份>（数据库迁移不会自动回退）", err, hint)
	}
	pr.say("升级完成")
	return nil
}
