// Package apply turns a new mounts config into files on disk and, optionally, a restarted stack,
// restoring the previous files when the stack does not come back healthy.
package apply

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"oa-agent/backend/internal/ctl/compose"
	"oa-agent/backend/internal/ctl/fsutil"
	"oa-agent/backend/internal/ctl/install"
	"oa-agent/backend/internal/ctl/mounts"
)

// Options controls one apply run.
type Options struct {
	DryRun  bool
	Restart bool
	// HealthTimeout bounds the wait for healthy containers after the restart (default 3 minutes).
	HealthTimeout time.Duration
}

// Report says what happened; Steps are human-readable progress lines.
type Report struct {
	Steps      []string
	Override   []byte
	RolledBack bool
	Backups    []string
}

// ErrRolledBack wraps the original failure when the previous configuration was restored.
var ErrRolledBack = errors.New("已回滚到之前的挂载配置")

type snapshot struct {
	mountsBak, overrideBak, envBak string
}

// Apply writes mounts.json, the generated override and the synced .env keys, then restarts if asked.
// The caller must already have validated and acknowledged risks (see mounts.Risks).
func Apply(ctx context.Context, p *compose.Project, old, cfg mounts.Config, o Options, progress func(string)) (Report, error) {
	var rep Report
	step := func(s string) {
		rep.Steps = append(rep.Steps, s)
		if progress != nil {
			progress(s)
		}
	}
	inst := p.Inst
	opt := mounts.Options{InstallDir: inst.Dir}
	cfg = cfg.Clone()
	cfg.Normalize()
	override, err := mounts.Render(cfg, opt)
	if err != nil {
		return rep, err
	}
	rep.Override = override
	step("校验通过，已生成 " + install.OverrideFile)
	if o.DryRun {
		step("演练模式：不写入任何文件")
		return rep, nil
	}

	var snap snapshot
	if snap.mountsBak, err = fsutil.Backup(inst.MountsPath(), 10); err != nil {
		return rep, err
	}
	if snap.overrideBak, err = fsutil.Backup(inst.OverridePath(), 10); err != nil {
		return rep, err
	}
	if snap.envBak, err = fsutil.Backup(inst.EnvPath(), 5); err != nil {
		return rep, err
	}
	for _, b := range []string{snap.mountsBak, snap.overrideBak, snap.envBak} {
		if b != "" {
			rep.Backups = append(rep.Backups, b)
		}
	}
	if len(rep.Backups) > 0 {
		step("已备份旧文件（*.bak-<时间>）")
	}

	rollback := func(cause error) (Report, error) {
		step("出错，正在回滚：" + cause.Error())
		var errs []string
		if e := fsutil.Restore(snap.mountsBak, inst.MountsPath(), 0o640); e != nil {
			errs = append(errs, e.Error())
		}
		if e := fsutil.Restore(snap.overrideBak, inst.OverridePath(), 0o644); e != nil {
			errs = append(errs, e.Error())
		}
		if e := fsutil.Restore(snap.envBak, inst.EnvPath(), 0o600); e != nil {
			errs = append(errs, e.Error())
		}
		_ = inst.ReloadEnv()
		rep.RolledBack = len(errs) == 0
		if len(errs) > 0 {
			return rep, fmt.Errorf("%w；回滚失败：%s", cause, strings.Join(errs, "; "))
		}
		step("已恢复旧的 mounts.json / 覆盖文件 / .env")
		if o.Restart {
			// bring the old configuration back up; failure here is reported but not fatal to the rollback
			if e := p.Stream(ctx, func(string) {}, "up", "-d", "--force-recreate", "api", "worker"); e != nil {
				step("旧配置重新启动失败：" + e.Error() + "（请手动 oa-agentctl restart）")
			} else {
				step("已用旧配置重新启动 api / worker")
			}
		}
		return rep, fmt.Errorf("%w: %v", ErrRolledBack, cause)
	}

	if err := fsutil.WriteAtomic(inst.MountsPath(), mustMarshal(cfg), 0o640); err != nil {
		return rollback(err)
	}
	step("已写入 mounts.json")
	if err := fsutil.WriteAtomic(inst.OverridePath(), override, 0o644); err != nil {
		return rollback(err)
	}
	step("已写入 " + install.OverrideFile)
	inst.Env.Set("COMPOSE_FILE", install.EnsureMountsLayer(inst.ComposeFile()))
	mounts.SyncEnv(inst.Env, cfg)
	if err := inst.Env.Save(inst.EnvPath()); err != nil {
		return rollback(err)
	}
	step("已同步 .env（COMPOSE_FILE 与 OA_*_DIR）")

	// the installer may call this before docker is usable; only a real compose error rolls back
	if _, err := p.Run(ctx, "config", "-q"); errors.Is(err, compose.ErrDockerMissing) {
		step("未找到 docker，跳过 compose 校验")
	} else if err != nil {
		return rollback(fmt.Errorf("docker compose config 校验失败：%w", err))
	} else {
		step("docker compose config 校验通过")
	}
	if !o.Restart {
		step("未重启：运行 oa-agentctl restart 使新挂载生效")
		return rep, nil
	}

	args := []string{"up", "-d", "--force-recreate", "api", "worker"}
	wait := []string{"api", "worker"}
	if old.PGData.Host != cfg.PGData.Host {
		args = []string{"up", "-d", "--force-recreate"}
		wait = inst.Services()
	}
	step("正在重建容器：docker compose " + strings.Join(args, " "))
	if err := p.Stream(ctx, func(l string) { step("  " + l) }, args...); err != nil {
		return rollback(err)
	}
	to := o.HealthTimeout
	if to <= 0 {
		to = 3 * time.Minute
	}
	step("等待 " + strings.Join(wait, "、") + " 健康 …")
	if err := p.WaitHealthy(ctx, wait, to); err != nil {
		return rollback(err)
	}
	step("全部健康，挂载已生效")
	return rep, nil
}

func mustMarshal(c mounts.Config) []byte {
	b, err := c.Marshal()
	if err != nil {
		panic(err) // Config only holds strings and bools
	}
	return b
}
