package cli

import (
	"context"
	"fmt"
	"strings"
	"text/tabwriter"

	"oa-agent/backend/internal/ctl/apply"
	"oa-agent/backend/internal/ctl/mounts"
	"oa-agent/backend/internal/ctl/ops"
)

func (a *App) cmdMounts(ctx context.Context, g global, rest []string) error {
	if len(rest) == 0 {
		return usage("用法: mounts list|check|add|remove|set|apply")
	}
	c, err := a.open(g)
	if err != nil {
		return err
	}
	cur, fromFile, err := mounts.Load(c.inst.Dir, c.inst.Env)
	if err != nil {
		return err
	}
	sub, args := rest[0], rest[1:]
	opt := mounts.Options{InstallDir: c.inst.Dir}
	switch sub {
	case "list":
		return a.mountsList(c, cur, fromFile)
	case "check":
		return a.mountsCheck(c, cur, takeBool(&args, "--fix"))
	case "apply":
		restart := takeBool(&args, "--restart")
		if len(args) > 0 {
			return usage("多余的参数 %v", args)
		}
		// apply regenerates from mounts.json without changing it
		return a.mountsMutate(ctx, c, cur, cur, mutateFlags{restart: restart, force: true})
	case "add":
		ro := !takeBool(&args, "--rw")
		takeBool(&args, "--ro")
		replace := takeBool(&args, "--replace")
		accept := takeBool(&args, "--accept-data-move")
		restart := takeBool(&args, "--restart")
		if len(args) != 2 {
			return usage("用法: mounts add <名称> <主机路径> [--ro|--rw] [--replace] [--accept-data-move] [--restart]")
		}
		next, issues := mounts.AddExtra(cur, args[0], args[1], ro, replace, opt)
		if mounts.HasErrors(issues) {
			a.printIssues(issues)
			return fmt.Errorf("校验未通过")
		}
		return a.mountsMutate(ctx, c, cur, next, mutateFlags{restart: restart, accept: accept})
	case "remove":
		restart := takeBool(&args, "--restart")
		if len(args) != 1 {
			return usage("用法: mounts remove <名称> [--restart]")
		}
		next, err := mounts.RemoveExtra(cur, args[0])
		if err != nil {
			return err
		}
		return a.mountsMutate(ctx, c, cur, next, mutateFlags{restart: restart})
	case "set":
		restart := takeBool(&args, "--restart")
		cp := takeBool(&args, "--copy")
		accept := takeBool(&args, "--accept-data-move")
		if len(args) != 2 {
			return usage("用法: mounts set <archive|spool|app|pgdata> <主机路径> [--copy] [--accept-data-move] [--restart]")
		}
		next, issues, err := mounts.SetCore(cur, args[0], args[1], opt)
		if err != nil {
			return usage("%v", err)
		}
		if mounts.HasErrors(issues) {
			a.printIssues(issues)
			return fmt.Errorf("校验未通过")
		}
		return a.mountsMutate(ctx, c, cur, next, mutateFlags{restart: restart, copy: cp, accept: accept, role: args[0]})
	}
	return usage("未知的 mounts 子命令 %q", sub)
}

func (a *App) printIssues(is []mounts.Issue) {
	for _, i := range is {
		fmt.Fprintln(a.Err, i.String())
		if i.Fix != "" {
			fmt.Fprintf(a.Err, "    修复命令: %s\n", i.Fix)
		}
	}
}

func human(b uint64) string {
	if b == 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f GiB", float64(b)/(1<<30))
}

func (a *App) mountsList(c *ctxt, cur mounts.Config, fromFile bool) error {
	type row struct {
		Name      string `json:"name"`
		Role      string `json:"role"`
		Host      string `json:"host"`
		Container string `json:"container"`
		Mode      string `json:"mode"`
		FreeBytes uint64 `json:"free_bytes"`
		FSType    string `json:"fs_type"`
		Status    string `json:"status"`
	}
	var rows []row
	for _, r := range mounts.CheckAll(cur, c.env.Prober) {
		mode := "rw"
		if r.Entry.ReadOnly {
			mode = "ro"
		}
		status := "ok"
		for _, i := range r.Issues {
			if i.Sev == mounts.Error {
				status = "error"
			} else if i.Sev == mounts.Warn && status == "ok" {
				status = "warn"
			}
		}
		rows = append(rows, row{r.Entry.Label(), r.Entry.Role, r.Entry.Host, r.Entry.Container, mode, r.Probe.FreeBytes, r.Probe.FSType, status})
	}
	if c.g.json {
		return a.emitJSON(map[string]any{"from_file": fromFile, "mounts": rows})
	}
	tw := tabwriter.NewWriter(a.Out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "名称\t主机路径\t容器路径\t模式\t剩余\t文件系统\t状态")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.Name, r.Host, r.Container, r.Mode, human(r.FreeBytes), orDash(r.FSType), r.Status)
	}
	tw.Flush()
	if !fromFile {
		a.printf("\n（没有 mounts.json，以上是 .env 中的默认目录；第一次修改或 mounts apply 时会创建它）\n")
	}
	return nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func (a *App) mountsCheck(c *ctxt, cur mounts.Config, fix bool) error {
	opt := mounts.Options{InstallDir: c.inst.Dir}
	issues := mounts.Validate(cur, opt)
	var fixes []mounts.Fix
	for _, r := range mounts.CheckAll(cur, c.env.Prober) {
		issues = append(issues, r.Issues...)
		fixes = append(fixes, r.Fixes...)
	}
	a.printIssuesOut(issues)
	if fix && len(fixes) > 0 {
		if a.getuid() != 0 {
			fmt.Fprintln(a.Err, "--fix 需要 root；请手动执行上面列出的命令。")
			return fmt.Errorf("需要 root")
		}
		if c.g.dry {
			for _, f := range fixes {
				a.printf("演练：%s\n", f.Command())
			}
			return nil
		}
		if err := a.confirm(c.g, fmt.Sprintf("对 %d 个目录执行属主/权限修复（chown -R 在大目录上可能较慢）？", len(fixes))); err != nil {
			return err
		}
		if err := ops.RunFixes(fixes, a.progress()); err != nil {
			return err
		}
	}
	if mounts.HasErrors(issues) {
		return fmt.Errorf("挂载检查发现错误")
	}
	if len(issues) == 0 {
		a.printf("所有挂载检查通过\n")
	}
	return nil
}

func (a *App) printIssuesOut(is []mounts.Issue) {
	for _, i := range is {
		a.printf("%s\n", i.String())
		if i.Fix != "" {
			a.printf("    修复命令: %s\n", i.Fix)
		}
	}
}

type mutateFlags struct {
	restart bool
	copy    bool
	accept  bool
	force   bool   // regenerate even when nothing changed (mounts apply)
	role    string // core role being changed (for --copy)
}

// mountsMutate is the shared flow: assess, acknowledge risks, optionally copy, apply.
func (a *App) mountsMutate(ctx context.Context, c *ctxt, cur, next mounts.Config, f mutateFlags) error {
	if !f.force && equalConfig(cur, next) {
		a.printf("%s\n", mounts.Msg("no_change"))
		return nil
	}
	as := c.env.AssessMounts(ctx, cur, next)
	if f.force {
		return a.mountsRegenerate(ctx, c, cur, as, f)
	}
	a.printIssues(as.Issues)
	if as.Block {
		return fmt.Errorf("存在必须先解决的错误，未做任何修改")
	}
	if as.MissingDirs || len(as.Fixes) > 0 {
		if a.getuid() == 0 && !c.g.dry {
			if err := a.confirm(c.g, "目录缺失或属主不符，现在以 root 创建/修复？"); err != nil {
				return err
			}
			if err := ops.RunFixes(as.Fixes, a.progress()); err != nil {
				return err
			}
		} else if as.MissingDirs && a.getuid() != 0 {
			fmt.Fprintln(a.Err, "目录不存在，请先用 root 执行上面列出的命令创建，然后重试。")
			return fmt.Errorf("目录不存在")
		}
	}
	if as.NeedAck && !c.g.dry {
		if f.accept {
			// explicit flag: the operator has read the warnings above
		} else if err := a.typed("上面的警告涉及已有数据，本工具不会移动或删除它们。", "I-UNDERSTAND"); err != nil {
			return err
		}
	}
	if c.g.dry {
		rep, err := apply.Apply(ctx, c.p, cur, next, apply.Options{DryRun: true, Restart: f.restart}, nil)
		if err != nil {
			return err
		}
		a.printf("演练：将生成的 %s：\n%s", "docker-compose.mounts.yml", rep.Override)
		a.printf("演练：%s\n", map[bool]string{true: "随后重建 api 与 worker", false: "不重启容器"}[f.restart])
		return nil
	}
	if f.copy && f.role != "" {
		from, to := cur.CoreHost(f.role), next.CoreHost(f.role)
		if from == "" || to == "" || from == to {
			return fmt.Errorf("--copy 不适用")
		}
		if pf, err := a.confirmCopy(c, from, to); err != nil || !pf {
			return err
		}
	} else if err := a.confirm(c.g, "应用以上挂载变更？"); err != nil {
		return err
	}
	opts := apply.Options{Restart: f.restart, HealthTimeout: a.HealthTimeout}
	_, err := apply.Apply(ctx, c.p, cur, next, opts, func(s string) { fmt.Fprintln(a.Out, s) })
	return err
}

func (a *App) confirmCopy(c *ctxt, from, to string) (bool, error) {
	running, _ := c.p.Running(context.Background())
	if len(running) > 0 {
		fmt.Fprintf(a.Err, "提示：服务仍在运行（%s），复制期间新写入的文件可能遗漏；建议先 oa-agentctl stop。\n", strings.Join(running, ", "))
	}
	if err := a.confirm(c.g, fmt.Sprintf("执行 cp -a '%s/.' '%s/' 并校验？（源目录不会被修改）", from, to)); err != nil {
		return false, err
	}
	if !c.g.yes {
		if err := a.typed("复制可能耗时较长。", "COPY"); err != nil {
			return false, err
		}
	}
	run := a.Exec
	if run == nil {
		run = mounts.DefaultExec
	}
	st, err := mounts.CopyAndVerify(context.Background(), run, from, to)
	if err != nil {
		return false, err
	}
	a.printf("复制完成并校验：%d 个文件，%s\n", st.Files, human(uint64(st.Bytes)))
	return true, nil
}

func equalConfig(a, b mounts.Config) bool {
	x, _ := a.Marshal()
	y, _ := b.Marshal()
	return string(x) == string(y)
}

// mountsRegenerate is "mounts apply": it only re-renders what mounts.json already says, so it
// never asks for confirmation (the installer calls it unattended). Missing or wrongly owned
// directories are repaired best-effort when root; validation errors still fail.
func (a *App) mountsRegenerate(ctx context.Context, c *ctxt, cur mounts.Config, as ops.Assessment, f mutateFlags) error {
	a.printIssues(as.Issues)
	if as.Block {
		return fmt.Errorf("mounts.json 校验未通过，未做任何修改")
	}
	if a.getuid() == 0 && !c.g.dry {
		for _, e := range cur.Entries() {
			for _, fx := range mounts.Check(e, c.env.Prober).Fixes {
				if err := mounts.RunFix(fx); err != nil {
					fmt.Fprintf(a.Err, "警告：%s 失败：%v\n", fx.Command(), err)
				}
			}
		}
	}
	opts := apply.Options{DryRun: c.g.dry, Restart: f.restart, HealthTimeout: a.HealthTimeout}
	rep, err := apply.Apply(ctx, c.p, cur, cur, opts, func(s string) {
		if !c.g.dry {
			fmt.Fprintln(a.Out, s)
		}
	})
	if c.g.dry && err == nil {
		a.printf("演练：将生成的 docker-compose.mounts.yml：\n%s", rep.Override)
	}
	return err
}
