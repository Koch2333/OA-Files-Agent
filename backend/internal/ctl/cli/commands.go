package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"

	"oa-agent/backend/internal/ctl/compose"
	"oa-agent/backend/internal/ctl/doctor"
	"oa-agent/backend/internal/ctl/ops"
)

func (a *App) dispatch(ctx context.Context, g global, cmd string, rest []string) error {
	switch cmd {
	case "status":
		return a.cmdStatus(ctx, g)
	case "ps":
		return a.cmdPS(ctx, g)
	case "start", "stop":
		return a.cmdStartStop(ctx, g, cmd)
	case "restart":
		return a.cmdRestart(ctx, g, rest)
	case "logs":
		return a.cmdLogs(ctx, g, rest)
	case "upgrade":
		return a.cmdUpgrade(ctx, g, rest)
	case "backup":
		return a.cmdBackup(ctx, g, rest)
	case "restore":
		return a.cmdRestore(ctx, g, rest)
	case "doctor":
		return a.cmdDoctor(ctx, g)
	case "setup-token":
		return a.cmdSetupToken(ctx, g)
	case "admin":
		return a.cmdAdmin(ctx, g, rest)
	case "users":
		return a.cmdUsers(ctx, g, rest)
	case "mounts":
		return a.cmdMounts(ctx, g, rest)
	case "config":
		return a.cmdConfig(ctx, g, rest)
	case "domain":
		return a.cmdDomain(ctx, g, rest)
	}
	return usage("未知命令 %q", cmd)
}

func (a *App) cmdVersion(ctx context.Context, g global) error {
	ver := a.Version
	if ver == "" {
		ver = "dev"
	}
	dir := g.dir
	inst, err := a.open(g)
	if err != nil {
		// version must work without an installation
		if g.json {
			return a.emitJSON(map[string]string{"oa_agentctl": ver})
		}
		a.printf("oa-agentctl %s\n", ver)
		_ = dir
		return nil
	}
	oav := inst.inst.Env.Value("OA_VERSION", "")
	if g.json {
		return a.emitJSON(map[string]string{"oa_agentctl": ver, "install_dir": inst.inst.Dir, "oa_version": oav})
	}
	a.printf("oa-agentctl %s\n安装目录: %s\nOA_VERSION: %s\n", ver, inst.inst.Dir, oav)
	if res, err := inst.p.Run(ctx, "ps", "--format", "{{.Service}}\t{{.Image}}"); err == nil && strings.TrimSpace(res.Stdout) != "" {
		a.printf("%s", res.Stdout)
	}
	return nil
}

func (a *App) cmdStatus(ctx context.Context, g global) error {
	c, err := a.open(g)
	if err != nil {
		return err
	}
	tbl, err := c.p.Table(ctx)
	if err != nil {
		return err
	}
	if g.json {
		type row struct {
			Service string `json:"service"`
			State   string `json:"state"`
			Health  string `json:"health"`
			Ports   string `json:"ports"`
			Uptime  string `json:"uptime"`
		}
		var rows []row
		allOK := true
		for _, s := range tbl {
			rows = append(rows, row{s.Name, s.State, s.Health, s.Ports, s.Uptime()})
			allOK = allOK && s.OK()
		}
		return a.emitJSON(map[string]any{"url": c.inst.URL(), "ok": allOK, "services": rows})
	}
	printServices(a.Out, tbl)
	a.printf("\n访问地址: %s\n", c.inst.URL())
	return nil
}

func printServices(w interface{ Write([]byte) (int, error) }, tbl []compose.Service) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "服务\t状态\t健康\t端口\t运行时间")
	for _, s := range tbl {
		st := s.State
		if st == "" {
			st = "missing"
		}
		h := s.Health
		if h == "" {
			h = "-"
		}
		p := s.Ports
		if p == "" {
			p = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", s.Name, st, h, p, s.Uptime())
	}
	tw.Flush()
}

func (a *App) cmdPS(ctx context.Context, g global) error {
	c, err := a.open(g)
	if err != nil {
		return err
	}
	svcs, err := c.p.PS(ctx)
	if err != nil {
		return err
	}
	if g.json {
		return a.emitJSON(svcs)
	}
	printServices(a.Out, svcs)
	return nil
}

func (a *App) cmdStartStop(ctx context.Context, g global, cmd string) error {
	c, err := a.open(g)
	if err != nil {
		return err
	}
	if g.dry {
		a.printf("演练：将执行 %s（docker compose %s）\n", cmd, map[string]string{"start": "up -d", "stop": "stop"}[cmd])
		return nil
	}
	if cmd == "start" {
		return c.env.Start(ctx, a.progress())
	}
	if err := a.confirm(g, "停止全部服务？"); err != nil {
		return err
	}
	return c.env.Stop(ctx, a.progress())
}

func (a *App) cmdRestart(ctx context.Context, g global, rest []string) error {
	if len(rest) > 1 {
		return usage("restart 最多接受一个服务名")
	}
	svc := ""
	if len(rest) == 1 {
		svc = rest[0]
		if !ops.ValidService(svc) {
			return usage("未知服务 %q（postgres api worker caddy）", svc)
		}
	}
	c, err := a.open(g)
	if err != nil {
		return err
	}
	if g.dry {
		a.printf("演练：将重启 %s\n", orAll(svc))
		return nil
	}
	return c.env.Restart(ctx, svc, a.progress())
}

func orAll(s string) string {
	if s == "" {
		return "全部服务"
	}
	return s
}

func (a *App) cmdLogs(ctx context.Context, g global, rest []string) error {
	follow := takeBool(&rest, "-f") || takeBool(&rest, "--follow")
	tail, _, err := takeValue(&rest, "--tail")
	if err != nil {
		return err
	}
	if tail == "" {
		tail = "200"
	}
	if n, e := strconv.Atoi(tail); e != nil || n < 0 {
		return usage("--tail 需要非负整数")
	}
	if len(rest) > 1 {
		return usage("logs 最多接受一个服务名")
	}
	args := []string{"logs", "--no-color", "--tail", tail}
	if follow {
		args = append(args, "--follow")
	}
	if len(rest) == 1 {
		if !ops.ValidService(rest[0]) {
			return usage("未知服务 %q", rest[0])
		}
		args = append(args, rest[0])
	}
	c, err := a.open(g)
	if err != nil {
		return err
	}
	return c.p.Stream(ctx, func(l string) { fmt.Fprintln(a.Out, l) }, args...)
}

func (a *App) cmdUpgrade(ctx context.Context, g global, rest []string) error {
	ver, _, err := takeValue(&rest, "--version")
	if err != nil {
		return err
	}
	tag, _, err := takeValue(&rest, "--tag")
	if err != nil {
		return err
	}
	if tag != "" {
		if ver != "" && ver != tag {
			return usage("--tag 与 --version 不能同时给出不同的值")
		}
		ver = tag
	}
	noBackup := takeBool(&rest, "--no-backup")
	if len(rest) > 0 {
		return usage("多余的参数 %v", rest)
	}
	c, err := a.open(g)
	if err != nil {
		return err
	}
	if g.dry {
		a.printf("演练：升级到版本/标签 %q（空 = 保持 .env 中的 OA_VERSION），先备份=%v，然后 pull（镜像模式，含更新管理文件）或 build，并重建 api 与 worker\n", ver, !noBackup)
		return nil
	}
	if err := a.confirm(g, "升级会重启服务，数据库迁移不会自动回退。继续？"); err != nil {
		return err
	}
	return c.env.Upgrade(ctx, ops.UpgradeOptions{Version: ver, NoBackup: noBackup}, a.progress())
}

func (a *App) cmdBackup(ctx context.Context, g global, rest []string) error {
	out, _, err := takeValue(&rest, "--out")
	if err != nil {
		return err
	}
	skip := takeBool(&rest, "--skip-archive")
	if len(rest) > 0 {
		return usage("多余的参数 %v", rest)
	}
	c, err := a.open(g)
	if err != nil {
		return err
	}
	if g.dry {
		a.printf("演练：将备份数据库、密钥%s到 %s\n", map[bool]string{true: "", false: "和归档目录"}[skip], orDefault(out, c.inst.Dir+"/backups"))
		return nil
	}
	file, err := c.env.Backup(ctx, ops.BackupOptions{OutDir: out, SkipArchive: skip}, a.progress())
	if err != nil {
		return err
	}
	if g.json {
		return a.emitJSON(map[string]string{"file": file})
	}
	return nil
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func (a *App) cmdRestore(ctx context.Context, g global, rest []string) error {
	if len(rest) != 1 {
		return usage("用法: restore <备份文件>")
	}
	c, err := a.open(g)
	if err != nil {
		return err
	}
	if err := ops.ValidateBackup(rest[0]); err != nil {
		return err
	}
	if g.dry {
		a.printf("演练：将用 %s 覆盖当前数据库与归档目录（旧归档目录会改名保留）\n", rest[0])
		return nil
	}
	if err := a.confirm(g, "还原会【覆盖】当前数据库与归档目录（旧归档目录改名保留为 archive.pre-restore-*）。继续？"); err != nil {
		return err
	}
	return c.env.Restore(ctx, rest[0], a.progress())
}

func (a *App) cmdDoctor(ctx context.Context, g global) error {
	c, err := a.open(g)
	if err != nil {
		return err
	}
	res := doctor.Run(ctx, doctor.Env{Inst: c.inst, P: c.p, Prober: c.env.Prober})
	fails, warns := doctor.Counts(res)
	if g.json {
		if err := a.emitJSON(map[string]any{"fails": fails, "warns": warns, "results": res}); err != nil {
			return err
		}
	} else {
		for _, r := range res {
			tag := map[doctor.Level]string{doctor.OK: "[ OK ]", doctor.Warn: "[WARN]", doctor.Fail: "[FAIL]"}[r.Level]
			a.printf("%s %s: %s\n", tag, r.Name, r.Detail)
			if r.Fix != "" && r.Level != doctor.OK {
				a.printf("         修复: %s\n", r.Fix)
			}
		}
		a.printf("\n诊断完成：%d 项失败，%d 项警告\n", fails, warns)
	}
	if fails > 0 {
		return fmt.Errorf("%d 项诊断失败", fails)
	}
	return nil
}

func (a *App) cmdSetupToken(ctx context.Context, g global) error {
	c, err := a.open(g)
	if err != nil {
		return err
	}
	tok, err := c.env.SetupToken(ctx)
	if err != nil {
		return err
	}
	if g.json {
		return a.emitJSON(map[string]string{"setup_token": tok})
	}
	a.printf("%s\n", tok)
	return nil
}

func (a *App) cmdAdmin(ctx context.Context, g global, rest []string) error {
	// rest are the oa-admin arguments (the word "admin" was consumed by dispatch)
	args := rest
	c, err := a.open(g)
	if err != nil {
		return err
	}
	running, err := c.p.Running(ctx)
	if err != nil {
		return err
	}
	up := false
	for _, n := range running {
		up = up || n == "api"
	}
	var cargs []string
	if up {
		cargs = []string{"exec"}
		if !a.tty() {
			cargs = append(cargs, "-T")
		}
		cargs = append(cargs, "api", "oa-admin")
	} else {
		cargs = []string{"run", "--rm"}
		if !a.tty() {
			cargs = append(cargs, "-T")
		}
		cargs = append(cargs, "api", "oa-admin")
	}
	cargs = append(cargs, args...)
	return c.p.R.Attach(ctx, c.p.Spec(cargs...), a.In, a.Out, a.Err)
}

func (a *App) cmdConfig(ctx context.Context, g global, rest []string) error {
	if len(rest) == 0 {
		return usage("用法: config show | config set <KEY> <值>")
	}
	c, err := a.open(g)
	if err != nil {
		return err
	}
	switch rest[0] {
	case "show":
		if g.json {
			m := map[string]string{}
			for _, kv := range c.inst.Env.Environ() {
				k, v, _ := strings.Cut(kv, "=")
				if envIsSecret(k) && v != "" {
					v = "********"
				}
				m[k] = v
			}
			return a.emitJSON(m)
		}
		fmt.Fprint(a.Out, c.inst.Env.MaskedText())
		return nil
	case "set":
		if len(rest) != 3 {
			return usage("用法: config set <KEY> <值>（值为 - 时读取标准输入）")
		}
		key, val := rest[1], rest[2]
		if val == "-" {
			var b strings.Builder
			buf := make([]byte, 4096)
			for {
				n, e := a.In.Read(buf)
				b.Write(buf[:n])
				if e != nil {
					break
				}
			}
			val = strings.TrimRight(b.String(), "\r\n")
		}
		if g.dry {
			if err := validateOnly(key, val); err != nil {
				return err
			}
			a.printf("演练：将设置 %s（%s）\n", key, shown(key, val))
			return nil
		}
		if err := c.env.ConfigSet(key, val); err != nil {
			return err
		}
		a.printf("已设置 %s；运行 oa-agentctl restart 使其生效\n", key)
		return nil
	}
	return usage("未知的 config 子命令 %q", rest[0])
}

func (a *App) cmdDomain(ctx context.Context, g global, rest []string) error {
	none := takeBool(&rest, "--none") || takeBool(&rest, "--off")
	noStart := takeBool(&rest, "--no-start")
	fqdn := ""
	if !none {
		if len(rest) != 1 {
			return usage("用法: domain <域名> | domain --none")
		}
		fqdn = rest[0]
	}
	c, err := a.open(g)
	if err != nil {
		return err
	}
	plan, err := c.env.PlanDomain(fqdn)
	if err != nil {
		return usage("%v", err)
	}
	if plan.InvalidatesKeys {
		fmt.Fprintln(a.Err, "警告：更换域名会使已注册的 Passkey 失效（绑定在旧域名上），需在 设置→安全 中更新 RP ID/来源；TOTP 不受影响。")
	}
	if g.dry {
		a.printf("演练：将切换为 %s，并%s\n", map[bool]string{true: "纯 HTTP", false: "HTTPS " + fqdn}[none], map[bool]string{true: "不重启", false: "重建容器"}[noStart])
		return nil
	}
	prompt := "切换到 HTTPS，域名 " + fqdn + "（需 80/443 对公网开放且域名已解析到本机）？"
	if none {
		prompt = "切换回仅 HTTP（Passkey 将不可用）？"
	}
	if err := a.confirm(g, prompt); err != nil {
		return err
	}
	return c.env.ApplyDomain(ctx, fqdn, a.hostIP(), noStart, a.progress())
}
