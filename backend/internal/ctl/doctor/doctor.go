// Package doctor runs read-only diagnostics of an installation.
package doctor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"oa-agent/backend/internal/ctl/compose"
	"oa-agent/backend/internal/ctl/install"
	"oa-agent/backend/internal/ctl/mounts"
)

type Level int

const (
	OK Level = iota
	Warn
	Fail
)

func (l Level) String() string { return [...]string{"ok", "warn", "fail"}[l] }

// Result is one diagnostic line.
type Result struct {
	Name   string `json:"name"`
	Level  Level  `json:"-"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

// Env carries everything the checks depend on, so tests can replace each piece.
type Env struct {
	Inst   *install.Install
	P      *compose.Project
	Prober mounts.Prober
	// EnvMode returns the permission bits of .env.
	EnvMode func(path string) (os.FileMode, error)
	// ClockSync returns "yes", "no" or "" (unknown).
	ClockSync func() string
}

// DefaultClockSync asks timedatectl; unknown on systems without it.
func DefaultClockSync() string {
	if _, err := exec.LookPath("timedatectl"); err != nil {
		return ""
	}
	out, err := exec.Command("timedatectl", "show", "-p", "NTPSynchronized", "--value").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

var (
	hex64 = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
)

// Run executes all checks. It never modifies anything.
func Run(ctx context.Context, e Env) []Result {
	var out []Result
	add := func(l Level, name, detail, fix string) {
		out = append(out, Result{Name: name, Level: l, Status: l.String(), Detail: detail, Fix: fix})
	}

	// docker
	if _, err := e.P.R.Run(ctx, compose.Spec{Args: []string{"version", "--format", "{{.Server.Version}}"}}); err != nil {
		add(Fail, "docker", fmt.Sprintf("docker 不可用：%v", err), "确认 docker 已安装且守护进程在运行（systemctl status docker）")
		return out
	}
	add(OK, "docker", "Docker 守护进程运行中", "")
	if _, err := e.P.R.Run(ctx, compose.Spec{Args: []string{"compose", "version"}}); err != nil {
		add(Fail, "compose", "缺少 Docker Compose v2（docker compose）", "安装 docker-compose-plugin")
	} else {
		add(OK, "compose", "Docker Compose v2 可用", "")
	}

	// .env
	mode := e.EnvMode
	if mode == nil {
		mode = func(p string) (os.FileMode, error) {
			st, err := os.Stat(p)
			if err != nil {
				return 0, err
			}
			return st.Mode().Perm(), nil
		}
	}
	if m, err := mode(e.Inst.EnvPath()); err == nil {
		if m&0o077 != 0 {
			add(Warn, "env-mode", fmt.Sprintf(".env 权限为 %04o，应为 0600", m), "chmod 600 "+e.Inst.EnvPath())
		} else {
			add(OK, "env-mode", ".env 权限 0600", "")
		}
	}
	if v := e.Inst.Env.Value("OA_SECRET_KEY", ""); !hex64.MatchString(v) {
		add(Fail, "secret-key", "OA_SECRET_KEY 不是 64 位十六进制", "")
	} else {
		add(OK, "secret-key", "OA_SECRET_KEY 格式正确", "")
	}
	if v := e.Inst.Env.Value("OA_WORKER_TOKEN", ""); len(v) < 32 {
		add(Fail, "worker-token", "OA_WORKER_TOKEN 少于 32 字符", "")
	} else {
		add(OK, "worker-token", "OA_WORKER_TOKEN 长度足够", "")
	}

	// services
	table, err := e.P.Table(ctx)
	if err != nil {
		add(Fail, "services", fmt.Sprintf("无法读取容器状态：%v", err), "")
	} else {
		for _, s := range table {
			if s.OK() {
				add(OK, "service:"+s.Name, s.Effective(), "")
			} else {
				add(Fail, "service:"+s.Name, s.Effective(), "oa-agentctl logs "+s.Name+" --tail 100")
			}
		}
		port := e.Inst.Port()
		found := false
		for _, s := range table {
			if s.Name == "api" && strings.Contains(s.Ports, ":"+port+"->") {
				found = true
			}
		}
		if found {
			add(OK, "port", "API 已发布在端口 "+port, "")
		} else {
			add(Fail, "port", "API 容器没有发布端口 "+port, "oa-agentctl restart")
		}
	}
	if e.P.Inst.ExternalDB() {
		add(OK, "database", "数据库：外部服务器 "+e.P.Inst.Env.Value("OA_DB_PROVIDER", "?")+" "+e.P.Inst.Env.Value("OA_DB_HOST", "?")+"（连接状态由 API 健康检查覆盖；备份请用数据库自带的工具）", "")
	} else if _, err := e.P.Run(ctx, "exec", "-T", "postgres", "psql", "-U", "oa", "-d", "oa", "-tAc", "select 1"); err != nil {
		add(Fail, "database", "无法连接数据库", "oa-agentctl logs postgres --tail 100")
	} else {
		add(OK, "database", "数据库连接正常", "")
	}

	// mounts
	cfg, fromFile, err := mounts.Load(e.Inst.Dir, e.Inst.Env)
	if err != nil {
		add(Fail, "mounts.json", err.Error(), "")
	} else {
		if !fromFile {
			add(Warn, "mounts.json", "没有 mounts.json，使用默认目录（oa-agentctl mounts apply 可生成）", "")
		}
		opt := mounts.Options{InstallDir: e.Inst.Dir}
		for _, is := range mounts.Validate(cfg, opt) {
			l := Warn
			if is.Sev == mounts.Error {
				l = Fail
			}
			add(l, "mounts:"+is.Subject, is.Text, is.Fix)
		}
		for _, r := range mounts.CheckAll(cfg, e.Prober) {
			if len(r.Issues) == 0 {
				add(OK, "mount:"+r.Entry.Label(), r.Entry.Host, "")
			}
			for _, is := range r.Issues {
				l := OK
				switch is.Sev {
				case mounts.Warn:
					l = Warn
				case mounts.Error:
					l = Fail
				}
				add(l, "mount:"+r.Entry.Label(), is.Text, is.Fix)
			}
		}
		if fromFile {
			want, rerr := mounts.Render(cfg, opt)
			have, herr := os.ReadFile(e.Inst.OverridePath())
			switch {
			case rerr != nil:
			case herr != nil || string(have) != string(want):
				add(Warn, "override", "docker-compose.mounts.yml 与 mounts.json 不一致", "oa-agentctl mounts apply --restart")
			default:
				add(OK, "override", "docker-compose.mounts.yml 与 mounts.json 一致", "")
			}
			if !strings.Contains(e.Inst.ComposeFile(), install.OverrideFile) {
				add(Fail, "compose-file", "COMPOSE_FILE 没有包含 "+install.OverrideFile, "oa-agentctl mounts apply")
			}
		}
	}

	if !e.Inst.HTTPS() {
		add(Warn, "https", "纯 HTTP 部署：Passkey 不可用（仅 TOTP），口令明文传输", "oa-agentctl domain <域名>")
	}
	switch e.clock() {
	case "yes":
		add(OK, "clock", "系统时钟已同步", "")
	case "no":
		add(Warn, "clock", "系统时钟未同步：TOTP 依赖准确时间", "启用 chrony 或 systemd-timesyncd")
	default:
		add(Warn, "clock", "无法确认时间同步状态（TOTP 依赖准确时间）", "")
	}
	return out
}

func (e Env) clock() string {
	if e.ClockSync != nil {
		return e.ClockSync()
	}
	return DefaultClockSync()
}

// Counts summarises results.
func Counts(rs []Result) (fails, warns int) {
	for _, r := range rs {
		switch r.Level {
		case Fail:
			fails++
		case Warn:
			warns++
		}
	}
	return
}
