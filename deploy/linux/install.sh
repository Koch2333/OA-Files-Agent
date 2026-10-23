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
# Is a docker CLI installed? (Test mode with OA_TEST_DOCKER_SHIM: "installed" means that shim file exists, so a docker on
# the host running the offline tests, e.g. a CI runner's, cannot leak into the "docker is missing" scenarios.)
have_docker() {
    if [ "$TEST_MODE" = 1 ] && [ -n "${OA_TEST_DOCKER_SHIM:-}" ]; then [ -x "$OA_TEST_DOCKER_SHIM" ]; else command -v docker >/dev/null 2>&1; fi
}
if [ "$TEST_MODE" = 1 ]; then
    LOG_FILE="${OA_INSTALL_LOG:-$LOG_FILE}"; BIN_DIR="${OA_INSTALL_BIN_DIR:-$BIN_DIR}"; UNIT_DIR="${OA_INSTALL_UNIT_DIR:-$UNIT_DIR}"
    ETC_MARKER="${OA_INSTALL_ETC_MARKER:-$ETC_MARKER}"
    LOCK_FILE="${OA_INSTALL_LOCK:-$LOCK_FILE}"; DAEMON_JSON="${OA_DAEMON_JSON:-$DAEMON_JSON}"
    OS_RELEASE="${OA_OS_RELEASE:-$OS_RELEASE}"; MEMINFO="${OA_MEMINFO:-$MEMINFO}"; TTY_DEV="${OA_INSTALL_TTY:-$TTY_DEV}"
fi

TMP=''
CTL_BINARY=0
PF_PASS=0; PF_WARN=0; PF_FAIL=0; PF_NOTES=()   # pre-flight counters; the warnings are repeated in the final summary
RETRY_BASE="${OA_RETRY_BASE:-5}"               # back-off base in seconds for network retries (5, 15, 45, ...)
EOS_SENTINEL='__OA_INSTALL_END_OF_SCRIPT__'
LOG_OK=0
STARTED_AT="$(date +%s)"

# ---------------------------------------------------------------------------------------------- UI
case "${OA_LANG:-${LC_ALL:-${LANG:-}}}" in zh* | *zh_* | *ZH*) UI=zh ;; *) UI=en ;; esac
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
pf_pass() { local s; s="$(m "$1" "$2")"; PF_PASS=$((PF_PASS + 1)); printf '%s[PASS]%s %s\n' "$C_G" "$C_0" "$s"; logf "PASS $s"; }
pf_warn() { local s; s="$(m "$1" "$2")"; PF_WARN=$((PF_WARN + 1)); PF_NOTES+=("$s"); printf '%s[WARN]%s %s\n' "$C_Y" "$C_0" "$s" >&2; logf "WARN $s"; }
pf_fail() { local s; s="$(m "$1" "$2")"; PF_FAIL=$((PF_FAIL + 1)); printf '%s[FAIL]%s %s\n' "$C_R" "$C_0" "$s" >&2; logf "FAIL $s"; }
pf_info() { local s; s="$(m "$1" "$2")"; printf '%s[INFO]%s %s\n' "$C_B" "$C_0" "$s"; logf "INFO $s"; }

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
  --mirror cn|default 中国大陆镜像：Docker 安装使用阿里云源，构建使用 goproxy.cn / npmmirror / 清华 PyPI
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
  --install-docker    缺少 Docker 时允许自动安装（--yes 模式下必须显式给出）
  --systemd           安装开机自启 systemd 单元（默认询问；--yes 下不安装）
  --open-firewall     在检测到 ufw/firewalld 时放行端口（默认询问；--yes 下不修改）
  --skip-verify       下载发行包时不强制校验和（不推荐）
  --no-start          只配置，不构建/启动
  --check, --dry-run  只读检查：打印环境检查报告（PASS/WARN/FAIL）与将要执行的操作，不做任何更改（不需要 root；有 FAIL 时退出码 3）
  --offline <文件.tar>  离线安装：用 docker load 载入 scripts/save-images.sh 生成的镜像包（api/worker/deploy，需同目录的 <文件>.sha256 校验），不访问镜像仓库
  --docker-mirror <URL>  把 Docker Hub 镜像加速地址写入 /etc/docker/daemon.json（可重复；只在交互确认后才修改，先备份、安全合并，重启 Docker 需再次确认；--yes 下只打印手动步骤）
  --detect-public-ip  结束时探测外网地址（默认从不访问第三方服务；先试云厂商元数据，再试 OA_PUBLIC_IP_URL，默认 https://api.ipify.org）
  --reinstall         对已有安装重建容器（保留 .env 与全部数据，重新拉取镜像并启动）
  --upgrade           升级现有安装（刷新源码与脚本，备份后重建并重启）
  --uninstall         卸载容器（数据保留）；加 --purge 同时删除数据
  --purge             与 --uninstall 联用
  --lang zh|en        界面语言（默认按 $LANG）
  -h, --help          显示本帮助

退出码: 0 成功 | 1 失败 | 2 参数错误 | 3 环境检查未通过 | 4 用户取消 | 130 被中断。已有安装且交互运行时，会询问：修复/升级/重装(保留数据)/卸载/取消。
EOF
    else
        cat <<'EOF'
Usage: install.sh [options]

  --dir <path>        install directory (default /opt/oa-agent)
  --port <port>       HTTP port (default 8080; with a domain the API only listens on 127.0.0.1)
  --domain <fqdn>     enable HTTPS (Caddy automatic certificates; 80/443 must be reachable, DNS must point here)
  --email <address>   certificate expiry notices (optional)
  --mirror cn|default China mirrors: Aliyun for Docker install; goproxy.cn / npmmirror / Tsinghua PyPI for builds
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
  --install-docker    allow installing Docker when missing (required explicitly with --yes)
  --systemd           install a start-on-boot systemd unit (asked by default; not installed with --yes)
  --open-firewall     open the port when ufw/firewalld is detected (asked by default; untouched with --yes)
  --skip-verify       do not insist on a checksum for downloaded releases (not recommended)
  --no-start          configure only; do not build/start
  --check, --dry-run  read-only check: print the pre-flight report (PASS/WARN/FAIL) and what would be done; changes nothing (no root needed; exit 3 when there are FAIL lines)
  --offline <file.tar>  offline install: docker load the image bundle made by scripts/save-images.sh (api/worker/deploy; needs <file>.sha256 next to it), no registry access
  --docker-mirror <URL>  add a Docker Hub registry mirror to /etc/docker/daemon.json (repeatable; modified only after interactive consent: backed up, safely merged, the Docker restart needs a second consent; with --yes only the manual steps are printed)
  --detect-public-ip  look up the external address at the end (third-party services are never contacted by default; cloud metadata first, then OA_PUBLIC_IP_URL, default https://api.ipify.org)
  --reinstall         recreate the containers of an existing install (.env and all data kept; images are pulled again)
  --upgrade           upgrade an existing install (refresh source and scripts, back up, rebuild, restart)
  --uninstall         remove containers (data kept); with --purge also delete the data
  --purge             combine with --uninstall
  --lang zh|en        UI language (default from $LANG)
  -h, --help          this help

Exit codes: 0 ok | 1 error | 2 invalid argument | 3 pre-flight failed | 4 cancelled | 130 interrupted. Interactive runs against an existing install ask: repair / upgrade / reinstall (keeps data) / uninstall / cancel.
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
    printf '\n%s[FAIL]%s %s\n' "$C_R" "$C_0" "$(m "已中断。可安全地重新运行本脚本（密钥不会被改变；Docker 的镜像下载会续传已完成的层）。" "Interrupted. It is safe to re-run this script (secrets are never changed; Docker resumes the layers it already pulled).")" >&2
    exit 130
}
trap 'on_err $LINENO' ERR
trap on_int INT TERM
trap cleanup EXIT

# lock_install: one installer at a time per host (flock when available); the lock is released when the process exits
lock_install() {
    command -v flock >/dev/null 2>&1 || return 0
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
            --dir) need_val "$@"; DIR="$2"; shift 2 ;;
            --port) need_val "$@"; PORT="$2"; shift 2 ;;
            --domain) need_val "$@"; DOMAIN="$2"; shift 2 ;;
            --email) need_val "$@"; EMAIL="$2"; shift 2 ;;
            --mirror) need_val "$@"; MIRROR="$2"; shift 2 ;;
            --version | --image-tag) need_val "$@"; VERSION="$2"; VERSION_SET=1; shift 2 ;;
            --repo-url) need_val "$@"; REPO_URL="$2"; shift 2 ;;
            --registry) need_val "$@"; REGISTRY="$2"; REGISTRY_SET=1; shift 2 ;;
            --build-from-source | --source) BUILD_FLAG=1; shift ;;
            --project) need_val "$@"; PROJECT="$2"; shift 2 ;;
            --lang) need_val "$@"; case "$2" in zh*) UI=zh ;; *) UI=en ;; esac; shift 2 ;;
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
ask_valid() {
    local var="$1" zh="$2" en="$3" def="$4" chk="$5" why tries=0
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
    if { [ "$ASSUME_YES" != 1 ] && have_tty; } && port_in_use "$1"; then m "端口 $1 已被占用（ss -ltnp | grep :$1 查看占用者），请换一个" "port $1 is already in use (ss -ltnp | grep :$1 shows who), choose another"; return 1; fi
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
env_get() {
    local line v
    [ -f "$(envf)" ] || return 0
    line="$(grep -E "^$1=" "$(envf)" | tail -n 1 || true)"
    v="${line#*=}"; v="${v%% #*}"; v="${v%"${v##*[![:space:]]}"}"
    printf '%s' "$v"
}
env_set() {
    local tmp
    tmp="$(mktemp "$DIR/.env.XXXXXX")"
    if [ -f "$(envf)" ]; then grep -v -E "^$1=" "$(envf)" >"$tmp" || true; fi
    printf '%s=%s\n' "$1" "$2" >>"$tmp"
    chmod 600 "$tmp"
    mv -f "$tmp" "$(envf)"
}
gen_hex() { # gen_hex NBYTES
    if command -v openssl >/dev/null 2>&1; then openssl rand -hex "$1"; else head -c "$1" /dev/urandom | od -An -tx1 | tr -d ' \n'; fi
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
    OA_OFFLINE="$off" OA_LANG="$UI" OA_UNIT_DIR="$UNIT_DIR" OA_CTL_SELF="$BIN_DIR/oa-agentctl" bash "$c" "${flags[@]}" "$@"
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
    if command -v ss >/dev/null 2>&1; then ss -ltn 2>/dev/null | awk '{print $4}' | grep -Eq "[:.]$1\$"
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
    if [ "$avail_kb" -lt 5242880 ]; then
        pf_fail "可用磁盘不足 5 GB，无法安装（镜像约需 2-3 GB，另需归档空间）。" "Less than 5 GB free disk: cannot install (the images need ~2-3 GB plus archive space)."
    elif [ "$avail_kb" -lt 20971520 ]; then
        pf_warn "可用磁盘不足 20 GB；归档文件会持续增长，请规划空间。" "Less than 20 GB free: archived files keep growing, plan your capacity."
    else pf_pass "磁盘空间充足（$((avail_kb / 1024 / 1024)) GB）" "Disk space ok ($((avail_kb / 1024 / 1024)) GB free)"; fi
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
    # firewall (never modified without asking)
    if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -qi '^Status: active'; then
        pf_info "防火墙 ufw 已启用：外部访问需要放行端口（安装结束时会询问，不会擅自修改）。" "Firewall ufw is active: open the port for external access (asked at the end, never changed on its own)."
    elif command -v firewall-cmd >/dev/null 2>&1 && firewall-cmd --state >/dev/null 2>&1; then
        pf_info "防火墙 firewalld 已启用：外部访问需要放行端口（安装结束时会询问，不会擅自修改）。" "Firewall firewalld is active: open the port for external access (asked at the end, never changed on its own)."
    fi
}

pf_ports() { # PORT...: a port held by our own running stack is fine
    local p bad=0
    for p in "$@"; do
        if port_in_use "$p"; then
            if [ -f "$(envf)" ] && have_docker && ctl_running_port "$p"; then continue; fi
            bad=1
            pf_fail "端口 $p 已被占用。请换一个端口（--port）或停止占用它的程序（ss -ltnp | grep :$p）。" "Port $p is already in use. Choose another port (--port) or stop the program using it (ss -ltnp | grep :$p)."
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

# pf_docker: checks that need a working docker CLI + daemon
pf_docker() {
    local sv cv root kb sec
    sv="$(docker version --format '{{.Server.Version}}' 2>/dev/null || true)"; cv="$(docker compose version --short 2>/dev/null || true)"
    if [ -z "$sv" ]; then pf_warn "无法读取 Docker 服务端版本。" "Cannot read the Docker server version."
    elif ! ver_ge "$(num_prefix "$sv")" 20.10; then pf_fail "Docker $sv 过旧（需要 20.10 以上，建议 24+）；请升级 Docker Engine。" "Docker $sv is too old (20.10+ required, 24+ recommended); upgrade Docker Engine."
    else pf_pass "Docker $sv / compose ${cv:-?}" "Docker $sv / compose ${cv:-?}"; fi
    root="$(docker info --format '{{.DockerRootDir}}' 2>/dev/null || true)"
    if [ -n "$root" ] && [ -d "$root" ]; then
        kb="$(df -Pk "$root" 2>/dev/null | awk 'NR==2{print $4}' || echo 0)"; kb="${kb:-0}"
        if [ "$kb" -lt 5242880 ]; then pf_warn "Docker 数据目录 $root 所在磁盘剩余不足 5 GB（镜像约 2-3 GB，升级时会同时存在新旧两套）。" "Less than 5 GB free where Docker stores images ($root); the images need ~2-3 GB and an upgrade briefly holds two sets."
        else pf_pass "Docker 数据目录 $root 空间充足" "Docker data directory $root has enough space"; fi
    fi
    sec="$(docker info --format '{{.SecurityOptions}}' 2>/dev/null || true)"
    case "$sec" in *rootless*) pf_warn "检测到 rootless Docker：绑定挂载目录的属主（uid 10001/70）与 1024 以下端口行为不同，本安装器按 rootful Docker 测试。" "Rootless Docker detected: bind-mount ownership (uid 10001/70) and ports below 1024 behave differently; this installer is tested with rootful Docker." ;; esac
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
    command -v curl >/dev/null 2>&1 || return 1
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
    command -v curl >/dev/null 2>&1 || return 0
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

# docker_official_script: Docker Inc.'s get.docker.com script (third-party, informed); with --mirror cn the Aliyun mirror is
# tried first, then AzureChinaCloud. Returns 1 when every attempt failed (the caller falls back to distro packages).
docker_official_script() {
    TMP="${TMP:-$(mktemp -d)}"
    command -v curl >/dev/null 2>&1 || install_pkg curl || return 1
    local mirrors=('') mir
    [ "$MIRROR" != cn ] || mirrors=(Aliyun AzureChinaCloud)
    if ! net_retry "get.docker.com" curl -fsSL --connect-timeout 10 --max-time 120 https://get.docker.com -o "$TMP/get-docker.sh"; then
        warn "下载 get.docker.com 脚本失败。" "Failed to download the get.docker.com script."
        return 1
    fi
    for mir in "${mirrors[@]}"; do
        if sh "$TMP/get-docker.sh" ${mir:+--mirror "$mir"} 2>&1 | tee -a "$LOG_FILE" && have_docker; then return 0; fi
        warn "get.docker.com 脚本${mir:+（镜像 $mir）}执行失败${mir:+，尝试下一个}。" "The get.docker.com script${mir:+ (mirror $mir)} failed${mir:+; trying the next one}."
    done
    return 1
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
    say "需要安装 Docker Engine + Compose v2。顺序：Docker 官方安装脚本（get.docker.com${MIRROR:+，镜像 $MIRROR}，可重试/换镜像）→ 失败则用发行版软件包（apt/dnf/yum/zypper/apk）。" \
        "Docker Engine + Compose v2 must be installed. Order: Docker's official script (get.docker.com${MIRROR:+, mirror $MIRROR}, with retries/mirror fallback) -> on failure the distribution packages (apt/dnf/yum/zypper/apk)."
    [ "$fam" != unknown ] || die "无法识别的发行版（$OS_ID）。请参考 https://docs.docker.com/engine/install/ 手动安装后重试。" "Unrecognised distribution ($OS_ID). Install Docker manually (https://docs.docker.com/engine/install/) and retry."
    if [ "$INSTALL_DOCKER" != 1 ]; then
        [ "$ASSUME_YES" != 1 ] || die "未检测到 Docker。--yes 模式下需显式加 --install-docker 才会自动安装；或先自行安装 Docker 与 compose 插件。" "Docker not found. In --yes mode pass --install-docker explicitly to allow installation, or install Docker and the compose plugin yourself."
        say "将执行（需要联网，需 root）：第三方脚本 https://get.docker.com（Docker Inc. 官方），失败时回退到发行版软件包。" "About to run (needs network and root): the third-party script https://get.docker.com (Docker Inc.'s official one), falling back to distribution packages."
        confirm "是否继续安装 Docker？" "Install Docker now?" n || die_cancel "已取消：未安装 Docker。" "Cancelled: Docker not installed."
    fi
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

ensure_docker() {
    step "检查 Docker" "Checking Docker"
    if ! docker_ready; then
        if have_docker; then
            warn "已有 docker 但缺少 Compose v2 插件。" "docker is present but the Compose v2 plugin is missing."
        else
            warn "未检测到 Docker。" "Docker is not installed."
        fi
        install_docker
    fi
    if ! docker info >/dev/null 2>&1; then
        if command -v systemctl >/dev/null 2>&1; then systemctl start docker >/dev/null 2>&1 || true; fi
        docker info >/dev/null 2>&1 || die "Docker 守护进程未运行（systemctl start docker）。" "The Docker daemon is not running (systemctl start docker)."
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
    if [ "${#DOCKER_MIRRORS[@]}" -eq 0 ]; then
        if [ "$MIRROR" = cn ] && [ -z "$OFFLINE_FILE" ]; then
            pf_info "提示：Docker Hub 基础镜像（postgres/caddy）在大陆可能很慢；可加 --docker-mirror <加速地址> 写入 daemon.json（会先询问）。生产镜像来自 ACR，不受影响。" \
                "Tip: Docker Hub base images (postgres/caddy) can be slow in mainland China; add --docker-mirror <mirror URL> to put one into daemon.json (asked first). The production images come from ACR and are not affected."
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
    command -v curl >/dev/null 2>&1 || install_pkg curl
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
    printf '%s/oa-agent-api:%s\n%s/oa-agent-worker:%s\n%s\n' "$REGISTRY" "$VERSION" "$REGISTRY" "$VERSION" "${pg:-postgres:16-alpine}"
    if [ -n "${DOMAIN:-}" ]; then printf '%s\n' "${cdy:-caddy:2-alpine}"; fi
}

# pull_stack_images: compose pull with retries; when it still fails, name the exact failing image(s)
pull_stack_images() {
    net_retry "docker compose pull" dcw pull && return 0
    local img failed=()
    say "拉取失败，逐个镜像排查 …" "Pull failed; checking the images one by one ..."
    while IFS= read -r img; do
        [ -n "$img" ] || continue
        docker pull "$img" >/dev/null 2>&1 || failed+=("$img")
    done < <(stack_images)
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
    mount_path_ok "$p" || die "$label 路径不合法: $p（须为绝对路径，仅含字母数字 _ . / -，且不能是系统目录如 / /etc /usr /var/lib/docker）" "$label path is not acceptable: $p (absolute, only alphanumerics _ . / -, and not a system directory such as / /etc /usr /var/lib/docker)"
    if [ ! -d "$p" ]; then
        mkdir -p "$p" 2>/dev/null || die "无法创建 $label 目录: $p" "Cannot create the $label directory: $p"
        chmod 750 "$p" 2>/dev/null || true
        created=1
    fi
    probe="$(mktemp "$p/.oa-write-test.XXXXXX" 2>/dev/null)" || die "$label 目录不可写（只读文件系统或权限不足）: $p" "The $label directory is not writable (read-only filesystem or permissions): $p"
    rm -f -- "$probe"
    fst="$(stat -f -c %T "$p" 2>/dev/null || echo unknown)"
    free_kb="$(df -Pk "$p" 2>/dev/null | awk 'NR==2{print $4}' || echo 0)"
    say "  $label: $p（剩余 $((free_kb / 1024 / 1024)) GiB，文件系统 $fst$([ "$created" = 1 ] && echo '，已创建')）" "  $label: $p ($((free_kb / 1024 / 1024)) GiB free, filesystem $fst$([ "$created" = 1 ] && echo ', created'))"
    if [ "$spacechk" = yes ] && [ "$free_kb" -lt 20971520 ]; then
        warn "$label 所在磁盘剩余不足 20 GiB，请确认容量足够。" "Less than 20 GiB free for $label; make sure there is enough capacity."
    fi
    case "$fst" in
        tmpfs | overlay | overlayfs | ramfs) warn "$label 位于 $fst：数据可能在重启后丢失或不持久，请改用真实磁盘/NAS 挂载点。" "$label is on $fst: data may not survive a reboot; use a real disk or NAS mount point." ;;
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
    if [ "$ASSUME_YES" != 1 ] && have_tty && confirm "是否自定义导入暂存/应用数据/数据库目录（默认都在安装目录的 data/ 下）？" "Customize the spool / app-data / database directories (default: under data/ in the install directory)?" n; then
        ask_valid SPOOL_DIR "导入暂存目录" "Import spool directory" "$SPOOL_DIR" storage_problem
        ask_valid APP_DIR "应用数据目录（含 secret.key）" "Application data directory (holds secret.key)" "$APP_DIR" storage_problem
        ask_valid PG_DIR "PostgreSQL 数据目录" "PostgreSQL data directory" "$PG_DIR" storage_problem
    fi
    local spec why
    for spec in ${EXTRA_MOUNTS[@]+"${EXTRA_MOUNTS[@]}"}; do parse_extra_mount "$spec"; done
    if [ "$ASSUME_YES" != 1 ] && have_tty; then
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
    check_mount_path "PostgreSQL" "$PG_DIR" 70 yes
    chmod 700 "$PG_DIR" "$APP_DIR" 2>/dev/null || true
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
        "$bin" --dir "$DIR" --yes mounts set "$role" "$path" || die "oa-agentctl mounts set $role 失败（已有数据不会被移动或删除；需要迁移请用 oa-agentctl mounts set $role <路径> --copy）" "oa-agentctl mounts set $role failed (existing data is never moved or deleted; use oa-agentctl mounts set $role <path> --copy to copy it)"
    done
    for spec in ${PENDING_EXTRA[@]+"${PENDING_EXTRA[@]}"}; do
        name="${spec%%=*}"; rest="${spec#*=}"; mode=--ro
        case "$rest" in *:rw) mode=--rw; rest="${rest%:rw}" ;; *:ro) rest="${rest%:ro}" ;; esac
        say "添加/更新外部挂载 $name → $rest" "Adding/updating extra mount $name -> $rest"
        "$bin" --dir "$DIR" --yes mounts add "$name" "$rest" "$mode" --replace || die "oa-agentctl mounts add $name 失败" "oa-agentctl mounts add $name failed"
    done
}

apply_mounts() { # generate docker-compose.mounts.yml via the Go binary (never in shell)
    local bin="$BIN_DIR/oa-agentctl"
    if [ "$CTL_BINARY" = 1 ] && [ -x "$bin" ]; then
        "$bin" mounts apply --dir "$DIR" || die "oa-agentctl mounts apply 失败" "oa-agentctl mounts apply failed"
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
        printf 'OA_DOMAIN=%s\n' "$DOMAIN"
        printf 'OA_ACME_EMAIL=%s\n' "$EMAIL"
        printf 'OA_HTTP_PORT=80\nOA_HTTPS_PORT=443\n'
        printf 'OA_PUBLIC_URL=%s\n' "$PUBLIC_URL"
        printf 'OA_TRUSTED_PROXY=%s\n' "$TRUSTED"
        printf 'OA_POSTGRES_PASSWORD=%s\n' "$(gen_hex 16)"
        printf 'OA_WORKER_TOKEN=%s\n' "$(gen_hex 24)"
        printf 'OA_SECRET_KEY=%s\n' "$(gen_hex 32)"
        printf 'OA_STORAGE=on\nOA_CORS_ORIGINS=\n'
        printf 'OA_LLM_BASE_URL=\nOA_LLM_MODEL=\nOA_LLM_API_KEY=\nOA_LLM_TIMEOUT_SECONDS=60\nOA_LOG_LEVEL=\n'
        printf 'OA_TZ=%s\n' "$TZ_NAME"
        printf 'OA_PG_MEM=1g\nOA_API_MEM=1g\nOA_WORKER_MEM=3g\nOA_CADDY_MEM=256m\nOA_LOG_MAX_SIZE=10m\nOA_LOG_MAX_FILE=5\n'
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
    command -v curl >/dev/null 2>&1 || return 0
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
    command -v systemctl >/dev/null 2>&1 || return 0
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
    printf '  %s %s\n' "$(m '其他数据:    ' 'Other data:     ')" "$(m '暂存' 'spool') $SPOOL_DIR | $(m '应用数据' 'app data') $APP_DIR | PostgreSQL $PG_DIR"
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

chk_menu() { case "$1" in 1 | 2 | 3 | 4 | 5) ;; *) m "请输入 1-5" "enter 1-5"; return 1 ;; esac; }
# existing_menu: interactive only (a terminal, no --yes, no action flag). Sets DO_UPGRADE / REINSTALL / DO_UNINSTALL, or cancels.
existing_menu() {
    local cur_ver choice=''
    cur_ver="$(env_get OA_VERSION)"
    say "在 $DIR 检测到已有安装（版本/标签: ${cur_ver:-?}）。请选择：" "An installation already exists in $DIR (version/tag: ${cur_ver:-?}). Choose:"
    say "  1) 修复/继续    保留 .env 与全部数据，刷新文件并启动（默认）" "  1) repair/continue   keep .env and all data, refresh files and start (default)"
    say "  2) 升级         备份后拉取/构建新版本并重建" "  2) upgrade           back up, then pull/build the new version and recreate"
    say "  3) 重装         删除并重建容器（数据与 .env 保留），重新拉取镜像" "  3) reinstall         remove and recreate the containers (data and .env kept), pull the images again"
    say "  4) 卸载         移除容器与 systemd 单元（数据保留；清除数据需 --uninstall --purge）" "  4) uninstall         remove the containers and the systemd unit (data kept; deleting data needs --uninstall --purge)"
    say "  5) 取消" "  5) cancel"
    ask_valid choice "选择 [1-5]" "Choice [1-5]" 1 chk_menu
    case "$choice" in
        2) DO_UPGRADE=1 ;;
        3) REINSTALL=1 ;;
        4) DO_UNINSTALL=1 ;;
        5) die_cancel "已取消" "Cancelled" ;;
    esac
    MENU_DONE=1
}

# ---------------------------------------------------------------------------------------------- --check
# run_check: everything it touches is read-only (no log file, no lock, no docker pull/load/login, no package or file changes).
run_check() {
    local i why
    step "只读检查（--check）：不会做任何更改" "Read-only check (--check): nothing will be changed"
    if [ "$(id -u)" -ne 0 ]; then pf_info "当前不是 root：实际安装需要 root（sudo）。" "Not running as root: the real installation needs root (sudo)."; fi
    pf_system
    pf_ports "${ports_to_check[@]}"
    if docker_ready && docker info >/dev/null 2>&1; then pf_docker
    elif have_docker; then pf_warn "Docker 已安装但守护进程不可用或缺少 Compose v2：安装时会尝试启动/补装。" "Docker is installed but the daemon is unavailable or Compose v2 is missing: the installer will try to start/complete it."
    else pf_warn "未安装 Docker：实际安装时需要 --install-docker（或交互确认）才会自动安装。" "Docker is not installed: the real installation needs --install-docker (or interactive consent) to install it."; fi
    pf_registry
    if [ -n "$OFFLINE_FILE" ]; then pf_pass "镜像包 $OFFLINE_FILE：仓库 $OFFLINE_REG，标签 $OFFLINE_TAG（api/worker/deploy 齐全）" "Image bundle $OFFLINE_FILE: registry $OFFLINE_REG, tag $OFFLINE_TAG (api/worker/deploy present)"; offline_verify; fi
    if [ "$EXISTING" = 0 ]; then
        for i in "$ARCHIVE_DIR" "$SPOOL_DIR" "$APP_DIR" "$PG_DIR"; do
            if why="$(storage_problem "$i")"; then pf_pass "存储目录可用: $i" "Storage directory usable: $i"; else pf_fail "存储目录 $i：$why" "Storage directory $i: $why"; fi
        done
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
    say "  安装目录:   $DIR；端口 $PORT${DOMAIN:+；域名 $DOMAIN（HTTPS，需放行 80/443）}" "  install dir:  $DIR; port $PORT${DOMAIN:+; domain $DOMAIN (HTTPS, needs 80/443)}"
    say "  镜像来源:   $src" "  images:       $src_en"
    if [ "$EXISTING" = 0 ]; then say "  存储目录:   归档 $ARCHIVE_DIR | 暂存 $SPOOL_DIR | 应用数据 $APP_DIR | 数据库 $PG_DIR" "  storage:      archive $ARCHIVE_DIR | spool $SPOOL_DIR | app data $APP_DIR | database $PG_DIR"; fi
    if ! have_docker; then say "  Docker:     将安装（get.docker.com${MIRROR:+，镜像 $MIRROR}，失败回退发行版软件包）" "  Docker:       would be installed (get.docker.com${MIRROR:+, mirror $MIRROR}, distribution packages as fallback)"; fi
    if [ "${#DOCKER_MIRRORS[@]}" -gt 0 ]; then say "  镜像加速:   ${DOCKER_MIRRORS[*]}（仅交互确认后写入 daemon.json）" "  registry mirror: ${DOCKER_MIRRORS[*]} (written to daemon.json only after interactive consent)"; fi
    say "  systemd:    $([ "$SYSTEMD" = yes ] && echo '安装' || echo '询问（--yes 下不安装）')；防火墙: $([ "$OPEN_FW" = yes ] && echo '放行端口' || echo '询问（--yes 下不修改）')" "  systemd:      $([ "$SYSTEMD" = yes ] && echo 'install' || echo 'ask (not with --yes)'); firewall: $([ "$OPEN_FW" = yes ] && echo 'open the port' || echo 'ask (untouched with --yes)')"
    pf_finish
    say "这是只读检查，没有做任何更改。" "This was a read-only check; nothing was changed."
    if [ "$PF_FAIL" -gt 0 ]; then exit 3; fi
    exit 0
}

# ---------------------------------------------------------------------------------------------- main
main() {
    # Truncation guard: the last line of this file passes the sentinel as the final argument (see the header comment).
    if [ $# -eq 0 ] || [ "${!#}" != "$EOS_SENTINEL" ]; then
        printf '%s[FAIL]%s %s\n' "$C_R" "$C_0" "$(m "脚本不完整（下载被截断？），未执行任何操作。请重新下载后再运行。" "The script is incomplete (truncated download?); nothing was executed. Download it again and re-run.")" >&2
        exit 1
    fi
    set -- "${@:1:$#-1}"
    parse_args "$@"
    if [ "$CHECK" = 1 ]; then ASSUME_YES=1; else require_root; setup_log; fi   # a dry run never prompts and never writes a log
    if [ "$ASSUME_YES" != 1 ]; then open_tty; fi
    if [ "$CHECK" != 1 ]; then lock_install; fi
    banner
    logf "install.sh started (dir=$DIR version=$VERSION yes=$ASSUME_YES)"

    valid_dir "$DIR" || die_usage "安装目录不合法: $DIR（须为绝对路径，仅含字母数字 _ . / -，且不能是系统目录）" "Invalid install dir: $DIR (absolute path, only alphanumerics _ . / -, and not a system directory)"
    DIR="${DIR%/}"
    [ ! -f "$DIR/.env" ] || EXISTING=1

    # existing install + interactive terminal + no explicit action: offer repair / upgrade / reinstall / uninstall / cancel
    if [ "$EXISTING" = 1 ] && [ "$ASSUME_YES" != 1 ] && have_tty && [ "$DO_UPGRADE" = 0 ] && [ "$DO_UNINSTALL" = 0 ] && [ "$REINSTALL" = 0 ]; then existing_menu; fi

    if [ "$DO_UNINSTALL" = 1 ]; then uninstall_flow; exit 0; fi

    detect_os
    [ "$DO_UPGRADE" = 1 ] && [ "$EXISTING" = 0 ] && die "没有找到可升级的安装: $DIR" "No installation to upgrade in $DIR"
    [ "$REINSTALL" = 1 ] && [ "$EXISTING" = 0 ] && die "没有找到可重装的安装: $DIR" "No installation to reinstall in $DIR"

    # ---- interactive questions (flags win; --yes uses defaults)
    if [ "$EXISTING" = 0 ] && [ "$DO_UPGRADE" = 0 ]; then
        step "配置" "Configuration"
        if [ "$ASSUME_YES" != 1 ] && have_tty; then say "直接回车使用方括号中的默认值。" "Press Enter to accept the default shown in brackets."; fi
        ask_valid DIR "安装目录（程序、配置 .env 与 mounts.json）" "Install directory (program, .env and mounts.json)" "$DIR" chk_new_install_dir
        DIR="${DIR%/}"
        ask_valid PORT "HTTP 端口" "HTTP port" "$PORT" chk_port
        ask_valid DOMAIN "域名（留空 = 仅 HTTP，无 Passkey）" "Domain (empty = HTTP only, no Passkeys)" "$DOMAIN" chk_domain
        if [ -n "$DOMAIN" ]; then ask_valid EMAIL "证书邮箱（可选）" "Certificate e-mail (optional)" "$EMAIL" chk_email; fi
        if [ -z "$MIRROR" ]; then
            local mdef=default; [ "$UI" = zh ] && mdef=cn
            ask_valid MIRROR "镜像源 cn(中国大陆)/default" "Mirror cn(China)/default" "$mdef" chk_mirror
        fi
        collect_mounts
    else
        say "检测到已有安装: $DIR（保留现有密钥与配置）。" "Existing installation detected: $DIR (keeping existing secrets and settings)."
        if [ "$MENU_DONE" = 0 ] && [ "$DO_UPGRADE" = 0 ] && [ "$ASSUME_YES" != 1 ]; then
            confirm "继续？将保留 .env 与全部数据，仅刷新文件并启动。" "Continue? .env and all data are kept; files are refreshed and the stack is started." y || die_cancel "已取消" "Cancelled"
        fi
        PORT="$(env_get OA_PORT)"; PORT="${PORT:-8080}"
        local old_domain; old_domain="$(env_get OA_DOMAIN)"
        [ -z "$DOMAIN" ] && DOMAIN="$old_domain"
        [ -z "$EMAIL" ] && EMAIL="$(env_get OA_ACME_EMAIL)"
        # Storage flags on an existing install are APPLIED (changing the working directory later is allowed):
        # remember them, load the current layout, and let oa-agentctl change the paths once it is installed.
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
                if [ -n "$cur_ver" ] && { [ "$VERSION_SET" = 0 ] || [ "$DO_UPGRADE" = 0 ]; }; then VERSION="$cur_ver"; fi ;;
        esac
    fi
    # ---- compose project: fixed at first install (renaming it would orphan the running containers)
    if [ "$EXISTING" = 1 ]; then
        local cur_proj; cur_proj="$(env_get COMPOSE_PROJECT_NAME)"; cur_proj="${cur_proj:-oa-agent}"
        if [ -n "$PROJECT" ] && [ "$PROJECT" != "$cur_proj" ]; then warn "已有安装：忽略 --project（保持 $cur_proj）。" "Existing install: --project ignored (keeping $cur_proj)."; fi
        PROJECT="$cur_proj"
    fi
    PROJECT="${PROJECT:-oa-agent}"
    [[ "$PROJECT" =~ ^[a-z0-9][a-z0-9_-]{0,30}$ ]] || die_usage "项目名不合法: $PROJECT（小写字母/数字/-/_，最多 31 个字符）" "Invalid project name: $PROJECT (lowercase letters/digits/-/_, at most 31 characters)"
    [ -z "$MIRROR" ] && MIRROR=default
    case "$MIRROR" in cn | default) ;; *) die_usage "--mirror 只能是 cn 或 default" "--mirror must be cn or default" ;; esac
    valid_port "$PORT" || die_usage "端口不合法: $PORT" "Invalid port: $PORT"
    if [ -n "$DOMAIN" ]; then
        valid_domain "$DOMAIN" || die_usage "域名不合法: $DOMAIN（需要完整域名，不能是 IP）" "Invalid domain: $DOMAIN (a full domain name is required, not an IP)"
    fi
    if [ -n "$EMAIL" ]; then valid_email "$EMAIL" || die_usage "邮箱不合法: $EMAIL" "Invalid e-mail: $EMAIL"; fi
    [[ "$VERSION" =~ ^[A-Za-z0-9._-]{1,64}$ ]] || die_usage "版本号不合法" "Invalid version"
    REGISTRY="${REGISTRY%/}"
    if [ -n "$REGISTRY" ]; then [[ "$REGISTRY" =~ ^[A-Za-z0-9._-]+(:[0-9]{1,5})?(/[a-z0-9._/-]+)?$ ]] || die_usage "镜像仓库地址不合法（应为 主机[:端口][/命名空间]，不含 https://）" "Invalid registry (expected host[:port][/namespace], without https://)"; fi
    if [ -n "$OFFLINE_FILE" ]; then offline_apply; REGISTRY="${REGISTRY%/}"; fi

    # ---- profile selection
    BIND='0.0.0.0'; TRUSTED=0; FW_PORTS="$PORT"
    local ports_to_check=("$PORT")
    if [ -n "$DOMAIN" ]; then
        BIND=127.0.0.1; TRUSTED=1; FW_PORTS='80/443'
        ports_to_check=(80 443)
        [ "$PORT" != 80 ] && [ "$PORT" != 443 ] || die_usage "使用域名时 --port 是内部端口，不能为 80/443。" "With a domain, --port is the internal port and must not be 80/443."
        ports_to_check+=("$PORT")
    fi
    PUBLIC_URL="http://$(host_ip):$PORT"
    [ -z "$DOMAIN" ] || PUBLIC_URL="https://$DOMAIN"
    COMPOSE_FILES='docker-compose.yml:docker-compose.mounts.yml:docker-compose.build.yml'
    [ -z "$REGISTRY" ] || COMPOSE_FILES='docker-compose.yml:docker-compose.mounts.yml'
    B_GOPROXY=''; B_NPM=''; B_PIP=''
    if [ "$MIRROR" = cn ]; then
        B_GOPROXY='https://goproxy.cn,direct'; B_NPM='https://registry.npmmirror.com'; B_PIP='https://pypi.tuna.tsinghua.edu.cn/simple'
    fi
    TZ_NAME="$(detect_tz)"

    if [ "$CHECK" = 1 ]; then run_check; fi

    # ---- preflight (nothing is installed or changed before the report is clean)
    step "环境检查" "Preflight checks"
    pf_system
    pf_ports "${ports_to_check[@]}"
    pf_finish
    ensure_docker
    pf_docker
    pf_registry
    configure_docker_mirror
    pf_finish
    if [ -n "$OFFLINE_FILE" ]; then offline_verify; offline_load
    elif [ -n "$REGISTRY" ]; then registry_login; fi

    # ---- source + files
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
        write_new_env
        ok "已生成 $(envf)（权限 600，含随机密钥）" "Generated $(envf) (mode 600, random secrets)"
        [ -z "$DOMAIN" ] || ctl_yes domain --no-start "$DOMAIN"
    else
        chmod 600 "$(envf)"
        # existing install: secrets are never touched; only an explicitly requested domain is applied
        if [ -n "$DOMAIN" ] && { [ "$DOMAIN" != "$(env_get OA_DOMAIN)" ] || [ ! -f "$DIR/Caddyfile" ]; }; then
            if [ "$ASSUME_YES" != 1 ] && [ -n "$(env_get OA_DOMAIN)" ]; then
                confirm "更换域名会使已注册的 Passkey 失效，继续？" "Changing the domain invalidates registered Passkeys. Continue?" n || die_cancel "已取消" "Cancelled"
            fi
            ctl_yes domain --no-start "$DOMAIN"
        fi
        ok "保留现有 .env（密钥未改动）" "Kept existing .env (secrets untouched)"
    fi
    apply_pending_storage
    apply_mounts

    offer_systemd

    if [ "$NO_START" = 1 ]; then
        ok "已按 --no-start 停止在此；运行 oa-agentctl start 启动。" "Stopped here (--no-start); run oa-agentctl start to launch."
        exit 0
    fi

    if [ "$DO_UPGRADE" = 1 ]; then
        step "升级" "Upgrading"
        if [ -n "$REGISTRY" ] && [ "$VERSION_SET" = 1 ]; then ctl upgrade --tag "$VERSION"; else ctl upgrade; fi
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
    step "启动服务" "Starting services"
    if ! dcw up -d 2>&1 | tee -a "$LOG_FILE"; then die "docker compose up 失败" "docker compose up failed"; fi
    ctl_yes wait --timeout 900 || die "服务未能在期限内就绪（上方已列出诊断信息）。" "Services did not become healthy in time (diagnostics above)."

    if [ -n "$DOMAIN" ]; then offer_firewall 80 443; else offer_firewall "$PORT"; fi
    summary
    logf "install finished in $(($(date +%s) - STARTED_AT))s"
}

main "$@" "$EOS_SENTINEL"
