#!/usr/bin/env bash
# Offline test-suite for install.sh / oa-agentctl.sh. No Docker needed: fake docker/curl/ss/systemctl/... shims are put first
# in PATH and every call is recorded in <sandbox>/calls.log. Everything happens below $OA_TEST_TMP (default: a mktemp dir).
# Usage: bash deploy/linux/test/run.sh [test_name_pattern]
set -u
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LINUX="$(cd "$HERE/.." && pwd)"
ROOT="$(cd "$LINUX/../.." && pwd)"
BASE="${OA_TEST_TMP:-$(mktemp -d)}"
mkdir -p "$BASE"
PASS=0; FAIL=0; FAILED=()
export OA_SUMMARY=full   # tests assert on the long closing screen; the compact one has its own test (t_summary_report)
FILTER="${1:-}"

# ---------------------------------------------------------------------------------------------- sandbox
new_sandbox() { # sets SB, prepares mini repo + shims
    SB="$BASE/$1"; rm -rf -- "${SB:?}"; mkdir -p "$SB"/{shim,state,bin,units,repo/deploy,repo/backend/cmd/oa-agentctl,repo/frontend,repo/migrations}
    cp -r "$LINUX" "$SB/repo/deploy/linux"; rm -rf "$SB/repo/deploy/linux/test"
    cp -r "$ROOT/deploy/docker" "$SB/repo/deploy/docker"
    echo 'package main' >"$SB/repo/backend/cmd/oa-agentctl/main.go"
    : >"$SB/calls.log"
    printf 'ID=ubuntu\nID_LIKE=debian\nPRETTY_NAME="Ubuntu 24.04 (fake)"\n' >"$SB/os-release"
    printf 'MemTotal:       16000000 kB\n' >"$SB/meminfo"
    make_shims
    export FAKE_SB="$SB" FAKE_EXAMPLE="$LINUX/docker-compose.mounts.example.yml"
    # detection / state-file overrides honoured by install.sh in test mode (a sandbox never sees the real /etc, /run or /opt)
    export OA_TICK=0.1   # poll interval of the Docker install loop (default 1 s)
    export OA_INSTALL_STATE="$SB/etc/install.json" OA_INSTALL_DEFAULT_DIR="$SB/nodefault" OA_SYSTEMD_RUN_DIR="$SB" OA_TEST_PKG_STATE="$SB/state/pkg-installed"
    unset FAKE_GETDOCKER_HANG FAKE_GETDOCKER_HANG_ONCE FAKE_GETDOCKER_SLEEP FAKE_PS_PROCS FAKE_PS_LATE FAKE_PS_LATE_FROM FAKE_LOCK_RELEASE_AFTER OA_TEST_ENVKIND OA_DOCKER_TIMEOUT OA_HEARTBEAT OA_LOCK_WAIT OA_APT_PARTIAL OA_DPKG_LOG OA_APT_TERM_LOG OA_TEST_MISSING_CMDS FAKE_GEO FAKE_OFFICIAL_DOWN FAKE_MIRROR_DOWN FAKE_MIRROR_FAST FAKE_RMIRROR_DOWN OA_TEST_TZ FAKE_NO_HUB FAKE_PS_EXTRA FAKE_COMPOSE_LS FAKE_COMPOSE_V1 FAKE_DOCKER_ENABLED FAKE_UNITS FAKE_DOCKER_DOWN FAKE_DOCKER_HOST FAKE_CTX_HOST FAKE_PODMAN FAKE_COMPOSE_VERSION FAKE_CHECK_RC FAKE_CHECK_OUT FAKE_INIT_OUT FAKE_INIT_RC FAKE_APPLY_RC FAKE_IPT_DROP 2>/dev/null || true
    unset FAKE_PORTS FAKE_UNHEALTHY FAKE_NO_COMPOSE FAKE_UFW FAKE_PULL_FAIL FAKE_COMPOSE_PULL_FAIL FAKE_LOGIN_FAIL FAKE_BUNDLE FAKE_KERNEL FAKE_SELINUX FAKE_NTP FAKE_CGROUP FAKE_DOCKER_VERSION FAKE_PULL_FLAKY FAKE_FAIL_IMAGE FAKE_MISSING_IMAGE FAKE_ECS_REGION FAKE_ECS_EIP FAKE_PUBLIC_IP FAKE_GETDOCKER_FAIL FAKE_GETDOCKER_BADMIRROR FAKE_NO_REGISTRY FAKE_IPS4 FAKE_IPS6 FAKE_DOCKER_ROOT FAKE_DOCKER_SECOPT FAKE_SAVE_FAIL FAKE_IMG_ARCH FAKE_LOAD_FAIL FAKE_PKG_FAIL FAKE_BUILD_FLAKY 2>/dev/null || true
}

make_shims() {
    local s="$SB/shim"
    cat >"$s/docker.real" <<'SH'
#!/usr/bin/env bash
SB="${FAKE_SB:?}"; echo "docker $*" >>"$SB/calls.log"
services() { local l="postgres api worker"; case ":${COMPOSE_FILE:-}:" in *:docker-compose.https.yml:*) l="$l caddy" ;; esac; echo "$l"; }
case "${1:-}" in
  --version) if [ -n "${FAKE_PODMAN:-}" ]; then echo "podman version 4.9.3"; else echo "Docker version ${FAKE_DOCKER_VERSION:-26.1.0}, build fake"; fi; exit 0 ;;
  context) echo "${FAKE_CTX_HOST:-unix:///var/run/docker.sock}"; exit 0 ;;
  info) if [ -n "${FAKE_DOCKER_DOWN:-}" ] && [ ! -e "$SB/state/daemon-started" ]; then echo "Cannot connect to the Docker daemon" >&2; exit 1; fi
    case "$*" in *DockerRootDir*) echo "${FAKE_DOCKER_ROOT:-}" ;; *SecurityOptions*) echo "${FAKE_DOCKER_SECOPT:-[name=seccomp,profile=builtin]}" ;; esac; exit 0 ;;
  pull) if [ -n "${FAKE_PULL_FAIL:-}" ]; then echo "Error response from daemon: manifest for $2 not found: manifest unknown" >&2; exit 1; fi
    if [ -n "${FAKE_FAIL_IMAGE:-}" ] && [[ "$2" == *"$FAKE_FAIL_IMAGE"* ]]; then echo "Error response from daemon: pull access denied for $2" >&2; exit 1; fi
    if [ "${FAKE_PULL_FLAKY:-0}" -gt 0 ]; then n=$(cat "$SB/state/flaky-pull" 2>/dev/null || echo 0); echo $((n + 1)) >"$SB/state/flaky-pull"; if [ "$n" -lt "$FAKE_PULL_FLAKY" ]; then echo "Error response from daemon: Get https://x/v2/: net/http: TLS handshake timeout" >&2; exit 1; fi; fi
    touch "$SB/state/pulled-$(echo "$2" | tr "/:" "__")"; echo "fake pull $2"; exit 0 ;;
  load) [ -z "${FAKE_LOAD_FAIL:-}" ] || { echo "open /var/lib/docker: no space left on device" >&2; exit 1; }; echo "Loaded image: fake"; exit 0 ;;
  image) case "${2:-}" in
      inspect) img="${@: -1}"; if [ -n "${FAKE_MISSING_IMAGE:-}" ] && [[ "$img" == *"$FAKE_MISSING_IMAGE"* ]] && [ ! -e "$SB/state/pulled-$(echo "$img" | tr "/:" "__")" ]; then exit 1; fi
        case "$*" in *Architecture*) echo "${FAKE_IMG_ARCH:-$(case "$(uname -m)" in aarch64|arm64) echo arm64 ;; *) echo amd64 ;; esac)}" ;; esac; exit 0 ;;
    esac; exit 0 ;;
  save) out=''; imgs=(); shift; while [ $# -gt 0 ]; do case "$1" in -o) out="$2"; shift ;; *) imgs+=("$1") ;; esac; shift; done
    [ -z "${FAKE_SAVE_FAIL:-}" ] || { echo "Error response from daemon: reference does not exist" >&2; exit 1; }
    d="$(mktemp -d)"; { printf '[{"Config":"blobs/x","RepoTags":['; sep=''; for i in "${imgs[@]}"; do printf '%s"%s"' "$sep" "$i"; sep=','; done; printf '],"Layers":[]}]'; } >"$d/manifest.json"
    (cd "$d" && tar -cf "$out" manifest.json); rm -rf "$d"; exit 0 ;;
  create) echo "cid-bundle-$RANDOM"; exit 0 ;;
  cp) dest="${@: -1}"; mkdir -p "$dest"; [ -d "${FAKE_BUNDLE:-/nonexistent}" ] && cp -a "$FAKE_BUNDLE/." "$dest/"; exit 0 ;;
  rm) exit 0 ;;
  login) cat >"$SB/state/login-stdin"; [ -z "${FAKE_LOGIN_FAIL:-}" ] || { echo "Error: unauthorized" >&2; exit 1; }; exit 0 ;;
  version) echo "${FAKE_DOCKER_VERSION:-26.1.0}"; exit 0 ;;
  ps) case "$*" in
      *.Label*) # rich listing used by detection: name|image|state|project|workdir|ports|service|status
        wd="$(cat "$SB/state/workdir" 2>/dev/null || true)"; proj="$(cat "$SB/state/project" 2>/dev/null || true)"; proj="${proj:-oa-agent}"
        for f in "$SB"/state/up-*; do
          [ -e "$f" ] || continue
          svc="$(basename "$f" | sed 's/^up-//')"; st="Up 5 minutes (healthy)"; [ "${FAKE_UNHEALTHY:-}" = "$svc" ] && st="Up 5 minutes (unhealthy)"
          pp=""; [ "$svc" = api ] && pp="0.0.0.0:$(grep -E '^OA_PORT=' "$wd/.env" 2>/dev/null | cut -d= -f2 || echo 8080)->8080/tcp"
          echo "$proj-$svc-1|reg.example.com/ns/oa-agent-$svc:latest|running|$proj|$wd|$pp|$svc|$st"
        done
        [ -z "${FAKE_PS_EXTRA:-}" ] || printf '%s\n' "$FAKE_PS_EXTRA" | tr ';' '\n'
        exit 0 ;;
      *) for f in "$SB"/state/up-*; do [ -e "$f" ] && echo "oa-agent-$(basename "$f" | sed 's/^up-//')-1"; done; exit 0 ;;
    esac ;;
  inspect) svc="${@: -1}"; svc="${svc#cid-}"
    if [ -e "$SB/state/up-$svc" ]; then [ "${FAKE_UNHEALTHY:-}" = "$svc" ] && echo unhealthy || echo healthy; else echo exited; fi; exit 0 ;;
  run) case " $* " in *" --entrypoint oa-admin "*)
      # fake "oa-admin db-check|db-init" of the api image. $SB/state/dbmode: ok-empty (default) | wrongpw (only the password "right" works) | noiso | has-data | down
      sub=''; seen=0; for a in "$@"; do if [ "$seen" = 2 ]; then sub="$a"; break; fi; [ "$seen" = 1 ] && seen=2; [ "$a" = oa-admin ] && seen=1; done
      IFS= read -r pw; echo "$pw" >"$SB/state/db-stdin-last"; echo "$pw" >>"$SB/state/db-stdin-all"
      mode="$(cat "$SB/state/dbmode" 2>/dev/null || echo ok-empty)"; T=$'\t'
      chk() { printf 'CHECK\t%s\t%s\t%s\t%s\n' "$1" "$2" "$3" "$4"; }
      case " $* " in *" OA_LANG=en "*) # the real tool prints English titles/details with OA_LANG=en
        chk() { case "$2" in connect) t=Connection ;; version) t="Server version" ;; perm_schema) t=Permissions ;; isolation) t="Isolation (dedicated database/schema)" ;; *) t="$3" ;; esac
          d="$4"; case "$d" in 延迟*) d="latency 3 ms" ;; CREATE*) d="CREATE privilege present" ;; 仅包含*) d="only OA Agent tables (dedicated database)" ;; public*) d="public contains tables that do not belong to OA Agent (orders, customers)" ;; esac
          printf 'CHECK\t%s\t%s\t%s\t%s\n' "$1" "$2" "$t" "$d"; } ;; esac
      if [ "$mode" = down ] || { [ "$mode" = wrongpw ] && [ "$pw" != right ]; }; then
        chk fail connect 连接数据库 'password authentication failed for user "oa"'; printf 'INFO\tprovider\tpostgres\nRESULT\tfail\tcannot connect\n'; exit 1
      fi
      schema=empty; [ "$mode" = has-data ] && schema=ok
      case "$sub" in
        db-check)
          chk pass connect 连接数据库 '延迟 3 ms'; chk pass version 服务器版本 'PostgreSQL 16.4'; chk pass perm_schema 'schema 权限' 'CREATE 权限已具备'
          if [ "$mode" = noiso ]; then chk fail isolation '隔离性' 'public 中存在非 OA Agent 的表（orders, customers）'; printf 'INFO\tprovider\tpostgres\nINFO\tserver_version\t16.4\nINFO\tisolation\tnot_isolated\nINFO\tschema\tempty\nRESULT\tfail\tthe target is not isolated\n'; exit 1; fi
          chk pass isolation '隔离性' '仅包含 OA Agent 的表（专用数据库）'
          printf 'INFO\tprovider\tpostgres\nINFO\tserver_version\t16.4\nINFO\tisolation\tdedicated_database\nINFO\tschema\t%s\nRESULT\tok\t\n' "$schema"; exit 0 ;;
        db-init)
          migs=5; [ "$schema" = ok ] && migs=0
          printf 'PLAN\tstate=%s\tmigrations=%s\tstatements=87\tdestructive=false\ttransactional=true\tfingerprint=0123456789abcdef01234567\n' "$schema" "$migs"
          if [ "$migs" -gt 0 ]; then
            printf 'STEP\t1\tinitial\t40\nSTEP\t2\tbusiness\t47\n'
            case " $* " in *" --show-ddl "*) printf 'DDL\tCREATE TABLE users (id uuid PRIMARY KEY)\n' ;; esac
            case " $* " in
              *" --apply "*) case " $* " in *" --plan 0123456789abcdef01234567 "*) echo applied >"$SB/state/db-applied"; printf 'RESULT\tok\tapplied 5 migration(s)\n'; exit 0 ;; *) printf 'RESULT\tfail\tplan fingerprint mismatch\n'; exit 1 ;; esac ;;
            esac
            printf 'RESULT\tok\tpreview only: nothing was changed\n'
          else printf 'RESULT\tok\tnothing to apply: the schema is already current\n'; fi
          exit 0 ;;
      esac ;; esac
    out=''; for a in "$@"; do case "$a" in *:/out) out="${a%:/out}" ;; esac; done
    if [ -z "$out" ]; then printf '#!/usr/bin/env bash\necho fake-deploy-image-install.sh\n'; exit 0; fi
    printf '#!/usr/bin/env bash\necho "oa-agentctl $*" >>"$FAKE_SB/calls.log"\nif [ "$1 $2" = "mounts apply" ]; then cp "$FAKE_EXAMPLE" "$4/docker-compose.mounts.yml"; fi\n' >"$out/oa-agentctl"; chmod +x "$out/oa-agentctl"; exit 0 ;;
  compose)
    shift
    if [ "${1:-}" = version ]; then [ -n "${FAKE_NO_COMPOSE:-}" ] && [ ! -e "$SB/state/compose-plugin" ] && exit 1; cv="${FAKE_COMPOSE_VERSION:-2.29.0}"; [ "${2:-}" = --short ] && echo "$cv" || echo "Docker Compose version v$cv"; exit 0; fi
    if [ "${1:-}" = ls ]; then echo "compose-ls" >>"$SB/calls.log"; printf '%s' "${FAKE_COMPOSE_LS:-[]}"; exit 0; fi
    echo "compose[CF=${COMPOSE_FILE:-}] $*" >>"$SB/calls.log"
    cmd="$1"; shift
    targets=(); for a in "$@"; do case "$a" in -*) ;; *) targets+=("$a") ;; esac; done
    case "$cmd" in
      build) if [ "${FAKE_BUILD_FLAKY:-0}" -gt 0 ]; then n=$(cat "$SB/state/flaky-build" 2>/dev/null || echo 0); echo $((n + 1)) >"$SB/state/flaky-build"; if [ "$n" -lt "$FAKE_BUILD_FLAKY" ]; then echo "failed to solve: temporary failure in name resolution" >&2; exit 1; fi; fi; echo "fake $cmd progress" ;;
      pull) [ -z "${FAKE_COMPOSE_PULL_FAIL:-}" ] || { echo "denied: requested access to the resource is denied" >&2; exit 1; }
          if [ "${FAKE_PULL_FLAKY:-0}" -gt 0 ]; then n=$(cat "$SB/state/flaky-cpull" 2>/dev/null || echo 0); echo $((n + 1)) >"$SB/state/flaky-cpull"; if [ "$n" -lt "$FAKE_PULL_FLAKY" ]; then echo "error pulling image: net/http: TLS handshake timeout" >&2; exit 1; fi; fi
          echo "fake pull progress" ;;
      up) if [ "${#targets[@]}" -eq 0 ]; then targets=($(services)); fi; for t in "${targets[@]}"; do touch "$SB/state/up-$t"; done
          echo "$PWD" >"$SB/state/workdir"; grep -E '^COMPOSE_PROJECT_NAME=' .env 2>/dev/null | cut -d= -f2 >"$SB/state/project" || true
          if [ -z "${FAKE_API_UP_ONLY:-}" ]; then :; fi ;;
      stop) if [ "${#targets[@]}" -eq 0 ]; then targets=($(services)); fi; for t in "${targets[@]}"; do rm -f "$SB/state/up-$t"; done ;;
      down) rm -f "$SB"/state/up-* ;;
      restart) ;;
      ps) if [ "${1:-}" = -q ]; then [ -e "$SB/state/up-${2:-}" ] && echo "cid-$2"; else for t in $(services); do [ -e "$SB/state/up-$t" ] && echo "$t running"; done; fi; exit 0 ;;
      logs) case " $* " in *" api "*) echo "api  | listening on :8080"; echo "api  | SETUP TOKEN: ABCDEFGH23456789TOKEN" ;; *) echo "log line" ;; esac ;;
      exec) shift_args=("$@"); i=0; [ "${shift_args[0]}" = -T ] && i=1; svc="${shift_args[$i]}"; tool="${shift_args[$((i+1))]}"
          case "$tool" in pg_dump) printf 'PGDUMP-BYTES' ;; psql|pg_restore) cat >/dev/null ;; oa-admin) echo "oa-admin ran: ${shift_args[*]:$((i+2))}" ;; esac ;;
    esac
    exit 0 ;;
esac
exit 0
SH
    cp "$s/docker.real" "$s/docker"
    cat >"$s/curl" <<'SH'
#!/usr/bin/env bash
echo "curl $*" >>"$FAKE_SB/calls.log"
out=''; url=''; while [ $# -gt 0 ]; do case "$1" in -o) out="$2"; shift ;; http*) url="$1" ;; esac; shift; done
case "$url" in
  *get.docker.com*) [ -z "${FAKE_GETDOCKER_FAIL:-}" ] || exit 22
    printf '#!/bin/sh\necho "get-docker.sh $* DOWNLOAD_URL=${DOWNLOAD_URL:-}" >>"$FAKE_SB/calls.log"\nif [ -n "$FAKE_GETDOCKER_HANG" ]; then exec sleep 987; fi\nif [ -n "$FAKE_GETDOCKER_HANG_ONCE" ] && [ ! -e "$FAKE_SB/state/hung" ]; then touch "$FAKE_SB/state/hung"; exec sleep 987; fi\nsleep "${FAKE_GETDOCKER_SLEEP:-0}"\nif [ -n "$FAKE_GETDOCKER_BADMIRROR" ] && [ "$2" != AzureChinaCloud ]; then echo mirror-failed >&2; exit 1; fi\ncp "$FAKE_SB/shim/docker.real" "$FAKE_SB/shim/docker"\n' >"$out"; exit 0 ;;
  *127.0.0.1*healthz*) exit 0 ;;
  *100.100.100.200*region-id) [ -n "${FAKE_ECS_REGION:-}" ] || exit 28; printf '%s' "$FAKE_ECS_REGION"; exit 0 ;;
  *100.100.100.200*eipv4) [ -n "${FAKE_ECS_EIP:-}" ] || exit 28; printf '%s' "$FAKE_ECS_EIP"; exit 0 ;;
  *api.ipify.org*) [ -n "${FAKE_PUBLIC_IP:-}" ] || exit 6; printf '%s' "$FAKE_PUBLIC_IP"; exit 0 ;;
  *ipinfo.io*) case "${FAKE_GEO-US}" in timeout) exit 28 ;; blocked) exit 7 ;; *) printf '%s\n' "${FAKE_GEO-US}" ;; esac; exit 0 ;;
  *download.docker.com/linux/*) [ -z "${FAKE_OFFICIAL_DOWN:-}" ] || exit 28; printf '200 0.05'; exit 0 ;;
  *docker-ce/linux/*) for t in ${FAKE_MIRROR_DOWN:-}; do case "$url" in *"$t"*) exit 28 ;; esac; done
    case "$url" in *"${FAKE_MIRROR_FAST:-@@@}"*) printf '200 0.01' ;; *) printf '200 0.05' ;; esac; exit 0 ;;
  *registry-1.docker.io*) [ -z "${FAKE_NO_HUB:-}" ] || exit 28; printf 401; exit 0 ;;
  */v2/) [ -z "${FAKE_NO_REGISTRY:-}" ] || exit 6; for t in ${FAKE_RMIRROR_DOWN:-}; do case "$url" in *"$t"*) exit 28 ;; esac; done; printf 401; exit 0 ;;
esac
exit 22
SH
    cat >"$s/ss" <<'SSSH'
#!/usr/bin/env bash
# FAKE_PORTS="80=nginx 5432=postgres@127.0.0.1 8080": port[=process][@bind]; the users: column only appears with -p
for e in ${FAKE_PORTS:-}; do
  p="${e%%=*}"; rest=""; [ "$e" != "$p" ] && rest="${e#*=}"
  n="${rest%%@*}"; b="0.0.0.0"; case "$rest" in *@*) b="${rest#*@}" ;; esac
  u=""; case "$*" in *p*) [ -n "$n" ] && u=" users:((\"$n\",pid=4242,fd=6))" ;; esac
  echo "LISTEN 0 4096 $b:$p 0.0.0.0:*$u"
done
SSSH
    cat >"$s/systemctl" <<'SCSH'
#!/usr/bin/env bash
echo "systemctl $*" >>"$FAKE_SB/calls.log"
case "${1:-}" in
  is-enabled) [ "${FAKE_DOCKER_ENABLED:-yes}" = yes ]; exit $? ;;
  list-unit-files) for u in ${FAKE_UNITS:-}; do echo "$u enabled enabled"; done; exit 0 ;;
  start) case "${2:-}" in docker) touch "$FAKE_SB/state/daemon-started" ;; esac ;;
esac
exit 0
SCSH
    printf '#!/usr/bin/env bash\n[ -n "$FAKE_COMPOSE_V1" ] || exit 127\necho "$FAKE_COMPOSE_V1"\n' >"$s/docker-compose"
    cat >"$s/ps" <<'PSSH'
#!/usr/bin/env bash
# FAKE_PS_PROCS="pid etime command;..." ; FAKE_LOCK_RELEASE_AFTER=N: the processes disappear after N calls; FAKE_PS_LATE (lines) appear from call FAKE_PS_LATE_FROM on
n=$(cat "$FAKE_SB/state/ps-calls" 2>/dev/null || echo 0); echo $((n + 1)) >"$FAKE_SB/state/ps-calls"
echo "  PID     ELAPSED COMMAND"
if [ -n "${FAKE_LOCK_RELEASE_AFTER:-}" ] && [ "$n" -ge "$FAKE_LOCK_RELEASE_AFTER" ]; then exit 0; fi
printf '%s\n' "${FAKE_PS_PROCS:-}" | tr ';' '\n' | grep -v '^$'
if [ -n "${FAKE_PS_LATE:-}" ] && [ "$n" -ge "${FAKE_PS_LATE_FROM:-3}" ]; then printf '%s\n' "$FAKE_PS_LATE" | tr ';' '\n'; fi
exit 0
PSSH
    printf '#!/usr/bin/env bash\necho "ufw $*" >>"$FAKE_SB/calls.log"\n[ "$1" = status ] && [ -n "${FAKE_UFW:-}" ] && echo "Status: active"\nexit 0\n' >"$s/ufw"
    printf '#!/usr/bin/env bash\nprintf "Filesystem 1024-blocks Used Available Capacity Mounted\\nfake 999999999 1 500000000 1pct /\\n"\n' >"$s/df"
    printf '#!/usr/bin/env bash\n[ "$1" = -I ] && echo 192.0.2.10 || echo testhost\n' >"$s/hostname"
    printf '#!/usr/bin/env bash\necho "${FAKE_NTP:-yes}"\n' >"$s/timedatectl"
    printf '#!/usr/bin/env bash\n[ "$1" = -r ] && { echo "${FAKE_KERNEL:-6.1.0-fake}"; exit 0; }\nPATH=/usr/bin:/bin exec uname "$@"\n' >"$s/uname"
    printf '#!/usr/bin/env bash\necho "${FAKE_SELINUX:-Disabled}"\n' >"$s/getenforce"
    printf '#!/usr/bin/env bash\necho 4\n' >"$s/nproc"
    cat >"$s/ip" <<'IPSH'
#!/usr/bin/env bash
# fake "ip -o -4|-6 addr show scope global": FAKE_IPS4 = "iface=addr ..." (default: a real NIC plus the docker bridge)
fam=4; [ "$2" = -6 ] && fam=6; [ "$1" = -o ] && [ "$2" = -6 ] && fam=6
if [ "$fam" = 4 ]; then for e in ${FAKE_IPS4-eth0=192.0.2.10 docker0=172.17.0.1}; do echo "2: ${e%%=*}    inet ${e#*=}/24 brd 0.0.0.0 scope global ${e%%=*}"; done
else for e in ${FAKE_IPS6:-}; do echo "2: ${e%%=*}    inet6 ${e#*=}/64 scope global"; done; fi
IPSH
    for p in apt-get dnf yum zypper apk; do printf '#!/usr/bin/env bash\necho "%s $*" >>"$FAKE_SB/calls.log"\n[ -z "$FAKE_PKG_FAIL" ] || exit 100\ncase " $* " in *" install "*|*" add "*) for a in "$@"; do case "$a" in -*|install|add|update) ;; *) echo "$a" >>"$FAKE_SB/state/pkg-installed" ;; esac; done; case " $* " in *docker*) cp "$FAKE_SB/shim/docker.real" "$FAKE_SB/shim/docker" ;; esac; case " $* " in *compose*) touch "$FAKE_SB/state/compose-plugin" ;; esac ;; esac\nexit 0\n' "$p" >"$s/$p"; done
    # fake stat: the file system type is configurable (FAKE_FSTYPE, default ext4), so the tests never depend on what /tmp is on
    cat >"$s/stat" <<'SH'
#!/usr/bin/env bash
if [ "${1:-}" = -f ] && [ "${2:-}" = -c ] && [ "${3:-}" = %T ]; then if [ "${4:-}" = /sys/fs/cgroup ]; then echo "${FAKE_CGROUP:-cgroup2fs}"; else echo "${FAKE_FSTYPE:-ext4}"; fi; exit 0; fi
PATH=/usr/bin:/bin exec stat "$@"
SH
    chmod +x "$s"/*
    [ -n "${NO_DOCKER:-}" ] && rm -f "$s/docker"
    return 0
}

run_install() { # run_install args... ; output in $SB/out.txt, status in RC
    OA_TEST_MODE=1 OA_INSTALL_LOG="$SB/install.log" OA_INSTALL_BIN_DIR="$SB/bin" OA_INSTALL_UNIT_DIR="$SB/units" OA_INSTALL_ETC_MARKER="$SB/etc/install_dir" \
        OA_OS_RELEASE="$SB/os-release" OA_MEMINFO="$SB/meminfo" OA_POLL_SECONDS=0 OA_RETRY_BASE=0 OA_INSTALL_LOCK="$SB/lock" OA_DAEMON_JSON="$SB/daemon.json" OA_TEST_DOCKER_SHIM="$SB/shim/docker" \
        OA_SUMMARY="${OA_SUMMARY:-full}" OA_BUILD_FROM_SOURCE="${OA_BUILD_FROM_SOURCE:-1}" PATH="$SB/shim:$PATH" bash "$SB/repo/deploy/linux/install.sh" --lang en "$@" >"$SB/out.txt" 2>&1
    RC=$?
}
run_ctl() { # run_ctl args... (bash fallback ctl against $SB/oa)
    OA_POLL_SECONDS=0 OA_UNIT_DIR="$SB/units" OA_STATE_FILE="$SB/etc/install.json" OA_CTL_SELF="$SB/bin/oa-agentctl" OA_LANG=en PATH="$SB/shim:$PATH" \
        bash "$SB/bin/oa-agentctl.sh" --dir "$SB/oa" "$@" >"$SB/ctl.txt" 2>&1
    RC=$?
}

# ---------------------------------------------------------------------------------------------- assertions
ok_() { PASS=$((PASS + 1)); }
bad_() { FAIL=$((FAIL + 1)); FAILED+=("$CUR: $1"); printf '    FAIL: %s\n' "$1"; }
assert_rc() { [ "$RC" -eq "$1" ] && ok_ || { bad_ "exit code $RC, expected $1"; tail -n 15 "${2:-$SB/out.txt}" | sed 's/^/      | /'; }; }
assert_rc_nz() { [ "$RC" -ne 0 ] && ok_ || bad_ "expected a non-zero exit code"; }
has() { grep -Eq -- "$2" "$1" 2>/dev/null; }
assert_has() { has "$1" "$2" && ok_ || bad_ "$1 should match /$2/"; }
assert_not() { has "$1" "$2" && bad_ "$1 should NOT match /$2/" || ok_; }
assert_file() { [ -e "$1" ] && ok_ || bad_ "missing $1"; }
assert_nofile() { [ ! -e "$1" ] && ok_ || bad_ "unexpected $1"; }
assert_eq() { [ "$1" = "$2" ] && ok_ || bad_ "expected '$2' got '$1' ($3)"; }
lineno() { grep -n -E -- "$2" "$1" | head -n 1 | cut -d: -f1; }
assert_order() { # file pat1 pat2: pat1 appears before pat2
    local a b; a="$(lineno "$1" "$2")"; b="$(lineno "$1" "$3")"
    [ -n "$a" ] && [ -n "$b" ] && [ "$a" -lt "$b" ] && ok_ || bad_ "order: /$2/ ($a) before /$3/ ($b)"
}
envv() { grep -E "^$1=" "$SB/oa/.env" | tail -n 1 | cut -d= -f2-; }
mode() { stat -c %a "$1"; }

# ---------------------------------------------------------------------------------------------- tests
t_syntax() {
    local f
    for f in "$LINUX/install.sh" "$LINUX/oa-agentctl.sh" "$LINUX/build-release.sh" "$HERE/run.sh"; do
        bash -n "$f" && ok_ || bad_ "bash -n $f"
    done
    if command -v shellcheck >/dev/null 2>&1; then
        shellcheck -x -S warning "$LINUX/install.sh" "$LINUX/oa-agentctl.sh" "$LINUX/build-release.sh" && ok_ || bad_ "shellcheck"
    else echo "    (shellcheck not installed: skipped)"; fi
}

t_install_happy() {
    new_sandbox happy; run_install --yes --dir "$SB/oa" --port 18080
    assert_rc 0
    assert_file "$SB/oa/.env"; assert_eq "$(mode "$SB/oa/.env")" 600 ".env mode"
    [[ "$(envv OA_SECRET_KEY)" =~ ^[0-9a-f]{64}$ ]] && ok_ || bad_ "secret key format"
    [ "$(envv OA_WORKER_TOKEN | wc -c)" -ge 33 ] && ok_ || bad_ "worker token length"
    assert_eq "$(envv OA_TRUSTED_PROXY)" 0 "http: trusted proxy off"
    assert_eq "$(envv OA_BIND_ADDR)" 0.0.0.0 bind
    assert_eq "$(envv COMPOSE_FILE)" docker-compose.yml:docker-compose.mounts.yml:docker-compose.build.yml compose-file
    assert_eq "$(envv OA_PUBLIC_URL)" http://192.0.2.10:18080 url
    assert_file "$SB/oa/mounts.json"; assert_has "$SB/oa/mounts.json" '"archive": \{"host": "'"$SB"'/oa/data/archive"\}'
    assert_file "$SB/oa/docker-compose.mounts.yml"
    assert_has "$SB/calls.log" 'oa-agentctl mounts apply --dir '"$SB"'/oa'
    assert_has "$SB/calls.log" 'compose\[CF=[^]]*\] build'
    assert_has "$SB/calls.log" 'compose\[CF=[^]]*\] up -d'
    assert_order "$SB/calls.log" ' build' ' up -d'
    assert_file "$SB/bin/oa-agentctl"; assert_file "$SB/bin/oa-agentctl.sh"
    assert_has "$SB/out.txt" 'SETUP TOKEN: +ABCDEFGH23456789TOKEN'
    assert_has "$SB/out.txt" 'Passkeys are unavailable'
    assert_has "$SB/out.txt" 'setup wizard'
    # no secrets on screen or in the install log
    local k v
    for k in OA_SECRET_KEY OA_WORKER_TOKEN OA_POSTGRES_PASSWORD; do
        v="$(envv $k)"; assert_not "$SB/out.txt" "$v"; assert_not "$SB/install.log" "$v"
    done
    assert_not "$SB/install.log" 'ABCDEFGH23456789TOKEN'
    assert_eq "$(mode "$SB/install.log")" 600 "log mode"
    assert_nofile "$SB/oa/data/pg/x"
    assert_eq "$(mode "$SB/oa/data/pg")" 700 "pg dir mode"
    assert_nofile "$SB/units/oa-agent.service"
    assert_not "$SB/calls.log" '^ufw allow'
}

t_idempotent() {
    new_sandbox idem; run_install --yes --dir "$SB/oa"; assert_rc 0
    local before after; before="$(grep -E '^OA_(SECRET_KEY|WORKER_TOKEN|POSTGRES_PASSWORD)=' "$SB/oa/.env")"
    cp "$SB/oa/mounts.json" "$SB/mounts.before"
    run_install --yes --dir "$SB/oa" --port 9999; assert_rc 0
    after="$(grep -E '^OA_(SECRET_KEY|WORKER_TOKEN|POSTGRES_PASSWORD)=' "$SB/oa/.env")"
    assert_eq "$after" "$before" "secrets unchanged"
    assert_has "$SB/out.txt" 'Existing installation detected'
    assert_eq "$(envv OA_PORT)" 8080 "existing port kept"
    cmp -s "$SB/mounts.before" "$SB/oa/mounts.json" && ok_ || bad_ "mounts.json changed on re-run"
}

t_missing_docker() {
    NO_DOCKER=1 new_sandbox nodocker; run_install --yes --dir "$SB/oa"
    assert_rc_nz; assert_has "$SB/out.txt" '--install-docker'; assert_nofile "$SB/oa/.env"
    assert_not "$SB/calls.log" 'get-docker'
    NO_DOCKER=1 new_sandbox nodocker2; run_install --yes --dir "$SB/oa" --install-docker --mirror cn
    assert_rc 0; assert_has "$SB/calls.log" 'curl .*https://get.docker.com'; assert_has "$SB/calls.log" 'get-docker.sh --mirror Aliyun'
    assert_eq "$(envv OA_BUILD_GOPROXY)" 'https://goproxy.cn,direct' goproxy
    # unknown distro: no automatic install
    NO_DOCKER=1 new_sandbox nodocker3; printf 'ID=plan9\n' >"$SB/os-release"; run_install --yes --dir "$SB/oa" --install-docker
    assert_rc_nz
    # docker present but compose v2 missing
    new_sandbox nocompose; FAKE_NO_COMPOSE=1 run_install --yes --dir "$SB/oa"
    assert_rc_nz
}

t_fstype_warning() {
    # tmpfs/overlay archive dirs are warned about; a normal file system is not (fs type comes from the fake stat)
    new_sandbox fs_ext4; mkdir -p "$SB/disk"; run_install --yes --dir "$SB/oa" --archive-dir "$SB/disk/archive"
    assert_rc 0; assert_has "$SB/out.txt" 'filesystem ext4'; assert_not "$SB/out.txt" 'is on (tmpfs|overlay)'
    new_sandbox fs_overlay; mkdir -p "$SB/disk"; FAKE_FSTYPE=overlay run_install --yes --dir "$SB/oa" --archive-dir "$SB/disk/archive"
    assert_rc 0; assert_has "$SB/out.txt" 'is on overlay'
}

t_port_conflict() {
    new_sandbox port; FAKE_PORTS="18080" run_install --yes --dir "$SB/oa" --port 18080
    assert_rc_nz; assert_has "$SB/out.txt" '[Pp]ort 18080 is already in use'; assert_nofile "$SB/oa/.env"
    new_sandbox port2; FAKE_PORTS="443" run_install --yes --dir "$SB/oa" --domain oa.example.com
    assert_rc_nz; assert_has "$SB/out.txt" 'Port 443 is already in use'
}

t_https() {
    new_sandbox https; run_install --yes --dir "$SB/oa" --domain oa.example.com --email admin@example.com
    assert_rc 0
    assert_eq "$(envv COMPOSE_FILE)" docker-compose.yml:docker-compose.mounts.yml:docker-compose.https.yml:docker-compose.build.yml cf
    assert_eq "$(envv OA_BIND_ADDR)" 127.0.0.1 bind; assert_eq "$(envv OA_TRUSTED_PROXY)" 1 proxy
    assert_eq "$(envv OA_PUBLIC_URL)" https://oa.example.com url
    assert_file "$SB/oa/Caddyfile"; assert_has "$SB/oa/Caddyfile" '^oa\.example\.com \{'; assert_has "$SB/oa/Caddyfile" 'email admin@example.com'
    assert_not "$SB/oa/Caddyfile" '__'
    assert_has "$SB/calls.log" 'CF=[^]]*https[^]]*\] up -d'
    assert_has "$SB/out.txt" 'Caddy requests the certificate'
    assert_not "$SB/out.txt" 'Passkeys are unavailable'
    new_sandbox https2; run_install --yes --dir "$SB/oa" --domain 10.0.0.1; assert_rc_nz
    new_sandbox https3; run_install --yes --dir "$SB/oa" --domain 'bad;domain.com'; assert_rc_nz
    new_sandbox https4; run_install --yes --dir "$SB/oa" --domain oa.example.com --email 'x;y@z.com'; assert_rc_nz
}

t_mounts() {
    new_sandbox mounts; mkdir -p "$SB/nas1" "$SB/scans" "$SB/disk"
    FAKE_FSTYPE=tmpfs run_install --yes --dir "$SB/oa" --archive-dir "$SB/disk/archive" --pg-dir "$SB/disk/pg" --spool-dir "$SB/disk/spool" --data-dir "$SB/disk/app" \
        --extra-mount "nas1=$SB/nas1" --extra-mount "scans=$SB/scans:rw"
    assert_rc 0
    assert_dir() { [ -d "$1" ] && ok_ || bad_ "missing dir $1"; }
    assert_dir "$SB/disk/archive"; assert_eq "$(mode "$SB/disk/archive")" 750 "created 0750"
    assert_has "$SB/oa/mounts.json" '"name": "nas1", "host": "'"$SB"'/nas1", "container": "/mnt/storage/nas1", "readonly": true'
    assert_has "$SB/oa/mounts.json" '"name": "scans".*"readonly": false'
    assert_eq "$(envv OA_ARCHIVE_DIR)" "$SB/disk/archive" env-archive
    assert_eq "$(envv OA_PG_DIR)" "$SB/disk/pg" env-pg
    assert_has "$SB/out.txt" 'free, filesystem'
    assert_has "$SB/out.txt" 'is on tmpfs'
    assert_has "$SB/out.txt" 'Archive dir: +'"$SB"'/disk/archive'
    # rejected inputs
    local bad
    for bad in / /etc /usr/local /var/lib/docker /var/lib/docker/x /proc/1 relative/path "/tmp/a b" "/tmp/x/../y"; do
        new_sandbox mbad; run_install --yes --dir "$SB/oa" --archive-dir "$bad"; assert_rc_nz; assert_nofile "$SB/oa/.env"
    done
    new_sandbox mbad2; run_install --yes --dir "$SB/oa" --archive-dir "$SB/same" --spool-dir "$SB/same"; assert_rc_nz
    new_sandbox mbad3; mkdir -p "$SB/ro"; chmod 555 "$SB/ro"
    if [ "$(id -u)" -ne 0 ]; then run_install --yes --dir "$SB/oa" --archive-dir "$SB/ro/archive"; assert_rc_nz; assert_has "$SB/out.txt" 'not|Cannot'; fi
    chmod 755 "$SB/ro"
    new_sandbox mbad4; run_install --yes --dir "$SB/oa" --extra-mount "nas1=$SB/missing"; assert_rc_nz; assert_has "$SB/out.txt" 'does not exist'
    new_sandbox mbad5; run_install --yes --dir "$SB/oa" --extra-mount 'BAD NAME=/x'; assert_rc_nz
    new_sandbox mbad6; mkdir -p "$SB/n"; run_install --yes --dir "$SB/oa" --extra-mount "a=$SB/n" --extra-mount "a=$SB/n"; assert_rc_nz
    # the generated override (sample) keeps the container targets of the base file
    new_sandbox mbad7; mkdir -p "$SB/n"; run_install --yes --dir "$SB/oa" --archive-dir "$SB/arch" ; assert_rc 0
    # without the Go binary custom paths are refused, defaults still work (fallback stub)
    new_sandbox nogo; rm -rf "$SB/repo/backend/cmd/oa-agentctl"; run_install --yes --dir "$SB/oa" --archive-dir "$SB/arch2"
    assert_rc_nz; assert_has "$SB/out.txt" 'need the Go oa-agentctl'
    new_sandbox nogo2; rm -rf "$SB/repo/backend/cmd/oa-agentctl"; run_install --yes --dir "$SB/oa"
    assert_rc 0; assert_has "$SB/oa/docker-compose.mounts.yml" 'services: \{\}'; assert_has "$SB/out.txt" 'minimal bash'
}

t_prebuilt_binary() {
    new_sandbox prebuilt; mkdir -p "$SB/repo/bin"
    printf '#!/usr/bin/env bash\necho "prebuilt $*" >>"$FAKE_SB/calls.log"\nif [ "$1 $2" = "mounts apply" ]; then cp "$FAKE_EXAMPLE" "$4/docker-compose.mounts.yml"; fi\n' >"$SB/repo/bin/oa-agentctl-linux-amd64"
    cp "$SB/repo/bin/oa-agentctl-linux-amd64" "$SB/repo/bin/oa-agentctl-linux-arm64"
    (cd "$SB/repo/bin" && sha256sum oa-agentctl-linux-* >SHA256SUMS)
    run_install --yes --dir "$SB/oa"; assert_rc 0
    assert_has "$SB/calls.log" '^prebuilt mounts apply'; assert_not "$SB/calls.log" 'docker run'
    assert_has "$SB/out.txt" 'SHA-256 verified'
    new_sandbox prebuilt2; mkdir -p "$SB/repo/bin"; echo evil >"$SB/repo/bin/oa-agentctl-linux-amd64"; cp "$SB/repo/bin/oa-agentctl-linux-amd64" "$SB/repo/bin/oa-agentctl-linux-arm64"
    echo "0000000000000000000000000000000000000000000000000000000000000000  oa-agentctl-linux-amd64" >"$SB/repo/bin/SHA256SUMS"
    echo "0000000000000000000000000000000000000000000000000000000000000000  oa-agentctl-linux-arm64" >>"$SB/repo/bin/SHA256SUMS"
    run_install --yes --dir "$SB/oa"; assert_rc_nz; assert_has "$SB/out.txt" 'Checksum mismatch'
}

t_firewall_systemd() {
    new_sandbox fw; FAKE_UFW=1 run_install --yes --dir "$SB/oa" --port 18081; assert_rc 0
    assert_not "$SB/calls.log" '^ufw allow'; assert_has "$SB/out.txt" 'was NOT modified'
    new_sandbox fw2; FAKE_UFW=1 run_install --yes --dir "$SB/oa" --port 18081 --open-firewall --systemd; assert_rc 0
    assert_has "$SB/calls.log" '^ufw allow 18081/tcp'
    assert_file "$SB/units/oa-agent.service"; assert_not "$SB/units/oa-agent.service" '__DIR__'
    assert_has "$SB/units/oa-agent.service" "WorkingDirectory=$SB/oa"
    assert_has "$SB/calls.log" '^systemctl enable oa-agent.service'
}

t_interactive() {
    new_sandbox inter; mkdir -p "$SB/data1"
    # answers in wizard order: dir, port, domain, database (1 = bundled), archive dir, customize? n, extra mount (empty), mirror, tag, review (Y)
    printf '%s\n' "$SB/oa" 18082 '' 1 "$SB/data1/archive" n '' default '' y >"$SB/answers"
    OA_INSTALL_TTY="$SB/answers" run_install --dir "$SB/oa"; assert_rc 0
    assert_eq "$(envv OA_PORT)" 18082 port; assert_eq "$(envv OA_ARCHIVE_DIR)" "$SB/data1/archive" archive
}

t_low_mem() {
    new_sandbox mem; printf 'MemTotal: 1000000 kB\n' >"$SB/meminfo"; run_install --yes --dir "$SB/oa"
    assert_rc 0; assert_has "$SB/out.txt" 'Less than 2 GB RAM'
}

t_no_start() {
    new_sandbox nostart; run_install --yes --dir "$SB/oa" --no-start; assert_rc 0
    assert_not "$SB/calls.log" 'compose.* up '; assert_file "$SB/oa/.env"
}

t_args() {
    new_sandbox args; run_install --help; assert_rc 0; assert_has "$SB/out.txt" '--archive-dir'
    run_install --bogus; assert_rc_nz
    run_install --yes --dir /etc; assert_rc_nz
    run_install --yes --dir "relative"; assert_rc_nz
    run_install --yes --dir "$SB/oa" --port 99999; assert_rc_nz
    run_install --yes --dir "$SB/oa" --mirror xx; assert_rc_nz
}

t_ctl_basic() {
    new_sandbox ctl; run_install --yes --dir "$SB/oa"; assert_rc 0
    run_ctl setup-token; assert_rc 0 "$SB/ctl.txt"; assert_eq "$(cat "$SB/ctl.txt")" ABCDEFGH23456789TOKEN token
    run_ctl config show; assert_not "$SB/ctl.txt" "$(envv OA_SECRET_KEY)"; assert_has "$SB/ctl.txt" 'OA_SECRET_KEY=\*+'
    run_ctl config set OA_PORT 9090; assert_rc 0 "$SB/ctl.txt"; assert_eq "$(envv OA_PORT)" 9090 port
    assert_eq "$(mode "$SB/oa/.env")" 600 "env mode after set"
    run_ctl config set OA_SECRET_KEY abc; assert_rc_nz
    run_ctl config set OA_PORT 'abc'; assert_rc_nz
    run_ctl config set OA_LLM_MODEL 'a b'; assert_rc_nz
    run_ctl config set OA_LLM_MODEL 'x$(id)'; assert_rc_nz
    run_ctl config set OA_LLM_API_KEY - <<<"sk-test123"; assert_rc 0 "$SB/ctl.txt"; assert_eq "$(envv OA_LLM_API_KEY)" sk-test123 stdin
    run_ctl admin reset-password alice; assert_rc 0 "$SB/ctl.txt"; assert_has "$SB/calls.log" 'exec -T api oa-admin reset-password alice'
    run_ctl restart; assert_rc 0 "$SB/ctl.txt"; assert_has "$SB/calls.log" 'up -d --force-recreate api worker'
    run_ctl logs api --tail 5; assert_rc 0 "$SB/ctl.txt"
    run_ctl doctor; assert_has "$SB/ctl.txt" 'Done: '; assert_has "$SB/ctl.txt" 'Passkeys are unavailable'; assert_has "$SB/ctl.txt" 'clock is synchronized'
    run_ctl mounts show; assert_has "$SB/ctl.txt" '"version": 1'
    run_ctl nonsense; assert_rc_nz
    FAKE_UNHEALTHY=worker run_ctl wait --timeout 1; assert_rc_nz "$SB/ctl.txt"; assert_has "$SB/ctl.txt" 'Services not ready: worker'
}

t_domain_switch() {
    new_sandbox dom; run_install --yes --dir "$SB/oa"; assert_rc 0
    run_ctl --yes domain a.example.org; assert_rc 0 "$SB/ctl.txt"
    assert_has "$SB/oa/.env" 'docker-compose.https.yml'; assert_eq "$(envv OA_TRUSTED_PROXY)" 1 proxy
    run_ctl --yes domain b.example.org; assert_has "$SB/ctl.txt" 'invalidates registered Passkeys'
    assert_has "$SB/oa/Caddyfile" '^b\.example\.org'
    run_ctl --yes domain --none; assert_rc 0 "$SB/ctl.txt"; assert_not "$SB/oa/.env" 'docker-compose.https.yml'; assert_eq "$(envv OA_TRUSTED_PROXY)" 0 proxy
    assert_has "$SB/oa/.env" '^COMPOSE_FILE=docker-compose.yml:docker-compose.mounts.yml:docker-compose.build.yml$'
    run_ctl --yes domain '1.2.3.4'; assert_rc_nz
}

t_backup_restore() {
    new_sandbox br; run_install --yes --dir "$SB/oa" --archive-dir "$SB/arch"; assert_rc 0
    echo "doc1" >"$SB/arch/file1.txt"; echo KEY >"$SB/oa/data/app/secret.key"
    run_ctl backup --out "$SB/bk"; assert_rc 0 "$SB/ctl.txt"
    local f; f="$(ls "$SB"/bk/oa-agent-backup-*.tar.gz)"; assert_eq "$(mode "$f")" 600 "backup mode"
    tar -tzf "$f" >"$SB/list.txt"
    assert_has "$SB/list.txt" '^\./db\.dump$'; assert_has "$SB/list.txt" '^\./env$'; assert_has "$SB/list.txt" '^\./app/secret\.key$'; assert_has "$SB/list.txt" 'archive/file1\.txt'
    assert_has "$SB/ctl.txt" 'contains the encryption key'
    assert_has "$SB/calls.log" 'exec -T postgres pg_dump -U oa -d oa -Fc'
    # restore: sequencing
    : >"$SB/calls.log"; echo "changed" >"$SB/arch/file1.txt"; echo newer >"$SB/arch/file2.txt"
    run_ctl --yes restore "$f"; assert_rc 0 "$SB/ctl.txt"
    assert_order "$SB/calls.log" 'compose.* stop worker api' 'up -d postgres'
    assert_order "$SB/calls.log" 'up -d postgres' 'DROP DATABASE'
    assert_order "$SB/calls.log" 'DROP DATABASE' 'CREATE DATABASE'
    assert_order "$SB/calls.log" 'CREATE DATABASE' 'pg_restore'
    assert_order "$SB/calls.log" 'pg_restore' 'compose\[CF=[^]]*\] up -d$'
    assert_eq "$(cat "$SB/arch/file1.txt")" doc1 "archive restored"
    ls -d "$SB"/arch.pre-restore-* >/dev/null 2>&1 && ok_ || bad_ "old archive not kept"
    assert_eq "$(cat "$SB/oa/data/app/secret.key")" KEY key
    run_ctl --yes restore "$SB/nonexistent"; assert_rc_nz
    echo junk >"$SB/junk.tar.gz"; run_ctl --yes restore "$SB/junk.tar.gz"; assert_rc_nz
    # restore without --yes and without a tty must refuse
    run_ctl restore "$f" </dev/null; assert_rc_nz
}

t_upgrade() {
    new_sandbox up; run_install --yes --dir "$SB/oa"; assert_rc 0
    : >"$SB/calls.log"; run_ctl --yes upgrade --version 1.2.3; assert_rc 0 "$SB/ctl.txt"
    assert_order "$SB/calls.log" 'pg_dump' ' build --pull'
    assert_order "$SB/calls.log" ' build --pull' ' up -d --remove-orphans'
    assert_eq "$(envv OA_VERSION)" 1.2.3 version
    FAKE_UNHEALTHY=api run_ctl --yes upgrade --no-backup; assert_rc_nz "$SB/ctl.txt"; assert_has "$SB/ctl.txt" 'Rollback'
    new_sandbox up2; run_install --yes --dir "$SB/oa"; run_install --yes --dir "$SB/oa" --upgrade; assert_rc 0
    assert_has "$SB/calls.log" 'pg_dump'
}

t_uninstall() {
    new_sandbox un; run_install --yes --dir "$SB/oa" --systemd; assert_rc 0
    echo keep >"$SB/oa/data/archive/doc.txt"
    run_install --yes --dir "$SB/oa" --uninstall; assert_rc 0
    assert_has "$SB/calls.log" 'compose.* down --remove-orphans'; assert_not "$SB/calls.log" ' down .*(-v|--volumes)'
    assert_file "$SB/oa/data/archive/doc.txt"; assert_file "$SB/oa/.env"; assert_file "$SB/oa/mounts.json"
    assert_nofile "$SB/units/oa-agent.service"
    # --yes cannot confirm a purge
    run_install --yes --dir "$SB/oa" --uninstall --purge; assert_rc_nz; assert_file "$SB/oa/.env"
    OA_PURGE_CONFIRM=oa run_install --yes --dir "$SB/oa" --uninstall --purge; assert_rc 0
    assert_nofile "$SB/oa"; assert_nofile "$SB/bin/oa-agentctl"
}

t_purge_guard() {
    new_sandbox pg; run_install --yes --dir "$SB/oa"; assert_rc 0
    # wrong name
    OA_PURGE_CONFIRM=nope run_ctl --yes uninstall --purge; assert_rc_nz "$SB/ctl.txt"; assert_file "$SB/oa/.env"
    # custom archive outside the install dir is never touched by purge
    new_sandbox pg2; run_install --yes --dir "$SB/oa" --archive-dir "$SB/outside/arch"; assert_rc 0; echo x >"$SB/outside/arch/f"
    OA_PURGE_CONFIRM=oa run_ctl --yes uninstall --purge; assert_nofile "$SB/oa"; assert_file "$SB/outside/arch/f"
}

t_no_tty() {
    new_sandbox notty; OA_TEST_MODE=1 OA_INSTALL_LOG="$SB/install.log" OA_INSTALL_BIN_DIR="$SB/bin" OA_INSTALL_UNIT_DIR="$SB/units" OA_OS_RELEASE="$SB/os-release" OA_MEMINFO="$SB/meminfo" OA_POLL_SECONDS=0 OA_RETRY_BASE=0 OA_INSTALL_LOCK="$SB/lock" OA_DAEMON_JSON="$SB/daemon.json" OA_INSTALL_TTY=/nonexistent OA_BUILD_FROM_SOURCE=1 PATH="$SB/shim:$PATH" bash "$SB/repo/deploy/linux/install.sh" --lang en --dir "$SB/oa" </dev/null >"$SB/out.txt" 2>&1
    RC=$?; assert_rc 2; assert_nofile "$SB/oa/.env"
    # the message says exactly what to do (it used to install a default plain-HTTP instance silently)
    assert_has "$SB/out.txt" 'No terminal is available'; assert_has "$SB/out.txt" '--yes'; assert_has "$SB/out.txt" 'ssh -t'; assert_not "$SB/calls.log" 'compose\['
}

# ---------------------------------------------------------------------------------------------- registry mode (deploy image bundle)
REG=reg.example.com/ns
make_bundle() { # a fake /deploy of the oa-agent-deploy image at $SB/bundle (flat layout), exported as FAKE_BUNDLE
    local b="$SB/bundle" f
    rm -rf -- "$b"; mkdir -p "$b/bin"
    for f in install.sh oa-agentctl.sh docker-compose.yml docker-compose.build.yml docker-compose.https.yml docker-compose.external-db.yml docker-compose.mounts.example.yml Caddyfile.template .env.example oa-agent.service; do cp "$LINUX/$f" "$b/$f"; done
    printf 'version=test\ncommit=abc1234\nregistry=%s\n' "$REG" >"$b/VERSION"
    printf '#!/usr/bin/env bash\necho "bundled-ctl $*" >>"$FAKE_SB/calls.log"\nif [ "$1 $2" = "mounts apply" ]; then cp "$FAKE_EXAMPLE" "$4/docker-compose.mounts.yml"; fi\n' >"$b/bin/oa-agentctl-linux-amd64"
    cp "$b/bin/oa-agentctl-linux-amd64" "$b/bin/oa-agentctl-linux-arm64"
    (cd "$b/bin" && sha256sum oa-agentctl-linux-* >SHA256SUMS)
    export FAKE_BUNDLE="$b"
}
run_install_piped() { # like "docker run --rm <deploy image> | sudo bash -s -- ...": no script file, cwd outside any checkout
    ( cd "$SB" && cat "$LINUX/install.sh" | OA_TEST_MODE=1 OA_INSTALL_LOG="$SB/install.log" OA_INSTALL_BIN_DIR="$SB/bin" OA_INSTALL_UNIT_DIR="$SB/units" OA_INSTALL_ETC_MARKER="$SB/etc/install_dir" \
        OA_OS_RELEASE="$SB/os-release" OA_MEMINFO="$SB/meminfo" OA_POLL_SECONDS=0 OA_RETRY_BASE=0 OA_INSTALL_LOCK="$SB/lock" OA_DAEMON_JSON="$SB/daemon.json" OA_TEST_DOCKER_SHIM="$SB/shim/docker" \
        OA_SUMMARY="${OA_SUMMARY:-full}" PATH="$SB/shim:$PATH" bash -s -- --lang en "$@" ) >"$SB/out.txt" 2>&1
    RC=$?
}

t_registry_piped() {
    new_sandbox regp; make_bundle; rm -rf "$SB/repo"
    run_install_piped --yes --dir "$SB/oa" --port 18083 --registry "$REG" --image-tag sha-abc1234; assert_rc 0
    assert_eq "$(envv OA_IMAGE_REGISTRY)" "$REG" registry
    assert_eq "$(envv OA_VERSION)" sha-abc1234 tag
    assert_eq "$(envv COMPOSE_FILE)" docker-compose.yml:docker-compose.mounts.yml "no build layer"
    assert_has "$SB/calls.log" "^docker pull $REG/oa-agent-deploy:sha-abc1234"
    assert_has "$SB/calls.log" "^docker create $REG/oa-agent-deploy:sha-abc1234"
    assert_has "$SB/calls.log" '^docker cp cid-bundle-[0-9]+:/deploy/\. '
    assert_has "$SB/calls.log" '^docker rm -f cid-bundle-'
    assert_has "$SB/calls.log" 'compose\[CF=[^]]*\] pull'
    assert_not "$SB/calls.log" 'compose.* build'; assert_not "$SB/calls.log" '^docker run'
    assert_order "$SB/calls.log" '^docker cp ' 'compose\[CF=[^]]*\] pull'
    assert_order "$SB/calls.log" 'compose\[CF=[^]]*\] pull' 'compose\[CF=[^]]*\] up -d'
    assert_has "$SB/calls.log" '^bundled-ctl mounts apply'
    assert_has "$SB/out.txt" 'Prebuilt oa-agentctl SHA-256 verified'
    assert_nofile "$SB/oa/src"; assert_file "$SB/oa/docker-compose.yml"; assert_file "$SB/oa/docker-compose.https.yml"; assert_file "$SB/oa/Caddyfile.template"
    assert_file "$SB/bin/oa-agentctl"; assert_has "$SB/bin/oa-agentctl.sh" "^OA_DEFAULT_DIR='$SB/oa'"
    assert_has "$SB/out.txt" "Images: +$REG/oa-agent-\{api,worker\}:sha-abc1234"
    assert_has "$SB/out.txt" 'SETUP TOKEN: +ABCDEFGH23456789TOKEN'
    # idempotent re-run keeps the secrets and the installed tag
    local before; before="$(grep -E '^OA_(SECRET_KEY|WORKER_TOKEN|POSTGRES_PASSWORD)=' "$SB/oa/.env")"
    run_install_piped --yes --dir "$SB/oa" --registry "$REG" --image-tag other; assert_rc 0
    assert_eq "$(grep -E '^OA_(SECRET_KEY|WORKER_TOKEN|POSTGRES_PASSWORD)=' "$SB/oa/.env")" "$before" secrets
    assert_eq "$(envv OA_VERSION)" sha-abc1234 "tag stays"; assert_has "$SB/out.txt" 'upgrade --tag other'
}

t_default_registry() {
    local D=crpi-d6fmgwmrnsa2kgp8.cn-shanghai.personal.cr.aliyuncs.com/koch2333
    # no flags at all, piped (no checkout): the hard-coded production registry is used
    new_sandbox defreg; make_bundle; rm -rf "$SB/repo"
    run_install_piped --yes --dir "$SB/oa"; assert_rc 0
    assert_eq "$(envv OA_IMAGE_REGISTRY)" "$D" "hard-coded registry"; assert_eq "$(envv OA_VERSION)" latest "default tag"
    assert_eq "$(envv COMPOSE_FILE)" docker-compose.yml:docker-compose.mounts.yml "no build layer"
    assert_has "$SB/calls.log" "^docker pull $D/oa-agent-deploy:latest"; assert_has "$SB/calls.log" 'compose\[CF=[^]]*\] pull'
    assert_not "$SB/calls.log" 'compose.* build'
    assert_has "$SB/out.txt" "Images: +$D/oa-agent-\{api,worker\}:latest"
    # from a checkout, plain run (no OA_BUILD_FROM_SOURCE): still pulls
    new_sandbox defchk; OA_BUILD_FROM_SOURCE=0 run_install --yes --dir "$SB/oa"; assert_rc 0
    assert_eq "$(envv OA_IMAGE_REGISTRY)" "$D" "registry from a checkout"; assert_not "$SB/calls.log" 'compose.* build'
    # opt-out: --build-from-source builds locally
    new_sandbox defsrc; OA_BUILD_FROM_SOURCE=0 run_install --yes --dir "$SB/oa" --build-from-source; assert_rc 0
    assert_has "$SB/calls.log" 'compose.* build'; assert_not "$SB/calls.log" '^docker pull'
    # an explicit --registry beats the OA_BUILD_FROM_SOURCE environment variable, but conflicts with the flag
    new_sandbox defenv; OA_BUILD_FROM_SOURCE=1 run_install --yes --dir "$SB/oa" --registry "$REG"; assert_rc 0
    assert_eq "$(envv OA_IMAGE_REGISTRY)" "$REG" "explicit registry wins over the env var"
    new_sandbox defconf; OA_BUILD_FROM_SOURCE=0 run_install --yes --dir "$SB/oa" --build-from-source --registry "$REG"; assert_rc_nz; assert_nofile "$SB/oa/.env"
    # the hard-coded endpoint is documented in --help
    new_sandbox defhelp; run_install --help; assert_has "$SB/out.txt" 'built in as the default'; assert_has "$SB/out.txt" 'build-from-source'
}

t_registry_version_synonym() {
    new_sandbox regv; make_bundle; rm -rf "$SB/repo"
    run_install_piped --yes --dir "$SB/oa" --registry "$REG/" --version 1.4.2; assert_rc 0
    assert_eq "$(envv OA_VERSION)" 1.4.2 "--version is the tag"; assert_eq "$(envv OA_IMAGE_REGISTRY)" "$REG" "trailing slash removed"
    new_sandbox regd; make_bundle; rm -rf "$SB/repo"
    OA_IMAGE_REGISTRY="$REG" run_install_piped --yes --dir "$SB/oa"; assert_rc 0
    assert_eq "$(envv OA_VERSION)" latest "default tag"; assert_has "$SB/calls.log" "^docker pull $REG/oa-agent-deploy:latest"
}

t_registry_from_checkout() {
    new_sandbox regc
    run_install --yes --dir "$SB/oa" --registry "$REG" --image-tag t1; assert_rc 0
    assert_not "$SB/calls.log" '^docker create'; assert_has "$SB/calls.log" 'compose\[CF=[^]]*\] pull'; assert_not "$SB/calls.log" 'compose.* build'
    assert_eq "$(envv COMPOSE_FILE)" docker-compose.yml:docker-compose.mounts.yml cf
    assert_file "$SB/oa/src/backend"
    new_sandbox regbad; run_install --yes --dir "$SB/oa" --registry 'https://x.example.com'; assert_rc_nz; assert_nofile "$SB/oa/.env"
    new_sandbox regbad2; run_install --yes --dir "$SB/oa" --registry 'a b'; assert_rc_nz
}

t_registry_login() {
    new_sandbox regl; make_bundle; rm -rf "$SB/repo"
    OA_REGISTRY_USER=alice OA_REGISTRY_PASSWORD='s3cr3t-Pw' run_install_piped --yes --dir "$SB/oa" --registry "$REG"; assert_rc 0
    assert_has "$SB/calls.log" "^docker login reg.example.com --username alice --password-stdin"
    assert_eq "$(cat "$SB/state/login-stdin")" 's3cr3t-Pw' "password only via stdin"
    assert_not "$SB/calls.log" 's3cr3t-Pw'; assert_not "$SB/out.txt" 's3cr3t-Pw'; assert_not "$SB/install.log" 's3cr3t-Pw'; assert_not "$SB/oa/.env" 's3cr3t-Pw'
    assert_not "$SB/oa/.env" 'OA_REGISTRY'
    assert_order "$SB/calls.log" '^docker login' '^docker pull'
    new_sandbox regl2; make_bundle; rm -rf "$SB/repo"
    FAKE_LOGIN_FAIL=1 OA_REGISTRY_USER=alice OA_REGISTRY_PASSWORD='s3cr3t-Pw' run_install_piped --yes --dir "$SB/oa" --registry "$REG"; assert_rc_nz
    assert_has "$SB/out.txt" 'Registry login failed'; assert_not "$SB/out.txt" 's3cr3t-Pw'; assert_nofile "$SB/oa/.env"
    new_sandbox regl3; make_bundle; rm -rf "$SB/repo"
    OA_REGISTRY_USER=alice run_install_piped --yes --dir "$SB/oa" --registry "$REG"; assert_rc_nz; assert_has "$SB/out.txt" 'must be set together'
}

t_registry_pull_errors() {
    new_sandbox regf; make_bundle; rm -rf "$SB/repo"
    FAKE_PULL_FAIL=1 run_install_piped --yes --dir "$SB/oa" --registry "$REG" --image-tag nope; assert_rc_nz
    assert_has "$SB/out.txt" "$REG/oa-agent-api:nope"; assert_has "$SB/out.txt" "$REG/oa-agent-worker:nope"; assert_has "$SB/out.txt" "$REG/oa-agent-deploy:nope"
    assert_has "$SB/out.txt" 'auto-create'; assert_has "$SB/out.txt" 'VPC'; assert_nofile "$SB/oa/.env"
    # bundle ok but the compose pull of the stack fails (e.g. api repository missing): same helpful text
    new_sandbox regf2; make_bundle; rm -rf "$SB/repo"
    FAKE_COMPOSE_PULL_FAIL=1 run_install_piped --yes --dir "$SB/oa" --registry "$REG" --image-tag t2; assert_rc_nz
    assert_has "$SB/out.txt" "$REG/oa-agent-api:t2"; assert_has "$SB/out.txt" 'Image pull failed'
    # tampered prebuilt binary in the bundle
    new_sandbox regs; make_bundle; rm -rf "$SB/repo"; echo tampered >>"$SB/bundle/bin/oa-agentctl-linux-amd64"; echo tampered >>"$SB/bundle/bin/oa-agentctl-linux-arm64"
    run_install_piped --yes --dir "$SB/oa" --registry "$REG"; assert_rc_nz; assert_has "$SB/out.txt" 'Checksum mismatch'
    # not a deploy image at all
    new_sandbox regn; make_bundle; rm -rf "$SB/repo" "$SB/bundle/docker-compose.yml"
    run_install_piped --yes --dir "$SB/oa" --registry "$REG"; assert_rc_nz; assert_has "$SB/out.txt" 'not a valid installer bundle'
}

registry_install() { # registry_install TAG : fresh registry install in $SB/oa from the piped bundle
    new_sandbox "$1"; make_bundle; rm -rf "$SB/repo"
    run_install_piped --yes --dir "$SB/oa" --registry "$REG" --image-tag "$2"; assert_rc 0
}

t_ctl_upgrade_registry() {
    registry_install regu sha-old0001
    local secrets mj; secrets="$(grep -E '^OA_(SECRET_KEY|WORKER_TOKEN|POSTGRES_PASSWORD)=' "$SB/oa/.env")"; mj="$(cat "$SB/oa/mounts.json")"
    local ov; ov="$(cat "$SB/oa/docker-compose.mounts.yml")"
    echo '# BUNDLE-REFRESHED' >>"$SB/bundle/docker-compose.yml"; echo '# BUNDLE-SH' >>"$SB/bundle/oa-agentctl.sh"
    : >"$SB/calls.log"; run_ctl --yes upgrade --tag sha-new0002; assert_rc 0 "$SB/ctl.txt"
    assert_order "$SB/calls.log" 'pg_dump' 'compose\[CF=[^]]*\] pull'
    assert_order "$SB/calls.log" 'compose\[CF=[^]]*\] pull' "^docker pull $REG/oa-agent-deploy:sha-new0002"
    assert_order "$SB/calls.log" "^docker pull $REG/oa-agent-deploy:sha-new0002" 'up -d --force-recreate api worker'
    assert_not "$SB/calls.log" 'compose.* build'; assert_not "$SB/calls.log" '--remove-orphans'
    assert_eq "$(envv OA_VERSION)" sha-new0002 "tag updated"
    assert_has "$SB/oa/docker-compose.yml" 'BUNDLE-REFRESHED'; assert_has "$SB/bin/oa-agentctl.sh" 'BUNDLE-SH'; assert_has "$SB/bin/oa-agentctl.sh" "^OA_DEFAULT_DIR='$SB/oa'"
    assert_eq "$(grep -E '^OA_(SECRET_KEY|WORKER_TOKEN|POSTGRES_PASSWORD)=' "$SB/oa/.env")" "$secrets" "secrets kept"
    assert_eq "$(cat "$SB/oa/mounts.json")" "$mj" "mounts.json kept"; assert_eq "$(cat "$SB/oa/docker-compose.mounts.yml")" "$ov" "mounts override kept"
    assert_eq "$(envv COMPOSE_FILE)" docker-compose.yml:docker-compose.mounts.yml "compose layers kept"
    # same pinned tag: pull + recreate, but no bundle refresh
    : >"$SB/calls.log"; run_ctl --yes upgrade --no-backup; assert_rc 0 "$SB/ctl.txt"
    assert_not "$SB/calls.log" '^docker (pull|create)'; assert_has "$SB/calls.log" 'up -d --force-recreate api worker'
    # rollback to the previous sha tag
    : >"$SB/calls.log"; run_ctl --yes upgrade --version sha-old0001 --no-backup; assert_rc 0 "$SB/ctl.txt"
    assert_eq "$(envv OA_VERSION)" sha-old0001 rollback; assert_has "$SB/calls.log" "^docker pull $REG/oa-agent-deploy:sha-old0001"
    # moving tag latest always refreshes
    : >"$SB/calls.log"; run_ctl --yes upgrade --tag latest --no-backup; assert_rc 0 "$SB/ctl.txt"; assert_has "$SB/calls.log" "^docker pull $REG/oa-agent-deploy:latest"
    # unhealthy afterwards: the hint names the rollback command
    FAKE_UNHEALTHY=api run_ctl --yes upgrade --tag sha-bad0003 --no-backup; assert_rc_nz "$SB/ctl.txt"; assert_has "$SB/ctl.txt" 'upgrade --tag latest --no-backup'
    run_ctl --yes upgrade --tag 'bad tag'; assert_rc_nz
}

t_ctl_upgrade_registry_guards() {
    # tampered bundle: aborts before anything is replaced or recreated
    registry_install regu2 sha-old0001
    echo tampered >>"$SB/bundle/bin/oa-agentctl-linux-amd64"; echo tampered >>"$SB/bundle/bin/oa-agentctl-linux-arm64"; echo '# EVIL' >>"$SB/bundle/docker-compose.yml"
    : >"$SB/calls.log"; run_ctl --yes upgrade --tag sha-new0002 --no-backup; assert_rc_nz "$SB/ctl.txt"; assert_has "$SB/ctl.txt" 'checksum mismatch'
    assert_not "$SB/oa/docker-compose.yml" 'EVIL'; assert_not "$SB/calls.log" 'up -d'
    # deploy image missing: warning only, upgrade still completes with the existing compose files
    registry_install regu3 sha-old0001
    FAKE_BUNDLE=/nonexistent run_ctl --yes upgrade --tag sha-new0002 --no-backup; assert_rc 0 "$SB/ctl.txt"; assert_has "$SB/ctl.txt" 'Could not refresh'
    assert_has "$SB/calls.log" 'up -d --force-recreate api worker'
    # a source (build-layer) install never pulls the registry or the deploy image
    new_sandbox regsrc; run_install --yes --dir "$SB/oa"; assert_rc 0
    : >"$SB/calls.log"; run_ctl --yes upgrade --tag 9.9.9 --no-backup; assert_rc 0 "$SB/ctl.txt"
    assert_has "$SB/calls.log" ' build --pull'; assert_not "$SB/calls.log" '^docker (pull|create)'; assert_not "$SB/calls.log" 'force-recreate'
    # installer --upgrade --image-tag is passed through to the control tool
    registry_install regu4 sha-old0001
    : >"$SB/calls.log"; run_install_piped --yes --dir "$SB/oa" --upgrade --image-tag sha-new0005; assert_rc 0
    assert_eq "$(envv OA_VERSION)" sha-new0005 "installer --upgrade --image-tag"
    assert_has "$SB/calls.log" 'up -d --force-recreate api worker'
}

# ---------------------------------------------------------------------------------------------- production-hardening tests
assert_count() { [ "$(grep -Ec -- "$2" "$1" 2>/dev/null || true)" = "$3" ] && ok_ || bad_ "$1: /$2/ should match $3 line(s), got $(grep -Ec -- "$2" "$1" 2>/dev/null || true)"; }
run_stdin() { # run_stdin FILE args... : FILE is fed to "bash -s" on stdin (a curl|bash run), cwd outside any checkout
    local f="$1"; shift
    ( cd "$SB" && OA_TEST_MODE=1 OA_INSTALL_LOG="$SB/install.log" OA_INSTALL_BIN_DIR="$SB/bin" OA_INSTALL_UNIT_DIR="$SB/units" OA_INSTALL_ETC_MARKER="$SB/etc/install_dir" \
        OA_OS_RELEASE="$SB/os-release" OA_MEMINFO="$SB/meminfo" OA_POLL_SECONDS=0 OA_RETRY_BASE=0 OA_INSTALL_LOCK="$SB/lock" OA_DAEMON_JSON="$SB/daemon.json" OA_TEST_DOCKER_SHIM="$SB/shim/docker" \
        OA_SUMMARY="${OA_SUMMARY:-full}" PATH="$SB/shim:$PATH" bash -s -- --lang en "$@" <"$f" ) >"$SB/out.txt" 2>&1
    RC=$?
}
make_tar() { # make_tar OUT TAG [REGISTRY] [omit api|worker|deploy]: a docker-save style tarball (manifest.json with RepoTags) + .sha256
    local out="$1" tag="$2" reg="${3:-$REG}" omit="${4:-}" d sep="" n
    d="$(mktemp -d)"
    { printf '[{"Config":"blobs/x","RepoTags":['
      for n in api worker deploy; do [ "$n" = "$omit" ] && continue; printf '%s"%s/oa-agent-%s:%s"' "$sep" "$reg" "$n" "$tag"; sep=','; done
      printf ',"postgres:16-alpine"],"Layers":[]}]'; } >"$d/manifest.json"
    (cd "$d" && tar -cf "$out" manifest.json); rm -rf "$d"
    (cd "$(dirname "$out")" && sha256sum "$(basename "$out")" >"$(basename "$out").sha256")
    return 0
}

t_truncated_script() {
    new_sandbox trunc; local full="$LINUX/install.sh" size cut n
    size="$(wc -c <"$full")"
    # cut at several byte positions, including inside the last line and right after "main"
    local last; last="$(tail -n 1 "$full")"
    for cut in 1000 $((size / 4)) $((size / 2)) $((size * 9 / 10)) $((size - ${#last} - 1)) $((size - 5)) $((size - ${#last} + 4)); do
        : >"$SB/calls.log"; rm -rf "$SB/oa" "${SB:?}/bin/"* "$SB/install.log"
        head -c "$cut" "$full" >"$SB/trunc.sh"
        run_stdin "$SB/trunc.sh" --yes --dir "$SB/oa"
        assert_nofile "$SB/oa"; assert_eq "$(ls -A "$SB/bin" | wc -l | tr -d ' ')" 0 "nothing installed (cut at $cut)"
        assert_eq "$(grep -Ec '^(docker|curl|compose|systemctl|ufw|apt-get)' "$SB/calls.log")" 0 "no command ran (cut at $cut)"
    done
    # the whole script minus the sentinel (a "main" call that survived the cut but lost its end marker) refuses to run
    : >"$SB/calls.log"; sed '$ s/ "\$EOS_SENTINEL"$//' "$full" >"$SB/nosentinel.sh"
    run_stdin "$SB/nosentinel.sh" --yes --dir "$SB/oa"
    assert_rc 1; assert_has "$SB/out.txt" 'incomplete'; assert_nofile "$SB/oa"; assert_eq "$(wc -c <"$SB/calls.log" | tr -d ' ')" 0 "no command ran without the sentinel"
    # the complete script still installs when piped (sanity for the guard)
    new_sandbox trunc2; make_bundle; rm -rf "$SB/repo"; run_stdin "$full" --yes --dir "$SB/oa" --registry "$REG"; assert_rc 0; assert_file "$SB/oa/.env"
    # the guard is not fooled by an extra sentinel-like option
    new_sandbox trunc3; run_install --yes --dir "$SB/oa" __OA_INSTALL_END_OF_SCRIPT__; assert_rc 2; assert_nofile "$SB/oa/.env"
}

t_exit_codes() {
    new_sandbox rc0; run_install --yes --dir "$SB/oa"; assert_rc 0
    run_install --bogus; assert_rc 2
    run_install --yes --dir "$SB/oa2" --port abc; assert_rc 2
    run_install --yes --dir "$SB/oa2" --mirror xx; assert_rc 2
    run_install --yes --dir /etc; assert_rc 2
    run_install --check --uninstall; assert_rc 2
    run_install --reinstall --upgrade; assert_rc 2
    run_install --docker-mirror 'ftp://x.example.com'; assert_rc 2
    run_install --offline "$SB/nonexistent.tar"; assert_rc 2
    new_sandbox rc3; FAKE_PORTS=18080 run_install --yes --dir "$SB/oa" --port 18080; assert_rc 3
    new_sandbox rc3b; FAKE_KERNEL=2.6.32 run_install --yes --dir "$SB/oa"; assert_rc 3
    NO_DOCKER=1 new_sandbox rc1b; run_install --yes --dir "$SB/oa"; assert_rc 1
    new_sandbox rc4; run_install --yes --dir "$SB/oa"; printf '5\n' >"$SB/answers"; OA_INSTALL_TTY="$SB/answers" run_install --dir "$SB/oa"; assert_rc 4
    assert_has "$SB/out.txt" 'Cancelled'
    # the documented codes are in --help and in the script header
    run_install --help; assert_has "$SB/out.txt" 'Exit codes: 0 ok'
    assert_has "$LINUX/install.sh" '^# Exit codes: 0 ok'
}

t_check_mode() {
    new_sandbox chk; snap() { (cd "$SB" && find . \( -path ./shim -o -path ./repo -o -name out.txt -o -name calls.log \) -prune -o -print | sort); }
    local before; before="$(snap)"
    run_install --check --dir "$SB/oa" --port 18080; assert_rc 0
    assert_has "$SB/out.txt" '\[PASS\]'; assert_has "$SB/out.txt" 'read-only check'; assert_has "$SB/out.txt" 'nothing was changed'; assert_has "$SB/out.txt" 'What would be done'
    assert_has "$SB/out.txt" 'Pre-flight: [0-9]+ passed'
    assert_eq "$(snap)" "$before" "check created nothing"
    assert_nofile "$SB/oa"; assert_nofile "$SB/install.log"; assert_nofile "$SB/etc/install_dir"
    assert_not "$SB/calls.log" '^docker (pull|create|cp|login|load|run|rm)'; assert_not "$SB/calls.log" 'compose\['
    assert_not "$SB/calls.log" '^ufw allow|^systemctl (enable|start|restart|daemon)|^(apt-get|dnf|yum|zypper|apk) '; assert_not "$SB/calls.log" 'get.docker.com'
    run_install --dry-run --dir "$SB/oa"; assert_rc 0; assert_has "$SB/out.txt" 'fresh installation'
    # --check never prompts, even with a terminal answering something dangerous
    printf 'y\ny\ny\ny\n' >"$SB/answers"; OA_INSTALL_TTY="$SB/answers" run_install --check --dir "$SB/oa" --systemd; assert_rc 0; assert_nofile "$SB/units/oa-agent.service"
    # a missing Docker is a WARN with the plan to install it, not an installation
    NO_DOCKER=1 new_sandbox chk2; run_install --check --dir "$SB/oa" --mirror cn; assert_rc 0
    assert_has "$SB/out.txt" 'Docker is not installed'; assert_has "$SB/out.txt" 'would be installed'; assert_not "$SB/calls.log" 'get-docker'; assert_nofile "$SB/oa"
    # FAIL lines give exit code 3, still without changes
    new_sandbox chk3; FAKE_PORTS=18080 run_install --check --dir "$SB/oa" --port 18080; assert_rc 3
    assert_has "$SB/out.txt" '\[FAIL\].*Port 18080 is already in use'; assert_has "$SB/out.txt" '1 failed'; assert_nofile "$SB/oa"
    new_sandbox chk4; FAKE_DOCKER_VERSION=19.03.1 run_install --check --dir "$SB/oa"; assert_rc 3; assert_has "$SB/out.txt" 'Docker 19.03.1 is too old'
    # an existing installation is not touched either
    new_sandbox chk5; run_install --yes --dir "$SB/oa"; assert_rc 0
    local sums; sums="$(cd "$SB" && find oa bin -type f -exec cksum {} + | sort)"; : >"$SB/calls.log"
    run_install --check --dir "$SB/oa" --upgrade; assert_rc 0; assert_has "$SB/out.txt" 'action: +upgrade'
    assert_eq "$(cd "$SB" && find oa bin -type f -exec cksum {} + | sort)" "$sums" "existing install untouched"
    assert_not "$SB/calls.log" 'compose\['
    # registry mode from a pipe: reports the plan, pulls nothing
    new_sandbox chk6; make_bundle; rm -rf "$SB/repo"; run_stdin "$LINUX/install.sh" --check --yes --dir "$SB/oa" --registry "$REG" --image-tag sha-abc1234
    assert_rc 0; assert_has "$SB/out.txt" "pull $REG/oa-agent-api\|worker:sha-abc1234"; assert_not "$SB/calls.log" '^docker (pull|create|cp|login)'; assert_has "$SB/out.txt" 'Image registry reachable'
}

t_preflight_report() {
    new_sandbox pf1; FAKE_KERNEL=3.10.0-1160.el7 run_install --yes --dir "$SB/oa"; assert_rc 0; assert_has "$SB/out.txt" 'Kernel 3.10.0-1160.el7 is old'
    new_sandbox pf2; FAKE_KERNEL=2.6.32-5 run_install --yes --dir "$SB/oa"; assert_rc 3; assert_has "$SB/out.txt" 'Kernel 2.6.32-5 is too old'; assert_nofile "$SB/oa/.env"
    assert_not "$SB/calls.log" 'compose\['; assert_has "$SB/out.txt" 'nothing was changed'
    new_sandbox pf3; FAKE_NTP=no run_install --yes --dir "$SB/oa"; assert_rc 0
    assert_has "$SB/out.txt" 'clock is NOT synchronized'; assert_has "$SB/out.txt" 'TOTP'; assert_has "$SB/out.txt" 'pre-flight warning\(s\) to review'
    new_sandbox pf4; FAKE_SELINUX=Enforcing run_install --yes --dir "$SB/oa"; assert_rc 0
    assert_has "$SB/out.txt" 'SELinux is Enforcing'; assert_has "$SB/out.txt" 'container_file_t'; assert_has "$SB/out.txt" ':z label'; assert_has "$SB/out.txt" 'never changes SELinux'
    assert_not "$SB/calls.log" 'chcon|setenforce|semanage'
    new_sandbox pf5; FAKE_CGROUP=tmpfs run_install --yes --dir "$SB/oa"; assert_rc 0; assert_has "$SB/out.txt" 'cgroup v1 detected'
    new_sandbox pf6; run_install --yes --dir "$SB/oa"; assert_rc 0; assert_has "$SB/out.txt" 'cgroup v2'; assert_has "$SB/out.txt" 'Docker Engine 26.1.0'; assert_has "$SB/out.txt" 'Compose v2 plugin 2.29.0'
    assert_has "$SB/out.txt" 'Pre-flight: [0-9]+ passed, 0 warning'; assert_not "$SB/out.txt" 'warning\(s\) to review'
    new_sandbox pf7; FAKE_DOCKER_VERSION=19.03.1 run_install --yes --dir "$SB/oa"; assert_rc 3; assert_has "$SB/out.txt" 'Docker 19.03.1 is too old'; assert_nofile "$SB/oa/.env"
    new_sandbox pf8; FAKE_IPS4='' FAKE_IPS6='eth0=2001:db8::5' run_install --yes --dir "$SB/oa"; assert_rc 0; assert_has "$SB/out.txt" 'Only IPv6 addresses'; assert_eq "$(envv OA_PUBLIC_URL)" 'http://[2001:db8::5]:8080' "ipv6 url"
    new_sandbox pf9; FAKE_IPS4='' run_install --yes --dir "$SB/oa"; assert_rc 0; assert_has "$SB/out.txt" 'No non-loopback address'
    new_sandbox pf10; FAKE_DOCKER_SECOPT='[name=seccomp name=rootless]' run_install --yes --dir "$SB/oa"; assert_rc 0; assert_has "$SB/out.txt" 'Rootless Docker'
    new_sandbox pf11; mkdir -p "$SB/droot"; FAKE_DOCKER_ROOT="$SB/droot" run_install --yes --dir "$SB/oa"; assert_rc 0; assert_has "$SB/out.txt" 'Docker data directory .* has enough space'
    # the docker bridge never shows up as an access URL; the other NICs do
    new_sandbox pf12; FAKE_IPS4='eth0=192.0.2.10 eth1=198.51.100.7 docker0=172.17.0.1 br-1a2b=172.18.0.1' run_install --yes --dir "$SB/oa" --port 18080; assert_rc 0
    assert_has "$SB/out.txt" 'URL: +http://192.0.2.10:18080'; assert_has "$SB/out.txt" 'Other URL: +http://198.51.100.7:18080'; assert_not "$SB/out.txt" '172\.1[78]\.0\.1'
    # the summary box: save-these guidance (the key itself is never printed), management commands, firewall reminder
    assert_has "$SB/out.txt" 'SAVE THESE'; assert_has "$SB/out.txt" "grep '\^OA_SECRET_KEY=' $SB/oa/.env"; assert_not "$SB/out.txt" "$(envv OA_SECRET_KEY)"
    assert_has "$SB/out.txt" 'Upgrade/redo:'; assert_has "$SB/out.txt" 'security group'
}

t_public_ip() {
    new_sandbox pip1; FAKE_PUBLIC_IP=203.0.113.9 run_install --yes --dir "$SB/oa" --port 18080; assert_rc 0
    assert_not "$SB/calls.log" 'ipify'; assert_not "$SB/out.txt" 'External URL'; assert_not "$SB/oa/.env" '203.0.113.9'
    new_sandbox pip2; FAKE_PUBLIC_IP=203.0.113.9 run_install --yes --dir "$SB/oa" --port 18080 --detect-public-ip; assert_rc 0
    assert_has "$SB/calls.log" 'ipify'; assert_has "$SB/out.txt" 'External URL: +http://203.0.113.9:18080'; assert_has "$SB/out.txt" 'api.ipify.org'
    new_sandbox pip3; FAKE_ECS_EIP=198.51.100.77 FAKE_PUBLIC_IP=203.0.113.9 run_install --yes --dir "$SB/oa" --port 18080 --detect-public-ip; assert_rc 0
    assert_has "$SB/out.txt" 'External URL: +http://198.51.100.77:18080'; assert_has "$SB/out.txt" 'Aliyun ECS metadata'; assert_not "$SB/calls.log" 'ipify'
    new_sandbox pip4; FAKE_PUBLIC_IP='<script>' run_install --yes --dir "$SB/oa" --detect-public-ip; assert_rc 0; assert_has "$SB/out.txt" 'could not be detected'
    new_sandbox pip5; run_install --yes --dir "$SB/oa" --detect-public-ip; assert_rc 0; assert_has "$SB/out.txt" 'could not be detected'
}

t_aliyun_hint() {
    local D=crpi-d6fmgwmrnsa2kgp8.cn-shanghai.personal.cr.aliyuncs.com/koch2333 V=crpi-d6fmgwmrnsa2kgp8-vpc.cn-shanghai.personal.cr.aliyuncs.com/koch2333
    new_sandbox ali1; make_bundle; rm -rf "$SB/repo"; FAKE_ECS_REGION=cn-shanghai run_stdin "$LINUX/install.sh" --yes --dir "$SB/oa"; assert_rc 0
    assert_has "$SB/out.txt" 'Alibaba Cloud ECS \(cn-shanghai\)'; assert_has "$SB/out.txt" "--registry $V"
    assert_eq "$(envv OA_IMAGE_REGISTRY)" "$D" "the hint never switches the registry on its own"
    new_sandbox ali2; make_bundle; rm -rf "$SB/repo"; FAKE_ECS_REGION=cn-beijing run_stdin "$LINUX/install.sh" --yes --dir "$SB/oa"; assert_rc 0; assert_not "$SB/out.txt" 'Alibaba Cloud ECS'
    new_sandbox ali3; make_bundle; rm -rf "$SB/repo"; run_stdin "$LINUX/install.sh" --yes --dir "$SB/oa"; assert_rc 0; assert_not "$SB/out.txt" 'Alibaba Cloud ECS'
    assert_has "$SB/calls.log" '100.100.100.200'
    new_sandbox ali4; make_bundle; rm -rf "$SB/repo"; FAKE_ECS_REGION=cn-shanghai run_stdin "$LINUX/install.sh" --yes --dir "$SB/oa" --registry "$V"; assert_rc 0; assert_not "$SB/out.txt" 'Alibaba Cloud ECS'
    # a non-Aliyun registry never triggers the metadata probe
    new_sandbox ali5; make_bundle; rm -rf "$SB/repo"; FAKE_ECS_REGION=cn-shanghai run_stdin "$LINUX/install.sh" --yes --dir "$SB/oa" --registry "$REG"; assert_rc 0; assert_not "$SB/calls.log" '100.100.100.200'
    # an unreachable registry is a warning with advice (the daemon may have its own proxy), not a stop
    new_sandbox ali6; make_bundle; rm -rf "$SB/repo"; FAKE_NO_REGISTRY=1 run_stdin "$LINUX/install.sh" --yes --dir "$SB/oa" --registry "$REG"; assert_rc 0
    assert_has "$SB/out.txt" 'Cannot reach the image registry reg.example.com'; assert_has "$SB/out.txt" 'offline'
}

t_pull_retry() {
    # transient failures are retried with back-off; finished layers resume inside docker
    new_sandbox rt1; make_bundle; rm -rf "$SB/repo"; FAKE_PULL_FLAKY=2 run_stdin "$LINUX/install.sh" --yes --dir "$SB/oa" --registry "$REG" --image-tag t1; assert_rc 0
    assert_count "$SB/calls.log" "^docker pull $REG/oa-agent-deploy:t1" 3; assert_count "$SB/calls.log" 'compose\[CF=[^]]*\] pull' 3
    assert_has "$SB/out.txt" 'attempt 1/3'; assert_has "$SB/out.txt" 'retrying in'
    # persistent failure: three attempts, then a clear stop naming the image
    new_sandbox rt2; make_bundle; rm -rf "$SB/repo"; FAKE_PULL_FLAKY=9 run_stdin "$LINUX/install.sh" --yes --dir "$SB/oa" --registry "$REG" --image-tag t2; assert_rc 1
    assert_count "$SB/calls.log" "^docker pull $REG/oa-agent-deploy:t2" 3; assert_has "$SB/out.txt" "Pulling $REG/oa-agent-deploy:t2 failed"; assert_nofile "$SB/oa/.env"
    # permanent errors (unknown tag) are not retried
    new_sandbox rt3; make_bundle; rm -rf "$SB/repo"; FAKE_PULL_FAIL=1 run_stdin "$LINUX/install.sh" --yes --dir "$SB/oa" --registry "$REG" --image-tag nope; assert_rc 1
    assert_count "$SB/calls.log" "^docker pull $REG/oa-agent-deploy:nope" 1
    # the exact failing image is named after a failed stack pull
    new_sandbox rt4; make_bundle; rm -rf "$SB/repo"; FAKE_COMPOSE_PULL_FAIL=1 FAKE_FAIL_IMAGE=oa-agent-worker run_stdin "$LINUX/install.sh" --yes --dir "$SB/oa" --registry "$REG" --image-tag t4; assert_rc 1
    assert_has "$SB/out.txt" "failing image\(s\): $REG/oa-agent-worker:t4"; assert_not "$SB/out.txt" "failing image\(s\):.*oa-agent-api"
    # a flaky build is retried too (first build attempt fails, the second works)
    new_sandbox rt5; FAKE_BUILD_FLAKY=1 run_install --yes --dir "$SB/oa"; assert_rc 0; assert_count "$SB/calls.log" 'compose\[CF=[^]]*\] build' 2; assert_has "$SB/out.txt" "docker compose build failed \(attempt 1/3\)"
}

t_docker_install_chain() {
    # official script, Aliyun mirror first; a failing mirror falls through to AzureChinaCloud
    NO_DOCKER=1 new_sandbox dc1; FAKE_GETDOCKER_BADMIRROR=1 run_install --yes --dir "$SB/oa" --install-docker --mirror cn; assert_rc 0
    assert_order "$SB/calls.log" 'get-docker.sh --mirror Aliyun' 'get-docker.sh --mirror AzureChinaCloud'; assert_has "$SB/out.txt" 'The installation source aliyun failed'
    assert_not "$SB/calls.log" '^apt-get install'
    # without --mirror cn: one plain attempt
    NO_DOCKER=1 new_sandbox dc2; run_install --yes --dir "$SB/oa" --install-docker; assert_rc 0; assert_has "$SB/calls.log" 'get-docker.sh  ?DOWNLOAD_URL=$'; assert_not "$SB/calls.log" '--mirror'
    # download of the script keeps failing: retries, then distribution packages (apt)
    NO_DOCKER=1 new_sandbox dc3; FAKE_GETDOCKER_FAIL=1 run_install --yes --dir "$SB/oa" --install-docker; assert_rc 0
    assert_count "$SB/calls.log" '^curl .*https://get.docker.com' 3; assert_has "$SB/out.txt" 'falling back to distribution packages'
    assert_has "$SB/calls.log" '^apt-get install -y docker.io docker-compose-v2'; assert_order "$SB/calls.log" 'get.docker.com' '^apt-get install'
    # other distribution families use their own package manager
    NO_DOCKER=1 new_sandbox dc4; printf 'ID=rocky\nID_LIKE="rhel centos fedora"\n' >"$SB/os-release"; FAKE_GETDOCKER_FAIL=1 run_install --yes --dir "$SB/oa" --install-docker; assert_rc 0; assert_has "$SB/calls.log" '^dnf install -y docker'
    NO_DOCKER=1 new_sandbox dc5; printf 'ID=fedora\n' >"$SB/os-release"; FAKE_GETDOCKER_FAIL=1 run_install --yes --dir "$SB/oa" --install-docker; assert_rc 0; assert_has "$SB/calls.log" '^dnf install -y moby-engine docker-compose'
    NO_DOCKER=1 new_sandbox dc6; printf 'ID=opensuse-leap\nID_LIKE="suse opensuse"\n' >"$SB/os-release"; run_install --yes --dir "$SB/oa" --install-docker; assert_rc 0
    assert_has "$SB/calls.log" '^zypper --non-interactive install docker'; assert_not "$SB/calls.log" 'get.docker.com'
    NO_DOCKER=1 new_sandbox dc7; printf 'ID=alpine\n' >"$SB/os-release"; run_install --yes --dir "$SB/oa" --install-docker; assert_rc 0; assert_has "$SB/calls.log" '^apk add --no-cache docker docker-cli-compose'
    # everything fails: a clear error, nothing installed
    NO_DOCKER=1 new_sandbox dc8; FAKE_GETDOCKER_FAIL=1 FAKE_PKG_FAIL=1 run_install --yes --dir "$SB/oa" --install-docker; assert_rc 1
    assert_has "$SB/out.txt" 'both the official script and the distribution packages'; assert_nofile "$SB/oa/.env"
    # no consent, no installation (also with the new chain)
    NO_DOCKER=1 new_sandbox dc9; run_install --yes --dir "$SB/oa"; assert_rc 1; assert_not "$SB/calls.log" 'get-docker|apt-get'
}

t_docker_mirror() {
    local M=https://mirror.example.com
    # --yes (or no terminal) never edits daemon.json
    new_sandbox dm1; run_install --yes --dir "$SB/oa" --docker-mirror "$M"; assert_rc 0
    assert_nofile "$SB/daemon.json"; assert_has "$SB/out.txt" 'never edits daemon.json'; assert_has "$SB/out.txt" 'registry-mirrors'
    new_sandbox dm1b; OA_DOCKER_MIRRORS="$M" run_install --yes --dir "$SB/oa"; assert_rc 0; assert_nofile "$SB/daemon.json"; assert_has "$SB/out.txt" 'never edits daemon.json'
    new_sandbox dm1c; run_install --yes --dir "$SB/oa" --mirror cn; assert_rc 0; assert_has "$SB/out.txt" 'Tip: Docker Hub base images'; assert_nofile "$SB/daemon.json"
    run_install --yes --dir "$SB/oa" --docker-mirror 'https://bad host'; assert_rc 2
    # interactive, no daemon.json yet: consent -> created (0644), restart declined -> docker is not restarted
    new_sandbox dm2; printf '%s\n' "$SB/oa" 18084 '' 1 "$SB/arch" n '' default y y n >"$SB/answers"
    OA_INSTALL_TTY="$SB/answers" run_install --dir "$SB/oa" --docker-mirror "$M"; assert_rc 0
    assert_file "$SB/daemon.json"; assert_has "$SB/daemon.json" "\"registry-mirrors\""; assert_has "$SB/daemon.json" "\"$M\""; assert_eq "$(mode "$SB/daemon.json")" 644 "daemon.json mode"
    assert_not "$SB/calls.log" 'systemctl restart docker'; assert_has "$SB/out.txt" 'Restart later yourself'
    # consent to the restart
    new_sandbox dm3; printf '%s\n' "$SB/oa" 18084 '' 1 "$SB/arch" n '' default y y y >"$SB/answers"
    OA_INSTALL_TTY="$SB/answers" run_install --dir "$SB/oa" --docker-mirror "$M"; assert_rc 0; assert_has "$SB/calls.log" '^systemctl restart docker'; assert_has "$SB/out.txt" 'Docker restarted'
    # consent refused: untouched
    new_sandbox dm4; printf '%s\n' "$SB/oa" 18084 '' 1 "$SB/arch" n '' default y n >"$SB/answers"; printf '{"log-driver": "json-file"}\n' >"$SB/daemon.json"; cp "$SB/daemon.json" "$SB/daemon.orig"
    OA_INSTALL_TTY="$SB/answers" run_install --dir "$SB/oa" --docker-mirror "$M"; assert_rc 0; cmp -s "$SB/daemon.json" "$SB/daemon.orig" && ok_ || bad_ "daemon.json changed without consent"
    assert_has "$SB/out.txt" 'Skipped the registry mirror'
    # an empty "{}" file is replaced, with a backup
    new_sandbox dm5; printf '%s\n' "$SB/oa" 18084 '' 1 "$SB/arch" n '' default y y n >"$SB/answers"; printf '{}\n' >"$SB/daemon.json"
    OA_INSTALL_TTY="$SB/answers" run_install --dir "$SB/oa" --docker-mirror "$M"; assert_rc 0; assert_has "$SB/daemon.json" "$M"
    [ -n "$(ls "$SB"/daemon.json.oa-bak.* 2>/dev/null)" ] && ok_ || bad_ "no backup of the old daemon.json"
    # without any JSON tool a non-trivial file is NEVER patched blindly
    new_sandbox dm6; printf '%s\n' "$SB/oa" 18084 '' 1 "$SB/arch" n '' default y y n >"$SB/answers"; printf '{\n  "log-driver": "json-file",\n  "registry-mirrors": ["https://old.example.com"]\n}\n' >"$SB/daemon.json"; cp "$SB/daemon.json" "$SB/daemon.orig"
    OA_TEST_NO_JSON=1 OA_INSTALL_TTY="$SB/answers" run_install --dir "$SB/oa" --docker-mirror "$M"; assert_rc 0
    cmp -s "$SB/daemon.json" "$SB/daemon.orig" && ok_ || bad_ "daemon.json patched without a JSON tool"; assert_has "$SB/out.txt" 'No python3 / jq'
    # with a real JSON tool: merge (other keys and the old mirror survive, the new one is added, a backup exists); invalid JSON is untouched
    if command -v python3 >/dev/null 2>&1 || command -v jq >/dev/null 2>&1 || perl -MJSON::PP -e1 >/dev/null 2>&1; then
        new_sandbox dm7; printf '%s\n' "$SB/oa" 18084 '' 1 "$SB/arch" n '' default y y n >"$SB/answers"; cp "$SB/daemon.orig" "$SB/daemon.json" 2>/dev/null || printf '{"log-driver": "json-file", "registry-mirrors": ["https://old.example.com"]}\n' >"$SB/daemon.json"
        OA_INSTALL_TTY="$SB/answers" run_install --dir "$SB/oa" --docker-mirror "$M"; assert_rc 0
        assert_has "$SB/daemon.json" 'log-driver'; assert_has "$SB/daemon.json" 'https://old.example.com'; assert_has "$SB/daemon.json" "$M"
        [ -n "$(ls "$SB"/daemon.json.oa-bak.* 2>/dev/null)" ] && ok_ || bad_ "no backup"
        new_sandbox dm8; printf '%s\n' "$SB/oa" 18084 '' 1 "$SB/arch" n '' default y y n >"$SB/answers"; printf '{ "log-driver": \n' >"$SB/daemon.json"; cp "$SB/daemon.json" "$SB/daemon.orig"
        OA_INSTALL_TTY="$SB/answers" run_install --dir "$SB/oa" --docker-mirror "$M"; assert_rc 0
        cmp -s "$SB/daemon.json" "$SB/daemon.orig" && ok_ || bad_ "invalid daemon.json was modified"; assert_has "$SB/out.txt" 'not a valid JSON object|No python3 / jq'
    else echo "    (no python3/jq/perl JSON::PP on this host: merge/invalid-JSON cases skipped)"; fi
    # already configured: nothing to do
    new_sandbox dm9; printf '{"registry-mirrors": ["%s"]}\n' "$M" >"$SB/daemon.json"; run_install --yes --dir "$SB/oa" --docker-mirror "$M"; assert_rc 0; assert_has "$SB/out.txt" 'already lists'
}

t_existing_menu() {
    new_sandbox mn; run_install --yes --dir "$SB/oa"; assert_rc 0
    local secrets; secrets="$(grep -E '^OA_(SECRET_KEY|WORKER_TOKEN|POSTGRES_PASSWORD)=' "$SB/oa/.env")"
    # 1 = repair/continue (keeps everything), an invalid answer is asked again
    : >"$SB/calls.log"; printf 'x\n1\n' >"$SB/answers"; OA_INSTALL_TTY="$SB/answers" run_install --dir "$SB/oa"; assert_rc 0
    assert_has "$SB/out.txt" 'repair/continue'; assert_has "$SB/out.txt" 'reinstall'; assert_has "$SB/out.txt" 'enter 1-6'; assert_has "$SB/out.txt" 'Existing installation detected'
    assert_eq "$(grep -E '^OA_(SECRET_KEY|WORKER_TOKEN|POSTGRES_PASSWORD)=' "$SB/oa/.env")" "$secrets" "secrets unchanged"; assert_not "$SB/calls.log" ' down '
    # 2 = upgrade (backup first)
    : >"$SB/calls.log"; printf '2\n' >"$SB/answers"; OA_INSTALL_TTY="$SB/answers" run_install --dir "$SB/oa"; assert_rc 0; assert_has "$SB/calls.log" 'pg_dump'; assert_has "$SB/out.txt" 'Upgrading'
    # 3 = reinstall: containers removed and recreated, data and .env kept
    : >"$SB/calls.log"; echo keep >"$SB/oa/data/archive/doc.txt"; printf '3\n' >"$SB/answers"; OA_INSTALL_TTY="$SB/answers" run_install --dir "$SB/oa"; assert_rc 0
    assert_order "$SB/calls.log" 'compose.* down --remove-orphans' 'compose.* build'; assert_order "$SB/calls.log" 'compose.* build' 'compose.* up -d'
    assert_not "$SB/calls.log" ' down .*(-v|--volumes)'; assert_file "$SB/oa/data/archive/doc.txt"
    assert_eq "$(grep -E '^OA_(SECRET_KEY|WORKER_TOKEN|POSTGRES_PASSWORD)=' "$SB/oa/.env")" "$secrets" "secrets unchanged by reinstall"
    # 4 = uninstall (one confirmation: the menu choice), data kept
    : >"$SB/calls.log"; printf '4\n' >"$SB/answers"; OA_INSTALL_TTY="$SB/answers" run_install --dir "$SB/oa"; assert_rc 0
    assert_has "$SB/calls.log" 'compose.* down --remove-orphans'; assert_file "$SB/oa/.env"; assert_file "$SB/oa/data/archive/doc.txt"
    # 5 = cancel: exit 4, nothing happens
    new_sandbox mn2; run_install --yes --dir "$SB/oa"; : >"$SB/calls.log"; printf '5\n' >"$SB/answers"; OA_INSTALL_TTY="$SB/answers" run_install --dir "$SB/oa"; assert_rc 4
    assert_not "$SB/calls.log" 'compose\['
    # the menu is only for terminals: explicit flags and --yes skip it
    new_sandbox mn3; run_install --yes --dir "$SB/oa"; printf '5\n' >"$SB/answers"; OA_INSTALL_TTY="$SB/answers" run_install --yes --dir "$SB/oa"; assert_rc 0; assert_not "$SB/out.txt" 'repair/continue'
    # --reinstall flag (non-interactive)
    : >"$SB/calls.log"; run_install --yes --dir "$SB/oa" --reinstall; assert_rc 0; assert_has "$SB/calls.log" 'compose.* down --remove-orphans'; assert_has "$SB/calls.log" 'compose.* up -d'
    new_sandbox mn4; run_install --yes --dir "$SB/oa" --reinstall; assert_rc_nz; assert_has "$SB/out.txt" 'No installation to reinstall'
    # interactive uninstall asks once (and --purge needs the directory name typed)
    new_sandbox mn5; run_install --yes --dir "$SB/oa"; : >"$SB/calls.log"; printf 'n\n' >"$SB/answers"; OA_INSTALL_TTY="$SB/answers" run_install --dir "$SB/oa" --uninstall; assert_rc 4; assert_not "$SB/calls.log" ' down '
    printf 'y\n' >"$SB/answers"; OA_INSTALL_TTY="$SB/answers" run_install --dir "$SB/oa" --uninstall; assert_rc 0; assert_has "$SB/calls.log" ' down '; assert_file "$SB/oa/.env"
    printf 'y\nwrongname\n' >"$SB/answers"; OA_INSTALL_TTY="$SB/answers" run_install --dir "$SB/oa" --uninstall --purge; assert_rc 4; assert_file "$SB/oa/.env"
    printf 'y\noa\n' >"$SB/answers"; OA_INSTALL_TTY="$SB/answers" run_install --dir "$SB/oa" --uninstall --purge; assert_rc 0; assert_nofile "$SB/oa"
}

t_offline() {
    :
    # fresh offline install through the pipe: registry and tag come from the tarball, nothing is pulled, nothing logs in
    new_sandbox of1; make_bundle; rm -rf "$SB/repo"; make_tar "$SB/oa-images.tar" sha-off0001
    run_stdin "$LINUX/install.sh" --yes --dir "$SB/oa" --offline "$SB/oa-images.tar"; assert_rc 0
    assert_eq "$(envv OA_IMAGE_REGISTRY)" "$REG" "registry from the tarball"; assert_eq "$(envv OA_VERSION)" sha-off0001 "tag from the tarball"
    assert_has "$SB/calls.log" "^docker load -i $SB/oa-images.tar"; assert_has "$SB/calls.log" "^docker create $REG/oa-agent-deploy:sha-off0001"
    assert_not "$SB/calls.log" '^docker pull'; assert_not "$SB/calls.log" 'compose.* pull'; assert_not "$SB/calls.log" '^docker login'; assert_not "$SB/calls.log" '/v2/'
    assert_order "$SB/calls.log" '^docker load' '^docker create'; assert_order "$SB/calls.log" '^docker create' 'compose\[CF=[^]]*\] up -d'
    assert_has "$SB/out.txt" 'Image bundle SHA-256 verified'; assert_has "$SB/out.txt" 'Offline mode'; assert_has "$SB/out.txt" "Images: +$REG/oa-agent-\{api,worker\}:sha-off0001"
    assert_eq "$(envv COMPOSE_FILE)" docker-compose.yml:docker-compose.mounts.yml "no build layer"
    # works from a checkout too (OA_BUILD_FROM_SOURCE=0)
    new_sandbox of1b; make_bundle; make_tar "$SB/oa-images.tar" t9; OA_BUILD_FROM_SOURCE=0 run_install --yes --dir "$SB/oa" --offline "$SB/oa-images.tar"; assert_rc 0; assert_eq "$(envv OA_VERSION)" t9 tag
    assert_not "$SB/calls.log" 'compose.* build'
    # integrity: wrong checksum stops before anything is loaded
    new_sandbox of2; make_bundle; rm -rf "$SB/repo"; make_tar "$SB/oa-images.tar" t1; echo tamper >>"$SB/oa-images.tar"
    run_stdin "$LINUX/install.sh" --yes --dir "$SB/oa" --offline "$SB/oa-images.tar"; assert_rc 1; assert_has "$SB/out.txt" 'SHA-256 mismatch'; assert_not "$SB/calls.log" '^docker load'; assert_nofile "$SB/oa/.env"
    # missing checksum: refused with --yes, allowed with --skip-verify (with a warning)
    new_sandbox of3; make_bundle; rm -rf "$SB/repo"; make_tar "$SB/oa-images.tar" t1; rm "$SB/oa-images.tar.sha256"
    run_stdin "$LINUX/install.sh" --yes --dir "$SB/oa" --offline "$SB/oa-images.tar"; assert_rc 1; assert_has "$SB/out.txt" 'sha256 found'; assert_not "$SB/calls.log" '^docker load'
    run_stdin "$LINUX/install.sh" --yes --dir "$SB/oa" --offline "$SB/oa-images.tar" --skip-verify; assert_rc 0; assert_has "$SB/out.txt" 'continuing because of --skip-verify'
    new_sandbox of3b; make_bundle; rm -rf "$SB/repo"; make_tar "$SB/oa-images.tar" t1; rm "$SB/oa-images.tar.sha256"
    OA_OFFLINE_SHA256="$(sha256sum "$SB/oa-images.tar" | awk '{print $1}')" run_stdin "$LINUX/install.sh" --yes --dir "$SB/oa" --offline "$SB/oa-images.tar"; assert_rc 0
    # content checks
    new_sandbox of4; make_bundle; rm -rf "$SB/repo"; make_tar "$SB/oa-images.tar" t1 "$REG" deploy; run_stdin "$LINUX/install.sh" --yes --dir "$SB/oa" --offline "$SB/oa-images.tar"; assert_rc 1; assert_has "$SB/out.txt" 'not a valid image bundle'
    new_sandbox of4b; make_bundle; rm -rf "$SB/repo"; make_tar "$SB/oa-images.tar" t1 "$REG" worker; run_stdin "$LINUX/install.sh" --yes --dir "$SB/oa" --offline "$SB/oa-images.tar"; assert_rc 1; assert_has "$SB/out.txt" "lacks $REG/oa-agent-worker:t1"
    new_sandbox of4c; make_bundle; rm -rf "$SB/repo"; echo junk >"$SB/oa-images.tar"; run_stdin "$LINUX/install.sh" --yes --dir "$SB/oa" --offline "$SB/oa-images.tar" --skip-verify; assert_rc 1; assert_has "$SB/out.txt" 'not a valid image bundle'
    # option conflicts
    new_sandbox of5; make_bundle; make_tar "$SB/oa-images.tar" t1
    run_install --yes --dir "$SB/oa" --offline "$SB/oa-images.tar" --build-from-source; assert_rc 2
    OA_BUILD_FROM_SOURCE=0 run_install --yes --dir "$SB/oa" --offline "$SB/oa-images.tar" --registry other.example.com/x; assert_rc 2; assert_has "$SB/out.txt" 'differs from the registry name'
    OA_BUILD_FROM_SOURCE=0 run_install --yes --dir "$SB/oa" --offline "$SB/oa-images.tar" --image-tag zzz; assert_rc 2; assert_has "$SB/out.txt" 'differs from the tag'
    OA_BUILD_FROM_SOURCE=0 run_install --yes --dir "$SB/oa" --offline "$SB/oa-images.tar" --registry "$REG" --image-tag t1; assert_rc 0
    # after loading: missing image, wrong architecture, load failure, missing PostgreSQL base image
    new_sandbox of6; make_bundle; make_tar "$SB/oa-images.tar" t1; FAKE_MISSING_IMAGE=oa-agent-worker OA_BUILD_FROM_SOURCE=0 run_install --yes --dir "$SB/oa" --offline "$SB/oa-images.tar"; assert_rc 1; assert_has "$SB/out.txt" 'oa-agent-worker:t1 not found after loading'
    new_sandbox of7; make_bundle; make_tar "$SB/oa-images.tar" t1; FAKE_IMG_ARCH=s390x OA_BUILD_FROM_SOURCE=0 run_install --yes --dir "$SB/oa" --offline "$SB/oa-images.tar"; assert_rc 1; assert_has "$SB/out.txt" 'is s390x'
    new_sandbox of8; make_bundle; make_tar "$SB/oa-images.tar" t1; FAKE_LOAD_FAIL=1 OA_BUILD_FROM_SOURCE=0 run_install --yes --dir "$SB/oa" --offline "$SB/oa-images.tar"; assert_rc 1; assert_has "$SB/out.txt" 'docker load failed'
    new_sandbox of9; make_bundle; make_tar "$SB/oa-images.tar" t1; FAKE_MISSING_IMAGE=postgres OA_BUILD_FROM_SOURCE=0 run_install --yes --dir "$SB/oa" --offline "$SB/oa-images.tar"; assert_rc 0; assert_has "$SB/out.txt" 'no PostgreSQL base image'
    # --check verifies the checksum and the contents without loading anything
    new_sandbox of10; make_bundle; make_tar "$SB/oa-images.tar" t1; OA_BUILD_FROM_SOURCE=0 run_install --check --dir "$SB/oa" --offline "$SB/oa-images.tar"; assert_rc 0
    assert_has "$SB/out.txt" "Image bundle .*registry $REG, tag t1"; assert_has "$SB/out.txt" 'Image bundle SHA-256 verified'; assert_not "$SB/calls.log" '^docker load'; assert_nofile "$SB/oa"
    # existing install: another tag needs --upgrade; with it, the loaded images are used without any pull
    new_sandbox of11; make_bundle; rm -rf "$SB/repo"; make_tar "$SB/old.tar" sha-old0001; run_stdin "$LINUX/install.sh" --yes --dir "$SB/oa" --offline "$SB/old.tar"; assert_rc 0
    make_tar "$SB/new.tar" sha-new0002; : >"$SB/calls.log"
    run_stdin "$LINUX/install.sh" --yes --dir "$SB/oa" --offline "$SB/new.tar"; assert_rc 2; assert_has "$SB/out.txt" 'add --upgrade'; assert_eq "$(envv OA_VERSION)" sha-old0001 "tag unchanged"
    run_stdin "$LINUX/install.sh" --yes --dir "$SB/oa" --offline "$SB/new.tar" --upgrade; assert_rc 0
    assert_eq "$(envv OA_VERSION)" sha-new0002 "upgraded to the tarball's tag"; assert_not "$SB/calls.log" '^docker pull'; assert_not "$SB/calls.log" 'compose.* pull'; assert_has "$SB/calls.log" 'up -d --force-recreate api worker'
    assert_has "$SB/out.txt" 'Offline mode: using the already loaded images'
    make_tar "$SB/other.tar" t1 other.example.com/ns; run_stdin "$LINUX/install.sh" --yes --dir "$SB/oa" --offline "$SB/other.tar" --upgrade; assert_rc 2; assert_has "$SB/out.txt" 'must match'
    # the control tool itself honours OA_OFFLINE
    : >"$SB/calls.log"; OA_OFFLINE=1 run_ctl --yes upgrade --tag sha-new0002 --no-backup; assert_rc 0 "$SB/ctl.txt"; assert_not "$SB/calls.log" '^docker pull'; assert_not "$SB/calls.log" 'compose.* pull'
}

t_save_images() {
    local S="$ROOT/scripts/save-images.sh" out
    new_sandbox sv; out="$SB/out"
    run_save() { DOCKER="$SB/shim/docker" PATH="$SB/shim:$PATH" bash "$S" "$@" >"$SB/save.txt" 2>&1; RC=$?; }
    run_save --registry "$REG" --tag sha-sv00001 --out "$out"; assert_rc 0 "$SB/save.txt"
    local tar arch; arch="$(case "$(uname -m)" in aarch64 | arm64) echo arm64 ;; *) echo amd64 ;; esac)"; tar="$out/oa-agent-images-sha-sv00001-$arch.tar"
    assert_file "$tar"; assert_file "$tar.sha256"; assert_file "$out/install.sh"; (cd "$out" && sha256sum -c "$(basename "$tar").sha256" >/dev/null 2>&1) && ok_ || bad_ "sha256 does not verify"
    tar -xOf "$tar" manifest.json >"$SB/manifest.json"
    local n; for n in api worker deploy; do assert_has "$SB/manifest.json" "\"$REG/oa-agent-$n:sha-sv00001\""; done; assert_has "$SB/manifest.json" '"postgres:16-alpine"'
    assert_not "$SB/calls.log" '^docker pull'; assert_has "$SB/save.txt" 'using the local image'; assert_has "$SB/save.txt" 'install.sh --offline'
    assert_eq "$(find "$out" -maxdepth 1 -name ".*" ! -name . ! -name .. | wc -l | tr -d " ")" 0 "no temp files left"
    # the tarball made by the script installs through the offline path (round trip)
    new_sandbox sv2; make_bundle; rm -rf "$SB/repo"; run_save --registry "$REG" --tag sha-sv00002 --out "$SB/out" --no-postgres; assert_rc 0 "$SB/save.txt"
    run_stdin "$LINUX/install.sh" --yes --dir "$SB/oa" --offline "$(ls "$SB"/out/oa-agent-images-*.tar)"; assert_rc 0; assert_eq "$(envv OA_VERSION)" sha-sv00002 "round trip tag"
    tar -xOf "$(ls "$SB"/out/oa-agent-images-*.tar)" manifest.json | grep -q postgres && bad_ "--no-postgres still packed postgres" || ok_
    # missing local images are pulled; --pull refreshes; --with-caddy adds caddy
    new_sandbox sv3; FAKE_MISSING_IMAGE=oa-agent-worker run_save --registry "$REG" --tag t3 --out "$SB/out" --with-caddy; assert_rc 0 "$SB/save.txt"
    assert_has "$SB/calls.log" "^docker pull $REG/oa-agent-worker:t3"; assert_not "$SB/calls.log" "^docker pull $REG/oa-agent-api"
    tar -xOf "$SB"/out/oa-agent-images-t3-*.tar manifest.json | grep -q 'caddy:2-alpine' && ok_ || bad_ "caddy not packed"
    : >"$SB/calls.log"; run_save --registry "$REG" --tag t4 --out "$SB/out2" --pull; assert_rc 0 "$SB/save.txt"; assert_count "$SB/calls.log" "^docker pull" 4
    # failures leave nothing published
    new_sandbox sv4; FAKE_SAVE_FAIL=1 run_save --registry "$REG" --tag t5 --out "$SB/out"; assert_rc_nz; assert_eq "$(ls -A "$SB/out" 2>/dev/null | wc -l | tr -d ' ')" 0 "nothing published after a failed save"
    FAKE_PULL_FAIL=1 FAKE_MISSING_IMAGE=oa-agent run_save --registry "$REG" --tag nope --out "$SB/out"; assert_rc_nz; assert_has "$SB/save.txt" 'cannot pull'
    run_save --registry 'https://x' --tag t; assert_rc_nz; run_save --tag 'bad tag'; assert_rc_nz; run_save --arch riscv; assert_rc_nz; run_save --bogus; assert_rc_nz
    run_save --help; assert_rc 0 "$SB/save.txt"; assert_has "$SB/save.txt" 'install.sh --offline'
    bash -n "$S" && ok_ || bad_ "bash -n save-images.sh"
}

t_lock() {
    if ! command -v flock >/dev/null 2>&1; then echo "    (flock not installed: skipped)"; return 0; fi
    new_sandbox lk
    exec 8>"$SB/lock"; flock -n 8
    run_install --yes --dir "$SB/oa"; assert_rc 1; assert_has "$SB/out.txt" 'Another install.sh is already running'; assert_nofile "$SB/oa/.env"
    flock -u 8; exec 8>&-
    run_install --yes --dir "$SB/oa"; assert_rc 0
    # --check does not take the lock
    exec 8>"$SB/lock"; flock -n 8; run_install --check --dir "$SB/oa2"; assert_rc 0; flock -u 8; exec 8>&-
}

t_help_new_options() {
    new_sandbox hn; run_install --help; assert_rc 0
    local o; for o in '--check' '--dry-run' '--offline' '--docker-mirror' '--detect-public-ip' '--reinstall' 'Exit codes'; do assert_has "$SB/out.txt" "$o"; done
    run_install --lang zh --help; assert_rc 0; assert_has "$SB/out.txt" '退出码'; assert_has "$SB/out.txt" '--offline'; assert_has "$SB/out.txt" '只读检查'
    # every message of the new code is bilingual: a Chinese run of --check prints Chinese
    new_sandbox hn2; run_install --lang zh --check --dir "$SB/oa"; assert_rc 0; assert_has "$SB/out.txt" '只读检查'; assert_has "$SB/out.txt" '没有做任何更改'
}

# ---------------------------------------------------------------------------------------------- detection / wizard (installer redesign)
answers() { printf '%s\n' "$@" >"$SB/answers"; }
tty_install() { OA_INSTALL_TTY="$SB/answers" run_install "$@"; }
snap_tree() { (cd "$SB" && find . \( -path ./shim -o -path ./repo -o -name out.txt -o -name calls.log -o -name answers -o -name lock \) -prune -o -print | sort); }
secret_scan() { # secret_scan PASSWORD: the password must not be on screen, in logs, in the call log or in the state file
    assert_not "$SB/out.txt" "$1"; assert_not "$SB/install.log" "$1"; assert_not "$SB/calls.log" "$1"
    [ ! -f "$SB/etc/install.json" ] || assert_not "$SB/etc/install.json" "$1"
}

t_detect_table() {
    new_sandbox dt1; run_install --check --dir "$SB/oa"; assert_rc 0
    assert_has "$SB/out.txt" 'Detection: what is already on this server'
    assert_has "$SB/out.txt" '\[PASS\] Required tools present'
    assert_has "$SB/out.txt" '\[FOUND\] Docker Engine 26.1.0: daemon running, enabled at boot, rootful, endpoint unix:///var/run/docker.sock -> reused, not reinstalled'
    assert_has "$SB/out.txt" '\[FOUND\] Compose v2 plugin 2.29.0 \(>= 2.24'
    assert_has "$SB/out.txt" '\[PASS\] OA Agent: no existing installation found'
    assert_has "$SB/out.txt" 'No other database server found'
    assert_has "$SB/out.txt" 'Detection summary: [0-9]+ passed, [0-9]+ found, 0 missing, 0 need action, 0 warning'
    assert_order "$SB/out.txt" 'Detection: what is already on this server' 'What would be done'
    run_install --lang zh --check --dir "$SB/oa"; assert_rc 0; assert_has "$SB/out.txt" '检测：这台服务器上已经有什么'; assert_has "$SB/out.txt" '检测汇总'; assert_has "$SB/out.txt" 'Docker 引擎 26.1.0'
}

t_detect_docker_states() {
    # docker missing: a MISS row and the plan to install it; --check never installs
    NO_DOCKER=1 new_sandbox dd1; run_install --check --dir "$SB/oa"; assert_rc 0
    assert_has "$SB/out.txt" '\[MISS\] Docker Engine: not installed -> will be installed'; assert_not "$SB/calls.log" 'get-docker|^apt-get'
    # too old: FAIL before anything changes
    new_sandbox dd2; FAKE_DOCKER_VERSION=19.03.1 run_install --yes --dir "$SB/oa"; assert_rc 3; assert_has "$SB/out.txt" '\[FAIL\] Docker 19.03.1 is too old'; assert_nofile "$SB/oa"
    # compose v1 only: only the plugin is installed, Docker is NOT reinstalled
    new_sandbox dd3; FAKE_NO_COMPOSE=1 FAKE_COMPOSE_V1=1.29.2 run_install --check --dir "$SB/oa"; assert_rc 0
    assert_has "$SB/out.txt" '\[TODO\] Compose: only a legacy docker-compose 1.29.2 \(v1\)'; assert_has "$SB/out.txt" 'only the plugin will be installed, Docker is not reinstalled'
    new_sandbox dd4; FAKE_NO_COMPOSE=1 FAKE_COMPOSE_V1=1.29.2 run_install --yes --install-docker --dir "$SB/oa"; assert_rc 0
    assert_has "$SB/calls.log" '^apt-get install -y docker-compose-plugin'; assert_not "$SB/calls.log" 'get-docker|get\.docker\.com|install .*docker\.io'
    assert_has "$SB/out.txt" 'Compose v2 plugin installed \(Docker was not reinstalled\)'; assert_has "$SB/out.txt" 'installed: Installed the Compose v2 plugin only'
    # compose missing without v1, no consent in --yes: stops, tells how
    new_sandbox dd5; FAKE_NO_COMPOSE=1 run_install --yes --dir "$SB/oa"; assert_rc 1; assert_has "$SB/out.txt" '\[MISS\] Compose v2 plugin: not installed -> only the plugin will be installed'; assert_has "$SB/out.txt" 'plugin only, Docker is not reinstalled'; assert_nofile "$SB/oa/.env"
    # daemon down: started, never reinstalled
    new_sandbox dd6; FAKE_DOCKER_DOWN=1 run_install --yes --dir "$SB/oa"; assert_rc 0
    assert_has "$SB/out.txt" '\[TODO\] Docker Engine: installed but the daemon is not running -> will try systemctl start docker'; assert_has "$SB/calls.log" '^systemctl start docker'; assert_not "$SB/calls.log" 'get-docker|^apt-get'
    assert_has "$SB/out.txt" 'changed: Started the Docker daemon'
    # rootless, boot state, endpoint, Podman, old compose
    new_sandbox dd7; FAKE_DOCKER_SECOPT='[name=seccomp name=rootless]' FAKE_DOCKER_ENABLED=no FAKE_CTX_HOST=unix:///run/user/1000/docker.sock run_install --check --dir "$SB/oa"; assert_rc 0
    assert_has "$SB/out.txt" 'rootless, endpoint unix:///run/user/1000/docker.sock'; assert_has "$SB/out.txt" 'not enabled at boot'; assert_has "$SB/out.txt" 'Rootless Docker detected'
    new_sandbox dd8; FAKE_PODMAN=1 run_install --check --dir "$SB/oa"; assert_rc 0; assert_has "$SB/out.txt" 'really Podman'
    new_sandbox dd9; FAKE_COMPOSE_VERSION=2.20.0 run_install --check --dir "$SB/oa"; assert_rc 0; assert_has "$SB/out.txt" 'Compose v2 plugin 2.20.0 \(< 2.24'
    new_sandbox dd10; FAKE_COMPOSE_VERSION=2.20.0 run_install --check --yes --dir "$SB/oa" --db postgres --db-host db.example.com; assert_rc 2
    printf 'db_password=Sup3rSecretPw\n' >"$SB/c.conf"; chmod 600 "$SB/c.conf"
    FAKE_COMPOSE_VERSION=2.20.0 run_install --check --dir "$SB/oa" --config "$SB/c.conf" --db postgres --db-host db.example.com; assert_rc 3
    assert_has "$SB/out.txt" 'needs Docker Compose 2.24\+, found 2.20.0'; assert_nofile "$SB/oa"
}

t_detect_prereqs() {
    new_sandbox pr1; OA_TEST_MISSING_CMDS='curl flock' run_install --check --dir "$SB/oa"; assert_rc 0
    assert_has "$SB/out.txt" '\[MISS\] Missing required tools: curl -> only these are installed'; assert_has "$SB/out.txt" 'Missing recommended tools: flock'
    assert_not "$SB/calls.log" '^apt-get'
    # --yes without consent: stops and says the exact command
    new_sandbox pr2; OA_TEST_MISSING_CMDS='curl flock' run_install --yes --dir "$SB/oa"; assert_rc 1; assert_has "$SB/out.txt" '--install-prereqs'; assert_has "$SB/out.txt" 'apt-get install curl util-linux'; assert_nofile "$SB/oa/.env"
    # with consent: ONLY the missing packages, nothing else
    new_sandbox pr3; OA_TEST_MISSING_CMDS='curl flock' run_install --yes --install-prereqs --dir "$SB/oa"; assert_rc 0
    assert_has "$SB/calls.log" '^apt-get install -y curl'; assert_has "$SB/calls.log" '^apt-get install -y util-linux'; assert_not "$SB/calls.log" 'install -y (tar|gzip|coreutils|gawk|openssl)'
    assert_not "$SB/calls.log" 'get-docker'; assert_has "$SB/out.txt" 'installed: Installed the missing packages: curl util-linux'
    # interactive consent
    new_sandbox pr4; answers "$SB/oa" 18080 '' 1 '' n '' default y n; OA_TEST_MISSING_CMDS=curl tty_install --dir "$SB/oa"; assert_rc 4; assert_not "$SB/calls.log" '^apt-get install'
}

t_detect_installs() {
    # found through the state file (custom directory, no --dir on the second run)
    new_sandbox in1; run_install --yes --dir "$SB/custom/oa"; assert_rc 0; assert_file "$SB/etc/install.json"
    assert_has "$SB/etc/install.json" "\"dir\":\"$SB/custom/oa\""; assert_has "$SB/etc/install.json" '"status":"installed"'; assert_eq "$(mode "$SB/etc/install.json")" 644 "state file mode"
    rm -f "$SB/etc/install_dir"; : >"$SB/calls.log"
    run_install --yes; assert_rc 0
    assert_has "$SB/out.txt" "OA Agent installed: $SB/custom/oa \\(project oa-agent, tag latest, database bundled, port 8080, running, healthy; found via: (state|state container|container state)"
    assert_has "$SB/out.txt" 'Existing installation detected'; assert_not "$SB/calls.log" 'compose\[[^]]*\] (up|down|build|pull|stop|restart)'
    # found through the container labels only
    rm -f "$SB/etc/install.json" "$SB/etc/install_dir"; run_install --yes; assert_rc 0; assert_has "$SB/out.txt" 'found via: container'
    # --dir that is not the installation but another one exists: a fresh install is allowed, the project name is kept apart
    # a state entry whose directory vanished is reported and left alone
    new_sandbox in3; mkdir -p "$SB/etc"; printf '{"schema":1,"installs":[\n{"dir":"%s/gone","project":"oa-agent","version":"1","registry":"x","db":"bundled","port":8080,"domain":"","status":"installed","archive":"","app":"","pg":"","updated":"2026-01-01T00:00:00Z"}\n]}\n' "$SB" >"$SB/etc/install.json"
    run_install --check --dir "$SB/oa"; assert_rc 0; assert_has "$SB/out.txt" "\\[TODO\\] $SB/gone is referenced \\(via state\\) but has no usable .env"; assert_nofile "$SB/gone"
    # two installations: --yes without --dir must not guess
    new_sandbox in4; run_install --yes --dir "$SB/a"; assert_rc 0; run_install --yes --dir "$SB/b"; assert_rc 0
    assert_has "$SB/out.txt" 'using oa-agent-2 instead'; assert_eq "$(grep -E '^COMPOSE_PROJECT_NAME=' "$SB/b/.env" | cut -d= -f2)" oa-agent-2 "second project"
    run_install --yes; assert_rc 2; assert_has "$SB/out.txt" 'Several installations exist: use --dir'
    answers 2; OA_INSTALL_TTY="$SB/answers" run_install; assert_rc 0; assert_has "$SB/out.txt" 'Several installations were found'
    # an explicit --project that collides is refused
    new_sandbox in5; run_install --yes --dir "$SB/a"; run_install --yes --dir "$SB/b" --project oa-agent; assert_rc 2; assert_has "$SB/out.txt" 'already used by the installation in'; assert_nofile "$SB/b"
    # --status is read-only and needs no root
    new_sandbox in6; run_install --status; assert_rc 0; assert_has "$SB/out.txt" 'No OA Agent installation found'
    run_install --yes --dir "$SB/oa"; local before; before="$(snap_tree)"; : >"$SB/calls.log"
    run_install --status --dir "$SB/oa"; assert_rc 0; assert_has "$SB/out.txt" 'Status: '; assert_has "$SB/out.txt" 'SETUP TOKEN'; assert_has "$SB/out.txt" 'api +running \(Up 5 minutes \(healthy\)\)'
    assert_eq "$(snap_tree)" "$before" "--status changed nothing"; assert_not "$SB/calls.log" 'compose\[[^]]*\] (up|down|build|pull|stop|restart)'
    run_install --status --upgrade; assert_rc 2
}

t_idempotent_fastpath() {
    new_sandbox fp1; run_install --yes --dir "$SB/oa"; assert_rc 0
    local before; before="$(grep -E '^OA_(SECRET_KEY|WORKER_TOKEN|POSTGRES_PASSWORD)=' "$SB/oa/.env")"; : >"$SB/calls.log"
    run_install --yes --dir "$SB/oa"; assert_rc 0
    assert_has "$SB/out.txt" 'Existing installation detected: .*all services are healthy'; assert_has "$SB/out.txt" 'SETUP TOKEN'
    assert_not "$SB/calls.log" '^docker (pull|create|cp|run|load)'; assert_not "$SB/calls.log" 'compose\[[^]]*\] (up|down|build|pull|stop|restart)'; assert_not "$SB/calls.log" 'oa-agentctl|apt-get|get-docker'
    assert_eq "$(grep -E '^OA_(SECRET_KEY|WORKER_TOKEN|POSTGRES_PASSWORD)=' "$SB/oa/.env")" "$before" "secrets unchanged"
    assert_not "$SB/out.txt" 'Preparing files'; assert_not "$SB/out.txt" 'Preflight checks'
    # interactive: the menu defaults to "nothing to do" (empty answer) and changes nothing
    : >"$SB/calls.log"; answers ''; tty_install --dir "$SB/oa"; assert_rc 0; assert_has "$SB/out.txt" 'nothing to do'; assert_has "$SB/out.txt" 'Choice \[1-6\] \(default 6\)'; assert_has "$SB/out.txt" 'Status: '
    assert_not "$SB/calls.log" 'compose\[[^]]*\] (up|down|build|pull)'
    # a requested change is NOT swallowed by the fast path
    : >"$SB/calls.log"; mkdir -p "$SB/newarch"; run_install --yes --dir "$SB/oa" --archive-dir "$SB/newarch/archive"; assert_rc 0; assert_has "$SB/calls.log" 'oa-agentctl --dir .* mounts set archive'
    # unhealthy / stopped: repair/continue (the old behaviour) instead of "nothing to do"
    new_sandbox fp2; run_install --yes --dir "$SB/oa"; : >"$SB/calls.log"; FAKE_UNHEALTHY=api run_install --yes --dir "$SB/oa"; assert_rc 0; assert_has "$SB/calls.log" 'compose\[[^]]*\] up -d'; assert_not "$SB/out.txt" 'all services are healthy'
    rm -f "$SB"/state/up-*; : >"$SB/calls.log"; run_install --yes --dir "$SB/oa"; assert_rc 0; assert_has "$SB/calls.log" 'compose\[[^]]*\] up -d'
    # --check on a healthy install still prints the plan (it is not the fast path)
    new_sandbox fp3; run_install --yes --dir "$SB/oa"; run_install --check --dir "$SB/oa"; assert_rc 0; assert_has "$SB/out.txt" 'What would be done'; assert_has "$SB/out.txt" 'repair/continue'
    # --port that differs from the installation is ignored with a note, not applied
    run_install --yes --dir "$SB/oa" --port 9999; assert_rc 0; assert_has "$SB/out.txt" 'Ignored --port 9999'; assert_eq "$(envv OA_PORT)" 8080 "port kept"
}

t_state_file() {
    new_sandbox sf1; run_install --check --dir "$SB/oa"; assert_nofile "$SB/etc/install.json"
    run_install --yes --dir "$SB/oa" --port 18080; assert_rc 0
    assert_has "$SB/etc/install.json" '^\{"schema":1,"installs":\['; assert_has "$SB/etc/install.json" '"project":"oa-agent","version":"latest","registry":"oa-agent","db":"bundled","port":18080'
    assert_has "$SB/out.txt" "State file: +$SB/etc/install.json"
    run_install --yes --dir "$SB/oa2" --port 18081; assert_rc 0; assert_count "$SB/etc/install.json" '^\{"dir":' 2; assert_has "$SB/etc/install.json" "\"dir\":\"$SB/oa\""
    # uninstall keeps the entry as removed; purge forgets it
    run_install --yes --dir "$SB/oa2" --uninstall; assert_rc 0; assert_has "$SB/etc/install.json" "\"dir\":\"$SB/oa2\".*\"status\":\"removed\""; assert_has "$SB/etc/install.json" "\"dir\":\"$SB/oa\".*\"status\":\"installed\""
    OA_PURGE_CONFIRM=oa run_install --yes --dir "$SB/oa" --uninstall --purge; assert_rc 0; assert_not "$SB/etc/install.json" "\"dir\":\"$SB/oa\""
    # an unwritable location is a warning, not a failure
    new_sandbox sf2; mkdir -p "$SB/ro"; chmod 555 "$SB/ro"; OA_INSTALL_STATE="$SB/ro/sub/install.json" run_install --yes --dir "$SB/oa"; assert_rc 0
    if [ "$(id -u)" -ne 0 ]; then assert_has "$SB/out.txt" 'Cannot write the state file'; fi
    chmod 755 "$SB/ro"
    # oa-agentctl keeps the version in step
    new_sandbox sf3; run_install --yes --dir "$SB/oa"; run_ctl upgrade --tag 1.2.3 --no-backup; assert_rc 0; assert_has "$SB/etc/install.json" '"version":"1.2.3"'
}

t_orphans() {
    # database files + secret.key, no .env: refused with an explanation, nothing is written
    new_sandbox or1; mkdir -p "$SB/oa/data/pg/pgdata" "$SB/oa/data/app"; echo 16 >"$SB/oa/data/pg/pgdata/PG_VERSION"; printf '%s\n' "$(printf 'ab%.0s' $(seq 32))" >"$SB/oa/data/app/secret.key"
    run_install --yes --dir "$SB/oa"; assert_rc 3; assert_has "$SB/out.txt" 'Database files of an earlier installation were found'; assert_has "$SB/out.txt" '\[TODO\] Data of an earlier installation without .env found in'; assert_has "$SB/out.txt" 'Refusing to continue'; assert_has "$SB/out.txt" 'never deleted'; assert_nofile "$SB/oa/.env"
    assert_not "$SB/calls.log" 'compose\[|apt-get|get-docker'; assert_file "$SB/oa/data/pg/pgdata/PG_VERSION"
    # the same data but a fresh, empty PostgreSQL directory: allowed, and the OLD key is reused (never a new key over old data)
    run_install --yes --dir "$SB/oa" --pg-dir "$SB/newpg"; assert_rc 0; assert_eq "$(envv OA_SECRET_KEY)" "$(head -n 1 "$SB/oa/data/app/secret.key")" "old key reused"
    assert_has "$SB/out.txt" 'reuses that key'; assert_has "$SB/out.txt" 'changed: Reused the existing secret.key'
    # damaged key file: refuse
    new_sandbox or2; mkdir -p "$SB/oa/data/app"; echo notakey >"$SB/oa/data/app/secret.key"; run_install --yes --dir "$SB/oa"; assert_rc 3; assert_has "$SB/out.txt" 'not a 64-hex-character key'; assert_nofile "$SB/oa/.env"
    # a backup with the old .env: --yes needs --adopt-data; with it the .env is restored and the install continues as repair
    new_sandbox or3; run_install --yes --dir "$SB/src1"; assert_rc 0
    run_ctl_dir() { OA_POLL_SECONDS=0 OA_UNIT_DIR="$SB/units" OA_CTL_SELF="$SB/bin/oa-agentctl" OA_LANG=en PATH="$SB/shim:$PATH" bash "$SB/bin/oa-agentctl.sh" --dir "$SB/src1" "$@" >"$SB/ctl.txt" 2>&1; RC=$?; }
    local oldkey; oldkey="$(grep -E '^OA_SECRET_KEY=' "$SB/src1/.env" | cut -d= -f2)"
    mkdir -p "$SB/ob/oa/backups" "$SB/ob/stage"; cp "$SB/src1/.env" "$SB/ob/stage/env"; echo PGDUMP >"$SB/ob/stage/db.dump"; (cd "$SB/ob/stage" && tar -czf "$SB/ob/oa/backups/oa-agent-backup-20260101-000000.tar.gz" ./env ./db.dump)
    mkdir -p "$SB/ob/oa/data/pg/pgdata"; echo 16 >"$SB/ob/oa/data/pg/pgdata/PG_VERSION"
    run_install --yes --dir "$SB/ob/oa"; assert_rc 3; assert_has "$SB/out.txt" '--adopt-data'; assert_nofile "$SB/ob/oa/.env"
    run_install --yes --adopt-data --dir "$SB/ob/oa"; assert_rc 0; assert_has "$SB/out.txt" 'Restored .*/\.env from the backup'; assert_eq "$(grep -E '^OA_SECRET_KEY=' "$SB/ob/oa/.env" | cut -d= -f2)" "$oldkey" "backup key"; assert_eq "$(mode "$SB/ob/oa/.env")" 600 ".env mode"
    # interactive: restore offered, default yes
    new_sandbox or4; mkdir -p "$SB/ob/oa/backups" "$SB/ob/stage"; printf 'OA_SECRET_KEY=%s\nOA_POSTGRES_PASSWORD=x\nCOMPOSE_FILE=docker-compose.yml:docker-compose.mounts.yml:docker-compose.build.yml\nOA_PORT=18080\n' "$(printf 'cd%.0s' $(seq 32))" >"$SB/ob/stage/env"
    (cd "$SB/ob/stage" && tar -czf "$SB/ob/oa/backups/oa-agent-backup-20260101-000000.tar.gz" ./env); mkdir -p "$SB/ob/oa/data/pg/pgdata"; echo 16 >"$SB/ob/oa/data/pg/pgdata/PG_VERSION"
    answers y y; tty_install --dir "$SB/ob/oa"; assert_rc 0; assert_has "$SB/out.txt" 'Restore the .env from this backup'; assert_eq "$(envv_at "$SB/ob/oa" OA_PORT)" 18080 "restored port"
    # archive files only: informational
    new_sandbox or5; mkdir -p "$SB/oa/data/archive/x"; echo f >"$SB/oa/data/archive/x/f.txt"; run_install --yes --dir "$SB/oa"; assert_rc 0; assert_has "$SB/out.txt" 'archive directory .* already holds files'; assert_file "$SB/oa/data/archive/x/f.txt"
}
envv_at() { grep -E "^$2=" "$1/.env" | tail -n 1 | cut -d= -f2-; }

t_ports_and_proxy() {
    # who owns the port is named; the default port moves to the next free one; an explicit busy --port is a FAIL
    new_sandbox pp1; FAKE_PORTS='8080=nginx' run_install --yes --dir "$SB/oa"; assert_rc 0
    assert_has "$SB/out.txt" 'Port 8080 is in use by nginx \(pid 4242\), a reverse proxy'; assert_has "$SB/out.txt" 'suggesting the free port 8081'; assert_eq "$(envv OA_PORT)" 8081 "next free port"
    new_sandbox pp2; FAKE_PORTS='18080=nginx' run_install --yes --dir "$SB/oa" --port 18080; assert_rc 3; assert_has "$SB/out.txt" 'Port 18080 is already in use \(nginx'; assert_has "$SB/out.txt" 'suggestion: 18081'; assert_nofile "$SB/oa"
    new_sandbox pp3; FAKE_PORTS='80=nginx 443=nginx' run_install --yes --dir "$SB/oa" --domain oa.example.com; assert_rc 3; assert_has "$SB/out.txt" 'A reverse proxy holds 80/443: add --behind-proxy --domain'
    new_sandbox pp4; FAKE_PS_EXTRA='web1|nginx:1.27|running|web||0.0.0.0:8080->80/tcp|web|Up 2 days' FAKE_PORTS='8080' run_install --check --dir "$SB/oa"; assert_has "$SB/out.txt" 'Port 8080 is in use by Docker container web1 \(image nginx:1.27\)'
    # unattended --behind-proxy: no Caddy, API on loopback, snippets printed
    new_sandbox pp5; FAKE_PORTS='80=nginx 443=nginx' run_install --yes --dir "$SB/oa" --domain oa.example.com --behind-proxy --port 18080; assert_rc 0
    assert_eq "$(envv OA_BIND_ADDR)" 127.0.0.1 bind; assert_eq "$(envv OA_TRUSTED_PROXY)" 1 trusted; assert_eq "$(envv OA_DOMAIN)" '' "no caddy domain"; assert_eq "$(envv OA_BEHIND_PROXY)" 1 flag; assert_eq "$(envv OA_PUBLIC_URL)" https://oa.example.com url
    assert_eq "$(envv COMPOSE_FILE)" docker-compose.yml:docker-compose.mounts.yml:docker-compose.build.yml "no https layer"; assert_nofile "$SB/oa/Caddyfile"
    assert_has "$SB/out.txt" 'proxy_pass http://127.0.0.1:18080'; assert_has "$SB/out.txt" 'server_name oa.example.com'; assert_has "$SB/out.txt" 'reverse_proxy 127.0.0.1:18080'; assert_has "$SB/out.txt" 'client_max_body_size 0'
    run_install --yes --dir "$SB/oa3" --behind-proxy; assert_rc 2; assert_has "$SB/out.txt" 'behind-proxy mode needs a domain'
    # interactive: domain given, 80/443 held by a proxy -> three choices, 1 = behind it
    new_sandbox pp6; answers "$SB/oa" 18080 oa.example.com 1 1 '' n '' default y; FAKE_PORTS='80=nginx 443=nginx' OA_INSTALL_TTY="$SB/answers" run_install --dir "$SB/oa"; assert_rc 0
    assert_has "$SB/out.txt" 'put OA behind the existing reverse proxy'; assert_has "$SB/out.txt" 'Port 80/443 is taken'; assert_eq "$(envv OA_BEHIND_PROXY)" 1 "wizard proxy"; assert_has "$SB/out.txt" 'behind an existing reverse proxy'
    # choice 2 ignores the domain
    new_sandbox pp7; answers "$SB/oa" 18080 oa.example.com 2 1 '' n '' default y; FAKE_PORTS='443=caddy' OA_INSTALL_TTY="$SB/answers" run_install --dir "$SB/oa"; assert_rc 0; assert_eq "$(envv OA_DOMAIN)" '' "domain ignored"; assert_eq "$(envv OA_BIND_ADDR)" 0.0.0.0 bind
    # interactive busy port is refused with the owner and a suggestion, then re-asked
    new_sandbox pp8; answers "$SB/oa" 18080 18081 '' 1 '' n '' default y; FAKE_PORTS='18080=apache2' OA_INSTALL_TTY="$SB/answers" run_install --dir "$SB/oa"; assert_rc 0
    assert_has "$SB/out.txt" 'port 18080 is already in use by apache2 \(pid 4242\)'; assert_eq "$(envv OA_PORT)" 18081 "second answer"
}

t_db_servers_detected() {
    new_sandbox ds1; FAKE_PORTS='5432=postgres@127.0.0.1 3306=mysqld 1433=sqlservr' FAKE_UNITS='postgresql.service mariadb.service' run_install --check --dir "$SB/oa"; assert_rc 0
    assert_has "$SB/out.txt" 'Existing database server: PostgreSQL on port 5432 \(process postgres; listening on loopback only'
    assert_has "$SB/out.txt" 'Existing database server: MySQL/MariaDB on port 3306 \(process mysqld; listening on 0.0.0.0'; assert_has "$SB/out.txt" 'Existing database server: SQL Server on port 1433'
    assert_has "$SB/out.txt" 'Database service units installed: postgresql.service mariadb.service'; assert_has "$SB/out.txt" 'never connects to it'
    assert_not "$SB/calls.log" 'psql|mysql |sqlcmd'
    new_sandbox ds2; FAKE_PS_EXTRA='pg-corp|postgres:16|running|corp||0.0.0.0:5433->5432/tcp|db|Up 3 days;oa-agent-postgres-1|postgres:16-alpine|running|oa-agent||5432/tcp|postgres|Up 1 hour (healthy);oa-agent-api-1|reg.example.com/ns/oa-agent-api:latest|running|oa-agent||8080/tcp|api|Up 1 hour (healthy)' run_install --check --dir "$SB/oa"
    assert_has "$SB/out.txt" 'Existing database container: pg-corp'; assert_not "$SB/out.txt" 'database container: oa-agent-postgres-1'
}

t_net_hint() {
    # Docker Hub unreachable: the mirror question defaults to cn (asked, never silent); --yes keeps "default" and says so
    new_sandbox nh1; FAKE_NO_HUB=1 run_install --check --dir "$SB/oa"; assert_has "$SB/out.txt" 'Docker Hub is not reachable'; assert_has "$SB/out.txt" 'nothing is changed silently'
    new_sandbox nh2; answers "$SB/oa" 18080 '' 1 '' n '' '' y; FAKE_NO_HUB=1 tty_install --dir "$SB/oa"; assert_rc 0; assert_has "$SB/out.txt" 'so the default is cn'; assert_eq "$(envv OA_BUILD_GOPROXY)" 'https://goproxy.cn,direct' goproxy; assert_has "$SB/out.txt" 'preselected from detection'
    new_sandbox nh3; FAKE_NO_HUB=1 run_install --yes --dir "$SB/oa"; assert_rc 0; assert_eq "$(envv OA_BUILD_GOPROXY)" '' "no silent cn"
    new_sandbox nh4; answers "$SB/oa" 18080 '' 1 '' n '' '' y; tty_install --dir "$SB/oa"; assert_rc 0; assert_not "$SB/out.txt" 'so the default is cn'; assert_eq "$(envv OA_BUILD_GOPROXY)" '' "reachable hub"
}

t_wizard_happy() {
    new_sandbox wz1; mkdir -p "$SB/d"
    answers "$SB/oa" 18090 '' 1 "$SB/d/archive" n '' default y
    tty_install --dir "$SB/oa"; assert_rc 0
    sed 's/\]: /]:\n/g' "$SB/out.txt" >"$SB/out.split"
    assert_order "$SB/out.split" 'Install directory \(program' 'HTTP port \['; assert_order "$SB/out.split" 'HTTP port \[' 'Domain \(empty = HTTP only'; assert_order "$SB/out.split" 'Domain \(empty' 'Database:'
    assert_order "$SB/out.txt" 'Database:' 'Archive root directory'; assert_order "$SB/out.txt" 'Archive root directory' 'Mirror cn\(China\)/default'; assert_order "$SB/out.txt" 'Mirror cn\(China\)/default' 'Review your settings'
    assert_order "$SB/out.txt" 'Review your settings' 'Proceed\? \[Y\]es / \[e\]dit a setting / \[c\]ancel'; assert_order "$SB/out.txt" 'Proceed\?' 'Preflight checks'
    assert_has "$SB/out.txt" 'Use the bundled PostgreSQL \(recommended for a new standalone server'; assert_has "$SB/out.txt" 'Use an existing MySQL 8.0\+ server'; assert_has "$SB/out.txt" 'Use an existing SQL Server 2019\+ server'
    assert_has "$SB/out.txt" '4\) Database +bundled PostgreSQL'; assert_has "$SB/out.txt" '1\) Install dir +'"$SB"'/oa'
    assert_eq "$(envv OA_PORT)" 18090 port; assert_eq "$(envv OA_ARCHIVE_DIR)" "$SB/d/archive" archive; assert_not "$SB/oa/.env" 'OA_DB_PROVIDER'
    # the wizard shows the detection first
    assert_order "$SB/out.txt" 'Detection summary' 'Configuration wizard'
    # Chinese UI
    new_sandbox wz2; answers "$SB/oa" 18090 '' 1 "$SB/d/archive" n '' default y; OA_INSTALL_TTY="$SB/answers" run_install --lang zh --dir "$SB/oa"; assert_rc 0; assert_has "$SB/out.txt" '配置向导'; assert_has "$SB/out.txt" '请复核全部设置'; assert_has "$SB/out.txt" '继续安装？'
}

t_review_edit_cancel() {
    # edit a setting by number: the port is changed from 18090 to 18091, the review is shown again, then Proceed
    new_sandbox rv1; answers "$SB/oa" 18090 '' 1 '' n '' default e 2 18091 y
    tty_install --dir "$SB/oa"; assert_rc 0; assert_eq "$(envv OA_PORT)" 18091 "edited port"; assert_count "$SB/out.txt" 'Review your settings' 2; assert_has "$SB/out.txt" 'Edit which setting'
    # edit the access question: set a domain
    new_sandbox rv2; answers "$SB/oa" 18090 '' 1 '' n '' default e 3 oa.example.com '' y; tty_install --dir "$SB/oa"; assert_rc 0; assert_eq "$(envv OA_DOMAIN)" oa.example.com domain; assert_eq "$(envv OA_BIND_ADDR)" 127.0.0.1 bind
    # edit the database: switch to bundled again is allowed; invalid review input is re-asked
    new_sandbox rv3; answers "$SB/oa" 18090 '' 1 '' n '' default zzz e 10 y; tty_install --dir "$SB/oa"; assert_rc 0; assert_has "$SB/out.txt" 'enter Y, e or c'; assert_has "$SB/out.txt" 'enter 1-9'; assert_count "$SB/out.txt" 'Review your settings' 3
    # cancel: exit 4 and NOTHING exists afterwards
    new_sandbox rv4; local before; before="$(snap_tree)"; answers "$SB/oa" 18090 '' 1 '' n '' default c; tty_install --dir "$SB/oa"; assert_rc 4; assert_has "$SB/out.txt" 'Cancelled: nothing was changed'
    assert_nofile "$SB/oa"; assert_nofile "$SB/etc/install.json"; assert_eq "$(snap_tree | grep -v '^./install.log$')" "$(echo "$before" | grep -v '^./install.log$')" "cancel changed nothing"; assert_not "$SB/calls.log" 'compose\[|^apt-get|^docker pull'
}

t_invalid_reprompt() {
    new_sandbox iv1; answers relative/dir /etc "$SB/oa" abc 99999 0 18090 'bad domain' 10.0.0.1 '' 1 '' n '' xx default y
    tty_install --dir "$SB/oa"; assert_rc 0
    assert_has "$SB/out.txt" 'must be an absolute path'; assert_has "$SB/out.txt" 'the port must be a number from 1 to 65535'; assert_count "$SB/out.txt" 'the port must be a number from 1 to 65535' 3
    assert_has "$SB/out.txt" 'a full domain name is required'; assert_has "$SB/out.txt" 'enter cn or default'; assert_eq "$(envv OA_PORT)" 18090 port
    # too many invalid answers: stop with exit 2, nothing changed
    new_sandbox iv2; answers a b c d e f g; tty_install --dir "$SB/oa"; assert_rc 2; assert_has "$SB/out.txt" 'Too many invalid answers'; assert_nofile "$SB/oa/.env"
    # unattended: an invalid flag stops (no re-prompt possible)
    new_sandbox iv3; run_install --yes --dir "$SB/oa" --port abc; assert_rc 2; assert_nofile "$SB/oa"
}

t_piped_tty() {
    # curl | bash: stdin is the script, the answers come from the terminal device
    new_sandbox pt1; answers "$SB/oa" 18090 '' 1 '' n '' default y
    ( cd "$SB" && cat "$LINUX/install.sh" | OA_TEST_MODE=1 OA_INSTALL_TTY="$SB/answers" OA_INSTALL_LOG="$SB/install.log" OA_INSTALL_BIN_DIR="$SB/bin" OA_INSTALL_UNIT_DIR="$SB/units" OA_INSTALL_ETC_MARKER="$SB/etc/install_dir" OA_OS_RELEASE="$SB/os-release" OA_MEMINFO="$SB/meminfo" OA_POLL_SECONDS=0 OA_RETRY_BASE=0 OA_INSTALL_LOCK="$SB/lock" OA_DAEMON_JSON="$SB/daemon.json" OA_TEST_DOCKER_SHIM="$SB/shim/docker" OA_BUILD_FROM_SOURCE=1 PATH="$SB/shim:$PATH" bash -s -- --lang en --dir "$SB/oa" ) >"$SB/out.txt" 2>&1; RC=$?
    # no local source when piped: the bundle/checkout is missing here, so only the wizard part is asserted
    assert_has "$SB/out.txt" 'Review your settings'; assert_has "$SB/out.txt" 'Proceed\?'
    # no terminal at all and no --yes: the message, exit 2, nothing changed
    new_sandbox pt2; ( cd "$SB" && cat "$LINUX/install.sh" | OA_TEST_MODE=1 OA_INSTALL_TTY=/nonexistent OA_INSTALL_LOG="$SB/install.log" OA_INSTALL_BIN_DIR="$SB/bin" OA_INSTALL_UNIT_DIR="$SB/units" OA_INSTALL_ETC_MARKER="$SB/etc/install_dir" OA_OS_RELEASE="$SB/os-release" OA_MEMINFO="$SB/meminfo" OA_POLL_SECONDS=0 OA_RETRY_BASE=0 OA_INSTALL_LOCK="$SB/lock" OA_DAEMON_JSON="$SB/daemon.json" OA_TEST_DOCKER_SHIM="$SB/shim/docker" PATH="$SB/shim:$PATH" bash -s -- --lang en --dir "$SB/oa" ) >"$SB/out.txt" 2>&1; RC=$?
    assert_rc 2; assert_has "$SB/out.txt" 'No terminal is available for the installation questions'; assert_has "$SB/out.txt" 'ssh -t'; assert_has "$SB/out.txt" 'docker run --rm'; assert_has "$SB/out.txt" 'pass --yes'; assert_nofile "$SB/oa"
    new_sandbox pt3; ( cd "$SB" && cat "$LINUX/install.sh" | OA_TEST_MODE=1 OA_INSTALL_TTY=/nonexistent OA_INSTALL_LOG="$SB/install.log" OA_INSTALL_BIN_DIR="$SB/bin" OA_INSTALL_UNIT_DIR="$SB/units" OA_INSTALL_ETC_MARKER="$SB/etc/install_dir" OA_OS_RELEASE="$SB/os-release" OA_MEMINFO="$SB/meminfo" OA_POLL_SECONDS=0 OA_RETRY_BASE=0 OA_INSTALL_LOCK="$SB/lock" OA_DAEMON_JSON="$SB/daemon.json" OA_TEST_DOCKER_SHIM="$SB/shim/docker" PATH="$SB/shim:$PATH" bash -s -- --lang zh --dir "$SB/oa" ) >"$SB/out.txt" 2>&1; RC=$?
    assert_rc 2; assert_has "$SB/out.txt" '没有可用的终端来回答安装问题'
    # an existing installation without a terminal only shows its status
    new_sandbox pt4; run_install --yes --dir "$SB/oa"; ( cd "$SB" && OA_TEST_MODE=1 OA_INSTALL_TTY=/nonexistent OA_INSTALL_LOG="$SB/install.log" OA_INSTALL_BIN_DIR="$SB/bin" OA_INSTALL_UNIT_DIR="$SB/units" OA_INSTALL_ETC_MARKER="$SB/etc/install_dir" OA_OS_RELEASE="$SB/os-release" OA_MEMINFO="$SB/meminfo" OA_POLL_SECONDS=0 OA_RETRY_BASE=0 OA_INSTALL_LOCK="$SB/lock" OA_DAEMON_JSON="$SB/daemon.json" OA_TEST_DOCKER_SHIM="$SB/shim/docker" PATH="$SB/shim:$PATH" bash "$LINUX/install.sh" --lang en --dir "$SB/oa" </dev/null ) >"$SB/out.txt" 2>&1; RC=$?
    assert_rc 0; assert_has "$SB/out.txt" 'only its status is shown'; assert_has "$SB/out.txt" 'Status: '
}

t_config_file() {
    new_sandbox cf1; mkdir -p "$SB/d" "$SB/nas"
    printf '# answers\ndir=%s/oa\nport=18092\nmirror=default\narchive_dir=%s/d/archive\nextra_mount=nas1=%s/nas:ro\n' "$SB" "$SB" "$SB" >"$SB/a.conf"
    # permissions are enforced
    chmod 644 "$SB/a.conf"; run_install --yes --config "$SB/a.conf"; assert_rc 2; assert_has "$SB/out.txt" 'has mode 644'; assert_has "$SB/out.txt" 'chmod 600'; assert_nofile "$SB/oa"
    chmod 640 "$SB/a.conf"; run_install --yes --config "$SB/a.conf"; assert_rc 2
    chmod 600 "$SB/a.conf"; ln -sf "$SB/a.conf" "$SB/link.conf"; run_install --yes --config "$SB/link.conf"; assert_rc 2; assert_has "$SB/out.txt" 'symlinks are refused'
    run_install --yes --config "$SB/missing.conf"; assert_rc 2
    run_install --yes --config "$SB/a.conf"; assert_rc 0; assert_eq "$(envv OA_PORT)" 18092 port; assert_eq "$(envv OA_ARCHIVE_DIR)" "$SB/d/archive" archive; assert_has "$SB/oa/mounts.json" '"name": "nas1"'
    # bad lines / unknown keys name the line
    printf 'port=1\nbogus_key=1\n' >"$SB/b.conf"; chmod 600 "$SB/b.conf"; run_install --yes --config "$SB/b.conf" --dir "$SB/o2"; assert_rc 2; assert_has "$SB/out.txt" 'line 2: unknown key bogus_key'
    printf 'port 1\n' >"$SB/b.conf"; run_install --yes --config "$SB/b.conf" --dir "$SB/o2"; assert_rc 2; assert_has "$SB/out.txt" 'line 1: expected key=value'
    # precedence: flag > environment > file
    new_sandbox cf2; printf 'port=18092\n' >"$SB/p.conf"; chmod 600 "$SB/p.conf"; run_install --yes --dir "$SB/oa" --config "$SB/p.conf"; assert_rc 0; assert_eq "$(envv OA_PORT)" 18092 "file value"
    new_sandbox cf2b; printf 'port=18092\n' >"$SB/p.conf"; chmod 600 "$SB/p.conf"; OA_INSTALL_PORT=18093 run_install --yes --dir "$SB/oa" --config "$SB/p.conf"; assert_rc 0; assert_eq "$(envv OA_PORT)" 18093 "env beats file"
    new_sandbox cf3; printf 'port=18092\n' >"$SB/p.conf"; chmod 600 "$SB/p.conf"; OA_INSTALL_PORT=18093 run_install --yes --dir "$SB/oa" --config "$SB/p.conf" --port 18094; assert_rc 0; assert_eq "$(envv OA_PORT)" 18094 "flag beats env"
    # seeded answers are validated but not asked again; the rest is asked
    new_sandbox cf4; printf 'dir=%s/oa\nport=18095\n' "$SB" >"$SB/s.conf"; chmod 600 "$SB/s.conf"; answers 1 '' n '' default y
    tty_install --config "$SB/s.conf"; assert_rc 0; assert_not "$SB/out.txt" 'Install directory \(program'; assert_not "$SB/out.txt" 'HTTP port \['; assert_has "$SB/out.txt" 'Domain \(empty'; assert_eq "$(envv OA_PORT)" 18095 "seeded port"
    new_sandbox cf5; printf 'port=abc\n' >"$SB/s.conf"; chmod 600 "$SB/s.conf"; answers "$SB/oa"; tty_install --config "$SB/s.conf" --dir "$SB/oa"; assert_rc 2; assert_has "$SB/out.txt" 'the port must be a number'
    # passwords only from the file
    new_sandbox cf6; OA_INSTALL_DB_PASSWORD=Sup3rSecretPw run_install --yes --dir "$SB/oa" --db postgres --db-host db.example.com; assert_rc 2; assert_has "$SB/out.txt" 'Ignoring OA_INSTALL_DB_PASSWORD'; assert_has "$SB/out.txt" 'password is missing'; secret_scan Sup3rSecretPw
}

t_print_config() {
    new_sandbox pc1; mkdir -p "$SB/d"; local before; before="$(snap_tree)"
    ( OA_TEST_MODE=1 OA_INSTALL_LOG="$SB/install.log" OA_INSTALL_BIN_DIR="$SB/bin" OA_INSTALL_UNIT_DIR="$SB/units" OA_INSTALL_ETC_MARKER="$SB/etc/install_dir" OA_OS_RELEASE="$SB/os-release" OA_MEMINFO="$SB/meminfo" OA_POLL_SECONDS=0 OA_RETRY_BASE=0 OA_INSTALL_LOCK="$SB/lock" OA_DAEMON_JSON="$SB/daemon.json" OA_TEST_DOCKER_SHIM="$SB/shim/docker" PATH="$SB/shim:$PATH" bash "$SB/repo/deploy/linux/install.sh" --lang en --yes --print-config --dir "$SB/oa" --port 18096 --archive-dir "$SB/d/archive" --domain oa.example.com --email a@example.com >"$SB/pc.stdout" 2>"$SB/pc.stderr" ); RC=$?
    assert_rc 0 "$SB/pc.stderr"; assert_has "$SB/pc.stdout" "^dir=$SB/oa\$"; assert_has "$SB/pc.stdout" '^port=18096$'; assert_has "$SB/pc.stdout" '^domain=oa.example.com$'; assert_has "$SB/pc.stdout" "^archive_dir=$SB/d/archive\$"; assert_has "$SB/pc.stdout" '^db_mode=bundled$'
    assert_not "$SB/pc.stdout" '(PASS|FOUND|Detection|Installing|\[)'; assert_not "$SB/pc.stdout" '^(db_password|secret)'
    assert_has "$SB/pc.stderr" 'Detection: what is already on this server'; assert_nofile "$SB/oa"; assert_nofile "$SB/etc/install.json"
    assert_eq "$(snap_tree | grep -v -e install.log -e pc.std)" "$(echo "$before" | grep -v install.log)" "print-config changed nothing"; assert_not "$SB/calls.log" 'compose\[|^docker pull'
    # round trip: the printed answers reproduce the settings on "another server"
    cp "$SB/pc.stdout" "$SB/rt.conf"; chmod 600 "$SB/rt.conf"; new_sandbox pc2; mkdir -p "$SB/d"; sed "s|$SB/oa|$SB/oa|" /dev/null; cp "$SB/../pc1/rt.conf" "$SB/rt.conf" 2>/dev/null || true
    sed -i "s|^dir=.*|dir=$SB/oa|; s|^archive_dir=.*|archive_dir=$SB/d/archive|; s|^spool_dir=.*|spool_dir=$SB/oa/data/spool|; s|^app_dir=.*|app_dir=$SB/oa/data/app|; s|^pg_dir=.*|pg_dir=$SB/oa/data/pg|" "$SB/rt.conf"; chmod 600 "$SB/rt.conf"
    run_install --yes --config "$SB/rt.conf"; assert_rc 0; assert_eq "$(envv OA_PORT)" 18096 "roundtrip port"; assert_eq "$(envv OA_DOMAIN)" oa.example.com "roundtrip domain"; assert_eq "$(envv OA_ACME_EMAIL)" a@example.com "roundtrip email"
    # --print-config never writes a database password, even when one is configured
    new_sandbox pc3; printf 'db_password=Sup3rSecretPw\n' >"$SB/pw.conf"; chmod 600 "$SB/pw.conf"
    ( OA_TEST_MODE=1 OA_INSTALL_LOG="$SB/install.log" OA_INSTALL_BIN_DIR="$SB/bin" OA_INSTALL_UNIT_DIR="$SB/units" OA_INSTALL_ETC_MARKER="$SB/etc/install_dir" OA_OS_RELEASE="$SB/os-release" OA_MEMINFO="$SB/meminfo" OA_POLL_SECONDS=0 OA_RETRY_BASE=0 OA_INSTALL_LOCK="$SB/lock" OA_DAEMON_JSON="$SB/daemon.json" OA_TEST_DOCKER_SHIM="$SB/shim/docker" PATH="$SB/shim:$PATH" bash "$SB/repo/deploy/linux/install.sh" --lang en --yes --print-config --dir "$SB/oa" --config "$SB/pw.conf" --db postgres --db-host db.example.com >"$SB/pc.stdout" 2>"$SB/pc.stderr" ); RC=$?
    assert_rc 0 "$SB/pc.stderr"; assert_has "$SB/pc.stdout" '^db_mode=external$'; assert_has "$SB/pc.stdout" '^db_host=db.example.com$'; assert_not "$SB/pc.stdout" 'Sup3rSecretPw'; assert_not "$SB/pc.stderr" 'Sup3rSecretPw'; assert_has "$SB/pc.stdout" 'db_password is deliberately NOT written'
}

t_db_external() {
    local P=Sup3rSecretPw
    # ---- happy path: PostgreSQL on another host, interactive, hidden password, DDL approved at the second confirmation
    new_sandbox de1; make_bundle
    answers "$SB/oa" 18090 '' 2 db.example.com '' '' '' require "$P" '' n '' default '' y y
    tty_install --dir "$SB/oa" --registry "$REG" --image-tag sha-abc1234; assert_rc 0
    assert_has "$SB/out.txt" 'Checking the existing database \(before anything is installed\)'; assert_has "$SB/out.txt" '\[PASS\] Connection: '; assert_has "$SB/out.txt" 'The database passed the checks'
    assert_has "$SB/out.txt" 'would create its schema in db.example.com:5432/oa: 5 migration\(s\), 87 DDL statement\(s\)'; assert_has "$SB/out.txt" 'Create these tables now\?'; assert_has "$SB/state/db-applied" 'applied'
    assert_has "$SB/calls.log" '--entrypoint oa-admin .*/oa-agent-api:sha-abc1234 db-check --password-stdin'; assert_has "$SB/calls.log" 'db-init --password-stdin --apply --plan 0123456789abcdef01234567'
    assert_has "$SB/calls.log" '--add-host host.docker.internal:host-gateway'; assert_has "$SB/calls.log" '-e OA_DB_HOST=db.example.com'
    assert_order "$SB/calls.log" 'db-check' 'db-init --password-stdin --apply'; assert_order "$SB/calls.log" 'db-init --password-stdin --apply' 'compose\[[^]]*\] up -d'
    # the password: only ever on stdin of the check container, only in .env (0600) on disk; never in args, logs, screen, state file
    assert_eq "$(cat "$SB/state/db-stdin-last")" "$P" "password on stdin"; secret_scan "$P"
    assert_has "$SB/oa/.env" "^OA_DB_PASSWORD='$P'\$"; assert_eq "$(mode "$SB/oa/.env")" 600 ".env mode"; assert_has "$SB/oa/.env" '^OA_DB_PROVIDER=postgres$'; assert_has "$SB/oa/.env" '^OA_DB_HOST=db.example.com$'; assert_has "$SB/oa/.env" '^OA_DB_TLS_MODE=require$'
    assert_has "$SB/oa/.env" '^COMPOSE_FILE=docker-compose.yml:docker-compose.mounts.yml:docker-compose.external-db.yml$'; assert_file "$SB/oa/docker-compose.external-db.yml"
    assert_nofile "$SB/oa/data/pg"; assert_has "$SB/out.txt" 'Database: +existing PostgreSQL db.example.com:5432/oa'; assert_has "$SB/out.txt" 'password in .env'
    assert_has "$SB/etc/install.json" '"db":"postgres"'
    # a re-run on this healthy install is the fast path and does not check the database again
    : >"$SB/calls.log"; run_install --yes --dir "$SB/oa"; assert_rc 0; assert_has "$SB/out.txt" 'database external:postgres'; assert_not "$SB/calls.log" 'db-check'
    # ---- the review screen masks the password
    new_sandbox de2; make_bundle; answers "$SB/oa" 18090 '' 3 db.example.com '' '' '' require "$P" '' n '' default '' c
    tty_install --dir "$SB/oa" --registry "$REG"; assert_rc 4; assert_has "$SB/out.txt" 'MySQL db.example.com:3306/oa, user oa, password \*+, TLS require'; secret_scan "$P"; assert_nofile "$SB/oa"
    # ---- wrong password: precise reason, loop, change the settings, succeed
    new_sandbox de3; make_bundle; echo wrongpw >"$SB/state/dbmode"
    answers "$SB/oa" 18090 '' 2 db.example.com '' '' '' require wrongpassword '' n '' default '' y  c db.example.com '' '' '' require right r
    # (after the failed check: c = change settings -> the connection questions again, then retry)
    tty_install --dir "$SB/oa" --registry "$REG"; assert_has "$SB/out.txt" '\[FAIL\] Connection: password authentication failed for user "oa"'; assert_has "$SB/out.txt" 'r\) retry with the same settings   c\) change the database settings   a\) abort'
    assert_nofile "$SB/oa/.env"; assert_not "$SB/calls.log" 'compose\['
    # ---- isolation failure then abort: nothing installed, nothing changed
    new_sandbox de4; make_bundle; echo noiso >"$SB/state/dbmode"; local before; before="$(snap_tree | grep -v -e install.log -e '^./state')"
    answers "$SB/oa" 18090 '' 2 db.example.com '' '' '' require "$P" '' n '' default '' y a
    tty_install --dir "$SB/oa" --registry "$REG"; assert_rc 4; assert_has "$SB/out.txt" '\[FAIL\] Isolation \(dedicated database/schema\)'; assert_has "$SB/out.txt" 'Aborted: nothing was changed'
    assert_nofile "$SB/oa"; assert_nofile "$SB/etc/install.json"; assert_not "$SB/calls.log" 'compose\[|db-init'; assert_eq "$(snap_tree | grep -v -e install.log -e '^./state')" "$before" "abort changed nothing"; secret_scan "$P"
    # ---- DDL declined at the second confirmation: cancelled, DB and host untouched
    new_sandbox de5; make_bundle; answers "$SB/oa" 18090 '' 2 db.example.com '' '' '' require "$P" '' n '' default '' y n
    tty_install --dir "$SB/oa" --registry "$REG"; assert_rc 4; assert_has "$SB/out.txt" 'the database was not changed and nothing was installed'; assert_nofile "$SB/state/db-applied"; assert_nofile "$SB/oa"; assert_not "$SB/calls.log" 'db-init --password-stdin --apply'
    # view the full DDL before deciding
    new_sandbox de6; make_bundle; answers "$SB/oa" 18090 '' 2 db.example.com '' '' '' require "$P" '' n '' default '' y v n
    tty_install --dir "$SB/oa" --registry "$REG"; assert_rc 4; assert_has "$SB/out.txt" 'CREATE TABLE users'; assert_has "$SB/calls.log" 'db-init --password-stdin --show-ddl'
    # ---- localhost is refused (it is the container itself) and re-asked
    new_sandbox de7; make_bundle; answers "$SB/oa" 18090 '' 2 localhost db.example.com '' '' '' require "$P" '' n '' default '' y y
    tty_install --dir "$SB/oa" --registry "$REG"; assert_rc 0; assert_has "$SB/out.txt" 'the database client runs in a container'; assert_has "$SB/out.txt" 'host.docker.internal'
    # invalid passwords are rejected at the prompt and asked again
    new_sandbox de8; make_bundle; answers "$SB/oa" 18090 '' 2 db.example.com '' '' '' require "pass word" "it's" "" "$P" '' n '' default '' y y
    tty_install --dir "$SB/oa" --registry "$REG"; assert_rc 0; assert_has "$SB/out.txt" 'must not contain spaces or the single quote'; assert_has "$SB/out.txt" 'must not be empty'; assert_has "$SB/oa/.env" "^OA_DB_PASSWORD='$P'\$"
    # TLS verify-full asks for the CA, which is copied into the app data dir (readable by uid 10001) and mounted read-only into the check
    new_sandbox de9; make_bundle; printf -- '-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n' >"$SB/ca.pem"
    answers "$SB/oa" 18090 '' 4 db.example.com '' mydb app verify-full "$SB/ca.pem" "$P" '' n '' default '' y y
    tty_install --dir "$SB/oa" --registry "$REG"; assert_rc 0; assert_has "$SB/calls.log" "-v $SB/ca.pem:/oa-db-ca.pem:ro"; assert_has "$SB/calls.log" '-e OA_DB_TLS_CA_FILE=/oa-db-ca.pem'; assert_has "$SB/calls.log" '-e OA_DB_PORT=1433'; assert_has "$SB/calls.log" '-e OA_DB_NAME=mydb'
    assert_file "$SB/oa/data/app/db-ca.pem"; assert_has "$SB/oa/.env" '^OA_DB_TLS_CA_FILE=/data/db-ca.pem$'; assert_has "$SB/oa/.env" '^OA_DB_PROVIDER=mssql$'; assert_has "$SB/oa/.env" '^OA_DB_USER=app$'
}

t_db_unattended() {
    local P=Sup3rSecretPw
    new_sandbox du1; make_bundle
    printf 'db_mode=external\ndb_provider=postgres\ndb_host=db.example.com\ndb_name=oa\ndb_user=oa\ndb_tls=require\ndb_password=%s\n' "$P" >"$SB/db.conf"; chmod 600 "$SB/db.conf"
    run_install --yes --dir "$SB/oa" --registry "$REG" --config "$SB/db.conf"; assert_rc 0
    assert_has "$SB/out.txt" 'The database passed the checks'; assert_has "$SB/out.txt" "doesn't approve DDL|does not approve DDL on your behalf"; assert_nofile "$SB/state/db-applied"; assert_not "$SB/calls.log" 'db-init --password-stdin --apply'
    assert_has "$SB/oa/.env" "^OA_DB_PASSWORD='$P'\$"; secret_scan "$P"
    # password from a separate 0600 file
    new_sandbox du2; make_bundle; printf '%s\n' "$P" >"$SB/pw"; chmod 600 "$SB/pw"
    run_install --yes --dir "$SB/oa" --registry "$REG" --db postgres --db-host db.example.com --db-password-file "$SB/pw"; assert_rc 0; assert_has "$SB/oa/.env" "^OA_DB_PASSWORD='$P'\$"; secret_scan "$P"
    chmod 644 "$SB/pw"; new_sandbox du3; make_bundle; printf '%s\n' "$P" >"$SB/pw"; chmod 644 "$SB/pw"; run_install --yes --dir "$SB/oa" --registry "$REG" --db postgres --db-host db.example.com --db-password-file "$SB/pw"; assert_rc 2; assert_has "$SB/out.txt" 'has mode 644'
    # required answers without a safe default fail with a clear message
    new_sandbox du4; run_install --yes --dir "$SB/oa" --db postgres; assert_rc 2; assert_has "$SB/out.txt" 'invalid host name|database host'; assert_nofile "$SB/oa"
    run_install --yes --dir "$SB/oa" --db postgres --db-host db.example.com; assert_rc 2; assert_has "$SB/out.txt" 'password is missing or invalid'; assert_has "$SB/out.txt" 'db_password=\.\.\. into the --config file'; assert_nofile "$SB/oa"
    run_install --yes --dir "$SB/oa" --db bundled --db-host db.example.com; assert_rc 2; assert_has "$SB/out.txt" 'external-database options'
    printf 'db_password=Sup3rSecretPw\n' >"$SB/p.conf"; chmod 600 "$SB/p.conf"; run_install --yes --dir "$SB/oa" --db postgres --db-host localhost --config "$SB/p.conf"; assert_rc 2; assert_has "$SB/out.txt" 'the database client runs in a container'
    run_install --yes --dir "$SB/oa" --db postgres --db-host db.example.com --db-tls maybe --config "$SB/p.conf"; assert_rc 2; assert_has "$SB/out.txt" 'must be disable, require, verify-ca or verify-full'
    # a failing check is a pre-flight failure (exit 3) and nothing is installed
    new_sandbox du5; make_bundle; echo wrongpw >"$SB/state/dbmode"; run_install --yes --dir "$SB/oa" --registry "$REG" --config "$SB/db.conf" 2>/dev/null; printf 'db_mode=external\ndb_provider=postgres\ndb_host=db.example.com\ndb_password=%s\n' "$P" >"$SB/db.conf"; chmod 600 "$SB/db.conf"
    run_install --yes --dir "$SB/oa" --registry "$REG" --config "$SB/db.conf"; assert_rc 3; assert_has "$SB/out.txt" 'The database check failed'; assert_nofile "$SB/oa/.env"; assert_not "$SB/calls.log" 'compose\['
    # existing OA data in the target database: the original key is required, never a new one
    new_sandbox du6; make_bundle; echo has-data >"$SB/state/dbmode"; printf 'db_mode=external\ndb_provider=postgres\ndb_host=db.example.com\ndb_password=%s\n' "$P" >"$SB/db.conf"; chmod 600 "$SB/db.conf"
    run_install --yes --dir "$SB/oa" --registry "$REG" --config "$SB/db.conf"; assert_rc 3; assert_has "$SB/out.txt" 'already holds OA Agent data'; assert_has "$SB/out.txt" 'A new key is never generated over old data'; assert_nofile "$SB/oa/.env"
    local K; K="$(printf 'ef%.0s' $(seq 32))"; printf '%s\n' "$K" >"$SB/old.key"; chmod 600 "$SB/old.key"
    printf 'secret_key_file=%s/old.key\n' "$SB" >>"$SB/db.conf"; run_install --yes --dir "$SB/oa" --registry "$REG" --config "$SB/db.conf"; assert_rc 0; assert_eq "$(envv OA_SECRET_KEY)" "$K" "original key reused"; assert_has "$SB/out.txt" 'schema is already current'
    # the check mode never pulls an image or touches the database
    new_sandbox du7; make_bundle; printf 'db_password=Sup3rSecretPw\n' >"$SB/p.conf"; chmod 600 "$SB/p.conf"; run_install --check --dir "$SB/oa" --registry "$REG" --config "$SB/p.conf" --db postgres --db-host db.example.com; assert_rc 0; assert_has "$SB/out.txt" 'External database check passed \(read-only\)'; assert_not "$SB/calls.log" '^docker pull|db-init'
}

t_version_matrix() {
    # registry-mode installation with a known tag, then every kind of requested tag
    new_sandbox vm1; make_bundle; run_install --yes --dir "$SB/oa" --registry "$REG" --image-tag 1.2.0; assert_rc 0
    run_install --yes --dir "$SB/oa" --image-tag 1.2.0; assert_rc 0; assert_has "$SB/out.txt" 'all services are healthy'                                  # same tag, healthy: nothing to do
    run_install --yes --dir "$SB/oa" --image-tag 1.3.0; assert_rc 0; assert_has "$SB/out.txt" 'installed image tag is 1.2.0 and stays'                    # newer but no --upgrade: stays
    run_install --yes --dir "$SB/oa" --upgrade --image-tag 1.3.0; assert_rc 0; assert_has "$SB/calls.log" 'pg_dump'                                        # upgrade: backup first
    run_install --yes --dir "$SB/oa" --upgrade --image-tag 1.1.0; assert_rc 2; assert_has "$SB/out.txt" 'Refusing to downgrade: installed 1.3.0, requested 1.1.0'
    run_install --yes --dir "$SB/oa" --upgrade --image-tag 1.2.0-rc.1; assert_rc 2; assert_has "$SB/out.txt" 'Refusing to downgrade'
    run_install --yes --dir "$SB/oa" --upgrade --image-tag 1.1.0 --allow-downgrade; assert_rc 0; assert_has "$SB/out.txt" 'Downgrading 1.3.0 -> 1.1.0 because of --allow-downgrade'
    new_sandbox vm2; make_bundle; run_install --yes --dir "$SB/oa" --registry "$REG" --image-tag 1.2.0
    run_install --yes --dir "$SB/oa" --upgrade --image-tag sha-abc1234; assert_rc 0; assert_has "$SB/out.txt" 'Cannot tell whether sha-abc1234 is newer than 1.2.0'
    new_sandbox vm3; make_bundle; run_install --yes --dir "$SB/oa" --registry "$REG"; run_install --yes --dir "$SB/oa"; assert_rc 0; assert_has "$SB/out.txt" 'all services are healthy'   # mutable same tag: status only
    run_install --yes --dir "$SB/oa" --upgrade; assert_rc 0; assert_has "$SB/out.txt" 'Upgrading'
    # the interactive menu explains the relation
    new_sandbox vm4; make_bundle; run_install --yes --dir "$SB/oa" --registry "$REG" --image-tag 1.2.0; answers 5; tty_install --dir "$SB/oa" --image-tag 1.3.0; assert_rc 4; assert_has "$SB/out.txt" 'upgrade available: 1.2.0 -> 1.3.0'
    answers 5; tty_install --dir "$SB/oa" --image-tag 1.0.0; assert_has "$SB/out.txt" 'older than the installed 1.2.0'; answers 5; tty_install --dir "$SB/oa" --image-tag 1.2.0; assert_has "$SB/out.txt" 'already the requested version 1.2.0'
    answers 5; tty_install --dir "$SB/oa" --image-tag sha-abc1234; assert_has "$SB/out.txt" 'cannot be compared with the installed 1.2.0'
}

t_ctl_external_db() {
    new_sandbox ce1; make_bundle; printf 'db_mode=external\ndb_provider=postgres\ndb_host=db.example.com\ndb_password=Sup3rSecretPw\n' >"$SB/db.conf"; chmod 600 "$SB/db.conf"
    run_install --yes --dir "$SB/oa" --registry "$REG" --config "$SB/db.conf"; assert_rc 0
    : >"$SB/calls.log"; run_ctl status; assert_rc 0; assert_has "$SB/ctl.txt" 'Database: postgres db.example.com:5432/oa'; assert_not "$SB/ctl.txt" ' postgres +(missing|healthy)'
    : >"$SB/calls.log"; echo keep >"$SB/oa/data/archive/doc.txt"; run_ctl backup; assert_rc 0; assert_has "$SB/ctl.txt" 'does NOT contain it'; assert_not "$SB/calls.log" 'pg_dump'
    local f; f="$(ls "$SB"/oa/backups/oa-agent-backup-*.tar.gz | head -n 1)"; tar -tzf "$f" >"$SB/list.txt"; assert_not "$SB/list.txt" 'db\.dump'; assert_has "$SB/list.txt" '^\./env$'; assert_eq "$(tar -xzOf "$f" ./MANIFEST | grep '^db=')" db=external manifest
    run_ctl --yes restore "$f"; assert_rc 0; assert_has "$SB/ctl.txt" 'the database is not restored'; assert_not "$SB/calls.log" 'pg_restore|DROP DATABASE|up -d postgres'
    run_ctl doctor; assert_has "$SB/ctl.txt" 'database: external postgres db.example.com'; assert_not "$SB/ctl.txt" 'cannot connect to the database'
    # a bundled-DB dump cannot be restored into an external-DB installation
    new_sandbox ce2; run_install --yes --dir "$SB/oa"; run_ctl backup; local g; g="$(ls "$SB"/oa/backups/oa-agent-backup-*.tar.gz | head -n 1)"; cp "$g" "$SB/bundled-backup.tar.gz"
    new_sandbox ce3; make_bundle; printf 'db_mode=external\ndb_provider=postgres\ndb_host=db.example.com\ndb_password=Sup3rSecretPw\n' >"$SB/db.conf"; chmod 600 "$SB/db.conf"; run_install --yes --dir "$SB/oa" --registry "$REG" --config "$SB/db.conf"
    run_ctl --yes restore "$SB/../ce2/bundled-backup.tar.gz"; assert_rc_nz
}

t_layer_files() {
    assert_has "$LINUX/docker-compose.external-db.yml" 'host.docker.internal:host-gateway'; assert_has "$LINUX/docker-compose.external-db.yml" 'OA_DB_TLS_CA_FILE'
    assert_has "$ROOT/deploy/docker/Dockerfile.deploy" 'docker-compose.external-db.yml'; assert_has "$LINUX/build-release.sh" 'docker-compose.external-db.yml'
    new_sandbox lf1; run_install --yes --dir "$SB/oa"; assert_rc 0; assert_file "$SB/oa/docker-compose.external-db.yml"
}

t_help_detection_options() {
    new_sandbox hd1; run_install --help; assert_rc 0
    local o; for o in '--config' '--print-config' '--status' '--db bundled\|postgres\|mysql\|mssql' '--db-ca-file' '--behind-proxy' '--install-prereqs' '--adopt-data' '--allow-downgrade' 'DETECTS what this server already has' 'nothing to do'; do assert_has "$SB/out.txt" "$o"; done
    run_install --lang zh --help; assert_rc 0; for o in '--config' '--print-config' '--behind-proxy' '检测' '无需操作'; do assert_has "$SB/out.txt" "$o"; done
    run_install --db oracle; assert_rc 2
}

t_summary_report() {
    NO_DOCKER=1 new_sandbox sr1; run_install --yes --install-docker --dir "$SB/oa"; assert_rc 0
    assert_has "$SB/out.txt" 'What was detected and done'; assert_has "$SB/out.txt" 'installed: Installed Docker Engine and Compose v2'; assert_has "$SB/out.txt" 'State file: +'"$SB"'/etc/install.json'
    assert_has "$SB/out.txt" 'Running this script again'; assert_has "$SB/out.txt" 'Database: +bundled PostgreSQL'
    new_sandbox sr2; run_install --yes --dir "$SB/oa"; assert_has "$SB/out.txt" 'already present, skipped: Docker Engine 26.1.0'; assert_has "$SB/out.txt" 'already present, skipped: Compose plugin 2.29.0'; assert_not "$SB/out.txt" 'installed: Installed Docker'
    # the default closing screen is SHORT; the long form goes to INSTALL-INFO.txt (without the setup token)
    new_sandbox sr3; OA_SUMMARY=compact run_install --yes --dir "$SB/oa" --port 18080; assert_rc 0
    assert_has "$SB/out.txt" 'Installation complete'; assert_has "$SB/out.txt" 'Next:'; assert_has "$SB/out.txt" 'OA_SECRET_KEY'; assert_has "$SB/out.txt" 'INSTALL-INFO.txt'
    assert_not "$SB/out.txt" 'SAVE THESE'; assert_not "$SB/out.txt" 'What was detected and done'
    assert_file "$SB/oa/INSTALL-INFO.txt"; assert_has "$SB/oa/INSTALL-INFO.txt" 'What was detected and done'; assert_not "$SB/oa/INSTALL-INFO.txt" 'SETUP TOKEN'
}


# ---------------------------------------------------------------------------------------------- language + Docker source (follow-up)
run_install_nolang() { # like run_install but WITHOUT --lang: the language comes from the locale / OA_LANG / the question
    OA_TEST_MODE=1 OA_INSTALL_LOG="$SB/install.log" OA_INSTALL_BIN_DIR="$SB/bin" OA_INSTALL_UNIT_DIR="$SB/units" OA_INSTALL_ETC_MARKER="$SB/etc/install_dir" \
        OA_OS_RELEASE="$SB/os-release" OA_MEMINFO="$SB/meminfo" OA_POLL_SECONDS=0 OA_RETRY_BASE=0 OA_INSTALL_LOCK="$SB/lock" OA_DAEMON_JSON="$SB/daemon.json" OA_TEST_DOCKER_SHIM="$SB/shim/docker" \
        OA_SUMMARY="${OA_SUMMARY:-full}" OA_BUILD_FROM_SOURCE="${OA_BUILD_FROM_SOURCE:-1}" PATH="$SB/shim:$PATH" bash "$SB/repo/deploy/linux/install.sh" "$@" >"$SB/out.txt" 2>&1
    RC=$?
}
# lrun LOCALE-ENV... -- args: run without --lang under the given locale variables (all others cleared)
lrun() {
    local envs=() e
    while [ $# -gt 0 ] && [ "$1" != -- ]; do envs+=("$1"); shift; done; shift
    ( unset OA_LANG; export LC_ALL='' LC_MESSAGES='' LANGUAGE='' LANG=''; for e in "${envs[@]}"; do declare -x "$e"; done; run_install_nolang "$@"; echo "$RC" >"$SB/rc" )
    RC="$(cat "$SB/rc")"
}

t_language_default() {
    new_sandbox ld; local c
    for c in zh_CN.UTF-8 ZH_TW zh-Hans zh_HK.utf8; do lrun LANG=$c -- --check --dir "$SB/oa"; assert_rc 0; assert_has "$SB/out.txt" '界面语言：中文'; assert_has "$SB/out.txt" '检测：这台服务器上已经有什么'; done
    for c in en_US.UTF-8 C POSIX C.UTF-8 de_DE.UTF-8; do lrun LANG=$c -- --check --dir "$SB/oa"; assert_rc 0; assert_has "$SB/out.txt" 'Language: English \(from the system locale; change it with --lang zh\|en\)'; done
    lrun LANG=C LANGUAGE=zh_CN:en -- --check --dir "$SB/oa"; assert_has "$SB/out.txt" '界面语言：中文'
    lrun LANG=zh_CN.UTF-8 LC_ALL=en_US.UTF-8 -- --check --dir "$SB/oa"; assert_has "$SB/out.txt" 'Language: English'
    printf 'LANG=zh_CN.UTF-8\n' >"$SB/locale.default"; OA_TEST_LOCALE_FILES="$SB/locale.default" lrun LANG=C -- --check --dir "$SB/oa"; assert_has "$SB/out.txt" '界面语言：中文'
    printf 'LANG="C.UTF-8"\n' >"$SB/locale.default"; OA_TEST_LOCALE_FILES="$SB/locale.default" lrun LANG=C -- --check --dir "$SB/oa"; assert_has "$SB/out.txt" 'Language: English'
    # explicit choices skip the message and the question
    lrun LANG=zh_CN.UTF-8 -- --check --lang en --dir "$SB/oa"; assert_has "$SB/out.txt" 'Detection: what is already on this server'; assert_not "$SB/out.txt" '界面语言|Language: '
    lrun LANG=C OA_LANG=zh -- --check --dir "$SB/oa"; assert_has "$SB/out.txt" '检测：这台服务器上已经有什么'; assert_not "$SB/out.txt" 'Language: '
    lrun LANG=C -- --check --lang xx --dir "$SB/oa"; assert_rc 2
    # --help follows --lang and the locale
    lrun LANG=zh_CN.UTF-8 -- --help; assert_has "$SB/out.txt" '用法: install.sh'; lrun LANG=C -- --help; assert_has "$SB/out.txt" 'Usage: install.sh'; lrun LANG=C -- --lang zh --help; assert_has "$SB/out.txt" '用法: install.sh'
}

t_language_question() {
    # interactive, no --lang: the FIRST thing asked, default from the locale, read from the terminal device
    new_sandbox lq1; answers '' "$SB/oa" 18090 '' 1 '' n '' default y; OA_INSTALL_TTY="$SB/answers" lrun LANG=zh_CN.UTF-8 OA_INSTALL_TTY="$SB/answers" -- --dir "$SB/oa" --port 18090; assert_rc 0
    assert_has "$SB/out.txt" '请选择语言 / Select language:'; assert_order "$SB/out.txt" 'Select language' '企业 OA 文件智能归档'; assert_has "$SB/out.txt" '配置向导'; assert_has "$SB/out.txt" '请复核全部设置'; assert_eq "$(envv OA_LANG)" zh "OA_LANG in .env"
    new_sandbox lq2; answers 2 "$SB/oa" 18090 '' 1 '' n '' default y; lrun LANG=zh_CN.UTF-8 OA_INSTALL_TTY="$SB/answers" -- --dir "$SB/oa"; assert_rc 0; assert_has "$SB/out.txt" 'Configuration wizard'; assert_has "$SB/out.txt" 'Review your settings'; assert_not "$SB/out.txt" '配置向导'; assert_eq "$(envv OA_LANG)" en "OA_LANG en"
    new_sandbox lq3; answers '' "$SB/oa" 18090 '' 1 '' n '' default y; lrun LANG=C OA_INSTALL_TTY="$SB/answers" -- --dir "$SB/oa"; assert_rc 0; assert_has "$SB/out.txt" 'Configuration wizard'
    new_sandbox lq4; answers x 3 中文 "$SB/oa" 18090 '' 1 '' n '' default y; lrun LANG=C OA_INSTALL_TTY="$SB/answers" -- --dir "$SB/oa"; assert_rc 0; assert_has "$SB/out.txt" '请输入 1 或 2 / enter 1 or 2'; assert_has "$SB/out.txt" '配置向导'
    # explicit flag or OA_LANG: the question is not asked
    new_sandbox lq5; answers "$SB/oa" 18090 '' 1 '' n '' default y; lrun LANG=C OA_INSTALL_TTY="$SB/answers" -- --lang en --dir "$SB/oa"; assert_not "$SB/out.txt" 'Select language'
    # the review screen can change the language (setting 9) and everything after it follows
    new_sandbox lq6; answers "$SB/oa" 18090 '' 1 '' n '' default e 9 1 y; run_install --dir "$SB/oa" 2>/dev/null; OA_INSTALL_TTY="$SB/answers" run_install --dir "$SB/oa"; assert_rc 0; assert_has "$SB/out.txt" '9\) Language'; assert_has "$SB/out.txt" '请选择语言 / Select language:'; assert_has "$SB/out.txt" '安装完成'
    # no terminal and no --yes: the refusal message is in the locale language
    new_sandbox lq7; lrun LANG=zh_CN.UTF-8 OA_INSTALL_TTY=/nonexistent -- --dir "$SB/oa" </dev/null; assert_rc 2; assert_has "$SB/out.txt" '没有可用的终端来回答安装问题'
    # oa-agentctl.sh follows the language stored in .env (OA_LANG in the environment wins)
    new_sandbox lq8; answers 1 "$SB/oa" 18090 '' 1 '' n '' default y; lrun LANG=C OA_INSTALL_TTY="$SB/answers" -- --dir "$SB/oa"; assert_rc 0
    env -u OA_LANG LANG=C LC_ALL= PATH="$SB/shim:$PATH" bash "$SB/bin/oa-agentctl.sh" --dir "$SB/oa" status >"$SB/ctl.txt" 2>&1; assert_has "$SB/ctl.txt" '数据库'
    OA_LANG=en PATH="$SB/shim:$PATH" bash "$SB/bin/oa-agentctl.sh" --dir "$SB/oa" status >"$SB/ctl.txt" 2>&1; assert_has "$SB/ctl.txt" 'Database:'
}

# lang_leaks FILE LANG: leftover other-language text. en: any CJK character. zh: lines of 4+ English words without any CJK (minus commands,
# URLs, config snippets and product/help lines that are meant to stay as they are)
lang_leaks() {
    if [ "$2" = en ]; then LC_ALL=C.UTF-8 grep -nP '[\x{4e00}-\x{9fff}\x{3000}-\x{303f}\x{ff00}-\x{ffef}]' "$1" | grep -v -e 'Select language' -e 'incomplete (truncated' || true
    else LC_ALL=C.UTF-8 grep -nP '^(?!.*[\x{4e00}-\x{9fff}])(?=.*\b[A-Za-z]{2,}\b \b[A-Za-z]{2,}\b \b[A-Za-z]{2,}\b \b[A-Za-z]{2,}\b)' "$1" \
        | grep -v -E 'https?://|sudo |docker |oa-agentctl|install\.sh|^[0-9]+:\s*(server|listen|location|client_max|proxy_|reverse_proxy|flush_interval|transport|response_header|#|\}|\{)|^[0-9]+:\s+(ssl_|\$DOMAIN)|^[0-9]+:\s*[a-z_]+ ' || true; fi
}
t_language_clean() {
    local L f
    for L in en zh; do
        # one full wizard run (bundled database, a proxy-held 80/443, mirror hint, docker missing -> install) + the other screens
        NO_DOCKER=1 new_sandbox lc_$L; make_bundle
        answers "$SB/oa" 18090 oa.example.com 1 1 '' n '' cn y 1 "" 1 y
        FAKE_PORTS='80=nginx 443=nginx' FAKE_NO_HUB=1 OA_INSTALL_TTY="$SB/answers" run_install --lang "$L" --dir "$SB/oa" --install-docker --registry "$REG" --image-tag sha-abc1234; assert_rc 0
        f="$SB/out.txt"; assert_eq "$(lang_leaks "$f" "$L" | head -n 5)" '' "leftover text in the $L wizard run"
        for a in "--check --dir $SB/oa3" "--status --dir $SB/oa" "--yes --dir $SB/oa"; do
            # shellcheck disable=SC2086
            run_install --lang "$L" $a; f="$SB/out.txt"; assert_eq "$(lang_leaks "$f" "$L" | head -n 5)" '' "leftover text in $L: $a"
        done
        # the existing-install menu and the external database path
        answers 5; tty_install --lang "$L" --dir "$SB/oa" 2>/dev/null; OA_INSTALL_TTY="$SB/answers" run_install --lang "$L" --dir "$SB/oa"; assert_eq "$(lang_leaks "$SB/out.txt" "$L" | head -n 5)" '' "leftover text in the $L menu"
        new_sandbox lc2_$L; make_bundle; answers "$SB/oa" 18090 '' 2 db.example.com '' '' '' require Sup3rSecretPw '' n '' default '' '' y y
        OA_INSTALL_TTY="$SB/answers" run_install --lang "$L" --dir "$SB/oa" --registry "$REG"; assert_rc 0; assert_has "$SB/calls.log" "-e OA_LANG=$L"
        assert_eq "$(lang_leaks "$SB/out.txt" "$L" | head -n 5)" '' "leftover text in the $L external-database run"
        new_sandbox lc3_$L; run_install --lang "$L" --yes --dir "$SB/oa" --behind-proxy --domain oa.example.com; assert_eq "$(lang_leaks "$SB/out.txt" "$L" | head -n 5)" '' "leftover text in the $L proxy summary"
    done
}

t_docker_source() {
    # validation and the legacy alias
    new_sandbox ds0; run_install --yes --dir "$SB/oa" --docker-source 'ftp://x'; assert_rc 2; run_install --yes --dir "$SB/oa" --docker-source 'http://mirror.example.com/docker-ce'; assert_rc 2; run_install --yes --dir "$SB/oa" --mirror foo; assert_rc 2
    # every explicit source: the exact command line and DOWNLOAD_URL/--mirror mechanics (checked against the get.docker.com script text);
    # an explicit source never asks the country lookup anything
    local spec tok want url
    for spec in 'official||' 'aliyun|--mirror Aliyun|' 'azure|--mirror AzureChinaCloud|' 'tuna||https://mirrors.tuna.tsinghua.edu.cn/docker-ce' 'ustc||https://mirrors.ustc.edu.cn/docker-ce' 'tencent||https://mirrors.cloud.tencent.com/docker-ce' 'https://docker.example.com/docker-ce||https://docker.example.com/docker-ce'; do
        tok="${spec%%|*}"; want="${spec#*|}"; url="${want#*|}"; want="${want%%|*}"
        NO_DOCKER=1 new_sandbox "ds_${tok//[^a-z]/}"; FAKE_GEO=CN run_install --yes --install-docker --dir "$SB/oa" --docker-source "$tok"; assert_rc 0
        assert_has "$SB/calls.log" "^get-docker.sh ${want} ?DOWNLOAD_URL=$url\$"; assert_has "$SB/out.txt" 'Running: .*sh get-docker.sh'; assert_has "$SB/out.txt" 'from https://get.docker.com'; assert_has "$SB/out.txt" 'third-party'
        assert_not "$SB/calls.log" 'ipinfo.io'; assert_count "$SB/calls.log" '^get-docker.sh' 1
        [ -z "$url" ] || assert_has "$SB/out.txt" "Running: DOWNLOAD_URL=$url sh get-docker.sh"
    done
    # explicit source failing: no silent source hopping under --yes, the distribution packages follow as before
    NO_DOCKER=1 new_sandbox ds1; FAKE_GETDOCKER_BADMIRROR=1 run_install --yes --install-docker --dir "$SB/oa" --docker-source aliyun; assert_rc 0
    assert_count "$SB/calls.log" '^get-docker.sh' 1; assert_has "$SB/calls.log" '^apt-get install'; assert_has "$SB/out.txt" 'The installation source aliyun failed'
    # explicit source failing, interactive: asked whether to try the others; yes -> they are tried, no -> distribution packages
    NO_DOCKER=1 new_sandbox ds4; answers "$SB/oa" 18090 '' 1 '' n '' default y y y; FAKE_GETDOCKER_BADMIRROR=1 OA_INSTALL_TTY="$SB/answers" run_install --dir "$SB/oa" --install-docker --docker-source tuna; assert_rc 0
    assert_has "$SB/out.txt" 'Try the other built-in installation sources in turn'; assert_has "$SB/calls.log" 'get-docker.sh --mirror AzureChinaCloud'
    NO_DOCKER=1 new_sandbox ds5; answers "$SB/oa" 18090 '' 1 '' n '' default y n y; FAKE_GETDOCKER_BADMIRROR=1 OA_INSTALL_TTY="$SB/answers" run_install --dir "$SB/oa" --install-docker --docker-source tuna; assert_rc 0
    assert_count "$SB/calls.log" '^get-docker.sh' 1; assert_has "$SB/calls.log" '^apt-get install'
    # config file, environment and print-config
    NO_DOCKER=1 new_sandbox ds10; printf 'docker_source=ustc\ninstall_docker=yes\n' >"$SB/d.conf"; chmod 600 "$SB/d.conf"; run_install --yes --dir "$SB/oa" --config "$SB/d.conf"; assert_rc 0; assert_has "$SB/calls.log" 'DOWNLOAD_URL=https://mirrors.ustc.edu.cn/docker-ce'
    NO_DOCKER=1 new_sandbox ds11; OA_INSTALL_DOCKER_SOURCE=aliyun run_install --yes --install-docker --dir "$SB/oa"; assert_rc 0; assert_has "$SB/calls.log" 'get-docker.sh --mirror Aliyun'
    # the review screen edit (setting 8): menu, custom URL validation, then the consent shows the exact command
    NO_DOCKER=1 new_sandbox ds6; answers "$SB/oa" 18090 '' 1 '' n '' default e 8 4 1 y y
    OA_INSTALL_TTY="$SB/answers" run_install --dir "$SB/oa"; assert_rc 0
    assert_has "$SB/out.txt" 'Docker installation source \(the default, automatic'; assert_has "$SB/out.txt" '8\) automatic \(default\)'
    assert_has "$SB/out.txt" '8\) Docker +will be installed; Docker installation source: as you specified: Tsinghua TUNA'
    assert_has "$SB/out.txt" 'About to run \(needs network and root\): DOWNLOAD_URL=https://mirrors.tuna.tsinghua.edu.cn/docker-ce sh get-docker.sh'; assert_has "$SB/calls.log" 'get-docker.sh  ?DOWNLOAD_URL=https://mirrors.tuna.tsinghua.edu.cn/docker-ce'
    NO_DOCKER=1 new_sandbox ds7; answers "$SB/oa" 18090 '' 1 '' n '' default e 8 7 'http://x.example.com' 'https://bad host' 'https://docker.example.com/docker-ce' 1 y y y
    OA_INSTALL_TTY="$SB/answers" run_install --dir "$SB/oa"; assert_rc 0; assert_count "$SB/out.txt" 'an https:// URL is required' 2; assert_has "$SB/calls.log" 'DOWNLOAD_URL=https://docker.example.com/docker-ce'
    NO_DOCKER=1 new_sandbox ds8; answers "$SB/oa" 18090 '' 1 '' n '' default y n; OA_INSTALL_TTY="$SB/answers" run_install --dir "$SB/oa"; assert_rc 4; assert_not "$SB/calls.log" 'get-docker'
    # a normal run asks NO Docker question; Docker present: the review says so
    new_sandbox ds9; answers "$SB/oa" 18090 '' 1 '' n '' default y; tty_install --dir "$SB/oa"; assert_rc 0; assert_not "$SB/out.txt" 'Installation source \('; assert_has "$SB/out.txt" '8\) Docker +Docker is already installed and kept'
    NO_DOCKER=1 new_sandbox ds12; run_install --check --dir "$SB/oa" --docker-source tuna; assert_rc 0; assert_has "$SB/out.txt" 'would be installed \(source\(s\) tuna: DOWNLOAD_URL=https://mirrors.tuna.tsinghua.edu.cn/docker-ce sh get-docker.sh'; assert_not "$SB/calls.log" 'get-docker'
}

# the 1Panel-style automatic choice: detect (country lookup, then local signals), announce in one line, probe the mirrors, try them in order of speed
t_docker_auto() {
    local c
    # CN: all mirrors reachable -> aliyun first (speed ties keep the preference order), the exact chain on failure, official last
    NO_DOCKER=1 new_sandbox da1; FAKE_GEO=CN FAKE_GETDOCKER_BADMIRROR=1 run_install --yes --install-docker --dir "$SB/oa"; assert_rc 0
    assert_has "$SB/out.txt" 'Docker installation source: automatic: mainland China detected \(ipinfo.io says country CN\) -> reachable mirrors tried by speed: aliyun azure tencent tuna ustc official'
    assert_has "$SB/calls.log" 'curl .*https://ipinfo.io/country'; assert_order "$SB/calls.log" '^get-docker.sh --mirror Aliyun' '^get-docker.sh --mirror AzureChinaCloud'; assert_count "$SB/calls.log" '^get-docker.sh' 2; assert_not "$SB/calls.log" '^apt-get install'
    assert_not "$SB/out.txt" 'Installation source \('; assert_not "$SB/out.txt" 'Choice \['
    run_install --lang zh --check --dir "$SB/oa2"; NO_DOCKER=1 new_sandbox da1z; FAKE_GEO=CN run_install --lang zh --check --dir "$SB/oa"; assert_has "$SB/out.txt" 'Docker 安装源：自动：检测到中国大陆（ipinfo.io 返回国家 CN）→ 按速度依次尝试可达的镜像：aliyun azure tencent tuna ustc official'
    # a faster mirror comes first; an unreachable one is dropped from the chain
    NO_DOCKER=1 new_sandbox da2; FAKE_GEO=CN FAKE_MIRROR_FAST=tsinghua FAKE_MIRROR_DOWN=aliyun run_install --yes --install-docker --dir "$SB/oa"; assert_rc 0
    assert_has "$SB/out.txt" 'tried by speed: tuna azure tencent ustc official'; assert_has "$SB/calls.log" 'get-docker.sh  ?DOWNLOAD_URL=https://mirrors.tuna.tsinghua.edu.cn/docker-ce'; assert_not "$SB/calls.log" '^get-docker.sh --mirror Aliyun'
    # no mirror answers: official source, said so
    NO_DOCKER=1 new_sandbox da3; FAKE_GEO=CN FAKE_MIRROR_DOWN='aliyun azure tencent tsinghua ustc' run_install --yes --install-docker --dir "$SB/oa"; assert_rc 0; assert_has "$SB/out.txt" 'but no mirror answered -> official source'; assert_has "$SB/calls.log" '^get-docker.sh  ?DOWNLOAD_URL=$'
    # non-CN: official, and the mirrors are not even probed
    NO_DOCKER=1 new_sandbox da4; FAKE_GEO=US run_install --yes --install-docker --dir "$SB/oa"; assert_rc 0; assert_has "$SB/out.txt" 'automatic: ipinfo.io says country US -> official source download.docker.com'; assert_not "$SB/calls.log" 'docker-ce/linux'; assert_count "$SB/calls.log" '^get-docker.sh' 1
    # lookup timed out / blocked: local signals. Official reachable and nothing says China -> official
    for c in timeout blocked; do
        NO_DOCKER=1 new_sandbox "da5$c"; FAKE_GEO=$c OA_TEST_TZ=UTC run_install --yes --install-docker --dir "$SB/oa"; assert_rc 0; assert_has "$SB/out.txt" 'country unknown \(lookup failed, blocked or disabled\), no local sign of mainland China'; assert_has "$SB/calls.log" '^get-docker.sh  ?DOWNLOAD_URL=$'
        # ... time zone Asia/Shanghai -> CN
        NO_DOCKER=1 new_sandbox "da6$c"; FAKE_GEO=$c OA_TEST_TZ=Asia/Shanghai run_install --yes --install-docker --dir "$SB/oa"; assert_rc 0; assert_has "$SB/out.txt" 'mainland China detected \(time zone Asia/Shanghai\)'; assert_has "$SB/calls.log" '^get-docker.sh --mirror Aliyun'
        # ... Aliyun ECS region -> CN
        NO_DOCKER=1 new_sandbox "da7$c"; FAKE_GEO=$c OA_TEST_TZ=UTC FAKE_ECS_REGION=cn-shanghai run_install --yes --install-docker --dir "$SB/oa"; assert_rc 0; assert_has "$SB/out.txt" 'mainland China detected \(Alibaba Cloud ECS region cn-shanghai\)'
        # ... download.docker.com unreachable -> CN-like
        NO_DOCKER=1 new_sandbox "da8$c"; FAKE_GEO=$c OA_TEST_TZ=UTC FAKE_OFFICIAL_DOWN=1 run_install --yes --install-docker --dir "$SB/oa"; assert_rc 0; assert_has "$SB/out.txt" 'mainland China detected \(download.docker.com is unreachable within 3 seconds\)'
    done
    # --no-geo-lookup / OA_INSTALL_GEO=off / config: no ipinfo request at all (the third-party lookup), local signals only
    NO_DOCKER=1 new_sandbox da9; FAKE_GEO=CN OA_TEST_TZ=UTC run_install --yes --install-docker --dir "$SB/oa" --no-geo-lookup; assert_rc 0; assert_not "$SB/calls.log" 'ipinfo.io'; assert_has "$SB/out.txt" 'the ipinfo.io lookup is off: --no-geo-lookup'; assert_has "$SB/calls.log" '^get-docker.sh  ?DOWNLOAD_URL=$'
    NO_DOCKER=1 new_sandbox da10; FAKE_GEO=CN OA_TEST_TZ=UTC OA_INSTALL_GEO=off run_install --yes --install-docker --dir "$SB/oa"; assert_rc 0; assert_not "$SB/calls.log" 'ipinfo.io'
    NO_DOCKER=1 new_sandbox da11; printf 'geo_lookup=no\n' >"$SB/g.conf"; chmod 600 "$SB/g.conf"; FAKE_GEO=CN OA_TEST_TZ=UTC run_install --yes --install-docker --dir "$SB/oa" --config "$SB/g.conf"; assert_not "$SB/calls.log" 'ipinfo.io'
    # overrides win over the detection: explicit official under a CN verdict; --mirror cn forces CN without any lookup
    NO_DOCKER=1 new_sandbox da12; FAKE_GEO=CN run_install --yes --install-docker --dir "$SB/oa" --docker-source official; assert_rc 0; assert_not "$SB/calls.log" 'ipinfo.io|docker-ce/linux'; assert_has "$SB/out.txt" 'as you specified: official download.docker.com'
    NO_DOCKER=1 new_sandbox da13; FAKE_GEO=US run_install --yes --install-docker --dir "$SB/oa" --mirror cn; assert_rc 0; assert_not "$SB/calls.log" 'ipinfo.io'; assert_has "$SB/out.txt" 'mainland China detected \(--mirror cn\)'; assert_has "$SB/calls.log" '^get-docker.sh --mirror Aliyun'
    NO_DOCKER=1 new_sandbox da14; FAKE_GEO=CN run_install --yes --install-docker --dir "$SB/oa" --docker-source auto; assert_rc 0; assert_has "$SB/out.txt" 'mainland China detected'
    # Docker already installed: no detection, no announcement, no lookup
    new_sandbox da15; FAKE_GEO=CN run_install --yes --dir "$SB/oa"; assert_rc 0; assert_not "$SB/calls.log" 'ipinfo.io'; assert_not "$SB/out.txt" 'Docker installation source:'
    # the wizard asks nothing about it but shows the automatic choice, editable, in the review
    NO_DOCKER=1 new_sandbox da16; answers "$SB/oa" 18090 '' 1 '' n '' default y y; FAKE_GEO=CN OA_INSTALL_TTY="$SB/answers" run_install --dir "$SB/oa" --install-docker; assert_rc 0
    assert_has "$SB/out.txt" '8\) Docker +will be installed; Docker installation source: automatic: mainland China detected'; assert_not "$SB/out.txt" 'Choice \[1-8\]'
    run_install --lang zh --check --dir "$SB/x" 2>/dev/null
}

# Docker Hub registry mirrors in daemon.json: automatic ONLY when this run installed Docker in mainland China (probed on /v2/); otherwise consent
t_docker_registry_mirror_auto() {
    local M=https://mirror.example.com
    # fresh Docker + CN: probed candidates are written directly and Docker is restarted, no question
    NO_DOCKER=1 new_sandbox rm1; FAKE_GEO=CN run_install --yes --install-docker --dir "$SB/oa"; assert_rc 0
    assert_has "$SB/daemon.json" 'https://docker.1ms.run'; assert_has "$SB/daemon.json" 'https://docker.m.daocloud.io'; assert_has "$SB/daemon.json" 'https://docker.1panel.live'; assert_eq "$(mode "$SB/daemon.json")" 644 "daemon.json mode"
    assert_has "$SB/calls.log" '^systemctl restart docker'; assert_has "$SB/calls.log" 'curl .*https://docker.1ms.run/v2/'; assert_has "$SB/out.txt" 'these Docker Hub mirrors answered and are written to'; assert_has "$SB/out.txt" 'changed: Wrote the Docker Hub mirrors that answered'
    # only the answering ones are written
    NO_DOCKER=1 new_sandbox rm2; FAKE_GEO=CN FAKE_RMIRROR_DOWN='1ms daocloud' run_install --yes --install-docker --dir "$SB/oa"; assert_rc 0; assert_has "$SB/daemon.json" 'docker.1panel.live'; assert_not "$SB/daemon.json" '1ms|daocloud'
    # none answers: nothing written, manual hint printed
    NO_DOCKER=1 new_sandbox rm3; FAKE_GEO=CN FAKE_RMIRROR_DOWN='1ms daocloud 1panel tencentyun' run_install --yes --install-docker --dir "$SB/oa"; assert_rc 0; assert_nofile "$SB/daemon.json"; assert_has "$SB/out.txt" 'none of the built-in Docker Hub mirrors answered'; assert_has "$SB/out.txt" 'registry-mirrors'
    # not CN, or Docker was already there: nothing automatic
    NO_DOCKER=1 new_sandbox rm4; FAKE_GEO=US run_install --yes --install-docker --dir "$SB/oa"; assert_nofile "$SB/daemon.json"
    new_sandbox rm5; FAKE_GEO=CN run_install --yes --dir "$SB/oa" --mirror cn; assert_nofile "$SB/daemon.json"
    # a daemon.json that already has content goes through the consent flow: --yes changes nothing; interactive y/y-restart-n merges it
    NO_DOCKER=1 new_sandbox rm6; printf '{"log-driver": "json-file"}\n' >"$SB/daemon.json"; cp "$SB/daemon.json" "$SB/daemon.orig"; FAKE_GEO=CN run_install --yes --install-docker --dir "$SB/oa"; assert_rc 0; cmp -s "$SB/daemon.json" "$SB/daemon.orig" && ok_ || bad_ "a non-trivial daemon.json changed under --yes"; assert_has "$SB/out.txt" 'never edits daemon.json'
    # explicit --docker-mirror stays the explicit override (consent flow, nothing automatic)
    NO_DOCKER=1 new_sandbox rm7; FAKE_GEO=CN run_install --yes --install-docker --dir "$SB/oa" --docker-mirror "$M"; assert_rc 0; assert_nofile "$SB/daemon.json"; assert_not "$SB/calls.log" 'curl .*docker.1ms.run'
    # the review screen (setting 8) can enter mirrors; invalid URLs are re-asked; consent before daemon.json is written
    new_sandbox rm8; answers "$SB/oa" 18090 '' 1 '' n '' default e 8 2 'ftp://x' "$M https://mirror2.example.com" y y n; tty_install --dir "$SB/oa"; assert_rc 0
    assert_has "$SB/out.txt" 'not a valid http\(s\):// URL: ftp://x'; assert_has "$SB/out.txt" "Docker Hub mirror $M https://mirror2.example.com"; assert_has "$SB/daemon.json" "\"$M\""; assert_not "$SB/calls.log" '^systemctl restart docker'
    new_sandbox rm9; printf '{"log-driver": "json-file"}\n' >"$SB/daemon.json"; cp "$SB/daemon.json" "$SB/daemon.orig"; answers "$SB/oa" 18090 '' 1 '' n '' default e 8 2 "$M" y n; tty_install --dir "$SB/oa"; assert_rc 0; cmp -s "$SB/daemon.json" "$SB/daemon.orig" && ok_ || bad_ "daemon.json changed without consent"
    # config / print-config keys: lang, docker_source, docker_mirrors, geo_lookup (no secrets)
    new_sandbox rm10; ( OA_TEST_MODE=1 OA_INSTALL_LOG="$SB/install.log" OA_INSTALL_BIN_DIR="$SB/bin" OA_INSTALL_UNIT_DIR="$SB/units" OA_INSTALL_ETC_MARKER="$SB/etc/install_dir" OA_OS_RELEASE="$SB/os-release" OA_MEMINFO="$SB/meminfo" OA_POLL_SECONDS=0 OA_RETRY_BASE=0 OA_INSTALL_LOCK="$SB/lock" OA_DAEMON_JSON="$SB/daemon.json" OA_TEST_DOCKER_SHIM="$SB/shim/docker" PATH="$SB/shim:$PATH" bash "$SB/repo/deploy/linux/install.sh" --lang zh --yes --print-config --dir "$SB/oa" --docker-source tuna --docker-mirror "$M" >"$SB/pc.stdout" 2>"$SB/pc.stderr" ); RC=$?
    assert_rc 0 "$SB/pc.stderr"; assert_has "$SB/pc.stdout" '^lang=zh$'; assert_has "$SB/pc.stdout" '^docker_source=tuna$'; assert_has "$SB/pc.stdout" "^docker_mirrors=$M\$"
    # registry_help names the Docker Hub base images and the mirror options
    new_sandbox rm11; FAKE_PULL_FAIL=1 FAKE_COMPOSE_PULL_FAIL=1 run_install --yes --dir "$SB/oa" --registry "$REG" --image-tag nope; assert_rc_nz; assert_has "$SB/out.txt" 'the postgres/caddy base images come from Docker Hub by default'
}

# ---------------------------------------------------------------------------------------------- Docker install: progress, timeout, lock, environment
t_docker_progress() {
    mkdir -p /tmp/oa-hb-fixture 2>/dev/null; local fx
    # heartbeat: a silent install shows elapsed time, the active process and whether the apt download directory is moving
    NO_DOCKER=1 new_sandbox hb1; fx="$SB/fx"; mkdir -p "$fx/partial"; printf 'x\n' >"$fx/dpkg.log"
    FAKE_GETDOCKER_SLEEP=2.5 OA_HEARTBEAT=1 OA_APT_PARTIAL="$fx/partial" OA_DPKG_LOG="$fx/dpkg.log" FAKE_PS_PROCS='42 00:03 sh /x/get-docker.sh --mirror Aliyun' FAKE_PS_LATE='43 00:02 apt-get -y -qq install docker-ce docker-ce-cli' FAKE_PS_LATE_FROM=2 \
        run_install --yes --install-docker --dir "$SB/oa" --docker-source aliyun; assert_rc 0
    assert_has "$SB/out.txt" 'Still installing Docker\.\.\. [12] s elapsed; current process: '; assert_has "$SB/out.txt" 'apt download directory [0-9]+ KB \((unchanged|growing)\)'; assert_has "$SB/out.txt" 'dpkg log last written [0-9]+ s ago'
    assert_has "$SB/out.txt" 'current process: .*(apt-get -y -qq install docker-ce|get-docker.sh)'
    # the download directory growing is reported as growing
    NO_DOCKER=1 new_sandbox hb2; fx="$SB/fx"; mkdir -p "$fx/partial"; ( sleep 1.2; head -c 300000 /dev/zero >"$fx/partial/pkg.deb" ) &
    FAKE_GETDOCKER_SLEEP=3 OA_HEARTBEAT=1 OA_APT_PARTIAL="$fx/partial" run_install --yes --install-docker --dir "$SB/oa" --docker-source official; assert_rc 0; wait; assert_has "$SB/out.txt" 'apt download directory [0-9]+ KB \(growing\)'
    # Chinese heartbeat
    NO_DOCKER=1 new_sandbox hb3; FAKE_GETDOCKER_SLEEP=2.5 OA_HEARTBEAT=1 run_install --lang zh --yes --install-docker --dir "$SB/oa" --docker-source official; assert_rc 0; assert_has "$SB/out.txt" '仍在安装 Docker…已用 [12] 秒；当前进程：'
    # a fast install prints no heartbeat
    NO_DOCKER=1 new_sandbox hb4; run_install --yes --install-docker --dir "$SB/oa"; assert_rc 0; assert_not "$SB/out.txt" 'Still installing Docker'
}

t_docker_timeout() {
    local fx
    # --yes: the child's process group is stopped, diagnostics are printed, the installer fails with the hints (exit 1), nothing else is killed
    NO_DOCKER=1 new_sandbox to1; fx="$SB/fx"; mkdir -p "$fx"; printf 'dpkg-line-one\ndpkg-last-line\n' >"$fx/dpkg.log"; printf 'term-last-line\n' >"$fx/term.log"
    OA_DOCKER_TIMEOUT=2 FAKE_GETDOCKER_HANG=1 OA_DPKG_LOG="$fx/dpkg.log" OA_APT_TERM_LOG="$fx/term.log" run_install --yes --install-docker --dir "$SB/oa" --docker-source official; assert_rc 1
    assert_has "$SB/out.txt" 'ran longer than 2 s; stopping its process group'; assert_has "$SB/out.txt" 'Diagnostics \(after the timeout\)'; assert_has "$SB/out.txt" 'dpkg-last-line'; assert_has "$SB/out.txt" 'term-last-line'; assert_has "$SB/out.txt" 'who holds the installation lock'
    assert_has "$SB/out.txt" 'The Docker installation timed out \(2 s\)'; assert_has "$SB/out.txt" 'OA_DOCKER_TIMEOUT=<seconds>'; assert_nofile "$SB/oa/.env"
    pgrep -fx 'sleep 987' >/dev/null 2>&1 && bad_ "the hung child was not stopped" || ok_
    # interactive: n = next source (the hang happens once), r = retry the same source, a = abort (default)
    NO_DOCKER=1 new_sandbox to2; answers "$SB/oa" 18090 '' 1 '' n '' default y n; OA_DOCKER_TIMEOUT=2 FAKE_GETDOCKER_HANG_ONCE=1 OA_INSTALL_TTY="$SB/answers" run_install --dir "$SB/oa" --install-docker --docker-source aliyun; assert_rc 0
    assert_has "$SB/out.txt" '\[r\]etry the same source / \[n\]ext source / \[a\]bort'; assert_has "$SB/calls.log" 'get-docker.sh --mirror Aliyun'
    NO_DOCKER=1 new_sandbox to3; answers "$SB/oa" 18090 '' 1 '' n '' default y r; OA_DOCKER_TIMEOUT=2 FAKE_GETDOCKER_HANG_ONCE=1 OA_INSTALL_TTY="$SB/answers" run_install --dir "$SB/oa" --install-docker --docker-source aliyun; assert_rc 0; assert_count "$SB/calls.log" '^get-docker.sh --mirror Aliyun' 2
    NO_DOCKER=1 new_sandbox to4; answers "$SB/oa" 18090 '' 1 '' n '' default y; OA_DOCKER_TIMEOUT=2 FAKE_GETDOCKER_HANG=1 OA_INSTALL_TTY="$SB/answers" run_install --dir "$SB/oa" --install-docker --docker-source aliyun; assert_rc 4; assert_has "$SB/out.txt" 'Aborted: the Docker installation timed out'
    # Chinese
    NO_DOCKER=1 new_sandbox to5; OA_DOCKER_TIMEOUT=2 FAKE_GETDOCKER_HANG=1 run_install --lang zh --yes --install-docker --dir "$SB/oa" --docker-source official; assert_rc 1; assert_has "$SB/out.txt" '诊断信息（超时后）'; assert_has "$SB/out.txt" 'Docker 安装超时（2 秒）'
    pgrep -fx 'sleep 987' >/dev/null 2>&1 && bad_ "a hung child is left" || ok_
}

t_pkg_lock() {
    # another apt/unattended-upgrades holds the lock: --yes waits (and says so) until it is gone
    NO_DOCKER=1 new_sandbox lk1; OA_HEARTBEAT=0.3 FAKE_PS_PROCS='777 12:00 /usr/bin/unattended-upgrade' FAKE_LOCK_RELEASE_AFTER=4 run_install --yes --install-docker --dir "$SB/oa"; assert_rc 0
    assert_has "$SB/out.txt" 'Another package-manager process is running'; assert_has "$SB/out.txt" '777 12:00 /usr/bin/unattended-upgrade'; assert_has "$SB/out.txt" 'still waiting for the package manager to become idle'; assert_order "$SB/out.txt" 'still waiting' 'Installing Docker'; assert_has "$SB/calls.log" '^get-docker.sh'
    # never released: a clear failure with the hint, nothing installed
    NO_DOCKER=1 new_sandbox lk2; OA_LOCK_WAIT=1 OA_HEARTBEAT=0.3 FAKE_PS_PROCS='777 12:00 apt-get -y install foo' run_install --yes --install-docker --dir "$SB/oa"; assert_rc 1; assert_has "$SB/out.txt" 'Waited 1 s and the package manager is still busy'; assert_has "$SB/out.txt" 'systemctl stop unattended-upgrades'; assert_not "$SB/calls.log" '^get-docker.sh'
    # interactive: continue regardless / abort / wait (default)
    NO_DOCKER=1 new_sandbox lk3; answers "$SB/oa" 18090 '' 1 '' n '' default y c; OA_INSTALL_TTY="$SB/answers" FAKE_PS_PROCS='777 12:00 dpkg --configure -a' run_install --dir "$SB/oa" --install-docker; assert_rc 0; assert_has "$SB/out.txt" '\[w\]ait for it to finish'; assert_has "$SB/calls.log" '^get-docker.sh'
    NO_DOCKER=1 new_sandbox lk4; answers "$SB/oa" 18090 '' 1 '' n '' default y a; OA_INSTALL_TTY="$SB/answers" FAKE_PS_PROCS='777 12:00 dpkg --configure -a' run_install --dir "$SB/oa" --install-docker; assert_rc 4; assert_has "$SB/out.txt" 'Aborted: the package manager is busy'; assert_not "$SB/calls.log" '^get-docker.sh'
    NO_DOCKER=1 new_sandbox lk5; answers "$SB/oa" 18090 '' 1 '' n '' default y ''; OA_HEARTBEAT=0.3 FAKE_LOCK_RELEASE_AFTER=4 OA_INSTALL_TTY="$SB/answers" FAKE_PS_PROCS='777 12:00 dnf install x' run_install --dir "$SB/oa" --install-docker; assert_rc 0; assert_has "$SB/out.txt" 'still waiting'
    # no lock holder: no message; the prerequisite and compose-plugin installs check too
    NO_DOCKER=1 new_sandbox lk6; run_install --yes --install-docker --dir "$SB/oa"; assert_not "$SB/out.txt" 'Another package-manager process'
    new_sandbox lk7; OA_TEST_MISSING_CMDS=flock OA_HEARTBEAT=0.3 FAKE_PS_PROCS='777 12:00 apt-get install y' FAKE_LOCK_RELEASE_AFTER=4 run_install --yes --install-prereqs --dir "$SB/oa"; assert_rc 0; assert_has "$SB/out.txt" 'Another package-manager process is running'
    # Chinese
    NO_DOCKER=1 new_sandbox lk8; OA_HEARTBEAT=0.3 FAKE_PS_PROCS='777 12:00 apt-get install y' FAKE_LOCK_RELEASE_AFTER=4 run_install --lang zh --yes --install-docker --dir "$SB/oa"; assert_has "$SB/out.txt" '检测到另一个包管理器进程正在运行'; assert_has "$SB/out.txt" '仍在等待包管理器空闲'
}

t_docker_env_warning() {
    local k
    # no systemd + container / WSL: the warning (with the alternative) is in the detection table and again before installing
    for k in container wsl; do
        NO_DOCKER=1 new_sandbox ev_$k; OA_TEST_ENVKIND=$k OA_SYSTEMD_RUN_DIR="$SB/nosystemd" run_install --check --dir "$SB/oa"; assert_rc 0; assert_has "$SB/out.txt" "\\[WARN\\] $k environment without systemd|\\[WARN\\] A $k environment without systemd"
        OA_TEST_ENVKIND=$k OA_SYSTEMD_RUN_DIR="$SB/nosystemd" run_install --yes --install-docker --dir "$SB/oa"; assert_rc 0; assert_has "$SB/out.txt" 'post-install script may hang or fail'
    done
    assert_has "$SB/out.txt" 'wsl.conf|docker.sock'
    NO_DOCKER=1 new_sandbox ev3; OA_TEST_ENVKIND=wsl OA_SYSTEMD_RUN_DIR="$SB/nosystemd" run_install --yes --install-docker --dir "$SB/oa"; assert_has "$SB/out.txt" '/etc/wsl.conf'
    NO_DOCKER=1 new_sandbox ev4; OA_TEST_ENVKIND=container OA_SYSTEMD_RUN_DIR="$SB/nosystemd" run_install --yes --install-docker --dir "$SB/oa"; assert_has "$SB/out.txt" '/var/run/docker.sock'
    # interactive: the default answer is "no"
    NO_DOCKER=1 new_sandbox ev5; answers "$SB/oa" 18090 '' 1 '' n '' default y ''; OA_TEST_ENVKIND=container OA_SYSTEMD_RUN_DIR="$SB/nosystemd" OA_INSTALL_TTY="$SB/answers" run_install --dir "$SB/oa" --install-docker; assert_rc 4; assert_has "$SB/out.txt" 'Install Docker here anyway'; assert_not "$SB/calls.log" '^get-docker.sh'
    NO_DOCKER=1 new_sandbox ev6; answers "$SB/oa" 18090 '' 1 '' n '' default y y; OA_TEST_ENVKIND=container OA_SYSTEMD_RUN_DIR="$SB/nosystemd" OA_INSTALL_TTY="$SB/answers" run_install --dir "$SB/oa" --install-docker; assert_rc 0
    # systemd present or a real host: no warning
    NO_DOCKER=1 new_sandbox ev7; OA_TEST_ENVKIND=container run_install --yes --install-docker --dir "$SB/oa"; assert_not "$SB/out.txt" 'post-install script may hang'
    NO_DOCKER=1 new_sandbox ev8; OA_TEST_ENVKIND=none OA_SYSTEMD_RUN_DIR="$SB/nosystemd" run_install --yes --install-docker --dir "$SB/oa"; assert_not "$SB/out.txt" 'post-install script may hang'
    # Chinese
    NO_DOCKER=1 new_sandbox ev9; OA_TEST_ENVKIND=wsl OA_SYSTEMD_RUN_DIR="$SB/nosystemd" run_install --lang zh --yes --install-docker --dir "$SB/oa"; assert_has "$SB/out.txt" '检测到 WSL 且没有 systemd'
}

# ---------------------------------------------------------------------------------------------- runner
for t in $(declare -F | awk '{print $3}' | grep '^t_'); do
    [ -z "$FILTER" ] || [[ "$t" == *"$FILTER"* ]] || continue
    CUR="$t"; echo "== $t"
    before=$FAIL
    "$t"
    [ "$FAIL" -eq "$before" ] && echo "   ok" || echo "   FAILED"
done
echo
echo "assertions passed: $PASS, failed: $FAIL"
if [ "$FAIL" -gt 0 ]; then printf ' - %s\n' "${FAILED[@]}"; exit 1; fi
[ -n "${OA_TEST_TMP:-}" ] || rm -rf -- "$BASE"
