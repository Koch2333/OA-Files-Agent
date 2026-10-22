package cli

import (
	"fmt"
	"io"
)

func (a *App) printUsage(w io.Writer) {
	fmt.Fprint(w, `oa-agentctl —— OA 文件智能归档 Docker 部署管理工具

用法: oa-agentctl [--dir 安装目录] [--json] [--yes] [--dry-run] [命令]
不带命令且在终端中运行时进入交互界面。安装目录默认 /opt/oa-agent（OA_AGENT_DIR 或 /etc/oa-agent/install_dir 可覆盖）。

服务
  status                      服务状态、端口、访问地址
  start | stop                启动 / 停止全部服务
  restart [服务]              重启（api 与 worker 总是一起重建）
  ps                          容器列表
  logs [服务] [-f] [--tail N]  查看日志
  upgrade [--version V] [--no-backup]
  backup [--out 目录] [--skip-archive]
  restore <备份文件>
  doctor                      诊断（有失败项时退出码为 1）
  version

账号（封装容器内的 oa-admin）
  setup-token                 显示初始化令牌
  users list | reset-password <用户> | reset-mfa <用户> | unlock <用户> | disable-user <用户> | create-admin <用户>
  admin <参数…>               直接透传给 oa-admin

存储挂载
  mounts list | check [--fix]
  mounts add <名称> <主机路径> [--ro|--rw] [--replace] [--restart]
  mounts remove <名称> [--restart]
  mounts set <archive|spool|app|pgdata> <主机路径> [--copy] [--accept-data-move] [--restart]
  mounts apply [--restart]    按 mounts.json 重新生成 docker-compose.mounts.yml

配置
  config show                 显示配置（密钥已遮蔽）
  config set <KEY> <值>       修改白名单内的键（值为 - 时读标准输入）
  domain <域名> | --none      切换到 HTTPS / 回到 HTTP

通用: --yes 跳过确认；--dry-run 只演示不写入；--json 机器可读输出。
退出码: 0 成功，1 失败，2 用法错误，3 未找到安装，4 未确认/被拒绝，5 docker 错误。
`)
}
