# Linux 服务器部署指南（企业版）

> **版本定位**：Windows 版面向个人，本机单用户（local 模式，Electron 外壳另行构建）；**Linux 版是企业版**：多用户、初始化向导默认 `enterprise` 模式、管理员默认强制 MFA（TOTP / Passkey）、审计日志、Docker 隔离部署。
>
> **诚实声明（验证范围，准确到实际运行过的东西）**：安装脚本、`oa-agentctl.sh` 与 Compose 文件有两层验证。**(1) 离线套件** `bash deploy/linux/test/run.sh`：假的 `docker`/`curl`/`ss`/`systemctl`/包管理器 shim，1300 多条断言，在 tmpfs 与 ext4 上各跑一遍；**(2) 真实 Docker 引擎**：在 Docker Desktop 29.9（Linux 虚拟机）上，用 `deploy/linux/test/docker-host.sh` 起一个 Debian 12 的“服务器”容器（通过 `docker.sock`，Docker-outside-of-Docker）和本地 `registry:2`，跑 `deploy/linux/test/real-e2e.sh`：镜像仓库模式下用**真实伪终端、以管道方式**（`cat install.sh | bash`）走完向导安装、再次运行时的“健康同版本→只显示状态”快速路径、真实容器占用端口的检测与命名、升级/降级判定、**已有 PostgreSQL 16 容器**的外部库流程（错误密码、非专用库被拒、DDL 预览与第二次确认、密码不出现在日志和进程参数）、卸载保留数据、再次运行复用旧数据。使用的镜像是本机已有镜像重新打标签的测试镜像（api 叠加了当前源码编出的 `oa-agent`/`oa-admin`，deploy 镜像由本检出打包），**不是**CI 构建的发布镜像。
>
> **仍然没有验证**：真实发行版上用 apt/dnf/yum/zypper/apk 安装 Docker 与前置包（只有 shim）；get.docker.com 与发行版软件包回退；Caddy 真实证书申请；真实阿里云 ACR 登录/推送与 GitHub 托管 runner；**MySQL 与 SQL Server 作为外部库的安装器端到端**（镜像为省磁盘已删除，安装器侧只有 shim；它们的引擎检查逻辑是后端 `dbadmin` 包，另有自己的真实引擎测试）；ARM64；systemd 单元（测试主机容器里没有 systemd）；Podman、rootless Docker；`lsof` 回退（没有 `ss` 的主机）；TLS 校验到企业 CA 的真实证书链。首次部署请先在测试机上走一遍。
>
> **镜像仓库模式的验证范围**：第 11 节的镜像仓库流程已在 Docker Desktop 上用本地 `registry:2`（`localhost:15000/koch2333`）做过端到端实测（三个镜像的构建与推送、管道安装、`oa-agentctl upgrade --tag` 升级/回滚且数据与密钥不变）；**未对真实阿里云 ACR 登录/推送，也未在 GitHub 托管 runner 上运行过工作流**。

## 1. 快速开始

```bash
# 方式 A：仓库检出内
sudo bash deploy/linux/install.sh

# 方式 B：远程一键（<OA_REPO_URL> 为占位，替换为你的发行页地址；脚本会校验 SHA-256）
curl -fsSL <OA_REPO_URL>/releases/latest/download/install.sh | sudo bash -s -- --domain oa.example.com

# 方式 C：从容器镜像仓库安装（预构建镜像，不在本机编译；详见第 11 节）
docker run --rm <仓库地址>/<命名空间>/oa-agent-deploy:latest | sudo bash -s -- --registry <仓库地址>/<命名空间>

# 非交互示例
sudo bash install.sh --yes --domain oa.example.com --email admin@example.com \
     --archive-dir /srv/oa/archive --extra-mount nas1=/mnt/nas1:ro --mirror cn
```

常用参数：`--dir`（默认 `/opt/oa-agent`）、`--port`、`--domain`、`--email`、`--mirror cn|default`、`--version`、`--registry` / `--image-tag`（第 11 节）、`--archive-dir`、`--spool-dir`、`--data-dir`（应用数据，含 `secret.key`）、`--pg-dir`、`--extra-mount name=/宿主机/路径[:ro|:rw]`（可重复，默认只读）、`--install-docker`（`--yes` 下允许自动安装 Docker）、`--systemd`、`--open-firewall`、`--no-start`、`--upgrade`、`--uninstall [--purge]`、`--help`。

**第 12 节的新选项**：`--check` / `--dry-run`（只读检查，什么都不改）、`--offline <镜像包.tar>`（离线安装）、`--docker-mirror <URL>`（经确认后写入 Docker 镜像加速）、`--detect-public-ip`（结束时探测外网地址）、`--reinstall`（重建容器，保留数据）。**强烈建议在生产服务器上先运行一次 `sudo bash install.sh --check [同样的参数]`**，确认全是 `[PASS]`、了解每条 `[WARN]` 之后再正式安装。

**第 14 节**：安装器会先**检测**本机已有什么（Docker/Compose、前置工具、已有安装、旧数据、端口占用者、已有数据库服务），只补缺少的；全新安装用向导一次问一个问题并给复核页；`--config`/`OA_INSTALL_*`/`--print-config` 预置答案；`--status` 只读查看状态；对健康的同版本安装重新运行只显示状态。

**退出码**：`0` 成功；`1` 失败；`2` 参数/取值错误；`3` 环境检查未通过（有 `[FAIL]`，此时尚未做任何改动；`--check` 有失败项时同样返回 3）；`4` 用户取消；`130` 被 Ctrl-C/TERM 中断（可安全重跑）。

**已有安装也可以更改存储目录**：对已安装的目录再次运行安装脚本并带上 `--archive-dir` / `--spool-dir` / `--data-dir` / `--pg-dir` / `--extra-mount`，脚本会通过 oa-agentctl 应用这些更改（等同于 `oa-agentctl mounts set/add`）；已有数据不会被移动或删除，需要迁移请用 `oa-agentctl mounts set archive <新路径> --copy`。

安装结束会打印访问地址、**初始化令牌**、目录与管理命令。**请立即完成初始化向导**：实例已对外暴露，拿到令牌的人可以抢先成为管理员；令牌每次重启都会更换（`oa-agentctl setup-token` 可重新查看）。

## 2. 系统要求

| 项目 | 要求 |
|---|---|
| 系统 | Ubuntu/Debian、CentOS/Rocky/Alma/RHEL/Fedora、Alpine（需先 `apk add bash`；以上均为尽力支持），x86_64 或 aarch64 |
| 资源 | ≥2 核、≥4 GB 内存（OCR 占内存，<2 GB 会警告）、≥5 GB 可用磁盘（构建约 3 GB），归档另算（<20 GiB 警告） |
| 网络 | 能访问镜像仓库/Go/npm/PyPI（中国大陆加 `--mirror cn` 用于构建镜像源；Docker 安装源自动选择，见 14.9）；HTTPS 需 80/443 对公网开放且域名已解析 |
| 软件 | Docker Engine ≥ 20.10 + Compose v2（低于 20.10 时预检失败）。缺失时脚本会**询问**（`--yes` 下必须显式 `--install-docker`）后安装：先用 Docker 官方脚本 get.docker.com（第三方脚本，会提示；失败自动重试；`--mirror cn` 依次试阿里云源、AzureChinaCloud 源），仍失败则回退到发行版软件包（apt/dnf/yum/zypper/apk），全部失败给出明确错误 |
| 内核/其他 | 内核 ≥ 3.10（<4.0 警告），建议 cgroup v2；系统时钟必须同步（TOTP）；SELinux Enforcing 时给出处理建议（脚本不改 SELinux）。完整清单见 12.2 |

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
- 前置自己的反向代理（nginx / 1Panel / Caddy）：保持 http 模式，把代理指向 `127.0.0.1:端口`，必须传 `X-Forwarded-Proto`、`X-Forwarded-For`，并执行 `oa-agentctl config set` 之外的手工修改：在 `.env` 设置 `OA_TRUSTED_PROXY=1`、`OA_PUBLIC_URL=https://域名`、`OA_BIND_ADDR=127.0.0.1`，然后 `oa-agentctl restart`。代理需支持 SSE（关闭缓冲）和**不限大小的上传**：nginx 设置 `client_max_body_size 0; proxy_request_buffering off; proxy_buffering off; proxy_read_timeout 3600s;`，Caddy 默认没有请求体限制（仓库模板已把 `response_header_timeout` 设为 0）。上传不限制大小，但磁盘必须够用（不足时 507 并给出需要/剩余），可选的运维安全阀 `OA_MAX_UPLOAD_BYTES`/`OA_MAX_BATCH_FILES`、`OA_DISK_RESERVE_BYTES` 见 `docs/大文件与批量导入.md`。

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
- 升级：`sudo bash install.sh --upgrade`（刷新源码/脚本）或 `oa-agentctl upgrade [--tag T | --version V]`：先备份 → 构建（源码模式）或拉取（镜像仓库模式，见 11.4）→ 重建 → 等待健康。数据库迁移不会自动回退；失败时按提示改回旧版本/旧标签并 `restore` 升级前备份。
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

## 11. 使用镜像仓库（阿里云 ACR 示例）

每次推送到 GitHub，`.github/workflows/images.yml` 都会构建三个镜像并推送到你的容器镜像仓库；服务器上的**安装脚本本身也取自同一个仓库**（`oa-agent-deploy` 镜像里带有 `install.sh`、`oa-agentctl`、compose 文件），因此服务器只需要能访问镜像仓库，不需要 GitHub 发行包、源码或本机构建（也就没有 5–15 分钟的编译）。

| 镜像仓库 | 内容 |
|---|---|
| `<仓库地址>/<命名空间>/oa-agent-api` | Go 后端 + 前端 + 迁移 + `oa-admin` |
| `<仓库地址>/<命名空间>/oa-agent-worker` | Python 文档/OCR Worker |
| `<仓库地址>/<命名空间>/oa-agent-deploy` | 安装包：`/deploy/install.sh`、`oa-agentctl.sh`、`docker-compose*.yml`、`Caddyfile.template`、`.env.example`、`bin/oa-agentctl-linux-{amd64,arm64}`、`bin/SHA256SUMS`、`VERSION`。直接 `docker run --rm <镜像>` 会打印 `install.sh` |

参考配置（**默认值，不是机密**；换成你自己的实例即可）：地域 cn-shanghai，公网地址 `crpi-d6fmgwmrnsa2kgp8.cn-shanghai.personal.cr.aliyuncs.com`，VPC 地址 `crpi-d6fmgwmrnsa2kgp8-vpc.cn-shanghai.personal.cr.aliyuncs.com`，命名空间 `koch2333`，仓库类型“公开”。下文用 `R` 表示 `crpi-d6fmgwmrnsa2kgp8.cn-shanghai.personal.cr.aliyuncs.com/koch2333`。

### 11.1 一次性设置（控制台与 GitHub）

1. **创建三个镜像仓库**：阿里云控制台 → 容器镜像服务 ACR → 个人版实例 → 命名空间 `koch2333` → 创建镜像仓库 `oa-agent-api`、`oa-agent-worker`、`oa-agent-deploy`（仓库类型“公开”，代码源选“本地仓库”，不要绑定代码源）。**或者**在 命名空间 → 设置 中打开「自动创建仓库」，首次推送时自动创建（自动创建的仓库类型取命名空间的默认值，请确认是否为公开）。实例里原来的单个仓库 `oa-agent` **不会被使用**，可以保留或删除。
2. **设置/确认镜像仓库登录密码**：实例 → 访问凭证 → 设置固定密码（个人版：登录用户名是阿里云账号名，不是 AccessKey；密码就是这里设置的固定密码）。同一页可以看到公网地址与 VPC 地址。
3. **在 GitHub 保存凭据**：仓库 `Koch2333/OA-Files-Agent` → Settings → Secrets and variables → Actions：
   - Secrets（机密）：`ACR_USERNAME`（登录名）、`ACR_PASSWORD`（固定密码）。
   - Variables（变量，非机密，推荐）：`ACR_REGISTRY`（上面的公网地址，不带 `https://`）、`ACR_NAMESPACE`（`koch2333`）。`ACR_REGISTRY` / `ACR_NAMESPACE` 也可以放进 Secrets（工作流两处都认；放在 Secrets 里 GitHub 会把它们在日志和作业摘要中显示为 `***`）。
   - 不要把真实的用户名/密码写进仓库、文档示例、issue 或日志。
4. **第一次推送**：向任意分支推送一个提交，或 Actions → “Container images” → Run workflow。打开运行页的 Summary，应看到每个镜像的推送标签和一行安装命令。凭据没配好时工作流仍会构建镜像并保持绿色，但摘要会写明“Push skipped: …”。

### 11.2 标签语义

| 触发 | 推送的标签 |
|---|---|
| 任意分支的 push | `sha-<7位提交>`、`<分支名>`（`/` 等非法字符替换为 `-`） |
| 默认分支（main）的 push | 同上，另加 `latest` |
| 标签 `v1.2.3` | `1.2.3`、`1.2`、`sha-<7位提交>`（预发布版本如 `v1.2.3-rc.1` 没有 `1.2`） |
| Pull Request | 只构建，不登录、不推送、拿不到任何机密（fork 也一样） |

`latest` 与分支标签是**会移动的**；生产环境请固定 `sha-<7位提交>` 或版本号。默认分支推送（`latest`）和发布标签（`v*`）同时构建 `linux/amd64` 与 `linux/arm64`，推送后工作流会校验镜像清单里确实列出这两个平台；其他分支和 Pull Request 只构建 `linux/amd64`；在这些场景下，手动运行工作流（workflow_dispatch）时可勾选 `arm64`（api 与 deploy 的 Go/前端在构建机上交叉编译，只有很短的 apk 步骤走 QEMU，代价小）和 `arm64_worker`（Worker 的 pip 安装在 QEMU 下运行，很慢，所以单独开关）。

### 11.3 安装

方式 1，服务器**已装 Docker**（推荐，也不依赖 GitHub 的网络）：

```bash
docker run --rm R/oa-agent-deploy:latest | sudo bash -s -- --registry R --yes --archive-dir /srv/oa/archive
# 固定版本：          ... | sudo bash -s -- --registry R --image-tag sha-0a1b2c3
# 私有仓库先登录：    docker login crpi-d6fmgwmrnsa2kgp8.cn-shanghai.personal.cr.aliyuncs.com
```

方式 2，**还没装 Docker**：

```bash
curl -fsSL https://raw.githubusercontent.com/Koch2333/OA-Files-Agent/main/deploy/linux/install.sh \
  | sudo bash -s -- --registry R --install-docker --mirror cn
```

`raw.githubusercontent.com` 在中国大陆可能很慢或不可达；此时先自行安装 Docker（例如用阿里云的 Docker CE 镜像源），再用方式 1。管道执行时没有本地文件，脚本检测到“无本地源码 + `--registry`”会自动从 `oa-agent-deploy` 镜像取出自己的安装包：`docker pull` → `docker create` → `docker cp /deploy/.` → `docker rm`，校验 `bin/SHA256SUMS` 后安装预编译的 `oa-agentctl`（不下载 GitHub 压缩包，不拉 golang 容器，不编译）。

要点：

- `--registry` 的格式是 `主机[:端口][/命名空间]`（不带 `https://`）；也可用环境变量 `OA_IMAGE_REGISTRY`。`--image-tag`（或 `--version`）指定标签，默认 `latest`。其余参数（`--domain`、`--port`、`--archive-dir`……）与源码安装完全相同。
- `.env` 里会写入 `OA_IMAGE_REGISTRY=R`、`OA_VERSION=<标签>`，`COMPOSE_FILE` **不含** `docker-compose.build.yml`，所以不会构建；PostgreSQL 基础镜像（`OA_PG_IMAGE`，默认 `postgres:16-alpine`）仍来自 Docker Hub，国内网络慢时可在 `.env` 里改成可访问的镜像地址。
- 私有仓库：设置环境变量 `OA_REGISTRY_USER` / `OA_REGISTRY_PASSWORD`（`sudo OA_REGISTRY_USER=… OA_REGISTRY_PASSWORD=… bash …`），脚本只通过标准输入传给 `docker login`，不回显、不写日志、不写 `.env`。docker 自己会把登录信息保存在 `/root/.docker/config.json`。
- **阿里云 ECS 在同地域**时用 VPC 地址（`crpi-d6fmgwmrnsa2kgp8-vpc.cn-shanghai.personal.cr.aliyuncs.com`）拉取，走内网、不占公网流量。把上面的 `R` 换成对应的 VPC 地址即可。VPC 地址只在同 VPC/同地域内网可达，工作流推送一律使用公网地址（GitHub 的机器在公网）。
- 以后重新运行安装脚本（或 `--upgrade`）会以 `.env` 中的仓库与标签为准；切换标签请用下面的升级命令。

### 11.4 升级与回滚

```bash
oa-agentctl upgrade                      # 备份 → 拉取 .env 里 OA_VERSION 对应的镜像 → 重建 api+worker → 等待健康
oa-agentctl upgrade --tag sha-0a1b2c3    # 换到指定标签（--version 同义）
oa-agentctl upgrade --tag 1.2.3
sudo bash install.sh --upgrade --image-tag sha-0a1b2c3   # 重新运行安装脚本的等价形式
# 回滚到之前的 sha 标签（升级失败时输出里会给出这条命令）：
oa-agentctl upgrade --tag sha-旧提交 --no-backup
```

镜像模式（`COMPOSE_FILE` 不含构建层）下 `upgrade` 的行为：先备份（`--no-backup` 跳过）→ `docker compose pull` → 标签变化或为 `latest` 时从 `oa-agent-deploy:<标签>` 刷新**管理文件**（compose 文件、`oa-agentctl.sh`，以及校验 SHA-256 后的 `oa-agentctl` 二进制；校验失败则中止、不替换任何文件；取不到 deploy 镜像只警告并继续）→ `up -d --force-recreate api worker`（worker 与 api 共用网络命名空间，必须一起重建）→ 等待健康。**不会**改动 `.env`、`mounts.json` 或生成的 `docker-compose.mounts.yml`，也不会尝试从 `./src` 构建。数据库迁移不会自动回退：回滚镜像后如数据不兼容，请 `oa-agentctl restore <升级前备份>`。

### 11.5 安全说明

- 仓库类型是“公开”：**任何人都能拉取镜像**。镜像里不包含任何密钥或口令（密钥在服务器的 `.env` 中，安装时随机生成）；不要往镜像里加私有内容。若需要私有，把仓库改成私有并使用 `OA_REGISTRY_USER/OA_REGISTRY_PASSWORD`。
- 标签是可变的（`latest`、分支名可以被后续构建覆盖）：生产环境固定 `sha-<7位提交>` 或版本号，需要更严格时记录并使用镜像摘要（`@sha256:…`）。
- `ACR_PASSWORD` 是镜像仓库凭据，只存在于 GitHub Actions secrets；一旦泄露（出现在日志、截图、聊天里），立即在 ACR 控制台重置固定密码并更新 GitHub secret。
- Fork 和 Pull Request 永远拿不到这些机密：工作流对 PR 只做构建，没有凭据时（包括 fork 的 push）也只构建并在摘要里说明已跳过推送。
- 工作流权限只有 `contents: read`；第三方 Action 固定到主版本，更严格的做法是固定到提交 SHA。
- 手动推送（不经过 GitHub）：先 `docker login` 再 `scripts/push-images.sh --registry R --push`（标签规则与工作流一致，`--print-tags` 可预览）。

### 11.6 故障排查

| 现象 | 原因与处理 |
|---|---|
| `denied: requested access to the resource is denied` | 登录名/密码错误或未登录；仓库不存在且命名空间没开「自动创建仓库」；推送时 `ACR_NAMESPACE` 写错。检查三个仓库是否存在、`docker login` 是否成功 |
| `manifest unknown` / `not found` | 标签不存在：该提交的工作流还没跑完或失败了。到 Actions 查看，或改用 `--image-tag latest` / 已存在的 `sha-…`；三个镜像用同一个标签 |
| 拉取很慢或超时 | 服务器在阿里云同地域：改用 VPC 地址；否则用公网地址并检查带宽；PostgreSQL 基础镜像来自 Docker Hub，可改 `OA_PG_IMAGE`；worker 镜像较大（OCR 依赖），首次拉取需要几分钟 |
| `docker login` 失败（RAM 子账号） | ACR 不支持企业别名中含半角点（`.`）的 RAM 子账号登录；请用主账号名，或把 RAM 用户换成不含点的企业别名 |
| 工作流绿色但没有推送 | 摘要里写着原因：未配置 `ACR_REGISTRY/ACR_NAMESPACE/ACR_USERNAME/ACR_PASSWORD`，或这是 PR；配置后重新运行 |
| 安装脚本提示 `not a valid installer bundle` | 指向的仓库里没有 `oa-agent-deploy` 镜像，或标签不对 |
| `Checksum mismatch for the prebuilt oa-agentctl` | deploy 镜像中的二进制与 `SHA256SUMS` 不符；不要继续，重新推送镜像并核对来源 |
| 管道安装时没有终端（ssh 无 tty、CI） | 加 `--yes` 并显式给出参数；或先 `docker run --rm <镜像> > install.sh`，再 `sudo bash install.sh …` |

## 12. 安装器的生产加固（预检、`--check`、离线、重装、退出码）

与 1Panel 等成熟安装器的逐项对比和取舍见 [安装脚本对比-1Panel.md](安装脚本对比-1Panel.md)。本节是使用说明。

### 12.1 安装流程

（第 14 节给出了现在的完整流程：先检测，再决定，再提问。以下是早先的描述。）

`解析参数 → root 与单实例锁 → （已有安装且在终端交互）选择：修复/升级/重装/卸载/取消 → 配置问答 → 环境预检 → 安装/检查 Docker → Docker 与仓库预检 →（可选）Docker 镜像加速 → 获取安装包 → 生成配置（绝不覆盖已有 .env、绝不重新生成密钥）→ 拉取或载入镜像 → 启动并等待健康 → 摘要`。

- **截断安全**：脚本所有可执行逻辑都在函数里，最后一行 `main "$@" "$EOS_SENTINEL"` 才开始执行，并且 `main` 要求最后一个参数是结束哨兵。`curl | bash` 或 `docker run … | sudo bash` 被截断在任何位置，都不会安装半套东西（会提示“脚本不完整”）。
- **单实例**：同一台机器上同时只能有一个 `install.sh`（`flock`，锁文件 `/var/lock/oa-agent-install.lock`）。
- **可重跑**：Ctrl-C 或失败后直接重新运行即可，密钥与已有配置不会变，Docker 会续传已下载完的镜像层。

### 12.2 环境预检（`[PASS]` / `[WARN]` / `[FAIL]`）

| 检查 | 级别 | 说明 |
|---|---|---|
| 架构 | 失败 | 仅 x86_64 / aarch64 |
| 内核 | <3.10 失败；<4.0 警告 | overlay2 存储驱动建议 4.0 以上 |
| cgroup | v1 警告 | 内存上限（`OA_*_MEM`）仍可用，但建议 v2 |
| 内存 / CPU | <2 GB、<4 GB、1 核警告 | OCR 占内存，建议 ≥4 GB 或加 swap（脚本不会自动建 swap） |
| 磁盘 | <5 GB 失败；<20 GB 警告 | 安装目录所在磁盘；另检查 Docker 数据目录（<5 GB 警告） |
| 时间同步 | 未同步/无法确认 警告 | TOTP 依赖准确时间：`timedatectl set-ntp true` 或 chrony |
| SELinux | Enforcing 警告 | 只给文字建议（`ausearch -m avc`、`chcon -Rt container_file_t <目录>`、卷加 `:z`、排障时 `setenforce 0`），脚本绝不修改 SELinux |
| 本机地址 | 无地址/仅 IPv6 警告 | 服务默认监听 IPv4 `0.0.0.0`；IPv6-only 主机需要 IPv4 或反向代理 |
| 防火墙 | 提示 | ufw/firewalld 已启用时提示；是否放行端口在结束时询问，绝不擅自修改 |
| 端口 | 占用 失败 | 自己正在运行的 OA 栈占用的端口不算冲突 |
| Docker | <20.10 失败；rootless 警告 | 以及 Compose v2 是否可用 |
| 镜像仓库 | 不可达 警告 | 只看本机 shell 能否访问；若 Docker 守护进程单独配了代理则仍可能拉取成功 |

**有任何 `[FAIL]` 时，安装器在安装/改动任何东西之前就以退出码 3 结束**，并说明“未做任何更改”。所有警告会在最后的摘要框里再列一遍，方便复核。

### 12.3 `--check` / `--dry-run`（只读）

`sudo bash install.sh --check [与正式安装相同的参数]`（也可以不用 root）：不写日志、不加锁、不提示、不装 Docker、不拉取/加载镜像、不登录、不改任何文件。输出预检报告和“将执行的操作”（动作、目录、端口、域名、镜像来源与标签、Docker 是否会被安装、存储目录、systemd/防火墙的处理方式），有失败项返回 3，否则 0。

### 12.4 已有安装：修复 / 升级 / 重装 / 卸载

在终端里对已有安装再次运行（没给 `--yes`、`--upgrade`、`--reinstall`、`--uninstall`）会弹出菜单：

1. **修复/继续**（默认）：保留 `.env` 与全部数据，刷新文件并启动；
2. **升级**：先备份，再拉取/构建新版本并重建（等同 `--upgrade`）；
3. **重装**：移除并重建容器（`compose down` 不带 `-v`），重新拉取镜像；数据、`.env`、密钥都保留（等同 `--reinstall`）；
4. **卸载**：移除容器与 systemd 单元，数据保留；要连数据一起删除必须显式 `--uninstall --purge` 并手动输入目录名；
5. **取消**（退出码 4）。

### 12.5 Docker 安装回退链与镜像加速

- `--install-docker`（或交互确认）后：get.docker.com 脚本（下载带重试；安装源默认**自动选择**：中国大陆时按速度试阿里云、Azure 中国、腾讯云、清华、中科大这些可达的镜像，最后官方源；见 14.9，`--docker-source` 可指定；`--mirror cn` 等于已知在大陆）→ 失败则发行版软件包（Debian/Ubuntu `docker.io docker-compose-v2`；RHEL 系 `docker docker-compose-plugin`；Fedora `moby-engine docker-compose`；SUSE `docker docker-compose`；Alpine `docker docker-cli-compose`）→ 仍失败则报错并指向官方文档。软件包的名称和是否带 Compose v2 因发行版而异，属“尽力而为”。
- `--docker-mirror <URL>`（可重复，或环境变量 `OA_DOCKER_MIRRORS="url1 url2"`）：把 Docker Hub 镜像加速写入 `/etc/docker/daemon.json` 的 `registry-mirrors`。**只在交互确认后才修改**；用 python3 / jq / perl(JSON::PP) 解析并合并（保留其他设置），先备份为 `daemon.json.oa-bak.<时间>`；文件不存在/为空/`{}` 时直接写入；已有文件不是合法 JSON 对象，或主机上没有任何 JSON 工具时**保持不动**并打印手动步骤。**重启 Docker 要再确认一次**（未开 live-restore 时所有容器会重启）。`--yes` 或没有终端时永远不改 `daemon.json`。加速只影响 Docker Hub 的基础镜像（`postgres`、`caddy`）；生产镜像来自 ACR。

### 12.6 拉取可靠性

`docker pull`、`docker compose pull`、构建、下载 get.docker.com 都带重试和退避（默认 3 次：5 秒、15 秒；环境变量 `OA_RETRIES`、`OA_RETRY_BASE` 可调）。“镜像不存在 / 拒绝访问 / 未授权”这类永久错误不重试；Docker 对已下载完的层会自动续传。仍失败时，安装器逐个镜像排查，并在错误里写出**具体失败的镜像**，同时给出检查清单。若本机是阿里云 ECS 且使用同地域的 ACR 公网地址，会提示改用 VPC 内网地址（`--registry crpi-…-vpc.<地域>.personal.cr.aliyuncs.com/<命名空间>`；只提示，不会自动切换，因为 VPC 地址需要同地域内网可达）。

### 12.7 离线安装（主机不能访问镜像仓库）

在**能访问仓库**的机器上打包（脚本位于 `scripts/save-images.sh`）：

```bash
scripts/save-images.sh --registry <仓库/命名空间> --tag sha-0a1b2c3 --out ./pack   # 可加 --pull 刷新、--with-caddy（要用域名/HTTPS 时）、--arch amd64|arm64
# 得到：pack/oa-agent-images-sha-0a1b2c3-amd64.tar  .tar.sha256  install.sh
```

把这三个文件拷到服务器（U 盘、scp 均可），然后：

```bash
sudo bash install.sh --offline oa-agent-images-sha-0a1b2c3-amd64.tar --yes --archive-dir /srv/oa/archive
```

要点：tar 里包含 api、worker、deploy 三个镜像（以及默认的 PostgreSQL 基础镜像；`--no-postgres` 可去掉，但那样纯离线主机起不来数据库）；打包脚本会校验 tar 内容并写出 `.sha256`；安装器会再次校验 SHA-256（缺校验和时 `--yes` 拒绝，可用 `--skip-verify` 或 `OA_OFFLINE_SHA256=<64 位十六进制>`），读取 tar 中的仓库名和标签（与显式的 `--registry` / `--image-tag` 冲突会报错），`docker load` 后确认三个镜像都在、CPU 架构与本机一致，然后全程不访问仓库。已有安装换标签必须显式 `--upgrade`：`sudo bash install.sh --offline <新 tar> --upgrade`（升级时不拉取镜像；compose 文件与 `oa-agentctl` 等管理文件取自 tar 里的 deploy 镜像）。`--check --offline <tar>` 可以先验证 tar 而不加载。

### 12.8 安装摘要

结束时的摘要框列出：所有候选访问地址（内网 IPv4 优先，docker/br-/veth 等虚拟网卡已过滤，IPv6 带方括号；使用域名时是 `https://域名`）、初始化令牌、安装目录与各存储目录、镜像与标签、管理命令（含升级/重装/卸载/检查）、防火墙与**云安全组**提醒、环境预检警告回顾，以及“请保存好这些”：`OA_SECRET_KEY` 所在位置与查看命令（**不会**把密钥打印出来或写入日志）、应用数据目录里的 `secret.key`、备份本身也是机密、TOTP 恢复码。外网地址默认**不探测**；加 `--detect-public-ip` 才会探测：先试云厂商元数据（阿里云 ECS 的链路本地地址，不经第三方），再请求 `OA_PUBLIC_IP_URL`（默认 `https://api.ipify.org`，第三方服务），NAT/云主机请自行核对安全组与端口映射。

### 12.9 故障排查补充

| 现象 | 处理 |
|---|---|
| 退出码 3，输出 `[FAIL]` | 先解决 FAIL 项（端口、磁盘、内核、Docker 版本等）；此时没有任何改动。用 `--check` 反复验证 |
| 提示“脚本不完整（下载被截断？）” | 重新下载脚本；不要用被截断的副本 |
| 提示“已有另一个 install.sh 正在运行” | 等待它结束；确认没有残留进程后再运行（锁在进程退出时自动释放） |
| 拉取失败，错误里点名了某个镜像 | 检查该镜像的地址/标签/权限；网络抖动会自动重试 3 次；仍不行可用 `--offline` |
| `--docker-mirror` 没有修改 daemon.json | `--yes`/无终端不会修改；或文件不是合法 JSON/没有 JSON 工具；按提示手动编辑后 `systemctl restart docker` |
| `--offline` 提示 SHA-256 不匹配 | tar 损坏或被改动；重新拷贝/重新打包 |
| `--offline` 提示镜像架构不一致 | 在目标架构（amd64/arm64）上重新用 `save-images.sh --pull --arch` 打包 |
| 安装提示「镜像 … 没有 arm64 架构的版本」 | 这个标签是在发布 arm64 镜像之前构建的：改用较新的标签（`--image-tag <版本号>` 或 `latest`），或加 `--build-from-source` 在本机构建；离线安装则在 arm64 机器上用 `save-images.sh --pull --arch arm64` 打包 |

## 13. 使用已有的 PostgreSQL / MySQL / SQL Server

默认仍使用 compose 自带的 PostgreSQL。要复用企业已有数据库，在 `.env` 的 `COMPOSE_FILE` 末尾追加 `docker-compose.external-db.yml` 并设置 `OA_DB_PROVIDER/HOST/PORT/NAME/USER/PASSWORD/TLS_MODE`（需要 Docker Compose 2.24+），自带 postgres 服务随之不再启动。专用库、最小权限的 SQL 与限制见 `多数据库部署与迁移指南.md`。现在 `install.sh` 的向导与 `--db`/`--config` 已支持（见 §14 与 `多数据库部署与迁移指南.md` §6.1）；也可以按上面手工编辑 `.env`。

## 14. 检测与安装向导（先检测、再决定、再提问）

这一节说明 `install.sh` 现在怎么工作。与 1Panel 的逐项对照见 [安装脚本对比-1Panel.md](安装脚本对比-1Panel.md)（§A 检测逻辑、§B 交互逻辑、§C 对照表）。

### 14.1 流程

`解析参数 → 读取 --config / OA_INSTALL_* → root/锁 → 【检测】（只读，打印组件表）→ 选定要管理的安装并决定做什么 → 【向导】（仅全新安装）→ 复核页 → 预检闸门（FAIL 即退出 3，此时未做任何改动）→ 只安装缺少的前置包/Docker/Compose 插件 → 外部数据库验证 → 取得安装包 → 写文件/.env → 拉取镜像并启动 → 写状态文件 → 摘要`

- **检测永远先于一切**：`--check`、`--status` 与正式安装都会打印同一张表；它只读，不需要 root，不写日志。
- **同版本且健康的已有安装**：重新运行只显示状态与访问信息（快速、不拉镜像、不执行 `up`），退出码 0；交互运行时是一个菜单，默认选 6「无需操作」。要升级/重装/卸载用菜单或 `--upgrade/--reinstall/--uninstall`。
- **没有终端且没有 `--yes`**：打印说明并以退出码 2 结束（不会悄悄装一个默认的纯 HTTP 实例）。已有安装时只显示状态。

### 14.2 检测表的状态词与各行

| 状态词 | 含义 |
|---|---|
| `[PASS]` | 满足，无需动作 |
| `[FOUND]` | 已存在：沿用或不碰（已有 Docker/Compose、已有安装、占用端口的进程、别的数据库服务） |
| `[MISS]` | 缺少：安装器只补这一项，需要同意（`--install-docker` / `--install-prereqs` 或交互确认） |
| `[TODO]` | 需要决定/处理（Docker 没运行、只有旧版 compose v1、旧数据没有 `.env`、记录指向的目录丢了） |
| `[WARN]` / `[FAIL]` / `[INFO]` | 风险 / 不能继续（退出码 3）/ 仅供参考 |

| 行 | 内容 |
|---|---|
| 系统资源 | 架构、内核、cgroup、CPU/内存、时钟同步（TOTP）、SELinux、本机地址；磁盘在最终安装目录确定后判定 |
| 前置工具 | `curl tar gzip sha256sum awk`（必需）、`openssl flock ss`（推荐）；缺哪个只装哪个，用检测到的包管理器 |
| systemd | 是否可提供开机单元 |
| Docker 引擎 | 版本（<20.10 失败）、守护进程是否运行、是否开机自启、是否 rootless、端点（`DOCKER_HOST`/context）、Podman 冒充警告；已有则跳过安装，没运行只启动 |
| Compose | v2 插件及版本（外部数据库层需要 ≥2.24）；只有旧版 `docker-compose` v1 或没有插件时**只装插件**，不重装 Docker |
| 已有安装 | 状态文件 `/etc/oa-agent/install.json`、`/etc/oa-agent/install_dir`、`/opt/oa-agent`、`--dir`、容器标签/镜像名、`docker compose ls`；解析各目录 `.env`（标签、项目名、端口、域名、数据库模式）并给出健康状态 |
| 旧数据 | 没有 `.env` 却有 `secret.key`、PostgreSQL 数据文件、非空归档、含 `.env` 的备份：沿用同一把密钥 / 从备份恢复 `.env` / 带解释地拒绝；**绝不在旧数据上生成新密钥** |
| 端口 | 80/443/所选端口的占用者（进程或容器）；反向代理占用 80/443 时提供「放在它后面」的配置片段 |
| 已有数据库服务 | 5432/3306/1433 的监听者（含是否只监听 127.0.0.1）、systemd 单元、数据库容器；只列为候选，**不连接** |
| 防火墙 | ufw / firewalld / iptables；只报告 |
| 网络 | 镜像仓库、Docker Hub 可达性（4 秒超时）、阿里云 ECS 元数据；只用于**预选**镜像源问题的默认答案 |

已有安装的决策：

| 检测到 | 终端 | `--yes` |
|---|---|---|
| 无安装 | 向导 | 参数/配置文件/默认值 |
| 健康、同版本、无变更请求 | 菜单默认 6「无需操作」 | 只显示状态，退出码 0 |
| 不健康/已停止 | 菜单默认 1「修复/继续」 | 修复/继续 |
| 请求更新的标签 | 菜单提示「可升级：x -> y」 | 需 `--upgrade` |
| 请求更旧的标签 | 提示迁移不可回退 | 拒绝，除非 `--allow-downgrade` |
| 多套安装 | 先选一套 | 要求 `--dir` |
| 同名 compose 项目 | 自动改名 `oa-agent-2…` | 同左；显式 `--project` 冲突则拒绝 |

标签比较：`1.2.3` 这类按版本排序（预发布版 `1.2.3-rc.1` 比 `1.2.3` 旧）；`sha-<提交>` 与版本号无法比较大小（提示请确认）；`latest`/分支名是「可移动标签」，同名时无法判断是否有更新，选「升级」才会拉取最新镜像。

### 14.3 向导问题与复核页

向导第一问是界面语言（见 14.9，指定了 `--lang` 就不问）。随后一次问一个问题，默认值来自检测（Docker 安装源和 Docker Hub 镜像加速不在问题里：自动选择并打印一行，可在复核页第 8 项修改）：安装目录 → HTTP 端口（默认被占用则建议下一个空闲端口并说明占用者）→ 域名/HTTPS（及邮箱；80/443 被占用时三选一：放在已有反向代理后面〔推荐〕/ 忽略域名只用 HTTP / 仍用内置 Caddy）→ 数据库（1 内置 PostgreSQL〔推荐〕、2 已有 PostgreSQL、3 已有 MySQL 8+、4 已有 SQL Server 2019+；已有库再问主机、端口、库名、用户、隐藏输入的密码、TLS 模式、CA 文件）→ 存储目录（归档、暂存/应用数据/数据库目录、额外挂载）→ 镜像源 → 镜像标签。全部回答后是**复核页**（密码遮蔽；第 8 项 Docker 安装源/加速、第 9 项语言），`[Y]继续 / [e]修改某项（按编号 1-9）/ [c]取消`；取消退出码 4，未做任何更改。

校验与重问：每个答案都有友好的错误说明；无效最多重问 5 次，超过以退出码 2 中止。Ctrl-C 随时安全（退出码 130）。

提问读自 `/dev/tty`：`curl … | sudo bash`、`docker run … | sudo bash` 时脚本占用标准输入，答案取自终端。密码用 `read -s` 隐藏输入，不进进程参数、环境、日志。

### 14.4 预置答案：`--config`、环境变量、`--print-config`

优先级：命令行参数 > `OA_INSTALL_<键>` 环境变量 > `--config` 文件 > 向导 > 默认值。配置文件/环境变量给出的项不再追问，只出现在复核页；`--yes` 接受默认值，但没有安全默认的必需答案（外部库的主机、密码）会失败并说明。

`--config <文件>`：`key=value`，`#` 注释，值可加引号。**文件权限必须是 600**（其他人可读写的文件、符号链接、属主不是 root/当前用户的文件一律拒绝，退出码 2）。**数据库密码只能写在这个文件里**（`db_password=…`，或 `db_password_file=<另一个 600 文件>`）；命令行参数和环境变量里的密码一律不接受（`OA_INSTALL_DB_PASSWORD` 会被忽略并警告）。

| 键 | 含义 | 对应参数 |
|---|---|---|
| `lang` `geo_lookup` | 界面语言 `zh`/`en`；`geo_lookup=no` 不请求 ipinfo.io | `--lang` `--no-geo-lookup` |
| `docker_source` `docker_mirrors` | Docker 安装源（见 14.9）；Docker Hub 加速地址（空格分隔） | `--docker-source` `--docker-mirror` |
| `dir` `port` `domain` `email` `project` | 安装目录、HTTP 端口、域名、证书邮箱、compose 项目名 | `--dir --port --domain --email --project` |
| `behind_proxy` | `yes`：放在已有反向代理后面（需 `domain`） | `--behind-proxy` |
| `mirror` | `cn` / `default` | `--mirror` |
| `image_tag` `registry` | 镜像标签、仓库 | `--image-tag --registry` |
| `archive_dir` `spool_dir` `app_dir` `pg_dir` | 存储目录 | `--archive-dir` 等 |
| `extra_mount` | `名称=/宿主机/路径[:ro\|:rw]`，可重复 | `--extra-mount` |
| `db_mode` | `bundled` / `external`（或直接写 `postgres`/`mysql`/`mssql`） | `--db` |
| `db_provider` `db_host` `db_port` `db_name` `db_user` `db_tls` `db_ca_file` `db_schema` | 外部库连接设置（`db_tls`: `disable/require/verify-ca/verify-full`） | `--db-*` |
| `db_password` / `db_password_file` | 外部库密码（**仅限配置文件**） | `--db-password-file` |
| `secret_key_file` | 目标库已有 OA 数据时，原 `OA_SECRET_KEY` 所在的 600 文件 | - |
| `systemd` `open_firewall` `install_docker` | `yes/no` | `--systemd --open-firewall --install-docker` |

`--print-config`：把本次答案（**不含密码**）按同一格式写到标准输出后退出，不做任何更改，也不检查数据库连接。第二台服务器：`sudo bash install.sh --print-config > answers.conf`，在新机器上补上 `db_password=…`、`chmod 600 answers.conf`，再 `sudo bash install.sh --config answers.conf --yes`。

### 14.5 反向代理、外部数据库、旧数据

- **已有反向代理（nginx / Caddy / 1Panel / 宝塔）占用 80/443**：向导选「放在它后面」或无人值守加 `--behind-proxy --domain oa.example.com`。OA 只监听 `127.0.0.1:<端口>`，`.env` 写 `OA_TRUSTED_PROXY=1`、`OA_PUBLIC_URL=https://域名`、`OA_BEHIND_PROXY=1`，不启用 Caddy；结束时打印可直接粘贴的 nginx 与 Caddy 配置片段（关闭缓冲以支持 SSE、不限上传大小、传 `X-Forwarded-*`）。证书由你的代理负责。
- **使用已有数据库**：详见 `多数据库部署与迁移指南.md` §6.1。要点：在安装任何东西之前先用 `oa-admin db-check` 验证（连接、版本、权限、隔离性、空库或只含 OA 表），失败时可重试/修改/中止；通过后显示 DDL 预览，第二次确认后才建表；密码只经标准输入。
- **旧数据**：安装器拒绝在旧数据上生成新密钥。PostgreSQL 数据文件存在但没有 `.env`：有备份则恢复 `.env`（交互询问，`--yes` 需 `--adopt-data`）；没有则拒绝并说明（可用 `--pg-dir` 指向空目录，旧文件原样保留、绝不删除）。只有 `secret.key` 时沿用同一把密钥。

### 14.6 状态文件 `/etc/oa-agent/install.json`

每次安装成功后写入（无密钥，权限 644），每个安装一行 JSON：`dir`、`project`、`version`、`registry`、`db`（`bundled` 或 `postgres/mysql/mssql`）、`port`、`domain`、`status`（`installed`/`removed`）、各数据目录、`updated`。下次运行时先读它，再扫描容器标签、默认目录和 `.env`。`.env` 仍是版本与端口的权威来源；`oa-agentctl upgrade/uninstall` 会同步更新它。卸载（保留数据）把条目标为 `removed`，`--purge` 才删除条目；写不了（只读 `/etc`）只警告，不影响安装。

### 14.7 一次运行的样子（真实引擎上的记录）

以下是在真实 Docker 引擎（Docker Desktop 的 Linux 虚拟机 + 本地 `registry:2`，见开头的验证说明）上，用真实伪终端以管道方式（`cat install.sh | bash`）跑出来的输出，只裁掉了过长的行与无关的拉取进度。端口 18080 被一个测试容器占用，所以向导点名并拒绝了它。

```text
$ docker run --rm localhost:15000/koch2333/oa-agent-deploy:1.0.0 > install.sh        # on a real server: <registry>/oa-agent-deploy:latest | sudo bash -s -- ...
$ cat install.sh | sudo bash -s -- --registry localhost:15000/koch2333      # stdin is the script; the answers come from the terminal (/dev/tty)
        OA file-archiving agent - Linux one-click installer
==> Detection: what is already on this server
[PASS] OS: Debian GNU/Linux 12 (bookworm) / amd64
[PASS] Kernel 6.18.33.2-microsoft-standard-WSL2
[PASS] cgroup v2 (unified hierarchy)
[INFO] CPU: 20 core(s); RAM: 7616 MB; free disk at /opt: 947 GB
[PASS] RAM 7616 MB
[WARN] Cannot verify time synchronization: make sure NTP is enabled (TOTP depends on correct time).
[PASS] Host addresses: 192.168.65.6 192.168.65.3 [fdc4:f303:9324::6] [fdc4:f303:9324::3]
[INFO] systemd not detected: the containers restart with Docker (restart: unless-stopped); no systemd unit is offered.
[PASS] Required tools present: curl tar gzip sha256sum awk
[FOUND] Docker Engine 29.9.2: daemon running, boot state unknown, rootful, endpoint unix:///var/run/docker.sock -> reused, not reinstalled
[FOUND] Compose v2 plugin 5.6.1 (>= 2.24: the external-database layer works) -> reused
[PASS] OA Agent: no existing installation found (checked the state file, container labels/images, compose projects and /opt/oa-agent) -> fresh install
[PASS] Ports free: 80 443 8080
[FOUND] Existing database container: inst-pg (image postgres:16-alpine, running, ports 5432/tcp) -> a candidate only, never modified
Detection summary: 8 passed, 3 found, 0 missing, 0 need action, 1 warning(s), 0 failed
[PASS] Docker Hub is reachable (the postgres/caddy base images can be pulled directly)
[PASS] Image registry reachable: localhost:15000
==> Configuration wizard
One question at a time; the value in brackets is a default derived from the detection, press Enter to accept it. A review screen follows where every setting can be edited
Install directory (program, .env and mounts.json) [/opt/oa-agent]: relative/dir
[!] must be an absolute path with only letters, digits and _ . / - (no spaces), and not a system directory such as / /etc /opt /var itself
HTTP port [8080]: 70000
[!] the port must be a number from 1 to 65535
HTTP port [8080]: 18080
[!] port 18080 is already in use by Docker container inst-dummy (image alpine:3.21). Choose another (suggestion: 18081)
HTTP port [8080]: 18081
Domain (empty = HTTP only, no Passkeys) []:
Database:
  1) Use the bundled PostgreSQL (recommended for a new standalone server, nothing to prepare)
  2) Use an existing PostgreSQL 14+ server
  3) Use an existing MySQL 8.0+ server (MariaDB is not supported)
  4) Use an existing SQL Server 2019+ server
Choice [1-4] [1]: 9
[!] enter 1-4
Choice [1-4] [1]:
Working directory (archive root): every archived file is stored here. Any absolute local path works, e.g. a mounted data disk or a NAS mount point.
Archive root directory [/var/lib/oa-inst-test/a/opt/oa-agent/data/archive]: archive
[!] must be an absolute path with only letters, digits and _ . / - (no spaces), and not a system directory (/ /etc /usr /var /tmp /home ..., nor below /etc /usr /proc /va
Archive root directory [/var/lib/oa-inst-test/a/opt/oa-agent/data/archive]: /etc/oa-archive
[!] must be an absolute path with only letters, digits and _ . / - (no spaces), and not a system directory (/ /etc /usr /var /tmp /home ..., nor below /etc /usr /proc /va
Archive root directory [/var/lib/oa-inst-test/a/opt/oa-agent/data/archive]: /var/lib/oa-inst-test/a/srv/my archive
[!] must be an absolute path with only letters, digits and _ . / - (no spaces), and not a system directory (/ /etc /usr /var /tmp /home ..., nor below /etc /usr /proc /va
Archive root directory [/var/lib/oa-inst-test/a/opt/oa-agent/data/archive]: /var/lib/oa-inst-test/a/srv/archive
  ... (storage questions, mirror, image tag: 1.0.0) ...
== Review your settings (passwords are masked) ==
  1) Install dir        /var/lib/oa-inst-test/a/opt/oa-agent
  2) HTTP port          18081
  3) Access             plain HTTP (no Passkeys)
  4) Database           bundled PostgreSQL (container in the compose stack)
  5) Storage            archive /var/lib/oa-inst-test/a/srv/archive; spool /var/lib/oa-inst-test/a/opt/oa-agent/data/spool; app data /var/lib/oa-inst-test/a/opt/oa-agent/
  6) Mirror             default
  7) Images/tag         localhost:15000/koch2333 : 1.0.0
  Also: the systemd unit and the firewall are asked at the end; the admin account is created by the setup wizard with a one-time token (shown when the installation ends).
Proceed? [Y]es / [e]dit a setting / [c]ancel: e
Edit which setting? [1-7]: 2
HTTP port [18081]: 18081
== Review your settings (passwords are masked) ==
  1) Install dir        /var/lib/oa-inst-test/a/opt/oa-agent
  ... (pull, start, wait healthy) ...
  Installation complete!

  URL:             http://192.168.65.6:18081
  Other URL:       http://192.168.65.3:18081
  Other URL:       http://[fdc4:f303:9324::6]:18081
  Other URL:       http://[fdc4:f303:9324::3]:18081
  SETUP TOKEN:     5QJQPK5VL4IMVDSOWBOF677J57KGB64DGJO54QQ
  ...
  [What was detected and done]
  - already present, skipped: Docker Engine 29.9.2 already present, not reinstalled
  - already present, skipped: Compose plugin 5.6.1 already present
  - installed: Generated .env (mode 600, random secrets
  - State file: /etc/oa-agent/install.json
```


使用已有 PostgreSQL（先报错的密码、再报错的非专用库、最后通过，并显示 DDL 预览）：

```text
# existing PostgreSQL (container postgres:16-alpine reachable through the host); wrong password first, then a non-dedicated database, then the right one
Database:
  1) Use the bundled PostgreSQL (recommended for a new standalone server, nothing to prepare)
  2) Use an existing PostgreSQL 14+ server
  3) Use an existing MySQL 8.0+ server (MariaDB is not supported)
  4) Use an existing SQL Server 2019+ server
Choice [1-4] [1]: 2
  Database:        existing PostgreSQL host.docker.internal:55442/oa (user oa, TLS disable; password in .env)
==> Checking the existing database (before anything is installed)
  PostgreSQL host.docker.internal:55442 / dirty, user oa, TLS disable
  [FAIL] Connection: failed to connect to `user=oa database=dirty`: 192.168.65.254:55442 (host.docker.internal): failed SASL auth: FATAL: password authentication failed for user "oa" (SQLSTA
  r) retry with the same settings   c) change the database settings   a) abort (nothing is changed)
Choice [r/c/a] [r]: c
PostgreSQL host (for a database on this host use host.docker.internal or the LAN address, not localhost) [host.docker.internal]:
Port [55442]:
Database name (must be dedicated: empty, or holding only OA Agent tables) [dirty]: dirty
User [oa]:
TLS mode disable/require/verify-ca/verify-full (verify-full recommended for production) [disable]:
Database password (input is not echoed):
  PostgreSQL host.docker.internal:55442 / dirty, user oa, TLS disable
  [PASS] Connection: 延迟 2 ms
  [PASS] Character set / collation: UTF8；比较区分大小写（搜索使用不区分大小写的 ILIKE）
  [PASS] Time zone: 连接固定使用 UTC（当前会话 UTC），时间一律以 UTC 存储
  [PASS] Permissions: 对 schema public 具有 USAGE/CREATE
  [PASS] Least privilege: 账号不是超级用户，也没有 CREATEROLE / CREATEDB
  [FAIL] Isolation (dedicated database/schema): schema public 中存在非 OA Agent 的表（orders）。请使用专用数据库或专用 schema（空的或仅包含 OA Agent 表）；用
  [INFO] Schema version: 目标库为空，需要管理员批准后初始化
  [INFO] schema state: empty
  r) retry with the same settings   c) change the database settings   a) abort (nothing is changed)
Choice [r/c/a] [r]: c
PostgreSQL host (for a database on this host use host.docker.internal or the LAN address, not localhost) [host.docker.internal]:
Port [55442]:
Database name (must be dedicated: empty, or holding only OA Agent tables) [dirty]: oa
User [oa]:
TLS mode disable/require/verify-ca/verify-full (verify-full recommended for production) [disable]:
Database password (input is not echoed):
  PostgreSQL host.docker.internal:55442 / oa, user oa, TLS disable
  [PASS] Connection: 延迟 1 ms
  [PASS] Character set / collation: UTF8；比较区分大小写（搜索使用不区分大小写的 ILIKE）
  [PASS] Time zone: 连接固定使用 UTC（当前会话 UTC），时间一律以 UTC 存储
  [PASS] Permissions: 对 schema public 具有 USAGE/CREATE
  [PASS] Least privilege: 账号不是超级用户，也没有 CREATEROLE / CREATEDB
  [PASS] Isolation (dedicated database/schema): schema public 仅包含 OA Agent 的表（专用数据库）
  [INFO] Schema version: 目标库为空，需要管理员批准后初始化
  [INFO] schema state: empty
[ OK ] The database passed the checks (version, permissions and isolation are fine)
OA Agent would create its schema in host.docker.internal:55442/oa: 10 migration(s), 64 DDL statement(s) in total (plan fingerprint cdc8d473f01c3bd0d073b362).
    migration 1 001_initial.up.sql (3 statements)
    migration 2 002_business.up.sql (18 statements)
    migration 3 003_file_management.up.sql (5 statements)
    migration 4 004_llm_settings.up.sql (1 statements)
    migration 5 005_vision.up.sql (2 statements)
    migration 6 006_storage.up.sql (5 statements)
  ... (y: create the tables; the installation continues) ...
```


说明：数据库检查清单里的具体说明文字（括号后的中文）来自后端 `dbadmin` 包，目前只有中文；标题与结论行随界面语言。

### 14.8 故障排查（检测与向导）

| 现象 | 处理 |
|---|---|
| 退出码 2，提示「没有可用的终端来回答安装问题」 | 用 `ssh -t`；或先 `docker run --rm <deploy 镜像> > install.sh` 再 `sudo bash install.sh`；或加 `--yes`（配合 `--config`） |
| `[TODO] … 但该目录没有可用的 .env` | 状态文件/容器指向的安装目录被移动或删除；安装器不碰它。需要时用 `--dir` 指向新位置，或编辑/删除状态文件里对应行 |
| 「发现多套安装」 | 加 `--dir <目录>`；要另装一套独立的，用不同的 `--dir` 与 `--project` |
| 拒绝：旧数据库文件没有 `.env` | 恢复旧 `.env`（备份里的 `env`），或 `--pg-dir` 指向空目录，或自行移走旧数据；绝不会生成新密钥覆盖旧数据 |
| `--config` 报权限错误 | `chmod 600 <文件>`；不接受符号链接 |
| 外部库检查失败 | 清单里逐项说明原因（密码、网络、权限、隔离性）；主机不能填 localhost；本机数据库填 `host.docker.internal` 并让它监听该地址；`--db-tls disable` 仅用于测试 |
| 「目标库已有 OA Agent 数据」 | 提供原 `OA_SECRET_KEY`（交互输入或 `secret_key_file`），否则拒绝 |
| 80/443 被占用 | 用向导的「放在它后面」或 `--behind-proxy`；否则停掉占用者 |
| 想只看状态 | `install.sh --status`（不需要 root）；`--check` 额外打印将执行的计划 |

### 14.9 语言与 Docker 安装源（自动选择，和 1Panel 同一思路）

**界面语言（中文 / English）。** 没有用 `--lang`、`OA_LANG` 或配置文件指定时：

- 交互运行：**第一个问题**就是「请选择语言 / Select language: 1) 中文 2) English」，默认值来自系统区域设置（依次看 `LC_ALL`、`LC_MESSAGES`、`LANGUAGE`、`LANG`，再看 `/etc/default/locale`、`/etc/locale.conf`；`zh_CN`、`zh_TW`、`zh-Hans`、`ZH` 等不分大小写都算中文；`C`/`POSIX` 视为没有设置，继续看下一个来源）。答案从终端读取，所以 `curl … | sudo bash` 时也能问。选定后的横幅、提问、复核页、错误、`--help`（在其后）和传给子脚本的 `OA_LANG` 全部跟随；复核页第 9 项可以随时改语言。
- 无人值守（`--yes` / `--check` / `--status`）：不提问，按区域设置选择，并打印一行「界面语言：中文（来自系统区域设置；用 --lang zh|en 更改）」。
- 选择会写入**新安装**的 `.env`（`OA_LANG=zh|en`），`oa-agentctl.sh` 在环境变量 `OA_LANG` 未设置时沿用它；已有安装的 `.env` 不会被改动。配置键 `lang`，环境变量 `OA_INSTALL_LANG`。
- 外部数据库检查清单（`oa-admin db-check`）和 Go 版 `oa-agentctl mounts apply` 的进度/错误输出也跟随 `OA_LANG`（安装器会传）。**仍然只有中文的部分**：Go 版 `oa-agentctl` 的 TUI 与其余子命令、`oa-admin` 的其他命令、数据库检查清单里未收录的新句子（保持原文，不会编造翻译）、以及 docker/apt 等外部程序自己的输出。

**Docker 安装源默认自动，不提问。** 只在需要安装 Docker 时发生，做法参照 1Panel 的安装脚本（`curl ipinfo.io/country` 判断是否为 `CN`，是则对几个 docker-ce 镜像测速并选最快的作为 `DOWNLOAD_URL`，否则用官方源），但更保守、始终可见：

1. **检测国家**：请求 `https://ipinfo.io/country`（3 秒超时）。这会联系**第三方服务**（与 1Panel 相同），只发送标准的 HTTPS 请求；用 `--no-geo-lookup`、环境变量 `OA_INSTALL_GEO=off` 或配置 `geo_lookup=no` 关闭，之后只用本地信号。
2. **本地信号**（国家未知——被屏蔽、超时或已关闭——时才用）：阿里云 ECS 元数据里的地域是 `cn-*`、时区是 `Asia/Shanghai` 等、`download.docker.com` 3 秒内不可达 → 视为中国大陆；都不满足 → 官方。国家已知且不是 CN → 官方，不探测任何镜像。
3. **中国大陆**：对 阿里云、Azure 中国、腾讯云、清华 TUNA、中科大 USTC 的 `…/docker-ce/linux/` 各做一次 3 秒的真实探测，只保留有响应的，按响应时间排序，**最后加上官方源**；依次尝试，失败自动换下一个；全部失败再用发行版软件包。一个都没响应时直接用官方源并说明。
4. **始终打印一行**：例如 `Docker 安装源：自动：检测到中国大陆（ipinfo.io 返回国家 CN）→ 按速度依次尝试可达的镜像：aliyun azure tencent tuna ustc official`，并在执行前打印将运行的**完整命令**与来源（`get.docker.com` 脚本是第三方脚本）。
5. **命令如何构成**（已对照 get.docker.com 脚本原文核实）：`--mirror` 只接受 `Aliyun`（`https://mirrors.aliyun.com/docker-ce`）和 `AzureChinaCloud`（`https://mirror.azure.cn/docker-ce`）；环境变量 `DOWNLOAD_URL`（默认 `https://download.docker.com`）替换软件包仓库与 GPG 密钥的根地址，其余镜像（腾讯云 `https://mirrors.cloud.tencent.com/docker-ce`、清华 `https://mirrors.tuna.tsinghua.edu.cn/docker-ce`、中科大 `https://mirrors.ustc.edu.cn/docker-ce`、你自己的 https 地址）就是这样用的：`DOWNLOAD_URL=<根地址> sh get-docker.sh`。
6. **覆盖**：`--docker-source auto|official|aliyun|azure|tencent|tuna|ustc|<https 地址>`（默认 auto）、`OA_INSTALL_DOCKER_SOURCE`、配置键 `docker_source`、复核页第 8 项（可编辑，显示例如「Docker：将安装；Docker 安装源：自动：检测到中国大陆 …」）。指定具体源时只用它，失败后**仅交互运行**会问是否依次试其他内置源，`--yes` 不会悄悄换源（之后仍会回退到发行版软件包，和以前一致）。`--mirror cn` 仍可用：等于「已知是中国大陆」，跳过国家查询。
7. 自定义地址必须是 https，且目录结构与 `download.docker.com` 相同（`linux/<发行版>/gpg` 等）。

**Docker Hub 镜像加速（`daemon.json` 的 `registry-mirrors`）。**

- 本次运行**由本脚本新装了 Docker**，且判定为中国大陆：从一个很小的内置列表（`docker.1ms.run`、`docker.m.daocloud.io`、`docker.1panel.live`、`mirror.ccs.tencentyun.com`）里，对每个地址真实探测 `/v2/`（返回 200 或 401），**只写有响应的**；`daemon.json` 不存在/为空/`{}` 时直接写入并重启 Docker（此时没有其他容器），并在摘要里列出「已更改」。一个都没响应：什么都不写，打印手动做法。这些公共加速地址会失效或易手，所以每次安装都重新探测，不依赖固定结论。
- **Docker 本来就有**（或 `daemon.json` 已有内容）：保持原有的征求同意流程——备份、安全合并、写入前确认、重启再确认；`--yes`/无终端只打印手动步骤。
- `--docker-mirror URL`（可重复，`OA_DOCKER_MIRRORS`，配置键 `docker_mirrors`）仍是显式覆盖，走同意流程，不做自动探测。复核页第 8 项也能输入自己的地址。
- 生产镜像（api/worker）来自镜像仓库（ACR），只有 postgres/caddy 基础镜像来自 Docker Hub；拉取失败时的提示会说明这一点，并指向 `--docker-mirror` 或 `.env` 里的 `OA_PG_IMAGE`/`OA_CADDY_IMAGE`。

### 14.10 Docker 安装过程的进度、超时、锁与环境检查

get.docker.com 安装 `docker-ce` 时 apt 是静默的（`apt-get -y -qq install … >/dev/null`），在慢网络或 Debian 13 这类系统上可能几分钟没有任何输出。安装器因此：

1. **心跳**：把 get-docker.sh 放在自己的会话（进程组）里运行，输出实时转发并写入日志；每 15 秒（`OA_HEARTBEAT`）打印一行，例如 `仍在安装 Docker…已用 45 秒；当前进程：1234 00:41 apt-get -y -qq install docker-ce …；apt 下载目录 18432 KB（在增长）；dpkg 日志 3 秒前有更新`。下载目录在增长或 dpkg 日志在更新就是慢而不是卡死；两者都不动、当前进程不变就该怀疑卡住。
2. **超时**：默认 20 分钟（`OA_DOCKER_TIMEOUT`，秒）。超时后**只停止本脚本启动的那个进程组**（先 TERM，5 个轮询周期后 KILL），然后打印诊断：包管理器进程（ps）、谁持有 apt 锁（fuser/lsof）、`/var/log/dpkg.log` 与 `/var/log/apt/term.log` 的末尾、`systemctl status docker`。交互运行可选 `[r]重试同一个源 / [n]换下一个源 / [a]中止（默认）`；`--yes` 直接以退出码 1 失败并给出同样的提示。Ctrl-C 不会杀掉正在运行的 apt（避免 dpkg 半途中断），而是告诉你它的 pid 仍在后台，等它结束再重跑。
3. **安装前检查锁**：另一个 apt/dpkg/unattended-upgrades/dnf/yum 在运行时，先列出它并等待（`OA_LOCK_WAIT`，默认 300 秒，每个心跳周期报告一次）；交互时可选 `[w]等待（默认）/ [c]不管锁继续 / [a]中止`；`--yes` 等到超时后以明确提示失败。检查同样用于前置包与 Compose 插件的安装。
4. **环境检查**：没有 systemd 的容器或 WSL 里，docker-ce 的安装后脚本启动服务可能卡住或失败。检测表里和安装前都会警告并给出正确做法：WSL 在 `/etc/wsl.conf` 写 `[boot] systemd=true` 后 `wsl --shutdown`，或用 Docker Desktop 的 WSL 集成；容器里不要装 Docker，挂载宿主的 `/var/run/docker.sock`，或在宿主机上运行本安装器。交互时默认答案是「不继续」。
