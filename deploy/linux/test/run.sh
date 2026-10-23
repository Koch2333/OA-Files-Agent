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
    unset FAKE_PORTS FAKE_UNHEALTHY FAKE_NO_COMPOSE FAKE_UFW FAKE_PULL_FAIL FAKE_COMPOSE_PULL_FAIL FAKE_LOGIN_FAIL FAKE_BUNDLE FAKE_KERNEL FAKE_SELINUX FAKE_NTP FAKE_CGROUP FAKE_DOCKER_VERSION FAKE_PULL_FLAKY FAKE_FAIL_IMAGE FAKE_MISSING_IMAGE FAKE_ECS_REGION FAKE_ECS_EIP FAKE_PUBLIC_IP FAKE_GETDOCKER_FAIL FAKE_GETDOCKER_BADMIRROR FAKE_NO_REGISTRY FAKE_IPS4 FAKE_IPS6 FAKE_DOCKER_ROOT FAKE_DOCKER_SECOPT FAKE_SAVE_FAIL FAKE_IMG_ARCH FAKE_LOAD_FAIL FAKE_PKG_FAIL FAKE_BUILD_FLAKY 2>/dev/null || true
}

make_shims() {
    local s="$SB/shim"
    cat >"$s/docker.real" <<'SH'
#!/usr/bin/env bash
SB="${FAKE_SB:?}"; echo "docker $*" >>"$SB/calls.log"
services() { local l="postgres api worker"; case ":${COMPOSE_FILE:-}:" in *:docker-compose.https.yml:*) l="$l caddy" ;; esac; echo "$l"; }
case "${1:-}" in
  info) case "$*" in *DockerRootDir*) echo "${FAKE_DOCKER_ROOT:-}" ;; *SecurityOptions*) echo "${FAKE_DOCKER_SECOPT:-[name=seccomp,profile=builtin]}" ;; esac; exit 0 ;;
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
  ps) for f in "$SB"/state/up-*; do [ -e "$f" ] && echo "oa-agent-$(basename "$f" | sed 's/^up-//')-1"; done; exit 0 ;;
  inspect) svc="${@: -1}"; svc="${svc#cid-}"
    if [ -e "$SB/state/up-$svc" ]; then [ "${FAKE_UNHEALTHY:-}" = "$svc" ] && echo unhealthy || echo healthy; else echo exited; fi; exit 0 ;;
  run) out=''; for a in "$@"; do case "$a" in *:/out) out="${a%:/out}" ;; esac; done
    if [ -z "$out" ]; then printf '#!/usr/bin/env bash\necho fake-deploy-image-install.sh\n'; exit 0; fi
    printf '#!/usr/bin/env bash\necho "oa-agentctl $*" >>"$FAKE_SB/calls.log"\nif [ "$1 $2" = "mounts apply" ]; then cp "$FAKE_EXAMPLE" "$4/docker-compose.mounts.yml"; fi\n' >"$out/oa-agentctl"; chmod +x "$out/oa-agentctl"; exit 0 ;;
  compose)
    shift
    if [ "${1:-}" = version ]; then [ -n "${FAKE_NO_COMPOSE:-}" ] && exit 1; [ "${2:-}" = --short ] && echo 2.29.0 || echo "Docker Compose version v2.29.0"; exit 0; fi
    echo "compose[CF=${COMPOSE_FILE:-}] $*" >>"$SB/calls.log"
    cmd="$1"; shift
    targets=(); for a in "$@"; do case "$a" in -*) ;; *) targets+=("$a") ;; esac; done
    case "$cmd" in
      build) if [ "${FAKE_BUILD_FLAKY:-0}" -gt 0 ]; then n=$(cat "$SB/state/flaky-build" 2>/dev/null || echo 0); echo $((n + 1)) >"$SB/state/flaky-build"; if [ "$n" -lt "$FAKE_BUILD_FLAKY" ]; then echo "failed to solve: temporary failure in name resolution" >&2; exit 1; fi; fi; echo "fake $cmd progress" ;;
      pull) [ -z "${FAKE_COMPOSE_PULL_FAIL:-}" ] || { echo "denied: requested access to the resource is denied" >&2; exit 1; }
          if [ "${FAKE_PULL_FLAKY:-0}" -gt 0 ]; then n=$(cat "$SB/state/flaky-cpull" 2>/dev/null || echo 0); echo $((n + 1)) >"$SB/state/flaky-cpull"; if [ "$n" -lt "$FAKE_PULL_FLAKY" ]; then echo "error pulling image: net/http: TLS handshake timeout" >&2; exit 1; fi; fi
          echo "fake pull progress" ;;
      up) if [ "${#targets[@]}" -eq 0 ]; then targets=($(services)); fi; for t in "${targets[@]}"; do touch "$SB/state/up-$t"; done
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
    printf '#!/bin/sh\necho "get-docker.sh $*" >>"$FAKE_SB/calls.log"\nif [ -n "$FAKE_GETDOCKER_BADMIRROR" ] && [ "$2" != AzureChinaCloud ]; then echo mirror-failed >&2; exit 1; fi\ncp "$FAKE_SB/shim/docker.real" "$FAKE_SB/shim/docker"\n' >"$out"; exit 0 ;;
  *127.0.0.1*healthz*) exit 0 ;;
  *100.100.100.200*region-id) [ -n "${FAKE_ECS_REGION:-}" ] || exit 28; printf '%s' "$FAKE_ECS_REGION"; exit 0 ;;
  *100.100.100.200*eipv4) [ -n "${FAKE_ECS_EIP:-}" ] || exit 28; printf '%s' "$FAKE_ECS_EIP"; exit 0 ;;
  *api.ipify.org*) [ -n "${FAKE_PUBLIC_IP:-}" ] || exit 6; printf '%s' "$FAKE_PUBLIC_IP"; exit 0 ;;
  */v2/) [ -z "${FAKE_NO_REGISTRY:-}" ] || exit 6; printf 401; exit 0 ;;
esac
exit 22
SH
    printf '#!/usr/bin/env bash\nfor p in ${FAKE_PORTS:-}; do echo "LISTEN 0 4096 0.0.0.0:$p 0.0.0.0:*"; done\n' >"$s/ss"
    printf '#!/usr/bin/env bash\necho "systemctl $*" >>"$FAKE_SB/calls.log"\nexit 0\n' >"$s/systemctl"
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
    for p in apt-get dnf yum zypper apk; do printf '#!/usr/bin/env bash\necho "%s $*" >>"$FAKE_SB/calls.log"\n[ -z "$FAKE_PKG_FAIL" ] || exit 100\ncase " $* " in *" install "*|*" add "*) case " $* " in *docker*) cp "$FAKE_SB/shim/docker.real" "$FAKE_SB/shim/docker" ;; esac ;; esac\nexit 0\n' "$p" >"$s/$p"; done
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
        OA_BUILD_FROM_SOURCE="${OA_BUILD_FROM_SOURCE:-1}" PATH="$SB/shim:$PATH" bash "$SB/repo/deploy/linux/install.sh" --lang en "$@" >"$SB/out.txt" 2>&1
    RC=$?
}
run_ctl() { # run_ctl args... (bash fallback ctl against $SB/oa)
    OA_POLL_SECONDS=0 OA_UNIT_DIR="$SB/units" OA_CTL_SELF="$SB/bin/oa-agentctl" OA_LANG=en PATH="$SB/shim:$PATH" \
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
    printf '%s\n' "$SB/oa" 18082 '' default "$SB/data1/archive" n '' >"$SB/answers"
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
    RC=$?; assert_rc 0; assert_file "$SB/oa/.env"
}

# ---------------------------------------------------------------------------------------------- registry mode (deploy image bundle)
REG=reg.example.com/ns
make_bundle() { # a fake /deploy of the oa-agent-deploy image at $SB/bundle (flat layout), exported as FAKE_BUNDLE
    local b="$SB/bundle" f
    rm -rf -- "$b"; mkdir -p "$b/bin"
    for f in install.sh oa-agentctl.sh docker-compose.yml docker-compose.build.yml docker-compose.https.yml docker-compose.mounts.example.yml Caddyfile.template .env.example oa-agent.service; do cp "$LINUX/$f" "$b/$f"; done
    printf 'version=test\ncommit=abc1234\nregistry=%s\n' "$REG" >"$b/VERSION"
    printf '#!/usr/bin/env bash\necho "bundled-ctl $*" >>"$FAKE_SB/calls.log"\nif [ "$1 $2" = "mounts apply" ]; then cp "$FAKE_EXAMPLE" "$4/docker-compose.mounts.yml"; fi\n' >"$b/bin/oa-agentctl-linux-amd64"
    cp "$b/bin/oa-agentctl-linux-amd64" "$b/bin/oa-agentctl-linux-arm64"
    (cd "$b/bin" && sha256sum oa-agentctl-linux-* >SHA256SUMS)
    export FAKE_BUNDLE="$b"
}
run_install_piped() { # like "docker run --rm <deploy image> | sudo bash -s -- ...": no script file, cwd outside any checkout
    ( cd "$SB" && cat "$LINUX/install.sh" | OA_TEST_MODE=1 OA_INSTALL_LOG="$SB/install.log" OA_INSTALL_BIN_DIR="$SB/bin" OA_INSTALL_UNIT_DIR="$SB/units" OA_INSTALL_ETC_MARKER="$SB/etc/install_dir" \
        OA_OS_RELEASE="$SB/os-release" OA_MEMINFO="$SB/meminfo" OA_POLL_SECONDS=0 OA_RETRY_BASE=0 OA_INSTALL_LOCK="$SB/lock" OA_DAEMON_JSON="$SB/daemon.json" OA_TEST_DOCKER_SHIM="$SB/shim/docker" \
        PATH="$SB/shim:$PATH" bash -s -- --lang en "$@" ) >"$SB/out.txt" 2>&1
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
        PATH="$SB/shim:$PATH" bash -s -- --lang en "$@" <"$f" ) >"$SB/out.txt" 2>&1
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
    new_sandbox pf6; run_install --yes --dir "$SB/oa"; assert_rc 0; assert_has "$SB/out.txt" 'cgroup v2'; assert_has "$SB/out.txt" 'Docker 26.1.0 / compose 2.29.0'
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
    assert_order "$SB/calls.log" 'get-docker.sh --mirror Aliyun' 'get-docker.sh --mirror AzureChinaCloud'; assert_has "$SB/out.txt" 'trying the next one'
    assert_not "$SB/calls.log" '^apt-get install'
    # without --mirror cn: one plain attempt
    NO_DOCKER=1 new_sandbox dc2; run_install --yes --dir "$SB/oa" --install-docker; assert_rc 0; assert_has "$SB/calls.log" 'get-docker.sh $'; assert_not "$SB/calls.log" '--mirror'
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
    new_sandbox dm2; printf '%s\n' "$SB/oa" 18084 '' default "$SB/arch" n '' y n >"$SB/answers"
    OA_INSTALL_TTY="$SB/answers" run_install --dir "$SB/oa" --docker-mirror "$M"; assert_rc 0
    assert_file "$SB/daemon.json"; assert_has "$SB/daemon.json" "\"registry-mirrors\""; assert_has "$SB/daemon.json" "\"$M\""; assert_eq "$(mode "$SB/daemon.json")" 644 "daemon.json mode"
    assert_not "$SB/calls.log" 'systemctl restart docker'; assert_has "$SB/out.txt" 'Restart later yourself'
    # consent to the restart
    new_sandbox dm3; printf '%s\n' "$SB/oa" 18084 '' default "$SB/arch" n '' y y >"$SB/answers"
    OA_INSTALL_TTY="$SB/answers" run_install --dir "$SB/oa" --docker-mirror "$M"; assert_rc 0; assert_has "$SB/calls.log" '^systemctl restart docker'; assert_has "$SB/out.txt" 'Docker restarted'
    # consent refused: untouched
    new_sandbox dm4; printf '%s\n' "$SB/oa" 18084 '' default "$SB/arch" n '' n >"$SB/answers"; printf '{"log-driver": "json-file"}\n' >"$SB/daemon.json"; cp "$SB/daemon.json" "$SB/daemon.orig"
    OA_INSTALL_TTY="$SB/answers" run_install --dir "$SB/oa" --docker-mirror "$M"; assert_rc 0; cmp -s "$SB/daemon.json" "$SB/daemon.orig" && ok_ || bad_ "daemon.json changed without consent"
    assert_has "$SB/out.txt" 'Skipped the registry mirror'
    # an empty "{}" file is replaced, with a backup
    new_sandbox dm5; printf '%s\n' "$SB/oa" 18084 '' default "$SB/arch" n '' y n >"$SB/answers"; printf '{}\n' >"$SB/daemon.json"
    OA_INSTALL_TTY="$SB/answers" run_install --dir "$SB/oa" --docker-mirror "$M"; assert_rc 0; assert_has "$SB/daemon.json" "$M"
    [ -n "$(ls "$SB"/daemon.json.oa-bak.* 2>/dev/null)" ] && ok_ || bad_ "no backup of the old daemon.json"
    # without any JSON tool a non-trivial file is NEVER patched blindly
    new_sandbox dm6; printf '%s\n' "$SB/oa" 18084 '' default "$SB/arch" n '' y n >"$SB/answers"; printf '{\n  "log-driver": "json-file",\n  "registry-mirrors": ["https://old.example.com"]\n}\n' >"$SB/daemon.json"; cp "$SB/daemon.json" "$SB/daemon.orig"
    OA_TEST_NO_JSON=1 OA_INSTALL_TTY="$SB/answers" run_install --dir "$SB/oa" --docker-mirror "$M"; assert_rc 0
    cmp -s "$SB/daemon.json" "$SB/daemon.orig" && ok_ || bad_ "daemon.json patched without a JSON tool"; assert_has "$SB/out.txt" 'No python3 / jq'
    # with a real JSON tool: merge (other keys and the old mirror survive, the new one is added, a backup exists); invalid JSON is untouched
    if command -v python3 >/dev/null 2>&1 || command -v jq >/dev/null 2>&1 || perl -MJSON::PP -e1 >/dev/null 2>&1; then
        new_sandbox dm7; printf '%s\n' "$SB/oa" 18084 '' default "$SB/arch" n '' y n >"$SB/answers"; cp "$SB/daemon.orig" "$SB/daemon.json" 2>/dev/null || printf '{"log-driver": "json-file", "registry-mirrors": ["https://old.example.com"]}\n' >"$SB/daemon.json"
        OA_INSTALL_TTY="$SB/answers" run_install --dir "$SB/oa" --docker-mirror "$M"; assert_rc 0
        assert_has "$SB/daemon.json" 'log-driver'; assert_has "$SB/daemon.json" 'https://old.example.com'; assert_has "$SB/daemon.json" "$M"
        [ -n "$(ls "$SB"/daemon.json.oa-bak.* 2>/dev/null)" ] && ok_ || bad_ "no backup"
        new_sandbox dm8; printf '%s\n' "$SB/oa" 18084 '' default "$SB/arch" n '' y n >"$SB/answers"; printf '{ "log-driver": \n' >"$SB/daemon.json"; cp "$SB/daemon.json" "$SB/daemon.orig"
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
    assert_has "$SB/out.txt" 'repair/continue'; assert_has "$SB/out.txt" 'reinstall'; assert_has "$SB/out.txt" 'enter 1-5'; assert_has "$SB/out.txt" 'Existing installation detected'
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
