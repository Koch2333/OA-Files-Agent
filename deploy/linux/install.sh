#!/usr/bin/env bash
# OA 文件智能归档 - Linux 一键安装脚本 / OA Agent one-click Linux installer (Docker based).
#
# The production image registry (Alibaba Cloud ACR, namespace koch2333) is HARD-CODED below (DEFAULT_REGISTRY), so a plain run
# pulls the pre-built images from it; the installer bundle itself is the <registry>/oa-agent-deploy image:
#       docker run --rm crpi-d6fmgwmrnsa2kgp8.cn-shanghai.personal.cr.aliyuncs.com/koch2333/oa-agent-deploy:latest | sudo bash [-s -- options]
#   No Docker yet (installs it first, then pulls the images):
#       curl -fsSL https://raw.githubusercontent.com/Koch2333/OA-Files-Agent/main/deploy/linux/install.sh | sudo bash -s -- --install-docker [--mirror cn]
#   From a checkout:   sudo bash deploy/linux/install.sh            (images are still pulled; add --build-from-source to build them locally)
#   Another registry:  --registry HOST[/NS]   Private registries: OA_REGISTRY_USER / OA_REGISTRY_PASSWORD (environment only, never stored or logged).
#   Pin a version for production: --image-tag sha-<7-char commit> (or a release tag like 1.2.3).
#
#   Read-only dry run (changes nothing, prints a PASS/WARN/FAIL report and the plan):   sudo bash install.sh --check [options]
#   Host without registry access: run scripts/save-images.sh on a connected machine, then   sudo bash install.sh --offline oa-agent-images-<tag>.tar
#
# HOW IT DECIDES: it first DETECTS (read-only) what the host already has - Docker/Compose, prerequisite tools, existing installations
# (state file /etc/oa-agent/install.json, container labels, .env), orphaned data, port owners, existing database servers - prints a
# component table (PASS / FOUND / MISS / TODO / WARN / FAIL) and installs only what is missing. A fresh install then asks one question at a
# time (defaults from the detection) and ends with a review screen; --config / OA_INSTALL_* / --yes preseed or skip the questions;
# --status and --check are read-only. A healthy installation of the same version only gets its status shown.
#
# Run with --help for all options. The installer never overwrites an existing .env or regenerates secrets.
# Exit codes: 0 ok | 1 error | 2 invalid usage/argument | 3 pre-flight failed (also --check with FAIL lines) | 4 cancelled by the user | 130 interrupted.
#
# TRUNCATION SAFETY: everything executable lives inside functions; the only statement that runs anything is the LAST line, which
# calls main with an end-of-script sentinel as its final argument. A download cut at any byte therefore either has a syntax error,
# or never reaches that line, or reaches main without the sentinel and refuses to run: a partial "curl | bash" installs nothing.
set -Eeuo pipefail
umask 077

# ---------------------------------------------------------------------------------------------- defaults
DIR='/opt/oa-agent'
PORT='8080'
DOMAIN=''
EMAIL=''
MIRROR=''            # cn | default (empty = ask / default)
DOCKER_SOURCE=''     # where Docker is installed from: official|aliyun|azure|tencent|tuna|ustc|auto|<https URL> (empty/auto = detected automatically)
SRC_ORDER=''; SRC_AUTO=0   # the sources docker_official_script will try, and whether falling through to the next needs no consent
LANG_SET=0           # 1 = the language was given (--lang / OA_LANG / config) or already asked
VERSION='latest'      # release version, or the image tag in registry mode (--image-tag)
VERSION_SET=0
REPO_URL="${OA_REPO_URL:-}"      # PLACEHOLDER: e.g. https://example.com/PLACEHOLDER/oa-agent
# PRODUCTION DEFAULT (hard-coded on purpose): the images are pulled from this Alibaba Cloud ACR namespace
# (oa-agent-api, oa-agent-worker, and oa-agent-deploy which carries the installer bundle). Nothing here is a secret.
# Overrides: --registry HOST[/NS] or OA_IMAGE_REGISTRY (another registry); --build-from-source / OA_BUILD_FROM_SOURCE=1 (build from
# a source tree instead of pulling). The public endpoint is the default; an ECS in the same region can use the VPC endpoint
# crpi-d6fmgwmrnsa2kgp8-vpc.cn-shanghai.personal.cr.aliyuncs.com/koch2333 via --registry.
DEFAULT_REGISTRY='crpi-d6fmgwmrnsa2kgp8.cn-shanghai.personal.cr.aliyuncs.com/koch2333'
REGISTRY="${OA_IMAGE_REGISTRY:-$DEFAULT_REGISTRY}"
REGISTRY_SET=0       # 1 = --registry was given explicitly
[ -z "${OA_IMAGE_REGISTRY:-}" ] || REGISTRY_SET=1
BUILD_SRC="${OA_BUILD_FROM_SOURCE:-0}"   # 1 = build the images from a source tree (no registry); the env var loses to an explicit --registry
BUILD_FLAG=0                                  # --build-from-source (conflicts with an explicit --registry)
BUNDLE=0            # 1 = source tree is the deploy-image bundle (flat /deploy layout), no sources / no build
DEPLOY_SRC=''        # directory holding install-time deploy files (compose files, oa-agentctl.sh, ...)
ASSUME_YES=0
CHECK=0              # --check / --dry-run: read-only report, changes nothing
REINSTALL=0          # --reinstall: recreate the containers of an existing install (data and .env kept)
OFFLINE_FILE=''      # --offline FILE: docker-save tarball with the api/worker/deploy images (no registry access needed)
DETECT_IP=0          # --detect-public-ip: look up the external address (never done by default)
DOCKER_MIRRORS=()    # --docker-mirror URL (repeatable): registry mirrors offered for /etc/docker/daemon.json (written only after asking)
NO_START=0
DO_UNINSTALL=0
DO_UPGRADE=0
PURGE=0
INSTALL_DOCKER=0
SYSTEMD=ask
EXISTING=0           # 1 = $DIR already holds an installation (.env present)
MENU_DONE=0          # 1 = the interactive repair/upgrade/reinstall/uninstall menu was shown
OPEN_FW=ask
SKIP_VERIFY=0
ARCHIVE_DIR=''; SPOOL_DIR=''; APP_DIR=''; PG_DIR=''
EXTRA_MOUNTS=()
PENDING_CORE=()
PENDING_EXTRA=()
UI=''
PROJECT=''           # compose project name (default oa-agent); also the local image namespace
PROJECT_SET=0
DIR_SET=0            # 1 = --dir / config "dir" was given explicitly (otherwise a found installation may be adopted)
PORT_SET=0
CONFIG_FILE=''       # --config FILE (key=value answers; secrets allowed only here)
PRINT_CONFIG=0       # --print-config: write the answers (never secrets) to stdout and stop
STATUS_ONLY=0        # --status: detection + status of the installation(s), changes nothing
ADOPT=0              # --adopt-data: allow restoring .env from an old backup / reusing orphaned data without asking
ALLOW_DOWNGRADE=0    # --allow-downgrade: install an older tag than the installed one (database migrations are not reversible)
INSTALL_PREREQS=0    # --install-prereqs: allow installing missing prerequisite packages (--install-docker implies it)
DB_MODE=''           # bundled | external ('' = not decided yet: asked, or bundled with --yes)
DB_PROVIDER=''; DB_HOST=''; DB_PORT=''; DB_NAME=''; DB_USER=''; DB_PASSWORD=''; DB_TLS=''; DB_CA_FILE=''; DB_SCHEMA=''
DB_PASSWORD_FILE=''  # config key db_password_file: the password is read from this 0600 file
PROXY_MODE=0         # 1 = OA runs behind a reverse proxy that already exists on this host (no Caddy; the API listens on 127.0.0.1)
SEEDED=' '           # settings given by --config / OA_INSTALL_* variables: not asked again, shown in the review
KEY_FROM_FILE=''     # 64-hex key found in an orphaned secret.key: the new .env reuses it (never a new key over old data)
DEFAULT_DIR='/opt/oa-agent'
ACT_SKIP=(); ACT_DONE=(); ACT_CHG=()     # what the installer skipped / installed / changed (final summary)
DET_PASS=0; DET_FOUND=0; DET_MISS=0; DET_TODO=0
PREREQ_MISSING=(); PREREQ_REC=()
SYSTEMD_OK=0
DK_EVAL=0            # 1 = the Docker version / compose were already evaluated by detection
DK_SVER=''; DK_UP=0; DK_CMP=''; DK_CMP_KIND=none

# Test-only overrides (honoured only when OA_TEST_MODE=1; lets the offline test-suite run unprivileged).
TEST_MODE="${OA_TEST_MODE:-0}"
LOG_FILE='/var/log/oa-agent-install.log'
LOCK_FILE='/var/lock/oa-agent-install.lock'
DAEMON_JSON='/etc/docker/daemon.json'
BIN_DIR='/usr/local/bin'
UNIT_DIR='/etc/systemd/system'
ETC_MARKER='/etc/oa-agent/install_dir'   # read by the Go oa-agentctl when neither --dir nor OA_AGENT_DIR is given
OS_RELEASE='/etc/os-release'
MEMINFO='/proc/meminfo'
TTY_DEV='/dev/tty'
STATE_FILE='/etc/oa-agent/install.json'     # one JSON line per installation made by this installer (dir, project, tag, registry, DB mode, ports)
SYSTEMD_RUN_DIR='/run/systemd/system'      # exists when systemd is PID 1
# Is a docker CLI installed? (Test mode with OA_TEST_DOCKER_SHIM: "installed" means that shim file exists, so a docker on
# the host running the offline tests, e.g. a CI runner's, cannot leak into the "docker is missing" scenarios.)
have_docker() {
    if [ "$TEST_MODE" = 1 ] && [ -n "${OA_TEST_DOCKER_SHIM:-}" ]; then [ -x "$OA_TEST_DOCKER_SHIM" ]; else command -v docker >/dev/null 2>&1; fi
}
# have_cmd NAME: is the tool installed? (Test mode: OA_TEST_MISSING_CMDS="curl flock" hides tools until the fake package manager
# "installs" the package that provides them, so the missing-prerequisite scenarios do not depend on the machine running the tests.)
have_cmd() {
    if [ "$TEST_MODE" = 1 ] && [ -n "${OA_TEST_MISSING_CMDS:-}" ]; then
        case " $OA_TEST_MISSING_CMDS " in
            *" $1 "*) if [ -n "${OA_TEST_PKG_STATE:-}" ] && grep -qx -- "$(pkg_for_cmd "$1")" "$OA_TEST_PKG_STATE" 2>/dev/null; then :; else return 1; fi ;;
        esac
    fi
    command -v "$1" >/dev/null 2>&1
}
if [ "$TEST_MODE" = 1 ]; then
    LOG_FILE="${OA_INSTALL_LOG:-$LOG_FILE}"; BIN_DIR="${OA_INSTALL_BIN_DIR:-$BIN_DIR}"; UNIT_DIR="${OA_INSTALL_UNIT_DIR:-$UNIT_DIR}"
    ETC_MARKER="${OA_INSTALL_ETC_MARKER:-$ETC_MARKER}"
    LOCK_FILE="${OA_INSTALL_LOCK:-$LOCK_FILE}"; DAEMON_JSON="${OA_DAEMON_JSON:-$DAEMON_JSON}"
    OS_RELEASE="${OA_OS_RELEASE:-$OS_RELEASE}"; MEMINFO="${OA_MEMINFO:-$MEMINFO}"; TTY_DEV="${OA_INSTALL_TTY:-$TTY_DEV}"
    STATE_FILE="${OA_INSTALL_STATE:-$STATE_FILE}"; DEFAULT_DIR="${OA_INSTALL_DEFAULT_DIR:-$DEFAULT_DIR}"; SYSTEMD_RUN_DIR="${OA_SYSTEMD_RUN_DIR:-$SYSTEMD_RUN_DIR}"
fi

TMP=''
CTL_BINARY=0
PF_PASS=0; PF_WARN=0; PF_FAIL=0; PF_NOTES=()   # pre-flight counters; the warnings are repeated in the final summary
RETRY_BASE="${OA_RETRY_BASE:-5}"               # back-off base in seconds for network retries (5, 15, 45, ...)
EOS_SENTINEL='__OA_INSTALL_END_OF_SCRIPT__'
LOG_OK=0
STARTED_AT="$(date +%s)"

# ---------------------------------------------------------------------------------------------- UI
UI=en   # replaced by init_lang (locale, OA_LANG, --lang, the language question) before anything is printed
if [ -t 1 ]; then C_R=$'\033[31m'; C_G=$'\033[32m'; C_Y=$'\033[33m'; C_B=$'\033[1m'; C_0=$'\033[0m'; else C_R=''; C_G=''; C_Y=''; C_B=''; C_0=''; fi
m() { if [ "$UI" = zh ]; then printf '%s' "$1"; else printf '%s' "$2"; fi; }
logf() { if [ "$LOG_OK" = 1 ]; then printf '%s %s\n' "$(date '+%F %T')" "$*" >>"$LOG_FILE" 2>/dev/null || true; fi; }
say() { local s; s="$(m "$1" "$2")"; printf '%s\n' "$s"; logf "$s"; }
step() { local s; s="$(m "$1" "$2")"; printf '\n%s==>%s %s%s%s\n' "$C_B" "$C_0" "$C_B" "$s" "$C_0"; logf "== $s"; }
ok() { local s; s="$(m "$1" "$2")"; printf '%s[ OK ]%s %s\n' "$C_G" "$C_0" "$s"; logf "OK $s"; }
warn() { local s; s="$(m "$1" "$2")"; printf '%s[WARN]%s %s\n' "$C_Y" "$C_0" "$s" >&2; logf "WARN $s"; }
die_rc() { local rc="$1" s; shift; s="$(m "$1" "$2")"; printf '%s[FAIL]%s %s\n' "$C_R" "$C_0" "$s" >&2; logf "FAIL $s"; exit "$rc"; }
die() { die_rc 1 "$@"; }
die_usage() { die_rc 2 "$@"; }       # exit 2: invalid option or value
die_preflight() { die_rc 3 "$@"; }   # exit 3: the pre-flight report contains FAIL lines
die_cancel() { die_rc 4 "$@"; }      # exit 4: the user declined / cancelled
# pf_pass / pf_warn / pf_fail: one line of the pre-flight report. Warnings are remembered and repeated in the final summary.
pf_pass() { local s; s="$(m "$1" "$2")"; PF_PASS=$((PF_PASS + 1)); DET_PASS=$((DET_PASS + 1)); printf '%s[PASS]%s %s\n' "$C_G" "$C_0" "$s"; logf "PASS $s"; }
pf_warn() { local s; s="$(m "$1" "$2")"; PF_WARN=$((PF_WARN + 1)); PF_NOTES+=("$s"); printf '%s[WARN]%s %s\n' "$C_Y" "$C_0" "$s" >&2; logf "WARN $s"; }
pf_fail() { local s; s="$(m "$1" "$2")"; PF_FAIL=$((PF_FAIL + 1)); printf '%s[FAIL]%s %s\n' "$C_R" "$C_0" "$s" >&2; logf "FAIL $s"; }
pf_info() { local s; s="$(m "$1" "$2")"; printf '%s[INFO]%s %s\n' "$C_B" "$C_0" "$s"; logf "INFO $s"; }
# Detection rows (read-only discovery). FOUND: something already there, reused or left alone; MISS: absent, the installer will add
# it (after consent); TODO: needs a decision or action before/while installing. The counters feed the detection summary line.
pf_found() { local s; s="$(m "$1" "$2")"; DET_FOUND=$((DET_FOUND + 1)); printf '%s[FOUND]%s %s\n' "$C_G" "$C_0" "$s"; logf "FOUND $s"; }
pf_missing() { local s; s="$(m "$1" "$2")"; DET_MISS=$((DET_MISS + 1)); printf '%s[MISS]%s %s\n' "$C_Y" "$C_0" "$s"; logf "MISS $s"; }
pf_todo() { local s; s="$(m "$1" "$2")"; DET_TODO=$((DET_TODO + 1)); printf '%s[TODO]%s %s\n' "$C_Y" "$C_0" "$s"; logf "TODO $s"; }
# act_skip / act_done / act_chg: remembered for the final summary ("skipped because already present / installed / changed")
act_skip() { ACT_SKIP+=("$(m "$1" "$2")"); }
act_done() { ACT_DONE+=("$(m "$1" "$2")"); }
act_chg() { ACT_CHG+=("$(m "$1" "$2")"); }

banner() {
    printf '%s\n' "$C_B"
    cat <<'EOF'
   ___    _       _                    _
  / _ \  / \     / \   __ _  ___ _ __ | |_
 | | | |/ _ \   / _ \ / _` |/ _ \ '_ \| __|
 | |_| / ___ \ / ___ \ (_| |  __/ | | | |_
  \___/_/   \_/_/   \_\__, |\___|_| |_|\__|
                      |___/
EOF
    printf '%s\n' "$C_0"
    say "        企业 OA 文件智能归档智能体 - Linux 一键安装" "        OA file-archiving agent - Linux one-click installer"
}

usage() {
    if [ "$UI" = zh ]; then
        cat <<'EOF'
用法: install.sh [选项]

  --dir <目录>        安装目录（默认 /opt/oa-agent）
  --port <端口>       HTTP 访问端口（默认 8080；使用域名时仅监听 127.0.0.1）
  --domain <域名>     启用 HTTPS（Caddy 自动证书，需要 80/443 对公网开放，域名已解析到本机）
  --email <邮箱>      证书到期通知邮箱（可选）
  --mirror cn|default 中国大陆镜像：构建使用 goproxy.cn / npmmirror / 清华 PyPI；同时把 Docker 安装源视为「中国大陆」（跳过国家查询，按速度试可达镜像）。要指定 Docker 安装源请用 --docker-source
  --docker-source auto|official|aliyun|azure|tencent|tuna|ustc|<https 地址>
                      Docker 安装源（缺少 Docker 时）。默认 auto，与 1Panel 同思路：先用 ipinfo.io/country 查国家（第三方服务；本地信号兜底：阿里云 ECS 地域、时区、download.docker.com 是否可达），中国大陆则按速度依次试可达镜像（阿里云、Azure 中国、腾讯云、清华、中科大），最后官方；其他地区用官方。始终打印检测结果与将使用的源，不会静默。指定具体源则只用它（失败后交互时才询问是否试其他源）。命令与来源：get.docker.com 脚本（第三方）+ --mirror Aliyun|AzureChinaCloud 或 DOWNLOAD_URL=<仓库根>
  --no-geo-lookup     不请求 ipinfo.io（环境变量 OA_INSTALL_GEO=off），只用本地信号判断是否在中国大陆
  --version <版本>    发行版本（默认 latest）
  --repo-url <URL>    发行包下载地址前缀（占位，环境变量 OA_REPO_URL）；从检出的仓库运行时不需要
  --registry <主机[/命名空间]>  换用另一个镜像仓库（OA_IMAGE_REGISTRY）。默认已内置生产仓库（阿里云 ACR koch2333 命名空间），无需指定：从 <仓库>/oa-agent-api|worker:<标签> 拉取镜像，安装包取自 <仓库>/oa-agent-deploy:<标签>，不在本机构建
  --build-from-source 不拉取镜像，改为用源码目录在本机构建（OA_BUILD_FROM_SOURCE=1；需要检出的仓库或 --repo-url 发行包）
  --image-tag <标签>  镜像标签（默认 latest；registry 模式下 --version 同义；可用 sha-<7位提交> 固定版本）
                      私有仓库登录：环境变量 OA_REGISTRY_USER / OA_REGISTRY_PASSWORD（只经标准输入传给 docker login，不写入 .env/日志）
  --project <名称>    Docker Compose 项目名（默认 oa-agent；同一台机器装多套时必须不同），也是本地镜像名前缀
  --archive-dir <路径>  归档根目录（宿主机绝对路径，可为挂载盘/NAS；默认 <安装目录>/data/archive）
  --spool-dir <路径>    导入暂存目录
  --data-dir <路径>     应用数据目录（含 secret.key）
  --pg-dir <路径>       PostgreSQL 数据目录
  --extra-mount 名称=/宿主机/路径[:ro|:rw]   额外本地存储挂载到 /mnt/storage/<名称>（可重复）
  --yes, -y           非交互模式，全部使用默认值/参数
  --install-docker    缺少 Docker 时允许自动安装（--yes 模式下必须显式给出）。安装过程每 15 秒（OA_HEARTBEAT）打印一行进度（耗时、当前包管理器进程、apt 下载目录/dpkg 日志是否在动）；超过 OA_DOCKER_TIMEOUT 秒（默认 1200）只停止本脚本启动的进程组并打印诊断；安装前若别的 apt/dpkg/unattended-upgrades 占着锁会等待（OA_LOCK_WAIT 秒，默认 300）；容器/WSL 且没有 systemd 时先警告
  --systemd           安装开机自启 systemd 单元（默认询问；--yes 下不安装）
  --open-firewall     在检测到 ufw/firewalld 时放行端口（默认询问；--yes 下不修改）
  --skip-verify       下载发行包时不强制校验和（不推荐）
  --no-start          只配置，不构建/启动
  --check, --dry-run  只读检查：打印环境检查报告（PASS/WARN/FAIL）与将要执行的操作，不做任何更改（不需要 root；有 FAIL 时退出码 3）
  --offline <文件.tar>  离线安装：用 docker load 载入 scripts/save-images.sh 生成的镜像包（api/worker/deploy，需同目录的 <文件>.sha256 校验），不访问镜像仓库
  --docker-mirror <URL>  把 Docker Hub 镜像加速地址写入 /etc/docker/daemon.json（可重复；只在交互确认后才修改，先备份、安全合并，重启 Docker 需再次确认；--yes 下只打印手动步骤）
  --detect-public-ip  结束时探测外网地址（默认从不访问第三方服务；先试云厂商元数据，再试 OA_PUBLIC_IP_URL，默认 https://api.ipify.org）
  --reinstall         对已有安装重建容器（保留 .env 与全部数据，重新拉取镜像并启动）
  --status            只读：检测并显示已有安装的状态与访问信息，不做任何更改（不需要 root）
  --config <文件>     从 key=value 文件读取答案（权限必须是 600；唯一可以放密码的地方：db_password=…；键见部署指南）。优先级：命令行 > OA_INSTALL_<键> 环境变量 > 配置文件 > 向导
  --print-config      把本次的答案（不含任何密钥）按 --config 格式写到标准输出后退出，供第二台服务器复用（不做任何更改，也不检查数据库连接）
  --db bundled|postgres|mysql|mssql   数据库：内置 PostgreSQL（默认）或已有服务器；其余 --db-host/--db-port/--db-name/--db-user/--db-tls disable|require|verify-ca|verify-full/--db-ca-file/--db-schema；密码只能来自 --config（db_password / db_password_file=<文件>）或交互输入，绝不接受命令行/环境变量
  --behind-proxy      OA 放在本机已有的反向代理（nginx/Caddy/1Panel 等）后面：需要 --domain，不启用 Caddy，API 只监听 127.0.0.1，结束时打印代理配置片段
  --install-prereqs   允许安装缺少的前置软件包（curl/tar/openssl/flock/ss…，只装缺少的；--install-docker 隐含本项）
  --adopt-data        发现无 .env 的旧数据时，允许从其备份恢复 .env 并沿用（默认询问；--yes 下必须显式给出）
  --allow-downgrade   允许安装比已安装版本更旧的标签（数据库迁移不可回退，默认拒绝）
  --upgrade           升级现有安装（刷新源码与脚本，备份后重建并重启）
  --uninstall         卸载容器（数据保留）；加 --purge 同时删除数据
  --purge             与 --uninstall 联用
  --lang zh|en        界面语言。不指定时：交互运行先问一次（默认来自系统区域设置 LC_ALL/LANGUAGE/LANG、/etc/default/locale、/etc/locale.conf），无人值守按区域设置并打印一行说明；选择会写入新安装的 .env（OA_LANG）
  -h, --help          显示本帮助

工作方式：先【检测】本机已有什么（Docker/Compose、前置软件、已有安装、旧数据、端口占用者、已有数据库服务、防火墙/SELinux/时间同步…）并打印组件表，再按检测结果跳过已满足的步骤、只补缺少的；全新安装时用【向导】一次问一个问题（默认值来自检测），最后给出复核页（可逐项修改）；外部数据库会在安装任何东西之前先检查，建表需单独确认。对已有且健康的同版本安装，重新运行只显示状态（快速、只读为主）。
退出码: 0 成功 | 1 失败 | 2 参数错误 | 3 环境检查未通过 | 4 用户取消 | 130 被中断。已有安装且交互运行时，会询问：修复/升级/重装(保留数据)/卸载/取消/无需操作。
EOF
    else
        cat <<'EOF'
Usage: install.sh [options]

  --dir <path>        install directory (default /opt/oa-agent)
  --port <port>       HTTP port (default 8080; with a domain the API only listens on 127.0.0.1)
  --domain <fqdn>     enable HTTPS (Caddy automatic certificates; 80/443 must be reachable, DNS must point here)
  --email <address>   certificate expiry notices (optional)
  --mirror cn|default China mirrors: goproxy.cn / npmmirror / Tsinghua PyPI for builds; also treats the Docker installation source as "mainland China" (skips the country lookup, tries the reachable mirrors by speed). To pick the Docker installation source use --docker-source
  --docker-source auto|official|aliyun|azure|tencent|tuna|ustc|<https URL>
                      where Docker comes from when it is missing. Default auto, the same idea as 1Panel: the country is looked up with ipinfo.io/country (a third-party service; local signals back it up: Alibaba Cloud ECS region, time zone, whether download.docker.com answers); in mainland China the reachable mirrors are tried by speed (Alibaba Cloud, Azure China, Tencent Cloud, Tsinghua, USTC) with the official source last; elsewhere the official source. What was detected and what will be used is always printed, nothing is silent. A named source is used alone (after a failure only an interactive run is asked whether to try the others). Command and origin: the get.docker.com script (third-party) + --mirror Aliyun|AzureChinaCloud or DOWNLOAD_URL=<repository root>
  --no-geo-lookup     do not contact ipinfo.io (env OA_INSTALL_GEO=off); only local signals decide whether this is mainland China
  --version <ver>     release version (default latest)
  --repo-url <URL>    release download base (PLACEHOLDER, env OA_REPO_URL); not needed from a checkout
  --registry <host[/ns]>  use another image registry (OA_IMAGE_REGISTRY). The production registry (Alibaba Cloud ACR, namespace koch2333) is built in as the default, so this is normally not needed: images come from <registry>/oa-agent-api|worker:<tag>, the installer bundle from <registry>/oa-agent-deploy:<tag>; nothing is built locally
  --build-from-source build the images locally from a source tree instead of pulling them (OA_BUILD_FROM_SOURCE=1; needs a checkout or a --repo-url release)
  --image-tag <tag>   image tag (default latest; --version is a synonym in registry mode; use sha-<7-char commit> to pin)
                      private registries: env OA_REGISTRY_USER / OA_REGISTRY_PASSWORD (only passed to docker login via stdin, never stored in .env or logs)
  --project <name>    Docker Compose project name (default oa-agent; must differ per install on one host), also the local image prefix
  --archive-dir <path>  archive root (absolute host path, e.g. a mounted disk/NAS; default <dir>/data/archive)
  --spool-dir <path>    import spool directory
  --data-dir <path>     application data directory (holds secret.key)
  --pg-dir <path>       PostgreSQL data directory
  --extra-mount name=/host/path[:ro|:rw]   extra local storage mounted at /mnt/storage/<name> (repeatable)
  --yes, -y           non-interactive: use defaults / flags for everything
  --install-docker    allow installing Docker when missing (required explicitly with --yes). The installation prints a progress line every 15 s (OA_HEARTBEAT: elapsed time, the active package-manager process, whether the apt download directory / dpkg log move); after OA_DOCKER_TIMEOUT seconds (default 1200) only the process group this script started is stopped and diagnostics are printed; another apt/dpkg/unattended-upgrades holding the lock is waited for first (OA_LOCK_WAIT seconds, default 300); a container/WSL without systemd is warned about first
  --systemd           install a start-on-boot systemd unit (asked by default; not installed with --yes)
  --open-firewall     open the port when ufw/firewalld is detected (asked by default; untouched with --yes)
  --skip-verify       do not insist on a checksum for downloaded releases (not recommended)
  --no-start          configure only; do not build/start
  --check, --dry-run  read-only check: print the pre-flight report (PASS/WARN/FAIL) and what would be done; changes nothing (no root needed; exit 3 when there are FAIL lines)
  --offline <file.tar>  offline install: docker load the image bundle made by scripts/save-images.sh (api/worker/deploy; needs <file>.sha256 next to it), no registry access
  --docker-mirror <URL>  add a Docker Hub registry mirror to /etc/docker/daemon.json (repeatable; modified only after interactive consent: backed up, safely merged, the Docker restart needs a second consent; with --yes only the manual steps are printed)
  --detect-public-ip  look up the external address at the end (third-party services are never contacted by default; cloud metadata first, then OA_PUBLIC_IP_URL, default https://api.ipify.org)
  --reinstall         recreate the containers of an existing install (.env and all data kept; images are pulled again)
  --status            read-only: detect and show the state and access information of the existing installation(s); changes nothing (no root needed)
  --config <file>     read the answers from a key=value file (mode must be 600; the ONLY place for a password: db_password=...; keys are listed in the deployment guide). Precedence: command line > OA_INSTALL_<KEY> environment > config file > wizard
  --print-config      write this run's answers (never any secret) to standard output in --config format and exit, so a second server can reuse them (changes nothing, does not check the database connection)
  --db bundled|postgres|mysql|mssql   database: the bundled PostgreSQL (default) or an existing server; plus --db-host/--db-port/--db-name/--db-user/--db-tls disable|require|verify-ca|verify-full/--db-ca-file/--db-schema; the password comes only from --config (db_password / db_password_file=<file>) or the hidden prompt, never from the command line or the environment
  --behind-proxy      run OA behind a reverse proxy that already exists on this host (nginx/Caddy/1Panel ...): needs --domain, no Caddy, the API listens on 127.0.0.1 only, a proxy snippet is printed at the end
  --install-prereqs   allow installing missing prerequisite packages (curl/tar/openssl/flock/ss ..., only the missing ones; --install-docker implies it)
  --adopt-data        when old data without .env is found, allow restoring the .env from its backup and reusing it (asked interactively; required explicitly with --yes)
  --allow-downgrade   allow installing an older tag than the installed one (database migrations are not reversible; refused by default)
  --upgrade           upgrade an existing install (refresh source and scripts, back up, rebuild, restart)
  --uninstall         remove containers (data kept); with --purge also delete the data
  --purge             combine with --uninstall
  --lang zh|en        UI language. When not given: an interactive run asks once first (default from the locale: LC_ALL/LANGUAGE/LANG, /etc/default/locale, /etc/locale.conf), an unattended run follows the locale and prints one line; the choice is stored in the .env of a new install (OA_LANG)
  -h, --help          this help

How it works: it first DETECTS what this server already has (Docker/Compose, prerequisite tools, existing installations, old data, port owners, existing database servers, firewall/SELinux/time sync ...) and prints a component table, then skips whatever is satisfied and adds only what is missing; a fresh install uses a WIZARD that asks one question at a time (defaults come from the detection) and ends with a review screen where every setting can be edited; an existing database is checked before anything is installed and creating its tables needs a separate confirmation. Re-running it on a healthy installation of the same version only shows the status (fast, read-mostly).
Exit codes: 0 ok | 1 error | 2 invalid argument | 3 pre-flight failed | 4 cancelled | 130 interrupted. Interactive runs against an existing install ask: repair / upgrade / reinstall (keeps data) / uninstall / cancel / nothing to do.
EOF
    fi
}

# ---------------------------------------------------------------------------------------------- error handling
cleanup() {
    if [ -n "$TMP" ] && [ -d "$TMP" ]; then rm -rf -- "$TMP"; fi
}
on_err() {
    local rc=$? line="$1"
    trap - ERR
    printf '\n%s[FAIL]%s %s (line %s, exit %s)\n' "$C_R" "$C_0" "$(m "安装中断。" "Installation aborted.")" "$line" "$rc" >&2
    if [ "$LOG_OK" = 1 ]; then printf '%s %s\n' "$(m "日志（不含密钥）:" "Log (no secrets):")" "$LOG_FILE" >&2; fi
    printf '%s\n' "$(m "可安全地重新运行本脚本：已生成的密钥不会被改变。" "It is safe to re-run this script: already generated secrets are never changed.")" >&2
    exit "$rc"
}
on_int() {
    trap - INT TERM ERR
    if [ -n "${DOCKER_CHILD:-}" ]; then printf '\n%s\n' "$(m "Docker 安装进程（pid $DOCKER_CHILD）仍在后台继续，请用 ps 查看；它结束前不要重跑本脚本。" "The Docker installation process (pid $DOCKER_CHILD) keeps running in the background, check it with ps; do not re-run this script until it has finished.")" >&2; fi
    printf '\n%s[FAIL]%s %s\n' "$C_R" "$C_0" "$(m "已中断。可安全地重新运行本脚本（密钥不会被改变；Docker 的镜像下载会续传已完成的层）。" "Interrupted. It is safe to re-run this script (secrets are never changed; Docker resumes the layers it already pulled).")" >&2
    exit 130
}
trap 'on_err $LINENO' ERR
trap on_int INT TERM
trap cleanup EXIT

# lock_install: one installer at a time per host (flock when available); the lock is released when the process exits
lock_install() {
    have_cmd flock || return 0
    mkdir -p "$(dirname -- "$LOCK_FILE")" 2>/dev/null || return 0
    { exec 8>>"$LOCK_FILE"; } 2>/dev/null || return 0
    flock -n 8 || die "已有另一个 install.sh 正在运行（$LOCK_FILE）。请等它结束后再运行。" "Another install.sh is already running ($LOCK_FILE). Wait for it to finish and run again."
}

# ---------------------------------------------------------------------------------------------- args
need_val() { [ $# -ge 2 ] && [ -n "$2" ] || die_usage "选项 $1 需要一个值" "option $1 needs a value"; }
valid_mirror_url() { [[ "$1" =~ ^https?://[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?(:[0-9]{1,5})?(/[A-Za-z0-9._~/-]*)?$ ]]; }
parse_args() {
    while [ $# -gt 0 ]; do
        case "$1" in
            --dir) need_val "$@"; DIR="$2"; DIR_SET=1; shift 2 ;;
            --port) need_val "$@"; PORT="$2"; PORT_SET=1; shift 2 ;;
            --domain) need_val "$@"; DOMAIN="$2"; shift 2 ;;
            --email) need_val "$@"; EMAIL="$2"; shift 2 ;;
            --mirror) need_val "$@"; case "$2" in cn | default) ;; *) die_usage "--mirror 只能是 cn 或 default（要指定 Docker 安装源请用 --docker-source）: $2" "--mirror must be cn or default (use --docker-source to pick the Docker installation source): $2" ;; esac; MIRROR="$2"; shift 2 ;;
            --version | --image-tag) need_val "$@"; VERSION="$2"; VERSION_SET=1; shift 2 ;;
            --repo-url) need_val "$@"; REPO_URL="$2"; shift 2 ;;
            --registry) need_val "$@"; REGISTRY="$2"; REGISTRY_SET=1; shift 2 ;;
            --build-from-source | --source) BUILD_FLAG=1; shift ;;
            --project) need_val "$@"; PROJECT="$2"; PROJECT_SET=1; shift 2 ;;
            --lang) need_val "$@"; case "$2" in zh* | ZH* | cn | 中文) UI=zh ;; en* | EN*) UI=en ;; *) die_usage "--lang 只能是 zh 或 en: $2" "--lang must be zh or en: $2" ;; esac; LANG_SET=1; shift 2 ;;
            --no-geo-lookup) GEO=off; shift ;;
            --docker-source) need_val "$@"; valid_docker_source "$2" || die_usage "--docker-source 只能是 official、aliyun、azure、tencent、tuna、ustc、auto 或 https:// 地址: $2" "--docker-source must be official, aliyun, azure, tencent, tuna, ustc, auto or an https:// URL: $2"; DOCKER_SOURCE="${2%/}"; shift 2 ;;
            --archive-dir) need_val "$@"; ARCHIVE_DIR="$2"; shift 2 ;;
            --spool-dir) need_val "$@"; SPOOL_DIR="$2"; shift 2 ;;
            --data-dir) need_val "$@"; APP_DIR="$2"; shift 2 ;;
            --pg-dir) need_val "$@"; PG_DIR="$2"; shift 2 ;;
            --extra-mount) need_val "$@"; EXTRA_MOUNTS+=("$2"); shift 2 ;;
            --offline) need_val "$@"; OFFLINE_FILE="$2"; shift 2 ;;
            --docker-mirror)
                need_val "$@"
                valid_mirror_url "$2" || die_usage "--docker-mirror 需要 http(s):// 地址: $2" "--docker-mirror needs an http(s):// URL: $2"
                DOCKER_MIRRORS+=("${2%/}"); shift 2 ;;
            --detect-public-ip) DETECT_IP=1; shift ;;
            --check | --dry-run) CHECK=1; shift ;;
            --reinstall) REINSTALL=1; shift ;;
            --config) need_val "$@"; CONFIG_FILE="$2"; shift 2 ;;
            --print-config) PRINT_CONFIG=1; shift ;;
            --status) STATUS_ONLY=1; shift ;;
            --adopt-data) ADOPT=1; shift ;;
            --allow-downgrade) ALLOW_DOWNGRADE=1; shift ;;
            --install-prereqs) INSTALL_PREREQS=1; shift ;;
            --behind-proxy) PROXY_MODE=1; shift ;;
            --db) need_val "$@"
                case "$2" in
                    bundled) DB_MODE=bundled ;;
                    postgres | mysql | mssql) DB_MODE=external; DB_PROVIDER="$2" ;;
                    *) die_usage "--db 只能是 bundled、postgres、mysql 或 mssql: $2" "--db must be bundled, postgres, mysql or mssql: $2" ;;
                esac
                shift 2 ;;
            --db-host) need_val "$@"; DB_HOST="$2"; shift 2 ;;
            --db-port) need_val "$@"; DB_PORT="$2"; shift 2 ;;
            --db-name) need_val "$@"; DB_NAME="$2"; shift 2 ;;
            --db-user) need_val "$@"; DB_USER="$2"; shift 2 ;;
            --db-tls) need_val "$@"; DB_TLS="$2"; shift 2 ;;
            --db-ca-file) need_val "$@"; DB_CA_FILE="$2"; shift 2 ;;
            --db-schema) need_val "$@"; DB_SCHEMA="$2"; shift 2 ;;
            --db-password-file) need_val "$@"; DB_PASSWORD_FILE="$2"; shift 2 ;;
            --yes | -y) ASSUME_YES=1; shift ;;
            --install-docker) INSTALL_DOCKER=1; shift ;;
            --systemd) SYSTEMD=yes; shift ;;
            --open-firewall) OPEN_FW=yes; shift ;;
            --skip-verify) SKIP_VERIFY=1; shift ;;
            --no-start) NO_START=1; shift ;;
            --upgrade) DO_UPGRADE=1; shift ;;
            --uninstall) DO_UNINSTALL=1; shift ;;
            --purge) PURGE=1; shift ;;
            -h | --help) usage; exit 0 ;;
            *) usage >&2; die_usage "未知选项: $1" "unknown option: $1" ;;
        esac
    done
    if [ -n "${OA_DOCKER_MIRRORS:-}" ] && [ "${#DOCKER_MIRRORS[@]}" -eq 0 ]; then
        local u; for u in $OA_DOCKER_MIRRORS; do
            valid_mirror_url "$u" || die_usage "OA_DOCKER_MIRRORS 含非法地址: $u" "OA_DOCKER_MIRRORS contains an invalid URL: $u"
            DOCKER_MIRRORS+=("${u%/}")
        done
    fi
    # Image source: the built-in registry by default; --build-from-source switches to a local build; an explicit --registry wins over
    # the OA_BUILD_FROM_SOURCE environment variable but conflicts with the flag itself.
    if [ "$BUILD_FLAG" = 1 ] && [ "$REGISTRY_SET" = 1 ]; then
        die_usage "--registry 与 --build-from-source 不能同时使用。" "--registry and --build-from-source cannot be combined."
    fi
    if { [ "$BUILD_FLAG" = 1 ] || [ "$BUILD_SRC" = 1 ]; } && [ "$REGISTRY_SET" = 0 ]; then REGISTRY=''; fi
    if [ -n "$OFFLINE_FILE" ]; then
        { [ "$BUILD_FLAG" = 0 ] && [ "$BUILD_SRC" != 1 ]; } || die_usage "--offline 使用预构建镜像，不能与 --build-from-source 同时使用。" "--offline uses pre-built images and cannot be combined with --build-from-source."
        [ -f "$OFFLINE_FILE" ] && [ -r "$OFFLINE_FILE" ] || die_usage "--offline 文件不存在或不可读: $OFFLINE_FILE" "--offline file does not exist or is not readable: $OFFLINE_FILE"
        OFFLINE_FILE="$(cd "$(dirname -- "$OFFLINE_FILE")" && pwd)/$(basename -- "$OFFLINE_FILE")"
    fi
    if [ "$CHECK" = 1 ] && { [ "$DO_UNINSTALL" = 1 ] || [ "$PURGE" = 1 ]; }; then die_usage "--check 不能与 --uninstall/--purge 同时使用。" "--check cannot be combined with --uninstall/--purge."; fi
    if [ "$REINSTALL" = 1 ] && { [ "$DO_UPGRADE" = 1 ] || [ "$DO_UNINSTALL" = 1 ]; }; then die_usage "--reinstall 不能与 --upgrade/--uninstall 同时使用。" "--reinstall cannot be combined with --upgrade/--uninstall."; fi
    if [ "$INSTALL_DOCKER" = 1 ]; then INSTALL_PREREQS=1; fi
}

# ---------------------------------------------------------------------------------------------- validation helpers
valid_port() { [[ "$1" =~ ^[0-9]{1,5}$ ]] && [ "$1" -ge 1 ] && [ "$1" -le 65535 ]; }
valid_domain() { [[ "$1" =~ ^([A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?\.)+[A-Za-z]{2,63}$ ]]; }
valid_email() { [[ "$1" =~ ^[^@[:space:]\|\&\;\"\'\\]+@[^@[:space:]\|\&\;\"\'\\]+\.[^@[:space:]\|\&\;\"\'\\]+$ ]]; }
valid_dir() {
    case "$1" in /*) ;; *) return 1 ;; esac
    [[ "$1" =~ ^/[A-Za-z0-9_./-]+$ ]] || return 1
    case "$1" in *..*) return 1 ;; esac
    case "${1%/}" in '' | /usr | /bin | /sbin | /lib | /lib64 | /etc | /var | /home | /root | /boot | /opt | /mnt | /srv | /tmp | /dev | /proc | /sys | /run) return 1 ;; esac
    return 0
}

# ---------------------------------------------------------------------------------------------- prompts
TTY_OPEN=0
open_tty() { if { exec 9<"$TTY_DEV"; } 2>/dev/null; then TTY_OPEN=1; fi; }
have_tty() { [ "$TTY_OPEN" = 1 ]; }
# ask VAR "中文" "English" default
ask() {
    local var="$1" prompt ans=''
    prompt="$(m "$2" "$3")"
    if [ "$ASSUME_YES" = 1 ] || ! have_tty; then printf -v "$var" '%s' "${!var:-$4}"; return 0; fi
    printf '%s [%s]: ' "$prompt" "${!var:-$4}" >&2
    IFS= read -r ans <&9 || ans=''
    if [ -n "$ans" ]; then printf -v "$var" '%s' "$ans"; else printf -v "$var" '%s' "${!var:-$4}"; fi
}
# ask_valid VAR "中文" "English" default CHECKFN: like ask, but CHECKFN VALUE must succeed. CHECKFN prints the reason
# on failure; interactively the question is asked again, otherwise (flags / --yes) the installer stops.
# var_key VAR: the answers-file key of a question variable (DIR -> dir, VERSION -> image_tag, DB_HOST -> db_host ...)
var_key() { case "$1" in VERSION) echo image_tag ;; *) printf '%s' "$1" | tr '[:upper:]' '[:lower:]' ;; esac; }
ask_valid() {
    local var="$1" zh="$2" en="$3" def="$4" chk="$5" why tries=0 key
    # an answer given by --config / OA_INSTALL_* is not asked again (it is validated, and shown in the review screen)
    key="$(var_key "$var")"
    if seeded "$key" && [ -n "${!var:-}" ]; then
        if why="$("$chk" "${!var}")"; then return 0; fi
        die_usage "$zh: $why" "$en: $why"
    fi
    while :; do
        ask "$var" "$zh" "$en" "$def"
        if why="$("$chk" "${!var}")"; then return 0; fi
        if [ "$ASSUME_YES" = 1 ] || ! have_tty; then die_usage "$zh: $why" "$en: $why"; fi
        printf '%s[!]%s %s\n' "$C_R" "$C_0" "$why" >&2
        printf -v "$var" '%s' "$def"
        tries=$((tries + 1))
        [ "$tries" -lt 5 ] || die_usage "输入无效次数过多，已中止。" "Too many invalid answers, aborting."
    done
}
chk_install_dir() { valid_dir "$1" || { m "须为绝对路径，只能包含字母、数字和 _ . / -（不支持空格/中文），且不能是 / /etc /opt /var 等系统目录本身" "must be an absolute path with only letters, digits and _ . / - (no spaces), and not a system directory such as / /etc /opt /var itself"; return 1; }; }
chk_new_install_dir() {
    chk_install_dir "$1" || return 1
    if [ -f "${1%/}/.env" ]; then m "${1%/} 中已有安装；要修复/升级它，请重新运行 install.sh --dir ${1%/}（或加 --upgrade）" "${1%/} already holds an installation; to repair/upgrade it re-run install.sh --dir ${1%/} (or add --upgrade)"; return 1; fi
}
chk_port() {
    valid_port "$1" || { m "端口须为 1-65535 的数字" "the port must be a number from 1 to 65535"; return 1; }
    # a busy port is only a question for a person at a terminal; unattended runs report it as a pre-flight FAIL (exit 3) instead
    if { [ "$ASSUME_YES" != 1 ] && have_tty; } && port_in_use "$1"; then m "端口 $1 已被占用：$(port_describe "$1")。请换一个（建议 $(next_free_port "$(( $1 + 1 ))")）" "port $1 is already in use by $(port_describe "$1"). Choose another (suggestion: $(next_free_port "$(( $1 + 1 ))"))"; return 1; fi
}
chk_domain() { [ -z "$1" ] || valid_domain "$1" || { m "需要完整域名（如 oa.example.com），不能是 IP；留空表示仅 HTTP" "a full domain name is required (e.g. oa.example.com), not an IP; leave empty for HTTP only"; return 1; }; }
chk_email() { [ -z "$1" ] || valid_email "$1" || { m "邮箱格式不正确" "invalid e-mail address"; return 1; }; }
chk_mirror() { case "$1" in cn | default) ;; *) m "只能输入 cn 或 default" "enter cn or default"; return 1 ;; esac; }
# storage_problem PATH: why PATH cannot be a storage directory (prints the reason, returns 1), or nothing
storage_problem() {
    local p="${1%/}" a
    if ! mount_path_ok "$p"; then
        m "须为绝对路径，只能包含字母、数字和 _ . / -（不支持空格/中文），不能是 / /etc /usr /var /tmp /home 等系统目录（或其中的 /etc /usr /proc /var/lib/docker 之下）" \
            "must be an absolute path with only letters, digits and _ . / - (no spaces), and not a system directory (/ /etc /usr /var /tmp /home ..., nor below /etc /usr /proc /var/lib/docker)"
        return 1
    fi
    case "$DIR/" in "$p"/*) m "不能包含安装目录 $DIR（其中的 .env 存放密钥）" "must not contain the install directory $DIR (its .env holds the secrets)"; return 1 ;; esac
    if [ -e "$p" ] && [ ! -d "$p" ]; then m "$p 已存在但不是目录" "$p exists but is not a directory"; return 1; fi
    a="$p"; while [ ! -e "$a" ] && [ "$a" != / ]; do a="$(dirname -- "$a")"; done
    if [ ! -w "$a" ]; then
        if [ "$a" = "$p" ]; then m "$p 不可写（只读文件系统或权限不足）" "$p is not writable (read-only filesystem or permissions)"
        else m "无法创建 $p：上级目录 $a 不可写（只读文件系统或权限不足）" "cannot create $p: parent $a is not writable (read-only filesystem or permissions)"; fi
        return 1
    fi
}
# space_note PATH: one line with the free space and filesystem of PATH (or its nearest existing parent)
space_note() {
    local a="${1%/}" kb fst
    while [ ! -e "$a" ] && [ "$a" != / ]; do a="$(dirname -- "$a")"; done
    kb="$(df -Pk "$a" 2>/dev/null | awk 'NR==2{print $4}' || echo 0)"; kb="${kb:-0}"
    fst="$(stat -f -c %T "$a" 2>/dev/null || echo unknown)"
    say "  -> $1：可用 $((kb / 1024 / 1024)) GiB（$fst）" "  -> $1: $((kb / 1024 / 1024)) GiB free ($fst)"
    if [ "$kb" -lt 20971520 ]; then warn "  该磁盘剩余不足 20 GiB，归档文件会持续增长。" "  Less than 20 GiB free there; the archive keeps growing."; fi
}
chk_extra() { # chk_extra SPEC (empty = done)
    [ -n "$1" ] || return 0
    local name host
    if [[ "$1" =~ ^([a-z0-9][a-z0-9_-]{0,30})=(/[A-Za-z0-9_./-]+)(:(ro|rw))?$ ]]; then name="${BASH_REMATCH[1]}"; host="${BASH_REMATCH[2]}"
    else m "格式应为 名称=/宿主机/路径[:ro|:rw]，名称为小写字母/数字/-/_，例如 nas1=/mnt/nas:ro" "use name=/host/path[:ro|:rw] with a lowercase name, e.g. nas1=/mnt/nas:ro"; return 1; fi
    mount_path_ok "$host" || { m "路径不合法: $host（不能是系统目录，不能含空格）" "unacceptable path: $host (no system directories, no spaces)"; return 1; }
    [ -d "$host" ] || { m "目录不存在: $host（请先挂载磁盘/NAS）" "directory does not exist: $host (mount the disk/NAS first)"; return 1; }
    local i; for i in "${!EX_NAME[@]}"; do [ "${EX_NAME[$i]}" != "$name" ] || { m "名称重复: $name" "duplicate name: $name"; return 1; }; done
}

# confirm "中文" "English" [default y|n]   -> 0 yes
confirm() {
    local ans='' def="${3:-n}" hint='[y/N]'
    [ "$def" = y ] && hint='[Y/n]'
    if [ "$ASSUME_YES" = 1 ]; then return 1; fi    # --yes never grants consent to side effects by itself
    have_tty || return 1
    printf '%s %s ' "$(m "$1" "$2")" "$hint" >&2
    IFS= read -r ans <&9 || ans=''
    [ -n "$ans" ] || ans="$def"
    case "$ans" in y | Y | yes | YES) return 0 ;; *) return 1 ;; esac
}

# ---------------------------------------------------------------------------------------------- env file helpers
envf() { printf '%s/.env' "$DIR"; }
# env_read FILE KEY: last assignment of KEY (a trailing " # comment" and one pair of quotes removed, like oa-agentctl does)
env_read() {
    local line v
    [ -f "$1" ] || return 0
    line="$(grep -E "^$2=" "$1" | tail -n 1 || true)"
    v="${line#*=}"; v="${v%% #*}"; v="${v%"${v##*[![:space:]]}"}"
    v="${v#\"}"; v="${v%\"}"; v="${v#\'}"; v="${v%\'}"
    printf '%s' "$v"
}
env_get() { env_read "$(envf)" "$1"; }
env_set() {
    local tmp
    tmp="$(mktemp "$DIR/.env.XXXXXX")"
    if [ -f "$(envf)" ]; then grep -v -E "^$1=" "$(envf)" >"$tmp" || true; fi
    printf '%s=%s\n' "$1" "$2" >>"$tmp"
    chmod 600 "$tmp"
    mv -f "$tmp" "$(envf)"
}
gen_hex() { # gen_hex NBYTES
    if have_cmd openssl; then openssl rand -hex "$1"; else head -c "$1" /dev/urandom | od -An -tx1 | tr -d ' \n'; fi
}

# ---------------------------------------------------------------------------------------------- uninstall / upgrade delegation
find_ctl() {
    if [ -x "$BIN_DIR/oa-agentctl.sh" ]; then printf "%s" "$BIN_DIR/oa-agentctl.sh"; return 0; fi
    local here
    here="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd || true)"
    if [ -n "$here" ] && [ -f "$here/oa-agentctl.sh" ]; then printf '%s' "$here/oa-agentctl.sh"; return 0; fi
    return 1
}
ctl_yes() { local s="$ASSUME_YES"; ASSUME_YES=1; ctl "$@"; ASSUME_YES="$s"; }
ctl() {
    local c
    c="$(find_ctl)" || die "找不到 oa-agentctl" "oa-agentctl not found"
    local flags=(--dir "$DIR") off=0
    [ "$ASSUME_YES" = 1 ] && flags+=(--yes)
    if [ -n "$OFFLINE_FILE" ]; then off=1; fi   # offline: oa-agentctl upgrade must not pull from a registry
    OA_OFFLINE="$off" OA_LANG="$UI" OA_UNIT_DIR="$UNIT_DIR" OA_STATE_FILE="$STATE_FILE" OA_CTL_SELF="$BIN_DIR/oa-agentctl" bash "$c" "${flags[@]}" "$@"
}

# ---------------------------------------------------------------------------------------------- preflight
require_root() {
    if [ "$(id -u)" -ne 0 ] && [ "$TEST_MODE" != 1 ]; then
        die "请使用 root 运行（sudo bash install.sh ...）。" "Please run as root (sudo bash install.sh ...)."
    fi
}

setup_log() {
    local d
    d="$(dirname -- "$LOG_FILE")"
    if mkdir -p "$d" 2>/dev/null && : >>"$LOG_FILE" 2>/dev/null; then chmod 600 "$LOG_FILE" 2>/dev/null || true; LOG_OK=1; fi
    [ "$LOG_OK" = 1 ] || LOG_FILE=/dev/null
}

detect_os() {
    OS_ID=''; OS_LIKE=''; OS_PRETTY=''
    if [ -r "$OS_RELEASE" ]; then
        # shellcheck disable=SC1090  # os-release is a runtime path (tests point OS_RELEASE at a fixture)
        OS_ID="$(. "$OS_RELEASE"; printf '%s' "${ID:-}")"
        # shellcheck disable=SC1090
        OS_LIKE="$(. "$OS_RELEASE"; printf '%s' "${ID_LIKE:-}")"
        # shellcheck disable=SC1090
        OS_PRETTY="$(. "$OS_RELEASE"; printf '%s' "${PRETTY_NAME:-$ID}")"
    fi
    [ "$(uname -s)" = Linux ] || die "仅支持 Linux。" "Linux only."
    ARCH="$(uname -m)"
    case "$ARCH" in
        x86_64 | amd64) ARCH=amd64 ;;
        aarch64 | arm64) ARCH=arm64 ;;
        *) die "不支持的 CPU 架构: $ARCH（需要 x86_64 或 aarch64）" "Unsupported CPU architecture: $ARCH (x86_64 or aarch64 required)" ;;
    esac
    pf_pass "系统: ${OS_PRETTY:-unknown} / $ARCH" "OS: ${OS_PRETTY:-unknown} / $ARCH"
}

os_family() {
    case "$OS_ID" in
        ubuntu | debian | raspbian | linuxmint) echo debian ;;
        centos | rocky | almalinux | rhel | ol | amzn) echo rhel ;;
        fedora) echo fedora ;;
        alpine) echo alpine ;;
        opensuse* | sles | sled) echo suse ;;
        *) case " $OS_LIKE " in *" debian "* | *" ubuntu "*) echo debian ;; *" rhel "* | *" centos "*) echo rhel ;; *" fedora "*) echo fedora ;; *" suse "*) echo suse ;; *) echo unknown ;; esac ;;
    esac
}

port_in_use() {
    if have_cmd ss; then ss -ltn 2>/dev/null | awk '{print $4}' | grep -Eq "[:.]$1\$"
    else (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null; fi
}

# ver_ge A B: version A >= version B (dotted numbers)
ver_ge() { [ "$(printf '%s\n%s\n' "$1" "$2" | sort -V | head -n 1)" = "$2" ]; }
num_prefix() { local v="${1%%[!0-9.]*}"; printf '%s' "${v:-0}"; } # "5.15.0-91-generic" -> 5.15.0, "26.1.0-rc1" -> 26.1.0

# time_sync_state: yes | no | unknown (TOTP codes depend on a correct clock)
time_sync_state() {
    local s=''
    if command -v timedatectl >/dev/null 2>&1; then s="$(timedatectl show -p NTPSynchronized --value 2>/dev/null || true)"; fi
    case "$s" in yes | no) printf '%s' "$s"; return 0 ;; esac
    if command -v chronyc >/dev/null 2>&1 && chronyc tracking >/dev/null 2>&1; then printf yes; return 0; fi
    printf unknown
}

# list_ips: one address per line (IPv4 first, IPv6 global in brackets); docker/bridge/virtual interfaces are left out
list_ips() {
    local v4='' v6=''
    if command -v ip >/dev/null 2>&1; then
        v4="$(ip -o -4 addr show scope global 2>/dev/null | awk '$2 !~ /^(docker|br-|veth|virbr|cni|flannel|cali|lo)/ {split($4, a, "/"); print a[1]}' || true)"
        v6="$(ip -o -6 addr show scope global 2>/dev/null | awk '$2 !~ /^(docker|br-|veth|virbr|cni|flannel|cali|lo)/ {split($4, a, "/"); print "[" a[1] "]"}' || true)"
    fi
    if ! command -v ip >/dev/null 2>&1; then v4="$(hostname -I 2>/dev/null | tr ' ' '\n' | grep -E '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$' || true)"; fi
    printf '%s\n%s\n' "$v4" "$v6" | grep -v '^$' || true
}

pf_system() { # checks that need no Docker; prints PASS/WARN/FAIL lines
    local cpus mem_kb mem_mb avail_kb parent kr kv fst st
    # kernel / cgroup
    kr="$(uname -r 2>/dev/null || echo 0)"; kv="$(num_prefix "$kr")"
    if ! ver_ge "$kv" 3.10; then pf_fail "内核 $kr 过旧（Docker 需要 3.10 以上，建议 4.x/5.x/6.x）。" "Kernel $kr is too old (Docker needs 3.10+, 4.x/5.x/6.x recommended)."
    elif ! ver_ge "$kv" 4.0; then pf_warn "内核 $kr 较旧（overlay2 存储驱动建议 4.0 以上），建议升级系统。" "Kernel $kr is old (the overlay2 storage driver prefers 4.0+); consider upgrading the OS."
    else pf_pass "内核 $kr" "Kernel $kr"; fi
    fst="$(stat -f -c %T /sys/fs/cgroup 2>/dev/null || echo unknown)"
    case "$fst" in
        cgroup2fs) pf_pass "cgroup v2（统一层级）" "cgroup v2 (unified hierarchy)" ;;
        tmpfs | cgroupfs) pf_warn "检测到 cgroup v1：容器内存上限（OA_*_MEM）仍可用，但新版 Docker/发行版正逐步淘汰 v1，建议启用 v2。" "cgroup v1 detected: container memory limits (OA_*_MEM) still work, but newer Docker/distros are phasing v1 out; prefer v2." ;;
    esac
    # CPU / memory / disk
    cpus="$(nproc 2>/dev/null || echo 1)"
    mem_kb="$(awk '/^MemTotal:/{print $2}' "$MEMINFO" 2>/dev/null || echo 0)"; mem_kb="${mem_kb:-0}"
    mem_mb=$((mem_kb / 1024))
    parent="$DIR"; while [ ! -d "$parent" ] && [ "$parent" != / ]; do parent="$(dirname -- "$parent")"; done
    avail_kb="$(df -Pk "$parent" 2>/dev/null | awk 'NR==2{print $4}' || echo 0)"; avail_kb="${avail_kb:-0}"
    pf_info "CPU: $cpus 核；内存: ${mem_mb} MB；$parent 可用磁盘: $((avail_kb / 1024 / 1024)) GB" "CPU: $cpus core(s); RAM: ${mem_mb} MB; free disk at $parent: $((avail_kb / 1024 / 1024)) GB"
    if [ "$mem_mb" -lt 2000 ]; then
        pf_warn "内存低于 2 GB：OCR/构建可能失败（建议 ≥4 GB，或加 swap）。" "Less than 2 GB RAM: OCR/builds may fail (4 GB+ recommended, or add swap)."
    elif [ "$mem_mb" -lt 3800 ]; then
        pf_warn "内存低于 4 GB：可运行，但并发 OCR 较慢。" "Less than 4 GB RAM: works, but concurrent OCR will be slow."
    else pf_pass "内存 ${mem_mb} MB" "RAM ${mem_mb} MB"; fi
    if [ "$cpus" -lt 2 ]; then pf_warn "仅 1 个 CPU 核，处理会较慢。" "Only 1 CPU core: processing will be slow."; fi
    # (the disk gate runs in pf_disk with the FINAL install directory, after the questions)
    # clock: TOTP
    st="$(time_sync_state)"
    case "$st" in
        yes) pf_pass "系统时钟已同步" "System clock is synchronized" ;;
        no) pf_warn "系统时钟未同步：管理员 TOTP 动态码依赖准确时间，请启用 chrony 或 systemd-timesyncd（timedatectl set-ntp true）。" "System clock is NOT synchronized: admin TOTP codes depend on correct time; enable chrony or systemd-timesyncd (timedatectl set-ntp true)." ;;
        *) pf_warn "无法确认时间同步状态：请确认 NTP 已启用（TOTP 依赖准确时间）。" "Cannot verify time synchronization: make sure NTP is enabled (TOTP depends on correct time)." ;;
    esac
    # SELinux (text only: this installer never changes SELinux)
    if command -v getenforce >/dev/null 2>&1 && [ "$(getenforce 2>/dev/null || true)" = Enforcing ]; then
        pf_warn "SELinux 处于 Enforcing：若容器写入绑定挂载目录报 Permission denied（属主正确时），请查看 ausearch -m avc -ts recent；处理办法：chcon -Rt container_file_t <目录>，或在 compose 覆盖文件里给卷加 :z 标签，或 setenforce 0（仅限排障）。本脚本不会修改 SELinux。" \
            "SELinux is Enforcing: if containers get Permission denied on bind-mounted directories although the owner is right, run ausearch -m avc -ts recent; fixes: chcon -Rt container_file_t <dir>, or add the :z label to the volume in a compose override, or setenforce 0 (troubleshooting only). This installer never changes SELinux."
    fi
    # addresses (the API binds IPv4 0.0.0.0 unless a domain is used)
    local ips; ips="$(list_ips | tr '\n' ' ')"
    if [ -z "${ips// /}" ]; then
        pf_warn "没有找到非回环地址：访问地址将显示 localhost（仅本机可用），请检查网络配置。" "No non-loopback address found: the access URL will show localhost (this machine only); check the network configuration."
    else
        case " $ips " in
            *" "[0-9]*) pf_pass "本机地址: $ips" "Host addresses: $ips" ;;
            *) pf_warn "只检测到 IPv6 地址（$ips）：服务默认监听 IPv4（0.0.0.0），IPv6-only 主机需要 IPv4 或反向代理。" "Only IPv6 addresses detected ($ips): the service listens on IPv4 (0.0.0.0) by default; an IPv6-only host needs IPv4 or a reverse proxy." ;;
        esac
    fi
}

pf_ports() { # PORT...: a port held by our own running stack is fine
    local p bad=0
    for p in "$@"; do
        if port_in_use "$p"; then
            if [ -f "$(envf)" ] && have_docker && ctl_running_port "$p"; then continue; fi
            bad=1
            pf_fail "端口 $p 已被占用（$(port_describe "$p")）。请换一个端口（--port；建议 $(next_free_port "$((p + 1))")）或停止占用它的程序（ss -ltnp | grep :$p）。$([ "$p" = 80 ] || [ "$p" = 443 ] && [ -n "$(proxy_owner_of_web)" ] && echo "80/443 被反向代理占用：可加 --behind-proxy --domain <域名> 把 OA 放在它后面（会打印配置片段）。")" "Port $p is already in use ($(port_describe "$p")). Choose another port (--port; suggestion: $(next_free_port "$((p + 1))")) or stop the program using it (ss -ltnp | grep :$p).$([ "$p" = 80 ] || [ "$p" = 443 ] && [ -n "$(proxy_owner_of_web)" ] && echo " A reverse proxy holds 80/443: add --behind-proxy --domain <name> to put OA behind it (a configuration snippet is printed).")"
        fi
    done
    [ "$bad" = 1 ] || pf_pass "端口可用: $*" "Ports free: $*"
}
ctl_running_port() { # does an existing OA install already publish this port?
    local cur_port cur_http cur_https
    cur_port="$(env_get OA_PORT)"; cur_http="$(env_get OA_HTTP_PORT)"; cur_https="$(env_get OA_HTTPS_PORT)"
    [ "$1" = "${cur_port:-}" ] || [ "$1" = "${cur_http:-80}" ] || [ "$1" = "${cur_https:-443}" ] || return 1
    docker ps --format '{{.Names}}' 2>/dev/null | grep -q "^${PROJECT:-oa-agent}-" || return 1
}


# aliyun_vpc_host HOST: the VPC endpoint of an Alibaba Cloud ACR address, or return 1
aliyun_vpc_host() {
    case "$1" in
        *-vpc.*) return 1 ;;
        crpi-*.personal.cr.aliyuncs.com) printf '%s' "${1/./-vpc.}" ;;
        *-registry.*.cr.aliyuncs.com) printf '%s' "${1/-registry./-registry-vpc.}" ;;
        *) return 1 ;;
    esac
}
# aliyun_ecs_region: prints the region id when running on an Alibaba Cloud ECS (metadata endpoint, 1 s timeout, link-local only)
aliyun_ecs_region() {
    have_cmd curl || return 1
    curl -fsS -m 2 --connect-timeout 1 http://100.100.100.200/latest/meta-data/region-id 2>/dev/null | head -c 64 | grep -E '^[a-z0-9-]+$'
}
registry_reachable() { # any HTTP answer from <host>/v2/ counts (a private registry answers 401)
    local h code sch schemes=(https)
    h="$(registry_host)"
    case "$h" in localhost | localhost:* | 127.* | *:*) schemes+=(http) ;; esac
    for sch in "${schemes[@]}"; do
        code="$(curl -sS -o /dev/null -m 6 --connect-timeout 4 -w '%{http_code}' "$sch://$h/v2/" 2>/dev/null || true)"
        [[ "$code" =~ ^[1-5][0-9][0-9]$ ]] && return 0
    done
    return 1
}
pf_registry() { # registry mode only: reachability from this shell + the ECS VPC-endpoint hint
    [ -n "$REGISTRY" ] && [ -z "$OFFLINE_FILE" ] || return 0
    have_cmd curl || return 0
    local h vpc region reg_region; h="$(registry_host)"
    if registry_reachable; then pf_pass "镜像仓库可访问: $h" "Image registry reachable: $h"
    else pf_warn "无法从本机访问镜像仓库 $h（若 Docker 守护进程单独配置了代理则可能仍可拉取）；拉取失败时请检查网络/DNS/代理，或改用 --offline。" "Cannot reach the image registry $h from this shell (it may still work if the Docker daemon has its own proxy); if pulls fail check network/DNS/proxy, or use --offline."; fi
    if vpc="$(aliyun_vpc_host "$h")" && region="$(aliyun_ecs_region)"; then
        reg_region="${h#*.}"; reg_region="${reg_region%%.*}"
        if [ "$region" = "$reg_region" ]; then
            pf_info "检测到本机是阿里云 ECS（$region）：同地域可改用 VPC 内网地址拉取（更快、不占公网流量）：--registry $vpc${REGISTRY#"$h"}" \
                "This host is an Alibaba Cloud ECS ($region): in the same region pull over the VPC endpoint (faster, no public traffic): --registry $vpc${REGISTRY#"$h"}"
        fi
    fi
}

# pf_finish STAGE: print the counters; stop (exit 3) when something FAILed
pf_finish() {
    printf '%s\n' "$(m "环境检查：通过 $PF_PASS 项，警告 $PF_WARN 项，失败 $PF_FAIL 项" "Pre-flight: $PF_PASS passed, $PF_WARN warning(s), $PF_FAIL failed")"
    logf "preflight pass=$PF_PASS warn=$PF_WARN fail=$PF_FAIL"
    if [ "$PF_FAIL" -gt 0 ] && [ "$CHECK" != 1 ]; then die_preflight "环境检查未通过，未做任何更改。请先解决上面的 FAIL 项再重试。" "Pre-flight failed; nothing was changed. Fix the FAIL lines above and run again."; fi
    return 0
}

# ---------------------------------------------------------------------------------------------- docker
docker_ready() { have_docker && docker compose version >/dev/null 2>&1; }

# net_retry LABEL CMD...: run CMD (output shown and logged); transient network failures are retried with back-off
# (RETRY_BASE, 3x each time: 5 s, 15 s, ... ; OA_RETRIES attempts, default 3). Permanent errors (unknown manifest, access
# denied, ...) are NOT retried. Docker resumes layers it already downloaded, so a retried pull continues where it stopped.
net_retry() {
    local label="$1" n="${OA_RETRIES:-3}" i=1 rc=0 out wait
    shift
    out="$(mktemp "${TMPDIR:-/tmp}/oa-retry.XXXXXX")"
    while :; do
        if "$@" 2>&1 | tee -a "$LOG_FILE" "$out"; then rc=0; else rc="${PIPESTATUS[0]}"; fi
        [ "$rc" -ne 0 ] || break
        if grep -Eqi 'manifest unknown|manifest for .* not found|not found: manifest|denied|unauthorized|requested access|no matching manifest|repository does not exist|invalid reference format' "$out"; then break; fi
        [ "$i" -lt "$n" ] || break
        wait=$((RETRY_BASE * 3 ** (i - 1)))
        say "  $label 第 $i/$n 次失败，${wait} 秒后重试 …" "  $label failed (attempt $i/$n); retrying in ${wait}s ..."
        sleep "$wait"
        : >"$out"
        i=$((i + 1))
    done
    rm -f -- "$out"
    return "$rc"
}


# docker_pkg_install: distribution packages (best effort: package names differ, and some distributions ship no Compose v2)
docker_pkg_install() {
    local fam; fam="$(os_family)"
    case "$fam" in
        debian) { apt-get update -y >/dev/null 2>&1 || true; } ; apt-get install -y docker.io docker-compose-v2 || apt-get install -y docker.io docker-compose-plugin || apt-get install -y docker.io ;;
        rhel) dnf install -y docker docker-compose-plugin || dnf install -y docker || yum install -y docker ;;
        fedora) dnf install -y moby-engine docker-compose || dnf install -y docker docker-compose-plugin ;;
        suse) zypper --non-interactive install docker docker-compose || zypper --non-interactive install docker ;;
        alpine) apk add --no-cache docker docker-cli-compose && { rc-update add docker default >/dev/null 2>&1 || true; } ;;
        *) return 1 ;;
    esac 2>&1 | tee -a "$LOG_FILE" || true
    have_docker
}

install_docker() {
    local fam
    fam="$(os_family)"
    docker_sources_order
    say "需要安装 Docker Engine + Compose v2。安装源（按顺序尝试）：${SRC_ORDER}${DOCKER_SOURCE:+（--docker-source $DOCKER_SOURCE）}；任何一个源失败后$([ "$SRC_AUTO" = 1 ] && echo '自动试下一个，' || echo '询问是否试其他源，')全部失败则用发行版软件包（apt/dnf/yum/zypper/apk）。" \
        "Docker Engine + Compose v2 must be installed. Installation source(s), tried in this order: ${SRC_ORDER}${DOCKER_SOURCE:+ (--docker-source $DOCKER_SOURCE)}; after a failed source $([ "$SRC_AUTO" = 1 ] && echo 'the next one is tried automatically,' || echo 'you are asked whether to try the other sources,') and when all fail the distribution packages (apt/dnf/yum/zypper/apk) are used."
    [ "$fam" != unknown ] || die "无法识别的发行版（$OS_ID）。请参考 https://docs.docker.com/engine/install/ 手动安装后重试。" "Unrecognised distribution ($OS_ID). Install Docker manually (https://docs.docker.com/engine/install/) and retry."
    if [ "$INSTALL_DOCKER" != 1 ]; then
        [ "$ASSUME_YES" != 1 ] || die "未检测到 Docker。--yes 模式下需显式加 --install-docker 才会自动安装；或先自行安装 Docker 与 compose 插件。" "Docker not found. In --yes mode pass --install-docker explicitly to allow installation, or install Docker and the compose plugin yourself."
        local first_src="${SRC_ORDER%% *}"
        say "将执行（需要联网，需 root）：$(docker_source_cmd "$first_src")；脚本取自 https://get.docker.com（Docker Inc. 官方，第三方脚本），软件包仓库 $(docker_source_base "$first_src")；失败时按上面的顺序/发行版软件包回退。" "About to run (needs network and root): $(docker_source_cmd "$first_src"); the script comes from https://get.docker.com (Docker Inc.'s official, third-party script), package repository $(docker_source_base "$first_src"); on failure the order above / distribution packages follow."
        confirm "是否继续安装 Docker？" "Install Docker now?" n || die_cancel "已取消：未安装 Docker。" "Cancelled: Docker not installed."
    fi
    warn_docker_env
    wait_pkg_lock
    step "安装 Docker" "Installing Docker"
    local done_=0
    case "$fam" in
        debian | rhel | fedora)
            if docker_official_script 8>&-; then done_=1
            else
                warn "官方脚本不可用，回退到发行版软件包 …" "The official script did not work; falling back to distribution packages ..."
                docker_pkg_install 8>&- && done_=1
            fi ;;
        *) docker_pkg_install 8>&- && done_=1 ;;
    esac
    [ "$done_" = 1 ] || die "Docker 安装失败（官方脚本与发行版软件包都失败了）。请参考 https://docs.docker.com/engine/install/ 手动安装后重新运行；详情见 $LOG_FILE。" "Docker installation failed (both the official script and the distribution packages). Install it manually (https://docs.docker.com/engine/install/) and re-run; see $LOG_FILE."
    if command -v systemctl >/dev/null 2>&1; then systemctl enable --now docker >/dev/null 2>&1 || true; fi
    if ! docker compose version >/dev/null 2>&1; then
        case "$fam" in
            debian) apt-get install -y docker-compose-plugin >/dev/null 2>&1 || apt-get install -y docker-compose-v2 >/dev/null 2>&1 || true ;;
            rhel | fedora) { dnf install -y docker-compose-plugin || yum install -y docker-compose-plugin; } >/dev/null 2>&1 || true ;;
        esac
    fi
    docker_ready || die "Docker 安装后仍缺少 compose v2 插件，请手动安装 docker-compose-plugin（https://docs.docker.com/compose/install/linux/）。" "compose v2 plugin still missing after installing Docker; install docker-compose-plugin manually (https://docs.docker.com/compose/install/linux/)."
    ok "Docker 已安装" "Docker installed"
}
install_pkg() {
    case "$(os_family)" in
        debian) apt-get update -y >/dev/null 2>&1 || true; apt-get install -y "$1" ;;
        rhel | fedora) dnf install -y "$1" 2>/dev/null || yum install -y "$1" ;;
        suse) zypper --non-interactive install "$1" ;;
        alpine) apk add --no-cache "$1" ;;
        *) die "请先安装 $1" "Please install $1 first" ;;
    esac
}

# install_compose_only: Docker is there but the Compose v2 plugin is not (or only the legacy v1 binary): add the plugin ONLY
install_compose_only() {
    if [ "$INSTALL_DOCKER" != 1 ]; then
        [ "$ASSUME_YES" != 1 ] || die "已有 Docker，但缺少 Compose v2 插件。--yes 模式下需显式加 --install-docker 才会安装插件（只装插件，不重装 Docker）；也可自行安装 docker-compose-plugin。" "Docker is present but the Compose v2 plugin is missing. In --yes mode pass --install-docker to allow installing the plugin (the plugin only, Docker is not reinstalled), or install docker-compose-plugin yourself."
        confirm "已有 Docker，缺少 Compose v2 插件。只安装该插件（不重装 Docker）？" "Docker is present but the Compose v2 plugin is missing. Install the plugin only (Docker is not reinstalled)?" y || die_cancel "已取消：缺少 Compose v2 插件。" "Cancelled: the Compose v2 plugin is missing."
    fi
    wait_pkg_lock
    step "安装 Compose v2 插件（只装插件）" "Installing the Compose v2 plugin (plugin only)"
    install_compose_plugin 8>&- || die "Compose v2 插件安装失败：请手动安装 docker-compose-plugin（https://docs.docker.com/compose/install/linux/），Docker 本身未被改动。" "Installing the Compose v2 plugin failed: install docker-compose-plugin manually (https://docs.docker.com/compose/install/linux/); Docker itself was not touched."
    ok "Compose v2 插件已安装（Docker 未重装）" "Compose v2 plugin installed (Docker was not reinstalled)"
    act_done "只安装了 Compose v2 插件（Docker 未重装）" "Installed the Compose v2 plugin only (Docker was not reinstalled)"
    DK_EVAL=0
}
ensure_docker() {
    step "检查 Docker" "Checking Docker"
    if ! have_docker; then
        warn "未检测到 Docker。" "Docker is not installed."
        install_docker
        act_done "已安装 Docker Engine 与 Compose v2" "Installed Docker Engine and Compose v2"
        DOCKER_FRESH=1
        DK_EVAL=0
    fi
    if ! docker info >/dev/null 2>&1; then
        if have_cmd systemctl; then systemctl start docker >/dev/null 2>&1 || true; fi
        docker info >/dev/null 2>&1 || die "Docker 守护进程未运行（systemctl start docker）。" "The Docker daemon is not running (systemctl start docker)."
        act_chg "已启动 Docker 守护进程" "Started the Docker daemon"
        DK_EVAL=0
    fi
    if ! docker compose version >/dev/null 2>&1; then
        warn "已有 docker 但缺少 Compose v2 插件。" "docker is present but the Compose v2 plugin is missing."
        install_compose_only
    fi
}

# json_merge_mirrors SRC DST URL...: DST = SRC (absent/empty = {}) with registry-mirrors extended. rc 0 ok, 3 no JSON tool, 4 SRC is
# not a valid JSON object. Needs python3, jq or perl+JSON::PP so that the existing file is really parsed, never patched by text.
json_merge_mirrors() {
    local src="$1" dst="$2" arr="" u sep=""; shift 2
    # nothing to merge into (absent, blank or "{}"): write the file directly, no JSON tool needed
    if [ ! -s "$src" ] || [ "$(tr -d ' \t\r\n' <"$src")" = '{}' ]; then
        for u in "$@"; do arr="$arr$sep    \"$u\""; sep=$',\n'; done
        printf '{\n  "registry-mirrors": [\n%s\n  ]\n}\n' "$arr" >"$dst"
        return 0
    fi
    if [ "$TEST_MODE" = 1 ] && [ "${OA_TEST_NO_JSON:-0}" = 1 ]; then return 3; fi
    arr=''; sep=''
    if command -v python3 >/dev/null 2>&1; then
        python3 - "$src" "$dst" "$@" <<'PY' || return 4
import json, sys
src, dst, urls = sys.argv[1], sys.argv[2], sys.argv[3:]
try:
    raw = open(src).read()
except FileNotFoundError:
    raw = ''
d = json.loads(raw) if raw.strip() else {}
cur = d.get('registry-mirrors', []) if isinstance(d, dict) else None
if not isinstance(d, dict) or not isinstance(cur, list):
    sys.exit(4)
cur += [u for u in urls if u not in cur]
d['registry-mirrors'] = cur
with open(dst, 'w') as f:
    f.write(json.dumps(d, indent=2) + '\n')
PY
        return 0
    fi
    for u in "$@"; do arr="$arr$sep\"$u\""; sep=','; done
    if command -v jq >/dev/null 2>&1; then
        if [ -s "$src" ]; then jq -e 'type == "object" and ((."registry-mirrors" // []) | type == "array")' "$src" >/dev/null 2>&1 || return 4; fi
        { if [ -s "$src" ]; then cat "$src"; else echo '{}'; fi; } | jq --argjson m "[$arr]" '(."registry-mirrors" // []) as $o | ."registry-mirrors" = ($o + ($m - $o))' >"$dst" || return 4
        return 0
    fi
    if perl -MJSON::PP -e1 >/dev/null 2>&1; then
        perl -MJSON::PP -e '
            my ($src, $dst, @urls) = @ARGV; my $raw = "";
            if (open(my $f, "<", $src)) { local $/; $raw = <$f>; close $f }
            my $d = ($raw =~ /\S/) ? eval { decode_json($raw) } : {};
            exit 4 unless ref($d) eq "HASH";
            my $cur = $d->{"registry-mirrors"} // []; exit 4 unless ref($cur) eq "ARRAY";
            my %have = map { $_ => 1 } @$cur; push @$cur, grep { !$have{$_} } @urls; $d->{"registry-mirrors"} = $cur;
            open(my $o, ">", $dst) or exit 1; print $o JSON::PP->new->pretty->canonical->encode($d); close $o;' "$src" "$dst" "$@" || return 4
        return 0
    fi
    return 3
}

# configure_docker_mirror: --docker-mirror URL ...: registry-mirrors in /etc/docker/daemon.json, ONLY after asking. The file is
# parsed and merged (never replaced), backed up first, validated, and an invalid existing file is never touched. Restarting
# Docker is a separate question. With --yes / no terminal nothing is changed and the manual steps are printed.
configure_docker_mirror() {
    # AUTOMATIC (only when THIS run installed Docker, so daemon.json is ours, and mainland China was detected): the built-in candidates
    # are probed on /v2/ and only the ones that answer are used. A trivial daemon.json (absent / empty / {}) is written directly and Docker,
    # which has no workload yet, is restarted; a daemon.json with content goes through the normal consent flow below.
    if [ "${#DOCKER_MIRRORS[@]}" -eq 0 ] && [ "$DOCKER_FRESH" = 1 ] && [ "$DK_VERDICT" = cn ] && [ -z "$OFFLINE_FILE" ]; then
        local cand=() cu tmpj
        while IFS= read -r cu; do [ -z "$cu" ] || cand+=("$cu"); done < <(auto_registry_mirrors)
        if [ "${#cand[@]}" -eq 0 ]; then
            say "Docker 刚装好且检测到中国大陆，但内置的 Docker Hub 镜像都探测不通，不写 daemon.json。需要时手动：在 $DAEMON_JSON 加 \"registry-mirrors\": [\"<你的加速地址>\"] 后 systemctl restart docker，或用 --docker-mirror。" "Docker was just installed and mainland China was detected, but none of the built-in Docker Hub mirrors answered, so daemon.json is not written. By hand when needed: add \"registry-mirrors\": [\"<your mirror>\"] to $DAEMON_JSON and run systemctl restart docker, or use --docker-mirror."
        elif [ ! -s "$DAEMON_JSON" ] || [ "$(tr -d ' \t\r\n' <"$DAEMON_JSON")" = '{}' ]; then
            say "Docker 刚由本脚本安装且检测到中国大陆：以下 Docker Hub 镜像探测可用，自动写入 $DAEMON_JSON 并重启 Docker（尚无其他容器）：${cand[*]}" "Docker was just installed by this script and mainland China was detected: these Docker Hub mirrors answered and are written to $DAEMON_JSON automatically, then Docker is restarted (no other workload yet): ${cand[*]}"
            mkdir -p "$(dirname -- "$DAEMON_JSON")"
            tmpj="$(mktemp "$(dirname -- "$DAEMON_JSON")/.daemon.json.XXXXXX")"
            json_merge_mirrors "$DAEMON_JSON" "$tmpj" "${cand[@]}" || { rm -f -- "$tmpj"; warn "写 daemon.json 失败，已跳过。" "Writing daemon.json failed; skipped."; return 0; }
            chmod 644 "$tmpj"; mv -f "$tmpj" "$DAEMON_JSON"
            if have_cmd systemctl; then systemctl restart docker >/dev/null 2>&1 || warn "重启 Docker 失败，请手动重启。" "Restarting Docker failed; restart it manually."; else service docker restart 8>&- >/dev/null 2>&1 || warn "重启 Docker 失败，请手动重启。" "Restarting Docker failed; restart it manually."; fi
            docker info >/dev/null 2>&1 || die "Docker 重启后未恢复；删除 $DAEMON_JSON 后 systemctl restart docker" "Docker did not come back after the restart; remove $DAEMON_JSON and run systemctl restart docker"
            act_chg "已把探测可用的 Docker Hub 镜像写入 $DAEMON_JSON：${cand[*]}" "Wrote the Docker Hub mirrors that answered to $DAEMON_JSON: ${cand[*]}"
            ok "Docker Hub 镜像加速已配置" "Docker Hub registry mirror configured"
            return 0
        else
            DOCKER_MIRRORS=("${cand[@]}")   # daemon.json already has content: ask first (below)
        fi
    fi
    if [ "${#DOCKER_MIRRORS[@]}" -eq 0 ]; then
        if [ "$MIRROR" = cn ] && [ -z "$OFFLINE_FILE" ]; then
            pf_info "提示：Docker Hub 基础镜像（postgres/caddy）在大陆可能很慢；可加 --docker-mirror <加速地址> 写入 daemon.json（会先询问）。生产镜像来自 ACR，不受影响。" \
                "Tip: Docker Hub base images (postgres/caddy) can be slow in mainland China; add --docker-mirror <mirror URL> (or answer the registry-mirror question of the wizard) to put one into daemon.json (asked first). The production images come from ACR and are not affected."
        fi
        return 0
    fi
    local u missing=() tmp bak rc=0 list=''
    for u in "${DOCKER_MIRRORS[@]}"; do
        if [ -f "$DAEMON_JSON" ] && grep -qF "\"$u\"" "$DAEMON_JSON"; then :; else missing+=("$u"); fi
        list="$list $u"
    done
    if [ "${#missing[@]}" -eq 0 ]; then ok "daemon.json 已包含这些镜像加速地址。" "daemon.json already lists these registry mirrors."; return 0; fi
    step "Docker 镜像加速（可选）" "Docker registry mirror (optional)"
    case "$list" in *" http://"*) warn "含 http:// 地址：镜像拉取内容不加密，建议使用 https。" "An http:// mirror is listed: pulls are not encrypted; prefer https." ;; esac
    say "将把以下地址加入 $DAEMON_JSON 的 \"registry-mirrors\"（保留文件中其他设置，修改前备份）：${missing[*]}" "These will be added to \"registry-mirrors\" in $DAEMON_JSON (other settings kept, backed up first): ${missing[*]}"
    if [ "$ASSUME_YES" = 1 ] || ! have_tty; then
        say "非交互模式不会修改 daemon.json。手动步骤：编辑 $DAEMON_JSON，加入 \"registry-mirrors\": [\"${missing[0]}\"]，然后 systemctl restart docker。" \
            "Non-interactive mode never edits daemon.json. Manual steps: edit $DAEMON_JSON, add \"registry-mirrors\": [\"${missing[0]}\"], then systemctl restart docker."
        return 0
    fi
    confirm "是否修改 $DAEMON_JSON？" "Modify $DAEMON_JSON?" n || { say "已跳过镜像加速配置。" "Skipped the registry mirror configuration."; return 0; }
    mkdir -p "$(dirname -- "$DAEMON_JSON")"
    tmp="$(mktemp "$(dirname -- "$DAEMON_JSON")/.daemon.json.XXXXXX")"
    json_merge_mirrors "$DAEMON_JSON" "$tmp" "${missing[@]}" || rc=$?
    case "$rc" in
        0) ;;
        3) rm -f -- "$tmp"; warn "没有 python3 / jq / perl(JSON::PP)，无法安全合并现有 daemon.json，已保持不变。请手动加入 \"registry-mirrors\": [\"${missing[0]}\"]。" "No python3 / jq / perl(JSON::PP): cannot merge into the existing daemon.json safely, so it was left untouched. Add \"registry-mirrors\": [\"${missing[0]}\"] by hand."; return 0 ;;
        *) rm -f -- "$tmp"; warn "$DAEMON_JSON 不是有效的 JSON 对象，已保持不变。请先修复该文件再配置镜像加速。" "$DAEMON_JSON is not a valid JSON object and was left untouched. Fix the file first, then configure the mirror."; return 0 ;;
    esac
    if command -v dockerd >/dev/null 2>&1 && dockerd --help 2>&1 | grep -q -- '--validate'; then
        dockerd --validate --config-file="$tmp" >/dev/null 2>&1 || { rm -f -- "$tmp"; warn "dockerd 校验新配置失败，daemon.json 保持不变。" "dockerd rejected the new configuration; daemon.json left untouched."; return 0; }
    fi
    if [ -f "$DAEMON_JSON" ]; then
        bak="$DAEMON_JSON.oa-bak.$(date +%Y%m%d-%H%M%S)"
        cp -p "$DAEMON_JSON" "$bak"
        chmod --reference="$DAEMON_JSON" "$tmp" 2>/dev/null || chmod 644 "$tmp"
        say "已备份: $bak" "Backed up to: $bak"
    else chmod 644 "$tmp"; fi
    mv -f "$tmp" "$DAEMON_JSON"
    ok "已更新 $DAEMON_JSON" "Updated $DAEMON_JSON"
    if confirm "镜像加速需要重启 Docker 才生效（未启用 live-restore 时所有运行中的容器会重启）。现在重启 Docker？" "The mirror takes effect after restarting Docker (all running containers restart unless live-restore is enabled). Restart Docker now?" n; then
        if command -v systemctl >/dev/null 2>&1; then systemctl restart docker >/dev/null 2>&1 || warn "重启 Docker 失败，请手动重启。" "Restarting Docker failed; restart it manually."; else service docker restart 8>&- >/dev/null 2>&1 || warn "重启 Docker 失败，请手动重启。" "Restarting Docker failed; restart it manually."; fi
        docker info >/dev/null 2>&1 || die "Docker 重启后未恢复；恢复备份：cp ${bak:-<备份>} $DAEMON_JSON && systemctl restart docker" "Docker did not come back after the restart; restore the backup: cp ${bak:-<backup>} $DAEMON_JSON && systemctl restart docker"
        ok "Docker 已重启" "Docker restarted"
    else
        say "稍后请自行执行：systemctl restart docker" "Restart later yourself: systemctl restart docker"
    fi
}

# ---------------------------------------------------------------------------------------------- source
SRC_ROOT=''
find_local_source() {
    local here root
    # a piped run (curl | bash, docker run | bash) has no script file: there is no local source then
    [ -f "${BASH_SOURCE[0]:-}" ] || return 1
    here="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd || true)"
    [ -n "$here" ] || return 1
    root="$(cd "$here/../.." 2>/dev/null && pwd || true)"
    if [ -n "$root" ] && [ -f "$root/deploy/linux/docker-compose.yml" ] && [ -f "$root/deploy/docker/Dockerfile.api" ] && [ -d "$root/backend" ]; then
        SRC_ROOT="$root"; DEPLOY_SRC="$root/deploy/linux"; return 0
    fi
    return 1
}

download_source() {
    [ -n "$REPO_URL" ] || die "不在源码仓库内运行，且未设置发行包地址。请设置 OA_REPO_URL / --repo-url（占位：发行页地址），或克隆仓库后运行 deploy/linux/install.sh。" \
        "Not running from a checkout and no release location is set. Set OA_REPO_URL / --repo-url (placeholder: your release page URL) or clone the repository and run deploy/linux/install.sh."
    case "$REPO_URL" in https://*) ;; *) die "发行包地址必须是 https://" "The release URL must be https://" ;; esac
    [[ "$REPO_URL" =~ ^https://[A-Za-z0-9._~:/?#@!\&\'\(\)*+,\;=%-]+$ ]] || die "发行包地址含非法字符" "release URL contains illegal characters"
    have_cmd curl || install_pkg curl
    TMP="${TMP:-$(mktemp -d)}"
    local base tag
    if [ "$VERSION" = latest ]; then base="${REPO_URL%/}/releases/latest/download"; else base="${REPO_URL%/}/releases/download/v$VERSION"; fi
    tag="oa-agent-source.tar.gz"
    step "下载发行包" "Downloading release"
    say "  $base/$tag" "  $base/$tag"
    curl -fL --retry 3 --connect-timeout 15 -o "$TMP/$tag" "$base/$tag" || die "下载失败" "Download failed"
    if curl -fsSL --retry 2 --connect-timeout 15 -o "$TMP/$tag.sha256" "$base/$tag.sha256" 2>/dev/null; then
        local want got
        want="$(awk '{print $1}' "$TMP/$tag.sha256" | head -n 1)"
        got="$(sha256sum "$TMP/$tag" | awk '{print $1}')"
        [[ "$want" =~ ^[0-9a-f]{64}$ ]] || die "校验和文件格式错误" "malformed checksum file"
        [ "$want" = "$got" ] || die "SHA-256 校验失败，已中止（文件可能被篡改或下载不完整）。" "SHA-256 mismatch, aborting (tampered or incomplete download)."
        ok "SHA-256 校验通过" "SHA-256 verified"
    else
        if [ "$SKIP_VERIFY" = 1 ]; then warn "未找到校验和，按 --skip-verify 继续。" "No checksum found; continuing because of --skip-verify."
        elif [ "$ASSUME_YES" = 1 ]; then die "未找到校验和文件，--yes 模式拒绝继续（可加 --skip-verify）。" "No checksum file found; refusing in --yes mode (add --skip-verify to override)."
        else confirm "未找到 SHA-256 校验和，无法验证完整性。仍要继续吗？" "No SHA-256 checksum found, integrity cannot be verified. Continue anyway?" n || die_cancel "已取消" "Cancelled"; fi
    fi
    mkdir -p "$TMP/src"
    # refuse absolute paths / .. entries before extracting
    if tar -tzf "$TMP/$tag" | grep -E '(^/|(^|/)\.\.(/|$))' >/dev/null; then die "发行包含不安全路径" "release archive contains unsafe paths"; fi
    tar -xzf "$TMP/$tag" -C "$TMP/src" --strip-components=1
    SRC_ROOT="$TMP/src"
    DEPLOY_SRC="$SRC_ROOT/deploy/linux"
    [ -f "$SRC_ROOT/deploy/linux/docker-compose.yml" ] || die "发行包结构不正确" "unexpected release layout"
}

# ---------------------------------------------------------------------------------------------- registry mode
registry_host() { printf '%s' "${REGISTRY%%/*}"; }
# registry_images: the exact image names this install uses (for messages)
registry_images() { printf '%s/oa-agent-api:%s, %s/oa-agent-worker:%s, %s/oa-agent-deploy:%s' "$REGISTRY" "$VERSION" "$REGISTRY" "$VERSION" "$REGISTRY" "$VERSION"; }
registry_help() { # printed after a failed pull/login: what to check
    say "  需要的镜像: $(registry_images)" "  Images needed: $(registry_images)"
    say "  请检查：1) 地址与标签是否正确（sha-<7位提交>、latest、版本号）；2) 三个仓库 oa-agent-api / oa-agent-worker / oa-agent-deploy 是否存在且已有该标签（ACR 命名空间需先创建仓库或开启「自动创建仓库」）；" \
        "  Check: 1) the address and tag (sha-<7-char commit>, latest, a version); 2) that the three repositories oa-agent-api / oa-agent-worker / oa-agent-deploy exist and carry this tag (an ACR namespace needs the repositories created, or \"auto-create repository\" enabled);"
    say "  3) 私有仓库：先 docker login，或设置 OA_REGISTRY_USER / OA_REGISTRY_PASSWORD 后重试；4) 网络：阿里云 ECS 同地域内网请改用 VPC 域名（把 crpi-xxx 换成 crpi-xxx-vpc）。" \
        "  3) private repository: docker login first, or set OA_REGISTRY_USER / OA_REGISTRY_PASSWORD and retry; 4) network: on an Aliyun ECS in the same region use the VPC endpoint (crpi-xxx -> crpi-xxx-vpc)."
    say "  6) 生产镜像（api/worker）来自上面的仓库，只有 postgres/caddy 基础镜像默认来自 Docker Hub，拉取失败时脚本会自动改用仓库里的副本（oa-agent-postgres / oa-agent-caddy）；仍不行可加 --docker-mirror <加速地址>（写入 daemon.json，先询问），或在 .env 里把 OA_PG_IMAGE / OA_CADDY_IMAGE 改成可访问的地址。" \
        "  6) The production images (api/worker) come from the registry above; the postgres/caddy base images come from Docker Hub by default and the installer automatically falls back to the registry copies (oa-agent-postgres / oa-agent-caddy) when the pull fails; if that fails too, when Docker Hub is slow or unreachable add --docker-mirror <mirror URL> (written to daemon.json, asked first) or point OA_PG_IMAGE / OA_CADDY_IMAGE in .env at a reachable address."
    say "  5) 主机不能访问仓库：在联网机器上运行 scripts/save-images.sh 生成镜像包，再用 install.sh --offline <文件.tar> 安装。" \
        "  5) host without registry access: run scripts/save-images.sh on a connected machine, then install with install.sh --offline <file.tar>."
}

# registry_login: docker login for private registries. Credentials come only from the environment, are passed on stdin
# and are never echoed, logged or written to .env (docker keeps its own login in ~/.docker/config.json).
registry_login() {
    [ -n "${OA_REGISTRY_USER:-}" ] || [ -n "${OA_REGISTRY_PASSWORD:-}" ] || return 0
    [ -n "${OA_REGISTRY_USER:-}" ] && [ -n "${OA_REGISTRY_PASSWORD:-}" ] || die "OA_REGISTRY_USER 与 OA_REGISTRY_PASSWORD 必须同时设置。" "OA_REGISTRY_USER and OA_REGISTRY_PASSWORD must be set together."
    step "登录镜像仓库" "Logging in to the registry"
    if ! printf '%s' "$OA_REGISTRY_PASSWORD" | docker login "$(registry_host)" --username "$OA_REGISTRY_USER" --password-stdin >/dev/null 2>&1; then
        unset OA_REGISTRY_PASSWORD
        say "  docker login $(registry_host) 失败（用户名/密码错误，或 RAM 子账号的企业别名含「.」——阿里云不支持，请改用主账号用户名）。" \
            "  docker login $(registry_host) failed (wrong user/password; Aliyun does not support RAM sub-accounts whose enterprise alias contains a dot: use the main account name)."
        die "镜像仓库登录失败" "Registry login failed"
    fi
    unset OA_REGISTRY_PASSWORD
    ok "已登录 $(registry_host)（凭据未写入 .env 或日志）" "Logged in to $(registry_host) (credentials are not stored in .env or the log)"
}

# fetch_registry_bundle: the installer's own files come from <registry>/oa-agent-deploy:<tag> (docker pull, create, cp, rm).
# Layout of /deploy: install.sh, oa-agentctl.sh, docker-compose*.yml, Caddyfile.template, .env.example, oa-agent.service,
# bin/oa-agentctl-linux-{amd64,arm64}, bin/SHA256SUMS, VERSION.
fetch_registry_bundle() {
    local image="$REGISTRY/oa-agent-deploy:$VERSION" cid
    TMP="${TMP:-$(mktemp -d)}"
    mkdir -p "$TMP/bundle"
    step "获取安装包（镜像 $image）" "Fetching the installer bundle (image $image)"
    if [ -z "$OFFLINE_FILE" ] && ! net_retry "docker pull $image" docker pull "$image"; then registry_help; die "拉取 $image 失败" "Pulling $image failed"; fi
    cid="$(docker create "$image" 2>>"$LOG_FILE")" && [ -n "$cid" ] || die "docker create $image 失败" "docker create $image failed"
    if ! docker cp "$cid:/deploy/." "$TMP/bundle" 2>>"$LOG_FILE"; then docker rm -f "$cid" >/dev/null 2>&1 || true; die "无法从 $image 复制 /deploy" "Cannot copy /deploy out of $image"; fi
    docker rm -f "$cid" >/dev/null 2>&1 || true
    SRC_ROOT="$TMP/bundle"; DEPLOY_SRC="$SRC_ROOT"; BUNDLE=1
    [ -f "$DEPLOY_SRC/docker-compose.yml" ] && [ -f "$DEPLOY_SRC/oa-agentctl.sh" ] || die "$image 不是有效的安装包镜像（缺少 /deploy/docker-compose.yml）" "$image is not a valid installer bundle (/deploy/docker-compose.yml missing)"
    ok "安装包已取出: $(tr '\n' ' ' <"$DEPLOY_SRC/VERSION" 2>/dev/null | cut -c1-120)" "Bundle extracted: $(tr '\n' ' ' <"$DEPLOY_SRC/VERSION" 2>/dev/null | cut -c1-120)"
}


# dcw ARGS...: docker compose inside the install dir with the compose selection recorded in .env
dcw() { (cd "$DIR" && env COMPOSE_FILE="$(env_get COMPOSE_FILE)" docker compose "$@"); }

# stack_images: every image the stack needs (api, worker, base images), one per line
stack_images() {
    local pg cdy
    pg="$(env_get OA_PG_IMAGE)"; cdy="$(env_get OA_CADDY_IMAGE)"
    printf '%s/oa-agent-api:%s\n%s/oa-agent-worker:%s\n%s\n' "$REGISTRY" "$VERSION" "$REGISTRY" "$VERSION" "${pg:-$REGISTRY/oa-agent-postgres:16-alpine}"
    if [ -n "${DOMAIN:-}" ]; then printf "%s\n" "${cdy:-$REGISTRY/oa-agent-caddy:2-alpine}"; fi
}

# default_base_images: pull the postgres/caddy base images from the registry copies (mirrored from Docker Hub by images.yml) instead of
# docker.io, which is slow or unreachable in mainland China. Only fills in what .env does not set, so an explicit OA_PG_IMAGE /
# OA_CADDY_IMAGE always wins. Not used when building from source (no registry) or with --offline (images are loaded from the tarball).
default_base_images() {
    [ "${BUILD_SRC:-0}" != 1 ] && [ -n "${REGISTRY:-}" ] || return 0
    [ -n "$(env_get OA_PG_IMAGE)" ] || env_set OA_PG_IMAGE "$REGISTRY/oa-agent-postgres:16-alpine"
    [ -n "$(env_get OA_CADDY_IMAGE)" ] || env_set OA_CADDY_IMAGE "$REGISTRY/oa-agent-caddy:2-alpine"
}

# pull_stack_images: compose pull with retries (base images come from the registry copies by default, see default_base_images); when it
# still fails, name the failing image(s), and for a base image try Docker Hub, then probed accelerators, before giving up
pull_stack_images() {
    local img failed=()
    default_base_images
    net_retry "docker compose pull" dcw pull && return 0
    say "拉取失败，逐个镜像排查 …" "Pull failed; checking the images one by one ..."
    while IFS= read -r img; do
        [ -n "$img" ] || continue
        docker pull "$img" >/dev/null 2>&1 || failed+=("$img")
    done < <(stack_images)
    if [ "${#failed[@]}" -gt 0 ] && [ -n "${REGISTRY:-}" ]; then
        local still=() var hub name mh u got probed='' hub_ok=''
        for img in "${failed[@]}"; do
            name="${img##*/}"; name="${name#oa-agent-}"      # postgres:16-alpine / caddy:2-alpine
            case "$name" in postgres:*) var=OA_PG_IMAGE ;; caddy:*) var=OA_CADDY_IMAGE ;; *) still+=("$img"); continue ;; esac
            hub="$name"; got=''
            # 1) the registry copy failed (not published yet?): try Docker Hub itself, if it answers within 4 s
            case "$img" in "$REGISTRY"/*)
                if have_cmd curl && case "$(curl -sS -o /dev/null -m 4 --connect-timeout 3 -w '%{http_code}' https://registry-1.docker.io/v2/ 2>/dev/null || true)" in 200 | 401) true ;; *) false ;; esac; then
                    say "仓库里没有 $img，改用 Docker Hub: $hub" "$img is not available in the registry; using Docker Hub: $hub"
                    if docker pull "$hub" >/dev/null 2>&1; then env_set "$var" "$hub"; continue; fi
                fi ;;
            esac
            # 2) accelerators that answer on /v2/ (yours first); pulled under the accelerator name, then tagged as the reference
            #    compose uses, so .env stays unchanged
            [ -n "$probed" ] || probed="$(for u in ${DOCKER_MIRRORS[@]+"${DOCKER_MIRRORS[@]}"}; do printf '%s\n' "$u"; done; auto_registry_mirrors)"
            for mh in $(printf '%s\n' "$probed" | sed 's#^https\?://##; s#/$##' | awk 'NF && !s[$0]++'); do
                if docker pull "$mh/library/$hub" >/dev/null 2>&1 && docker tag "$mh/library/$hub" "$img" >/dev/null 2>&1; then
                    say "已从加速地址拉取: $mh" "Pulled through the accelerator: $mh"; got=1; break
                fi
            done
            [ -n "$got" ] || still+=("$img")
        done
        if [ "${#still[@]}" -eq 0 ]; then
            # images pulled via an accelerator carry the expected tag locally; do not re-pull them
            dcw pull --ignore-pull-failures >/dev/null 2>&1 || true
            local miss=""; while IFS= read -r img; do [ -z "$img" ] || docker image inspect "$img" >/dev/null 2>&1 || miss=1; done < <(stack_images)
            [ -n "$miss" ] || return 0
        else
            failed=("${still[@]}")
        fi
    fi
    registry_help
    if [ "${#failed[@]}" -gt 0 ]; then
        die "镜像拉取失败，出错的镜像: ${failed[*]}（详见上方提示与 $LOG_FILE）" "Image pull failed; failing image(s): ${failed[*]} (see the hints above and $LOG_FILE)"
    fi
    die "镜像拉取失败（未能定位具体镜像，可能是网络抖动；详见上方提示与 $LOG_FILE）" "Image pull failed (could not isolate one image, probably a network hiccup; see the hints above and $LOG_FILE)"
}

# ---------------------------------------------------------------------------------------------- offline (docker save tarball)
OFFLINE_REG=''; OFFLINE_TAG=''
# offline_tags: the oa-agent image names found in the tarball (read from manifest.json / index.json; nothing is extracted)
offline_tags() {
    local t
    t="$(tar -xOf "$OFFLINE_FILE" manifest.json 2>/dev/null | grep -oE '"[^"]+"' || true)"
    echo "$t" | grep -q '/oa-agent-' || t="$(tar -xOf "$OFFLINE_FILE" index.json 2>/dev/null | grep -oE '"[^"]+"' || true)"
    printf '%s\n' "$t" | tr -d '"' | grep -E '/oa-agent-(api|worker|deploy):[A-Za-z0-9._-]+$' || true
}
# offline_inspect: registry and tag stored in the tarball; api, worker and deploy must all be there with the same tag
offline_inspect() {
    local tags dep n
    tags="$(offline_tags)"
    dep="$(printf '%s\n' "$tags" | grep -E '/oa-agent-deploy:' | head -n 1 || true)"
    [ -n "$dep" ] || die "$OFFLINE_FILE 不是有效的镜像包（找不到 oa-agent-deploy 镜像；请用 scripts/save-images.sh 生成）。" "$OFFLINE_FILE is not a valid image bundle (no oa-agent-deploy image found; create it with scripts/save-images.sh)."
    OFFLINE_REG="${dep%/oa-agent-deploy:*}"; OFFLINE_TAG="${dep##*:}"
    for n in api worker; do
        printf '%s\n' "$tags" | grep -qxF "$OFFLINE_REG/oa-agent-$n:$OFFLINE_TAG" || die "镜像包缺少 $OFFLINE_REG/oa-agent-$n:$OFFLINE_TAG。" "The image bundle lacks $OFFLINE_REG/oa-agent-$n:$OFFLINE_TAG."
    done
}
# offline_apply: take registry + tag from the tarball (fresh install) or require that they match the installed ones
offline_apply() {
    [ -n "$OFFLINE_FILE" ] || return 0
    offline_inspect
    if [ "$EXISTING" = 1 ]; then
        [ "$OFFLINE_REG" = "$REGISTRY" ] || die_usage "镜像包的仓库名是 $OFFLINE_REG，已有安装使用 $REGISTRY；两者必须一致。" "The bundle's registry is $OFFLINE_REG but the installation uses $REGISTRY; they must match."
        if [ "$OFFLINE_TAG" != "$VERSION" ]; then
            [ "$DO_UPGRADE" = 1 ] || die_usage "镜像包标签是 $OFFLINE_TAG，已有安装使用 $VERSION：请加 --upgrade 切换到该标签。" "The bundle carries tag $OFFLINE_TAG but the installation uses $VERSION: add --upgrade to switch to it."
            VERSION="$OFFLINE_TAG"; VERSION_SET=1
        fi
    else
        if [ "$REGISTRY_SET" = 1 ] && [ "${REGISTRY%/}" != "$OFFLINE_REG" ]; then die_usage "--registry ${REGISTRY%/} 与镜像包中的仓库名 $OFFLINE_REG 不一致。" "--registry ${REGISTRY%/} differs from the registry name in the bundle ($OFFLINE_REG)."; fi
        if [ "$VERSION_SET" = 1 ] && [ "$VERSION" != "$OFFLINE_TAG" ]; then die_usage "--image-tag $VERSION 与镜像包中的标签 $OFFLINE_TAG 不一致。" "--image-tag $VERSION differs from the tag in the bundle ($OFFLINE_TAG)."; fi
        REGISTRY="$OFFLINE_REG"; VERSION="$OFFLINE_TAG"
    fi
}
# offline_verify: SHA-256 of the tarball against <file>.sha256 (or OA_OFFLINE_SHA256); read-only
offline_verify() {
    local want='' got
    if [ -n "${OA_OFFLINE_SHA256:-}" ]; then want="$OA_OFFLINE_SHA256"
    elif [ -f "$OFFLINE_FILE.sha256" ]; then want="$(awk '{print $1}' "$OFFLINE_FILE.sha256" | head -n 1)"; fi
    if [ -n "$want" ]; then
        [[ "$want" =~ ^[0-9a-fA-F]{64}$ ]] || die "校验和格式错误（需要 64 位十六进制）" "malformed checksum (64 hex characters expected)"
        got="$(sha256sum "$OFFLINE_FILE" | awk '{print $1}')"
        [ "${want,,}" = "$got" ] || die "镜像包 SHA-256 校验失败，已中止（文件损坏或被篡改）。" "SHA-256 mismatch for the image bundle, aborting (corrupted or tampered file)."
        pf_pass "镜像包 SHA-256 校验通过" "Image bundle SHA-256 verified"
    elif [ "$SKIP_VERIFY" = 1 ]; then pf_warn "未找到 $OFFLINE_FILE.sha256，按 --skip-verify 继续。" "No $OFFLINE_FILE.sha256 found; continuing because of --skip-verify."
    elif [ "$ASSUME_YES" = 1 ]; then die "未找到 $OFFLINE_FILE.sha256，--yes 模式拒绝继续（可加 --skip-verify）。" "No $OFFLINE_FILE.sha256 found; refusing in --yes mode (add --skip-verify to override)."
    else confirm "未找到 SHA-256 校验和，无法验证镜像包完整性。仍要继续吗？" "No SHA-256 checksum found, the bundle's integrity cannot be verified. Continue anyway?" n || die_cancel "已取消" "Cancelled"; fi
}
# offline_load: docker load the tarball and make sure the three images (and the host architecture) are right
offline_load() {
    step "载入离线镜像包" "Loading the offline image bundle"
    say "  $OFFLINE_FILE" "  $OFFLINE_FILE"
    docker load -i "$OFFLINE_FILE" 2>&1 | tee -a "$LOG_FILE" || die "docker load 失败（磁盘空间不足或文件损坏？）" "docker load failed (not enough disk space or a corrupt file?)"
    local n img arch pg
    for n in api worker deploy; do
        img="$REGISTRY/oa-agent-$n:$VERSION"
        docker image inspect "$img" >/dev/null 2>&1 || die "载入后找不到镜像 $img" "Image $img not found after loading"
        arch="$(docker image inspect --format '{{.Architecture}}' "$img" 2>/dev/null || true)"
        if [ -n "$arch" ] && [ "$arch" != "$ARCH" ]; then die "镜像 $img 是 $arch 架构，本机是 $ARCH：请在对应架构上重新生成镜像包。" "Image $img is $arch but this host is $ARCH: build the bundle for this architecture."; fi
    done
    ok "api / worker / deploy 镜像已载入（标签 $VERSION）" "api / worker / deploy images loaded (tag $VERSION)"
    pg="$(env_get OA_PG_IMAGE)"; pg="${pg:-postgres:16-alpine}"
    if ! docker image inspect "$pg" >/dev/null 2>&1; then
        warn "镜像包里没有 PostgreSQL 基础镜像 $pg：启动时将尝试从 Docker Hub 拉取，纯离线主机会失败（用 scripts/save-images.sh 默认会包含它）。" "The bundle has no PostgreSQL base image $pg: startup will try Docker Hub, which fails on a fully offline host (scripts/save-images.sh includes it by default)."
    fi
}

sync_source() { # copy source tree + deploy files into the install dir
    if [ "$BUNDLE" != 1 ]; then mkdir -p "$DIR/src"; fi
    if [ "$BUNDLE" != 1 ] && [ "$(cd "$SRC_ROOT" && pwd)" != "$(cd "$DIR/src" && pwd)" ]; then
        (cd "$SRC_ROOT" && tar --exclude=./.git --exclude=node_modules --exclude=.venv --exclude=__pycache__ --exclude=./frontend/dist --exclude=./dist --exclude=./data --exclude=./backend/bin --exclude=./tests/samples -cf - .) |
            (umask 022; cd "$DIR/src" && tar --no-same-owner --no-same-permissions -xf -) # never inherit lax modes (e.g. 0777 from a Windows share)
    fi
    local f
    for f in docker-compose.yml docker-compose.build.yml docker-compose.https.yml Caddyfile.template; do
        cp -f "$DEPLOY_SRC/$f" "$DIR/$f"; chmod 644 "$DIR/$f"
    done
    # the external-database layer exists in releases that support it (an older bundle may not carry it)
    if [ -f "$DEPLOY_SRC/docker-compose.external-db.yml" ]; then cp -f "$DEPLOY_SRC/docker-compose.external-db.yml" "$DIR/docker-compose.external-db.yml"; chmod 644 "$DIR/docker-compose.external-db.yml"
    elif [ "$DB_MODE" = external ]; then die "这个安装包不含 docker-compose.external-db.yml，无法使用已有数据库。请换用较新的 oa-agent-deploy 镜像标签。" "This bundle has no docker-compose.external-db.yml, so an existing database cannot be used. Use a newer oa-agent-deploy image tag."; fi
    if [ -f "$DEPLOY_SRC/.env.example" ]; then cp -f "$DEPLOY_SRC/.env.example" "$DIR/.env.example"; chmod 644 "$DIR/.env.example"; fi
}

install_ctl() {
    # 1) the bash fallback is always installed as oa-agentctl.sh (used by this installer and by the systemd unit)
    mkdir -p "$BIN_DIR"
    local tmp
    tmp="$(mktemp "$BIN_DIR/.oa-agentctl.XXXXXX")"
    sed "s|^OA_DEFAULT_DIR=.*|OA_DEFAULT_DIR='$DIR'|" "$DEPLOY_SRC/oa-agentctl.sh" >"$tmp"
    chmod 755 "$tmp"
    mv -f "$tmp" "$BIN_DIR/oa-agentctl.sh"
    # 2) the Go TUI binary: prebuilt (checksum verified) -> built in a golang container -> bash fallback
    CTL_BINARY=0
    local pre="$SRC_ROOT/bin/oa-agentctl-linux-$ARCH" sums="$SRC_ROOT/bin/SHA256SUMS" want got built=''
    if [ -f "$pre" ]; then
        if [ -f "$sums" ]; then
            want="$(awk -v f="oa-agentctl-linux-$ARCH" '$2==f || $2=="*"f {print $1}' "$sums" | head -n 1)"
            got="$(sha256sum "$pre" | awk '{print $1}')"
            [ -n "$want" ] && [ "$want" = "$got" ] || die "预编译 oa-agentctl 校验和不匹配，已中止。" "Checksum mismatch for the prebuilt oa-agentctl; aborting."
            ok "预编译 oa-agentctl SHA-256 校验通过" "Prebuilt oa-agentctl SHA-256 verified"
        elif [ "$SKIP_VERIFY" != 1 ]; then
            die "发行包缺少 bin/SHA256SUMS，无法校验预编译二进制（可加 --skip-verify）。" "bin/SHA256SUMS missing; cannot verify the prebuilt binary (add --skip-verify to override)."
        fi
        built="$pre"
    elif [ "$BUNDLE" != 1 ] && [ -d "$DIR/src/backend/cmd/oa-agentctl" ] && have_docker; then
        # build from the copy in the install dir (synced above): the same tree the images are built from, and a
        # path the Docker daemon can bind even when the checkout or download dir lives elsewhere
        say "没有预编译二进制，使用 golang 容器编译 oa-agentctl …" "No prebuilt binary; building oa-agentctl in a golang container ..."
        TMP="${TMP:-$(mktemp -d)}"
        mkdir -p "$TMP/ctl-out"
        local goproxy='https://proxy.golang.org,direct'
        [ "$MIRROR" != cn ] || goproxy='https://goproxy.cn,direct'
        if docker run --rm -e CGO_ENABLED=0 -e GOOS=linux -e "GOARCH=$ARCH" -e GOFLAGS=-mod=readonly -e "GOPROXY=$goproxy" -e GOCACHE=/tmp/gocache \
            -v "$DIR/src:/src:ro" -v "$TMP/ctl-out:/out" -w /src/backend golang:1.26 \
            go build -trimpath -ldflags "-s -w" -o /out/oa-agentctl ./cmd/oa-agentctl 2>&1 | tee -a "$LOG_FILE" && [ -s "$TMP/ctl-out/oa-agentctl" ]; then
            built="$TMP/ctl-out/oa-agentctl"
        else
            warn "编译 oa-agentctl 失败，将使用最小备用脚本。" "Building oa-agentctl failed; the minimal fallback script will be used."
        fi
    fi
    if [ -n "$built" ]; then
        tmp="$(mktemp "$BIN_DIR/.oa-agentctl.XXXXXX")"
        cp "$built" "$tmp"; chmod 755 "$tmp"; mv -f "$tmp" "$BIN_DIR/oa-agentctl"
        CTL_BINARY=1
        ok "管理工具已安装: $BIN_DIR/oa-agentctl（TUI）" "Management tool installed: $BIN_DIR/oa-agentctl (TUI)"
    else
        cp -f "$BIN_DIR/oa-agentctl.sh" "$BIN_DIR/oa-agentctl"
        warn "未安装 Go 版 oa-agentctl：$BIN_DIR/oa-agentctl 是最小 bash 备用版。" "Go oa-agentctl not installed: $BIN_DIR/oa-agentctl is the minimal bash fallback."
    fi
}

# ---------------------------------------------------------------------------------------------- mounts (local storage)
EX_NAME=(); EX_HOST=(); EX_RO=()

# mount_path_ok PATH: absolute, safe characters, not a system directory (or inside one)
mount_path_ok() {
    local p="${1%/}"
    [[ "$p" =~ ^/[A-Za-z0-9_./-]+$ ]] || return 1
    case "$p" in *..* | *//*) return 1 ;; esac
    case "$p" in
        /etc | /etc/* | /usr | /usr/* | /bin | /bin/* | /sbin | /sbin/* | /lib | /lib/* | /lib64 | /lib64/* | /boot | /boot/* | \
            /proc | /proc/* | /sys | /sys/* | /dev | /dev/* | /run | /run/* | /var/lib/docker | /var/lib/docker/* | \
            /var | /home | /opt | /mnt | /srv | /tmp | /root) return 1 ;;
    esac
    return 0
}

# check_mount_path LABEL PATH UID [space-check yes|no]: create (0750), test writable, report space/fs type, chown top level
check_mount_path() {
    local label="$1" p="${2%/}" uid="$3" spacechk="${4:-no}" created=0 probe free_kb fst
    local lbl_en="$label"
    case "$label" in 归档根目录) lbl_en="archive root" ;; 导入暂存) lbl_en="spool" ;; 应用数据) lbl_en="application data" ;; esac
    mount_path_ok "$p" || die "$label 路径不合法: $p（须为绝对路径，仅含字母数字 _ . / -，且不能是系统目录如 / /etc /usr /var/lib/docker）" "$lbl_en path is not acceptable: $p (absolute, only alphanumerics _ . / -, and not a system directory such as / /etc /usr /var/lib/docker)"
    if [ ! -d "$p" ]; then
        mkdir -p "$p" 2>/dev/null || die "无法创建 $label 目录: $p" "Cannot create the $lbl_en directory: $p"
        chmod 750 "$p" 2>/dev/null || true
        created=1
    fi
    probe="$(mktemp "$p/.oa-write-test.XXXXXX" 2>/dev/null)" || die "$label 目录不可写（只读文件系统或权限不足）: $p" "The $lbl_en directory is not writable (read-only filesystem or permissions): $p"
    rm -f -- "$probe"
    fst="$(stat -f -c %T "$p" 2>/dev/null || echo unknown)"
    free_kb="$(df -Pk "$p" 2>/dev/null | awk 'NR==2{print $4}' || echo 0)"
    say "  $label: $p（剩余 $((free_kb / 1024 / 1024)) GiB，文件系统 $fst$([ "$created" = 1 ] && echo '，已创建')）" "  $lbl_en: $p ($((free_kb / 1024 / 1024)) GiB free, filesystem $fst$([ "$created" = 1 ] && echo ', created'))"
    if [ "$spacechk" = yes ] && [ "$free_kb" -lt 20971520 ]; then
        warn "$label 所在磁盘剩余不足 20 GiB，请确认容量足够。" "Less than 20 GiB free for $lbl_en; make sure there is enough capacity."
    fi
    case "$fst" in
        tmpfs | overlay | overlayfs | ramfs) warn "$label 位于 $fst：数据可能在重启后丢失或不持久，请改用真实磁盘/NAS 挂载点。" "$lbl_en is on $fst: data may not survive a reboot; use a real disk or NAS mount point." ;;
    esac
    if [ "$(id -u)" -eq 0 ]; then
        chown "$uid:$uid" "$p" 2>/dev/null || warn "无法 chown $p（NAS/NFS 可能限制了属主；请确保容器用户 uid $uid 可写）。" "Cannot chown $p (NAS/NFS may restrict ownership; make sure container uid $uid can write)."
    fi
}

# parse_extra_mount "name=/host/path[:ro|:rw]" -> appends to EX_* (default read-only)
parse_extra_mount() {
    local spec="$1" name host mode=ro i
    if [[ "$spec" =~ ^([a-z0-9][a-z0-9_-]{0,30})=(/[A-Za-z0-9_./-]+)$ ]]; then
        name="${BASH_REMATCH[1]}"; host="${BASH_REMATCH[2]}"
    elif [[ "$spec" =~ ^([a-z0-9][a-z0-9_-]{0,30})=(/[A-Za-z0-9_./-]+):(ro|rw)$ ]]; then
        name="${BASH_REMATCH[1]}"; host="${BASH_REMATCH[2]}"; mode="${BASH_REMATCH[3]}"
    else
        die "--extra-mount 格式应为 名称=/宿主机/路径[:ro|:rw]（名称为小写字母/数字/-/_）: $spec" "--extra-mount must look like name=/host/path[:ro|:rw] (name: lowercase letters/digits/-/_): $spec"
    fi
    mount_path_ok "$host" || die "额外挂载路径不合法: $host" "Unacceptable extra mount path: $host"
    for i in "${!EX_NAME[@]}"; do [ "${EX_NAME[$i]}" != "$name" ] || die "额外挂载名称重复: $name" "Duplicate extra mount name: $name"; done
    EX_NAME+=("$name"); EX_HOST+=("${host%/}"); EX_RO+=("$mode")
}

collect_mounts() { # new installs only: prompts (or flags / defaults) for the host paths
    local x=''
    ARCHIVE_DIR="${ARCHIVE_DIR:-$DIR/data/archive}"
    say "工作目录（归档根目录）：归档后的文件都存放在这里。可以是任意本机绝对路径，例如已挂载的数据盘或 NAS 挂载点。" \
        "Working directory (archive root): every archived file is stored here. Any absolute local path works, e.g. a mounted data disk or a NAS mount point."
    ask_valid ARCHIVE_DIR "归档根目录" "Archive root directory" "$ARCHIVE_DIR" storage_problem
    space_note "${ARCHIVE_DIR%/}"
    SPOOL_DIR="${SPOOL_DIR:-$DIR/data/spool}"; APP_DIR="${APP_DIR:-$DIR/data/app}"; PG_DIR="${PG_DIR:-$DIR/data/pg}"
    if [ "$ASSUME_YES" != 1 ] && have_tty && { seeded spool_dir || seeded app_dir || seeded pg_dir || confirm "是否自定义导入暂存/应用数据$([ "$DB_MODE" = external ] || echo '/数据库')目录（默认都在安装目录的 data/ 下）？" "Customize the spool / app-data$([ "$DB_MODE" = external ] || echo ' / database') directories (default: under data/ in the install directory)?" n; }; then
        ask_valid SPOOL_DIR "导入暂存目录" "Import spool directory" "$SPOOL_DIR" storage_problem
        ask_valid APP_DIR "应用数据目录（含 secret.key）" "Application data directory (holds secret.key)" "$APP_DIR" storage_problem
        if [ "$DB_MODE" != external ]; then ask_valid PG_DIR "PostgreSQL 数据目录" "PostgreSQL data directory" "$PG_DIR" storage_problem; fi
    fi
    local spec why
    for spec in ${EXTRA_MOUNTS[@]+"${EXTRA_MOUNTS[@]}"}; do parse_extra_mount "$spec"; done
    if [ "$ASSUME_YES" != 1 ] && have_tty && ! seeded extra_mount; then
        say "可选：把其他本地目录/NAS 挂载给系统读取（例如待归档的共享盘），容器内路径为 /mnt/storage/<名称>，默认只读。" \
            "Optional: expose other local directories / NAS mounts to the system (e.g. a share to import from) at /mnt/storage/<name>, read-only by default."
        while :; do
            x=''
            ask_valid x "额外本地存储（名称=/宿主机/路径[:ro|:rw]，直接回车结束）" "Extra local storage (name=/host/path[:ro|:rw], empty to finish)" "" chk_extra
            [ -n "$x" ] || break
            parse_extra_mount "$x"
        done
    fi
    ARCHIVE_DIR="${ARCHIVE_DIR%/}"; SPOOL_DIR="${SPOOL_DIR%/}"; APP_DIR="${APP_DIR%/}"; PG_DIR="${PG_DIR%/}"
    if why="$(storage_overlap)"; then die "$why" "$why"; fi
}

# storage_overlap: prints a message and succeeds when two storage directories are equal or nested
storage_overlap() {
    local -a n=(archive spool app pgdata) v=("$ARCHIVE_DIR" "$SPOOL_DIR" "$APP_DIR" "$PG_DIR")
    local i j
    for i in "${!EX_NAME[@]}"; do n+=("${EX_NAME[$i]}"); v+=("${EX_HOST[$i]}"); done
    for ((i = 0; i < ${#v[@]}; i++)); do
        for ((j = i + 1; j < ${#v[@]}; j++)); do
            case "${v[$j]}/" in "${v[$i]}"/*) ;; *) case "${v[$i]}/" in "${v[$j]}"/*) ;; *) continue ;; esac ;; esac
            m "存储目录不能相同或互相嵌套：${n[$i]}=${v[$i]} 与 ${n[$j]}=${v[$j]}" "storage directories must not be equal or nested: ${n[$i]}=${v[$i]} and ${n[$j]}=${v[$j]}"
            return 0
        done
    done
    return 1
}

# validate and prepare the mount directories (also for an existing install: top-level mkdir/chown only)
prepare_mounts() {
    step "检查存储目录" "Checking storage directories"
    check_mount_path "归档根目录" "$ARCHIVE_DIR" 10001 yes
    check_mount_path "导入暂存" "$SPOOL_DIR" 10001 no
    check_mount_path "应用数据" "$APP_DIR" 10001 no
    if [ "$DB_MODE" != external ]; then check_mount_path "PostgreSQL" "$PG_DIR" 70 yes; chmod 700 "$PG_DIR" 2>/dev/null || true; fi
    chmod 700 "$APP_DIR" 2>/dev/null || true
    local i p
    for i in "${!EX_NAME[@]}"; do
        p="${EX_HOST[$i]}"
        [ -d "$p" ] || die "额外挂载目录不存在: $p（请先挂载磁盘/NAS）" "Extra mount directory does not exist: $p (mount the disk/NAS first)"
        if [ "${EX_RO[$i]}" = rw ]; then
            local probe; probe="$(mktemp "$p/.oa-write-test.XXXXXX" 2>/dev/null)" || die "额外挂载 ${EX_NAME[$i]} 声明为 rw 但不可写: $p" "Extra mount ${EX_NAME[$i]} is rw but not writable: $p"
            rm -f -- "$probe"
        fi
        say "  额外挂载 ${EX_NAME[$i]}: $p -> /mnt/storage/${EX_NAME[$i]} (${EX_RO[$i]})" "  extra mount ${EX_NAME[$i]}: $p -> /mnt/storage/${EX_NAME[$i]} (${EX_RO[$i]})"
    done
}

# mounts.json is the source of truth (docker-compose.mounts.yml is generated from it by "oa-agentctl mounts apply")
write_mounts_json() {
    local tmp i sep=''
    tmp="$(mktemp "$DIR/.mounts.XXXXXX")"
    {
        printf '{\n  "version": 1,\n'
        printf '  "archive": {"host": "%s"},\n  "spool": {"host": "%s"},\n  "app": {"host": "%s"},\n  "pgdata": {"host": "%s"},\n' "$ARCHIVE_DIR" "$SPOOL_DIR" "$APP_DIR" "$PG_DIR"
        printf '  "extra": ['
        for i in "${!EX_NAME[@]}"; do
            printf '%s\n    {"name": "%s", "host": "%s", "container": "/mnt/storage/%s", "readonly": %s}' "$sep" "${EX_NAME[$i]}" "${EX_HOST[$i]}" "${EX_NAME[$i]}" "$([ "${EX_RO[$i]}" = ro ] && echo true || echo false)"
            sep=','
        done
        if [ "${#EX_NAME[@]}" -gt 0 ]; then printf '\n  '; fi
        printf ']\n}\n'
    } >"$tmp"
    chmod 640 "$tmp"
    mv -f "$tmp" "$DIR/mounts.json"
}

# read the current host paths of an existing install back from .env (synced there from mounts.json)
load_mounts_from_env() {
    ARCHIVE_DIR="$(env_get OA_ARCHIVE_DIR)"; ARCHIVE_DIR="${ARCHIVE_DIR:-$DIR/data/archive}"
    SPOOL_DIR="$(env_get OA_SPOOL_DIR)"; SPOOL_DIR="${SPOOL_DIR:-$DIR/data/spool}"
    APP_DIR="$(env_get OA_APP_DIR)"; APP_DIR="${APP_DIR:-$DIR/data/app}"
    PG_DIR="$(env_get OA_PG_DIR)"; PG_DIR="${PG_DIR:-$DIR/data/pg}"
    local d
    for d in ARCHIVE_DIR SPOOL_DIR APP_DIR PG_DIR; do
        case "${!d}" in /*) ;; *) printf -v "$d" '%s/%s' "$DIR" "${!d#./}" ;; esac
    done
}

mounts_are_default() {
    [ "$ARCHIVE_DIR" = "$DIR/data/archive" ] && [ "$SPOOL_DIR" = "$DIR/data/spool" ] && [ "$APP_DIR" = "$DIR/data/app" ] && [ "$PG_DIR" = "$DIR/data/pg" ] &&
        { [ ! -f "$DIR/mounts.json" ] || ! grep -q '"name"' "$DIR/mounts.json"; }
}

# Existing install + storage flags: change the working directory / extra mounts through the Go oa-agentctl
# (it validates, keeps mounts.json and .env in sync and never moves or deletes data).
apply_pending_storage() {
    [ "${#PENDING_CORE[@]}" -gt 0 ] || [ "${#PENDING_EXTRA[@]}" -gt 0 ] || return 0
    local bin="$BIN_DIR/oa-agentctl"
    if [ "$CTL_BINARY" != 1 ] || [ ! -x "$bin" ]; then
        die "更改存储目录需要 Go 版 oa-agentctl，但它未能安装。" "Changing storage paths needs the Go oa-agentctl, which could not be installed."
    fi
    local kv role path spec name rest mode
    for kv in ${PENDING_CORE[@]+"${PENDING_CORE[@]}"}; do
        role="${kv%%=*}"; path="${kv#*=}"
        say "更改 $role 目录 → $path" "Changing the $role directory to $path"
        env OA_LANG="$UI" "$bin" --dir "$DIR" --yes mounts set "$role" "$path" || die "oa-agentctl mounts set $role 失败（已有数据不会被移动或删除；需要迁移请用 oa-agentctl mounts set $role <路径> --copy）" "oa-agentctl mounts set $role failed (existing data is never moved or deleted; use oa-agentctl mounts set $role <path> --copy to copy it)"
    done
    for spec in ${PENDING_EXTRA[@]+"${PENDING_EXTRA[@]}"}; do
        name="${spec%%=*}"; rest="${spec#*=}"; mode=--ro
        case "$rest" in *:rw) mode=--rw; rest="${rest%:rw}" ;; *:ro) rest="${rest%:ro}" ;; esac
        say "添加/更新外部挂载 $name → $rest" "Adding/updating extra mount $name -> $rest"
        env OA_LANG="$UI" "$bin" --dir "$DIR" --yes mounts add "$name" "$rest" "$mode" --replace || die "oa-agentctl mounts add $name 失败" "oa-agentctl mounts add $name failed"
    done
}

apply_mounts() { # generate docker-compose.mounts.yml via the Go binary (never in shell)
    local bin="$BIN_DIR/oa-agentctl"
    if [ "$CTL_BINARY" = 1 ] && [ -x "$bin" ]; then
        env OA_LANG="$UI" "$bin" mounts apply --dir "$DIR" || die "oa-agentctl mounts apply 失败" "oa-agentctl mounts apply failed"
        [ -f "$DIR/docker-compose.mounts.yml" ] || die "oa-agentctl 没有生成 docker-compose.mounts.yml" "oa-agentctl did not generate docker-compose.mounts.yml"
        ok "已生成 docker-compose.mounts.yml" "Generated docker-compose.mounts.yml"
    else
        mounts_are_default || die "自定义存储目录/额外挂载需要 Go 版 oa-agentctl 生成 compose 挂载，但它未能安装。请重试（需要 Docker 以编译，或使用含预编译二进制的发行包）。" "Custom storage paths / extra mounts need the Go oa-agentctl to generate the compose mounts, but it could not be installed. Retry (Docker is needed to build it, or use a release with the prebuilt binary)."
        printf '# Fallback stub (Go oa-agentctl unavailable): default directories from docker-compose.yml are used.\nservices: {}\n' >"$DIR/docker-compose.mounts.yml"
        chmod 644 "$DIR/docker-compose.mounts.yml"
        warn "Go 版 oa-agentctl 不可用：使用默认目录与最小备用脚本。" "Go oa-agentctl unavailable: default directories and the minimal fallback script are used."
    fi
}

# ---------------------------------------------------------------------------------------------- configuration
prepare_dirs() {
    mkdir -p "$DIR" "$DIR/backups"
    chmod 750 "$DIR"; chmod 700 "$DIR/backups"
    mkdir -p "$DIR/data/caddy/data" "$DIR/data/caddy/config"
    chmod 750 "$DIR/data"
}

# record DIR as the default install dir of oa-agentctl, unless another live install already owns the marker
CTL_HINT='oa-agentctl'
write_marker() {
    local cur=''
    [ -f "$ETC_MARKER" ] && cur="$(head -n 1 "$ETC_MARKER" 2>/dev/null || true)"
    if [ -n "$cur" ] && [ "$cur" != "$DIR" ] && [ -f "$cur/.env" ]; then
        CTL_HINT="oa-agentctl --dir $DIR"
        warn "本机已有另一套安装 $cur 是 oa-agentctl 的默认目录；管理本安装请加 --dir $DIR。" "Another install ($cur) is oa-agentctl's default; manage this one with --dir $DIR."
        return 0
    fi
    if mkdir -p "$(dirname -- "$ETC_MARKER")" 2>/dev/null && printf '%s\n' "$DIR" >"$ETC_MARKER" 2>/dev/null; then
        chmod 755 "$(dirname -- "$ETC_MARKER")" 2>/dev/null || true
        chmod 644 "$ETC_MARKER" 2>/dev/null || true
    else
        [ "$DIR" = /opt/oa-agent ] || CTL_HINT="oa-agentctl --dir $DIR"
        warn "无法写入 $ETC_MARKER；管理本安装请加 --dir $DIR。" "Cannot write $ETC_MARKER; manage this install with --dir $DIR."
    fi
}

write_new_env() {
    local tmp
    umask 077
    tmp="$(mktemp "$DIR/.env.XXXXXX")"
    {
        printf '# Generated by install.sh on %s. Mode 0600. NEVER share; back up OA_SECRET_KEY separately.\n' "$(date -u +%FT%TZ)"
        printf '# See %s/.env.example for the meaning of every variable.\n' "$DIR"
        printf 'COMPOSE_FILE=%s\n' "$COMPOSE_FILES"
        printf 'COMPOSE_PROJECT_NAME=%s\n' "$PROJECT"
        printf 'OA_DATA_DIR=./data\n'
        printf 'OA_ARCHIVE_DIR=%s\nOA_SPOOL_DIR=%s\nOA_APP_DIR=%s\nOA_PG_DIR=%s\n' "$ARCHIVE_DIR" "$SPOOL_DIR" "$APP_DIR" "$PG_DIR"
        printf 'OA_SRC_DIR=./src\n'
        printf 'OA_VERSION=%s\n' "$VERSION"
        printf 'OA_COMMIT=unknown\n'
        printf 'OA_IMAGE_REGISTRY=%s\n' "${REGISTRY:-$PROJECT}"
        printf 'OA_REPO_URL=%s\n' "$REPO_URL"
        printf 'OA_BUILD_GOPROXY=%s\n' "$B_GOPROXY"
        printf 'OA_BUILD_NPM_REGISTRY=%s\n' "$B_NPM"
        printf 'OA_BUILD_PIP_INDEX=%s\n' "$B_PIP"
        printf 'OA_PORT=%s\n' "$PORT"
        printf 'OA_BIND_ADDR=%s\n' "$BIND"
        if [ "$PROXY_MODE" = 1 ]; then printf 'OA_DOMAIN=\nOA_ACME_EMAIL=\nOA_BEHIND_PROXY=1\n'   # your reverse proxy serves the domain: no Caddy layer
        else printf 'OA_DOMAIN=%s\nOA_ACME_EMAIL=%s\n' "$DOMAIN" "$EMAIL"; fi
        printf 'OA_HTTP_PORT=80\nOA_HTTPS_PORT=443\n'
        printf 'OA_PUBLIC_URL=%s\n' "$PUBLIC_URL"
        printf 'OA_TRUSTED_PROXY=%s\n' "$TRUSTED"
        printf 'OA_POSTGRES_PASSWORD=%s\n' "$(gen_hex 16)"
        printf 'OA_WORKER_TOKEN=%s\n' "$(gen_hex 24)"
        # an orphaned secret.key / the original key of an existing database is REUSED: a new key is never generated over old data
        printf 'OA_SECRET_KEY=%s\n' "${KEY_FROM_FILE:-$(gen_hex 32)}"
        printf 'OA_STORAGE=on\nOA_CORS_ORIGINS=\n'
        printf 'OA_LLM_BASE_URL=\nOA_LLM_MODEL=\nOA_LLM_API_KEY=\nOA_LLM_TIMEOUT_SECONDS=60\nOA_LOG_LEVEL=\n'
        printf 'OA_TZ=%s\n' "$TZ_NAME"
        printf 'OA_LANG=%s\n' "$UI"      # the language chosen at install time: oa-agentctl.sh and the Go tool follow it (OA_LANG in the environment wins)
        printf 'OA_PG_MEM=1g\nOA_API_MEM=1g\nOA_WORKER_MEM=3g\nOA_CADDY_MEM=256m\nOA_LOG_MAX_SIZE=10m\nOA_LOG_MAX_FILE=5\n'
        if [ "$DB_MODE" = external ]; then
            # secrets stay in this 0600 file; the password is single-quoted (compose and oa-agentctl read it literally)
            printf '# ---- existing database server (install.sh validated it; docker-compose.external-db.yml is in COMPOSE_FILE) ----\n'
            printf 'OA_DB_PROVIDER=%s\nOA_DB_HOST=%s\nOA_DB_PORT=%s\nOA_DB_NAME=%s\nOA_DB_USER=%s\n' "$DB_PROVIDER" "$DB_HOST" "$DB_PORT" "$DB_NAME" "$DB_USER"
            printf "OA_DB_PASSWORD='%s'\n" "$(db_password_value)"
            printf 'OA_DB_SCHEMA=%s\nOA_DB_TLS_MODE=%s\n' "$DB_SCHEMA" "${DB_TLS:-require}"
            if [ -n "$DB_CA_FILE" ]; then printf 'OA_DB_TLS_CA_FILE=/data/db-ca.pem\n'; else printf 'OA_DB_TLS_CA_FILE=\n'; fi
        fi
    } >"$tmp"
    chmod 600 "$tmp"
    mv "$tmp" "$(envf)"
}

detect_tz() {
    local tz=''
    if [ -L /etc/localtime ]; then tz="$(readlink /etc/localtime | sed 's|.*/zoneinfo/||')"; fi
    [[ "$tz" =~ ^[A-Za-z0-9_+/-]{1,64}$ ]] || tz=UTC
    printf '%s' "$tz"
}

# detect_public_ip: only with --detect-public-ip. The cloud metadata service (link-local, no third party) is tried first; then
# OA_PUBLIC_IP_URL (default https://api.ipify.org: a THIRD-PARTY service). Prints "address|source" or nothing.
detect_public_ip() {
    local ip='' url="${OA_PUBLIC_IP_URL:-https://api.ipify.org}" src=''
    have_cmd curl || return 0
    ip="$(curl -fsS -m 2 --connect-timeout 1 http://100.100.100.200/latest/meta-data/eipv4 2>/dev/null | head -c 64 || true)"
    if [[ "$ip" =~ ^[0-9]{1,3}(\.[0-9]{1,3}){3}$ ]]; then src='Aliyun ECS metadata'; else ip=''; fi
    if [ -z "$ip" ]; then
        case "$url" in https://*) ;; *) return 0 ;; esac
        ip="$(curl -fsS -m 6 --connect-timeout 4 "$url" 2>/dev/null | head -c 64 || true)"
        src="${url#https://}"; src="${src%%/*}"
    fi
    if [[ "$ip" =~ ^[0-9]{1,3}(\.[0-9]{1,3}){3}$ ]]; then printf '%s|%s' "$ip" "$src"
    elif [[ "$ip" =~ ^[0-9a-fA-F]{1,4}(:[0-9a-fA-F]{0,4}){2,7}$ ]]; then printf '[%s]|%s' "$ip" "$src"; fi
}

host_ip() { local ip; ip="$(list_ips | head -n 1)"; printf '%s' "${ip:-localhost}"; }

# ---------------------------------------------------------------------------------------------- firewall / systemd
offer_firewall() {
    local ports=("$@") tool=''
    if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -qi '^Status: active'; then tool=ufw
    elif command -v firewall-cmd >/dev/null 2>&1 && firewall-cmd --state >/dev/null 2>&1; then tool=firewalld; fi
    [ -n "$tool" ] || return 0
    local want=0
    if [ "$OPEN_FW" = yes ]; then want=1
    elif [ "$ASSUME_YES" != 1 ] && confirm "检测到 $tool 防火墙，是否放行端口 ${ports[*]}？" "$tool firewall detected. Open port(s) ${ports[*]}?" n; then want=1; fi
    if [ "$want" = 1 ]; then
        local p
        for p in "${ports[@]}"; do
            if [ "$tool" = ufw ]; then ufw allow "$p/tcp" >/dev/null; else firewall-cmd --permanent --add-port="$p/tcp" >/dev/null; fi
        done
        if [ "$tool" = firewalld ]; then firewall-cmd --reload >/dev/null; fi
        ok "已放行端口 ${ports[*]}" "Opened port(s) ${ports[*]}"
    else
        say "提示：防火墙 ($tool) 已启用，未做任何修改；如需外部访问请自行放行端口 ${ports[*]}。" "Note: firewall ($tool) is active and was NOT modified; open port(s) ${ports[*]} yourself for external access."
    fi
}

offer_systemd() {
    [ "$SYSTEMD_OK" = 1 ] || return 0
    local want=0
    if [ "$SYSTEMD" = yes ]; then want=1
    elif [ "$ASSUME_YES" != 1 ] && confirm "是否安装 systemd 单元（开机时 compose up -d；容器本身已设置 restart: unless-stopped）？" "Install a systemd unit (compose up -d at boot; containers already use restart: unless-stopped)?" n; then want=1; fi
    [ "$want" = 1 ] || return 0
    mkdir -p "$UNIT_DIR"
    sed "s|__DIR__|$DIR|g" "$DEPLOY_SRC/oa-agent.service" >"$UNIT_DIR/oa-agent.service"
    chmod 644 "$UNIT_DIR/oa-agent.service"
    systemctl daemon-reload >/dev/null 2>&1 || true
    systemctl enable oa-agent.service >/dev/null 2>&1 || true
    ok "systemd 单元已启用: oa-agent.service" "systemd unit enabled: oa-agent.service"
}

# ---------------------------------------------------------------------------------------------- summary
# summary_report: what detection found, what was skipped because it was already there, what was installed or changed
summary_report() {
    local x
    printf '\n%s%s%s\n' "$C_B" "$(m '  【检测与处理结果】' '  [What was detected and done]')" "$C_0"
    for x in ${ACT_SKIP[@]+"${ACT_SKIP[@]}"}; do printf '  - %s %s\n' "$(m '已存在，跳过：' 'already present, skipped:')" "$x"; done
    for x in ${ACT_DONE[@]+"${ACT_DONE[@]}"}; do printf '  - %s %s\n' "$(m '已安装：' 'installed:')" "$x"; done
    for x in ${ACT_CHG[@]+"${ACT_CHG[@]}"}; do printf '  - %s %s\n' "$(m '已更改：' 'changed:')" "$x"; done
    [ "${#ACT_SKIP[@]}${#ACT_DONE[@]}${#ACT_CHG[@]}" != 000 ] || printf '  - %s\n' "$(m '全新的服务器：Docker 之外没有需要跳过的组件。' 'Nothing had to be skipped on this server.')"
    printf '  - %s %s\n' "$(m '状态文件:' 'State file:')" "$([ -f "$STATE_FILE" ] && echo "$STATE_FILE" || m '未能写入（不影响运行；检测会改用 .env 与容器标签）' 'could not be written (no effect on running; detection falls back to .env and container labels)')"
    printf '  - %s\n' "$(m '再次运行本脚本：健康的同版本安装只会显示状态与访问信息（快速、只读为主）；升级/重装/卸载用菜单或 --upgrade/--reinstall/--uninstall。' 'Running this script again: a healthy installation of the same version only shows its status and access information (fast, read-mostly); upgrade/reinstall/uninstall through the menu or --upgrade/--reinstall/--uninstall.')"
    proxy_snippets
}

summary() {
    local url token w=64 ip i pub
    url="$(env_get OA_PUBLIC_URL)"
    token="$(ctl setup-token 2>/dev/null | tail -n 1 || true)"
    [ -n "$token" ] || token="$(m '（无；可能已完成初始化，或运行 oa-agentctl setup-token）' '(none; setup may be complete, or run oa-agentctl setup-token)')"
    printf '\n%s' "$C_G"
    printf '+%*s+\n' "$w" '' | tr ' ' '='
    printf '%s\n' "$(m '  安装完成！' '  Installation complete!')"
    printf '%s\n' "$C_0"
    printf '  %s %s\n' "$(m '访问地址:    ' 'URL:            ')" "$url"
    if [ -z "$(env_get OA_DOMAIN)" ]; then
        while IFS= read -r ip; do
            [ -n "$ip" ] || continue
            if [ "http://$ip:$PORT" != "$url" ]; then printf '  %s %s\n' "$(m '其他地址:    ' 'Other URL:      ')" "http://$ip:$PORT"; fi
        done < <(list_ips)
    else
        printf '  %s %s\n' "$(m '本机地址:    ' 'Loopback URL:   ')" "http://127.0.0.1:$PORT  $(m '（域名模式下仅本机可访问）' '(loopback only in domain mode)')"
    fi
    if [ "$DETECT_IP" = 1 ]; then
        pub="$(detect_public_ip)"
        if [ -n "$pub" ]; then printf '  %s %s\n' "$(m '外网地址:    ' 'External URL:   ')" "http://${pub%%|*}:$PORT  $(m "（探测来源：${pub#*|}；NAT/云主机请核对安全组与端口映射）" "(detected via ${pub#*|}; behind NAT/cloud check the security group and port mapping)")"
        else printf '  %s %s\n' "$(m '外网地址:    ' 'External URL:   ')" "$(m '未能探测到' 'could not be detected')"; fi
    fi
    printf '  %s %s\n' "$(m '初始化令牌:  ' 'SETUP TOKEN:    ')" "$token"
    printf '  %s %s\n' "$(m '安装目录:    ' 'Install dir:    ')" "$DIR"
    if [ -n "$REGISTRY" ]; then printf '  %s %s\n' "$(m '镜像:        ' 'Images:         ')" "$REGISTRY/oa-agent-{api,worker}:$(env_get OA_VERSION)"; fi
    printf '  %s %s\n' "$(m '归档目录:    ' 'Archive dir:    ')" "$ARCHIVE_DIR  $(m '（工作目录：归档后的文件存放在这里）' '(working directory: archived files are stored here)')"
    if [ "$DB_MODE" = external ]; then
        printf '  %s %s\n' "$(m '其他数据:    ' 'Other data:     ')" "$(m '暂存' 'spool') $SPOOL_DIR | $(m '应用数据' 'app data') $APP_DIR"
        printf '  %s %s\n' "$(m '数据库:      ' 'Database:       ')" "$(m '已有' 'existing') $(db_label "$DB_PROVIDER") $DB_HOST:$DB_PORT/$DB_NAME ($(m '用户' 'user') $DB_USER, TLS ${DB_TLS:-require}; $(m '密码在 .env' 'password in .env'))"
    else
        printf '  %s %s\n' "$(m '其他数据:    ' 'Other data:     ')" "$(m '暂存' 'spool') $SPOOL_DIR | $(m '应用数据' 'app data') $APP_DIR | PostgreSQL $PG_DIR"
        printf '  %s %s\n' "$(m '数据库:      ' 'Database:       ')" "$(m '内置 PostgreSQL（compose 容器，不对外发布端口）' 'bundled PostgreSQL (compose container, no published port)')"
    fi
    for i in "${!EX_NAME[@]}"; do
        printf '  %s %s\n' "$(m '额外挂载:    ' 'Extra mount:    ')" "${EX_NAME[$i]}: ${EX_HOST[$i]} -> /mnt/storage/${EX_NAME[$i]} (${EX_RO[$i]})"
    done
    printf '  %s %s\n' "$(m '配置目录:    ' 'Config dir:     ')" "$DIR (.env, mounts.json)"
    printf '  %s %s\n' "$(m '管理命令:    ' 'Management:     ')" "$CTL_HINT  $(m '（不带参数进入交互界面）' '(no arguments: interactive UI)')"
    printf '  %s %s\n' "$(m '             ' '                ')" "$CTL_HINT status | logs | doctor | backup | upgrade | setup-token | help"
    printf '  %s %s\n' "$(m '升级/重装:   ' 'Upgrade/redo:   ')" "$CTL_HINT upgrade [--tag T]  |  sudo bash install.sh --check | --reinstall | --uninstall"
    printf '  %s %s\n' "$(m '更换目录:    ' 'Change dirs:    ')" "$CTL_HINT mounts list | mounts set archive <$(m '新路径' 'new path')> --copy | mounts add <$(m '名称' 'name')> <$(m '路径' 'path')>"
    printf '  %s %s\n' "$(m '安装日志:    ' 'Install log:    ')" "$([ "$LOG_OK" = 1 ] && echo "$LOG_FILE" || echo '-')"
    printf '\n%s%s%s\n' "$C_Y" "$(m '  【重要】' '  [IMPORTANT]')" "$C_0"
    printf '  %s\n' "$(m '1. 实例已对外暴露：请立即打开上面的地址完成初始化向导（创建管理员 + 绑定二次验证），' '1. The instance is exposed: open the URL above NOW and finish the setup wizard (create the admin + enroll MFA);')"
    printf '  %s\n' "$(m '   否则拿到令牌的人可以抢先成为管理员。令牌每次重启都会更换。' '   otherwise anyone who gets the token can become the admin first. The token changes on every restart.')"
    if [ -z "$(env_get OA_DOMAIN)" ]; then
        printf '  %s\n' "$(m '2. 当前为纯 HTTP：Passkey 不可用（只能用 TOTP），口令与会话以明文传输。强烈建议绑定域名：oa-agentctl domain <域名>。' '2. Plain HTTP: Passkeys are unavailable (TOTP only) and credentials travel unencrypted. Strongly recommended: oa-agentctl domain <fqdn>.')"
    else
        printf '  %s\n' "$(m '2. 域名首次访问时 Caddy 申请证书，需要 80/443 对公网可达且 DNS 已解析；更换域名会使 Passkey 失效。' '2. Caddy requests the certificate on first access: 80/443 must be reachable and DNS must resolve; changing the domain invalidates Passkeys.')"
    fi
    printf '  %s\n' "$(m "3. 请备份 $DIR/.env 中的 OA_SECRET_KEY（丢失后已保存的凭据无法解密）：oa-agentctl backup。" "3. Back up OA_SECRET_KEY from $DIR/.env (without it saved credentials cannot be decrypted): oa-agentctl backup.")"
    printf '  %s\n' "$(m '4. 防火墙/云安全组：外部访问需放行 '"$FW_PORTS"' 端口；本脚本不会擅自修改防火墙。' '4. Firewall / cloud security group: open port(s) '"$FW_PORTS"' for external access; this script never changes it on its own.')"
    printf '\n%s%s%s\n' "$C_Y" "$(m '  【请保存好这些】' '  [SAVE THESE]')" "$C_0"
    printf '  %s\n' "$(m "- 密钥与口令都在 $DIR/.env（权限 600）；其中 OA_SECRET_KEY 加密所有已保存的 API 密钥/存储凭据，只有这一份，丢了无法找回。" "- All secrets live in $DIR/.env (mode 600); OA_SECRET_KEY encrypts every saved API key / storage credential, exists only there and cannot be recovered.")"
    printf '  %s\n' "$(m "  查看命令: sudo grep '^OA_SECRET_KEY=' $DIR/.env   （请抄写/存入密码管理器，不要发到聊天或工单里）" "  show it: sudo grep '^OA_SECRET_KEY=' $DIR/.env   (copy it into a password manager; never paste it into chat or tickets)")"
    printf '  %s\n' "$(m "- 应用数据目录 $APP_DIR 里的 secret.key 同样重要；$CTL_HINT backup --out <异地目录> 会把数据库、归档、secret.key 与 .env 一起打包（备份本身也是机密）。" "- secret.key in the app-data directory $APP_DIR matters just as much; $CTL_HINT backup --out <offsite dir> packs the database, archive, secret.key and .env (the backup itself is a secret).")"
    printf '  %s\n' "$(m '- 初始化向导里生成的 TOTP 恢复码也请离线保管。' '- Keep the TOTP recovery codes created in the setup wizard offline as well.')"
    summary_report
    if [ "${#PF_NOTES[@]}" -gt 0 ]; then
        printf '\n%s%s%s\n' "$C_Y" "$(m "  【环境检查中的 ${#PF_NOTES[@]} 条警告，请复核】" "  [${#PF_NOTES[@]} pre-flight warning(s) to review]")" "$C_0"
        for i in "${!PF_NOTES[@]}"; do printf '  - %s\n' "${PF_NOTES[$i]}"; done
    fi
    printf '%s' "$C_G"
    printf '+%*s+\n' "$w" '' | tr ' ' '='
    printf '%s\n' "$C_0"
}

# ---------------------------------------------------------------------------------------------- existing install: menu
# uninstall_flow: containers (and the systemd unit) are removed, data is kept; --purge additionally deletes the install directory
# after the directory NAME is typed. Without a terminal / with --yes the control tool does its own (stricter) confirmation.
uninstall_flow() {
    if [ "$ASSUME_YES" = 1 ] || ! have_tty; then
        if [ "$PURGE" = 1 ]; then ctl uninstall --purge; else ctl uninstall; fi
        return 0
    fi
    local base typed=''
    if [ "$MENU_DONE" != 1 ]; then confirm "停止并移除 OA Agent 容器（数据默认保留）？" "Stop and remove OA Agent containers (data is kept by default)?" n || die_cancel "已取消" "Cancelled"; fi
    if [ "$PURGE" = 1 ]; then
        base="$(basename -- "$DIR")"
        warn "将永久删除 $DIR 下的全部数据（数据库、归档文件、密钥、备份）！" "This permanently deletes everything under $DIR (database, archived files, key, backups)!"
        printf '%s ' "$(m "输入目录名 '$base' 以确认:" "Type the directory name '$base' to confirm:")" >&2
        IFS= read -r typed <&9 || typed=''
        [ "$typed" = "$base" ] || die_cancel "名称不符，已取消（未做任何更改）。" "Name mismatch; cancelled (nothing was changed)."
        OA_PURGE_CONFIRM="$base" ctl_yes uninstall --purge
    else
        ctl_yes uninstall
    fi
}

# ---------------------------------------------------------------------------------------------- language
# locale_ui: zh or en from the locale (LC_ALL, LC_MESSAGES, LANGUAGE, LANG, then /etc/default/locale and /etc/locale.conf). A value of C /
# POSIX / empty says nothing and the next source is consulted; zh_CN, zh_TW, zh-Hans, ZH ... (case-insensitive) mean Chinese.
locale_ui() {
    local v f files="/etc/default/locale /etc/locale.conf" line
    [ "$TEST_MODE" != 1 ] || files="${OA_TEST_LOCALE_FILES-$files}"
    for v in "${LC_ALL:-}" "${LC_MESSAGES:-}" "$(printf %s "${LANGUAGE:-}" | cut -d: -f1)" "${LANG:-}"; do
        v="$(printf '%s' "$v" | tr '[:upper:]' '[:lower:]')"
        case "$v" in '' | c | c.* | posix) continue ;; zh*) printf zh; return 0 ;; *) printf en; return 0 ;; esac
    done
    for f in $files; do
        [ -r "$f" ] || continue
        line="$(grep -E '^(LC_ALL|LC_MESSAGES|LANGUAGE|LANG)=' "$f" 2>/dev/null | head -n 1 || true)"
        v="$(printf '%s' "${line#*=}" | tr -d '"'"'"' ' | cut -d: -f1 | tr '[:upper:]' '[:lower:]')"
        case "$v" in '' | c | c.* | posix) continue ;; zh*) printf zh; return 0 ;; *) printf en; return 0 ;; esac
    done
    printf en
}
# init_lang: the default language before anything is printed; OA_LANG (also set by the parent installer / the control tool) wins
init_lang() {
    UI="$(locale_ui)"
    case "${OA_LANG:-}" in zh* | ZH*) UI=zh; LANG_SET=1 ;; en* | EN*) UI=en; LANG_SET=1 ;; esac
}
# ask_language [force]: the very first question of an interactive run when the language was not given explicitly (read from the
# terminal, so it also works when the script is piped into bash). Unattended runs ask nothing and say which language is used.
ask_language() {
    local c='' def=2 tries=0
    [ "$UI" = zh ] && def=1
    if [ "${1:-}" != force ] && [ "$LANG_SET" = 1 ]; then return 0; fi
    if [ "$ASSUME_YES" = 1 ] || ! have_tty; then
        LANG_SET=1
        say "界面语言：中文（来自系统区域设置；用 --lang zh|en 更改）。" "Language: English (from the system locale; change it with --lang zh|en)."
        return 0
    fi
    printf '\n请选择语言 / Select language:\n  1) 中文\n  2) English\n' >&2
    while :; do
        printf '[%s]: ' "$def" >&2
        IFS= read -r c <&9 || c=''
        case "$c" in
            '') c="$def" ;;
        esac
        case "$c" in
            1 | zh | ZH | cn | 中文) UI=zh; break ;;
            2 | en | EN | english | English) UI=en; break ;;
        esac
        tries=$((tries + 1))
        if [ "$tries" -ge 5 ]; then [ "$def" = 1 ] && UI=zh || UI=en; break; fi
        printf '%s[!]%s 请输入 1 或 2 / enter 1 or 2\n' "$C_R" "$C_0" >&2
    done
    LANG_SET=1
}
q_lang() { ask_language force; }

# ---------------------------------------------------------------------------------------------- Docker installation source
# get.docker.com (verified against the script text): --mirror takes ONLY "Aliyun" (https://mirrors.aliyun.com/docker-ce) or
# "AzureChinaCloud" (https://mirror.azure.cn/docker-ce); the environment variable DOWNLOAD_URL (default https://download.docker.com)
# replaces the package repository base ("$DOWNLOAD_URL/linux/<distribution>/..." and the GPG key), which is how the other mirrors work.
valid_docker_source() {
    case "$1" in official | aliyun | azure | tencent | tuna | ustc | auto) return 0 ;; esac
    [[ "$1" =~ ^https://[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?(:[0-9]{1,5})?(/[A-Za-z0-9._~/-]*)?/?$ ]]
}
docker_source_base() {
    case "$1" in
        official) echo https://download.docker.com ;;
        aliyun) echo https://mirrors.aliyun.com/docker-ce ;;
        azure) echo https://mirror.azure.cn/docker-ce ;;
        tencent) echo https://mirrors.cloud.tencent.com/docker-ce ;;
        tuna) echo https://mirrors.tuna.tsinghua.edu.cn/docker-ce ;;
        ustc) echo https://mirrors.ustc.edu.cn/docker-ce ;;
        https://*) echo "${1%/}" ;;
    esac
}
# docker_source_cmd TOKEN: the exact command line (what will be run)
docker_source_cmd() {
    case "$1" in
        official) echo 'sh get-docker.sh' ;;
        aliyun) echo 'sh get-docker.sh --mirror Aliyun' ;;
        azure) echo 'sh get-docker.sh --mirror AzureChinaCloud' ;;
        *) echo "DOWNLOAD_URL=$(docker_source_base "$1") sh get-docker.sh" ;;
    esac
}
# docker_source_name TOKEN: a readable name
docker_source_name() {
    case "$1" in
        official) m "官方 download.docker.com" "official download.docker.com" ;; aliyun) m "阿里云" "Alibaba Cloud" ;; azure) m "Azure 中国" "Azure China" ;;
        tencent) m "腾讯云" "Tencent Cloud" ;; tuna) m "清华 TUNA" "Tsinghua TUNA" ;; ustc) m "中科大 USTC" "USTC" ;; auto) m "自动" "automatic" ;; *) printf '%s' "$1" ;;
    esac
}
# ---- automatic selection (the same idea as 1Panel: a country lookup decides, then the mirrors are measured and the usable ones are tried
# in order of speed; official last). Local signals keep it working when the lookup is blocked. Nothing is hidden: one line says what
# was detected and what will be used. --no-geo-lookup / OA_INSTALL_GEO=off skips the third-party lookup (ipinfo.io), local signals only.
GEO=on; GEO_COUNTRY=''; DK_VERDICT=''; DK_AUTO_ORDER=''; DK_WHY_ZH=''; DK_WHY_EN=''; DK_RESOLVED=0; DOCKER_FRESH=0
geo_lookup() {
    [ "$GEO" = on ] || return 0
    have_cmd curl || return 0
    local c
    c="$(curl -fsS -m 3 --connect-timeout 2 https://ipinfo.io/country 2>/dev/null | tr -d '[:space:]' | head -c 2 || true)"
    if [[ "$c" =~ ^[A-Za-z]{2}$ ]]; then printf '%s' "$(printf '%s' "$c" | tr '[:lower:]' '[:upper:]')"; fi
    return 0
}
# probe_url URL: "HTTP-code seconds" when ANY HTTP answer came back within 3 seconds, nothing otherwise
probe_url() {
    local out code t
    out="$(curl -sS -o /dev/null -m 3 --connect-timeout 2 -w '%{http_code} %{time_total}' "$1" 2>/dev/null || true)"
    code="${out%% *}"; t="${out#* }"
    if [[ "$code" =~ ^[1-4][0-9][0-9]$ ]]; then printf '%s %s' "$code" "${t:-9}"; fi
    return 0
}
resolve_docker_source() {
    [ "$DK_RESOLVED" = 0 ] || return 0
    DK_RESOLVED=1; DK_AUTO_ORDER=''; DK_VERDICT=''
    case "${DOCKER_SOURCE:-}" in
        '' | auto) ;;
        *) DK_WHY_ZH="按你的指定使用 $(docker_source_name "$DOCKER_SOURCE")"; DK_WHY_EN="as you specified: $(docker_source_name "$DOCKER_SOURCE")"; return 0 ;;
    esac
    local tz ecs why_zh why_en sig_zh='' sig_en='' official=''
    tz="$(detect_tz)"; [ "$TEST_MODE" != 1 ] || tz="${OA_TEST_TZ:-$tz}"
    if [ "$MIRROR" = cn ]; then
        DK_VERDICT=cn; sig_zh="--mirror cn"; sig_en="--mirror cn"
    else
        GEO_COUNTRY="$(geo_lookup)"
        if [ "$GEO_COUNTRY" = CN ]; then DK_VERDICT=cn; sig_zh="ipinfo.io 返回国家 CN"; sig_en="ipinfo.io says country CN"
        elif [ -n "$GEO_COUNTRY" ]; then DK_VERDICT=other; sig_zh="ipinfo.io 返回国家 $GEO_COUNTRY"; sig_en="ipinfo.io says country $GEO_COUNTRY"
        else
            # unknown (blocked, offline, or disabled): local signals
            ecs="$(aliyun_ecs_region 2>/dev/null || true)"
            case "$ecs" in cn-*) DK_VERDICT=cn; sig_zh="阿里云 ECS 地域 $ecs"; sig_en="Alibaba Cloud ECS region $ecs" ;; esac
            if [ -z "$DK_VERDICT" ]; then case "$tz" in Asia/Shanghai | Asia/Chongqing | Asia/Harbin | Asia/Urumqi | PRC) DK_VERDICT=cn; sig_zh="时区 $tz"; sig_en="time zone $tz" ;; esac; fi
            if [ -z "$DK_VERDICT" ] && have_cmd curl; then
                official="$(probe_url https://download.docker.com/linux/)"
                if [ -z "$official" ]; then DK_VERDICT=cn; sig_zh="download.docker.com 3 秒内不可达"; sig_en="download.docker.com is unreachable within 3 seconds"; fi
            fi
            [ -n "$DK_VERDICT" ] || { DK_VERDICT=other; sig_zh="国家未知（${GEO:+查询失败或被屏蔽}），本地信号无中国大陆迹象"; sig_en="country unknown (lookup failed, blocked or disabled), no local sign of mainland China"; }
            [ "$GEO" = on ] || { sig_zh="$sig_zh（已按 --no-geo-lookup 关闭 ipinfo.io 查询）"; sig_en="$sig_en (the ipinfo.io lookup is off: --no-geo-lookup)"; }
        fi
    fi
    if [ "$DK_VERDICT" = cn ]; then
        local tok r list=''
        if have_cmd curl; then
            for tok in aliyun azure tencent tuna ustc; do
                r="$(probe_url "$(docker_source_base "$tok")/linux/")"
                if [ -n "$r" ]; then list="$list${r#* } $tok"$'\n'; fi
            done
            DK_AUTO_ORDER="$(printf '%s' "$list" | sort -s -n -k1,1 | awk '{print $2}' | tr '\n' ' ')official"
        else DK_AUTO_ORDER="aliyun azure tuna ustc official"; fi
        why_zh="自动：检测到中国大陆（$sig_zh）→ 按速度依次尝试可达的镜像：${DK_AUTO_ORDER}"
        why_en="automatic: mainland China detected ($sig_en) -> reachable mirrors tried by speed: ${DK_AUTO_ORDER}"
        [ "${DK_AUTO_ORDER%% *}" != official ] || { why_zh="自动：检测到中国大陆（$sig_zh），但没有可达的镜像 → 使用官方源"; why_en="automatic: mainland China detected ($sig_en) but no mirror answered -> official source"; }
    else
        DK_AUTO_ORDER=official
        why_zh="自动：${sig_zh} → 官方源 download.docker.com"; why_en="automatic: ${sig_en} -> official source download.docker.com"
    fi
    DK_WHY_ZH="$why_zh"; DK_WHY_EN="$why_en"
}
announce_docker_source() {
    resolve_docker_source
    say "Docker 安装源：$DK_WHY_ZH（可用 --docker-source 或复核页第 8 项更改）" "Docker installation source: $DK_WHY_EN (change it with --docker-source or setting 8 of the review screen)"
}
# docker_sources_order: sets SRC_ORDER (tried in this order) and SRC_AUTO (1 = fall through to the next without asking)
docker_sources_order() {
    SRC_AUTO=0
    case "${DOCKER_SOURCE:-}" in
        '' | auto) resolve_docker_source; SRC_ORDER="${DK_AUTO_ORDER:-official}"; SRC_AUTO=1 ;;
        *) SRC_ORDER="$DOCKER_SOURCE" ;;
    esac
}
# auto_registry_mirrors: the built-in candidates that really answer on /v2/ right now (200 or 401); never a mirror that was not probed
auto_registry_mirrors() {
    local u c
    have_cmd curl || return 0
    for u in https://docker.1ms.run https://docker.m.daocloud.io https://docker.1panel.live https://mirror.ccs.tencentyun.com; do
        c="$(curl -sS -o /dev/null -m 4 --connect-timeout 3 -w '%{http_code}' "$u/v2/" 2>/dev/null || true)"
        case "$c" in 200 | 401) printf '%s\n' "$u" ;; esac
    done
}
chk_https_url() { valid_docker_source "$1" && [ "$1" != auto ] && [[ "$1" == https://* ]] || { m "需要 https:// 开头的地址（该地址下应有 linux/<发行版>/gpg 与仓库文件，与 download.docker.com 同样的目录结构）" "an https:// URL is required (it must serve linux/<distribution>/gpg and the repository files in the same layout as download.docker.com)"; return 1; }; }
chk_choice_1_8() { case "$1" in 1 | 2 | 3 | 4 | 5 | 6 | 7 | 8) ;; *) m "请输入 1-8" "enter 1-8"; return 1 ;; esac; }
# ---------------------------------------------------------------------------------------------- visible progress for the Docker installation
# get.docker.com installs silently (apt-get -qq ... >/dev/null) for minutes; run_docker_cmd therefore runs it in its own session, streams
# what it prints, and every OA_HEARTBEAT seconds (default 15) says how long it has been running, which package-manager process is active
# and whether the download directory / dpkg log are moving, so "slow" and "hung" can be told apart. After OA_DOCKER_TIMEOUT seconds
# (default 1200) ONLY that child's process group is stopped and diagnostics are shown.
APT_PARTIAL='/var/cache/apt/archives/partial'; DPKG_LOG='/var/log/dpkg.log'; APT_TERM_LOG='/var/log/apt/term.log'
if [ "$TEST_MODE" = 1 ]; then APT_PARTIAL="${OA_APT_PARTIAL:-$APT_PARTIAL}"; DPKG_LOG="${OA_DPKG_LOG:-$DPKG_LOG}"; APT_TERM_LOG="${OA_APT_TERM_LOG:-$APT_TERM_LOG}"; fi
DOCKER_CHILD=''
# pkg_procs: running package-manager processes ("pid etime command ..."), one per line
pkg_procs() {
    ps -eo pid,etime,args 2>/dev/null | awk 'NR > 0 { n = split($3, a, "/"); b = a[n]
        if (b ~ /^(apt|apt-get|aptitude|dpkg|unattended-upgr|unattended-upgrade|unattended-upgrades|packagekitd|dnf|yum|rpm|zypper|apk)$/ || $0 ~ /get-docker\.sh/) print }' || true
}
# pkg_lock_holders: processes that hold the package-manager lock (the same list, minus our own get-docker run)
pkg_lock_holders() {
    local files="/var/lib/dpkg/lock-frontend /var/lib/dpkg/lock /var/lib/apt/lists/lock /var/cache/apt/archives/lock /var/run/yum.pid /var/lib/rpm/.rpm.lock"
    pkg_procs | grep -v 'get-docker\.sh' || true
    if [ "$TEST_MODE" != 1 ] && have_cmd fuser; then
        # shellcheck disable=SC2086
        fuser -v $files 2>&1 | grep -E '[0-9]+' | sed 's/^/lock: /' || true
    fi
}
# wait_pkg_lock: another apt/dpkg/unattended-upgrades run would make the installation block silently
wait_pkg_lock() {
    local limit="${OA_LOCK_WAIT:-300}" start now h ans=''
    h="$(pkg_lock_holders)"
    [ -n "$h" ] || return 0
    warn "检测到另一个包管理器进程正在运行（apt/dpkg/unattended-upgrades 等），它持有安装锁：" "Another package-manager process is running (apt/dpkg/unattended-upgrades ...) and holds the installation lock:"
    printf '%s\n' "$h" | head -n 5 | sed 's/^/    /' >&2
    if [ "$ASSUME_YES" != 1 ] && have_tty; then
        printf '%s ' "$(m '[w]等它结束（最长 '"$limit"' 秒，默认）/ [c]不管锁直接继续 / [a]中止:' '[w]ait for it to finish (up to '"$limit"' s, default) / [c]ontinue regardless / [a]bort:')" >&2
        IFS= read -r ans <&9 || ans=''
        case "$ans" in c | C) return 0 ;; a | A) die_cancel "已中止：包管理器被占用。" "Aborted: the package manager is busy." ;; esac
    fi
    start="$(date +%s)"
    while [ -n "$(pkg_lock_holders)" ]; do
        now="$(date +%s)"
        if [ $((now - start)) -ge "$limit" ]; then
            die "等了 ${limit} 秒，包管理器仍被占用：先让上面的进程结束（或 systemctl stop unattended-upgrades），再重新运行本脚本；也可用 OA_LOCK_WAIT=<秒> 延长等待。" "Waited ${limit} s and the package manager is still busy: let the process above finish (or systemctl stop unattended-upgrades) and re-run this script; OA_LOCK_WAIT=<seconds> waits longer."
        fi
        say "  仍在等待包管理器空闲（已等 $((now - start)) 秒）：$(pkg_lock_holders | head -n 1 | cut -c1-100)" "  still waiting for the package manager to become idle ($((now - start)) s so far): $(pkg_lock_holders | head -n 1 | cut -c1-100)"
        sleep "${OA_HEARTBEAT:-15}"
    done
}
# env_kind: container | wsl | none (where docker-ce's postinst may not be able to start the service)
env_kind() {
    [ "$TEST_MODE" != 1 ] || [ -z "${OA_TEST_ENVKIND:-}" ] || { printf '%s' "$OA_TEST_ENVKIND"; return 0; }
    if grep -qi microsoft /proc/version 2>/dev/null; then printf wsl
    elif [ -e /.dockerenv ] || [ -e /run/.containerenv ] || { have_cmd systemd-detect-virt && systemd-detect-virt -cq 2>/dev/null; }; then printf container
    else printf none; fi
}
# warn_docker_env: BEFORE installing Docker: without systemd (container / WSL without systemd) the docker-ce post-install script can hang or fail
warn_docker_env() {
    local k; k="$(env_kind)"
    [ "$k" != none ] || return 0
    [ "$SYSTEMD_OK" != 1 ] || return 0
    case "$k" in
        wsl) warn "检测到 WSL 且没有 systemd：docker-ce 的安装后脚本启动服务时可能卡住或失败。正确做法：在 /etc/wsl.conf 写 [boot] systemd=true 后 wsl --shutdown 重开，或使用 Docker Desktop 的 WSL 集成（宿主 Windows 上的 Docker）。" "WSL without systemd detected: the docker-ce post-install script may hang or fail when it starts the service. Do this instead: put [boot] systemd=true into /etc/wsl.conf and run wsl --shutdown, or use Docker Desktop's WSL integration (Docker on the Windows host)." ;;
        *) warn "检测到容器环境且没有 systemd：在容器里装 docker-ce，安装后脚本启动服务时可能卡住或失败。正确做法：不要在容器里装 Docker，改为挂载宿主机的 /var/run/docker.sock 使用宿主 Docker，或在宿主机上运行本安装器。" "A container without systemd was detected: installing docker-ce inside a container, its post-install script may hang or fail when it starts the service. Do this instead: do not install Docker inside the container, use the host's Docker through its /var/run/docker.sock, or run this installer on the host." ;;
    esac
    if [ "$ASSUME_YES" != 1 ] && have_tty; then
        confirm "仍要在这里安装 Docker？" "Install Docker here anyway?" n || die_cancel "已取消：未安装 Docker。" "Cancelled: Docker not installed."
    fi
}
# dir_kb DIR: size in KB (0 when absent)
dir_kb() { du -sk "$1" 2>/dev/null | awk '{print $1}' | head -n 1 | grep -E '^[0-9]+$' || echo 0; }
docker_heartbeat() { # docker_heartbeat ELAPSED PREV_KB_VAR
    local el="$1" prev kb cur trend age mt
    prev="${!2:-0}"; kb="$(dir_kb "$APT_PARTIAL")"
    if [ "$kb" -gt "$prev" ]; then trend="$(m '在增长' 'growing')"; elif [ "$kb" -lt "$prev" ]; then trend="$(m '已减小（下载完成/清理）' 'shrank (download finished / cleaned)')"; else trend="$(m '无变化' 'unchanged')"; fi
    printf -v "$2" '%s' "$kb"
    cur="$(pkg_procs | tail -n 1 | cut -c1-110)"; [ -n "$cur" ] || cur="$(m '（没有发现包管理器进程）' '(no package-manager process found)')"
    age=''; if [ -f "$DPKG_LOG" ]; then mt="$(stat -c %Y "$DPKG_LOG" 2>/dev/null || echo 0)"; [ "$mt" -le 0 ] || age="$(( $(date +%s) - mt ))"; fi
    say "  仍在安装 Docker…已用 ${el} 秒；当前进程：${cur}；apt 下载目录 ${kb} KB（${trend}）${age:+；dpkg 日志 ${age} 秒前有更新}" "  Still installing Docker... ${el} s elapsed; current process: ${cur}; apt download directory ${kb} KB (${trend})${age:+; dpkg log last written ${age} s ago}"
}
# docker_diag: concrete diagnostics after a timeout
docker_diag() {
    say "诊断信息（超时后）：" "Diagnostics (after the timeout):"
    printf '  -- %s\n' "$(m '包管理器相关进程 (ps)' 'package-manager processes (ps)')" >&2; pkg_procs | head -n 8 | sed 's/^/     /' >&2
    printf '  -- %s\n' "$(m '谁持有安装锁 (fuser/lsof)' 'who holds the installation lock (fuser/lsof)')" >&2
    if have_cmd fuser; then fuser -v /var/lib/dpkg/lock-frontend /var/lib/dpkg/lock /var/lib/apt/lists/lock 2>&1 | sed 's/^/     /' >&2 || true
    elif have_cmd lsof; then lsof /var/lib/dpkg/lock-frontend /var/lib/dpkg/lock 2>/dev/null | sed 's/^/     /' >&2 || true
    else printf '     %s\n' "$(m '（没有 fuser/lsof）' '(neither fuser nor lsof installed)')" >&2; fi
    if [ -f "$DPKG_LOG" ]; then printf '  -- tail %s\n' "$DPKG_LOG" >&2; tail -n 15 "$DPKG_LOG" | sed 's/^/     /' >&2; fi
    if [ -f "$APT_TERM_LOG" ]; then printf '  -- tail %s\n' "$APT_TERM_LOG" >&2; tail -n 15 "$APT_TERM_LOG" | sed 's/^/     /' >&2; fi
    if [ "$SYSTEMD_OK" = 1 ]; then printf '  -- systemctl status docker\n' >&2; systemctl status docker --no-pager -l 2>&1 | head -n 15 | sed 's/^/     /' >&2 || true; fi
}
# run_docker_cmd CMD...: rc 124 = timed out (the child's process group was stopped), otherwise the command's own status
run_docker_cmd() {
    local to="${OA_DOCKER_TIMEOUT:-1200}" hb="${OA_HEARTBEAT:-15}" tick="${OA_TICK:-1}" out start now el last=0 off=0 size rc=0 timed=0 prev=0 i
    out="$(mktemp "${TMPDIR:-/tmp}/oa-docker.XXXXXX")"
    prev="$(dir_kb "$APT_PARTIAL")"
    if have_cmd setsid; then setsid "$@" >"$out" 2>&1 </dev/null 8>&- & else "$@" >"$out" 2>&1 </dev/null 8>&- & fi
    DOCKER_CHILD=$!
    start="$(date +%s)"
    while kill -0 "$DOCKER_CHILD" 2>/dev/null; do
        sleep "$tick"
        size="$(wc -c <"$out" 2>/dev/null || echo 0)"
        if [ "$size" -gt "$off" ]; then tail -c +"$((off + 1))" "$out" | tee -a "$LOG_FILE"; off="$size"; fi
        now="$(date +%s)"; el=$((now - start))
        if [ "$el" -ge "$to" ]; then
            timed=1
            warn "Docker 安装超过 ${to} 秒仍未结束，停止它的进程组（只停本脚本启动的这一个，不会碰其他进程）。" "The Docker installation ran longer than ${to} s; stopping its process group (only the one started by this script, nothing else)."
            kill -TERM -- "-$DOCKER_CHILD" 2>/dev/null || kill -TERM "$DOCKER_CHILD" 2>/dev/null || true
            for i in 1 2 3 4 5; do kill -0 "$DOCKER_CHILD" 2>/dev/null || break; sleep "$tick"; done
            kill -KILL -- "-$DOCKER_CHILD" 2>/dev/null || kill -KILL "$DOCKER_CHILD" 2>/dev/null || true
            break
        fi
        if [ $((el - last)) -ge "$hb" ]; then docker_heartbeat "$el" prev; last="$el"; fi
    done
    wait "$DOCKER_CHILD" 2>/dev/null || rc=$?
    DOCKER_CHILD=''
    size="$(wc -c <"$out" 2>/dev/null || echo 0)"
    if [ "$size" -gt "$off" ]; then tail -c +"$((off + 1))" "$out" | tee -a "$LOG_FILE"; fi
    rm -f -- "$out"
    if [ "$timed" = 1 ]; then return 124; fi
    return "$rc"
}
# docker_timeout_choice: interactive menu after a timeout -> r | n | a ; unattended: fail with the hints
TO_CHOICE=''
docker_timeout_choice() {
    local ch=''
    docker_diag
    if [ "$ASSUME_YES" = 1 ] || ! have_tty; then
        die "Docker 安装超时（${OA_DOCKER_TIMEOUT:-1200} 秒）。看上面的诊断：常见原因是 apt 锁被占用、网络极慢、或容器/WSL 无 systemd。解决后重新运行；OA_DOCKER_TIMEOUT=<秒> 可延长。" "The Docker installation timed out (${OA_DOCKER_TIMEOUT:-1200} s). See the diagnostics above: common causes are a held apt lock, a very slow network, or a container/WSL without systemd. Fix it and re-run; OA_DOCKER_TIMEOUT=<seconds> allows longer."
    fi
    printf '%s ' "$(m '[r]重试同一个源 / [n]换下一个源 / [a]中止（默认）:' '[r]etry the same source / [n]ext source / [a]bort (default):')" >&2
    IFS= read -r ch <&9 || ch=''
    case "$ch" in r | R) TO_CHOICE=r ;; n | N) TO_CHOICE=n ;; *) TO_CHOICE=a ;; esac
}

# run_docker_source TOKEN: show exactly what is run and from where, then run it (rc 0 ok, 1 failed, 2 timed out)
run_docker_source() {
    local tok="$1" base cmd
    base="$(docker_source_base "$tok")"; cmd="$(docker_source_cmd "$tok")"
    say "执行：$cmd   （脚本取自 https://get.docker.com，Docker Inc. 官方，属第三方脚本；软件包仓库：$base）" "Running: $cmd   (the script comes from https://get.docker.com, Docker Inc.'s official but third-party script; package repository: $base)"
    local rc=0
    case "$tok" in
        official) run_docker_cmd env -u DOWNLOAD_URL sh "$TMP/get-docker.sh" || rc=$? ;;
        aliyun) run_docker_cmd env -u DOWNLOAD_URL sh "$TMP/get-docker.sh" --mirror Aliyun || rc=$? ;;
        azure) run_docker_cmd env -u DOWNLOAD_URL sh "$TMP/get-docker.sh" --mirror AzureChinaCloud || rc=$? ;;
        *) run_docker_cmd env DOWNLOAD_URL="$base" sh "$TMP/get-docker.sh" || rc=$? ;;
    esac
    if [ "$rc" = 124 ]; then return 2; fi
    have_docker
}
# docker_official_script: Docker Inc.'s get.docker.com script from the chosen source; the next source is only tried with consent
# (interactive) or when the order came from auto / --mirror cn. Returns 1 when everything failed (the caller falls back to distro packages).
docker_official_script() {
    TMP="${TMP:-$(mktemp -d)}"
    have_cmd curl || install_pkg curl || return 1
    local srcs=() src rest='' all="official aliyun azure tuna ustc" x rc
    if ! net_retry "get.docker.com" curl -fsSL --connect-timeout 10 --max-time 120 https://get.docker.com -o "$TMP/get-docker.sh"; then
        warn "下载 get.docker.com 脚本失败。" "Failed to download the get.docker.com script."
        return 1
    fi
    docker_sources_order
    read -r -a srcs <<<"$SRC_ORDER"
    for src in "${srcs[@]}"; do
        while :; do
            rc=0; run_docker_source "$src" || rc=$?
            [ "$rc" != 0 ] || return 0
            if [ "$rc" = 2 ]; then
                warn "安装源 $src 超时（$(docker_source_cmd "$src")）。" "The installation source $src timed out ($(docker_source_cmd "$src"))."
                docker_timeout_choice
                case "$TO_CHOICE" in r) continue ;; n) break ;; *) die_cancel "已中止：Docker 安装超时。" "Aborted: the Docker installation timed out." ;; esac
            fi
            warn "安装源 $src 失败（$(docker_source_cmd "$src")）。" "The installation source $src failed ($(docker_source_cmd "$src"))."
            break
        done
    done
    if [ "$SRC_AUTO" = 0 ]; then
        for x in $all; do case " $SRC_ORDER " in *" $x "*) ;; *) rest="$rest$x " ;; esac; done
        if [ "$ASSUME_YES" != 1 ] && have_tty && confirm "依次尝试其他内置安装源（${rest}）？" "Try the other built-in installation sources in turn (${rest})?" y; then
            for src in $rest; do
                rc=0; run_docker_source "$src" || rc=$?
                [ "$rc" != 0 ] || return 0
                warn "安装源 $src 失败。" "The installation source $src failed."
            done
        fi
    fi
    return 1
}

# ---------------------------------------------------------------------------------------------- state file
# $STATE_FILE (/etc/oa-agent/install.json) records every installation this installer made: one JSON object per line inside
# {"schema":1,"installs":[ ... ]}. Values are restricted to a safe character set (validated paths, tags, project names), so
# no JSON escaping is needed and plain bash can rewrite it. It holds NO secrets. The .env of each directory stays authoritative
# for versions and ports; the state file's job is to FIND installations again (also those under a custom directory).
state_entries() { [ -f "$STATE_FILE" ] || return 0; grep -E '^\{"dir":"' "$STATE_FILE" 2>/dev/null | sed 's/,[[:space:]]*$//' || true; }
state_get() { # state_get LINE KEY
    local v
    v="$(printf '%s' "$1" | sed -n 's/.*"'"$2"'":"\([^"]*\)".*/\1/p')"
    if [ -z "$v" ]; then v="$(printf '%s' "$1" | sed -n 's/.*"'"$2"'":\([0-9][0-9]*\).*/\1/p')"; fi
    printf '%s' "$v"
}
# state_write STATUS: (re)write the entry of $DIR from the current settings (.env when it exists), keeping the other entries
state_write() {
    local status="$1" ver proj reg port dom dbm
    ver="$(env_get OA_VERSION)"; ver="${ver:-$VERSION}"
    proj="$(env_get COMPOSE_PROJECT_NAME)"; proj="${proj:-${PROJECT:-oa-agent}}"
    reg="$(env_get OA_IMAGE_REGISTRY)"; reg="${reg:-${REGISTRY:-$PROJECT}}"
    port="$(env_get OA_PORT)"; port="${port:-$PORT}"
    dom="$(env_get OA_DOMAIN)"
    case ":$(env_get COMPOSE_FILE):" in *:docker-compose.external-db.yml:*) dbm="$(env_get OA_DB_PROVIDER)" ;; *) dbm=bundled ;; esac
    [[ "$ver" =~ ^[A-Za-z0-9._-]{1,64}$ ]] || ver=unknown
    [[ "$reg" =~ ^[A-Za-z0-9._:/-]{1,200}$ ]] || reg=unknown
    [[ "$port" =~ ^[0-9]{1,5}$ ]] || port=0
    [[ "$dom" =~ ^[A-Za-z0-9.-]{0,253}$ ]] || dom=''
    if ! mkdir -p "$(dirname -- "$STATE_FILE")" 2>/dev/null; then
        warn "无法写入状态文件 $STATE_FILE（不影响安装；以后检测将依赖 $DIR/.env 与容器标签）。" "Cannot write the state file $STATE_FILE (the installation is not affected; later detection relies on $DIR/.env and the container labels)."
        return 0
    fi
    state_store "$DIR" "$(printf '{"dir":"%s","project":"%s","version":"%s","registry":"%s","db":"%s","port":%s,"domain":"%s","status":"%s","archive":"%s","app":"%s","pg":"%s","updated":"%s"}' \
        "$DIR" "$proj" "$ver" "$reg" "$dbm" "$port" "$dom" "$status" "${ARCHIVE_DIR:-}" "${APP_DIR:-}" "${PG_DIR:-}" "$(date -u +%FT%TZ)")"
    logf "state file updated: $STATE_FILE ($status $DIR)"
    return 0
}
# state_store DIR ENTRY: replace the entry of DIR by ENTRY (empty ENTRY = forget DIR), keeping the other entries; atomic
state_store() {
    local tmp l d first=1
    tmp="$(mktemp "$(dirname -- "$STATE_FILE")/.install.json.XXXXXX" 2>/dev/null)" || return 0
    {
        printf '{"schema":1,"installs":[\n'
        while IFS= read -r l; do
            [ -n "$l" ] || continue
            d="$(state_get "$l" dir)"
            [ "$d" != "$1" ] || continue
            [ "$first" = 1 ] || printf ',\n'
            printf '%s' "$l"; first=0
        done < <(state_entries)
        if [ -n "$2" ]; then
            [ "$first" = 1 ] || printf ',\n'
            printf '%s' "$2"
        fi
        printf '\n]}\n'
    } >"$tmp"
    chmod 644 "$tmp" 2>/dev/null || true
    mv -f "$tmp" "$STATE_FILE" 2>/dev/null || rm -f -- "$tmp"
    return 0
}
state_forget() { [ -f "$STATE_FILE" ] || return 0; state_store "$1" ""; }
state_has() { local l; while IFS= read -r l; do [ "$(state_get "$l" dir)" != "$1" ] || return 0; done < <(state_entries); return 1; }

# ---------------------------------------------------------------------------------------------- detection helpers
os_pm() {
    case "$(os_family)" in
        debian) echo apt-get ;;
        rhel) if have_cmd dnf; then echo dnf; else echo yum; fi ;;
        fedora) echo dnf ;;
        suse) echo zypper ;;
        alpine) echo apk ;;
        *) echo '' ;;
    esac
}
# pkg_for_cmd TOOL: the package that provides it on this distribution family
pkg_for_cmd() {
    case "$1" in
        flock) echo util-linux ;;
        ss) case "$(os_family)" in rhel | fedora) echo iproute ;; *) echo iproute2 ;; esac ;;
        sha256sum) echo coreutils ;;
        awk) echo gawk ;;
        *) echo "$1" ;;
    esac
}

CT_LINES=()   # docker ps -a: name|image|state|project|workdir|ports|service|status
load_containers() {
    CT_LINES=()
    have_docker || return 0
    local out l
    out="$(docker ps -a --format '{{.Names}}|{{.Image}}|{{.State}}|{{.Label "com.docker.compose.project"}}|{{.Label "com.docker.compose.project.working_dir"}}|{{.Ports}}|{{.Label "com.docker.compose.service"}}|{{.Status}}' 2>/dev/null || true)"
    while IFS= read -r l; do
        case "$l" in *'|'*'|'*'|'*'|'*'|'*'|'*'|'*) CT_LINES+=("$l") ;; esac
    done <<<"$out"
    return 0
}
ct_field() { # ct_field LINE N (1-based)
    printf '%s' "$1" | awk -F'|' -v n="$2" '{print $n}'
}

# ss_line PORT: the "ss -ltnp" line of the listener on PORT ('' when none / no ss)
ss_line() {
    have_cmd ss || return 0
    ss -ltnp 2>/dev/null | awk -v p="$1" '{n = split($4, a, ":"); if (a[n] == p) {print; exit}}' || true
}
# port_owner PORT: "process|pid" (either may be empty: without root ss cannot show other users' processes)
port_owner() {
    local l name='' pid='' r
    l="$(ss_line "$1")"
    if [ -n "$l" ]; then
        name="$(printf '%s' "$l" | sed -n 's/.*users:(("\([^"]*\)".*/\1/p')"
        pid="$(printf '%s' "$l" | sed -n 's/.*pid=\([0-9][0-9]*\).*/\1/p' | head -n 1)"
    elif have_cmd lsof; then
        r="$(lsof -nP -iTCP:"$1" -sTCP:LISTEN 2>/dev/null | awk 'NR==2{print $1 "|" $2}' || true)"
        if [ -n "$r" ]; then name="${r%%|*}"; pid="${r#*|}"; fi
    fi
    printf '%s|%s' "$name" "$pid"
}
# port_bind PORT: the address the listener is bound to (0.0.0.0, 127.0.0.1, [::], *, ...)
port_bind() { ss_line "$1" | awk '{n = split($4, a, ":"); s = $4; sub(":" a[n] "$", "", s); print s}' || true; }
# port_container PORT: "name|image" of the running container that publishes PORT ('' when none)
port_container() {
    local l p
    for l in ${CT_LINES[@]+"${CT_LINES[@]}"}; do
        [ "$(ct_field "$l" 3)" = running ] || continue
        p="$(ct_field "$l" 6)"
        case "$p" in *":$1->"* | *":$1-"*) printf '%s|%s' "$(ct_field "$l" 1)" "$(ct_field "$l" 2)"; return 0 ;; esac
    done
    return 0
}
# port_kind NAME: proxy | database | other (by process name)
port_kind() {
    case "$1" in
        nginx | openresty | caddy | apache2 | httpd | haproxy | traefik | envoy | lighttpd | 1panel | 1panel-core | BT-Panel | bt) echo proxy ;;
        postgres | postmaster | mysqld | mariadbd | sqlservr | mysqld_safe) echo database ;;
        *) echo other ;;
    esac
}
# port_describe PORT: a human description of who holds the port
port_describe() {
    local o name pid c cname cimg
    o="$(port_owner "$1")"; name="${o%%|*}"; pid="${o#*|}"
    c="$(port_container "$1")"
    if [ -n "$c" ]; then
        cname="${c%%|*}"; cimg="${c#*|}"
        printf '%s' "$(m "Docker 容器 $cname（镜像 $cimg）" "Docker container $cname (image $cimg)")"
    elif [ -n "$name" ] && [ "$name" != docker-proxy ]; then
        printf '%s%s%s' "$name" "${pid:+ (pid $pid)}" "$([ "$(port_kind "$name")" = proxy ] && m '，反向代理/面板' ', a reverse proxy / panel')"
    else
        printf '%s' "$(m '未知进程（用 root 运行 ss -ltnp 可看到属主）' 'an unknown process (run ss -ltnp as root to see the owner)')"
    fi
}
# next_free_port START: the first free port from START upward (at most 200 tries)
next_free_port() {
    local candp="$1" tries=0
    while [ "$tries" -lt 200 ] && [ "$candp" -lt 65535 ]; do
        if ! port_in_use "$candp"; then printf '%s' "$candp"; return 0; fi
        candp=$((candp + 1)); tries=$((tries + 1))
    done
    printf '%s' "$1"
}
# proxy_owner_of_web: prints the process name of a reverse proxy holding 80 or 443 ('' otherwise)
proxy_owner_of_web() {
    local p o name
    for p in 80 443; do
        port_in_use "$p" || continue
        o="$(port_owner "$p")"; name="${o%%|*}"
        if [ "$(port_kind "$name")" = proxy ]; then printf '%s' "$name"; return 0; fi
    done
    return 0
}

# ver_kind TAG: semver | sha | mutable (latest, stable, branch names ... move over time)
ver_kind() {
    if [[ "$1" =~ ^v?[0-9]+(\.[0-9]+)*([.-][0-9A-Za-z.]+)?$ ]]; then echo semver
    elif [[ "$1" =~ ^sha-[0-9a-f]{7,40}$ ]]; then echo sha
    else echo mutable; fi
}
# ver_relation INSTALLED REQUESTED: same | newer (requested is newer) | older | different | mutable (same moving tag)
ver_relation() {
    local a="${1#v}" b="${2#v}" ba bb
    if [ "$1" = "$2" ]; then
        if [ "$(ver_kind "$1")" = mutable ]; then echo mutable; else echo same; fi
        return 0
    fi
    if [ "$(ver_kind "$1")" = semver ] && [ "$(ver_kind "$2")" = semver ]; then
        ba="${a%%-*}"; bb="${b%%-*}"
        if [ "$ba" = "$bb" ]; then
            # same base version: a pre-release (1.2.3-rc.1) is older than the release (1.2.3)
            if [ "$a" = "$ba" ]; then echo older; else echo newer; fi
            return 0
        fi
        if [ "$(printf '%s\n%s\n' "$ba" "$bb" | sort -V | head -n 1)" = "$ba" ]; then echo newer; else echo older; fi
        return 0
    fi
    echo different
}

# ---------------------------------------------------------------------------------------------- detection: prerequisites, systemd, firewall
det_prereqs() {
    PREREQ_MISSING=(); PREREQ_REC=()
    local c
    for c in curl tar gzip sha256sum awk; do have_cmd "$c" || PREREQ_MISSING+=("$c"); done
    for c in openssl flock ss; do have_cmd "$c" || PREREQ_REC+=("$c"); done
    if [ "${#PREREQ_MISSING[@]}" -eq 0 ]; then
        pf_pass "必需工具齐全：curl tar gzip sha256sum awk" "Required tools present: curl tar gzip sha256sum awk"
    else
        pf_missing "缺少必需工具：${PREREQ_MISSING[*]} -> 只安装缺少的这些（经 $(os_pm)），其余保持不动" "Missing required tools: ${PREREQ_MISSING[*]} -> only these are installed (through $(os_pm)); everything else is left alone"
    fi
    if [ "${#PREREQ_REC[@]}" -gt 0 ]; then
        pf_missing "缺少推荐工具：${PREREQ_REC[*]}（openssl 生成密钥，flock 防并发，ss 识别端口占用者）-> 同样只安装缺少的" "Missing recommended tools: ${PREREQ_REC[*]} (openssl makes keys, flock prevents concurrent runs, ss shows port owners) -> installed the same way"
    fi
    return 0
}
# install_prereqs: install ONLY what det_prereqs found missing, through the detected package manager, after consent
install_prereqs() {
    local all=("${PREREQ_MISSING[@]}" "${PREREQ_REC[@]}") c pk pkgs=() p seen=' '
    [ "${#all[@]}" -gt 0 ] || return 0
    [ "$(os_family)" != unknown ] || { if [ "${#PREREQ_MISSING[@]}" -gt 0 ]; then die "缺少必需工具 ${PREREQ_MISSING[*]}，且无法识别发行版（$OS_ID）：请手动安装后重试。" "Required tools ${PREREQ_MISSING[*]} are missing and the distribution ($OS_ID) is not recognised: install them manually and retry."; fi; return 0; }
    for c in "${all[@]}"; do
        pk="$(pkg_for_cmd "$c")"
        case "$seen" in *" $pk "*) ;; *) pkgs+=("$pk"); seen="$seen$pk " ;; esac
    done
    if [ "$INSTALL_PREREQS" != 1 ]; then
        if [ "$ASSUME_YES" = 1 ] || ! have_tty; then
            if [ "${#PREREQ_MISSING[@]}" -gt 0 ]; then
                die "缺少必需工具：${PREREQ_MISSING[*]}。--yes 模式需显式加 --install-prereqs（或 --install-docker）才会安装；也可自行执行：$(os_pm) install ${pkgs[*]}" "Required tools are missing: ${PREREQ_MISSING[*]}. In --yes mode add --install-prereqs (or --install-docker) to allow installing them, or run: $(os_pm) install ${pkgs[*]}"
            fi
            say "提示：缺少推荐工具 ${PREREQ_REC[*]}，未安装（--yes 下需 --install-prereqs）。" "Note: recommended tools ${PREREQ_REC[*]} are missing and were not installed (needs --install-prereqs with --yes)."
            return 0
        fi
        confirm "将用 $(os_pm) 只安装缺少的软件包：${pkgs[*]}。继续？" "Install only the missing packages with $(os_pm): ${pkgs[*]}. Continue?" y || {
            if [ "${#PREREQ_MISSING[@]}" -gt 0 ]; then die_cancel "已取消：缺少必需工具。" "Cancelled: required tools are missing."; fi
            return 0
        }
    fi
    wait_pkg_lock
    step "安装缺少的软件包" "Installing the missing packages"
    for p in "${pkgs[@]}"; do install_pkg "$p" 8>&- 2>&1 | tee -a "$LOG_FILE" || true; done
    for c in "${PREREQ_MISSING[@]}"; do have_cmd "$c" || die "安装后仍缺少 $c。" "$c is still missing after the installation."; done
    act_done "已安装缺少的软件包：${pkgs[*]}" "Installed the missing packages: ${pkgs[*]}"
    PREREQ_MISSING=(); PREREQ_REC=()
}

det_systemd() {
    if have_cmd systemctl && [ -d "$SYSTEMD_RUN_DIR" ]; then
        SYSTEMD_OK=1
        pf_pass "systemd 可用（可选：开机自启单元）" "systemd is available (optional: start-on-boot unit)"
    else
        SYSTEMD_OK=0
        pf_info "未检测到 systemd：容器靠 Docker 的 restart: unless-stopped 随 Docker 自启；不提供 systemd 单元。" "systemd not detected: the containers restart with Docker (restart: unless-stopped); no systemd unit is offered."
    fi
    return 0
}

# ---------------------------------------------------------------------------------------------- detection: Docker and Compose
# docker_enabled_state: yes | no | unknown (is the docker service enabled at boot?)
docker_enabled_state() {
    [ "$SYSTEMD_OK" = 1 ] || { printf unknown; return 0; }
    if systemctl is-enabled --quiet docker >/dev/null 2>&1; then printf yes; else printf no; fi
}
# docker_socket: the endpoint the CLI talks to
docker_socket() {
    local h="${DOCKER_HOST:-}"
    if [ -z "$h" ]; then h="$(docker context inspect --format '{{.Endpoints.docker.Host}}' 2>/dev/null | head -n 1 || true)"; fi
    if [ -z "$h" ]; then if [ -S /var/run/docker.sock ]; then h=unix:///var/run/docker.sock; else h="$(m '未知' 'unknown')"; fi; fi
    printf '%s' "$h"
}
# det_compose_v1: version of a legacy docker-compose (v1) binary, '' when absent
compose_v1_version() {
    have_cmd docker-compose || return 0
    docker-compose version --short 2>/dev/null | head -n 1 || true
}
det_docker() {
    DK_SVER=''; DK_UP=0; DK_CMP=''; DK_CMP_KIND=none; DK_EVAL=0
    local v1 sock en rl
    if ! have_docker; then
        if [ "$(env_kind)" != none ] && [ "$SYSTEMD_OK" != 1 ]; then pf_warn "$(env_kind) 环境且没有 systemd：在这里安装 docker-ce，其安装后脚本启动服务时可能卡住或失败（正确做法：WSL 开启 systemd 或用 Docker Desktop 集成；容器里改用宿主的 docker.sock）。" "A $(env_kind) environment without systemd: installing docker-ce here, its post-install script may hang or fail when starting the service (do this instead: enable systemd in WSL or use Docker Desktop integration; in a container use the host's docker.sock)."; fi
        pf_missing "Docker 引擎：未安装 -> 将安装（需 --install-docker 或交互确认）；Compose v2 插件随 Docker 一起安装" "Docker Engine: not installed -> will be installed (needs --install-docker or interactive consent); the Compose v2 plugin comes with it"
        return 0
    fi
    case "$(docker --version 2>/dev/null || true)" in *[Pp]odman*) pf_warn "docker 命令实际是 Podman：本安装器只支持 Docker Engine（Compose v2 与 compose 文件的某些语法在 Podman 下行为不同）。" "The docker command is really Podman: this installer supports Docker Engine only (Compose v2 and some compose-file syntax behave differently under Podman)." ;; esac
    if ! docker info >/dev/null 2>&1; then
        pf_todo "Docker 引擎：已安装但守护进程未运行 -> 将尝试 systemctl start docker（已安装的 Docker 不会被重装）" "Docker Engine: installed but the daemon is not running -> will try systemctl start docker (an installed Docker is never reinstalled)"
        v1="$(compose_v1_version)"
        [ -z "$v1" ] || pf_info "另检测到旧版 docker-compose $v1（v1，已停止维护）：OA Agent 需要 Compose v2 插件。" "A legacy docker-compose $v1 (v1, end of life) was also found: OA Agent needs the Compose v2 plugin."
        return 0
    fi
    DK_UP=1
    DK_SVER="$(docker version --format '{{.Server.Version}}' 2>/dev/null || true)"

    DK_CMP="$(docker compose version --short 2>/dev/null || true)"; DK_CMP="${DK_CMP#v}"
    if [ -n "$DK_CMP" ]; then DK_CMP_KIND=plugin; fi
    sock="$(docker_socket)"; en="$(docker_enabled_state)"
    rl="$(m '以 root 运行' 'rootful')"
    case "$(docker info --format '{{.SecurityOptions}}' 2>/dev/null || true)" in *rootless*) rl="rootless" ;; esac
    case "$en" in yes) en="$(m '开机自启已启用' 'enabled at boot')" ;; no) en="$(m '未设置开机自启（可 systemctl enable docker）' 'not enabled at boot (systemctl enable docker)')" ;; *) en="$(m '开机自启状态未知' 'boot state unknown')" ;; esac
    if [ -n "$DK_SVER" ] && ! ver_ge "$(num_prefix "$DK_SVER")" 20.10; then
        pf_fail "Docker $DK_SVER 过旧（需要 20.10 以上，建议 24+）；请升级 Docker Engine。" "Docker $DK_SVER is too old (20.10+ required, 24+ recommended); upgrade Docker Engine."
    else
        pf_found "Docker 引擎 ${DK_SVER:-?}：守护进程运行中，$en，$rl，端点 $sock -> 沿用，不重装" "Docker Engine ${DK_SVER:-?}: daemon running, $en, $rl, endpoint $sock -> reused, not reinstalled"
        act_skip "Docker 引擎 ${DK_SVER:-?} 已存在，未重装" "Docker Engine ${DK_SVER:-?} already present, not reinstalled"
        case "$rl" in rootless) pf_warn "检测到 rootless Docker：绑定挂载目录的属主（uid 10001/70）与 1024 以下端口行为不同，本安装器按 rootful Docker 测试。" "Rootless Docker detected: bind-mount ownership (uid 10001/70) and ports below 1024 behave differently; this installer is tested with rootful Docker." ;; esac
    fi
    if [ "$DK_CMP_KIND" = plugin ]; then
        if ver_ge "$(num_prefix "$DK_CMP")" 2.24; then
            pf_found "Compose v2 插件 $DK_CMP（≥2.24：外部数据库层可用）-> 沿用" "Compose v2 plugin $DK_CMP (>= 2.24: the external-database layer works) -> reused"
        elif ver_ge "$(num_prefix "$DK_CMP")" 2.0; then
            pf_found "Compose v2 插件 $DK_CMP（<2.24：可用于内置数据库；外部数据库层需要 2.24+）-> 沿用" "Compose v2 plugin $DK_CMP (< 2.24: fine for the bundled database; the external-database layer needs 2.24+) -> reused"
        else
            pf_todo "Compose 插件 $DK_CMP 过旧（需要 v2）-> 将只升级该插件" "Compose plugin $DK_CMP is too old (v2 required) -> only the plugin will be upgraded"
        fi
        act_skip "Compose 插件 $DK_CMP 已存在" "Compose plugin $DK_CMP already present"
    else
        v1="$(compose_v1_version)"
        if [ -n "$v1" ]; then
            pf_todo "Compose：只找到旧版 docker-compose $v1（v1）；OA Agent 需要 Compose v2 插件 -> 将只安装该插件，不重装 Docker" "Compose: only a legacy docker-compose $v1 (v1) was found; OA Agent needs the Compose v2 plugin -> only the plugin will be installed, Docker is not reinstalled"
        else
            pf_missing "Compose v2 插件：未安装 -> 将只安装该插件，不重装 Docker" "Compose v2 plugin: not installed -> only the plugin will be installed, Docker is not reinstalled"
        fi
    fi
    DK_EVAL=1
    return 0
}
# docker_data_space: the Docker data directory's free space (only meaningful when the daemon runs)
det_docker_space() {
    local root kb
    [ "$DK_UP" = 1 ] || return 0
    root="$(docker info --format '{{.DockerRootDir}}' 2>/dev/null || true)"
    if [ -n "$root" ] && [ -d "$root" ]; then
        kb="$(df -Pk "$root" 2>/dev/null | awk 'NR==2{print $4}' || echo 0)"; kb="${kb:-0}"
        if [ "$kb" -lt 5242880 ]; then pf_warn "Docker 数据目录 $root 所在磁盘剩余不足 5 GB（镜像约 2-3 GB，升级时会同时存在新旧两套）。" "Less than 5 GB free where Docker stores images ($root); the images need ~2-3 GB and an upgrade briefly holds two sets."
        else pf_pass "Docker 数据目录 $root 空间充足" "Docker data directory $root has enough space"; fi
    fi
    return 0
}

# install_compose_plugin: the Compose v2 plugin ONLY (distribution package of the repository Docker came from); never touches Docker itself
install_compose_plugin() {
    local fam; fam="$(os_family)"
    case "$fam" in
        debian) { apt-get install -y docker-compose-plugin || apt-get install -y docker-compose-v2; } >/dev/null 2>&1 || { apt-get update -y >/dev/null 2>&1 || true; apt-get install -y docker-compose-plugin >/dev/null 2>&1 || apt-get install -y docker-compose-v2 >/dev/null 2>&1 || true; } ;;
        rhel) { dnf install -y docker-compose-plugin || yum install -y docker-compose-plugin; } >/dev/null 2>&1 || true ;;
        fedora) { dnf install -y docker-compose-plugin || dnf install -y docker-compose; } >/dev/null 2>&1 || true ;;
        suse) zypper --non-interactive install docker-compose >/dev/null 2>&1 || true ;;
        alpine) apk add --no-cache docker-cli-compose >/dev/null 2>&1 || true ;;
        *) return 1 ;;
    esac
    docker compose version >/dev/null 2>&1
}

# ---------------------------------------------------------------------------------------------- detection: existing installations
CAND_DIR=(); CAND_SRC=()
IN_DIR=(); IN_SRC=(); IN_VER=(); IN_PROJ=(); IN_PORT=(); IN_DOM=(); IN_DBM=(); IN_REG=(); IN_HEALTH=(); IN_COMPOSE=()
DANGLING=()
in_add_candidate() { # in_add_candidate DIR SOURCE
    local d="${1%/}" i
    [ -n "$d" ] || return 0
    case "$d" in /*) ;; *) return 0 ;; esac
    for i in "${!CAND_DIR[@]}"; do
        if [ "${CAND_DIR[$i]}" = "$d" ]; then
            case " ${CAND_SRC[$i]} " in *" $2 "*) ;; *) CAND_SRC[i]="${CAND_SRC[$i]} $2" ;; esac
            return 0
        fi
    done
    CAND_DIR+=("$d"); CAND_SRC+=("$2")
    return 0
}
# install_health DIR: healthy | unhealthy | partial | stopped | unknown (from the "docker ps -a" snapshot, no compose calls)
install_health() {
    local d="$1" proj cf svcs s l st ok=0 bad=0 total=0 hit
    [ "$DK_UP" = 1 ] || { printf unknown; return 0; }
    proj="$(env_read "$d/.env" COMPOSE_PROJECT_NAME)"; proj="${proj:-oa-agent}"
    cf="$(env_read "$d/.env" COMPOSE_FILE)"
    svcs="api worker"
    case ":$cf:" in *:docker-compose.external-db.yml:*) ;; *) svcs="postgres $svcs" ;; esac
    case ":$cf:" in *:docker-compose.https.yml:*) svcs="$svcs caddy" ;; esac
    for s in $svcs; do
        total=$((total + 1)); hit=0
        for l in ${CT_LINES[@]+"${CT_LINES[@]}"}; do
            [ "$(ct_field "$l" 4)" = "$proj" ] && [ "$(ct_field "$l" 7)" = "$s" ] || continue
            hit=1; st="$(ct_field "$l" 8)"
            if [ "$(ct_field "$l" 3)" != running ]; then bad=$((bad + 1))
            else case "$st" in *unhealthy* | *"health: starting"*) bad=$((bad + 1)) ;; *) ok=$((ok + 1)) ;; esac; fi
            break
        done
        : "$hit"
    done
    if [ "$ok" = "$total" ]; then printf healthy
    elif [ "$ok" = 0 ] && [ "$bad" = 0 ]; then printf stopped
    elif [ "$bad" -gt 0 ]; then printf unhealthy
    else printf partial; fi
}
find_installs() {
    CAND_DIR=(); CAND_SRC=(); IN_DIR=(); IN_SRC=(); IN_VER=(); IN_PROJ=(); IN_PORT=(); IN_DOM=(); IN_DBM=(); IN_REG=(); IN_HEALTH=(); IN_COMPOSE=(); DANGLING=()
    local l d st i p cf cj
    while IFS= read -r l; do
        [ -n "$l" ] || continue
        st="$(state_get "$l" status)"
        if [ "$st" != removed ]; then in_add_candidate "$(state_get "$l" dir)" state; fi
    done < <(state_entries)
    if [ -f "$ETC_MARKER" ]; then in_add_candidate "$(head -n 1 "$ETC_MARKER" 2>/dev/null || true)" marker; fi
    in_add_candidate "$DEFAULT_DIR" default
    if [ "$DIR_SET" = 1 ]; then in_add_candidate "$DIR" requested; fi
    for l in ${CT_LINES[@]+"${CT_LINES[@]}"}; do
        case "$(ct_field "$l" 2)" in */oa-agent-api:* | */oa-agent-worker:* | oa-agent-api:* | oa-agent-worker:* | */oa-agent-api | */oa-agent-worker) in_add_candidate "$(ct_field "$l" 5)" container ;; esac
    done
    if [ "$DK_UP" = 1 ]; then
        cj="$(docker compose ls -a --format json 2>/dev/null || true)"
        while IFS= read -r d; do in_add_candidate "$(dirname -- "$d")" compose-ls; done < <(printf '%s' "$cj" | grep -oE '"ConfigFiles":"[^"]*"' | sed 's/^"ConfigFiles":"//; s/"$//' | tr ',' '\n' | grep -E '/docker-compose\.yml$' || true)
    fi
    for i in "${!CAND_DIR[@]}"; do
        d="${CAND_DIR[$i]}"
        if [ -f "$d/.env" ] && grep -q '^OA_SECRET_KEY=' "$d/.env" 2>/dev/null; then
            IN_DIR+=("$d"); IN_SRC+=("${CAND_SRC[$i]# }")
            IN_VER+=("$(env_read "$d/.env" OA_VERSION)")
            p="$(env_read "$d/.env" COMPOSE_PROJECT_NAME)"; IN_PROJ+=("${p:-oa-agent}")
            IN_PORT+=("$(env_read "$d/.env" OA_PORT)"); IN_DOM+=("$(env_read "$d/.env" OA_DOMAIN)")
            cf="$(env_read "$d/.env" COMPOSE_FILE)"; IN_COMPOSE+=("$cf")
            case ":$cf:" in *:docker-compose.external-db.yml:*) IN_DBM+=("external:$(env_read "$d/.env" OA_DB_PROVIDER)") ;; *) IN_DBM+=(bundled) ;; esac
            IN_REG+=("$(env_read "$d/.env" OA_IMAGE_REGISTRY)")
            IN_HEALTH+=("$(install_health "$d")")
        else
            case " ${CAND_SRC[$i]} " in *" state "* | *" marker "* | *" container "* | *" compose-ls "* | *" requested "*) if [ -d "$d" ] || [ " ${CAND_SRC[$i]} " != " requested " ]; then DANGLING+=("$d|${CAND_SRC[$i]# }"); fi ;; esac
        fi
    done
    return 0
}
install_index() { # install_index DIR -> index in IN_DIR or -1
    local i
    for i in "${!IN_DIR[@]}"; do if [ "${IN_DIR[$i]}" = "${1%/}" ]; then printf '%s' "$i"; return 0; fi; done
    printf '%s' -1
}
det_installs() {
    local i h hs dl
    if [ "${#IN_DIR[@]}" -eq 0 ] && [ "${#DANGLING[@]}" -eq 0 ]; then
        pf_pass "OA Agent：本机未发现已有安装（已查：状态文件、容器标签/镜像名、compose 项目、$DEFAULT_DIR）-> 全新安装" "OA Agent: no existing installation found (checked the state file, container labels/images, compose projects and $DEFAULT_DIR) -> fresh install"
        return 0
    fi
    for i in "${!IN_DIR[@]}"; do
        h="${IN_HEALTH[$i]}"
        case "$h" in healthy) hs="$(m '运行正常' 'running, healthy')" ;; unhealthy) hs="$(m '运行中但有服务不健康' 'running, some service unhealthy')" ;; partial) hs="$(m '部分服务未运行' 'partly running')" ;; stopped) hs="$(m '已停止' 'stopped')" ;; *) hs="$(m '状态未知（Docker 未运行）' 'state unknown (Docker is not running)')" ;; esac
        pf_found "OA Agent 已安装：${IN_DIR[$i]}（项目 ${IN_PROJ[$i]}，标签 ${IN_VER[$i]:-?}，数据库 ${IN_DBM[$i]}，端口 ${IN_PORT[$i]:-?}，$hs；发现依据：${IN_SRC[$i]}）" "OA Agent installed: ${IN_DIR[$i]} (project ${IN_PROJ[$i]}, tag ${IN_VER[$i]:-?}, database ${IN_DBM[$i]}, port ${IN_PORT[$i]:-?}, $hs; found via: ${IN_SRC[$i]})"
    done
    for dl in ${DANGLING[@]+"${DANGLING[@]}"}; do
        pf_todo "记录/容器指向 ${dl%%|*}（依据：${dl#*|}），但该目录没有可用的 .env：安装被移动或删除了？不会碰它" "${dl%%|*} is referenced (via ${dl#*|}) but has no usable .env: moved or deleted? It is left untouched"
    done
    return 0
}

# ---------------------------------------------------------------------------------------------- detection: orphaned data, database servers, ports
ORPH_KEY=''; ORPH_PG=''; ORPH_ARCH=0; ORPH_BACKUP=''
# orphan_scan DIR APP PG ARCHIVE: evidence of an earlier installation's data (secret.key, database files, archive files, backups)
orphan_scan() {
    ORPH_KEY=''; ORPH_PG=''; ORPH_ARCH=0; ORPH_BACKUP=''
    local d="$1" app="$2" pg="$3" arch="$4" b
    if [ -f "$app/secret.key" ]; then ORPH_KEY="$app/secret.key"; fi
    if [ -f "$pg/pgdata/PG_VERSION" ]; then ORPH_PG="$pg/pgdata"; elif [ -f "$pg/PG_VERSION" ]; then ORPH_PG="$pg"; fi
    if [ -d "$arch" ] && [ -n "$(find "$arch" -mindepth 1 -maxdepth 2 -print -quit 2>/dev/null || true)" ]; then ORPH_ARCH=1; fi
    b="$(ls -1t "$d"/backups/oa-agent-backup-*.tar.gz 2>/dev/null | head -n 1 || true)"
    if [ -n "$b" ] && tar -tzf "$b" 2>/dev/null | grep -qx './env'; then ORPH_BACKUP="$b"; fi
    return 0
}
orphan_found() { [ -n "$ORPH_KEY" ] || [ -n "$ORPH_PG" ] || [ "$ORPH_ARCH" = 1 ]; }
det_orphans() { # det_orphans DIR: default-layout dirs under DIR (informational; the real gate runs with the final directories)
    local d="$1"
    [ ! -f "$d/.env" ] || return 0
    orphan_scan "$d" "$d/data/app" "$d/data/pg" "$d/data/archive"
    orphan_found || return 0
    pf_todo "发现无 .env 的旧数据（$d）：${ORPH_KEY:+密钥文件 $ORPH_KEY；}${ORPH_PG:+数据库文件 $ORPH_PG；}${ORPH_ARCH:+归档目录非空；}${ORPH_BACKUP:+有可恢复 .env 的备份 $(basename "$ORPH_BACKUP")}-> 绝不会生成新密钥覆盖旧数据：将沿用旧密钥、从备份恢复 .env，或拒绝并说明" \
        "Data of an earlier installation without .env found in $d: ${ORPH_KEY:+key file $ORPH_KEY; }${ORPH_PG:+database files $ORPH_PG; }${ORPH_ARCH:+archive directory is not empty; }${ORPH_BACKUP:+backup with a recoverable .env: $(basename "$ORPH_BACKUP") }-> a new key is never generated over old data: the old key is reused, .env restored from a backup, or the installer refuses with an explanation"
    return 0
}

DBS_FOUND=()   # engine|host|port|description of database servers seen on this host (never connected to)
det_db_servers() {
    DBS_FOUND=()
    local spec port label o name bind c l img who any=0 desc unit proj own i oa_projects=' '
    for l in ${CT_LINES[@]+"${CT_LINES[@]}"}; do
        case "$(ct_field "$l" 2)" in */oa-agent-api:* | */oa-agent-worker:* | oa-agent-api:* | oa-agent-worker:*) oa_projects="$oa_projects$(ct_field "$l" 4) " ;; esac
    done
    for spec in '5432|PostgreSQL|postgres' '3306|MySQL/MariaDB|mysql' '1433|SQL Server|mssql'; do
        port="${spec%%|*}"; label="${spec#*|}"; label="${label%%|*}"
        port_in_use "$port" || continue
        any=1
        o="$(port_owner "$port")"; name="${o%%|*}"
        bind="$(port_bind "$port")"
        c="$(port_container "$port")"; who="${name:-?}"; [ -z "$c" ] || who="docker ${c%%|*}"
        case "$bind" in
            127.0.0.1* | localhost* | '[::1]'*) desc="$(m "仅监听本机回环（容器访问不到：需让它监听 docker 网桥地址或内网地址，或把 OA 放到同一网络）" "listening on loopback only (containers cannot reach it: make it listen on the docker bridge or a LAN address)")" ;;
            *) desc="$(m "监听 ${bind:-所有地址}（容器可经 host.docker.internal 或本机内网地址访问）" "listening on ${bind:-all addresses} (containers reach it through host.docker.internal or the LAN address)")" ;;
        esac
        DBS_FOUND+=("${spec##*|}|${bind:-}|$port|$who")
        pf_found "已有数据库服务：$label，端口 $port（进程 $who；$desc）-> 可作为「使用已有数据库」的候选；检测阶段不连接、不改动、不尝试任何凭据" "Existing database server: $label on port $port (process $who; $desc) -> a candidate for \"use an existing database\"; detection never connects to it, changes it or tries credentials"
    done
    for l in ${CT_LINES[@]+"${CT_LINES[@]}"}; do
        img="$(ct_field "$l" 2)"
        case "$img" in postgres:* | */postgres:* | postgres | mysql:* | */mysql:* | mariadb:* | */mariadb:* | mcr.microsoft.com/mssql/* ) ;; *) continue ;; esac
        # the bundled PostgreSQL of an OA Agent installation is not a candidate
        proj="$(ct_field "$l" 4)"; own=0
        for i in "${!IN_PROJ[@]}"; do if [ "${IN_PROJ[$i]}" = "$proj" ]; then own=1; fi; done
        case "$oa_projects" in *" $proj "*) own=1 ;; esac
        if [ -n "$proj" ] && [ "$own" = 1 ]; then continue; fi
        any=1
        pf_found "已有数据库容器：$(ct_field "$l" 1)（镜像 $img，$(ct_field "$l" 3)，端口 $(ct_field "$l" 6)）-> 同样只是候选，不会被改动" "Existing database container: $(ct_field "$l" 1) (image $img, $(ct_field "$l" 3), ports $(ct_field "$l" 6)) -> a candidate only, never modified"
    done
    if [ "$SYSTEMD_OK" = 1 ]; then
        unit="$(systemctl list-unit-files --no-legend 2>/dev/null | awk '{print $1}' | grep -E '^(postgresql|mysql|mysqld|mariadb|mssql-server)([@.-]|$)' | tr '\n' ' ' || true)"
        if [ -n "${unit// /}" ]; then any=1; pf_found "已安装的数据库服务单元：${unit}-> 仅供参考" "Database service units installed: ${unit}-> for information only"; fi
    fi
    [ "$any" = 1 ] || pf_pass "未在本机发现其他数据库服务（5432/3306/1433 空闲，无数据库容器）" "No other database server found on this host (5432/3306/1433 free, no database containers)"
    return 0
}

det_ports() { # who holds the ports OA would like (informational; the gate checks the final choice)
    local p free=''
    for p in 80 443 "${PORT:-8080}"; do
        if port_in_use "$p"; then
            pf_found "端口 $p 已被占用：$(port_describe "$p") -> 向导会建议空闲端口；80/443 被反向代理占用时会提供「放在它后面」的配置片段" "Port $p is in use by $(port_describe "$p") -> the wizard suggests a free port; when a reverse proxy holds 80/443 a ready-made \"behind it\" configuration is offered"
        else free="$free$p "; fi
    done
    [ -z "$free" ] || pf_pass "端口空闲：${free}" "Ports free: ${free}"
    return 0
}

det_firewall() {
    if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -qi '^Status: active'; then
        pf_info "防火墙 ufw 已启用：外部访问需要放行端口（安装结束时会询问，不会擅自修改）。" "Firewall ufw is active: open the port for external access (asked at the end, never changed on its own)."
    elif command -v firewall-cmd >/dev/null 2>&1 && firewall-cmd --state >/dev/null 2>&1; then
        pf_info "防火墙 firewalld 已启用：外部访问需要放行端口（安装结束时会询问，不会擅自修改）。" "Firewall firewalld is active: open the port for external access (asked at the end, never changed on its own)."
    elif command -v iptables >/dev/null 2>&1 && [ "$(id -u)" -eq 0 ] && [ "$(iptables -S INPUT 2>/dev/null | head -n 1 || true)" = "-P INPUT DROP" ]; then
        pf_info "iptables 的 INPUT 默认策略是 DROP：外部访问需要自行放行端口（本脚本不改 iptables）。" "iptables INPUT policy is DROP: open the port yourself for external access (this installer never edits iptables)."
    fi
    return 0
}

# ---------------------------------------------------------------------------------------------- detection: network (only when something is going to be pulled)
MIRROR_HINT=''; MIRROR_HINT_WHY=''
hub_reachable() {
    local code
    have_cmd curl || return 2
    code="$(curl -sS -o /dev/null -m 4 --connect-timeout 3 -w '%{http_code}' https://registry-1.docker.io/v2/ 2>/dev/null || true)"
    [[ "$code" =~ ^[1-5][0-9][0-9]$ ]]
}
det_network() {
    local hub=1
    [ -z "$OFFLINE_FILE" ] || { pf_info "离线模式：不探测网络。" "Offline mode: the network is not probed."; return 0; }
    have_cmd curl || { pf_info "没有 curl：跳过网络探测。" "No curl: network probes skipped."; return 0; }
    hub_reachable || hub=0
    if [ "$hub" = 1 ]; then
        pf_pass "Docker Hub 可访问（postgres/caddy 基础镜像可直接拉取）" "Docker Hub is reachable (the postgres/caddy base images can be pulled directly)"
    else
        MIRROR_HINT=cn; MIRROR_HINT_WHY="$(m '本机无法访问 Docker Hub（4 秒内无响应）' 'Docker Hub did not answer from this host within 4 seconds')"
        pf_info "Docker Hub 不可达（4 秒内无响应）：很可能在受限网络（如中国大陆）。镜像源问题的默认答案会预选 cn，由你确认；不会静默更改任何设置。" "Docker Hub is not reachable (no answer within 4 seconds): probably a restricted network (e.g. mainland China). The default answer of the mirror question will be cn, for you to confirm; nothing is changed silently."
    fi
    pf_registry
    return 0
}

# ---------------------------------------------------------------------------------------------- detection driver
detect_summary() {
    printf '%s\n' "$(m "检测汇总：通过 $DET_PASS，已存在 $DET_FOUND，缺少 $DET_MISS，需处理 $DET_TODO，警告 $PF_WARN，失败 $PF_FAIL" "Detection summary: $DET_PASS passed, $DET_FOUND found, $DET_MISS missing, $DET_TODO need action, $PF_WARN warning(s), $PF_FAIL failed")"
}
# detect_local: everything that needs no network; read-only. Prints the component table.
detect_local() {
    step "检测：这台服务器上已经有什么" "Detection: what is already on this server"
    detect_os
    pf_system
    det_systemd
    det_prereqs
    det_docker
    det_docker_space
    load_containers
    find_installs
    det_installs
    det_orphans "$DIR"
    det_ports
    det_db_servers
    det_firewall
    detect_summary
}

# ---------------------------------------------------------------------------------------------- answers: config file, environment, print-config
# Precedence: command-line flags > OA_INSTALL_<KEY> environment variables > --config file > wizard answers > built-in defaults.
# Settings from the environment/config file are "seeded": the wizard does not ask them again (they appear in the review screen).
CFG_KEYS='lang geo_lookup docker_source docker_mirrors dir port domain email mirror image_tag registry project archive_dir spool_dir app_dir pg_dir extra_mount behind_proxy db_mode db_provider db_host db_port db_name db_user db_password db_password_file db_tls db_ca_file db_schema secret_key_file systemd open_firewall install_docker'
SECRET_KEY_FILE=''
seeded() { case "$SEEDED" in *" $1 "*) return 0 ;; *) return 1 ;; esac; }
# cfg_assign KEY VALUE SOURCE: take VALUE for KEY unless a higher-precedence source already set it
cfg_assign() {
    local k="$1" v="$2" src="$3"
    case "$k" in
        lang) [ "$LANG_SET" = 0 ] || return 0; case "$v" in zh* | ZH*) UI=zh ;; en* | EN*) UI=en ;; *) die_usage "$src: lang 只能是 zh 或 en" "$src: lang must be zh or en" ;; esac; LANG_SET=1 ;;
        geo_lookup) case "$v" in no | off | false | 0) GEO=off ;; yes | on | true | 1) ;; *) die_usage "$src: geo_lookup 只能是 yes/no" "$src: geo_lookup must be yes or no" ;; esac ;;
        docker_source) [ -z "$DOCKER_SOURCE" ] || return 0; valid_docker_source "$v" || die_usage "$src: docker_source 不合法: $v" "$src: invalid docker_source: $v"; DOCKER_SOURCE="${v%/}" ;;
        docker_mirrors) [ "${#DOCKER_MIRRORS[@]}" -eq 0 ] || return 0; local mu; for mu in $v; do valid_mirror_url "$mu" || die_usage "$src: docker_mirrors 含非法地址: $mu" "$src: docker_mirrors contains an invalid URL: $mu"; DOCKER_MIRRORS+=("${mu%/}"); done ;;
        dir) [ "$DIR_SET" = 0 ] || return 0; DIR="$v"; DIR_SET=1 ;;
        port) [ "$PORT_SET" = 0 ] || return 0; PORT="$v"; PORT_SET=1 ;;
        project) [ "$PROJECT_SET" = 0 ] || return 0; PROJECT="$v"; PROJECT_SET=1 ;;
        domain) [ -z "$DOMAIN" ] || return 0; DOMAIN="$v" ;;
        email) [ -z "$EMAIL" ] || return 0; EMAIL="$v" ;;
        mirror) [ -z "$MIRROR" ] || return 0; MIRROR="$v" ;;
        image_tag) [ "$VERSION_SET" = 0 ] || return 0; VERSION="$v"; VERSION_SET=1 ;;
        registry) [ "$REGISTRY_SET" = 0 ] || return 0; REGISTRY="$v"; REGISTRY_SET=1 ;;
        archive_dir) [ -z "$ARCHIVE_DIR" ] || return 0; ARCHIVE_DIR="$v" ;;
        spool_dir) [ -z "$SPOOL_DIR" ] || return 0; SPOOL_DIR="$v" ;;
        app_dir) [ -z "$APP_DIR" ] || return 0; APP_DIR="$v" ;;
        pg_dir) [ -z "$PG_DIR" ] || return 0; PG_DIR="$v" ;;
        extra_mount) [ "$CFG_EXTRA_FROM_FLAGS" = 0 ] || return 0; EXTRA_MOUNTS+=("$v") ;;
        behind_proxy) [ "$PROXY_MODE" = 0 ] || return 0; case "$v" in 1 | yes | true | on) PROXY_MODE=1 ;; 0 | no | false | off) PROXY_MODE=0 ;; *) die_usage "$src: behind_proxy 只能是 yes/no" "$src: behind_proxy must be yes or no" ;; esac ;;
        db_mode)
            [ -z "$DB_MODE" ] || return 0
            case "$v" in bundled) DB_MODE=bundled ;; external) DB_MODE=external ;; postgres | mysql | mssql) DB_MODE=external; DB_PROVIDER="$v" ;; *) die_usage "$src: db_mode 只能是 bundled/external（或 postgres/mysql/mssql）" "$src: db_mode must be bundled/external (or postgres/mysql/mssql)" ;; esac ;;
        db_provider) [ -z "$DB_PROVIDER" ] || return 0; DB_PROVIDER="$v" ;;
        db_host) [ -z "$DB_HOST" ] || return 0; DB_HOST="$v" ;;
        db_port) [ -z "$DB_PORT" ] || return 0; DB_PORT="$v" ;;
        db_name) [ -z "$DB_NAME" ] || return 0; DB_NAME="$v" ;;
        db_user) [ -z "$DB_USER" ] || return 0; DB_USER="$v" ;;
        db_tls) [ -z "$DB_TLS" ] || return 0; DB_TLS="$v" ;;
        db_ca_file) [ -z "$DB_CA_FILE" ] || return 0; DB_CA_FILE="$v" ;;
        db_schema) [ -z "$DB_SCHEMA" ] || return 0; DB_SCHEMA="$v" ;;
        db_password_file) [ -z "$DB_PASSWORD_FILE" ] || return 0; DB_PASSWORD_FILE="$v" ;;
        secret_key_file) [ -z "$SECRET_KEY_FILE" ] || return 0; SECRET_KEY_FILE="$v" ;;
        db_password)
            [ "$src" = config ] || die_usage "密码只能写在 --config 文件里（权限 600），不能放进命令行或环境变量。" "Passwords are accepted only inside the --config file (mode 600), never on the command line or in the environment."
            [ -z "$DB_PASSWORD" ] || return 0; DB_PASSWORD="$v" ;;
        systemd) [ "$SYSTEMD" = ask ] || return 0; case "$v" in yes | true | 1) SYSTEMD=yes ;; no | false | 0) SYSTEMD=no ;; *) die_usage "$src: systemd 只能是 yes/no" "$src: systemd must be yes or no" ;; esac ;;
        open_firewall) [ "$OPEN_FW" = ask ] || return 0; case "$v" in yes | true | 1) OPEN_FW=yes ;; no | false | 0) OPEN_FW=no ;; *) die_usage "$src: open_firewall 只能是 yes/no" "$src: open_firewall must be yes or no" ;; esac ;;
        install_docker) [ "$INSTALL_DOCKER" = 0 ] || return 0; case "$v" in yes | true | 1) INSTALL_DOCKER=1; INSTALL_PREREQS=1 ;; no | false | 0) ;; *) die_usage "$src: install_docker 只能是 yes/no" "$src: install_docker must be yes or no" ;; esac ;;
        *) return 0 ;;
    esac
    SEEDED="$SEEDED$k "
    return 0
}
CFG_EXTRA_FROM_FLAGS=0
# check_secret_file FILE LABEL: refuse files other users can read (secrets live in them) or that are not plain files
check_secret_file() {
    local f="$1" mode owner
    [ -f "$f" ] && [ ! -L "$f" ] || die_usage "$2: $f 不存在或不是普通文件（不接受符号链接）" "$2: $f does not exist or is not a regular file (symlinks are refused)"
    mode="$(stat -c %a "$f" 2>/dev/null || echo 777)"
    owner="$(stat -c %u "$f" 2>/dev/null || echo -1)"
    if [ $((8#$mode & 8#077)) -ne 0 ]; then
        die_usage "$2: $f 的权限是 $mode，其他用户可读写；密码/密钥文件必须是 600（chmod 600 $f）。" "$2: $f has mode $mode, readable by other users; files that can hold secrets must be mode 600 (chmod 600 $f)."
    fi
    if [ "$owner" != 0 ] && [ "$owner" != "$(id -u)" ]; then
        die_usage "$2: $f 的属主既不是 root 也不是当前用户。" "$2: $f is owned by neither root nor the current user."
    fi
}
load_config_file() {
    [ -n "$CONFIG_FILE" ] || return 0
    check_secret_file "$CONFIG_FILE" "--config"
    local line k v lineno=0
    while IFS= read -r line || [ -n "$line" ]; do
        lineno=$((lineno + 1)); line="${line%$'\r'}"
        line="${line#"${line%%[![:space:]]*}"}"
        case "$line" in '' | '#'*) continue ;; esac
        [[ "$line" =~ ^([a-z_]+)[[:space:]]*=[[:space:]]*(.*)$ ]] || die_usage "$CONFIG_FILE 第 $lineno 行格式应为 key=value" "$CONFIG_FILE line $lineno: expected key=value"
        k="${BASH_REMATCH[1]}"; v="${BASH_REMATCH[2]}"
        v="${v%"${v##*[![:space:]]}"}"
        case "$v" in \"*\") v="${v#\"}"; v="${v%\"}" ;; \'*\') v="${v#\'}"; v="${v%\'}" ;; esac
        case " $CFG_KEYS " in *" $k "*) ;; *) die_usage "$CONFIG_FILE 第 $lineno 行：未知的键 $k" "$CONFIG_FILE line $lineno: unknown key $k" ;; esac
        [ -n "$v" ] || continue
        cfg_assign "$k" "$v" config
    done <"$CONFIG_FILE"
    logf "config file loaded: $CONFIG_FILE"
}
load_env_answers() {
    local k up v
    [ "${#EXTRA_MOUNTS[@]}" -eq 0 ] || CFG_EXTRA_FROM_FLAGS=1
    case "${OA_INSTALL_GEO:-}" in off | no | false | 0) GEO=off ;; esac   # OA_INSTALL_GEO=off: no ipinfo.io lookup, local signals only
    for k in $CFG_KEYS; do
        up="OA_INSTALL_$(printf '%s' "$k" | tr '[:lower:]' '[:upper:]')"
        v="${!up:-}"
        [ -n "$v" ] || continue
        if [ "$k" = db_password ]; then warn "忽略环境变量 $up：密码只接受 --config 文件。" "Ignoring $up: passwords are accepted only from the --config file."; continue; fi
        cfg_assign "$k" "$v" env
    done
}
# db_password_value: the external database password (config file value, or the first line of db_password_file)
db_password_value() {
    if [ -n "$DB_PASSWORD" ]; then printf '%s' "$DB_PASSWORD"; return 0; fi
    if [ -n "$DB_PASSWORD_FILE" ]; then check_secret_file "$DB_PASSWORD_FILE" "db_password_file"; head -n 1 "$DB_PASSWORD_FILE" | tr -d '\r'; return 0; fi
}

# print_config: the answers WITHOUT secrets, as a --config file (written to the original stdout, descriptor 3)
print_config() {
    local i
    {
        printf '# OA Agent installer answers, written by install.sh --print-config. Contains NO secrets.\n'
        printf '# Reuse on another server:  sudo bash install.sh --config THIS_FILE   (chmod 600 THIS_FILE; the file may also hold db_password=...)\n'
        printf 'lang=%s\n' "$UI"
        [ -z "$DOCKER_SOURCE" ] || printf 'docker_source=%s\n' "$DOCKER_SOURCE"
        [ "${#DOCKER_MIRRORS[@]}" -eq 0 ] || printf 'docker_mirrors=%s\n' "${DOCKER_MIRRORS[*]}"
        printf 'dir=%s\nport=%s\n' "$DIR" "$PORT"
        [ -z "$DOMAIN" ] || printf 'domain=%s\n' "$DOMAIN"
        [ -z "$EMAIL" ] || printf 'email=%s\n' "$EMAIL"
        [ "$PROXY_MODE" = 0 ] || printf 'behind_proxy=yes\n'
        printf 'mirror=%s\n' "${MIRROR:-default}"
        printf 'image_tag=%s\n' "$VERSION"
        if [ -n "$REGISTRY" ] && [ "$REGISTRY_SET" = 1 ]; then printf 'registry=%s\n' "$REGISTRY"; fi
        [ "$PROJECT_SET" = 0 ] || printf 'project=%s\n' "$PROJECT"
        printf 'archive_dir=%s\nspool_dir=%s\napp_dir=%s\npg_dir=%s\n' "$ARCHIVE_DIR" "$SPOOL_DIR" "$APP_DIR" "$PG_DIR"
        for i in "${!EX_NAME[@]}"; do printf 'extra_mount=%s=%s:%s\n' "${EX_NAME[$i]}" "${EX_HOST[$i]}" "${EX_RO[$i]}"; done
        if [ "$DB_MODE" = external ]; then
            printf 'db_mode=external\ndb_provider=%s\ndb_host=%s\ndb_port=%s\ndb_name=%s\ndb_user=%s\ndb_tls=%s\n' "$DB_PROVIDER" "$DB_HOST" "$DB_PORT" "$DB_NAME" "$DB_USER" "${DB_TLS:-require}"
            [ -z "$DB_CA_FILE" ] || printf 'db_ca_file=%s\n' "$DB_CA_FILE"
            [ -z "$DB_SCHEMA" ] || printf 'db_schema=%s\n' "$DB_SCHEMA"
            printf '# db_password is deliberately NOT written: add db_password=... to your copy of this file (mode 600) or use db_password_file=/path\n'
        else
            printf 'db_mode=bundled\n'
        fi
    } >&3
}

# ---------------------------------------------------------------------------------------------- wizard: validators and secret input
chk_tag() { [[ "$1" =~ ^[A-Za-z0-9._-]{1,64}$ ]] || { m "标签只能含字母、数字和 . _ -（最长 64 个字符）" "the tag may contain letters, digits and . _ - (at most 64 characters)"; return 1; }; }
chk_db_host() {
    [[ "$1" =~ ^[A-Za-z0-9]([A-Za-z0-9._-]*[A-Za-z0-9])?$ ]] || { m "主机名/IPv4 地址格式不正确" "invalid host name / IPv4 address"; return 1; }
    case "$1" in
        localhost | 127.* | 0.0.0.0) m "不能填 $1：数据库客户端运行在容器里，那里的 localhost 指向容器自己。数据库在本机时请填 host.docker.internal 或本机内网地址（数据库须监听该地址，不能只监听 127.0.0.1）" "do not use $1: the database client runs in a container where localhost is the container itself. For a database on this host use host.docker.internal or the host's LAN address (the database must listen on it, not only on 127.0.0.1)"; return 1 ;;
    esac
}
chk_db_port() { valid_port "$1" || { m "端口须为 1-65535 的数字" "the port must be a number from 1 to 65535"; return 1; }; }
chk_db_ident() { [[ "$1" =~ ^[A-Za-z0-9_.@-]{1,64}$ ]] || { m "只能含字母、数字和 _ . @ -（最长 64 个字符）" "letters, digits and _ . @ - only (at most 64 characters)"; return 1; }; }
chk_db_tls() { case "$1" in disable | require | verify-ca | verify-full) ;; *) m "只能是 disable、require、verify-ca 或 verify-full" "must be disable, require, verify-ca or verify-full"; return 1 ;; esac; }
chk_db_ca() {
    [ -z "$1" ] && return 0
    [ -f "$1" ] && [ -r "$1" ] || { m "找不到可读的 CA 文件: $1" "no readable CA file: $1"; return 1; }
    head -c 4096 "$1" | grep -q -- '-----BEGIN CERTIFICATE-----' || { m "不是 PEM 证书（缺少 BEGIN CERTIFICATE）" "not a PEM certificate (BEGIN CERTIFICATE missing)"; return 1; }
}
chk_db_password() {
    [ -n "$1" ] || { m "密码不能为空" "the password must not be empty"; return 1; }
    case "$1" in *"'"* | *[[:space:]]*) m "密码不能含空格或单引号 '（.env 文件的写法限制）" "the password must not contain spaces or the single quote ' (a limitation of the .env file format)"; return 1 ;; esac
    if printf '%s' "$1" | LC_ALL=C grep -q '[^!-~]'; then m "密码只能含可打印的 ASCII 字符" "the password may contain printable ASCII characters only"; return 1; fi
}
chk_choice_1_4() { case "$1" in 1 | 2 | 3 | 4) ;; *) m "请输入 1-4" "enter 1-4"; return 1 ;; esac; }
# ask_secret VAR "中文" "English": hidden input read from the terminal (never echoed, never an argument); Enter keeps the current value
ask_secret() {
    local var="$1" prompt ans='' cur="${!1:-}" hint=''
    prompt="$(m "$2" "$3")"
    if [ "$ASSUME_YES" = 1 ] || ! have_tty; then return 0; fi
    [ -z "$cur" ] || hint=" [$(m '回车保留已输入的' 'Enter keeps the current one')]"
    printf '%s%s: ' "$prompt" "$hint" >&2
    IFS= read -rs ans <&9 || ans=''
    printf '\n' >&2
    if [ -n "$ans" ]; then printf -v "$var" '%s' "$ans"; fi
    return 0
}
db_default_port() { case "$1" in postgres) echo 5432 ;; mysql) echo 3306 ;; mssql) echo 1433 ;; *) echo '' ;; esac; }
db_label() { case "$1" in postgres) echo "PostgreSQL" ;; mysql) echo "MySQL" ;; mssql) echo "SQL Server" ;; *) echo "$1" ;; esac; }
mask_pw() { if [ -n "$(db_password_value)" ]; then printf '********'; else printf '%s' "$(m '（未设置）' '(not set)')"; fi; }

# ---------------------------------------------------------------------------------------------- wizard: the questions
# Every question works for both modes: interactively it asks (the current value is the default); with --yes / no terminal it
# keeps the value from flags/config/defaults and only validates it.
q_dir() {
    ask_valid DIR "安装目录（程序、配置 .env 与 mounts.json）" "Install directory (program, .env and mounts.json)" "$DIR" chk_new_install_dir
    DIR="${DIR%/}"
}
q_port() {
    if [ "$PORT_SET" = 0 ] && port_in_use "$PORT"; then
        local np; np="$(next_free_port "$PORT")"
        say "默认端口 $PORT 已被占用（$(port_describe "$PORT")），建议改用空闲的 $np。" "The default port $PORT is in use ($(port_describe "$PORT")); suggesting the free port $np."
        PORT="$np"
    fi
    ask_valid PORT "HTTP 端口" "HTTP port" "$PORT" chk_port
}
q_expose() {
    local proxy c='' interactive=0
    if [ "$ASSUME_YES" != 1 ] && have_tty; then interactive=1; fi
    ask_valid DOMAIN "域名（留空 = 仅 HTTP，无 Passkey）" "Domain (empty = HTTP only, no Passkeys)" "$DOMAIN" chk_domain
    if [ "$interactive" = 1 ] && [ -n "$DOMAIN" ] && [ "$PROXY_MODE" = 0 ] && { port_in_use 80 || port_in_use 443; }; then
        proxy="$(proxy_owner_of_web)"
        say "80/443 已被占用（80: $(port_in_use 80 && port_describe 80 || echo '-')；443: $(port_in_use 443 && port_describe 443 || echo '-')），内置的 Caddy 无法申请证书。" \
            "Port 80/443 is taken (80: $(port_in_use 80 && port_describe 80 || echo '-'); 443: $(port_in_use 443 && port_describe 443 || echo '-')), so the bundled Caddy cannot obtain a certificate."
        say "  1) 放在已有反向代理后面（推荐；安装结束会打印 ${proxy:-nginx/Caddy} 的配置片段，OA 只监听 127.0.0.1:$PORT）" "  1) put OA behind the existing reverse proxy (recommended; a ready-made ${proxy:-nginx/Caddy} snippet is printed at the end, OA listens on 127.0.0.1:$PORT only)"
        say "  2) 忽略域名，只用 HTTP 端口 $PORT（无 Passkey）" "  2) ignore the domain and use plain HTTP on port $PORT (no Passkeys)"
        say "  3) 仍然使用内置 Caddy（预检会因端口占用失败，除非你先停掉占用者）" "  3) still use the bundled Caddy (the pre-flight will fail on the busy port unless you stop the other program first)"
        ask_valid c "选择 [1-3]" "Choice [1-3]" 1 chk_choice_1_3
        case "$c" in 1) PROXY_MODE=1 ;; 2) DOMAIN='' ;; esac
    fi
    if [ -n "$DOMAIN" ] && [ "$PROXY_MODE" = 0 ]; then ask_valid EMAIL "证书邮箱（可选）" "Certificate e-mail (optional)" "$EMAIL" chk_email; fi
    if [ "$PROXY_MODE" = 1 ] && [ -z "$DOMAIN" ]; then
        ask_valid DOMAIN "对外访问的域名（反向代理后面的主机名，用于 Passkey/链接）" "Public domain served by the reverse proxy (used for Passkeys and links)" "$DOMAIN" chk_domain_required
    fi
    return 0
}
chk_choice_1_3() { case "$1" in 1 | 2 | 3) ;; *) m "请输入 1-3" "enter 1-3"; return 1 ;; esac; }
chk_domain_required() { [ -n "$1" ] || { m "反向代理模式需要域名" "behind-proxy mode needs a domain"; return 1; }; chk_domain "$1"; }
q_db() {
    local c=1 hint='' d
    if [ "$ASSUME_YES" = 1 ] || ! have_tty; then
        [ -n "$DB_MODE" ] || DB_MODE=bundled
        [ "$DB_MODE" != external ] || q_db_conn
        return 0
    fi
    if [ -z "$DB_MODE" ] || ! seeded db_mode; then
        for d in ${DBS_FOUND[@]+"${DBS_FOUND[@]}"}; do hint="$hint ${d##*|}:${d%%|*}"; done
        say "数据库：" "Database:"
        say "  1) 使用内置 PostgreSQL（推荐：全新的独立服务器，无需额外准备）" "  1) Use the bundled PostgreSQL (recommended for a new standalone server, nothing to prepare)"
        say "  2) 使用已有的 PostgreSQL 14+ 服务器" "  2) Use an existing PostgreSQL 14+ server"
        say "  3) 使用已有的 MySQL 8.0+ 服务器（不支持 MariaDB）" "  3) Use an existing MySQL 8.0+ server (MariaDB is not supported)"
        say "  4) 使用已有的 SQL Server 2019+ 服务器" "  4) Use an existing SQL Server 2019+ server"
        [ -z "$hint" ] || say "  （本机检测到数据库服务：$hint；仅供参考，未连接过）" "  (database servers seen on this host:$hint; for information only, never connected to)"
        if [ "$DB_MODE" = external ]; then
            case "$DB_PROVIDER" in postgres) c=2 ;; mysql) c=3 ;; mssql) c=4 ;; esac
        fi
        ask_valid c "选择 [1-4]" "Choice [1-4]" "$c" chk_choice_1_4
        case "$c" in
            1) DB_MODE=bundled; DB_PROVIDER='' ;;
            2) DB_MODE=external; DB_PROVIDER=postgres ;;
            3) DB_MODE=external; DB_PROVIDER=mysql ;;
            4) DB_MODE=external; DB_PROVIDER=mssql ;;
        esac
    fi
    [ "$DB_MODE" = external ] || return 0
    q_db_conn
}
chk_db_provider() { case "$1" in postgres | mysql | mssql) ;; *) m "只能是 postgres、mysql 或 mssql" "must be postgres, mysql or mssql"; return 1 ;; esac; }
q_db_conn() {
    local pw_tries=0
    [ -n "$DB_PROVIDER" ] || ask_valid DB_PROVIDER "数据库类型 postgres/mysql/mssql" "Database type postgres/mysql/mssql" postgres chk_db_provider
    [ -n "$DB_PORT" ] || DB_PORT="$(db_default_port "$DB_PROVIDER")"
    if [ -z "$DB_HOST" ]; then
        local d
        for d in ${DBS_FOUND[@]+"${DBS_FOUND[@]}"}; do
            if [ "${d%%|*}" = "$DB_PROVIDER" ]; then DB_HOST=host.docker.internal; break; fi
        done
    fi
    ask_valid DB_HOST "$(db_label "$DB_PROVIDER") 主机（本机的数据库用 host.docker.internal 或内网地址，不能填 localhost）" "$(db_label "$DB_PROVIDER") host (for a database on this host use host.docker.internal or the LAN address, not localhost)" "$DB_HOST" chk_db_host
    ask_valid DB_PORT "端口" "Port" "$DB_PORT" chk_db_port
    ask_valid DB_NAME "数据库名（须是专用的空库，或只含 OA Agent 的表）" "Database name (must be dedicated: empty, or holding only OA Agent tables)" "${DB_NAME:-oa}" chk_db_ident
    ask_valid DB_USER "用户名" "User" "${DB_USER:-oa}" chk_db_ident
    ask_valid DB_TLS "TLS 模式 disable/require/verify-ca/verify-full（生产建议 verify-full）" "TLS mode disable/require/verify-ca/verify-full (verify-full recommended for production)" "${DB_TLS:-require}" chk_db_tls
    case "$DB_TLS" in verify-ca | verify-full) ask_valid DB_CA_FILE "CA 证书文件路径（PEM；留空 = 使用系统根证书）" "CA certificate file path (PEM; empty = system roots)" "$DB_CA_FILE" chk_db_ca ;; esac
    if [ -z "$DB_PASSWORD" ] && [ -z "$DB_PASSWORD_FILE" ]; then
        while :; do
            ask_secret DB_PASSWORD "数据库密码（输入不会回显）" "Database password (input is not echoed)"
            if [ "$ASSUME_YES" = 1 ] || ! have_tty; then break; fi
            if chk_db_password "$DB_PASSWORD" >/dev/null; then break; fi
            printf '%s[!]%s %s\n' "$C_R" "$C_0" "$(chk_db_password "$DB_PASSWORD" || true)" >&2
            DB_PASSWORD=''; pw_tries=$((pw_tries + 1))
            [ "$pw_tries" -lt 5 ] || die_usage "输入无效次数过多，已中止。" "Too many invalid answers, aborting."
        done
    fi
}
q_mirror() {
    local mdef=default why=''
    if { [ "$ASSUME_YES" = 1 ] || ! have_tty; } && [ -z "$MIRROR" ]; then
        # unattended: no question, so nothing is preselected silently; the detection only produces a hint
        [ -z "$MIRROR_HINT" ] || say "提示：$MIRROR_HINT_WHY；未指定 --mirror，使用 default。若在受限网络（如中国大陆）请加 --mirror cn。" "Hint: $MIRROR_HINT_WHY; no --mirror given, using default. On a restricted network (e.g. mainland China) add --mirror cn."
        return 0
    fi
    if [ -n "$MIRROR_HINT" ]; then mdef="$MIRROR_HINT"; why="$MIRROR_HINT_WHY"
    elif [ "$UI" = zh ]; then mdef=cn; fi
    if [ -z "$MIRROR" ] || ! seeded mirror; then
        [ -z "$why" ] || say "提示：$why，所以默认选 $mdef（可改）。" "Hint: $why, so the default is $mdef (change it if you like)."
        ask_valid MIRROR "镜像源 cn(中国大陆)/default" "Mirror cn(China)/default" "${MIRROR:-$mdef}" chk_mirror
    fi
}
q_tag() {
    [ -n "$REGISTRY" ] && [ -z "$OFFLINE_FILE" ] || return 0
    say "镜像标签：latest 会随新版本移动；生产环境建议固定 sha-<7位提交> 或版本号（如 1.2.3）。" "Image tag: latest moves with every release; for production pin sha-<7-char commit> or a version (e.g. 1.2.3)."
    ask_valid VERSION "镜像标签" "Image tag" "$VERSION" chk_tag
    VERSION_SET=1
}
q_storage() { collect_mounts; }
# q_docker: where Docker comes from (only when it has to be installed) and the Docker Hub registry mirror (daemon.json, never applied
# without consent). Unattended runs only validate what the flags / config gave.
q_docker() {
    local c='' def=8 u='' cur
    # not part of the questions of a normal run (the source is chosen automatically and announced); the review screen's setting 8 lands here
    [ -z "$OFFLINE_FILE" ] || return 0
    if [ "$ASSUME_YES" = 1 ] || ! have_tty; then return 0; fi
    if ! have_docker && ! seeded docker_source; then
        say "Docker 安装源（默认「自动」：先查国家/本地信号，再按速度尝试可达镜像，官方源垫底）：" "Docker installation source (the default, automatic, checks the country / local signals, then tries the reachable mirrors by speed with the official source last):"
        say "  1) 官方 download.docker.com" "  1) official download.docker.com"
        say "  2) 阿里云 mirrors.aliyun.com/docker-ce（get.docker.com --mirror Aliyun）" "  2) Alibaba Cloud mirrors.aliyun.com/docker-ce (get.docker.com --mirror Aliyun)"
        say "  3) Azure 中国 mirror.azure.cn/docker-ce（--mirror AzureChinaCloud）" "  3) Azure China mirror.azure.cn/docker-ce (--mirror AzureChinaCloud)"
        say "  4) 清华 TUNA mirrors.tuna.tsinghua.edu.cn/docker-ce（DOWNLOAD_URL）" "  4) Tsinghua TUNA mirrors.tuna.tsinghua.edu.cn/docker-ce (DOWNLOAD_URL)"
        say "  5) 中科大 USTC mirrors.ustc.edu.cn/docker-ce（DOWNLOAD_URL）" "  5) USTC mirrors.ustc.edu.cn/docker-ce (DOWNLOAD_URL)"
        say "  6) 腾讯云 mirrors.cloud.tencent.com/docker-ce（DOWNLOAD_URL）" "  6) Tencent Cloud mirrors.cloud.tencent.com/docker-ce (DOWNLOAD_URL)"
        say "  7) 自定义 https 地址（须与 download.docker.com 同样的目录结构）" "  7) custom https URL (must use the same layout as download.docker.com)"
        say "  8) 自动（默认）" "  8) automatic (default)"
        case "$DOCKER_SOURCE" in official) def=1 ;; aliyun) def=2 ;; azure) def=3 ;; tuna) def=4 ;; ustc) def=5 ;; tencent) def=6 ;; https://*) def=7 ;; esac
        ask_valid c "选择 [1-8]" "Choice [1-8]" "$def" chk_choice_1_8
        case "$c" in
            1) DOCKER_SOURCE=official ;; 2) DOCKER_SOURCE=aliyun ;; 3) DOCKER_SOURCE=azure ;; 4) DOCKER_SOURCE=tuna ;; 5) DOCKER_SOURCE=ustc ;; 6) DOCKER_SOURCE=tencent ;; 8) DOCKER_SOURCE='' ;;
            7) cur=''; case "$DOCKER_SOURCE" in https://*) cur="$DOCKER_SOURCE" ;; esac
                ask_valid u "自定义安装源地址（https://…）" "Custom installation source URL (https://...)" "$cur" chk_https_url; DOCKER_SOURCE="${u%/}" ;;
        esac
        DK_RESOLVED=0
    fi
    if ! seeded docker_mirrors; then
        def=1; [ "${#DOCKER_MIRRORS[@]}" -eq 0 ] || def=2
        say "Docker Hub 镜像加速（写入 /etc/docker/daemon.json 的 registry-mirrors；只影响 postgres/caddy 基础镜像，生产镜像来自镜像仓库；本次由本脚本新装 Docker 且在中国大陆时会自动探测并配置，已有 Docker 总是先征求同意）：" "Docker Hub registry mirror (registry-mirrors in /etc/docker/daemon.json; it only affects the postgres/caddy base images, the production images come from the registry; when this run installs Docker itself in mainland China it is probed and configured automatically, an existing Docker is always asked first):"
        say "  1) 不手动指定（默认）" "  1) none given (default)"
        say "  2) 输入我自己的加速地址（可填多个，空格分隔）" "  2) enter my own mirror URL(s) (several separated by spaces)"
        ask_valid c "选择 [1-2]" "Choice [1-2]" "$def" chk_choice_1_2
        if [ "$c" = 2 ]; then
            u="${DOCKER_MIRRORS[*]:-}"
            ask_valid u "加速地址（https://…，空格分隔）" "Mirror URL(s) (https://..., space separated)" "$u" chk_mirror_urls
            DOCKER_MIRRORS=(); for c in $u; do DOCKER_MIRRORS+=("${c%/}"); done
        else DOCKER_MIRRORS=(); fi
    fi
}
chk_choice_1_2() { case "$1" in 1 | 2) ;; *) m "请输入 1 或 2" "enter 1 or 2"; return 1 ;; esac; }
chk_mirror_urls() {
    local x any=0
    for x in $1; do valid_mirror_url "$x" || { m "不是合法的 http(s):// 地址: $x" "not a valid http(s):// URL: $x"; return 1; }; any=1; done
    [ "$any" = 1 ] || { m "至少填一个地址（或回到上一步选 1 不配置）" "enter at least one URL (or choose 1, none, in the previous question)"; return 1; }
}


# wizard_answers: all questions, in order. Used for a fresh installation.
wizard_answers() {
    q_dir
    q_port
    q_expose
    q_db
    q_storage
    q_mirror
    q_tag
}

# review_screen: every setting (secrets masked) and "Proceed? [Y]es / [e]dit a setting / [c]ancel"
EDIT_FNS=(q_dir q_port q_expose q_db q_storage q_mirror q_tag q_docker q_lang)
review_line() { printf '  %s) %-18s %s\n' "$1" "$2" "$3"; }
review_screen() {
    local ans='' n i db acc stor img dk
    while :; do
        if [ "$PROXY_MODE" = 1 ]; then acc="$(m "域名 $DOMAIN，放在已有反向代理后面（OA 监听 127.0.0.1:$PORT，不启用 Caddy）" "domain $DOMAIN behind an existing reverse proxy (OA listens on 127.0.0.1:$PORT, no Caddy)")"
        elif [ -n "$DOMAIN" ]; then acc="$(m "https://$DOMAIN（内置 Caddy 自动证书${EMAIL:+，邮箱 $EMAIL}）" "https://$DOMAIN (bundled Caddy, automatic certificates${EMAIL:+, e-mail $EMAIL})")"
        else acc="$(m "仅 HTTP（无 Passkey）" "plain HTTP (no Passkeys)")"; fi
        if [ "$DB_MODE" = external ]; then db="$(db_label "$DB_PROVIDER") $DB_HOST:$DB_PORT/$DB_NAME, $(m '用户' 'user') $DB_USER, $(m '密码' 'password') $(mask_pw), TLS ${DB_TLS:-require}${DB_CA_FILE:+, CA $DB_CA_FILE}"
        else db="$(m '内置 PostgreSQL（compose 里的容器）' 'bundled PostgreSQL (container in the compose stack)')"; fi
        stor="$(m "归档 $ARCHIVE_DIR；暂存 $SPOOL_DIR；应用数据 $APP_DIR" "archive $ARCHIVE_DIR; spool $SPOOL_DIR; app data $APP_DIR")"
        if [ "$DB_MODE" != external ]; then stor="$stor; $(m 'PostgreSQL' 'PostgreSQL') $PG_DIR"; fi
        for i in "${!EX_NAME[@]}"; do stor="$stor; ${EX_NAME[$i]}=${EX_HOST[$i]}(${EX_RO[$i]})"; done
        printf '\n%s\n' "$(m '== 请复核全部设置（密码已遮蔽）==' '== Review your settings (passwords are masked) ==')" >&2
        review_line 1 "$(m '安装目录' 'Install dir')" "$DIR" >&2
        review_line 2 "$(m 'HTTP 端口' 'HTTP port')" "$PORT" >&2
        review_line 3 "$(m '访问方式' 'Access')" "$acc" >&2
        review_line 4 "$(m '数据库' 'Database')" "$db" >&2
        review_line 5 "$(m '存储目录' 'Storage')" "$stor" >&2
        review_line 6 "$(m '镜像源' 'Mirror')" "${MIRROR:-default}$([ -n "$MIRROR_HINT" ] && [ "$MIRROR" = "$MIRROR_HINT" ] && m '（依据检测预选）' ' (preselected from detection)')" >&2
        if [ -n "$REGISTRY" ]; then img="$REGISTRY : $VERSION"; else img="$(m '本机用源码构建' 'built from source on this host')"; fi
        review_line 7 "$(m '镜像/标签' 'Images/tag')" "$img" >&2
        if have_docker; then dk="$(m 'Docker 已安装，沿用不重装' 'Docker is already installed and kept')"
        elif [ -n "$OFFLINE_FILE" ]; then dk="$(m '离线模式' 'offline mode')"
        else resolve_docker_source; dk="$(m "将安装；Docker 安装源：$DK_WHY_ZH" "will be installed; Docker installation source: $DK_WHY_EN")"; fi
        if [ "${#DOCKER_MIRRORS[@]}" -gt 0 ]; then dk="$dk; $(m 'Docker Hub 加速' 'Docker Hub mirror') ${DOCKER_MIRRORS[*]} $(m '（写 daemon.json 前会再询问）' '(asked again before daemon.json is written)')"; fi
        review_line 8 "Docker" "$dk" >&2
        review_line 9 "$(m '界面语言' 'Language')" "$([ "$UI" = zh ] && echo '中文' || echo 'English')" >&2
        printf '  %s\n' "$(m "此外：systemd 开机单元、防火墙放行在最后单独询问；管理员账号由初始化向导 + 一次性令牌创建（安装结束时显示令牌）。" "Also: the systemd unit and the firewall are asked at the end; the admin account is created by the setup wizard with a one-time token (shown when the installation ends).")" >&2
        if [ "$ASSUME_YES" = 1 ] || ! have_tty; then return 0; fi
        printf '%s ' "$(m '继续安装？[Y]是 / [e]修改某项 / [c]取消:' 'Proceed? [Y]es / [e]dit a setting / [c]ancel:')" >&2
        IFS= read -r ans <&9 || ans=''
        case "$ans" in
            '' | y | Y | yes | YES) return 0 ;;
            c | C | cancel | n | N | no) die_cancel "已取消：未做任何更改。" "Cancelled: nothing was changed." ;;
            e | E | edit)
                printf '%s ' "$(m '修改哪一项？[1-9]:' 'Edit which setting? [1-9]:')" >&2
                IFS= read -r n <&9 || n=''
                case "$n" in
                    1 | 2 | 3 | 4 | 5 | 6 | 7 | 8 | 9) SEEDED=' '; "${EDIT_FNS[$((n - 1))]}" ;;
                    *) printf '%s[!]%s %s\n' "$C_R" "$C_0" "$(m '请输入 1-9' 'enter 1-9')" >&2 ;;
                esac ;;
            *) printf '%s[!]%s %s\n' "$C_R" "$C_0" "$(m '请输入 Y、e 或 c' 'enter Y, e or c')" >&2 ;;
        esac
    done
}

# ---------------------------------------------------------------------------------------------- decisions: which installation, what to do
TGT=-1
STATUS_REQ=0
chk_pick() { if [[ "$1" =~ ^[0-9]+$ ]] && [ "$1" -ge 1 ] && [ "$1" -le "${#IN_DIR[@]}" ]; then return 0; fi; m "请输入 1-${#IN_DIR[@]}" "enter 1-${#IN_DIR[@]}"; return 1; }
# resolve_target: which installation do we manage? Sets TGT (index into IN_*), EXISTING and DIR.
resolve_target() {
    local n="${#IN_DIR[@]}" i choice=''
    TGT="$(install_index "$DIR")"
    if [ "$TGT" -ge 0 ]; then EXISTING=1; return 0; fi
    EXISTING=0
    if [ "$DIR_SET" = 1 ] || [ "$n" -eq 0 ]; then return 0; fi
    if [ "$n" -eq 1 ]; then
        say "在 ${IN_DIR[0]} 发现已有安装（不在 $DIR）：改为管理它。要另装一套独立的，请加 --dir <新目录> --project <新名称>。" "Found an existing installation in ${IN_DIR[0]} (not in $DIR): managing that one. To install a second, independent copy add --dir <new dir> --project <new name>."
        DIR="${IN_DIR[0]}"; TGT=0; EXISTING=1
        return 0
    fi
    say "发现多套安装：" "Several installations were found:"
    for i in "${!IN_DIR[@]}"; do say "  $((i + 1))) ${IN_DIR[$i]}（项目 ${IN_PROJ[$i]}，标签 ${IN_VER[$i]:-?}）" "  $((i + 1))) ${IN_DIR[$i]} (project ${IN_PROJ[$i]}, tag ${IN_VER[$i]:-?})"; done
    if [ "$ASSUME_YES" = 1 ] || ! have_tty; then die_usage "发现多套安装：请用 --dir 指定要管理哪一个。" "Several installations exist: use --dir to say which one to manage."; fi
    ask_valid choice "管理哪一套？[1-${#IN_DIR[@]}]" "Manage which one? [1-${#IN_DIR[@]}]" 1 chk_pick
    TGT=$((choice - 1)); DIR="${IN_DIR[$TGT]}"; EXISTING=1
}

NOTTY_MSG_ZH='没有可用的终端来回答安装问题（标准输入是脚本本身，也无法打开 /dev/tty，例如 ssh 没加 -t 或在 CI 中）。请：1) 用 ssh -t 登录；或 2) 先下载脚本再运行（docker run --rm <deploy 镜像> > install.sh && sudo bash install.sh）；或 3) 加 --yes 接受默认值（可配合 --config <文件> 与各项参数做无人值守安装）。为避免悄悄装出一个纯 HTTP 的默认实例，本次未做任何更改。'
NOTTY_MSG_EN='No terminal is available for the installation questions (stdin is the script itself and /dev/tty cannot be opened, e.g. ssh without -t, or CI). Either: 1) log in with ssh -t; 2) download the script first (docker run --rm <deploy image> > install.sh && sudo bash install.sh); or 3) pass --yes to accept the defaults (combine it with --config <file> and the option flags for an unattended install). To avoid silently creating a default plain-HTTP instance nothing was changed.'

# version_note IDX: one line comparing the installed tag with the requested one (empty when no tag was requested)
version_note() {
    local cur="${IN_VER[$1]}" rel
    if [ "$VERSION_SET" = 0 ]; then printf '%s' ""; return 0; fi
    rel="$(ver_relation "$cur" "$VERSION")"
    case "$rel" in
        same) printf '%s' "$(m "已是请求的版本 $VERSION" "already the requested version $VERSION")" ;;
        mutable) printf '%s' "$(m "标签 $VERSION 会移动：选「升级」才会拉取最新镜像" "the tag $VERSION moves: choose upgrade to pull the newest image")" ;;
        newer) printf '%s' "$(m "可升级：$cur -> $VERSION" "upgrade available: $cur -> $VERSION")" ;;
        older) printf '%s' "$(m "请求的 $VERSION 比已安装的 $cur 旧：数据库迁移不可回退，默认拒绝降级（--allow-downgrade 可强制）" "the requested $VERSION is older than the installed $cur: database migrations are not reversible, downgrades are refused (--allow-downgrade forces it)")" ;;
        *) printf '%s' "$(m "请求的 $VERSION 与已安装的 $cur 无法比较大小" "the requested $VERSION cannot be compared with the installed $cur")" ;;
    esac
}

chk_menu() { case "$1" in 1 | 2 | 3 | 4 | 5 | 6) ;; *) m "请输入 1-6" "enter 1-6"; return 1 ;; esac; }
# existing_menu: interactive only (a terminal, no --yes, no action flag). Sets DO_UPGRADE / REINSTALL / DO_UNINSTALL / STATUS_REQ, or cancels.
existing_menu() {
    local cur_ver choice='' def=1 h note
    cur_ver="${IN_VER[$TGT]}"; h="${IN_HEALTH[$TGT]}"
    say "在 $DIR 检测到已有安装（版本/标签: ${cur_ver:-?}，状态: $h）。请选择：" "An installation already exists in $DIR (version/tag: ${cur_ver:-?}, state: $h). Choose:"
    note="$(version_note "$TGT")"
    [ -z "$note" ] || say "  $note" "  $note"
    say "  1) 修复/继续    保留 .env 与全部数据，刷新文件并启动" "  1) repair/continue   keep .env and all data, refresh files and start"
    say "  2) 升级         备份后拉取/构建新版本并重建" "  2) upgrade           back up, then pull/build the new version and recreate"
    say "  3) 重装         删除并重建容器（数据与 .env 保留），重新拉取镜像" "  3) reinstall         remove and recreate the containers (data and .env kept), pull the images again"
    say "  4) 卸载         移除容器与 systemd 单元（数据保留；清除数据需 --uninstall --purge）" "  4) uninstall         remove the containers and the systemd unit (data kept; deleting data needs --uninstall --purge)"
    say "  5) 取消" "  5) cancel"
    say "  6) 无需操作     只显示状态与访问信息" "  6) nothing to do     only show the status and access information"
    if [ "$h" = healthy ]; then def=6; fi
    ask_valid choice "选择 [1-6]（默认 $def）" "Choice [1-6] (default $def)" "$def" chk_menu
    case "$choice" in
        2) DO_UPGRADE=1 ;;
        3) REINSTALL=1 ;;
        4) DO_UNINSTALL=1 ;;
        5) die_cancel "已取消" "Cancelled" ;;
        6) STATUS_REQ=1 ;;
    esac
    MENU_DONE=1
}
# change_requested: did the command line ask for something that a plain "everything is fine" status answer would ignore?
change_requested() {
    [ -z "$ARCHIVE_DIR$SPOOL_DIR$APP_DIR$PG_DIR" ] || return 0
    [ "${#EXTRA_MOUNTS[@]}" -eq 0 ] || return 0
    if [ -n "$DOMAIN" ] && [ "$DOMAIN" != "$(env_get OA_DOMAIN)" ]; then return 0; fi
    if [ "$VERSION_SET" = 1 ] && [ "$VERSION" != "$(env_get OA_VERSION)" ]; then return 0; fi
    [ "$SYSTEMD" != yes ] || return 0
    [ "$OPEN_FW" != yes ] || return 0
    [ -z "$OFFLINE_FILE" ] || return 0
    return 1
}
# decide_existing: explicit flags win; a terminal gets the menu; unattended runs on a healthy, up-to-date install only report status
decide_existing() {
    local h="${IN_HEALTH[$TGT]}"
    if [ "$DO_UPGRADE" = 1 ] || [ "$REINSTALL" = 1 ] || [ "$DO_UNINSTALL" = 1 ]; then return 0; fi
    if [ "$CHECK" = 1 ]; then return 0; fi
    if [ "$ASSUME_YES" != 1 ] && have_tty; then existing_menu; return 0; fi
    if [ "$ASSUME_YES" != 1 ]; then
        warn "$NOTTY_MSG_ZH 已有安装时只显示状态（加 --yes 才会修复/继续）。" "$NOTTY_MSG_EN With an existing installation only its status is shown (add --yes to repair/continue)."
        STATUS_REQ=1; return 0
    fi
    if [ "$h" = healthy ] && ! change_requested; then STATUS_REQ=1; fi
    return 0
}

# status_report IDX: state of one installation + access information (read-mostly: nothing is changed)
status_report() {
    local i="$1" d="${IN_DIR[$1]}" proj="${IN_PROJ[$1]}" url l tok shown=0 hs
    case "${IN_HEALTH[$i]}" in healthy) hs="$(m '全部服务运行正常' 'all services are healthy')" ;; unhealthy) hs="$(m '有服务不健康' 'some service is unhealthy')" ;; partial) hs="$(m '部分服务未运行' 'some services are not running')" ;; stopped) hs="$(m '服务已停止' 'the services are stopped')" ;; *) hs="$(m '状态未知（Docker 未运行）' 'state unknown (Docker is not running)')" ;; esac
    step "状态：$d" "Status: $d"
    ok "已有安装: $d（版本/标签 ${IN_VER[$i]:-?}），$hs" "Existing installation detected: $d (version/tag ${IN_VER[$i]:-?}), $hs"
    printf '  %-16s %s\n' "$(m '项目' 'Project')" "$proj" "$(m '镜像仓库' 'Registry')" "${IN_REG[$i]:-?}" "$(m '数据库' 'Database')" "${IN_DBM[$i]}"
    printf '  %-16s %s / %s\n' "$(m '端口 / 域名' 'Port / domain')" "${IN_PORT[$i]:-?}" "${IN_DOM[$i]:--}"
    for l in ${CT_LINES[@]+"${CT_LINES[@]}"}; do
        [ "$(ct_field "$l" 4)" = "$proj" ] || continue
        printf '  %-16s %s (%s)\n' "$(ct_field "$l" 7)" "$(ct_field "$l" 3)" "$(ct_field "$l" 8)"; shown=1
    done
    [ "$shown" = 1 ] || printf '  %s\n' "$(m '（没有找到该项目的容器）' '(no containers of this project)')"
    url="$(env_read "$d/.env" OA_PUBLIC_URL)"
    [ -z "$url" ] || printf '  %-16s %s\n' "$(m '访问地址' 'URL')" "$url"
    if [ "${IN_HEALTH[$i]}" = healthy ] || [ "${IN_HEALTH[$i]}" = unknown ]; then
        tok="$(DIR="$d" ctl setup-token 2>/dev/null | tail -n 1 || true)"
        printf '  %-16s %s\n' "$(m '初始化令牌' 'SETUP TOKEN')" "${tok:-$(m '（无；可能已完成初始化，或运行 oa-agentctl setup-token）' '(none; setup may be complete, or run oa-agentctl setup-token)')}"
    fi
    printf '  %-16s %s\n' "$(m '状态文件' 'State file')" "$([ -f "$STATE_FILE" ] && echo "$STATE_FILE" || m '（尚无）' '(none yet)')"
    printf '\n  %s\n' "$(m "下一步：oa-agentctl status | logs | doctor | backup | upgrade；重新运行本脚本并加 --upgrade / --reinstall / --uninstall 可升级、重建、卸载（数据保留）。" "Next: oa-agentctl status | logs | doctor | backup | upgrade; re-run this script with --upgrade / --reinstall / --uninstall to upgrade, recreate or remove (data is kept).")"
}

# ---------------------------------------------------------------------------------------------- orphaned data (old data, no .env)
# orphan_restore_env BACKUP: put the .env of an old backup back (mode 0600); the backup's key and database password match the old data
orphan_restore_env() {
    local tmp
    mkdir -p "$DIR"; chmod 750 "$DIR"
    tmp="$(mktemp "$DIR/.env.XXXXXX")"
    if ! tar -xzOf "$1" ./env >"$tmp" 2>/dev/null || ! grep -q '^OA_SECRET_KEY=' "$tmp"; then rm -f -- "$tmp"; return 1; fi
    chmod 600 "$tmp"; mv "$tmp" "$DIR/.env"
}
# orphan_decide: before the questions. Database files without .env cannot be reused with newly generated secrets.
orphan_decide() {
    [ ! -f "$DIR/.env" ] || return 0
    orphan_scan "$DIR" "$DIR/data/app" "$DIR/data/pg" "$DIR/data/archive"
    [ -n "$ORPH_PG" ] || return 0
    say "在 $DIR 发现旧安装的数据库文件（$ORPH_PG），但没有 .env（里面有数据库口令和 OA_SECRET_KEY）。" "Database files of an earlier installation were found in $DIR ($ORPH_PG) but its .env (with the database password and OA_SECRET_KEY) is missing."
    if [ -z "$ORPH_BACKUP" ]; then
        say "没有找到可用的备份来恢复 .env。继续时请为 PostgreSQL 数据目录选择一个空目录；否则预检会拒绝（绝不会生成新密钥覆盖旧数据）。" "No backup that could restore the .env was found. Continue with an empty PostgreSQL data directory, otherwise the pre-flight refuses (a new key is never generated over old data)."
        return 0
    fi
    say "找到备份 $(basename "$ORPH_BACKUP")，其中的 .env 与这份数据匹配。" "Found the backup $(basename "$ORPH_BACKUP"); the .env inside matches this data."
    if [ "$ADOPT" = 1 ] || { [ "$ASSUME_YES" != 1 ] && have_tty && confirm "从该备份恢复 .env 并沿用旧数据（相当于修复安装）？" "Restore the .env from this backup and keep using the old data (a repair)?" y; }; then
        orphan_restore_env "$ORPH_BACKUP" || die "无法从 $ORPH_BACKUP 取出 .env。" "Cannot extract the .env from $ORPH_BACKUP."
        ok "已从备份恢复 $DIR/.env（权限 600）；按「修复/继续」处理，沿用旧密钥与数据库口令。" "Restored $DIR/.env from the backup (mode 600); treated as repair/continue with the old key and database password."
        act_chg ".env 已从备份 $(basename "$ORPH_BACKUP") 恢复" ".env restored from the backup $(basename "$ORPH_BACKUP")"
        EXISTING=1
        return 0
    fi
    if [ "$ASSUME_YES" = 1 ]; then
        die_preflight "发现旧数据且没有 .env：--yes 不会自行决定。加 --adopt-data 从备份 $(basename "$ORPH_BACKUP") 恢复 .env，或用 --dir/--pg-dir 指向空目录，或自行处理旧数据。未做任何更改。" "Old data without .env found: --yes will not decide this by itself. Add --adopt-data to restore the .env from the backup $(basename "$ORPH_BACKUP"), or point --dir/--pg-dir at empty directories, or deal with the old data yourself. Nothing was changed."
    fi
    say "好的：不使用旧数据。请在后续问题里选择新的、空的目录。" "OK: the old data is not used. Choose new, empty directories in the questions that follow."
}
# orphan_gate: with the FINAL directories, before anything is written
orphan_gate() {
    local k
    [ ! -f "$DIR/.env" ] || return 0
    orphan_scan "$DIR" "$APP_DIR" "$PG_DIR" "$ARCHIVE_DIR"
    if [ "$DB_MODE" != external ] && [ -n "$ORPH_PG" ]; then
        die_preflight "拒绝继续：$ORPH_PG 里有旧安装的数据库文件，但它的 .env（数据库口令与 OA_SECRET_KEY）不存在。用新生成的口令数据库会拒绝连接，已保存的 API 密钥/存储凭据也无法用新密钥解密。选项：1) 恢复旧的 .env（oa-agentctl 备份里的 env 文件）；2) 用 --pg-dir 指向一个空目录（旧文件原样保留，不会被删除）；3) 自行移走旧数据。未做任何更改。" \
            "Refusing to continue: $ORPH_PG holds the database files of an earlier installation but its .env (database password and OA_SECRET_KEY) is missing. With newly generated secrets the database would refuse the connection and saved API keys / storage credentials could not be decrypted. Options: 1) restore the old .env (the env file in an oa-agentctl backup); 2) point --pg-dir at an empty directory (the old files are left untouched, never deleted); 3) move the old data away yourself. Nothing was changed."
    fi
    if [ -n "$ORPH_KEY" ] && [ -z "$KEY_FROM_FILE" ]; then
        k="$(head -n 1 "$ORPH_KEY" 2>/dev/null | tr -d '[:space:]' || true)"
        if [[ "$k" =~ ^[0-9a-fA-F]{64}$ ]]; then
            KEY_FROM_FILE="$k"
            ok "发现旧的 secret.key（$ORPH_KEY）：新 .env 沿用这把密钥，不会生成新密钥覆盖旧数据。" "Found an old secret.key ($ORPH_KEY): the new .env reuses that key; no new key is generated over old data."
            act_chg "沿用已有的 secret.key 作为 OA_SECRET_KEY" "Reused the existing secret.key as OA_SECRET_KEY"
        else
            die_preflight "$ORPH_KEY 存在但内容不是 64 位十六进制密钥（文件损坏？）：不会在旧数据上生成新密钥。请修复或移走该文件。未做任何更改。" "$ORPH_KEY exists but is not a 64-hex-character key (damaged?): a new key is not generated over old data. Repair or move the file. Nothing was changed."
        fi
    fi
    if [ "$ORPH_ARCH" = 1 ]; then say "提示：归档目录 $ARCHIVE_DIR 里已有文件；新数据库不认识它们（台账在数据库里），它们不会被改动。" "Note: the archive directory $ARCHIVE_DIR already holds files; the new database does not know them (the ledger lives in the database) and they are not touched."; fi
}

# ---------------------------------------------------------------------------------------------- external database: validation before anything is installed
DB_SCHEMA_STATE=''
db_image() { printf '%s/oa-agent-api:%s' "${REGISTRY:-$PROJECT}" "$VERSION"; }
# db_run SUBCOMMAND [ARGS...]: oa-admin <SUBCOMMAND> inside the oa-agent-api image. The password goes in on stdin only:
# it never appears in a process argument list, the environment of the container, the log or the screen.
db_run() {
    local sub="$1" pw; shift
    pw="$(db_password_value)"
    local a=(run --rm -i --add-host host.docker.internal:host-gateway -e "OA_LANG=$UI" -e "OA_DB_PROVIDER=$DB_PROVIDER" -e "OA_DB_HOST=$DB_HOST" -e "OA_DB_PORT=$DB_PORT" -e "OA_DB_NAME=$DB_NAME" -e "OA_DB_USER=$DB_USER" -e "OA_DB_TLS_MODE=${DB_TLS:-require}")
    [ -z "$DB_SCHEMA" ] || a+=(-e "OA_DB_SCHEMA=$DB_SCHEMA")
    if [ -n "$DB_CA_FILE" ]; then a+=(-v "$DB_CA_FILE:/oa-db-ca.pem:ro" -e OA_DB_TLS_CA_FILE=/oa-db-ca.pem); fi
    a+=(--entrypoint oa-admin "$(db_image)" "$sub" --password-stdin "$@")
    printf '%s\n' "$pw" | docker "${a[@]}" 2>&1
}
db_image_ready() {
    local img; img="$(db_image)"
    docker image inspect "$img" >/dev/null 2>&1 && return 0
    [ -n "$REGISTRY" ] && [ -z "$OFFLINE_FILE" ] || return 1
    step "拉取 API 镜像（用来检查数据库）" "Pulling the API image (it is used to check the database)"
    net_retry "docker pull $img" docker pull "$img" || { registry_help; die "拉取 $img 失败" "Pulling $img failed"; }
}
db_check_title() { # db_check_title ID ZH-TITLE
    if [ "$UI" = zh ]; then printf '%s' "$2"; return 0; fi
    case "$1" in
        connect) echo "Connection" ;; version) echo "Server version" ;; charset) echo "Character set / collation" ;; timezone) echo "Time zone" ;;
        compat) echo "Compatibility level / collation" ;; perm_schema) echo "Permissions" ;; least_privilege) echo "Least privilege" ;;
        schema) echo "Target database / schema" ;; isolation) echo "Isolation (dedicated database/schema)" ;; migration) echo "Schema version" ;; *) echo "$2" ;;
    esac
}
# db_show OUTPUT: the checklist from "oa-admin db-check" as [PASS]/[WARN]/[FAIL] lines
db_show() {
    local kind st id title detail
    while IFS=$'\t' read -r kind st id title detail; do
        case "$kind" in
            CHECK)
                case "$st" in
                    pass) printf '  %s[PASS]%s %s: %s\n' "$C_G" "$C_0" "$(db_check_title "$id" "$title")" "$detail" ;;
                    warn) printf '  %s[WARN]%s %s: %s\n' "$C_Y" "$C_0" "$(db_check_title "$id" "$title")" "$detail" ;;
                    fail) printf '  %s[FAIL]%s %s: %s\n' "$C_R" "$C_0" "$(db_check_title "$id" "$title")" "$detail" ;;
                    *) printf '  %s[INFO]%s %s: %s\n' "$C_B" "$C_0" "$(db_check_title "$id" "$title")" "$detail" ;;
                esac ;;
            INFO) case "$st" in server_version) [ -z "$id" ] || printf '  [INFO] %s: %s\n' "$(m '服务器版本' 'server version')" "$id" ;; schema) printf '  [INFO] %s: %s\n' "$(m '表结构状态' 'schema state')" "$id" ;; esac ;;
        esac
    done <<<"$1"
}
db_menu_choice() { case "$1" in r | R | c | C | a | A) ;; *) m "请输入 r、c 或 a" "enter r, c or a"; return 1 ;; esac; }
# db_gate: connectivity, version, permissions, isolation, empty-or-OA-only schema. Loops until it passes (retry / change / abort).
db_gate() {
    local out rc ch=''
    [ "$DB_MODE" = external ] || return 0
    step "检查已有数据库（安装任何东西之前）" "Checking the existing database (before anything is installed)"
    docker_ready || die "需要 Docker 才能检查数据库。" "Docker is needed to check the database."
    db_image_ready || return 2
    while :; do
        say "  $(db_label "$DB_PROVIDER") $DB_HOST:$DB_PORT / $DB_NAME，$(m '用户' 'user') $DB_USER，TLS ${DB_TLS:-require}" "  $(db_label "$DB_PROVIDER") $DB_HOST:$DB_PORT / $DB_NAME, user $DB_USER, TLS ${DB_TLS:-require}"
        rc=0; out="$(db_run db-check)" || rc=$?
        db_show "$out"
        DB_SCHEMA_STATE="$(printf '%s\n' "$out" | awk -F'\t' '$1=="INFO" && $2=="schema" {print $3; exit}')"
        if [ "$rc" -eq 0 ]; then ok "数据库检查通过（版本、权限、隔离性均满足）" "The database passed the checks (version, permissions and isolation are fine)"; return 0; fi
        logf "db-check failed rc=$rc"
        if [ "$ASSUME_YES" = 1 ] || ! have_tty; then
            die_preflight "数据库检查失败（见上面的清单），未做任何更改。修正数据库/账号/网络后重试。" "The database check failed (see the checklist above); nothing was changed. Fix the database, account or network and retry."
        fi
        say "  r) 重试同样的设置   c) 修改数据库设置   a) 中止（不做任何更改）" "  r) retry with the same settings   c) change the database settings   a) abort (nothing is changed)"
        ch=''
        ask_valid ch "选择 [r/c/a]" "Choice [r/c/a]" r db_menu_choice
        case "$ch" in
            a | A) die_cancel "已中止：未做任何更改。" "Aborted: nothing was changed." ;;
            c | C) DB_PASSWORD=''; DB_PASSWORD_FILE=''; SEEDED=' '; q_db_conn ;;
        esac
    done
}
# db_secret_key_gate: a database that already holds OA Agent data needs the key that encrypted it
db_secret_key_gate() {
    [ "$DB_MODE" = external ] || return 0
    case "$DB_SCHEMA_STATE" in ok | migration_required) ;; *) return 0 ;; esac
    if [ -z "$KEY_FROM_FILE" ] && [ -n "$SECRET_KEY_FILE" ]; then
        check_secret_file "$SECRET_KEY_FILE" "secret_key_file"
        KEY_FROM_FILE="$(head -n 1 "$SECRET_KEY_FILE" | tr -d '[:space:]')"
    fi
    if [[ "$KEY_FROM_FILE" =~ ^[0-9a-fA-F]{64}$ ]]; then
        ok "目标库已有 OA Agent 数据：沿用你提供的原 OA_SECRET_KEY。" "The target already holds OA Agent data: reusing the original OA_SECRET_KEY you provided."
        return 0
    fi
    warn "目标数据库里已经有 OA Agent 的数据（表结构状态：$DB_SCHEMA_STATE）。其中保存的 API 密钥/存储凭据由原来的 OA_SECRET_KEY 加密；用新生成的密钥将无法解密。" "The target database already holds OA Agent data (schema state: $DB_SCHEMA_STATE). Saved API keys / storage credentials in it are encrypted with the original OA_SECRET_KEY; a newly generated key could not decrypt them."
    if [ "$ASSUME_YES" != 1 ] && have_tty; then
        local k=''
        ask_secret k "请输入原来的 OA_SECRET_KEY（64 位十六进制，不回显；留空 = 取消）" "Enter the original OA_SECRET_KEY (64 hex characters, not echoed; empty = cancel)"
        if [[ "$k" =~ ^[0-9a-fA-F]{64}$ ]]; then KEY_FROM_FILE="$k"; return 0; fi
        die_cancel "已取消：未提供原密钥，未做任何更改。" "Cancelled: the original key was not provided; nothing was changed."
    fi
    die_preflight "目标库已有 OA Agent 数据，但没有提供原 OA_SECRET_KEY（配置项 secret_key_file=<权限 600 的文件>）。绝不会在旧数据上生成新密钥。未做任何更改。" "The target holds OA Agent data but the original OA_SECRET_KEY was not provided (config key secret_key_file=<mode 600 file>). A new key is never generated over old data. Nothing was changed."
}
# db_init_flow: the second, explicit confirmation: show the DDL preview, then create the tables with exactly that plan
db_init_flow() {
    [ "$DB_MODE" = external ] || return 0
    local out rc=0 plan fp stmts migs ans='' ddl
    out="$(db_run db-init)" || rc=$?
    plan="$(printf '%s\n' "$out" | awk -F'\t' '$1=="PLAN" {print; exit}')"
    if [ "$rc" -ne 0 ] || [ -z "$plan" ]; then
        db_show "$out"; printf '%s\n' "$out" | awk -F'\t' '$1=="RESULT" {print "  " $3}' >&2
        die_preflight "无法预览建表计划（见上）。未做任何更改。" "Cannot preview the schema plan (see above). Nothing was changed."
    fi
    if [ "$(printf '%s' "$plan" | tr '\t' '\n' | sed -n 's/^migrations=//p')" = 0 ]; then ok "表结构已是最新：无需建表。" "The schema is already current: nothing to create."; return 0; fi
    fp="$(printf '%s' "$plan" | tr '\t' '\n' | sed -n 's/^fingerprint=//p')"
    stmts="$(printf '%s' "$plan" | tr '\t' '\n' | sed -n 's/^statements=//p')"
    migs="$(printf '%s' "$plan" | tr '\t' '\n' | sed -n 's/^migrations=//p')"
    say "将在 $DB_HOST:$DB_PORT/$DB_NAME 中创建 OA Agent 的表结构：$migs 个迁移、共 $stmts 条 DDL（计划指纹 $fp）。" "OA Agent would create its schema in $DB_HOST:$DB_PORT/$DB_NAME: $migs migration(s), $stmts DDL statement(s) in total (plan fingerprint $fp)."
    printf '%s\n' "$out" | awk -F'\t' -v fmt="$(m '    迁移 %s %s（%s 条语句）' '    migration %s %s (%s statements)')" '$1=="STEP" {printf fmt "\n", $2, $3, $4}' >&2
    if [ "$ASSUME_YES" = 1 ] || ! have_tty; then
        say "非交互模式不会代你批准 DDL：表结构将由 API 首次启动时按部署级批准（OA_AUTO_MIGRATE=1）创建。预览与指纹已记入日志。" "Non-interactive mode does not approve DDL on your behalf: the API creates the schema on its first start under the deployment-level approval (OA_AUTO_MIGRATE=1). The preview and fingerprint are in the log."
        logf "db-init preview fingerprint=$fp statements=$stmts"
        return 0
    fi
    while :; do
        printf '%s ' "$(m '现在创建这些表？[y]创建 / [v]查看全部 DDL / [n]取消安装:' 'Create these tables now? [y]es / [v]iew the full DDL / [n]o, cancel the installation:')" >&2
        IFS= read -r ans <&9 || ans=''
        case "$ans" in
            v | V) ddl="$(db_run db-init --show-ddl)" || true; printf '%s\n' "$ddl" | awk -F'\t' '$1=="DDL" {print "    " $2}' >&2 ;;
            y | Y | yes) break ;;
            n | N | no | '') die_cancel "已取消：数据库没有被改动，主机上也没有安装任何东西。" "Cancelled: the database was not changed and nothing was installed on this host." ;;
        esac
    done
    rc=0; out="$(db_run db-init --apply --plan "$fp")" || rc=$?
    if [ "$rc" -ne 0 ]; then printf '%s\n' "$out" | awk -F'\t' '$1=="RESULT" {print "  " $3}' >&2; die_preflight "建表失败（见上）；数据库保持原样或已按其事务规则回滚。" "Creating the schema failed (see above); the database is unchanged or rolled back according to its transaction rules."; fi
    ok "OA Agent 表结构已创建（$migs 个迁移）。" "The OA Agent schema was created ($migs migration(s))."
    act_chg "在外部数据库 $DB_HOST/$DB_NAME 创建了表结构（经你批准）" "Created the schema in the external database $DB_HOST/$DB_NAME (approved by you)"
}

# ---------------------------------------------------------------------------------------------- reverse proxy snippets
proxy_snippets() {
    [ "$PROXY_MODE" = 1 ] && [ -n "$DOMAIN" ] || return 0
    printf '\n%s%s%s\n' "$C_Y" "$(m '  【把 OA 放到已有反向代理后面】' '  [Put OA behind your existing reverse proxy]')" "$C_0"
    printf '  %s\n' "$(m "OA 只监听 127.0.0.1:$PORT；代理需把 $DOMAIN 转发到它，并传 X-Forwarded-Proto/For、关闭缓冲（SSE）、不限上传大小。" "OA listens on 127.0.0.1:$PORT only; the proxy must forward $DOMAIN to it, pass X-Forwarded-Proto/For, disable buffering (SSE) and not limit upload size.")"
    printf '  nginx:\n'
    cat <<EOF
    server {
        listen 443 ssl http2;
        server_name $DOMAIN;
        # ssl_certificate / ssl_certificate_key: yours
        client_max_body_size 0;
        proxy_request_buffering off;
        proxy_buffering off;
        proxy_read_timeout 3600s;
        location / {
            proxy_pass http://127.0.0.1:$PORT;
            proxy_http_version 1.1;
            proxy_set_header Host \$host;
            proxy_set_header X-Real-IP \$remote_addr;
            proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
            proxy_set_header X-Forwarded-Proto \$scheme;
        }
    }
EOF
    printf '  Caddy:\n'
    cat <<EOF
    $DOMAIN {
        reverse_proxy 127.0.0.1:$PORT {
            flush_interval -1
            transport http {
                response_header_timeout 0
            }
        }
    }
EOF
}

# validate_db_settings: the external database settings are complete and valid (flags / config / wizard; unattended runs stop here
# when something without a safe default, such as the password or the host, is missing)
validate_db_settings() {
    local why
    [ -n "$DB_PROVIDER" ] || die_usage "已有数据库需要指定类型：--db postgres|mysql|mssql（或配置项 db_provider）。" "An existing database needs a type: --db postgres|mysql|mssql (or the config key db_provider)."
    why="$(chk_db_provider "$DB_PROVIDER")" || die_usage "数据库类型: $why" "database type: $why"
    [ -n "$DB_PORT" ] || DB_PORT="$(db_default_port "$DB_PROVIDER")"
    [ -n "$DB_NAME" ] || DB_NAME=oa
    [ -n "$DB_USER" ] || DB_USER=oa
    [ -n "$DB_TLS" ] || DB_TLS=require
    why="$(chk_db_host "$DB_HOST")" || die_usage "数据库主机: $why" "database host: $why"
    why="$(chk_db_port "$DB_PORT")" || die_usage "数据库端口: $why" "database port: $why"
    why="$(chk_db_ident "$DB_NAME")" || die_usage "数据库名: $why" "database name: $why"
    why="$(chk_db_ident "$DB_USER")" || die_usage "数据库用户: $why" "database user: $why"
    why="$(chk_db_tls "$DB_TLS")" || die_usage "TLS 模式: $why" "TLS mode: $why"
    why="$(chk_db_ca "$DB_CA_FILE")" || die_usage "CA 文件: $why" "CA file: $why"
    if [ -n "$DB_SCHEMA" ] && ! [[ "$DB_SCHEMA" =~ ^[A-Za-z0-9_]{1,63}$ ]]; then die_usage "db_schema 只能含字母、数字和下划线。" "db_schema may contain letters, digits and underscores only."; fi
    why="$(chk_db_password "$(db_password_value)")" || die_usage "数据库密码缺失或不合法：$why。无人值守时把 db_password=... 写进 --config 文件（权限 600）或用 db_password_file=<文件>。" "The database password is missing or invalid: $why. For unattended runs put db_password=... into the --config file (mode 600) or use db_password_file=<file>."
    if [ "$DB_TLS" = disable ]; then warn "数据库连接未加密（TLS disable）：口令与数据以明文在网络上传输，生产环境请改用 require/verify-full。" "The database connection is NOT encrypted (TLS disable): the password and data cross the network in clear text; use require/verify-full in production."; fi
}
# prepare_db_files: the CA certificate goes into the application data directory (mounted at /data in the container)
prepare_db_files() {
    [ "$DB_MODE" = external ] || return 0
    if [ -n "$DB_CA_FILE" ]; then
        mkdir -p "$APP_DIR"
        install -m 0644 "$DB_CA_FILE" "$APP_DIR/db-ca.pem"
        if [ "$(id -u)" -eq 0 ]; then chown 10001:10001 "$APP_DIR/db-ca.pem" 2>/dev/null || true; fi
    fi
}

# ---------------------------------------------------------------------------------------------- --check
# pf_disk DIR: free disk where the install directory will live (the gating counterpart of the info line printed by detection)
pf_disk() {
    local parent="$1" avail_kb
    while [ ! -d "$parent" ] && [ "$parent" != / ]; do parent="$(dirname -- "$parent")"; done
    avail_kb="$(df -Pk "$parent" 2>/dev/null | awk 'NR==2{print $4}' || echo 0)"; avail_kb="${avail_kb:-0}"
    if [ "$avail_kb" -lt 5242880 ]; then
        pf_fail "可用磁盘不足 5 GB，无法安装（镜像约需 2-3 GB，另需归档空间）。" "Less than 5 GB free disk: cannot install (the images need ~2-3 GB plus archive space)."
    elif [ "$avail_kb" -lt 20971520 ]; then
        pf_warn "可用磁盘不足 20 GB；归档文件会持续增长，请规划空间。" "Less than 20 GB free: archived files keep growing, plan your capacity."
    else pf_pass "磁盘空间充足（$((avail_kb / 1024 / 1024)) GB）" "Disk space ok ($((avail_kb / 1024 / 1024)) GB free)"; fi
}
# db_compose_check: the external-database layer uses "!reset", which needs Docker Compose 2.24+
db_compose_check() {
    [ "$DB_MODE" = external ] || return 0
    local cv; cv="$(docker compose version --short 2>/dev/null || true)"; cv="${cv#v}"
    if [ -n "$cv" ] && ! ver_ge "$(num_prefix "$cv")" 2.24; then
        pf_fail "使用已有数据库需要 Docker Compose 2.24+，当前是 $cv：请升级 compose 插件（只升级插件即可），或改用内置数据库。" "Using an existing database needs Docker Compose 2.24+, found $cv: upgrade the compose plugin (the plugin alone is enough) or use the bundled database."
    fi
    return 0
}

# run_check: everything it touches is read-only (no log file, no lock, no docker pull/load/login, no package or file changes).
run_check() {
    local i why
    step "只读检查（--check）：不会做任何更改" "Read-only check (--check): nothing will be changed"
    if [ "$(id -u)" -ne 0 ]; then pf_info "当前不是 root：实际安装需要 root（sudo）。" "Not running as root: the real installation needs root (sudo)."; fi
    pf_disk "$DIR"
    pf_ports "${ports_to_check[@]}"
    if [ "$DK_UP" = 1 ]; then db_compose_check
    elif have_docker; then pf_warn "Docker 已安装但守护进程不可用或缺少 Compose v2：安装时会尝试启动/补装。" "Docker is installed but the daemon is unavailable or Compose v2 is missing: the installer will try to start/complete it."
    else pf_warn "未安装 Docker：实际安装时需要 --install-docker（或交互确认）才会自动安装。" "Docker is not installed: the real installation needs --install-docker (or interactive consent) to install it."; fi
    if [ -n "$OFFLINE_FILE" ]; then pf_pass "镜像包 $OFFLINE_FILE：仓库 $OFFLINE_REG，标签 $OFFLINE_TAG（api/worker/deploy 齐全）" "Image bundle $OFFLINE_FILE: registry $OFFLINE_REG, tag $OFFLINE_TAG (api/worker/deploy present)"; offline_verify; fi
    if [ "$EXISTING" = 0 ]; then
        for i in "$ARCHIVE_DIR" "$SPOOL_DIR" "$APP_DIR" "$PG_DIR"; do
            if why="$(storage_problem "$i")"; then pf_pass "存储目录可用: $i" "Storage directory usable: $i"; else pf_fail "存储目录 $i：$why" "Storage directory $i: $why"; fi
        done
        orphan_scan "$DIR" "$APP_DIR" "$PG_DIR" "$ARCHIVE_DIR"
        if [ "$DB_MODE" != external ] && [ -n "$ORPH_PG" ] && [ ! -f "$DIR/.env" ]; then pf_fail "旧数据库文件 $ORPH_PG 没有对应的 .env：安装会被拒绝（见上面的说明）。" "Old database files in $ORPH_PG have no .env: the installation would be refused (see the explanation above)."; fi
    fi
    if [ "$DB_MODE" = external ]; then
        db_compose_check
        if [ "$DK_UP" = 1 ] && docker image inspect "$(db_image)" >/dev/null 2>&1 && [ -n "$(db_password_value)" ]; then
            local out rc=0; out="$(db_run db-check)" || rc=$?
            db_show "$out"
            if [ "$rc" -eq 0 ]; then pf_pass "外部数据库检查通过（只读）" "External database check passed (read-only)"; else pf_fail "外部数据库检查失败" "External database check failed"; fi
        else
            pf_info "外部数据库：--check 不拉取镜像，所以不连接数据库；正式安装时会先用 oa-agent-api 镜像检查连接、权限与隔离性，再征求建表批准。" "External database: --check pulls no image, so it does not connect; the real installation first checks connectivity, permissions and isolation with the oa-agent-api image, then asks to approve the schema."
        fi
    fi
    step "将执行的操作" "What would be done"
    local act="新安装" act_en="fresh installation" src src_en
    if [ "$EXISTING" = 1 ]; then
        act="修复/继续（保留 .env 与数据）"; act_en="repair/continue (.env and data kept)"
        if [ "$DO_UPGRADE" = 1 ]; then act="升级"; act_en="upgrade"; fi
        if [ "$REINSTALL" = 1 ]; then act="重装（重建容器，保留数据）"; act_en="reinstall (recreate containers, data kept)"; fi
    fi
    if [ -n "$OFFLINE_FILE" ]; then src="离线镜像包 $OFFLINE_FILE（docker load）"; src_en="offline bundle $OFFLINE_FILE (docker load)"
    elif [ -n "$REGISTRY" ]; then src="从 $REGISTRY 拉取 oa-agent-api/worker:$VERSION（安装包取自 oa-agent-deploy 镜像）"; src_en="pull $REGISTRY/oa-agent-api|worker:$VERSION (installer bundle from the oa-agent-deploy image)"
    else src="用源码在本机构建镜像"; src_en="build the images from source on this host"; fi
    say "  操作:       $act" "  action:       $act_en"
    say "  安装目录:   $DIR；端口 $PORT${DOMAIN:+；域名 $DOMAIN（$([ "$PROXY_MODE" = 1 ] && echo '放在已有反向代理后面' || echo 'HTTPS，需放行 80/443')）}" "  install dir:  $DIR; port $PORT${DOMAIN:+; domain $DOMAIN ($([ "$PROXY_MODE" = 1 ] && echo 'behind your existing reverse proxy' || echo 'HTTPS, needs 80/443'))}"
    say "  镜像来源:   $src" "  images:       $src_en"
    if [ "$DB_MODE" = external ]; then say "  数据库:     已有 $(db_label "$DB_PROVIDER") $DB_HOST:$DB_PORT/$DB_NAME（先检查再建表，建表需你确认）" "  database:     existing $(db_label "$DB_PROVIDER") $DB_HOST:$DB_PORT/$DB_NAME (checked first; creating tables needs your approval)"
    else say "  数据库:     内置 PostgreSQL 容器" "  database:     bundled PostgreSQL container"; fi
    if [ "$EXISTING" = 0 ]; then say "  存储目录:   归档 $ARCHIVE_DIR | 暂存 $SPOOL_DIR | 应用数据 $APP_DIR | 数据库 $PG_DIR" "  storage:      archive $ARCHIVE_DIR | spool $SPOOL_DIR | app data $APP_DIR | database $PG_DIR"; fi
    if ! have_docker; then docker_sources_order; say "  Docker:     将安装（安装源 ${SRC_ORDER}：$(docker_source_cmd "${SRC_ORDER%% *}")；失败回退发行版软件包）" "  Docker:       would be installed (source(s) ${SRC_ORDER}: $(docker_source_cmd "${SRC_ORDER%% *}"); distribution packages as fallback)"; fi
    if [ "${#PREREQ_MISSING[@]}" -gt 0 ]; then say "  软件包:     将只安装缺少的：${PREREQ_MISSING[*]}" "  packages:     only the missing ones would be installed: ${PREREQ_MISSING[*]}"; fi
    if [ "${#DOCKER_MIRRORS[@]}" -gt 0 ]; then say "  镜像加速:   ${DOCKER_MIRRORS[*]}（仅交互确认后写入 daemon.json）" "  registry mirror: ${DOCKER_MIRRORS[*]} (written to daemon.json only after interactive consent)"; fi
    say "  systemd:    $([ "$SYSTEMD" = yes ] && echo '安装' || echo '询问（--yes 下不安装）')；防火墙: $([ "$OPEN_FW" = yes ] && echo '放行端口' || echo '询问（--yes 下不修改）')" "  systemd:      $([ "$SYSTEMD" = yes ] && echo 'install' || echo 'ask (not with --yes)'); firewall: $([ "$OPEN_FW" = yes ] && echo 'open the port' || echo 'ask (untouched with --yes)')"
    say "  状态文件:   $STATE_FILE（成功后写入）" "  state file:   $STATE_FILE (written after success)"
    pf_finish
    say "这是只读检查，没有做任何更改。" "This was a read-only check; nothing was changed."
    if [ "$PF_FAIL" -gt 0 ]; then exit 3; fi
    exit 0
}

# ---------------------------------------------------------------------------------------------- existing installation: settings
# load_existing_settings: an existing .env decides everything; flags for storage are remembered and applied through oa-agentctl later
load_existing_settings() {
    say "检测到已有安装: $DIR（保留现有密钥与配置）。" "Existing installation detected: $DIR (keeping existing secrets and settings)."
    if [ "$MENU_DONE" = 0 ] && [ "$DO_UPGRADE" = 0 ] && [ "$ASSUME_YES" != 1 ]; then
        confirm "继续？将保留 .env 与全部数据，仅刷新文件并启动。" "Continue? .env and all data are kept; files are refreshed and the stack is started." y || die_cancel "已取消" "Cancelled"
    fi
    PORT="$(env_get OA_PORT)"; PORT="${PORT:-8080}"
    local old_domain; old_domain="$(env_get OA_DOMAIN)"
    [ -z "$DOMAIN" ] && DOMAIN="$old_domain"
    [ -z "$EMAIL" ] && EMAIL="$(env_get OA_ACME_EMAIL)"
    if [ "$(env_get OA_BEHIND_PROXY)" = 1 ]; then PROXY_MODE=1; fi
    case ":$(env_get COMPOSE_FILE):" in
        *:docker-compose.external-db.yml:*) DB_MODE=external; DB_PROVIDER="$(env_get OA_DB_PROVIDER)"; DB_HOST="$(env_get OA_DB_HOST)"; DB_PORT="$(env_get OA_DB_PORT)"; DB_NAME="$(env_get OA_DB_NAME)"; DB_USER="$(env_get OA_DB_USER)" ;;
        *) DB_MODE=bundled ;;
    esac
    PENDING_CORE=()
    [ -n "$ARCHIVE_DIR" ] && PENDING_CORE+=("archive=$ARCHIVE_DIR")
    [ -n "$SPOOL_DIR" ] && PENDING_CORE+=("spool=$SPOOL_DIR")
    [ -n "$APP_DIR" ] && PENDING_CORE+=("app=$APP_DIR")
    [ -n "$PG_DIR" ] && PENDING_CORE+=("pgdata=$PG_DIR")
    PENDING_EXTRA=(${EXTRA_MOUNTS[@]+"${EXTRA_MOUNTS[@]}"})
    load_mounts_from_env
    # the existing .env decides the image source: registry mode only when COMPOSE_FILE has no build layer
    case ":$(env_get COMPOSE_FILE):" in
        *:docker-compose.build.yml:*)
            if [ -n "$REGISTRY" ]; then warn "已有安装使用源码构建：忽略 --registry。" "Existing install builds from source: --registry ignored."; fi
            REGISTRY='' ;;
        *)
            local cur_reg cur_ver; cur_reg="$(env_get OA_IMAGE_REGISTRY)"; cur_ver="$(env_get OA_VERSION)"
            if [ -n "$REGISTRY" ] && [ "${REGISTRY%/}" != "$cur_reg" ]; then warn "已有安装使用 $cur_reg：忽略 --registry。" "Existing install uses $cur_reg: --registry ignored."; fi
            REGISTRY="$cur_reg"
            if [ -n "$cur_ver" ] && [ "$VERSION_SET" = 1 ] && [ "$VERSION" != "$cur_ver" ] && [ "$DO_UPGRADE" = 0 ]; then
                warn "已有安装的镜像标签是 $cur_ver，保持不变（切换标签请用 --upgrade --image-tag $VERSION 或 oa-agentctl upgrade --tag $VERSION）。" "The installed image tag is $cur_ver and stays (to switch use --upgrade --image-tag $VERSION or oa-agentctl upgrade --tag $VERSION)."
            fi
            if [ "$DO_UPGRADE" = 1 ] && [ "$VERSION_SET" = 1 ] && [ -n "$cur_ver" ]; then
                case "$(ver_relation "$cur_ver" "$VERSION")" in
                    older) if [ "$ALLOW_DOWNGRADE" != 1 ]; then die_usage "拒绝降级：已安装 $cur_ver，请求 $VERSION。数据库迁移不会自动回退，旧版本可能无法读取新结构；确需回退请先备份并加 --allow-downgrade，或用 oa-agentctl restore 恢复升级前备份。" "Refusing to downgrade: installed $cur_ver, requested $VERSION. Database migrations are not reversed automatically and the old version may not read the new schema; if you really need it back up first and add --allow-downgrade, or restore the pre-upgrade backup with oa-agentctl restore."; fi
                        warn "按 --allow-downgrade 降级 $cur_ver -> $VERSION：风险自负，请确认已有备份。" "Downgrading $cur_ver -> $VERSION because of --allow-downgrade: at your own risk, make sure a backup exists." ;;
                    different) warn "无法比较 $cur_ver 与 $VERSION 的新旧：请确认这是升级而不是降级。" "Cannot tell whether $VERSION is newer than $cur_ver: make sure this is an upgrade, not a downgrade." ;;
                esac
            fi
            if [ -n "$cur_ver" ] && { [ "$VERSION_SET" = 0 ] || [ "$DO_UPGRADE" = 0 ]; }; then VERSION="$cur_ver"; fi ;;
    esac
}
# resolve_project: the compose project name is fixed at the first install (renaming it would orphan the running containers)
# and must not collide with another installation on this host (they would take over each other's containers).
resolve_project() {
    local cur_proj i n=2 cand
    if [ "$EXISTING" = 1 ]; then
        cur_proj="$(env_get COMPOSE_PROJECT_NAME)"; cur_proj="${cur_proj:-oa-agent}"
        if [ -n "$PROJECT" ] && [ "$PROJECT" != "$cur_proj" ]; then warn "已有安装：忽略 --project（保持 $cur_proj）。" "Existing install: --project ignored (keeping $cur_proj)."; fi
        PROJECT="$cur_proj"
    else
        PROJECT="${PROJECT:-oa-agent}"
        for i in "${!IN_DIR[@]}"; do
            [ "${IN_DIR[$i]}" != "$DIR" ] || continue
            [ "${IN_PROJ[$i]}" = "$PROJECT" ] || continue
            if [ "$PROJECT_SET" = 1 ]; then
                die_usage "Compose 项目名 $PROJECT 已被 ${IN_DIR[$i]} 的安装占用：同名项目会互相接管容器。请换一个 --project 名称。" "The compose project name $PROJECT is already used by the installation in ${IN_DIR[$i]}: projects with the same name take over each other's containers. Choose another --project name."
            fi
            cand="$PROJECT-$n"
            while :; do
                local taken=0 j; for j in "${!IN_PROJ[@]}"; do [ "${IN_PROJ[$j]}" != "$cand" ] || taken=1; done
                [ "$taken" = 1 ] || break
                n=$((n + 1)); cand="oa-agent-$n"
            done
            say "项目名 $PROJECT 已被 ${IN_DIR[$i]} 的安装使用，本次改用 $cand（避免互相接管容器）。" "The project name $PROJECT is used by the installation in ${IN_DIR[$i]}; using $cand instead (so the two never take over each other's containers)."
            PROJECT="$cand"
        done
    fi
    PROJECT="${PROJECT:-oa-agent}"
}
# state_after_uninstall: the containers are gone; the data stays unless --purge (then the entry is forgotten, otherwise kept as "removed"
# so that the data directories can still be found as orphaned data later)
state_after_uninstall() {
    state_has "$DIR" || return 0
    if [ "$PURGE" = 1 ]; then state_forget "$DIR"; elif [ -f "$DIR/.env" ]; then state_write removed; fi
    return 0
}
network_needed() { [ "$STATUS_REQ" = 0 ] && [ "$DO_UNINSTALL" = 0 ]; }
quick_find() { DK_UP=0; if have_docker && docker info >/dev/null 2>&1; then DK_UP=1; fi; load_containers; find_installs; }
status_all() {
    local i
    if [ "${#IN_DIR[@]}" -eq 0 ]; then say "没有找到 OA Agent 安装（已查状态文件、容器、compose 项目、默认目录）。可直接运行 install.sh 安装。" "No OA Agent installation found (checked the state file, containers, compose projects and the default directory). Run install.sh to install."; return 0; fi
    for i in "${!IN_DIR[@]}"; do DIR="${IN_DIR[$i]}"; status_report "$i"; done
}

# ---------------------------------------------------------------------------------------------- main
main() {
    # Truncation guard: the last line of this file passes the sentinel as the final argument (see the header comment).
    if [ $# -eq 0 ] || [ "${!#}" != "$EOS_SENTINEL" ]; then
        printf '%s[FAIL]%s 脚本不完整（下载被截断？），未执行任何操作。请重新下载后再运行。 / The script is incomplete (truncated download?); nothing was executed. Download it again and re-run.\n' "$C_R" "$C_0" >&2
        exit 1
    fi
    set -- "${@:1:$#-1}"
    init_lang
    parse_args "$@"
    local ro=0
    if [ "$CHECK" = 1 ] || [ "$STATUS_ONLY" = 1 ] || [ "$PRINT_CONFIG" = 1 ]; then ro=1; fi   # read-only runs need no root, no log, no lock
    if [ "$CHECK" = 1 ] || [ "$STATUS_ONLY" = 1 ]; then ASSUME_YES=1; fi                       # ... and never prompt
    if [ "$PRINT_CONFIG" = 1 ]; then exec 3>&1 1>&2; fi                                         # stdout carries only the answers
    load_env_answers
    load_config_file
    if [ "$ro" != 1 ]; then require_root; setup_log; fi
    if [ "$ASSUME_YES" != 1 ]; then open_tty; fi
    if [ "$ro" != 1 ]; then lock_install; fi
    ask_language                       # the first question (interactive, language not given), or one line saying which language is used
    banner
    logf "install.sh started (dir=$DIR version=$VERSION yes=$ASSUME_YES lang=$UI)"
    if [ "$DB_MODE" = bundled ] && [ -n "$DB_HOST$DB_NAME$DB_USER$DB_PASSWORD$DB_PASSWORD_FILE$DB_CA_FILE$DB_TLS$DB_SCHEMA" ]; then die_usage "选了内置数据库，却给了外部数据库的参数（--db-*）。" "The bundled database was chosen but external-database options (--db-*) were given."; fi
    if [ -z "$DB_MODE" ] && [ -n "$DB_PROVIDER$DB_HOST" ]; then DB_MODE=external; fi
    if [ "$STATUS_ONLY" = 1 ] && { [ "$DO_UPGRADE" = 1 ] || [ "$REINSTALL" = 1 ] || [ "$DO_UNINSTALL" = 1 ] || [ "$PURGE" = 1 ] || [ "$PRINT_CONFIG" = 1 ]; }; then die_usage "--status 不能与改动类选项同时使用。" "--status cannot be combined with options that change things."; fi
    if [ "$PRINT_CONFIG" = 1 ] && { [ "$DO_UPGRADE" = 1 ] || [ "$REINSTALL" = 1 ] || [ "$DO_UNINSTALL" = 1 ] || [ "$CHECK" = 1 ]; }; then die_usage "--print-config 只用于新安装的答案。" "--print-config is only for the answers of a fresh installation."; fi

    valid_dir "$DIR" || die_usage "安装目录不合法: $DIR（须为绝对路径，仅含字母数字 _ . / -，且不能是系统目录）" "Invalid install dir: $DIR (absolute path, only alphanumerics _ . / -, and not a system directory)"
    DIR="${DIR%/}"

    # ---- uninstall needs only to know WHICH installation (no detection table)
    if [ "$DO_UNINSTALL" = 1 ]; then
        quick_find; resolve_target
        uninstall_flow
        state_after_uninstall
        exit 0
    fi

    # ---- 1. DETECT (read-only): what is already on this server -> drives every decision below
    detect_local
    if [ "$STATUS_ONLY" = 1 ]; then status_all; exit 0; fi
    resolve_target
    if [ "$EXISTING" = 1 ]; then decide_existing
    else
        [ "$DO_UPGRADE" = 0 ] || die "没有找到可升级的安装: $DIR" "No installation to upgrade in $DIR"
        [ "$REINSTALL" = 0 ] || die "没有找到可重装的安装: $DIR" "No installation to reinstall in $DIR"
        if [ "$ASSUME_YES" != 1 ] && ! have_tty; then die_usage "$NOTTY_MSG_ZH" "$NOTTY_MSG_EN"; fi
    fi
    if [ "$DO_UNINSTALL" = 1 ]; then uninstall_flow; state_after_uninstall; exit 0; fi
    if [ "$STATUS_REQ" = 1 ]; then
        status_report "$TGT"
        if [ "$ro" != 1 ] && ! state_has "$DIR"; then state_write installed; fi
        if [ "$PORT_SET" = 1 ] && [ "$PORT" != "${IN_PORT[$TGT]}" ]; then warn "已忽略 --port $PORT：已有安装使用端口 ${IN_PORT[$TGT]}（更改：oa-agentctl config set OA_PORT <端口>）。" "Ignored --port $PORT: the installation uses port ${IN_PORT[$TGT]} (change it with oa-agentctl config set OA_PORT <port>)."; fi
        exit 0
    fi
    if network_needed; then
        det_network
        if ! have_docker && [ -z "$OFFLINE_FILE" ]; then announce_docker_source; fi   # automatic, never hidden: one line says what was detected and used
    fi

    # ---- 2. DECIDE the answers: orphaned data first, then the wizard (fresh) or the existing .env
    if [ "$EXISTING" = 0 ]; then orphan_decide; fi
    if [ "$EXISTING" = 0 ]; then
        step "配置向导" "Configuration wizard"
        if [ "$ASSUME_YES" != 1 ] && have_tty; then say "一次一个问题；方括号里是根据检测结果给出的默认值，直接回车采用。全部回答后会有复核页，可逐项修改。" "One question at a time; the value in brackets is a default derived from the detection, press Enter to accept it. A review screen follows where every setting can be edited."; fi
        wizard_answers
        local wiz_why
        if wiz_why="$(storage_overlap)"; then die "$wiz_why" "$wiz_why"; fi
        review_screen
        if wiz_why="$(storage_overlap)"; then die "$wiz_why" "$wiz_why"; fi
    else
        load_existing_settings
    fi
    resolve_project
    [ -z "$MIRROR" ] && MIRROR=default
    case "$MIRROR" in cn | default) ;; *) die_usage "--mirror 只能是 cn 或 default" "--mirror must be cn or default" ;; esac
    valid_port "$PORT" || die_usage "端口不合法: $PORT" "Invalid port: $PORT"
    [[ "$PROJECT" =~ ^[a-z0-9][a-z0-9_-]{0,30}$ ]] || die_usage "项目名不合法: $PROJECT（小写字母/数字/-/_，最多 31 个字符）" "Invalid project name: $PROJECT (lowercase letters/digits/-/_, at most 31 characters)"
    if [ -n "$DOMAIN" ]; then
        valid_domain "$DOMAIN" || die_usage "域名不合法: $DOMAIN（需要完整域名，不能是 IP）" "Invalid domain: $DOMAIN (a full domain name is required, not an IP)"
    fi
    if [ "$PROXY_MODE" = 1 ] && [ -z "$DOMAIN" ]; then die_usage "--behind-proxy 需要同时给出 --domain（反向代理服务的域名）。" "--behind-proxy needs --domain (the name the reverse proxy serves)."; fi
    if [ -n "$EMAIL" ]; then valid_email "$EMAIL" || die_usage "邮箱不合法: $EMAIL" "Invalid e-mail: $EMAIL"; fi
    [[ "$VERSION" =~ ^[A-Za-z0-9._-]{1,64}$ ]] || die_usage "版本号不合法" "Invalid version"
    REGISTRY="${REGISTRY%/}"
    if [ -n "$REGISTRY" ]; then [[ "$REGISTRY" =~ ^[A-Za-z0-9._-]+(:[0-9]{1,5})?(/[a-z0-9._/-]+)?$ ]] || die_usage "镜像仓库地址不合法（应为 主机[:端口][/命名空间]，不含 https://）" "Invalid registry (expected host[:port][/namespace], without https://)"; fi
    if [ -n "$OFFLINE_FILE" ]; then offline_apply; REGISTRY="${REGISTRY%/}"; fi
    if [ "$DB_MODE" = external ] && [ "$EXISTING" = 0 ]; then validate_db_settings; fi
    [ -n "$DB_MODE" ] || DB_MODE=bundled

    # ---- profile selection
    BIND='0.0.0.0'; TRUSTED=0; FW_PORTS="$PORT"
    local ports_to_check=("$PORT")
    if [ -n "$DOMAIN" ] && [ "$PROXY_MODE" = 1 ]; then
        BIND=127.0.0.1; TRUSTED=1; FW_PORTS="$(m '（由你的反向代理自己的端口）' "(your reverse proxy's own ports)")"
    elif [ -n "$DOMAIN" ]; then
        BIND=127.0.0.1; TRUSTED=1; FW_PORTS='80/443'
        ports_to_check=(80 443)
        [ "$PORT" != 80 ] && [ "$PORT" != 443 ] || die_usage "使用域名时 --port 是内部端口，不能为 80/443。" "With a domain, --port is the internal port and must not be 80/443."
        ports_to_check+=("$PORT")
    fi
    PUBLIC_URL="http://$(host_ip):$PORT"
    [ -z "$DOMAIN" ] || PUBLIC_URL="https://$DOMAIN"
    COMPOSE_FILES='docker-compose.yml:docker-compose.mounts.yml:docker-compose.build.yml'
    [ -z "$REGISTRY" ] || COMPOSE_FILES='docker-compose.yml:docker-compose.mounts.yml'
    [ "$DB_MODE" != external ] || COMPOSE_FILES="$COMPOSE_FILES:docker-compose.external-db.yml"
    B_GOPROXY=''; B_NPM=''; B_PIP=''
    if [ "$MIRROR" = cn ]; then
        B_GOPROXY='https://goproxy.cn,direct'; B_NPM='https://registry.npmmirror.com'; B_PIP='https://pypi.tuna.tsinghua.edu.cn/simple'
    fi
    TZ_NAME="$(detect_tz)"

    if [ "$CHECK" = 1 ]; then run_check; fi
    if [ "$PRINT_CONFIG" = 1 ]; then
        print_config
        say "答案已写到标准输出（不含任何密钥）；未做任何更改，也未检查数据库连接。" "The answers were written to standard output (no secrets); nothing was changed and the database connection was not checked."
        exit 0
    fi

    # ---- 3. GATE: nothing is installed or changed before this report is clean
    step "环境检查" "Preflight checks"
    pf_disk "$DIR"
    pf_ports "${ports_to_check[@]}"
    pf_finish
    if [ "$EXISTING" = 0 ]; then orphan_gate; fi
    install_prereqs
    ensure_docker
    if [ "$DK_EVAL" = 0 ]; then det_docker; det_docker_space; fi   # Docker was installed / started during this run
    db_compose_check
    configure_docker_mirror
    pf_finish
    if [ -n "$OFFLINE_FILE" ]; then offline_verify; offline_load
    elif [ -n "$REGISTRY" ]; then registry_login; fi
    local db_deferred=0
    if [ "$DB_MODE" = external ] && [ "$EXISTING" = 0 ]; then
        if [ -n "$REGISTRY" ] || [ -n "$OFFLINE_FILE" ]; then db_gate; db_secret_key_gate; db_init_flow
        else db_deferred=1; fi
    fi

    # ---- 4. INSTALL: source + files
    step "准备文件" "Preparing files"
    if find_local_source; then say "使用本地源码: $SRC_ROOT" "Using local source: $SRC_ROOT"
    elif [ -n "$REGISTRY" ]; then fetch_registry_bundle
    else download_source; fi
    prepare_dirs
    write_marker
    sync_source
    install_ctl
    prepare_mounts
    if [ ! -f "$DIR/mounts.json" ]; then write_mounts_json; fi
    if [ ! -f "$(envf)" ]; then
        prepare_db_files
        write_new_env
        ok "已生成 $(envf)（权限 600，含随机密钥）" "Generated $(envf) (mode 600, random secrets)"
        act_done "生成 .env（权限 600，随机密钥$([ -n "$KEY_FROM_FILE" ] && echo '；OA_SECRET_KEY 沿用已有密钥')）" "Generated .env (mode 600, random secrets$([ -n "$KEY_FROM_FILE" ] && echo '; OA_SECRET_KEY reused from the existing key'))"
        [ -z "$DOMAIN" ] || [ "$PROXY_MODE" = 1 ] || ctl_yes domain --no-start "$DOMAIN"
    else
        chmod 600 "$(envf)"
        # existing install: secrets are never touched; only an explicitly requested domain is applied
        if [ -n "$DOMAIN" ] && [ "$PROXY_MODE" = 0 ] && { [ "$DOMAIN" != "$(env_get OA_DOMAIN)" ] || [ ! -f "$DIR/Caddyfile" ]; }; then
            if [ "$ASSUME_YES" != 1 ] && [ -n "$(env_get OA_DOMAIN)" ]; then
                confirm "更换域名会使已注册的 Passkey 失效，继续？" "Changing the domain invalidates registered Passkeys. Continue?" n || die_cancel "已取消" "Cancelled"
            fi
            ctl_yes domain --no-start "$DOMAIN"
        fi
        ok "保留现有 .env（密钥未改动）" "Kept existing .env (secrets untouched)"
        act_skip "已有 .env 与密钥保持不变" "The existing .env and secrets were left unchanged"
    fi
    apply_pending_storage
    apply_mounts

    offer_systemd

    if [ "$NO_START" = 1 ]; then
        state_write installed
        ok "已按 --no-start 停止在此；运行 oa-agentctl start 启动。" "Stopped here (--no-start); run oa-agentctl start to launch."
        exit 0
    fi

    if [ "$DO_UPGRADE" = 1 ]; then
        step "升级" "Upgrading"
        if [ -n "$REGISTRY" ] && [ "$VERSION_SET" = 1 ]; then ctl upgrade --tag "$VERSION"; else ctl upgrade; fi
        state_write installed
        exit 0
    fi

    if [ "$REINSTALL" = 1 ]; then
        step "重装：移除旧容器（数据与 .env 保留）" "Reinstall: removing the old containers (data and .env are kept)"
        dcw down --remove-orphans 2>&1 | tee -a "$LOG_FILE" || die "docker compose down 失败" "docker compose down failed"
    fi

    # ---- build / pull, start
    step "获取镜像" "Obtaining images"
    if [ -z "$REGISTRY" ]; then
        say "首次构建需要下载依赖并编译前端/后端，通常需 5-15 分钟。" "The first build downloads dependencies and compiles the frontend/backend; it usually takes 5-15 minutes."
        if ! net_retry "docker compose build" dcw build --progress=plain; then die "镜像构建失败（详见 $LOG_FILE；中国大陆可加 --mirror cn）" "Image build failed (see $LOG_FILE; in mainland China try --mirror cn)"; fi
    elif [ -n "$OFFLINE_FILE" ]; then
        say "离线模式：使用已载入的镜像，不访问镜像仓库。" "Offline mode: using the loaded images, no registry access."
    else
        say "从 $REGISTRY 拉取镜像（首次约 1-2 GB，含 PostgreSQL 基础镜像；网络抖动会自动重试，已下载的层可续传）：" "Pulling images from $REGISTRY (about 1-2 GB the first time, including the PostgreSQL base image; transient network errors are retried and finished layers are resumed):"
        say "  $(registry_images)" "  $(registry_images)"
        pull_stack_images
    fi
    if [ "$db_deferred" = 1 ]; then db_gate; db_secret_key_gate; db_init_flow; fi
    step "启动服务" "Starting services"
    if ! dcw up -d 2>&1 | tee -a "$LOG_FILE"; then die "docker compose up 失败" "docker compose up failed"; fi
    ctl_yes wait --timeout 900 || die "服务未能在期限内就绪（上方已列出诊断信息）。" "Services did not become healthy in time (diagnostics above)."
    state_write installed

    if [ -n "$DOMAIN" ] && [ "$PROXY_MODE" = 0 ]; then offer_firewall 80 443; elif [ "$PROXY_MODE" = 1 ]; then :; else offer_firewall "$PORT"; fi
    summary
    logf "install finished in $(($(date +%s) - STARTED_AT))s"
}

main "$@" "$EOS_SENTINEL"
