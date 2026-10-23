# Linux 服务器部署指南（企业版）

> **版本定位**：Windows 版面向个人，本机单用户（local 模式，Electron 外壳另行构建）；**Linux 版是企业版**：多用户、初始化向导默认 `enterprise` 模式、管理员默认强制 MFA（TOTP / Passkey）、审计日志、Docker 隔离部署。
>
> **诚实声明**：`deploy/linux/` 下的安装脚本、`oa-agentctl.sh` 与 Compose 文件只在离线环境用假的 `docker`/`curl`/`ss`/`systemctl` shim 测试过（`bash deploy/linux/test/run.sh`）。**没有在真实 Docker 上运行过**：镜像构建、`docker compose config`、容器健康检查、Caddy 证书申请、真实发行版上的 Docker 安装都未验证（CI 的 `deploy.yml` 会补做其中一部分）。首次部署请先在测试机上走一遍。

## 1. 快速开始

```bash
# 方式 A：仓库检出内
sudo bash deploy/linux/install.sh

# 方式 B：远程一键（<OA_REPO_URL> 为占位，替换为你的发行页地址；脚本会校验 SHA-256）
curl -fsSL <OA_REPO_URL>/releases/latest/download/install.sh | sudo bash -s -- --domain oa.example.com

# 非交互示例
sudo bash install.sh --yes --domain oa.example.com --email admin@example.com \
     --archive-dir /srv/oa/archive --extra-mount nas1=/mnt/nas1:ro --mirror cn
```

常用参数：`--dir`（默认 `/opt/oa-agent`）、`--port`、`--domain`、`--email`、`--mirror cn|default`、`--version`、`--archive-dir`、`--spool-dir`、`--data-dir`（应用数据，含 `secret.key`）、`--pg-dir`、`--extra-mount name=/宿主机/路径[:ro|:rw]`（可重复，默认只读）、`--install-docker`（`--yes` 下允许自动安装 Docker）、`--systemd`、`--open-firewall`、`--no-start`、`--upgrade`、`--uninstall [--purge]`、`--help`。

**已有安装也可以更改存储目录**：对已安装的目录再次运行安装脚本并带上 `--archive-dir` / `--spool-dir` / `--data-dir` / `--pg-dir` / `--extra-mount`，脚本会通过 oa-agentctl 应用这些更改（等同于 `oa-agentctl mounts set/add`）；已有数据不会被移动或删除，需要迁移请用 `oa-agentctl mounts set archive <新路径> --copy`。

安装结束会打印访问地址、**初始化令牌**、目录与管理命令。**请立即完成初始化向导**：实例已对外暴露，拿到令牌的人可以抢先成为管理员；令牌每次重启都会更换（`oa-agentctl setup-token` 可重新查看）。

## 2. 系统要求

| 项目 | 要求 |
|---|---|
| 系统 | Ubuntu/Debian、CentOS/Rocky/Alma/RHEL/Fedora、Alpine（需先 `apk add bash`；以上均为尽力支持），x86_64 或 aarch64 |
| 资源 | ≥2 核、≥4 GB 内存（OCR 占内存，<2 GB 会警告）、≥5 GB 可用磁盘（构建约 3 GB），归档另算（<20 GiB 警告） |
| 网络 | 能访问镜像仓库/Go/npm/PyPI（中国大陆加 `--mirror cn`）；HTTPS 需 80/443 对公网开放且域名已解析 |
| 软件 | Docker Engine + Compose v2。缺失时脚本会**询问**后用发行版包管理器或 Docker 官方安装脚本（get.docker.com，第三方脚本，会提示；`--mirror cn` 使用阿里云源）安装 |

## 3. 架构与目录

- `postgres`（16-alpine，不发布端口）、`api`（Go 二进制 + 前端 + 迁移 + `oa-admin`，唯一发布端口的服务）、`worker`（Python 文档/OCR）、可选 `caddy`。
- **Worker 与 API 共用网络命名空间**（`network_mode: service:api`）：API 只接受回环 http 的 Worker 地址，Worker 只绑定 127.0.0.1，因此无需放宽任何规则，也不暴露 Worker。
- 所有容器：非 root、`no-new-privileges`、`cap_drop: ALL`、只读根文件系统 + tmpfs、日志轮转、`restart: unless-stopped`、内存上限。
- 安装目录（默认 `/opt/oa-agent`）：`.env`（0600，含密钥）、`mounts.json`（存储挂载的唯一配置源）、`docker-compose*.yml`、`Caddyfile`、`src/`、`backups/`、`data/`。
- **存储挂载**：归档根目录可以是任意本机绝对路径（挂载盘、NAS 挂载点）。安装器校验：绝对路径、不得为 `/` 或系统目录（`/etc /usr /bin /boot /proc /sys /dev /var/lib/docker` 等）、可创建（0750）并可写、显示剩余空间、位于 tmpfs/overlay 时警告。`mounts.json` → `oa-agentctl mounts apply` → 生成 `docker-compose.mounts.yml`（`COMPOSE_FILE=docker-compose.yml:docker-compose.mounts.yml[:docker-compose.https.yml][:docker-compose.build.yml]`）。额外挂载在 API 容器内位于 `/mnt/storage/<name>`（`OA_LOCAL_ROOTS=/mnt/storage`）。
- 归档目录属主为容器 uid 10001，数据库目录为 70。NFS 开启 root_squash 时 chown 可能失败，请在服务端授权。

## 4. 域名、HTTPS 与 Passkey

- 提供 `--domain` 时启用 Caddy 自动 HTTPS，API 仅监听 127.0.0.1，`OA_TRUSTED_PROXY=1`，`OA_PUBLIC_URL=https://域名`。
- **Passkey 只能在 HTTPS（或 localhost）下使用**。没有域名则为纯 HTTP：Passkey 不可用，只能用 TOTP，口令与会话明文传输——强烈建议配域名（`oa-agentctl domain <域名>`）。
- 更换域名会使已注册 Passkey 失效（绑定在旧域名），需在 设置→安全 更新 RP ID/来源；TOTP 不受影响。
- 前置自己的反向代理（nginx / 1Panel / Caddy）：保持 http 模式，把代理指向 `127.0.0.1:端口`，必须传 `X-Forwarded-Proto`、`X-Forwarded-For`，并执行 `oa-agentctl config set` 之外的手工修改：在 `.env` 设置 `OA_TRUSTED_PROXY=1`、`OA_PUBLIC_URL=https://域名`、`OA_BIND_ADDR=127.0.0.1`，然后 `oa-agentctl restart`。代理需支持 SSE（关闭缓冲）和大文件上传。

## 5. 初始化向导

1. 打开访问地址 → `/setup`；2. 输入初始化令牌；3. 模式保持默认 **enterprise**；4. 创建管理员；5. 确认域名与安全策略（管理员 MFA 默认强制）；6. 绑定 MFA（Passkey 或 TOTP）并保存恢复码；7. 完成。随后在 设置→识别引擎 配置大模型。

## 6. 日常运维（`oa-agentctl`）

主工具是 Go 版 TUI（直接运行 `oa-agentctl`）；同时有非交互子命令：`status start stop restart logs backup restore upgrade doctor setup-token admin mounts`。`oa-agentctl.sh` 是最小 bash 备用版（命令：status/start/stop/restart/ps/logs/upgrade/backup/restore/setup-token/admin/config/domain/mounts/doctor/version/uninstall）。

| 需求 | 命令 |
|---|---|
| 忘记密码 / 重置 MFA / 解锁 | `oa-agentctl admin reset-password <用户>`、`reset-mfa`、`unlock`、`create-admin`、`list-users` |
| 看配置（密钥遮蔽）/ 改配置 | `oa-agentctl.sh config show`、`config set OA_PORT 9090`（白名单；密钥用 `-` 从标准输入读入） |
| 切换域名 | `oa-agentctl.sh domain oa.example.com` / `domain --none` |
| 诊断 | `oa-agentctl doctor`（含时间同步检查：TOTP 依赖准确时间） |

## 7. 备份 / 还原 / 升级

- `backup`：`pg_dump -Fc` + 归档目录 + `secret.key` + `.env` 副本，打成 0600 的 tar.gz。**备份包含加密密钥与数据库口令，按密码级别保管**。额外挂载不在备份内。`--skip-archive` 可只备份数据库与密钥。
- `restore <文件>`：确认后停 api/worker → 重建数据库并 `pg_restore` → 旧归档目录改名为 `archive.pre-restore-*` 后换入新目录 → 启动。
- 升级：`sudo bash install.sh --upgrade`（刷新源码/脚本）或 `oa-agentctl upgrade [--version V]`：先备份 → 构建/拉取 → `up -d` → 等待健康。数据库迁移不会自动回退；失败时按提示改回旧版本并 `restore` 升级前备份。
- 发布包：`deploy/linux/build-release.sh` 交叉编译 `oa-agentctl`（amd64/arm64）并生成 `oa-agent-source.tar.gz`、`.sha256`、`SHA256SUMS`。

## 8. 安全加固清单

- [ ] 防火墙只放行 80/443（或所选端口）；安装器**不会擅自修改**防火墙，检测到 ufw/firewalld 时才询问。
- [ ] SSH 仅密钥登录、禁用 root 口令；限制管理来源。
- [ ] 启用时间同步（chrony 或 systemd-timesyncd），否则 TOTP 失败。
- [ ] 定期 `backup` 并**异地保存** `OA_SECRET_KEY`（丢失后已保存的 API 密钥/存储凭据无法解密）。
- [ ] `.env` 权限 600；`doctor` 会检查。
- [ ] 使用域名 + HTTPS；PostgreSQL 与 Worker 不发布端口（Compose 已保证）。
- [ ] 容器日志已轮转（`OA_LOG_MAX_SIZE`×`OA_LOG_MAX_FILE`）；安装日志 `/var/log/oa-agent-install.log` 不含密钥。
- [ ] 可选 systemd 单元 `oa-agent.service`（开机 `up -d`；容器本身已 `unless-stopped`）。

## 9. 故障排查

| 现象 | 处理 |
|---|---|
| 端口被占用 | `ss -ltnp \| grep :端口`，换 `--port` 或停止占用者 |
| api 不健康 | `oa-agentctl logs api`；常见原因：数据库未就绪、`OA_WORKER_TOKEN` <32 字符、目录权限 |
| worker 不健康 | 内存不足（调大 `OA_WORKER_MEM`）；`oa-agentctl logs worker` |
| 看不到初始化令牌 | `oa-agentctl setup-token`；若已完成初始化则不再生成 |
| Caddy 申请证书失败 | 检查 DNS、80/443 可达、`oa-agentctl logs caddy` |
| Passkey 不可用 | 需 HTTPS 与正确域名；或先用 TOTP |
| TOTP 码总是错 | 校时：`timedatectl`、chrony |
| 归档目录写入失败 | 目录属主应为 uid 10001，NAS 检查 root_squash/只读挂载 |
| 构建超时（大陆） | 加 `--mirror cn`（goproxy.cn / npmmirror / 清华 PyPI） |

## 10. 卸载

`oa-agentctl.sh uninstall`（或 `install.sh --uninstall`）移除容器与 systemd 单元，**数据保留**。`--purge` 需手动输入安装目录名确认后删除安装目录（`--yes` 不能确认清除）；自定义的归档目录（安装目录之外）永远不会被删除。
