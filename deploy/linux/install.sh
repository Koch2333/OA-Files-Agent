#!/usr/bin/env bash
# OA 文件智能归档 - Linux 一键安装脚本 / OA Agent one-click Linux installer (Docker based).
#
#   From a checkout:   sudo bash deploy/linux/install.sh
#   Remote (placeholder URL, replace with your release location):
#       curl -fsSL <OA_REPO_URL>/releases/latest/download/install.sh | sudo bash -s -- --domain oa.example.com
#
# Run with --help for all options. The installer never overwrites an existing .env or regenerates secrets.
set -Eeuo pipefail
umask 077

# ---------------------------------------------------------------------------------------------- defaults
DIR='/opt/oa-agent'
PORT='8080'
DOMAIN=''
EMAIL=''
MIRROR=''            # cn | default (empty = ask / default)
VERSION='latest'
REPO_URL="${OA_REPO_URL:-}"      # PLACEHOLDER: e.g. https://example.com/PLACEHOLDER/oa-agent
REGISTRY="${OA_IMAGE_REGISTRY:-}"
ASSUME_YES=0
NO_START=0
DO_UNINSTALL=0
DO_UPGRADE=0
PURGE=0
INSTALL_DOCKER=0
SYSTEMD=ask
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
BIN_DIR='/usr/local/bin'
UNIT_DIR='/etc/systemd/system'
ETC_MARKER='/etc/oa-agent/install_dir'   # read by the Go oa-agentctl when neither --dir nor OA_AGENT_DIR is given
OS_RELEASE='/etc/os-release'
MEMINFO='/proc/meminfo'
TTY_DEV='/dev/tty'
if [ "$TEST_MODE" = 1 ]; then
    LOG_FILE="${OA_INSTALL_LOG:-$LOG_FILE}"; BIN_DIR="${OA_INSTALL_BIN_DIR:-$BIN_DIR}"; UNIT_DIR="${OA_INSTALL_UNIT_DIR:-$UNIT_DIR}"
    ETC_MARKER="${OA_INSTALL_ETC_MARKER:-$ETC_MARKER}"
    OS_RELEASE="${OA_OS_RELEASE:-$OS_RELEASE}"; MEMINFO="${OA_MEMINFO:-$MEMINFO}"; TTY_DEV="${OA_INSTALL_TTY:-$TTY_DEV}"
fi

TMP=''
CTL_BINARY=0
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
die() { local s; s="$(m "$1" "$2")"; printf '%s[FAIL]%s %s\n' "$C_R" "$C_0" "$s" >&2; logf "FAIL $s"; exit 1; }

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
  --registry <地址>   使用预构建镜像 <地址>/oa-agent-api:<版本> 而非本地构建（OA_IMAGE_REGISTRY）
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
  --upgrade           升级现有安装（刷新源码与脚本，备份后重建并重启）
  --uninstall         卸载容器（数据保留）；加 --purge 同时删除数据
  --purge             与 --uninstall 联用
  --lang zh|en        界面语言（默认按 $LANG）
  -h, --help          显示本帮助
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
  --registry <addr>   use pre-built images <addr>/oa-agent-api:<ver> instead of building locally (OA_IMAGE_REGISTRY)
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
  --upgrade           upgrade an existing install (refresh source and scripts, back up, rebuild, restart)
  --uninstall         remove containers (data kept); with --purge also delete the data
  --purge             combine with --uninstall
  --lang zh|en        UI language (default from $LANG)
  -h, --help          this help
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
trap 'on_err $LINENO' ERR
trap cleanup EXIT

# ---------------------------------------------------------------------------------------------- args
need_val() { [ $# -ge 2 ] && [ -n "$2" ] || die "选项 $1 需要一个值" "option $1 needs a value"; }
while [ $# -gt 0 ]; do
    case "$1" in
        --dir) need_val "$@"; DIR="$2"; shift 2 ;;
        --port) need_val "$@"; PORT="$2"; shift 2 ;;
        --domain) need_val "$@"; DOMAIN="$2"; shift 2 ;;
        --email) need_val "$@"; EMAIL="$2"; shift 2 ;;
        --mirror) need_val "$@"; MIRROR="$2"; shift 2 ;;
        --version) need_val "$@"; VERSION="$2"; shift 2 ;;
        --repo-url) need_val "$@"; REPO_URL="$2"; shift 2 ;;
        --registry) need_val "$@"; REGISTRY="$2"; shift 2 ;;
        --project) need_val "$@"; PROJECT="$2"; shift 2 ;;
        --lang) need_val "$@"; case "$2" in zh*) UI=zh ;; *) UI=en ;; esac; shift 2 ;;
        --archive-dir) need_val "$@"; ARCHIVE_DIR="$2"; shift 2 ;;
        --spool-dir) need_val "$@"; SPOOL_DIR="$2"; shift 2 ;;
        --data-dir) need_val "$@"; APP_DIR="$2"; shift 2 ;;
        --pg-dir) need_val "$@"; PG_DIR="$2"; shift 2 ;;
        --extra-mount) need_val "$@"; EXTRA_MOUNTS+=("$2"); shift 2 ;;
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
        *) usage >&2; die "未知选项: $1" "unknown option: $1" ;;
    esac
done

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
        if [ "$ASSUME_YES" = 1 ] || ! have_tty; then die "$zh: $why" "$en: $why"; fi
        printf '%s[!]%s %s\n' "$C_R" "$C_0" "$why" >&2
        printf -v "$var" '%s' "$def"
        tries=$((tries + 1))
        [ "$tries" -lt 5 ] || die "输入无效次数过多，已中止。" "Too many invalid answers, aborting."
    done
}
chk_install_dir() { valid_dir "$1" || { m "须为绝对路径，只能包含字母、数字和 _ . / -（不支持空格/中文），且不能是 / /etc /opt /var 等系统目录本身" "must be an absolute path with only letters, digits and _ . / - (no spaces), and not a system directory such as / /etc /opt /var itself"; return 1; }; }
chk_new_install_dir() {
    chk_install_dir "$1" || return 1
    if [ -f "${1%/}/.env" ]; then m "${1%/} 中已有安装；要修复/升级它，请重新运行 install.sh --dir ${1%/}（或加 --upgrade）" "${1%/} already holds an installation; to repair/upgrade it re-run install.sh --dir ${1%/} (or add --upgrade)"; return 1; fi
}
chk_port() {
    valid_port "$1" || { m "端口须为 1-65535 的数字" "the port must be a number from 1 to 65535"; return 1; }
    if port_in_use "$1"; then m "端口 $1 已被占用（ss -ltnp | grep :$1 查看占用者），请换一个" "port $1 is already in use (ss -ltnp | grep :$1 shows who), choose another"; return 1; fi
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
    local flags=(--dir "$DIR")
    [ "$ASSUME_YES" = 1 ] && flags+=(--yes)
    OA_LANG="$UI" OA_UNIT_DIR="$UNIT_DIR" OA_CTL_SELF="$BIN_DIR/oa-agentctl" bash "$c" "${flags[@]}" "$@"
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
        OS_ID="$(. "$OS_RELEASE"; printf '%s' "${ID:-}")"
        OS_LIKE="$(. "$OS_RELEASE"; printf '%s' "${ID_LIKE:-}")"
        OS_PRETTY="$(. "$OS_RELEASE"; printf '%s' "${PRETTY_NAME:-$ID}")"
    fi
    [ "$(uname -s)" = Linux ] || die "仅支持 Linux。" "Linux only."
    ARCH="$(uname -m)"
    case "$ARCH" in
        x86_64 | amd64) ARCH=amd64 ;;
        aarch64 | arm64) ARCH=arm64 ;;
        *) die "不支持的 CPU 架构: $ARCH（需要 x86_64 或 aarch64）" "Unsupported CPU architecture: $ARCH (x86_64 or aarch64 required)" ;;
    esac
    ok "系统: ${OS_PRETTY:-unknown} / $ARCH" "OS: ${OS_PRETTY:-unknown} / $ARCH"
}

os_family() {
    case "$OS_ID" in
        ubuntu | debian | raspbian | linuxmint) echo debian ;;
        centos | rocky | almalinux | rhel | ol | amzn) echo rhel ;;
        fedora) echo fedora ;;
        alpine) echo alpine ;;
        *) case " $OS_LIKE " in *" debian "* | *" ubuntu "*) echo debian ;; *" rhel "* | *" centos "*) echo rhel ;; *" fedora "*) echo fedora ;; *) echo unknown ;; esac ;;
    esac
}

port_in_use() {
    if command -v ss >/dev/null 2>&1; then ss -ltn 2>/dev/null | awk '{print $4}' | grep -Eq "[:.]$1\$"
    else (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null; fi
}

preflight_resources() {
    local cpus mem_kb mem_mb avail_kb parent
    cpus="$(nproc 2>/dev/null || echo 1)"
    mem_kb="$(awk '/^MemTotal:/{print $2}' "$MEMINFO" 2>/dev/null || echo 0)"
    mem_mb=$((mem_kb / 1024))
    parent="$DIR"; while [ ! -d "$parent" ] && [ "$parent" != / ]; do parent="$(dirname -- "$parent")"; done
    avail_kb="$(df -Pk "$parent" 2>/dev/null | awk 'NR==2{print $4}' || echo 0)"
    say "CPU: $cpus 核；内存: ${mem_mb} MB；$parent 可用磁盘: $((avail_kb / 1024 / 1024)) GB" "CPU: $cpus core(s); RAM: ${mem_mb} MB; free disk at $parent: $((avail_kb / 1024 / 1024)) GB"
    if [ "$mem_mb" -lt 2000 ]; then
        warn "内存低于 2 GB：OCR/构建可能失败（建议 ≥4 GB，或加 swap）。" "Less than 2 GB RAM: OCR/builds may fail (4 GB+ recommended, or add swap)."
    elif [ "$mem_mb" -lt 3800 ]; then
        warn "内存低于 4 GB：可运行，但并发 OCR 较慢。" "Less than 4 GB RAM: works, but concurrent OCR will be slow."
    fi
    if [ "$cpus" -lt 2 ]; then warn "仅 1 个 CPU 核，处理会较慢。" "Only 1 CPU core: processing will be slow."; fi
    if [ "$avail_kb" -lt 5242880 ]; then
        die "可用磁盘不足 5 GB，无法安装（镜像构建约需 3 GB，另需归档空间）。" "Less than 5 GB free disk: cannot install (the image build needs ~3 GB plus archive space)."
    elif [ "$avail_kb" -lt 20971520 ]; then
        warn "可用磁盘不足 20 GB；归档文件会持续增长，请规划空间。" "Less than 20 GB free: archived files keep growing, plan your capacity."
    fi
}

check_ports() { # skipped for ports our own running stack already holds
    local p
    for p in "$@"; do
        if port_in_use "$p"; then
            if [ -f "$(envf)" ] && command -v docker >/dev/null 2>&1 && ctl_running_port "$p"; then continue; fi
            die "端口 $p 已被占用。请换一个端口（--port）或停止占用它的程序（ss -ltnp | grep :$p）。" "Port $p is already in use. Choose another port (--port) or stop the program using it (ss -ltnp | grep :$p)."
        fi
    done
    ok "端口可用: $*" "Ports free: $*"
}
ctl_running_port() { # does an existing OA install already publish this port?
    local cur_port cur_http cur_https
    cur_port="$(env_get OA_PORT)"; cur_http="$(env_get OA_HTTP_PORT)"; cur_https="$(env_get OA_HTTPS_PORT)"
    [ "$1" = "${cur_port:-}" ] || [ "$1" = "${cur_http:-80}" ] || [ "$1" = "${cur_https:-443}" ] || return 1
    docker ps --format '{{.Names}}' 2>/dev/null | grep -q "^${PROJECT:-oa-agent}-" || return 1
}

# ---------------------------------------------------------------------------------------------- docker
docker_ready() { command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; }

install_docker() {
    local fam
    fam="$(os_family)"
    say "需要安装 Docker Engine + Compose v2（系统包管理器或 Docker 官方安装脚本）。" "Docker Engine + Compose v2 must be installed (distro packages or Docker's official convenience script)."
    if [ "$INSTALL_DOCKER" != 1 ]; then
        [ "$ASSUME_YES" != 1 ] || die "未检测到 Docker。--yes 模式下需显式加 --install-docker 才会自动安装；或先自行安装 Docker 与 compose 插件。" "Docker not found. In --yes mode pass --install-docker explicitly to allow installation, or install Docker and the compose plugin yourself."
        say "将执行（需要联网，需 root）：" "About to run (needs network and root):"
        case "$fam" in
            alpine) say "  apk add docker docker-cli-compose && rc-update add docker default && service docker start" "  apk add docker docker-cli-compose && rc-update add docker default && service docker start" ;;
            unknown) die "无法识别的发行版（$OS_ID）。请参考 https://docs.docker.com/engine/install/ 手动安装后重试。" "Unrecognised distribution ($OS_ID). Install Docker manually (https://docs.docker.com/engine/install/) and retry." ;;
            *) say "  从 https://get.docker.com 下载 Docker Inc. 官方安装脚本并运行（第三方脚本，请知悉）${MIRROR:+，镜像: $MIRROR}" "  download and run Docker Inc.'s official script from https://get.docker.com (third-party script, you are informed)${MIRROR:+, mirror: $MIRROR}" ;;
        esac
        confirm "是否继续安装 Docker？" "Install Docker now?" n || die "已取消：未安装 Docker。" "Cancelled: Docker not installed."
    fi
    step "安装 Docker" "Installing Docker"
    case "$fam" in
        alpine)
            apk add --no-cache docker docker-cli-compose 2>&1 | tee -a "$LOG_FILE" || die "apk 安装失败" "apk install failed"
            rc-update add docker default >/dev/null 2>&1 || true
            service docker start >/dev/null 2>&1 || true
            ;;
        debian | rhel | fedora)
            TMP="${TMP:-$(mktemp -d)}"
            command -v curl >/dev/null 2>&1 || install_pkg curl
            local args=()
            if [ "$MIRROR" = cn ]; then args=(--mirror Aliyun); fi
            curl -fsSL https://get.docker.com -o "$TMP/get-docker.sh" || die "下载 get.docker.com 脚本失败" "Failed to download the get.docker.com script"
            sh "$TMP/get-docker.sh" ${args[@]+"${args[@]}"} 2>&1 | tee -a "$LOG_FILE"
            ;;
        *) die "无法自动安装 Docker（$OS_ID）。" "Cannot install Docker automatically ($OS_ID)." ;;
    esac
    if command -v systemctl >/dev/null 2>&1; then systemctl enable --now docker >/dev/null 2>&1 || true; fi
    if ! docker compose version >/dev/null 2>&1; then
        case "$fam" in
            debian) apt-get install -y docker-compose-plugin >/dev/null 2>&1 || true ;;
            rhel | fedora) (dnf install -y docker-compose-plugin || yum install -y docker-compose-plugin) >/dev/null 2>&1 || true ;;
        esac
    fi
    docker_ready || die "Docker 安装后仍缺少 compose v2 插件，请手动安装 docker-compose-plugin。" "compose v2 plugin still missing after installing Docker; install docker-compose-plugin manually."
    ok "Docker 已安装" "Docker installed"
}
install_pkg() {
    case "$(os_family)" in
        debian) apt-get update -y >/dev/null 2>&1 || true; apt-get install -y "$1" ;;
        rhel | fedora) dnf install -y "$1" 2>/dev/null || yum install -y "$1" ;;
        alpine) apk add --no-cache "$1" ;;
        *) die "请先安装 $1" "Please install $1 first" ;;
    esac
}

ensure_docker() {
    step "检查 Docker" "Checking Docker"
    if ! docker_ready; then
        if command -v docker >/dev/null 2>&1; then
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
    ok "Docker $(docker version --format '{{.Server.Version}}' 2>/dev/null || echo '?') / $(docker compose version --short 2>/dev/null || echo '?')" "Docker $(docker version --format '{{.Server.Version}}' 2>/dev/null || echo '?') / compose $(docker compose version --short 2>/dev/null || echo '?')"
}

# ---------------------------------------------------------------------------------------------- source
SRC_ROOT=''
find_local_source() {
    local here root
    here="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd || true)"
    [ -n "$here" ] || return 1
    root="$(cd "$here/../.." 2>/dev/null && pwd || true)"
    if [ -n "$root" ] && [ -f "$root/deploy/linux/docker-compose.yml" ] && [ -f "$root/deploy/docker/Dockerfile.api" ] && [ -d "$root/backend" ]; then
        SRC_ROOT="$root"; return 0
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
        else confirm "未找到 SHA-256 校验和，无法验证完整性。仍要继续吗？" "No SHA-256 checksum found, integrity cannot be verified. Continue anyway?" n || die "已取消" "Cancelled"; fi
    fi
    mkdir -p "$TMP/src"
    # refuse absolute paths / .. entries before extracting
    if tar -tzf "$TMP/$tag" | grep -E '(^/|(^|/)\.\.(/|$))' >/dev/null; then die "发行包含不安全路径" "release archive contains unsafe paths"; fi
    tar -xzf "$TMP/$tag" -C "$TMP/src" --strip-components=1
    SRC_ROOT="$TMP/src"
    [ -f "$SRC_ROOT/deploy/linux/docker-compose.yml" ] || die "发行包结构不正确" "unexpected release layout"
}

sync_source() { # copy source tree + deploy files into the install dir
    mkdir -p "$DIR/src"
    if [ "$(cd "$SRC_ROOT" && pwd)" != "$(cd "$DIR/src" && pwd)" ]; then
        (cd "$SRC_ROOT" && tar --exclude=./.git --exclude=node_modules --exclude=.venv --exclude=__pycache__ --exclude=./frontend/dist --exclude=./dist --exclude=./data --exclude=./backend/bin --exclude=./tests/samples -cf - .) |
            (umask 022; cd "$DIR/src" && tar --no-same-owner --no-same-permissions -xf -) # never inherit lax modes (e.g. 0777 from a Windows share)
    fi
    local f
    for f in docker-compose.yml docker-compose.build.yml docker-compose.https.yml Caddyfile.template; do
        cp -f "$SRC_ROOT/deploy/linux/$f" "$DIR/$f"; chmod 644 "$DIR/$f"
    done
    if [ -f "$SRC_ROOT/deploy/linux/.env.example" ]; then cp -f "$SRC_ROOT/deploy/linux/.env.example" "$DIR/.env.example"; chmod 644 "$DIR/.env.example"; fi
}

install_ctl() {
    # 1) the bash fallback is always installed as oa-agentctl.sh (used by this installer and by the systemd unit)
    mkdir -p "$BIN_DIR"
    local tmp
    tmp="$(mktemp "$BIN_DIR/.oa-agentctl.XXXXXX")"
    sed "s|^OA_DEFAULT_DIR=.*|OA_DEFAULT_DIR='$DIR'|" "$SRC_ROOT/deploy/linux/oa-agentctl.sh" >"$tmp"
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
    elif [ -d "$DIR/src/backend/cmd/oa-agentctl" ] && command -v docker >/dev/null 2>&1; then
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
    local pub tmp
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

host_ip() {
    local ip=''
    ip="$(hostname -I 2>/dev/null | awk '{print $1}' || true)"
    printf '%s' "${ip:-localhost}"
}

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
    sed "s|__DIR__|$DIR|g" "$SRC_ROOT/deploy/linux/oa-agent.service" >"$UNIT_DIR/oa-agent.service"
    chmod 644 "$UNIT_DIR/oa-agent.service"
    systemctl daemon-reload >/dev/null 2>&1 || true
    systemctl enable oa-agent.service >/dev/null 2>&1 || true
    ok "systemd 单元已启用: oa-agent.service" "systemd unit enabled: oa-agent.service"
}

# ---------------------------------------------------------------------------------------------- summary
summary() {
    local url token tokline w=64
    url="$(env_get OA_PUBLIC_URL)"
    token="$(ctl setup-token 2>/dev/null | tail -n 1 || true)"
    [ -n "$token" ] || token="$(m '（无；可能已完成初始化，或运行 oa-agentctl setup-token）' '(none; setup may be complete, or run oa-agentctl setup-token)')"
    printf '\n%s' "$C_G"
    printf '+%*s+\n' "$w" '' | tr ' ' '='
    printf '%s\n' "$(m '  安装完成！' '  Installation complete!')"
    printf '%s\n' "$C_0"
    printf '  %s %s\n' "$(m '访问地址:    ' 'URL:            ')" "$url"
    printf '  %s %s\n' "$(m '初始化令牌:  ' 'SETUP TOKEN:    ')" "$token"
    printf '  %s %s\n' "$(m '安装目录:    ' 'Install dir:    ')" "$DIR"
    printf '  %s %s\n' "$(m '归档目录:    ' 'Archive dir:    ')" "$ARCHIVE_DIR  $(m '（工作目录：归档后的文件存放在这里）' '(working directory: archived files are stored here)')"
    printf '  %s %s\n' "$(m '其他数据:    ' 'Other data:     ')" "$(m '暂存' 'spool') $SPOOL_DIR | $(m '应用数据' 'app data') $APP_DIR | PostgreSQL $PG_DIR"
    local i
    for i in "${!EX_NAME[@]}"; do
        printf '  %s %s\n' "$(m '额外挂载:    ' 'Extra mount:    ')" "${EX_NAME[$i]}: ${EX_HOST[$i]} -> /mnt/storage/${EX_NAME[$i]} (${EX_RO[$i]})"
    done
    printf '  %s %s\n' "$(m '配置目录:    ' 'Config dir:     ')" "$DIR (.env, mounts.json)"
    printf '  %s %s\n' "$(m '管理命令:    ' 'Management:     ')" "$CTL_HINT  $(m '（不带参数进入交互界面）' '(no arguments: interactive UI)')"
    printf '  %s %s\n' "$(m '             ' '                ')" "$CTL_HINT status | logs | doctor | backup | upgrade | setup-token | help"
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
    printf '  %s\n' "$(m '4. 防火墙：外部访问需放行 '"$FW_PORTS"' 端口；本脚本不会擅自修改防火墙。' '4. Firewall: open port(s) '"$FW_PORTS"' for external access; this script never changes it on its own.')"
    printf '%s' "$C_G"
    printf '+%*s+\n' "$w" '' | tr ' ' '='
    printf '%s\n' "$C_0"
}

# ---------------------------------------------------------------------------------------------- main
main() {
    require_root
    setup_log
    [ "$ASSUME_YES" = 1 ] || open_tty
    banner
    logf "install.sh started (dir=$DIR version=$VERSION yes=$ASSUME_YES)"

    valid_dir "$DIR" || die "安装目录不合法: $DIR（须为绝对路径，仅含字母数字 _ . / -，且不能是系统目录）" "Invalid install dir: $DIR (absolute path, only alphanumerics _ . / -, and not a system directory)"
    DIR="${DIR%/}"

    if [ "$DO_UNINSTALL" = 1 ]; then
        if [ "$PURGE" = 1 ]; then ctl uninstall --purge; else ctl uninstall; fi
        exit 0
    fi

    detect_os
    local existing=0
    [ -f "$DIR/.env" ] && existing=1
    [ "$DO_UPGRADE" = 1 ] && [ "$existing" = 0 ] && die "没有找到可升级的安装: $DIR" "No installation to upgrade in $DIR"

    # ---- interactive questions (flags win; --yes uses defaults)
    if [ "$existing" = 0 ] && [ "$DO_UPGRADE" = 0 ]; then
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
        [ "$existing" = 1 ] && say "检测到已有安装: $DIR（保留现有密钥与配置）。" "Existing installation detected: $DIR (keeping existing secrets and settings)."
        if [ "$existing" = 1 ] && [ "$DO_UPGRADE" = 0 ] && [ "$ASSUME_YES" != 1 ]; then
            confirm "继续？将保留 .env 与全部数据，仅刷新文件并启动。" "Continue? .env and all data are kept; files are refreshed and the stack is started." y || die "已取消" "Cancelled"
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
    fi
    # ---- compose project: fixed at first install (renaming it would orphan the running containers)
    if [ "$existing" = 1 ]; then
        local cur_proj; cur_proj="$(env_get COMPOSE_PROJECT_NAME)"; cur_proj="${cur_proj:-oa-agent}"
        if [ -n "$PROJECT" ] && [ "$PROJECT" != "$cur_proj" ]; then warn "已有安装：忽略 --project（保持 $cur_proj）。" "Existing install: --project ignored (keeping $cur_proj)."; fi
        PROJECT="$cur_proj"
    fi
    PROJECT="${PROJECT:-oa-agent}"
    [[ "$PROJECT" =~ ^[a-z0-9][a-z0-9_-]{0,30}$ ]] || die "项目名不合法: $PROJECT（小写字母/数字/-/_，最多 31 个字符）" "Invalid project name: $PROJECT (lowercase letters/digits/-/_, at most 31 characters)"
    [ -z "$MIRROR" ] && MIRROR=default
    case "$MIRROR" in cn | default) ;; *) die "--mirror 只能是 cn 或 default" "--mirror must be cn or default" ;; esac
    valid_port "$PORT" || die "端口不合法: $PORT" "Invalid port: $PORT"
    if [ -n "$DOMAIN" ]; then
        valid_domain "$DOMAIN" || die "域名不合法: $DOMAIN（需要完整域名，不能是 IP）" "Invalid domain: $DOMAIN (a full domain name is required, not an IP)"
    fi
    if [ -n "$EMAIL" ]; then valid_email "$EMAIL" || die "邮箱不合法: $EMAIL" "Invalid e-mail: $EMAIL"; fi
    [[ "$VERSION" =~ ^[A-Za-z0-9._-]{1,64}$ ]] || die "版本号不合法" "Invalid version"
    if [ -n "$REGISTRY" ]; then [[ "$REGISTRY" =~ ^[A-Za-z0-9._:/-]{1,200}$ ]] || die "镜像仓库地址不合法" "Invalid registry"; fi

    # ---- profile selection
    BIND='0.0.0.0'; TRUSTED=0; FW_PORTS="$PORT"
    local ports_to_check=("$PORT")
    if [ -n "$DOMAIN" ]; then
        BIND=127.0.0.1; TRUSTED=1; FW_PORTS='80/443'
        ports_to_check=(80 443)
        [ "$PORT" != 80 ] && [ "$PORT" != 443 ] || die "使用域名时 --port 是内部端口，不能为 80/443。" "With a domain, --port is the internal port and must not be 80/443."
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

    # ---- preflight
    step "环境检查" "Preflight checks"
    preflight_resources
    ensure_docker
    # an existing install keeps its own port bindings; only check ports when we are about to bind them
    check_ports "${ports_to_check[@]}"

    # ---- source + files
    step "准备文件" "Preparing files"
    if find_local_source; then say "使用本地源码: $SRC_ROOT" "Using local source: $SRC_ROOT"; else download_source; fi
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
                confirm "更换域名会使已注册的 Passkey 失效，继续？" "Changing the domain invalidates registered Passkeys. Continue?" n || die "已取消" "Cancelled"
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
        ctl upgrade
        exit 0
    fi

    # ---- build / pull, start
    step "获取镜像" "Obtaining images"
    dcw() { (cd "$DIR" && env COMPOSE_FILE="$(env_get COMPOSE_FILE)" docker compose "$@"); }
    if [ -z "$REGISTRY" ]; then
        say "首次构建需要下载依赖并编译前端/后端，通常需 5-15 分钟。" "The first build downloads dependencies and compiles the frontend/backend; it usually takes 5-15 minutes."
        if ! dcw build --progress=plain 2>&1 | tee -a "$LOG_FILE"; then die "镜像构建失败（详见 $LOG_FILE；中国大陆可加 --mirror cn）" "Image build failed (see $LOG_FILE; in mainland China try --mirror cn)"; fi
    else
        if ! dcw pull 2>&1 | tee -a "$LOG_FILE"; then die "镜像拉取失败（检查 --registry / --version）" "Image pull failed (check --registry / --version)"; fi
    fi
    step "启动服务" "Starting services"
    if ! dcw up -d 2>&1 | tee -a "$LOG_FILE"; then die "docker compose up 失败" "docker compose up failed"; fi
    ctl_yes wait --timeout 900 || die "服务未能在期限内就绪（上方已列出诊断信息）。" "Services did not become healthy in time (diagnostics above)."

    if [ -n "$DOMAIN" ]; then offer_firewall 80 443; else offer_firewall "$PORT"; fi
    summary
    logf "install finished in $(($(date +%s) - STARTED_AT))s"
}

main "$@"
