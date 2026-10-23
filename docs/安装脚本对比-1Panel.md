# 安装脚本对比：1Panel 与 OA Agent（`deploy/linux/install.sh`）

> 目的：把 1Panel 的服务器安装脚本当作“成熟的一键安装体验”的参照，找出我们的 Linux 安装器在**生产环境**中缺什么，决定采纳或放弃，并记录理由。
>
> 来源与许可：研究对象是 `https://resource.fit2cloud.com/1panel/package/v2/quick_start.sh`（以及无 `v2` 的旧路径）和 GitHub `1Panel-dev/installer` 仓库 v2 分支的 `install.sh` / `1pctl`（读取日期 2026-10-23；`1Panel-dev/1Panel` 的 `dev-v2` 分支与 `1Panel-dev/installer` 的 `main` 分支上没有找到对应路径）。下载文件只读不执行，放在临时目录。1Panel 为 GPL-3.0，本项目为 AGPL-3.0；**我们没有复制它的代码**，只借鉴“做什么”的思路，实现全部是自己写的，所以没有需要在 `THIRD-PARTY-LICENSES.md` 里补记的改编代码。
>
> 重要：1Panel 是“面板 + 若干二进制”的安装，我们是“Docker Compose 栈 + 管理工具”的安装，所以并不是每一项都适合照搬。

## 1. 对比表

“采纳”= 已在本次改动中实现（括号内是选项名/位置）；“已有”= 之前就具备；“放弃”= 不做，理由在最后一列。

| # | 主题 | 1Panel 做法 | 我们原来的状态 | 决定 |
|---|---|---|---|---|
| 1 | curl\|bash 截断安全 | `quick_start.sh` 先下载并解压发行包，再运行包内 `install.sh`，所以管道截断不会半路执行；但 `install.sh` 本身是顶层顺序脚本，包里的文件被截断时没有保护 | 末行已是 `main "$@"`，但参数解析、`die` 等在顶层执行；`main` 若被截成不带参数的 `main` 会按默认值运行 | **采纳**（结束哨兵）：所有可执行逻辑都在函数里（含参数解析 `parse_args`），最后一行 `main "$@" "$EOS_SENTINEL"`；`main` 发现最后一个参数不是结束哨兵就拒绝执行。任意位置截断要么语法错误、要么到不了最后一行、要么没有哨兵——都不会安装任何东西。离线测试用多个截断点验证 |
| 2 | 传输/校验 | `curl -LOk` 下载（`-k` 关闭 TLS 校验）；`checksums.txt` 只用来判断“本地包是否可复用”，下载后**不校验**；另外会 `curl … installation-log.sh \| sh` 上报安装 | 发行包下载强制 `https://`、SHA-256 校验，缺校验和时 `--yes` 拒绝；预编译 `oa-agentctl` 校验 `SHA256SUMS` | **已有且更严格**；不采纳 `-k` 和上报 |
| 3 | root / sudo | 非 root 直接退出 | `require_root`，提示 `sudo` | 已有；`--check` 例外（不需要 root） |
| 4 | 系统/架构/内核检测 | `uname -a` 字符串匹配 amd64/arm64/armv7/ppc64le/s390x/riscv64；不检查内核/cgroup | 仅检查 Linux 与 x86_64/aarch64 | **采纳**（预检报告）：内核版本（<3.10 失败、<4.0 警告）、cgroup v1/v2（v1 警告）；架构仍只支持 amd64/arm64（镜像只构建这两种，其余放弃） |
| 5 | Docker 安装与镜像源 | 有 Docker 则只启动；没有则：内置离线 Docker 包 → 在线：大陆（用 `ipinfo.io` 判断国家）时对 4 个 docker-ce 镜像做延迟测速并选最快，再按 6 个脚本地址（含 jsdelivr 上的第三方副本）依次尝试，最后提示 `linuxmirrors.cn` | 仅 `get.docker.com`（`--mirror cn` 用 Aliyun），失败即退出 | **采纳**（`--install-docker`、`--mirror cn`）：官方脚本带重试（5/15 秒退避）；`--mirror cn` 先 Aliyun 再 AzureChinaCloud；脚本仍失败则回退到发行版软件包（apt `docker.io docker-compose-v2` / dnf / yum / zypper / apk）；全部失败给出明确错误和官方安装文档链接。**放弃**：延迟测速选源（多一次对外探测、收益有限，先用确定的顺序）、jsdelivr/GitHub 上的第三方脚本副本（以 root 执行，供应链风险）、第三方 IP 国家判断（`ipinfo.io`）、内置离线 Docker 包（体积大，发行版软件包回退已覆盖多数场景） |
| 6 | Docker 守护进程镜像加速 | 国家为 CN 时询问；写入 `daemon.json` 的 `registry-mirrors`；已有文件时询问后**整体替换**（备份为 `.1panel_bak`）；随后**立即重启 Docker** | 无 | **采纳**（`--docker-mirror URL`，可重复，`OA_DOCKER_MIRRORS`）：只在交互确认后才改；用 python3/jq/perl(JSON::PP) **解析并合并**（保留其他设置与已有镜像），修改前备份 `daemon.json.oa-bak.<时间>`，有 `dockerd --validate` 时先校验；已有文件不是合法 JSON 对象或没有 JSON 工具时**不碰文件**并打印手动步骤；文件不存在/为空/`{}` 时直接写入；重启 Docker 是第二次单独确认（运行中的容器会重启）；`--yes`/无终端永不修改。我们不内置任何第三方加速地址（它们会失效/易手），由用户提供。加速只影响 Docker Hub 的 `postgres`/`caddy`，生产镜像来自 ACR |
| 7 | 目录/端口/入口/账号/口令交互 | 安装目录、随机端口（10000-65535，检测占用）、随机“安全入口”、随机用户名和口令（口令带遮蔽输入）；末尾打印明文口令（日志里事后打码） | 安装目录、端口（默认 8080）、域名、邮箱、镜像源、各存储目录都可交互/参数；管理员由初始化向导 + 一次性令牌创建；数据库口令、Worker 令牌、`OA_SECRET_KEY` 随机生成且不覆盖 | **已有**（路径、端口、域名）；**放弃**随机端口/入口/账号口令：我们没有面板账号，初始化令牌每次启动重新生成，默认端口保持可预期，反向代理/防火墙规则更易管理 |
| 8 | 已有安装 | `which 1panel` 存在就直接退出（升级用 `1pctl update`）；数据目录里有 `core.db` 时复用 | 非交互：保留 `.env` 与数据，刷新文件并启动；`--upgrade`；`--uninstall [--purge]` | **采纳**（交互菜单）：检测到 `.env` 且在终端交互运行、没给动作参数时询问：1 修复/继续（默认）2 升级 3 重装（保留数据与 `.env`）4 卸载（保留数据）5 取消；新增 `--reinstall`（重建容器，不删数据）。卸载仍需手动输入目录名才能 `--purge`，`--yes` 不能确认清除 |
| 9 | 防火墙 | 检测到 firewalld/ufw 就**自动**放行端口（必要时还会启动防火墙服务） | 检测到才询问，`--yes` 下不改动；预检不提示 | **已有且更保守**（`--open-firewall`）；**采纳**预检里的提示、摘要里的“防火墙/云安全组”提醒（1Panel 也有安全组提醒）。**放弃**自动放行/自动启动防火墙：改动网络暴露面必须由人决定 |
| 10 | systemd | 安装 core/agent 两个服务，另有 procd/openrc/sysv 初始化脚本 | 可选 `oa-agent.service`（`--systemd`，开机 `compose up -d`；容器本身 `restart: unless-stopped`） | 已有；**放弃** procd/openrc/sysv（OpenWrt/Alpine OpenRC 不是目标平台，容器重启策略已能随 Docker 自启） |
| 11 | SELinux / swap / 时间同步 | 脚本里没有 | `doctor` 里有时间同步检查，安装时没有 | **采纳**（预检）：时间未同步或无法确认 → 警告（TOTP 依赖准确时间，`timedatectl`/`chronyc` 判断）；SELinux Enforcing → 警告并给出 `ausearch`、`chcon -Rt container_file_t`、`:z` 标签、`setenforce 0`（仅排障）等**文字**建议，脚本绝不改 SELinux；内存 <2 GB/<4 GB 警告并提示加 swap（不自动建 swap） |
| 12 | 预检报告 | 仅端口占用 | 资源/端口检查分散，遇到问题直接退出，没有汇总 | **采纳**：`[PASS] / [WARN] / [FAIL]` 逐行报告 + 汇总计数；FAIL 时在安装/改动任何东西**之前**退出（退出码 3），警告在最终摘要里再列一遍。检查项：架构、内核、cgroup、CPU/内存/磁盘、时间同步、SELinux、本机地址（含 IPv6-only/无地址提示）、防火墙、端口占用、Docker 版本（<20.10 失败）、Docker 数据目录空间、rootless Docker 警告、镜像仓库可达性 |
| 13 | 只读/试运行 | 无 | 无 | **采纳**（`--check` / `--dry-run`）：只读：不需要 root、不写日志、不加锁、不拉取/加载镜像、不装包、不登录；打印预检报告和“将执行的操作”（动作、目录、端口、镜像来源、Docker 是否会安装、存储目录、systemd/防火墙决定）；有 FAIL 退出码 3，否则 0；永不提示 |
| 14 | 安装日志 | `install.log` 在当前目录，口令事后用 `sed` 打码 | `/var/log/oa-agent-install.log`（0600，不含密钥） | 已有（更安全：日志从不写入密钥） |
| 15 | 彩色/步骤输出 | `log()` 带时间戳与颜色 | 彩色 `step/ok/warn/die`，中英文 | 已有；新增 `[PASS]`/`[INFO]` 行 |
| 16 | 语言选择 | 5 种语言，交互选择或 `--lang` | 中/英，按 `$LANG` 自动选择，`--lang zh\|en` | 已有（所有新增消息同样中英双语）；**放弃**更多语言 |
| 17 | 访问地址与摘要 | `ip route get 8.8.8.8` 找内网地址；**默认**请求 `api64.ipify.org` 取公网地址；打印地址、账号、口令、官网信息、安全组提醒 | 打印一个地址（`hostname -I` 第一个） | **采纳**（摘要框）：列出所有候选内网地址（IPv4 优先，过滤 docker/br-/veth 等虚拟网卡，IPv6 带方括号）、初始化令牌、各数据目录、管理命令（升级/重装/卸载/检查）、防火墙与云安全组提醒、“请保存好这些”（`OA_SECRET_KEY` 的位置与查看命令——**不**打印密钥本身、`secret.key`、备份也是机密、TOTP 恢复码）、预检警告回顾。外网地址仅在 `--detect-public-ip` 时探测：先云元数据（阿里云 ECS 链路本地地址，无第三方），再 `OA_PUBLIC_IP_URL`（默认 `https://api.ipify.org`，第三方，故需显式开启）；**默认永不访问第三方服务** |
| 18 | 离线安装包 | 安装包内含离线 Docker 与 appstore 包（`docker/`、`appstore.tar.gz`） | 无 | **采纳**（`--offline <file.tar>` + `scripts/save-images.sh`）：见 3.3 |
| 19 | 管理 CLI | `1pctl`（status/start/stop/restart/uninstall/reset/update/restore…） | `oa-agentctl`（Go TUI + bash 备用） | 已有 |
| 20 | 并发/中断 | 无 | 无 | **采纳**：`flock` 单实例锁（`/var/lock/oa-agent-install.lock`，`--check` 不加锁）；Ctrl-C/TERM 提示“可安全重跑”，退出码 130 |
| 21 | 网络可靠性 | 脚本下载有 `--retry`；镜像相关无 | `curl --retry`；`docker pull` 失败即退出 | **采纳**：`docker pull`/`compose pull`/get.docker.com/构建都带重试和退避（默认 3 次：5、15 秒；`OA_RETRIES`、`OA_RETRY_BASE` 可调），“镜像不存在/拒绝访问”等永久错误不重试；Docker 自带层级续传，重试从断点继续；仍失败时逐个镜像排查并指出**出错的具体镜像**；阿里云 ECS 在同地域时提示改用 VPC 内网地址（只提示，不擅自切换） |
| 22 | 退出码 | 失败统一 1 | 失败统一 1 | **采纳**：0 成功、1 失败、2 参数错误、3 环境检查未通过、4 用户取消、130 被中断（`--help`、脚本头部、部署指南都有说明） |

## 2. 1Panel 里我们**没有**采纳的做法（汇总）

- `curl -k`（跳过 TLS 校验）、下载后不校验、安装上报：不符合我们“校验后才使用”的原则。
- 默认访问 `ipinfo.io` / `api64.ipify.org`：我们默认不访问任何第三方服务。
- 自动改防火墙、整体替换 `daemon.json`、立即重启 Docker：这些都是会影响整机的副作用，必须询问；`--yes` 永远不等于同意。
- 以 root 执行 jsdelivr/GitHub raw 上的第三方 Docker 安装脚本副本。
- 随机端口/入口/用户名/口令：我们的管理员由初始化向导创建，并强制 MFA。
- 多语言（5 种）、OpenWrt/OpenRC/SysV 初始化脚本、内置离线 Docker 包：超出当前目标。

## 3. 本次实现要点

### 3.1 流程（新）

`解析参数 → root/锁 → 已有安装菜单（交互）→ 配置问答 → 预检（系统部分；FAIL 即退出 3）→ 安装/检查 Docker → 预检（Docker/镜像仓库）→（可选）镜像加速 → 获取安装包（本地源码 / 镜像 oa-agent-deploy / --offline 载入）→ 生成配置（不覆盖 .env）→ 拉取或载入镜像 → 启动并等待健康 → 摘要`。`--check` 在“配置问答”之后、任何改动之前结束。

### 3.2 与“保持现状”的约束

内置的生产镜像仓库 `DEFAULT_REGISTRY`、`--registry`、`--build-from-source`/`OA_BUILD_FROM_SOURCE`、已有安装沿用 `.env` 记录的镜像来源、管道安装（`docker run --rm <deploy 镜像> | sudo bash`、`curl | bash`）、`.env` 不覆盖、密钥不重新生成、`mounts.json`/`oa-agentctl mounts apply`/`--archive-dir` 等全部保持不变，原有 372 条断言继续通过。

### 3.3 离线安装

1. 在能访问仓库的机器上：`scripts/save-images.sh --registry <仓库/命名空间> --tag sha-<7位> [--out DIR] [--with-caddy] [--arch amd64|arm64]`。脚本把 api、worker、deploy（以及默认的 PostgreSQL 基础镜像，`--no-postgres` 可去掉）`docker save` 成**一个** tar，写出 `.sha256`，**先校验再发布**（tar 里必须列出每个镜像、校验和能复算），并从 deploy 镜像取出对应的 `install.sh`。
2. 把三个文件拷到服务器：`sudo bash install.sh --offline oa-agent-images-<tag>-<arch>.tar [其他参数]`。安装器读 tar 里的 `manifest.json` 得到仓库名和标签（与显式的 `--registry/--image-tag` 冲突则报错），校验 SHA-256（缺校验和时 `--yes` 拒绝，可用 `--skip-verify` 或环境变量 `OA_OFFLINE_SHA256`），`docker load`，确认三个镜像都在、架构与本机一致，然后从已载入的 deploy 镜像取出安装包，全程不访问镜像仓库。
3. 升级：做一个新 tar，`sudo bash install.sh --offline <新 tar> --upgrade`（已有安装必须显式 `--upgrade` 才会换标签；`oa-agentctl upgrade` 在离线安装脚本里以 `OA_OFFLINE=1` 运行，不拉取、不刷新管理文件）。

### 3.4 尚未验证/局限（务必知悉）

- apt/dnf/yum/zypper/apk 的软件包回退链只在假的包管理器 shim 下测过；各发行版的包名和是否带 Compose v2 不同，回退失败时脚本会明确报错并指向官方文档。
- `daemon.json` 的“解析并合并”依赖 python3/jq/perl(JSON::PP)；在没有任何一个的主机上，非空 `daemon.json` 一律不修改（只打印手动步骤）。
- 预检阈值（内核 3.10/4.0、Docker 20.10、磁盘 5/20 GB、内存 2/4 GB）是我们保守选定的，不是对每个发行版的实测结论。
- 阿里云 ECS 探测只访问链路本地元数据地址 `100.100.100.200`（1 秒超时），且只在镜像仓库是阿里云地址时才探测。
