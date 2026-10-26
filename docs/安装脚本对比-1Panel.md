# 安装脚本对比：1Panel 与 OA Agent（`deploy/linux/install.sh`）

> **本文分两部分。** 第一部分（§A、§B、§C）是 2026-10-25 重做的、针对用户真正关心的两件事：1Panel 的安装器**如何检测已有组件来判断是否需要安装**（§A 检测逻辑），以及它**如何与用户交互**（§B 交互逻辑），并给出逐项采纳/改造/放弃的对照表（§C）。第二部分（§D 起）是此前那一轮的对比，偏重命令行参数、退出码、重试等，保留作参考，它**没有**回答检测与交互这两个问题。
>
> 来源与许可：研究对象是 `https://resource.fit2cloud.com/1panel/package/v2/quick_start.sh`、无 `v2` 的旧路径 `…/package/quick_start.sh`，以及 GitHub `1Panel-dev/installer` 仓库 `v2` 分支的 `install.sh`、`1pctl`、`lang/en.sh`、`initscript/`（读取日期 2026-10-25；分支只有 `v1`、`v1.10.21`、`v2`，没有 `main`/`dev`）。下载文件只读不执行，放在临时目录。1Panel 为 GPL-3.0，本项目为 AGPL-3.0；**没有复制它的代码**，只借鉴“做什么”，实现全部自己写。所以没有需要在 `THIRD-PARTY-LICENSES.md` 补记的改编代码。
>
> 重要：1Panel 是“面板 + 两个二进制 + systemd 服务”，我们是“Docker Compose 栈 + 管理工具”，组件不同，不能逐项照搬。

## A. 检测逻辑（1Panel 实际做了什么，我们怎么做）

### A.1 1Panel 实际的检测（读 `install.sh` v2 得到的事实）

`quick_start.sh` 只负责：用 `uname -a` 字符串匹配得到架构（amd64/arm64/armv7/ppc64le/s390x/riscv64，否则退出）→ 取最新版本号 → 下载 `tar.gz` 并（仅当本地已有同名包时）用 `checksums.txt` 比对哈希决定是否跳过下载 → 解压后运行包内 `install.sh`。之后的检测全部在 `install.sh` 里：

| 检测点 | 1Panel 的做法 | 它据此决定什么 |
|---|---|---|
| 是否已安装 1Panel | `which 1panel`：存在就记录“已安装，请勿重复安装”并 `exit 1`。**没有**“升级/修复/重装”提示（升级走面板内或 `1pctl update`）。不看 systemd 单元、不看版本 | 已安装 → 直接中止 |
| 旧数据 | 选定安装目录后，若存在 `<目录>/1panel/db/core.db` 则 `USE_EXISTING=true`：不清空 `<目录>/1panel/*`，并把 `1pctl` 里的 `CHANGE_USER_INFO=use_existing`，即沿用旧账号信息；否则 `rm -rf <目录>/1panel/*` | 复用旧数据 / 清空重来 |
| Docker | `which docker`；有则取 `docker --version` 的前两段数字；用 `systemctl start docker && systemctl status docker`（无 systemd 则 `service dockerd …`）的输出里有没有 `running` 判断守护进程；主版本 <20 只记一条“建议手动升级”警告，**不拦截** | 有且在运行 → 跳过安装；有但没运行 → 只警告；没有 → 询问是否安装 |
| Compose | **不检测**（既不查 v1 也不查 v2 插件）；Docker 版本也不与 Compose 关联 | 无 |
| 镜像加速 | 有 Docker 时：若 `curl ipinfo.io/country` 为 `CN` 则询问是否配置 `registry-mirrors`；`/etc/docker/daemon.json` 已存在则再询问是否**整体替换**（先备份），写完立即重启 Docker | 仅国家判断，第三方服务 |
| 在线/离线安装 Docker | 安装包里有 `docker/` 目录就询问用内置离线包还是在线；在线时 CN 用 4 个镜像源延迟测速选最快 | 决定 Docker 来源 |
| 端口 | 默认随机 10000–65535；用 `netstat -tlun \| grep ":$port "`（无则 `ss`，再无则 `lsof`）判断占用；占用则重问（无人值守最多换 10 次随机口）。**不显示谁占用** | 占用 → 重问 |
| 防火墙 | 有 `firewall-cmd`/`ufw` 就**自动**放行所选端口，必要时先启动防火墙服务 | 自动改防火墙 |
| systemd | `command -v systemctl`：有则安装 core/agent 两个 unit；否则按 `/etc/rc.common`(procd)、`/sbin/openrc-run`(OpenRC)、SysV 三选一 | 决定服务管理方式 |
| root | `Check_Root`：非 root 退出 | 中止 |
| 系统资源、内核、内存、磁盘、cgroup、SELinux、时间同步、已有数据库服务、反向代理 | **都不检查** | 无 |
| 结束后 | 轮询 systemd 状态最多 5 次、`ss -tlun` 等端口最多 30 次；用 `ip route get 8.8.8.8` 找内网地址，**默认**请求 `api64.ipify.org` 取公网地址 | 报告访问地址 |

结论：1Panel 的“检测”很浅——只有“有没有 1panel 命令”“有没有 core.db”“有没有 docker 命令/是否在运行”“端口是否被占”四类，决定“中止/复用/跳过 Docker 安装/重问端口”。没有检测 Compose、已有数据库、端口占用者、系统资源、旧版本比较，也没有“同版本且健康就什么都不做”的快速路径。

### A.2 我们现在的检测阶段（`install.sh` 先于一切运行，只读）

先打印一张组件表（每行一个状态词，同时写入安装日志），再用结果驱动后面的每个决定：

| 状态词 | 含义 |
|---|---|
| `[PASS]` | 满足要求，无需动作 |
| `[FOUND]` | 已存在，沿用或不碰（已有 Docker、已有安装、别的数据库服务、占用端口的进程） |
| `[MISS]` | 缺少，安装器将只补这一项（需要同意：`--install-docker` / `--install-prereqs` 或交互确认） |
| `[TODO]` | 需要决定或处理（Docker 没运行、compose 只有 v1、旧数据没有 .env、状态文件指向的目录丢了） |
| `[WARN]` / `[FAIL]` | 风险 / 不能继续（FAIL 在改动任何东西之前以退出码 3 结束） |
| `[INFO]` | 仅供参考 |

检测内容与它如何决定“装不装”：

| 行 | 检测方法（均只读） | 决定 |
|---|---|---|
| 系统/内核/cgroup/内存/CPU/磁盘/时钟/SELinux/本机地址 | `uname`、`/etc/os-release`、`stat -f /sys/fs/cgroup`、`/proc/meminfo`、`df`、`timedatectl`/`chronyc`、`getenforce`、`ip` | 阈值给 PASS/WARN/FAIL；磁盘在**最终安装目录确定后**再判 |
| 前置工具 | `curl tar gzip sha256sum awk`（必需）与 `openssl flock ss`（推荐）逐个 `command -v` | **只**用检测到的包管理器（apt/dnf/yum/zypper/apk）装缺少的那几个包 |
| systemd | `systemctl` 且 `/run/systemd/system` 存在 | 决定是否提供开机单元 |
| Docker 引擎 | CLI 是否存在（Podman 冒充会警告）；`docker info` 判断守护进程；`docker version` 取版本（<20.10 失败）；`systemctl is-enabled docker`；`docker info` 的 SecurityOptions 判 rootless；`DOCKER_HOST`/`docker context` 取端点 | 有且运行 → **跳过安装**；有但没运行 → 只尝试启动；没有 → 才安装 |
| Compose | `docker compose version --short`（v2 插件，≥2.24 才能用外部数据库层）；插件缺失时再看有没有旧版 `docker-compose` | 缺 v2 插件 → **只装插件**，不重装 Docker |
| 已有 OA Agent 安装 | 依次找：状态文件 `/etc/oa-agent/install.json`、`/etc/oa-agent/install_dir`、`/opt/oa-agent`、`--dir`、`docker ps -a` 的容器标签（`com.docker.compose.project(.working_dir)`）与镜像名 `oa-agent-api/worker`、`docker compose ls`；再解析各目录 `.env`（标签、项目名、端口、域名、数据库模式、仓库）并用容器状态判断健康 | 见 A.3 的决策表 |
| 旧数据（孤儿数据） | 目录里有 `secret.key`、`pgdata/PG_VERSION`、非空归档、可恢复 `.env` 的备份，但没有 `.env` | 沿用同一把密钥、从备份恢复 `.env`，或带解释地拒绝；**绝不在旧数据上生成新密钥** |
| 端口 | 80/443/所选端口；`ss -ltnp`（无则 `lsof`）取占用进程，再对照 `docker ps` 的端口映射 | 占用者是反向代理 → 给“放在它后面”的配置片段；否则建议下一个空闲端口 |
| 已有数据库服务 | 5432/3306/1433 的监听者（含绑定地址是否只有 127.0.0.1）、`systemctl list-unit-files` 里的 postgresql/mysql/mariadb/mssql-server、`docker ps` 里的 postgres/mysql/mariadb/mssql 镜像（排除本安装自己的） | 只报告，作为“使用已有数据库”的候选；**不连接、不试任何凭据** |
| 防火墙 | ufw / firewalld / iptables INPUT DROP | 只报告，是否放行在结束时询问 |
| 网络 | 镜像仓库可达性、Docker Hub 可达性（4 秒超时）、阿里云 ECS 元数据（原有） | 只用来**预选**镜像源问题的默认答案（cn/default），由人确认；`--yes` 下不悄悄改 |

### A.3 已有安装的决策表（同一份检测结果，不同处理）

| 检测到 | 交互（终端） | 无人值守 `--yes` |
|---|---|---|
| 没有安装 | 向导 | 用参数/配置文件/默认值安装 |
| 已安装、健康、版本与请求一致，无变更请求 | 菜单默认选 6「无需操作：显示状态与访问信息」 | 只显示状态与访问信息，退出码 0（快速、只读为主，不拉镜像、不 `up`） |
| 已安装但不健康/已停止 | 菜单默认选 1「修复/继续」 | 修复/继续（保留 `.env` 与数据） |
| 请求的标签更新（semver/`sha-`/可移动标签） | 菜单提示“可升级：x -> y”，选 2 升级 | 需显式 `--upgrade` |
| 请求的标签更旧 | 提示数据库迁移不可回退 | 拒绝，除非 `--allow-downgrade` |
| 多套安装 | 先问管理哪一套 | 要求 `--dir` |
| 状态文件/容器指向的目录没有 `.env` | `[TODO]` 行，不碰它 | 同左 |
| 新装目录的 compose 项目名与已有安装相同 | 自动换成 `oa-agent-2…` 并说明 | 同左；显式 `--project` 冲突则拒绝（同名项目会互相接管容器） |

## B. 交互逻辑（1Panel 实际做了什么，我们怎么做）

### B.1 1Panel 的实际交互

| 方面 | 1Panel 的做法 |
|---|---|
| 提问顺序 | 语言（5 种，数字选择，写入 `.selected_language`）→ 安装目录（默认 `/opt`，120 秒超时后用默认）→ 是否安装 Docker（`[y/n]`，默认 y；有内置离线包再问一次）→ 是否配置镜像加速（仅 CN）→ 面板端口 → 安全入口 → 用户名 → 密码。一问一答，没有汇总页 |
| 默认值 | 端口随机（10000–65535），入口/用户名/密码都是 `md5sum` 截取的 10 位随机串，直接回车采用 |
| 校验与重问 | 目录必须以 `/` 开头；端口 1–65535 且未被占用；入口与用户名 `^[a-zA-Z0-9_]{3,30}$`；密码 `^[a-zA-Z0-9_!@#$%*,.?]{8,30}$`。非法则提示并**无限重问**（`continue`），没有次数上限；是/否问题非法也一直重问 |
| 密码输入 | 若有 `stty`：`stty cbreak -echo` + `dd if=/dev/tty bs=1 count=1` 逐字符读，打 `*`，支持退格；这是唯一显式读 `/dev/tty` 的地方 |
| 通过管道 | `quick_start.sh` 先下载解压再 `bash install.sh`（脚本是文件），所以其他提问用普通 `read -p` 读**标准输入**；如果用户把整个安装脚本通过管道喂给 bash，这些 `read` 会读到脚本自身，**没有任何 `/dev/tty` 兜底，也没有“没有终端”的提示** |
| 无人值守 | `--non-interactive/-y` 或环境变量；每个值都有对应参数/环境变量（`PANEL_PORT` 等，**包括密码参数** `--password`，脚本里自己建议用环境变量）；未给的项用默认值；Docker 安装与镜像加速在无人值守下默认 **不做**；给出的值非法直接退出 |
| 确认/汇总 | 没有确认页；结束时 `Show_Result` 打印外网/内网地址、用户名、**明文密码**、官网/文档链接、安全组提醒；日志文件里事后用 `sed` 把密码打码 |
| 其他 | 没有“返回上一步/修改某项”；没有 `--config` 文件；没有 `--print-config`；无人值守没有“缺少必需答案就失败”的概念（所有项都有随机默认） |

### B.2 我们的向导（`install.sh`，仅全新安装时）

- **显示检测结果在前，再一次问一个问题，默认值来自检测**：安装目录 → HTTP 端口（默认被占用则建议下一个空闲端口并说明占用者）→ 域名/HTTPS（及邮箱；80/443 被反向代理占用时给出三选一：放在它后面〔默认，结束时打印 nginx/Caddy 片段〕/忽略域名用 HTTP/仍用内置 Caddy）→ 数据库（内置 PostgreSQL〔推荐〕/ 已有 PostgreSQL 14+ / MySQL 8+ / SQL Server 2019+；已有库依次问主机、端口、库名、用户、**隐藏输入的密码**、TLS 模式、CA 文件）→ 存储目录（归档 / 暂存 / 应用数据 / 数据库目录、额外挂载）→ 镜像源（默认值依检测预选）→ 镜像标签。
- **复核页**：列出每一项（密码遮蔽），问 `[Y]继续 / [e]修改某项 / [c]取消`，选 `e` 后按编号重问该项；无效输入提示再问。
- **校验与重问**：每个答案都有友好的校验消息；无效最多 5 次，超过以退出码 2 中止（不会无限循环，也不会悄悄采用默认）。
- **外部数据库在安装任何东西之前先验证**：用 `oa-agent-api` 镜像里的 `oa-admin db-check`（复用 `internal/dbadmin`）检查连接、版本、权限、隔离性、是否为空库或只含 OA 的表；密码只经标准输入传入容器，不出现在进程参数、环境、日志、屏幕；失败时给出原因并让你选 重试/修改设置/中止。通过后展示 DDL 预览（迁移数、语句数、计划指纹），要**第二次明确确认**才执行 `oa-admin db-init --apply --plan <指纹>`；无人值守不替你批准 DDL。
- **读取答案的位置**：`open_tty` 打开 `/dev/tty`（`OA_INSTALL_TTY` 仅测试用），所以 `curl … | sudo bash` 与 `docker run … | sudo bash` 时提问照样可用（脚本占用标准输入，答案取自终端）；隐藏输入用 `read -s`。**没有终端且没有 `--yes`**：打印明确说明（ssh 加 `-t`、先下载再运行、或加 `--yes`/`--config`）并以退出码 2 结束，**不会**悄悄装一个默认的纯 HTTP 实例。
- **预置答案**：`--config <文件>`（key=value；权限必须 600，符号链接/他人可读的文件被拒；**密码只能写在这个文件里**，命令行与环境变量一律不接受）、`OA_INSTALL_<KEY>` 环境变量、各命令行参数。优先级：命令行 > 环境变量 > 配置文件 > 向导 > 默认值。配置文件/环境变量给出的项不再追问，只出现在复核页。`--print-config` 把答案（**不含密码**）按同一格式写到标准输出，供第二台服务器复用。`--yes` 接受默认值，但没有安全默认的必需答案（外部库的主机、密码）会失败并说明。
- **结束摘要**：访问地址、初始化令牌、数据库模式、各目录、状态文件位置，以及“检测与处理结果”（因已存在而跳过了什么、装了什么、改了什么）、下一步与重跑/升级/卸载方法。

## C. 1Panel 行为 → 我们的做法（采纳 / 改造 / 放弃）

| # | 1Panel 行为 | 决定 | 我们的做法与理由 |
|---|---|---|---|
| 1 | `which 1panel` 存在就中止 | **改造** | 不中止：识别已有安装（状态文件、容器标签、.env），按 A.3 给出 状态/修复/升级/重装/卸载；同版本健康则“无需操作” |
| 2 | 有 `core.db` 就复用旧数据，否则清空目录 | **改造（更安全）** | 绝不 `rm -rf` 任何东西。旧数据存在且没有 `.env`：沿用旧 `secret.key`、从备份恢复 `.env`，或拒绝并解释；旧数据库文件没有对应 `.env` 时一律拒绝 |
| 3 | `which docker` + `systemctl start docker` 输出里找 `running` | **采纳并加强** | 有 CLI → 再看 `docker info`、版本 ≥20.10、是否开机自启、rootless、端点；已有且运行则跳过；没运行只启动；缺才装 |
| 4 | Docker 主版本 <20 仅警告 | **改造** | <20.10 直接 FAIL（改动前以退出码 3 结束），因为 compose 文件用到的特性需要它 |
| 5 | 不检测 Compose | **新增** | 检测 v2 插件及版本（≥2.24 才允许外部数据库层），只有旧 v1 时**只装插件**，不重装 Docker |
| 6 | 内置离线 Docker 包、在线测速选源、第三方脚本副本 | **放弃**（沿用此前决定） | 体积、供应链风险；用 get.docker.com + 发行版软件包回退 |
| 7 | 用 `ipinfo.io/country` 判断是否在中国 | **改造** | 不访问第三方定位服务；用 Docker Hub 4 秒可达性探测，**只预选**镜像源问题的默认答案，由人确认；`--yes` 不悄悄改 |
| 8 | 自动写 `daemon.json` 并立即重启 Docker | **放弃** | 仍只在 `--docker-mirror` + 交互确认后修改，重启再确认一次 |
| 9 | `netstat/ss/lsof` 判断端口占用，不说谁占用 | **采纳并加强** | 报告占用者（进程/容器），建议空闲端口；80/443 被反向代理占用时打印 nginx/Caddy 配置片段，并支持 `--behind-proxy` |
| 10 | 自动放行防火墙、必要时启动防火墙 | **放弃** | 只报告，放行在结束时单独询问；`--yes` 不改 |
| 11 | systemd / procd / OpenRC / SysV | **部分** | 只做 systemd（可选）；其他平台靠容器 `restart: unless-stopped` |
| 12 | 一问一答、无汇总、无回退 | **改造** | 复核页 + 按编号修改 + 取消 |
| 13 | 无限重问 | **改造** | 最多 5 次，超过以退出码 2 中止，避免死循环 |
| 14 | 随机端口/入口/用户/密码 | **放弃** | 管理员由初始化向导 + 一次性令牌创建并强制 MFA；默认端口可预期，更适合防火墙/代理规则；默认端口被占用时才建议空闲端口 |
| 15 | 密码逐字符读 `/dev/tty` 并打 `*` | **改造** | 外部库密码用 `read -s` 从终端读，不回显，不进参数/环境/日志 |
| 16 | 管道执行时提问读标准输入（会读到脚本自己） | **改造** | 提问一律读 `/dev/tty`；没有终端且无 `--yes` 时明确报错 |
| 17 | `--password` 参数 / `PANEL_PASSWORD` 环境变量 | **放弃** | 秘密只能来自 600 权限的配置文件或隐藏输入 |
| 18 | 无人值守每项都有随机默认 | **改造** | 有安全默认才用默认；外部库主机/密码没有安全默认 → 失败并说明 |
| 19 | 无配置文件 / 无导出 | **新增** | `--config`、`OA_INSTALL_*`、`--print-config` |
| 20 | 结束时打印明文密码，日志事后打码 | **放弃** | 安装器不生成账号口令；摘要只打印初始化令牌，日志从不写入密钥 |
| 21 | 安装上报 `installation-log.sh`、`curl -k` | **放弃** | 不上报，强制 TLS 与校验 |
| 22 | 无“升级/降级”判断 | **新增** | 标签比较：semver 排序、`sha-`、可移动标签；降级默认拒绝 |
| 23 | 无已有数据库概念 | **新增** | 检测本机数据库服务作为候选；外部库先验证（连接/权限/隔离性）再征求建表批准 |
| 24 | 无状态文件 | **新增** | `/etc/oa-agent/install.json`（目录、项目、标签、仓库、数据库模式、端口，无密钥），先读它再扫描；`.env` 仍是版本/端口的权威来源 |

## C2. 语言与 Docker 安装源/镜像加速（2026-10-26 补充）

**1Panel 实际做的（`install.sh` v2）。** 语言：`--lang` / `PANEL_LANG`，否则交互时列出 5 种语言让用户按数字选，选择写入 `.selected_language`；无人值守按 edition（`cn` → `zh`，否则 `en`），**不看系统区域设置**。Docker 安装源：只在**没有 docker 命令**且用户同意安装时发生；有内置离线包就先问用哪个；在线时用 `curl ipinfo.io/country` 判断，`CN` 则对 4 个 docker-ce 镜像（阿里云、腾讯云、网易、教育网）各测 2 次延迟取最快的作为 `DOWNLOAD_URL`，再从 6 个 get-docker 脚本地址（含 jsdelivr 上的第三方副本）依次下载并运行；非 CN 直接用 `get.docker.com` + `download.docker.com`。镜像加速（`daemon.json` 的 `registry-mirrors`）：国家为 CN 时询问（Docker 已存在也问），写入固定的第三方地址（默认 docker.1panel.live / hub.1panel.dev / docker.1ms.run，腾讯云内网可 ping 通时改用其内网镜像），已有 `daemon.json` 时询问后**整体替换**，随后立即重启 Docker。

| 方面 | 1Panel | 我们 | 决定 |
|---|---|---|---|
| 语言选择 | 交互按数字选 5 种；无人值守按 edition；不看区域设置 | 交互运行的**第一个问题**（中文/English，默认取自 `LC_ALL`/`LANGUAGE`/`LANG`、`/etc/default/locale`、`/etc/locale.conf`，读自 `/dev/tty`）；无人值守按区域设置并打印一行说明；`--lang`/`OA_LANG`/配置 `lang` 覆盖；写入新装的 `.env`（`OA_LANG`） | 采纳并加强（只做中/英） |
| 国家判断 | `ipinfo.io/country`，被屏蔽时就当作非 CN | 同样用 `ipinfo.io/country`（3 秒超时），**另有本地信号兜底**（阿里云 ECS 地域、时区、`download.docker.com` 是否可达），并可用 `--no-geo-lookup`/`OA_INSTALL_GEO=off` 关闭第三方查询 | 采纳并加强；文档明示这是第三方请求 |
| 选源 | CN：测速选最快的 1 个，再换脚本地址；非 CN：官方 | CN：对 5 个镜像各做一次真实探测，只保留有响应的，按速度排序，**最后加官方**，失败自动换下一个，全部失败再用发行版软件包；非 CN：官方（不探测镜像） | 采纳（自动、无提问） |
| 是否可见 | 只打印选中的源 | 始终打印一行「检测到什么 → 将用什么」与将执行的完整命令；复核页第 8 项可编辑；`--docker-source` 覆盖 | 采纳并加强 |
| 脚本来源 | 6 个地址，含 jsdelivr/GitHub 上的第三方副本 | 只用 `get.docker.com`；镜像通过脚本自己的 `--mirror`/`DOWNLOAD_URL` 机制 | 放弃第三方副本（供应链） |
| 镜像加速 | CN 时询问并写固定第三方地址，整体替换，立即重启 | 本脚本**新装** Docker 且在大陆：只写**当场探测 `/v2/` 有响应**的内置地址，`daemon.json` 为空才直接写并重启；已有 Docker 或已有内容：备份、安全合并、写前确认、重启再确认；`--docker-mirror` 显式覆盖 | 改造（自动但可验证、不碰别人的配置） |

---

# D. 此前一轮的对比（命令行参数、退出码、重试等）

> 下面是之前的内容，只覆盖参数与加固项，保留作参考。其中“已有安装”“端口/目录/口令交互”两行已被上面的 §A–§C 取代。

## D.1 对比表

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

## D.2 1Panel 里我们**没有**采纳的做法（汇总）

- `curl -k`（跳过 TLS 校验）、下载后不校验、安装上报：不符合我们“校验后才使用”的原则。
- 默认访问 `ipinfo.io` / `api64.ipify.org`：我们默认不访问任何第三方服务。
- 自动改防火墙、整体替换 `daemon.json`、立即重启 Docker：这些都是会影响整机的副作用，必须询问；`--yes` 永远不等于同意。
- 以 root 执行 jsdelivr/GitHub raw 上的第三方 Docker 安装脚本副本。
- 随机端口/入口/用户名/口令：我们的管理员由初始化向导创建，并强制 MFA。
- 多语言（5 种）、OpenWrt/OpenRC/SysV 初始化脚本、内置离线 Docker 包：超出当前目标。

## D.3 当时的实现要点

### D.3.1 流程（新）

`解析参数 → root/锁 → 已有安装菜单（交互）→ 配置问答 → 预检（系统部分；FAIL 即退出 3）→ 安装/检查 Docker → 预检（Docker/镜像仓库）→（可选）镜像加速 → 获取安装包（本地源码 / 镜像 oa-agent-deploy / --offline 载入）→ 生成配置（不覆盖 .env）→ 拉取或载入镜像 → 启动并等待健康 → 摘要`。`--check` 在“配置问答”之后、任何改动之前结束。

### D.3.2 与“保持现状”的约束

内置的生产镜像仓库 `DEFAULT_REGISTRY`、`--registry`、`--build-from-source`/`OA_BUILD_FROM_SOURCE`、已有安装沿用 `.env` 记录的镜像来源、管道安装（`docker run --rm <deploy 镜像> | sudo bash`、`curl | bash`）、`.env` 不覆盖、密钥不重新生成、`mounts.json`/`oa-agentctl mounts apply`/`--archive-dir` 等全部保持不变，原有 372 条断言继续通过。

### D.3.3 离线安装

1. 在能访问仓库的机器上：`scripts/save-images.sh --registry <仓库/命名空间> --tag sha-<7位> [--out DIR] [--with-caddy] [--arch amd64|arm64]`。脚本把 api、worker、deploy（以及默认的 PostgreSQL 基础镜像，`--no-postgres` 可去掉）`docker save` 成**一个** tar，写出 `.sha256`，**先校验再发布**（tar 里必须列出每个镜像、校验和能复算），并从 deploy 镜像取出对应的 `install.sh`。
2. 把三个文件拷到服务器：`sudo bash install.sh --offline oa-agent-images-<tag>-<arch>.tar [其他参数]`。安装器读 tar 里的 `manifest.json` 得到仓库名和标签（与显式的 `--registry/--image-tag` 冲突则报错），校验 SHA-256（缺校验和时 `--yes` 拒绝，可用 `--skip-verify` 或环境变量 `OA_OFFLINE_SHA256`），`docker load`，确认三个镜像都在、架构与本机一致，然后从已载入的 deploy 镜像取出安装包，全程不访问镜像仓库。
3. 升级：做一个新 tar，`sudo bash install.sh --offline <新 tar> --upgrade`（已有安装必须显式 `--upgrade` 才会换标签；`oa-agentctl upgrade` 在离线安装脚本里以 `OA_OFFLINE=1` 运行，不拉取、不刷新管理文件）。

### D.3.4 尚未验证/局限（务必知悉）

- apt/dnf/yum/zypper/apk 的软件包回退链只在假的包管理器 shim 下测过；各发行版的包名和是否带 Compose v2 不同，回退失败时脚本会明确报错并指向官方文档。
- `daemon.json` 的“解析并合并”依赖 python3/jq/perl(JSON::PP)；在没有任何一个的主机上，非空 `daemon.json` 一律不修改（只打印手动步骤）。
- 预检阈值（内核 3.10/4.0、Docker 20.10、磁盘 5/20 GB、内存 2/4 GB）是我们保守选定的，不是对每个发行版的实测结论。
- 阿里云 ECS 探测只访问链路本地元数据地址 `100.100.100.200`（1 秒超时），且只在镜像仓库是阿里云地址时才探测。
