package cli

import (
	"context"
	"fmt"
	"strings"
	"text/tabwriter"

	"oa-agent/backend/internal/ctl/oaadmin"
)

func yn(b bool) string {
	if b {
		return "是"
	}
	return "否"
}

func (a *App) cmdUsers(ctx context.Context, g global, rest []string) error {
	if len(rest) == 0 {
		return usage("用法: users list | reset-password|reset-mfa|unlock|disable-user|create-admin <用户>")
	}
	c, err := a.open(g)
	if err != nil {
		return err
	}
	cl := &oaadmin.Client{P: c.p}
	sub := rest[0]
	if sub == "list" {
		us, err := cl.ListUsers(ctx)
		if err != nil {
			return err
		}
		if g.json {
			type row struct {
				Username string `json:"username"`
				Role     string `json:"role"`
				Status   string `json:"status"`
				MFA      bool   `json:"mfa"`
				Locked   bool   `json:"locked"`
			}
			rows := make([]row, 0, len(us))
			for _, u := range us {
				rows = append(rows, row(u))
			}
			return a.emitJSON(rows)
		}
		tw := tabwriter.NewWriter(a.Out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "用户名\t角色\t状态\tMFA\t锁定")
		for _, u := range us {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", u.Username, u.Role, u.Status, yn(u.MFA), yn(u.Locked))
		}
		return tw.Flush()
	}
	if len(rest) != 2 {
		return usage("用法: users %s <用户>", sub)
	}
	user := rest[1]
	if err := oaadmin.ValidUsername(user); err != nil {
		return usage("%v", err)
	}
	switch sub {
	case "reset-password", "create-admin":
		if g.dry {
			a.printf("演练：将对 %s 执行 oa-admin %s\n", user, sub)
			return nil
		}
		if err := a.confirm(g, map[string]string{
			"reset-password": "重置 " + user + " 的密码？旧密码立即失效",
			"create-admin":   "创建管理员 " + user + "？",
		}[sub]); err != nil {
			return err
		}
		var sec oaadmin.Secret
		if sub == "reset-password" {
			sec, err = cl.ResetPassword(ctx, user)
		} else {
			sec, err = cl.CreateAdmin(ctx, user)
		}
		if err != nil {
			return err
		}
		return a.showSecret(g, user, sec)
	case "reset-mfa", "unlock", "disable-user":
		if g.dry {
			a.printf("演练：将对 %s 执行 oa-admin %s\n", user, sub)
			return nil
		}
		if err := a.confirm(g, fmt.Sprintf("对 %s 执行 %s？", user, sub)); err != nil {
			return err
		}
		out, err := cl.Simple(ctx, sub, user)
		if err != nil {
			return err
		}
		if g.json {
			return a.emitJSON(map[string]string{"username": user, "action": sub, "output": out})
		}
		if out == "" {
			out = "完成"
		}
		a.printf("%s: %s\n", user, out)
		return nil
	}
	return usage("未知的 users 子命令 %q", sub)
}

// showSecret prints a one-time password to stdout only; it is never logged or written to disk.
func (a *App) showSecret(g global, user string, s oaadmin.Secret) error {
	if g.json {
		return a.emitJSON(map[string]string{"username": user, "temporary_password": s.Password})
	}
	if s.Password == "" {
		fmt.Fprintln(a.Err, "警告：无法识别临时密码，以下为 oa-admin 的原始输出（只显示这一次）：")
		a.printf("%s\n", strings.TrimSpace(s.Raw))
		return nil
	}
	a.printf("用户 %s 的临时密码（仅显示这一次，之后无法再查看；用户首次登录须修改）：\n\n    %s\n\n", user, s.Password)
	return nil
}
