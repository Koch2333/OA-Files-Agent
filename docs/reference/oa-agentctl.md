# oa-agentctl 命令

`oa-agentctl` 是单个静态 Go 程序（Linux amd64 / arm64），管理 Docker Compose 部署：终端中不带参数进入全屏界面（TUI），带子命令则是可脚本化的非交互命令。只通过 `docker compose` v2 工作，`.env` 中的密钥在所有输出里被遮蔽。安装脚本同时提供 bash 备用版 `oa-agentctl.sh`。

安装目录的确定顺序：`--dir` → 环境变量 `OA_AGENT_DIR` → `/etc/oa-agent/install_dir` → `/opt/oa-agent`。

## 常用命令

| 目的 | 命令 |
|---|---|
| 状态与访问地址 | `oa-agentctl status [--json]` |
| 启动 / 停止 / 重启 | `oa-agentctl start` / `stop` / `restart [postgres\|api\|worker\|caddy]` |
| 日志 | `oa-agentctl logs [服务] [-f] [--tail N]` |
| 初始化令牌 | `oa-agentctl setup-token` |
| 备份 / 还原 | `oa-agentctl backup [--out 目录] [--skip-archive]` / `restore <备份文件>` |
| 升级 / 回滚 | `oa-agentctl upgrade [--tag T \| --version V] [--no-backup]`（先备份，再拉取或构建，重建，等待健康） |
| 体检 | `oa-agentctl doctor`（含时间同步；有失败项退出码 1） |
| 用户应急 | `oa-agentctl users list` / `admin reset-password\|reset-mfa\|unlock\|create-admin <用户>` |
| 存储挂载 | `oa-agentctl mounts list\|check\|add\|remove\|set\|apply` |
| 配置 | `oa-agentctl config show` / `config set <KEY> <值\|->` |
| 域名 | `oa-agentctl domain <域名>` / `domain --none` |

通用选项：`--dir`、`--json`、`--yes`/`-y`、`--dry-run`。

退出码：`0` 成功；`1` 失败；`2` 用法错误；`3` 未找到安装；`4` 需要确认但未确认；`5` docker 错误或未安装。

::: warning 升级与数据库
数据库迁移不会自动回退。升级失败时，改回旧标签并 `restore` 升级前的备份。备份包含加密密钥与数据库口令，请按密码级别保管。
:::

完整说明（TUI、存储挂载规则、镜像仓库模式的升级与回滚）：[oa-agentctl 使用说明](/oa-agentctl)。配置项见[配置项](./configuration)。
