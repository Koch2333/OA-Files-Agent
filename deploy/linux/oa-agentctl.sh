#!/usr/bin/env bash
# oa-agentctl - manage an OA Agent Docker installation (installed to /usr/local/bin by install.sh).
# 管理 OA 文件智能归档的 Docker 部署。Usage: oa-agentctl [--dir DIR] [--yes] <command> [args]   (oa-agentctl help)
set -Eeuo pipefail
umask 077

OA_DEFAULT_DIR='/opt/oa-agent'
CTL_VERSION='1'

# ---------------------------------------------------------------------------------------------- messages
case "${OA_LANG:-${LC_ALL:-${LANG:-}}}" in zh* | *zh_* | *ZH*) UI=zh ;; *) UI=en ;; esac
if [ -n "${OA_LANG:-}" ]; then case "$OA_LANG" in zh*) UI=zh ;; *) UI=en ;; esac; fi
m() { if [ "$UI" = zh ]; then printf '%s' "$1"; else printf '%s' "$2"; fi; }
if [ -t 1 ]; then C_R=$'\033[31m'; C_G=$'\033[32m'; C_Y=$'\033[33m'; C_0=$'\033[0m'; else C_R=''; C_G=''; C_Y=''; C_0=''; fi
info() { printf '%s\n' "$(m "$1" "$2")"; }
ok() { printf '%s[ OK ]%s %s\n' "$C_G" "$C_0" "$(m "$1" "$2")"; }
warn() { printf '%s[WARN]%s %s\n' "$C_Y" "$C_0" "$(m "$1" "$2")" >&2; }
die() { printf '%s[FAIL]%s %s\n' "$C_R" "$C_0" "$(m "$1" "$2")" >&2; exit 1; }

DIR="${OA_AGENT_DIR:-$OA_DEFAULT_DIR}"
ASSUME_YES=0

# ---------------------------------------------------------------------------------------------- helpers
env_file() { printf '%s/.env' "$DIR"; }

# env_get KEY: value from .env (last assignment wins, surrounding quotes and trailing " # comment" removed).
env_get() {
    local f line v
    f="$(env_file)"
    [ -f "$f" ] || return 0
    line="$(grep -E "^$1=" "$f" | tail -n 1 || true)"
    v="${line#*=}"
    v="${v%% #*}"
    v="${v%"${v##*[![:space:]]}"}"
    v="${v#\"}"; v="${v%\"}"; v="${v#\'}"; v="${v%\'}"
    printf '%s' "$v"
}

# env_set KEY VALUE: replace or append, atomically, keeping mode 0600. VALUE is validated by callers.
env_set() {
    local f tmp
    f="$(env_file)"
    tmp="$(mktemp "$DIR/.env.XXXXXX")"
    if [ -f "$f" ]; then
        grep -v -E "^$1=" "$f" >"$tmp" || true
    fi
    printf '%s=%s\n' "$1" "$2" >>"$tmp"
    chmod 600 "$tmp"
    mv -f "$tmp" "$f"
}

need_install() {
    [ -d "$DIR" ] && [ -f "$DIR/.env" ] && [ -f "$DIR/docker-compose.yml" ] ||
        die "在 $DIR 未找到安装（缺少 .env / docker-compose.yml）。用 --dir 指定安装目录。" "No installation found in $DIR (.env / docker-compose.yml missing). Use --dir."
}

need_docker() {
    command -v docker >/dev/null 2>&1 || die "未找到 docker。" "docker not found."
    docker compose version >/dev/null 2>&1 || die "需要 Docker Compose v2（docker compose）。" "Docker Compose v2 (docker compose) is required."
}

# dc ARGS...: docker compose inside the install dir with the compose selection taken from .env.
dc() {
    local cf
    cf="$(env_get COMPOSE_FILE)"
    (
        cd "$DIR"
        export COMPOSE_FILE="${cf:-docker-compose.yml}"
        docker compose "$@"
    )
}

confirm() { # confirm "中文" "English"  (default no)
    [ "$ASSUME_YES" = 1 ] && return 0
    local ans
    if [ -t 0 ]; then
        printf '%s [y/N] ' "$(m "$1" "$2")"
        read -r ans || ans=''
    else
        die "需要交互确认，但标准输入不是终端；请加 --yes。" "Confirmation needed but stdin is not a terminal; pass --yes."
    fi
    case "$ans" in y | Y | yes | YES) return 0 ;; *) return 1 ;; esac
}

valid_domain() { [[ "$1" =~ ^([A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?\.)+[A-Za-z]{2,63}$ ]]; }
valid_port() { [[ "$1" =~ ^[0-9]{1,5}$ ]] && [ "$1" -ge 1 ] && [ "$1" -le 65535 ]; }

ts() { date +%Y%m%d-%H%M%S; }

host_ip() {
    local ip=''
    ip="$(hostname -I 2>/dev/null | awk '{print $1}' || true)"
    printf '%s' "${ip:-localhost}"
}

owner_fix() { # chown data dirs to the container uids (skipped when not root)
    [ "$(id -u)" -eq 0 ] || return 0
    local pg
    pg="$(path_of OA_PG_DIR pg)"
    chown -R 70:70 "$pg" 2>/dev/null || true
    chmod 700 "$pg" 2>/dev/null || true
    chown -R 10001:10001 "$(path_of OA_APP_DIR app)" "$(path_of OA_SPOOL_DIR spool)" "$(path_of OA_ARCHIVE_DIR archive)" 2>/dev/null || true
}

# path_of ENVKEY DEFAULT_SUBDIR: host path of a mount (synced into .env from mounts.json), default <dir>/data/<sub>.
path_of() {
    local d
    d="$(env_get "$1")"; d="${d:-./data/$2}"
    case "$d" in /*) printf "%s" "$d" ;; *) printf "%s/%s" "$DIR" "${d#./}" ;; esac
}

https_on() { case ":$(env_get COMPOSE_FILE):" in *:docker-compose.https.yml:*) return 0 ;; *) return 1 ;; esac; }

# cf_set_https on|off: add/remove the https layer in COMPOSE_FILE (keeps the others, build layer last)
cf_set_https() {
    local cur out="" part
    cur="$(env_get COMPOSE_FILE)"; cur="${cur:-docker-compose.yml}"
    local IFS=:
    for part in $cur; do [ "$part" = docker-compose.https.yml ] || [ "$part" = docker-compose.build.yml ] || out="${out:+$out:}$part"; done
    [ "$1" != on ] || out="$out:docker-compose.https.yml"
    case ":$cur:" in *:docker-compose.build.yml:*) out="$out:docker-compose.build.yml" ;; esac
    env_set COMPOSE_FILE "$out"
}

port_listening() { # port_listening PORT
    if command -v ss >/dev/null 2>&1; then
        ss -ltn 2>/dev/null | awk '{print $4}' | grep -Eq "[:.]$1\$"
    else
        (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null
    fi
}

# service_state SERVICE -> healthy|unhealthy|starting|running|exited|missing
service_state() {
    local cid st
    cid="$(dc ps -q "$1" 2>/dev/null | head -n 1 || true)"
    [ -n "$cid" ] || { printf 'missing'; return 0; }
    st="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$cid" 2>/dev/null || true)"
    printf '%s' "${st:-missing}"
}

# db_external: the stack uses an EXISTING database server (docker-compose.external-db.yml): there is no postgres service to start,
# dump or restore; the database is backed up with the engine's own tools
db_external() { case ":$(env_get COMPOSE_FILE):" in *:docker-compose.external-db.yml:*) return 0 ;; *) return 1 ;; esac; }

expected_services() {
    local s="postgres api worker"
    if db_external; then s="api worker"; fi
    ! https_on || s="$s caddy"
    printf '%s' "$s"
}

# state_edit version TAG | removed | forget: keep the installer's state file (/etc/oa-agent/install.json) in step with this install
state_edit() {
    local sf="${OA_STATE_FILE:-/etc/oa-agent/install.json}" tmp l first=1
    [ -f "$sf" ] && [ -w "$sf" ] || return 0
    grep -q "\"dir\":\"$DIR\"" "$sf" 2>/dev/null || return 0
    tmp="$(mktemp "$(dirname -- "$sf")/.install.json.XXXXXX" 2>/dev/null)" || return 0
    {
        printf '{"schema":1,"installs":[\n'
        grep -E '^\{"dir":"' "$sf" | sed 's/,[[:space:]]*$//' | while IFS= read -r l; do
            case "$l" in
                "{\"dir\":\"$DIR\""*)
                    case "$1" in
                        forget) continue ;;
                        removed) l="$(printf '%s' "$l" | sed 's/"status":"[^"]*"/"status":"removed"/')" ;;
                        version) l="$(printf '%s' "$l" | sed "s/\"version\":\"[^\"]*\"/\"version\":\"$2\"/; s/\"updated\":\"[^\"]*\"/\"updated\":\"$(date -u +%FT%TZ)\"/")" ;;
                    esac ;;
            esac
            [ "$first" = 1 ] || printf ',\n'
            printf '%s' "$l"; first=0
        done
        printf '\n]}\n'
    } >"$tmp"
    chmod 644 "$tmp" 2>/dev/null || true
    mv -f "$tmp" "$sf" 2>/dev/null || rm -f -- "$tmp"
}

# ---------------------------------------------------------------------------------------------- commands
cmd_wait() {
    local timeout=600 svc st all bad
    while [ $# -gt 0 ]; do
        case "$1" in --timeout) timeout="${2:?}"; shift 2 ;; *) shift ;; esac
    done
    local start now
    start="$(date +%s)"
    info "等待服务就绪（最长 ${timeout}s）…" "Waiting for services to become healthy (up to ${timeout}s) ..."
    while :; do
        all=1; bad=''
        for svc in $(expected_services); do
            st="$(service_state "$svc")"
            case "$st" in
                healthy | running) ;;
                unhealthy | exited | dead) all=0; bad="$bad $svc" ;;
                *) all=0 ;;
            esac
        done
        if [ "$all" = 1 ]; then ok "全部服务正常" "All services are healthy"; return 0; fi
        now="$(date +%s)"
        if [ -n "$bad" ] || [ $((now - start)) -ge "$timeout" ]; then
            warn "服务未能就绪:${bad:- (超时)}" "Services not ready:${bad:- (timeout)}"
            dc ps || true
            for svc in ${bad:-$(expected_services)}; do
                printf '\n--- %s (last 40 log lines; token lines hidden) ---\n' "$svc"
                dc logs --no-color --tail 40 "$svc" 2>&1 | grep -v -i 'SETUP TOKEN' || true
            done
            return 1
        fi
        sleep "${OA_POLL_SECONDS:-3}"
    done
}

cmd_status() {
    need_install; need_docker
    dc ps
    printf '\n'
    local svc
    for svc in $(expected_services); do printf '  %-9s %s\n' "$svc" "$(service_state "$svc")"; done
    printf '\nURL: %s\n' "$(env_get OA_PUBLIC_URL)"
    if db_external; then printf '%s: %s %s:%s/%s (%s)\n' "$(m '数据库' 'Database')" "$(env_get OA_DB_PROVIDER)" "$(env_get OA_DB_HOST)" "$(env_get OA_DB_PORT)" "$(env_get OA_DB_NAME)" "$(m '外部服务器；密码在 .env' 'external server; password in .env')"
    else printf '%s: %s\n' "$(m '数据库' 'Database')" "$(m '内置 PostgreSQL' 'bundled PostgreSQL')"; fi
}

cmd_start() { need_install; need_docker; owner_fix; dc up -d; cmd_wait; }
cmd_stop() { need_install; need_docker; dc stop; }

cmd_restart() {
    need_install; need_docker
    local svc="${1:-}"
    case "$svc" in
        '' | api | worker)
            # worker shares api's network namespace: always recreate both together
            dc up -d --force-recreate api worker
            if [ -z "$svc" ]; then
                if db_external; then if https_on; then dc restart caddy; fi
                elif https_on; then dc restart postgres caddy; else dc restart postgres; fi
            fi
            ;;
        *) dc restart "$svc" ;;
    esac
    cmd_wait
}

cmd_logs() {
    need_install; need_docker
    local args=() svc=''
    while [ $# -gt 0 ]; do
        case "$1" in
            -f | --follow) args+=(--follow); shift ;;
            --tail) args+=(--tail "${2:?}"); shift 2 ;;
            -*) die "未知选项 $1" "unknown option $1" ;;
            *) svc="$1"; shift ;;
        esac
    done
    case " $(printf '%s' "${args[*]:-}") " in *--tail*) ;; *) args+=(--tail 200) ;; esac
    if [ -n "$svc" ]; then dc logs --no-color "${args[@]}" "$svc"; else dc logs --no-color "${args[@]}"; fi
}

cmd_ps() { need_install; need_docker; dc ps -a; }

cmd_setup_token() {
    need_install; need_docker
    local tok out
    tok="$(dc logs --no-color api 2>/dev/null | grep -o 'SETUP TOKEN: *[A-Za-z0-9_-]*' | tail -n 1 | sed 's/^SETUP TOKEN: *//' || true)"
    if [ -n "$tok" ]; then printf '%s\n' "$tok"; return 0; fi
    out="$(dc exec -T api oa-admin setup-token 2>&1 || true)"
    if [ -n "$out" ]; then
        printf '%s\n' "$out"
    else
        warn "没有可用的初始化令牌（可能已完成初始化）。" "No setup token available (setup may already be complete)."
        return 1
    fi
}

cmd_admin() {
    need_install; need_docker
    [ $# -gt 0 ] || { dc exec -T api oa-admin help || true; return 0; }
    if [ -t 0 ] && [ -t 1 ]; then dc exec api oa-admin "$@"; else dc exec -T api oa-admin "$@"; fi
}

mask_env() {
    awk -F= '
        /^[[:space:]]*#/ || NF == 0 { print; next }
        { k = $1; v = substr($0, length(k) + 2)
          if (k ~ /(PASSWORD|TOKEN|SECRET|API_KEY)/ && v != "") v = "******** (hidden)"
          print k "=" v }' "$1"
}

config_key_ok() { # whitelist + per-key validation: config_key_ok KEY VALUE
    local k="$1" v="$2"
    case "$v" in *[$'\n\r\t ']* | *'"'* | *"'"* | *'$'* | *'\'* | *'`'* | *'#'*) return 1 ;; esac
    case "$k" in
        OA_PORT | OA_HTTP_PORT | OA_HTTPS_PORT) valid_port "$v" ;;
        OA_STORAGE) [ "$v" = on ] || [ "$v" = off ] ;;
        OA_PG_MEM | OA_API_MEM | OA_WORKER_MEM | OA_CADDY_MEM | OA_LOG_MAX_SIZE) [[ "$v" =~ ^[0-9]+[kKmMgG]$ ]] ;;
        OA_LOG_MAX_FILE | OA_LLM_TIMEOUT_SECONDS) [[ "$v" =~ ^[0-9]+$ ]] ;;
        OA_MAX_UPLOAD_BYTES | OA_MAX_BATCH_FILES | OA_DISK_RESERVE_BYTES | OA_UPLOAD_SESSION_TTL_HOURS | OA_EXTRACT_MAX_BYTES) [[ "$v" =~ ^[0-9]*$ ]] ;;
        OA_TZ) [[ "$v" =~ ^[A-Za-z0-9_+/-]{1,64}$ ]] ;;
        OA_LOG_LEVEL) [[ "$v" =~ ^(|info|debug)$ ]] ;;
        OA_PUBLIC_URL) [[ "$v" =~ ^https?://[A-Za-z0-9.:_-]+$ ]] ;;
        OA_LLM_BASE_URL) [[ "$v" =~ ^(|https?://[^[:space:]]+)$ ]] ;;
        OA_LLM_MODEL) [[ "$v" =~ ^[A-Za-z0-9._:/-]*$ ]] ;;
        OA_LLM_API_KEY) [[ "$v" =~ ^[A-Za-z0-9._:/+=-]*$ ]] ;;
        OA_ACME_EMAIL) [[ "$v" =~ ^(|[^@[:space:]]+@[^@[:space:]]+\.[^@[:space:]]+)$ ]] ;;
        OA_VERSION) [[ "$v" =~ ^[A-Za-z0-9._-]{1,64}$ ]] ;;
        OA_IMAGE_REGISTRY) [[ "$v" =~ ^[A-Za-z0-9._:/-]{1,200}$ ]] ;;
        *) return 2 ;;
    esac
}

cmd_config() {
    need_install
    local sub="${1:-show}"
    shift || true
    case "$sub" in
        show) mask_env "$(env_file)" ;;
        set)
            [ $# -eq 2 ] || die "用法: config set <KEY> <VALUE>（VALUE 为 - 时从标准输入读取）" "usage: config set <KEY> <VALUE> (use - to read VALUE from stdin)"
            local k="$1" v="$2" rc=0
            if [ "$v" = "-" ]; then IFS= read -r v || [ -n "$v" ] || v=''; fi
            config_key_ok "$k" "$v" || rc=$?
            if [ "$rc" = 2 ]; then
                die "$k 不允许通过 config set 修改（密钥/域名请用专用命令或手工编辑）。" "$k cannot be changed with config set (secrets / domain have dedicated commands)."
            elif [ "$rc" != 0 ]; then
                die "$k 的值不合法（不允许空白、引号、\$、#、反斜杠等）。" "Invalid value for $k (no whitespace, quotes, \$, #, backslash...)."
            fi
            env_set "$k" "$v"
            ok "已设置 $k；运行 oa-agentctl restart 使其生效" "$k set; run oa-agentctl restart to apply"
            ;;
        *) die "用法: config show | config set <KEY> <VALUE>" "usage: config show | config set <KEY> <VALUE>" ;;
    esac
}

render_caddyfile() { # render_caddyfile DOMAIN EMAIL
    local tpl="$DIR/Caddyfile.template" out="$DIR/Caddyfile" emailline='' tmp
    [ -f "$tpl" ] || die "缺少 $tpl" "Missing $tpl"
    [ -z "$2" ] || emailline="email $2"
    tmp="$(mktemp "$DIR/.Caddyfile.XXXXXX")"
    sed -e "s|__DOMAIN__|$1|g" -e "s|__EMAIL_LINE__|$emailline|g" "$tpl" >"$tmp"
    chmod 644 "$tmp"
    mv -f "$tmp" "$out"
}

cmd_domain() {
    need_install
    local fqdn='' nostart=0 off=0
    while [ $# -gt 0 ]; do
        case "$1" in
            --no-start) nostart=1; shift ;;
            --none | --off) off=1; shift ;;
            -*) die "未知选项 $1" "unknown option $1" ;;
            *) fqdn="$1"; shift ;;
        esac
    done
    local port email
    port="$(env_get OA_PORT)"; port="${port:-8080}"
    if [ "$off" = 1 ]; then
        confirm "切换回仅 HTTP（Passkey 将不可用）？" "Switch back to HTTP only (Passkeys become unavailable)?" || { info "已取消" "Cancelled"; return 1; }
        cf_set_https off; env_set OA_DOMAIN ''; env_set OA_BIND_ADDR 0.0.0.0; env_set OA_TRUSTED_PROXY 0
        env_set OA_PUBLIC_URL "http://$(host_ip):$port"
    else
        [ -n "$fqdn" ] || die "用法: domain <域名> | domain --none" "usage: domain <fqdn> | domain --none"
        valid_domain "$fqdn" || die "域名格式不正确: $fqdn" "Invalid domain: $fqdn"
        local old
        old="$(env_get OA_DOMAIN)"
        if [ -n "$old" ] && [ "$old" != "$fqdn" ]; then
            warn "更换域名会使已注册的 Passkey 失效（绑定在旧域名上），并需在 设置→安全 中更新 RP ID/来源；TOTP 不受影响。" \
                "Changing the domain invalidates registered Passkeys (bound to the old domain) and requires updating RP ID/origins in Settings; TOTP is unaffected."
        fi
        confirm "切换到 HTTPS，域名 $fqdn（需要 80/443 端口对公网开放且域名已解析到本机）？" "Switch to HTTPS for $fqdn (ports 80/443 must be reachable and the DNS record must point here)?" || { info "已取消" "Cancelled"; return 1; }
        email="$(env_get OA_ACME_EMAIL)"
        render_caddyfile "$fqdn" "$email"
        cf_set_https on; env_set OA_DOMAIN "$fqdn"; env_set OA_BIND_ADDR 127.0.0.1; env_set OA_TRUSTED_PROXY 1
        env_set OA_PUBLIC_URL "https://$fqdn"
    fi
    ok "配置已更新" "Configuration updated"
    if [ "$nostart" = 0 ]; then
        need_docker
        dc up -d --force-recreate --remove-orphans
        cmd_wait
    fi
}

backup_warning() {
    warn "备份文件包含加密密钥（OA_SECRET_KEY）、数据库口令与全部归档文件：请像对待密码一样保管，不要上传到不受信任的位置。" \
        "The backup contains the encryption key (OA_SECRET_KEY), the database password and all archived files: store it like a password and never upload it to untrusted places."
}

cmd_backup() {
    need_install; need_docker
    local out="$DIR/backups" skip_archive=0 quiet_path=0
    while [ $# -gt 0 ]; do
        case "$1" in
            --out) out="${2:?}"; shift 2 ;;
            --skip-archive) skip_archive=1; shift ;;
            --print-path) quiet_path=1; shift ;;
            *) die "未知选项 $1" "unknown option $1" ;;
        esac
    done
    mkdir -p "$out"; chmod 700 "$out"
    if ! db_external; then
        [ "$(service_state postgres)" = healthy ] || die "postgres 未运行；先执行 oa-agentctl start" "postgres is not running; run oa-agentctl start first"
    fi
    local ad pd stage file
    ad="$(path_of OA_ARCHIVE_DIR archive)"; pd="$(path_of OA_APP_DIR app)"
    stage="$(mktemp -d "$out/.stage.XXXXXX")"
    # shellcheck disable=SC2064
    trap "rm -rf -- '$stage'; trap - RETURN" RETURN
    file="$out/oa-agent-backup-$(ts).tar.gz"
    if db_external; then
        warn "数据库在外部服务器上（$(env_get OA_DB_PROVIDER) $(env_get OA_DB_HOST)）：本备份【不含数据库】，只含 .env、密钥与归档文件。请用数据库自带的工具另行备份（pg_dump / mysqldump --single-transaction / BACKUP DATABASE），并与本备份对应保管。" \
            "The database lives on an external server ($(env_get OA_DB_PROVIDER) $(env_get OA_DB_HOST)): this backup does NOT contain it, only .env, the key and the archive files. Back the database up with its own tools (pg_dump / mysqldump --single-transaction / BACKUP DATABASE) and keep that backup together with this one."
    else
        info "导出数据库 …" "Dumping database ..."
        dc exec -T postgres pg_dump -U oa -d oa -Fc >"$stage/db.dump"
        [ -s "$stage/db.dump" ] || die "pg_dump 输出为空" "pg_dump produced no output"
    fi
    cp "$(env_file)" "$stage/env"; chmod 600 "$stage/env"
    mkdir -p "$stage/app"
    if [ -f "$pd/secret.key" ]; then cp "$pd/secret.key" "$stage/app/secret.key"; chmod 600 "$stage/app/secret.key"; fi
    if [ "$skip_archive" = 0 ] && [ -d "$ad" ]; then ln -s "$ad" "$stage/archive"; fi
    printf 'oa-agent backup\ncreated=%s\nversion=%s\nskip_archive=%s\ndb=%s\n' "$(date -u +%FT%TZ)" "$(env_get OA_VERSION)" "$skip_archive" "$(if db_external; then echo external; else echo bundled; fi)" >"$stage/MANIFEST"
    info "打包文件 …" "Archiving files ..."
    tar -C "$stage" -czhf "$file" .
    chmod 600 "$file"
    ok "备份完成: $file" "Backup written: $file"
    backup_warning
    if [ "$quiet_path" = 1 ]; then printf '%s\n' "$file" >"$DIR/.last-backup"; fi
}

cmd_restore() {
    need_install; need_docker
    local file="${1:-}"
    [ -n "$file" ] || die "用法: restore <备份文件>" "usage: restore <backup-file>"
    [ -f "$file" ] || die "文件不存在: $file" "No such file: $file"
    local ext_backup=0
    if tar -tzf "$file" 2>/dev/null | grep -q '^\./db\.dump$'; then
        db_external && die "这份备份含内置数据库的 dump，而当前安装使用外部数据库：请用数据库自带的工具还原，不能用 oa-agentctl restore。" "This backup holds a dump of the bundled database but the current installation uses an external one: restore it with the database's own tools, not with oa-agentctl restore."
    elif tar -xzOf "$file" ./MANIFEST 2>/dev/null | grep -q '^db=external$'; then
        db_external || die "这份备份来自外部数据库的安装（不含数据库）；当前安装使用内置数据库，无法还原。" "This backup comes from an installation with an external database (it holds no database); the current installation uses the bundled one."
        ext_backup=1
    else
        die "不是有效的 OA 备份（缺少 db.dump）" "Not a valid OA backup (db.dump missing)"
    fi
    warn "还原会【覆盖】当前数据库与归档目录（旧归档目录会被改名保留为 archive.pre-restore-*）。" \
        "Restore OVERWRITES the current database and archive directory (the old archive dir is renamed to archive.pre-restore-*)."
    confirm "确认还原？" "Proceed with restore?" || { info "已取消" "Cancelled"; return 1; }
    local ad pd stage stamp
    ad="$(path_of OA_ARCHIVE_DIR archive)"; pd="$(path_of OA_APP_DIR app)"; stamp="$(ts)"
    stage="$(mktemp -d "$DIR/.restore.XXXXXX")"
    # shellcheck disable=SC2064
    trap "rm -rf -- '$stage'; trap - RETURN" RETURN
    tar -C "$stage" -xzf "$file"
    if [ "$ext_backup" = 0 ]; then [ -s "$stage/db.dump" ] || die "db.dump 为空" "db.dump is empty"; fi

    info "停止 api / worker …" "Stopping api / worker ..."
    dc stop worker api || true
    if [ "$ext_backup" = 1 ]; then
        info "外部数据库：不还原数据库（请用数据库自带的工具还原），只还原归档文件/密钥。" "External database: the database is not restored (use the database's own tools); only archive files and the key are restored."
    else
        dc up -d postgres
        local n=0
        until [ "$(service_state postgres)" = healthy ]; do
            n=$((n + 1)); [ "$n" -le 60 ] || die "postgres 未就绪" "postgres did not become healthy"
            sleep "${OA_POLL_SECONDS:-3}"
        done
        info "还原数据库 …" "Restoring database ..."
        dc exec -T postgres psql -U oa -d postgres -v ON_ERROR_STOP=1 -c 'DROP DATABASE IF EXISTS oa WITH (FORCE)'
        dc exec -T postgres psql -U oa -d postgres -v ON_ERROR_STOP=1 -c 'CREATE DATABASE oa OWNER oa'
        dc exec -T postgres pg_restore -U oa -d oa --no-owner --exit-on-error <"$stage/db.dump"
    fi

    if [ -d "$stage/archive" ]; then
        info "还原归档文件 …" "Restoring archive files ..."
        if [ -d "$ad" ]; then mv "$ad" "$ad.pre-restore-$stamp"; fi
        mv "$stage/archive" "$ad"
    else
        info "备份不含归档文件，保留当前归档目录。" "Backup has no archive files; keeping the current archive directory."
    fi
    if [ -f "$stage/app/secret.key" ]; then
        mkdir -p "$pd"
        cp "$stage/app/secret.key" "$pd/secret.key"; chmod 600 "$pd/secret.key"
    fi
    if [ -f "$stage/env" ]; then
        local oldkey curkey
        oldkey="$(grep -E '^OA_SECRET_KEY=' "$stage/env" | tail -n 1 | cut -d= -f2- || true)"
        curkey="$(env_get OA_SECRET_KEY)"
        if [ -n "$oldkey" ] && [ "$oldkey" != "$curkey" ]; then
            warn "备份中的 OA_SECRET_KEY 与当前不同：已采用备份中的密钥（否则已保存的凭据无法解密）。" \
                "OA_SECRET_KEY in the backup differs from the current one: using the backup's key (otherwise saved credentials cannot be decrypted)."
            env_set OA_SECRET_KEY "$oldkey"
        fi
    fi
    owner_fix
    dc up -d
    cmd_wait
    ok "还原完成" "Restore complete"
}

# registry_mode: the stack runs pre-built images (no build layer in COMPOSE_FILE)
registry_mode() { case ":$(env_get COMPOSE_FILE):" in *:docker-compose.build.yml:*) return 1 ;; *) return 0 ;; esac; }

# refresh_management TAG: replace compose files, oa-agentctl.sh and (checksum verified) the Go binary with the ones in the
# <registry>/oa-agent-deploy:TAG image. Never touches .env, mounts.json or docker-compose.mounts.yml.
# Returns 0 ok, 1 = bundle could not be fetched (caller warns and continues), 2 = checksum failure (caller aborts).
refresh_management() {
    local tag="$1" reg image tmp cid='' f arch bin_dir want got ctl_tmp
    reg="$(env_get OA_IMAGE_REGISTRY)"; reg="${reg%/}"
    [ -n "$reg" ] || return 1
    image="$reg/oa-agent-deploy:$tag"
    tmp="$(mktemp -d)"
    info "从镜像更新管理文件 $image …" "Refreshing management files from $image ..."
    if ! docker pull "$image" >&2; then rm -rf -- "$tmp"; return 1; fi
    if ! cid="$(docker create "$image" 2>/dev/null)" || [ -z "$cid" ]; then rm -rf -- "$tmp"; return 1; fi
    if ! docker cp "$cid:/deploy/." "$tmp" >/dev/null 2>&1; then docker rm -f "$cid" >/dev/null 2>&1 || true; rm -rf -- "$tmp"; return 1; fi
    docker rm -f "$cid" >/dev/null 2>&1 || true
    [ -f "$tmp/docker-compose.yml" ] || { rm -rf -- "$tmp"; return 1; }
    case "$(uname -m)" in aarch64 | arm64) arch=arm64 ;; *) arch=amd64 ;; esac
    ctl_tmp=''
    if [ -f "$tmp/bin/oa-agentctl-linux-$arch" ]; then
        want="$(awk -v f="oa-agentctl-linux-$arch" '$2==f || $2=="*"f {print $1}' "$tmp/bin/SHA256SUMS" 2>/dev/null | head -n 1)"
        got="$(sha256sum "$tmp/bin/oa-agentctl-linux-$arch" | awk '{print $1}')"
        if [ -z "$want" ] || [ "$want" != "$got" ]; then rm -rf -- "$tmp"; return 2; fi
        ctl_tmp="$tmp/bin/oa-agentctl-linux-$arch"
    fi
    for f in docker-compose.yml docker-compose.build.yml docker-compose.https.yml docker-compose.external-db.yml Caddyfile.template .env.example; do
        [ -f "$tmp/$f" ] || continue
        cp -f "$tmp/$f" "$DIR/.$f.new" && chmod 644 "$DIR/.$f.new" && mv -f "$DIR/.$f.new" "$DIR/$f"
    done
    bin_dir="${OA_CTL_SELF:+$(dirname -- "$OA_CTL_SELF")}"
    bin_dir="${bin_dir:-$(dirname -- "${BASH_SOURCE[0]}")}"
    if [ -f "$tmp/oa-agentctl.sh" ] && [ -w "$bin_dir" ]; then
        sed "s|^OA_DEFAULT_DIR=.*|OA_DEFAULT_DIR='$DIR'|" "$tmp/oa-agentctl.sh" >"$bin_dir/.oa-agentctl.sh.new"
        chmod 755 "$bin_dir/.oa-agentctl.sh.new"; mv -f "$bin_dir/.oa-agentctl.sh.new" "$bin_dir/oa-agentctl.sh"
        # the "oa-agentctl" next to it is either the Go binary (ELF) or a copy of this script: refresh whichever it is
        if [ -f "$bin_dir/oa-agentctl" ]; then
            if [ -n "$ctl_tmp" ] && [ "$(head -c 4 "$bin_dir/oa-agentctl" 2>/dev/null)" = $'\x7f'ELF ]; then
                cp -f "$ctl_tmp" "$bin_dir/.oa-agentctl.new"; chmod 755 "$bin_dir/.oa-agentctl.new"; mv -f "$bin_dir/.oa-agentctl.new" "$bin_dir/oa-agentctl"
                ok "oa-agentctl 已更新（SHA-256 校验通过）" "oa-agentctl updated (SHA-256 verified)"
            elif [ "$(head -c 4 "$bin_dir/oa-agentctl" 2>/dev/null)" != $'\x7f'ELF ]; then
                cp -f "$bin_dir/oa-agentctl.sh" "$bin_dir/.oa-agentctl.new"; mv -f "$bin_dir/.oa-agentctl.new" "$bin_dir/oa-agentctl"
            fi
        fi
    fi
    rm -rf -- "$tmp"
    ok "管理文件已更新" "Management files refreshed"
}

cmd_upgrade() {
    need_install; need_docker
    local newver='' skipbackup=0 oldver tag rc
    while [ $# -gt 0 ]; do
        case "$1" in
            --version | --tag) newver="${2:?}"; shift 2 ;;
            --no-backup) skipbackup=1; shift ;;
            *) die "未知选项 $1" "unknown option $1" ;;
        esac
    done
    oldver="$(env_get OA_VERSION)"
    if [ "$skipbackup" = 0 ]; then
        info "升级前先备份 …" "Backing up before upgrade ..."
        cmd_backup --print-path
    fi
    if [ -n "$newver" ]; then config_key_ok OA_VERSION "$newver" || die "版本号不合法" "Invalid version"; env_set OA_VERSION "$newver"; fi
    tag="$(env_get OA_VERSION)"
    if registry_mode; then
        if [ "${OA_OFFLINE:-0}" = 1 ]; then
            info "离线模式：使用已载入的镜像（不拉取，不刷新管理文件）。" "Offline mode: using the already loaded images (no pull, no management-file refresh)."
        else
            info "拉取镜像 $(env_get OA_IMAGE_REGISTRY)/oa-agent-{api,worker}:$tag …" "Pulling images $(env_get OA_IMAGE_REGISTRY)/oa-agent-{api,worker}:$tag ..."
            dc pull || die "拉取镜像失败：检查仓库可访问、标签 $tag 是否存在（私有仓库先 docker login）；OA_VERSION 可用 oa-agentctl config set OA_VERSION ${oldver:-<旧版本>} 还原。" \
                "Image pull failed: check the registry is reachable and tag $tag exists (docker login first for private repositories); restore OA_VERSION with oa-agentctl config set OA_VERSION ${oldver:-<old>}."
            if [ -n "$(env_get OA_IMAGE_REGISTRY)" ] && { [ "$tag" != "$oldver" ] || [ "$tag" = latest ]; }; then
                rc=0; refresh_management "$tag" || rc=$?
                case "$rc" in
                    0) ;;
                    2) die "升级已中止：预编译 oa-agentctl 校验和不匹配。" "Upgrade aborted: checksum mismatch for the prebuilt oa-agentctl." ;;
                    *) warn "未能从镜像更新管理文件；继续使用现有 compose 文件。" "Could not refresh the management files from the image; continuing with the existing compose files." ;;
                esac
            fi
        fi
        owner_fix
        # worker shares api's network namespace: always recreate both together
        dc up -d --force-recreate api worker
    else
        info "从源码构建镜像 …" "Building images from source ..."
        dc build --pull
        owner_fix
        dc up -d --remove-orphans
    fi
    if cmd_wait; then
        state_edit version "$tag"
        ok "升级完成" "Upgrade finished"
    elif registry_mode; then
        warn "升级后服务未就绪。回滚办法：oa-agentctl upgrade --tag ${oldver:-<旧标签>} --no-backup；必要时 oa-agentctl restore <升级前备份>（数据库迁移不会自动回退）" \
            "Services not healthy after upgrade. Rollback: oa-agentctl upgrade --tag ${oldver:-<old tag>} --no-backup; if needed oa-agentctl restore <pre-upgrade backup> (DB migrations are not reverted automatically)"
        return 1
    else
        warn "升级后服务未就绪。回滚办法：oa-agentctl config set OA_VERSION ${oldver:-<旧版本>}；oa-agentctl restore <升级前备份>（数据库迁移不会自动回退）" \
            "Services not healthy after upgrade. Rollback: oa-agentctl config set OA_VERSION ${oldver:-<old>}; oa-agentctl restore <pre-upgrade backup> (DB migrations are not reverted automatically)"
        return 1
    fi
}

cmd_mounts() {
    need_install
    local sub="${1:-show}"
    case "$sub" in
        show) if [ -f "$DIR/mounts.json" ]; then cat "$DIR/mounts.json"; else info "没有 mounts.json（使用默认目录）" "No mounts.json (default directories in use)"; fi ;;
        apply)
            local bin="${OA_CTL_BIN:-/usr/local/bin/oa-agentctl}"
            [ -x "$bin" ] || die "生成挂载配置需要 Go 版 oa-agentctl（$bin）；本脚本只是最小备用工具。" "Generating the mounts override needs the Go oa-agentctl ($bin); this script is only a minimal fallback."
            "$bin" mounts apply --dir "$DIR" ;;
        *) die "用法: mounts show | mounts apply" "usage: mounts show | mounts apply" ;;
    esac
}

cmd_version() {
    printf 'oa-agentctl %s\ninstall dir: %s\nOA_VERSION: %s\n' "$CTL_VERSION" "$DIR" "$(env_get OA_VERSION)"
    if [ -f "$DIR/.env" ] && command -v docker >/dev/null 2>&1; then dc ps --format '{{.Service}}\t{{.Image}}' 2>/dev/null || true; fi
}

cmd_uninstall() {
    need_install; need_docker
    local purge=0
    while [ $# -gt 0 ]; do
        case "$1" in --purge) purge=1; shift ;; *) die "未知选项 $1" "unknown option $1" ;; esac
    done
    confirm "停止并移除 OA Agent 容器（数据目录默认保留）？" "Stop and remove OA Agent containers (data is kept by default)?" || { info "已取消" "Cancelled"; return 1; }
    dc down --remove-orphans
    local unit="${OA_UNIT_DIR:-/etc/systemd/system}/oa-agent.service"
    if [ -f "$unit" ]; then
        if command -v systemctl >/dev/null 2>&1; then systemctl disable oa-agent.service >/dev/null 2>&1 || true; fi
        rm -f -- "$unit"
        if command -v systemctl >/dev/null 2>&1; then systemctl daemon-reload >/dev/null 2>&1 || true; fi
    fi
    if [ "$purge" = 1 ]; then
        local base typed
        # safety: DIR must be absolute, deep enough, contain our marker files, and be confirmed by typing its name
        case "$DIR" in /*) ;; *) die "安装目录必须是绝对路径" "install dir must be absolute" ;; esac
        case "$DIR" in / | /usr | /usr/* | /bin | /sbin | /etc | /var | /home | /root | /boot | /lib* | /opt | /mnt | /srv | /tmp) die "拒绝清除危险目录: $DIR" "refusing to purge dangerous directory: $DIR" ;; esac
        [ -f "$DIR/.env" ] && [ -f "$DIR/docker-compose.yml" ] || die "目录不像 OA Agent 安装目录" "directory does not look like an OA Agent install"
        base="$(basename -- "$DIR")"
        [ -n "$base" ] || die "目录名为空" "empty directory name"
        warn "将永久删除 $DIR 下的全部数据（数据库、归档文件、密钥、备份）！" "This permanently deletes everything under $DIR (database, archived files, key, backups)!"
        if [ "$ASSUME_YES" = 1 ] && [ "${OA_PURGE_CONFIRM:-}" != "$base" ]; then
            die "--yes 不能确认清除；需手动输入目录名，或设置 OA_PURGE_CONFIRM=$base" "--yes cannot confirm a purge; type the directory name or set OA_PURGE_CONFIRM=$base"
        fi
        if [ "${OA_PURGE_CONFIRM:-}" = "$base" ]; then typed="$base"; else
            [ -t 0 ] || die "需要交互输入目录名确认" "typing the directory name is required"
            printf '%s ' "$(m "输入目录名 '$base' 以确认:" "Type the directory name '$base' to confirm:")"
            read -r typed || typed=''
        fi
        [ "$typed" = "$base" ] || { info "名称不符，已取消清除（容器已移除，数据保留）" "Name mismatch; purge cancelled (containers removed, data kept)"; return 1; }
        rm -rf -- "${DIR:?}"
        state_edit forget
        ok "已清除 $DIR" "Purged $DIR"
    else
        state_edit removed
        ok "已卸载容器；数据仍在 $DIR/data（含 .env 与密钥）。重新运行 install.sh 可恢复运行。" "Containers removed; data remains in $DIR/data (with .env and key). Re-run install.sh to bring it back."
    fi
    local self="${OA_CTL_SELF:-/usr/local/bin/oa-agentctl}"
    if [ "$purge" = 1 ] && [ "$(basename -- "$self")" = oa-agentctl ]; then rm -f -- "$self" "$self.sh"; fi
}

cmd_doctor() {
    local fails=0 warns=0 v
    pass() { ok "$1" "$2"; }
    bad() { fails=$((fails + 1)); printf '%s[FAIL]%s %s\n' "$C_R" "$C_0" "$(m "$1" "$2")"; }
    wrn() { warns=$((warns + 1)); warn "$1" "$2"; }

    command -v docker >/dev/null 2>&1 && pass "docker 已安装" "docker installed" || { bad "未安装 docker" "docker is not installed"; exit 1; }
    docker compose version >/dev/null 2>&1 && pass "Docker Compose v2 可用" "Docker Compose v2 available" || bad "缺少 Docker Compose v2" "Docker Compose v2 missing"
    docker info >/dev/null 2>&1 && pass "Docker 守护进程运行中" "Docker daemon running" || bad "Docker 守护进程不可用" "Docker daemon not reachable"
    if [ ! -f "$DIR/.env" ]; then bad "未找到安装: $DIR" "no installation in $DIR"; exit 1; fi
    local mode
    mode="$(stat -c %a "$DIR/.env" 2>/dev/null || echo '?')"
    [ "$mode" = 600 ] && pass ".env 权限 600" ".env mode 600" || wrn ".env 权限为 $mode，应为 600（chmod 600 $DIR/.env）" ".env mode is $mode, should be 600 (chmod 600 $DIR/.env)"
    v="$(env_get OA_SECRET_KEY)"
    [[ "$v" =~ ^[0-9a-fA-F]{64}$ ]] && pass "OA_SECRET_KEY 格式正确" "OA_SECRET_KEY format ok" || bad "OA_SECRET_KEY 不是 64 位十六进制" "OA_SECRET_KEY is not 64 hex chars"
    v="$(env_get OA_WORKER_TOKEN)"
    [ "${#v}" -ge 32 ] && pass "OA_WORKER_TOKEN 长度足够" "OA_WORKER_TOKEN long enough" || bad "OA_WORKER_TOKEN 少于 32 字符" "OA_WORKER_TOKEN shorter than 32 chars"

    local avail_kb
    avail_kb="$(df -Pk "$DIR" 2>/dev/null | awk 'NR==2{print $4}' || true)"
    if [ -n "$avail_kb" ]; then
        if [ "$avail_kb" -lt 2097152 ]; then bad "磁盘剩余不足 2GB" "less than 2 GB free on disk"
        elif [ "$avail_kb" -lt 10485760 ]; then wrn "磁盘剩余不足 10GB" "less than 10 GB free on disk"
        else pass "磁盘空间充足" "disk space ok"; fi
    fi

    local svc st port
    for svc in $(expected_services); do
        st="$(service_state "$svc")"
        case "$st" in healthy | running) pass "$svc: $st" "$svc: $st" ;; *) bad "$svc: $st" "$svc: $st" ;; esac
    done
    port="$(env_get OA_PORT)"; port="${port:-8080}"
    if port_listening "$port"; then pass "端口 $port 在监听" "port $port is listening"; else bad "端口 $port 未监听" "port $port is not listening"; fi
    if db_external; then
        pass "数据库：外部 $(env_get OA_DB_PROVIDER) $(env_get OA_DB_HOST):$(env_get OA_DB_PORT)（由 API 的健康检查覆盖连接状态；备份需用数据库自带工具）" "database: external $(env_get OA_DB_PROVIDER) $(env_get OA_DB_HOST):$(env_get OA_DB_PORT) (connectivity is covered by the API health check; back it up with the database's own tools)"
    elif [ "$(service_state postgres)" = healthy ] && dc exec -T postgres psql -U oa -d oa -tAc 'select 1' >/dev/null 2>&1; then
        pass "数据库连接正常" "database connection ok"
    else bad "无法连接数据库" "cannot connect to the database"; fi
    if command -v curl >/dev/null 2>&1; then
        if curl -fsS -m 5 "http://127.0.0.1:$port/healthz" >/dev/null 2>&1; then pass "API /healthz 正常" "API /healthz ok"; else bad "API /healthz 无响应" "API /healthz not answering"; fi
    fi
    https_on || wrn "当前为纯 HTTP 部署：Passkey 不可用（仅 TOTP），且口令明文传输；建议绑定域名启用 HTTPS（oa-agentctl domain <域名>）。" "HTTP-only deployment: Passkeys are unavailable (TOTP only) and traffic is unencrypted; use a domain for HTTPS (oa-agentctl domain <fqdn>)."
    local sync=''
    if command -v timedatectl >/dev/null 2>&1; then sync="$(timedatectl show -p NTPSynchronized --value 2>/dev/null || true)"; fi
    case "$sync" in
        yes) pass "系统时钟已同步" "system clock is synchronized" ;;
        no) wrn "系统时钟未同步：TOTP 动态码依赖准确时间，请启用 chrony/systemd-timesyncd。" "System clock is NOT synchronized: TOTP depends on correct time; enable chrony/systemd-timesyncd." ;;
        *) if command -v chronyc >/dev/null 2>&1 && chronyc tracking >/dev/null 2>&1; then pass "chrony 运行中" "chrony is running"
           else wrn "无法确认时间同步状态：请确认 NTP 已启用（TOTP 依赖准确时间）。" "Cannot verify time sync: make sure NTP is enabled (TOTP depends on correct time)."; fi ;;
    esac
    printf '\n%s\n' "$(m "诊断完成：$fails 项失败，$warns 项警告" "Done: $fails failure(s), $warns warning(s)")"
    [ "$fails" -eq 0 ]
}

usage() {
    if [ "$UI" = zh ]; then
        cat <<'EOF'
用法: oa-agentctl [--dir 安装目录] [--yes] <命令> [参数]

  status                    服务状态与访问地址
  start | stop              启动 / 停止全部服务
  restart [服务]            重启（api 与 worker 总是一起重建）
  ps                        容器列表
  logs [服务] [-f] [--tail N]
  upgrade [--tag T] [--no-backup]   先备份；镜像模式：拉取标签 T（默认 .env 中的 OA_VERSION）、更新管理文件并重建 api+worker；源码模式：构建并重启
  backup [--out 目录] [--skip-archive]  备份数据库+归档+密钥（含加密密钥，请妥善保管）
  restore <文件>            从备份还原（会覆盖当前数据）
  setup-token               显示初始化令牌
  admin <oa-admin 参数…>    管理员命令: reset-password / reset-mfa / unlock / create-admin / list-users …
  config show               显示配置（密钥已遮蔽）
  config set <KEY> <值>     修改白名单内的配置（值为 - 时从标准输入读取）
  domain <域名> | --none    切换到 HTTPS（域名）或回到 HTTP
  mounts show|apply         显示 mounts.json / 调用 Go 版重新生成 compose 挂载
  doctor                    诊断
  version                   版本信息
  uninstall [--purge]       卸载容器（默认保留数据；--purge 需输入目录名确认后删除全部数据）
  wait [--timeout 秒]       等待服务健康（内部使用）
EOF
    else
        cat <<'EOF'
Usage: oa-agentctl [--dir INSTALL_DIR] [--yes] <command> [args]

  status                    service states and URL
  start | stop              start / stop everything
  restart [service]         restart (api and worker are always recreated together)
  ps                        container list
  logs [service] [-f] [--tail N]
  upgrade [--tag T] [--no-backup]   back up first; registry mode: pull tag T (default OA_VERSION from .env), refresh management files, recreate api+worker; source mode: build and restart
  backup [--out DIR] [--skip-archive]   database + archive + key backup (contains the encryption key!)
  restore <file>            restore from a backup (overwrites current data)
  setup-token               print the setup-wizard token
  admin <oa-admin args...>  admin tool: reset-password / reset-mfa / unlock / create-admin / list-users ...
  config show               show configuration (secrets masked)
  config set <KEY> <VALUE>  change a whitelisted setting (VALUE '-' reads stdin)
  domain <fqdn> | --none    switch to HTTPS (domain) or back to HTTP
  mounts show|apply         show mounts.json / regenerate the compose mounts via the Go binary
  doctor                    diagnostics
  version                   version information
  uninstall [--purge]       remove containers (data kept; --purge deletes all data after typing the dir name)
  wait [--timeout SEC]      wait for healthy services (internal)
EOF
    fi
}

main() {
    while [ $# -gt 0 ]; do
        case "$1" in
            --dir) DIR="${2:?--dir needs a value}"; shift 2 ;;
            --yes | -y) ASSUME_YES=1; shift ;;
            -h | --help) usage; exit 0 ;;
            *) break ;;
        esac
    done
    case "$DIR" in /*) ;; *) die "安装目录必须是绝对路径: $DIR" "install dir must be absolute: $DIR" ;; esac
    local cmd="${1:-help}"
    shift || true
    case "$cmd" in
        status) cmd_status "$@" ;;
        start) cmd_start "$@" ;;
        stop) cmd_stop "$@" ;;
        restart) cmd_restart "$@" ;;
        ps) cmd_ps "$@" ;;
        logs) cmd_logs "$@" ;;
        upgrade) cmd_upgrade "$@" ;;
        backup) cmd_backup "$@" ;;
        restore) cmd_restore "$@" ;;
        setup-token) cmd_setup_token "$@" ;;
        admin) cmd_admin "$@" ;;
        config) cmd_config "$@" ;;
        domain) cmd_domain "$@" ;;
        mounts) cmd_mounts "$@" ;;
        version | --version) cmd_version "$@" ;;
        uninstall) cmd_uninstall "$@" ;;
        doctor) need_install; cmd_doctor "$@" ;;
        wait) need_install; need_docker; cmd_wait "$@" ;;
        help | -h | --help) usage ;;
        *) usage >&2; exit 2 ;;
    esac
}

main "$@"
