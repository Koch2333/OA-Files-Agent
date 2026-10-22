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
    unset FAKE_PORTS FAKE_UNHEALTHY FAKE_NO_COMPOSE FAKE_UFW 2>/dev/null || true
}

make_shims() {
    local s="$SB/shim"
    cat >"$s/docker.real" <<'SH'
#!/usr/bin/env bash
SB="${FAKE_SB:?}"; echo "docker $*" >>"$SB/calls.log"
services() { local l="postgres api worker"; case ":${COMPOSE_FILE:-}:" in *:docker-compose.https.yml:*) l="$l caddy" ;; esac; echo "$l"; }
case "${1:-}" in
  info) exit 0 ;;
  version) echo 26.1.0; exit 0 ;;
  ps) for f in "$SB"/state/up-*; do [ -e "$f" ] && echo "oa-agent-$(basename "$f" | sed 's/^up-//')-1"; done; exit 0 ;;
  inspect) svc="${@: -1}"; svc="${svc#cid-}"
    if [ -e "$SB/state/up-$svc" ]; then [ "${FAKE_UNHEALTHY:-}" = "$svc" ] && echo unhealthy || echo healthy; else echo exited; fi; exit 0 ;;
  run) out=''; for a in "$@"; do case "$a" in *:/out) out="${a%:/out}" ;; esac; done
    printf '#!/usr/bin/env bash\necho "oa-agentctl $*" >>"$FAKE_SB/calls.log"\nif [ "$1 $2" = "mounts apply" ]; then cp "$FAKE_EXAMPLE" "$4/docker-compose.mounts.yml"; fi\n' >"$out/oa-agentctl"; chmod +x "$out/oa-agentctl"; exit 0 ;;
  compose)
    shift
    if [ "${1:-}" = version ]; then [ -n "${FAKE_NO_COMPOSE:-}" ] && exit 1; [ "${2:-}" = --short ] && echo 2.29.0 || echo "Docker Compose version v2.29.0"; exit 0; fi
    echo "compose[CF=${COMPOSE_FILE:-}] $*" >>"$SB/calls.log"
    cmd="$1"; shift
    targets=(); for a in "$@"; do case "$a" in -*) ;; *) targets+=("$a") ;; esac; done
    case "$cmd" in
      build|pull) echo "fake $cmd progress" ;;
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
  *get.docker.com*) printf '#!/bin/sh\necho "get-docker.sh $*" >>"$FAKE_SB/calls.log"\ncp "$FAKE_SB/shim/docker.real" "$FAKE_SB/shim/docker"\n' >"$out"; exit 0 ;;
  *127.0.0.1*healthz*) exit 0 ;;
esac
exit 22
SH
    printf '#!/usr/bin/env bash\nfor p in ${FAKE_PORTS:-}; do echo "LISTEN 0 4096 0.0.0.0:$p 0.0.0.0:*"; done\n' >"$s/ss"
    printf '#!/usr/bin/env bash\necho "systemctl $*" >>"$FAKE_SB/calls.log"\nexit 0\n' >"$s/systemctl"
    printf '#!/usr/bin/env bash\necho "ufw $*" >>"$FAKE_SB/calls.log"\n[ "$1" = status ] && [ -n "${FAKE_UFW:-}" ] && echo "Status: active"\nexit 0\n' >"$s/ufw"
    printf '#!/usr/bin/env bash\nprintf "Filesystem 1024-blocks Used Available Capacity Mounted\\nfake 999999999 1 500000000 1pct /\\n"\n' >"$s/df"
    printf '#!/usr/bin/env bash\n[ "$1" = -I ] && echo 192.0.2.10 || echo testhost\n' >"$s/hostname"
    printf '#!/usr/bin/env bash\necho yes\n' >"$s/timedatectl"
    chmod +x "$s"/*
    [ -n "${NO_DOCKER:-}" ] && rm -f "$s/docker"
    return 0
}

run_install() { # run_install args... ; output in $SB/out.txt, status in RC
    OA_TEST_MODE=1 OA_INSTALL_LOG="$SB/install.log" OA_INSTALL_BIN_DIR="$SB/bin" OA_INSTALL_UNIT_DIR="$SB/units" \
        OA_OS_RELEASE="$SB/os-release" OA_MEMINFO="$SB/meminfo" OA_POLL_SECONDS=0 \
        PATH="$SB/shim:$PATH" bash "$SB/repo/deploy/linux/install.sh" --lang en "$@" >"$SB/out.txt" 2>&1
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

t_port_conflict() {
    new_sandbox port; FAKE_PORTS="18080" run_install --yes --dir "$SB/oa" --port 18080
    assert_rc_nz; assert_has "$SB/out.txt" 'Port 18080 is already in use'; assert_nofile "$SB/oa/.env"
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
    new_sandbox mounts; mkdir -p "$SB/nas1" "$SB/disk"
    run_install --yes --dir "$SB/oa" --archive-dir "$SB/disk/archive" --pg-dir "$SB/disk/pg" --spool-dir "$SB/disk/spool" --data-dir "$SB/disk/app" \
        --extra-mount "nas1=$SB/nas1" --extra-mount "scans=$SB/nas1:rw"
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
    new_sandbox notty; OA_TEST_MODE=1 OA_INSTALL_LOG="$SB/install.log" OA_INSTALL_BIN_DIR="$SB/bin" OA_INSTALL_UNIT_DIR="$SB/units" OA_OS_RELEASE="$SB/os-release" OA_MEMINFO="$SB/meminfo" OA_POLL_SECONDS=0 OA_INSTALL_TTY=/nonexistent PATH="$SB/shim:$PATH" bash "$SB/repo/deploy/linux/install.sh" --lang en --dir "$SB/oa" </dev/null >"$SB/out.txt" 2>&1
    RC=$?; assert_rc 0; assert_file "$SB/oa/.env"
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
