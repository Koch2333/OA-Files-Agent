#!/usr/bin/env bash
# docker-host.sh - a disposable "Linux server" container for testing deploy/linux/install.sh and oa-agentctl
# against a REAL Docker engine (Docker-outside-of-Docker: the engine's socket is mounted into the container).
#
#   deploy/linux/test/docker-host.sh up              build the host image and start the host container
#   deploy/linux/test/docker-host.sh shell           interactive bash inside it
#   deploy/linux/test/docker-host.sh exec CMD [ARG]  run a command inside it (stdin attached)
#   deploy/linux/test/docker-host.sh snapshot        copy the read-only /repo to <test root>/repo (installs run from it)
#   deploy/linux/test/docker-host.sh vm-ls PATH      list PATH as the Docker daemon sees it (owner uids, sizes)
#   deploy/linux/test/docker-host.sh e2e             full real-engine acceptance run (see real-e2e.sh)
#   deploy/linux/test/docker-host.sh down [--purge]  remove the host container; --purge also removes every
#                                                    container/volume/network/image named <prefix>-* and the test tree
#
# PATH RULE: bind-mount sources written into compose files are resolved by the DAEMON, so the install dir and every
# working directory must exist at the same absolute path in this container and on the daemon's host. The test root
# (default /var/lib/oa-linux-test) is therefore bind-mounted at the identical path, and all installs/archive dirs used by
# the tests live below it. On Docker Desktop, a source starting with "/" is a path inside Docker Desktop's Linux VM.
#
# Environment:
#   DOCKER            docker client to use (default: docker). On WSL you can point it at a wrapper around docker.exe.
#   OA_TEST_PREFIX    name prefix of everything created (default: linux)  -> container <prefix>-oa-host
#   OA_TEST_ROOT      shared test tree (default: /var/lib/oa-linux-test; NOT /tmp: on Docker Desktop and many distros
#                     /tmp is a small tmpfs, which fails the installer's 5 GB free-space check)
#   OA_HOST_REPO      repository path AS THE DAEMON SEES IT (default: this checkout; on Docker Desktop for Windows
#                     pass the Windows path, e.g. 'D:\src\oa-agent'). Mounted read-only at /repo.
set -Eeuo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_LOCAL="$(cd "$HERE/../../.." && pwd)"
read -r -a DOCKER_CMD <<<"${DOCKER:-docker}"
PREFIX="${OA_TEST_PREFIX:-linux}"
ROOT="${OA_TEST_ROOT:-/var/lib/oa-linux-test}"
REPO="${OA_HOST_REPO:-$REPO_LOCAL}"
NAME="$PREFIX-oa-host"
IMAGE="$PREFIX-oa-host:test"

d() { "${DOCKER_CMD[@]}" "$@"; }
die() { printf 'docker-host: %s\n' "$*" >&2; exit 1; }
[[ "$PREFIX" =~ ^[a-z0-9][a-z0-9-]{0,30}$ ]] || die "OA_TEST_PREFIX must be lowercase letters/digits/-"
case "$ROOT" in /?*/*) ;; *) die "OA_TEST_ROOT must be an absolute path at least two levels deep (e.g. /var/lib/oa-linux-test)" ;; esac

running() { [ "$(d inspect -f '{{.State.Running}}' "$NAME" 2>/dev/null || true)" = true ]; }

cmd_build() {
    d build -t "$IMAGE" - <"$HERE/host.Dockerfile"   # no context needed: the Dockerfile copies nothing
}

cmd_up() {
    if running; then echo "$NAME is already running"; return 0; fi
    d rm -f "$NAME" >/dev/null 2>&1 || true
    d image inspect "$IMAGE" >/dev/null 2>&1 || cmd_build
    # create the shared tree on the daemon's host (and a TMPDIR below it: the installer's golang build container
    # bind-mounts mktemp directories, which therefore have to be shared as well)
    d run --rm -v "$(dirname "$ROOT"):/vmparent" alpine:3.21 mkdir -p "/vmparent/$(basename "$ROOT")/tmp"
    d run -d --name "$NAME" --hostname "$PREFIX-oa-host" \
        -v /var/run/docker.sock:/var/run/docker.sock \
        -v "$ROOT:$ROOT" \
        -v "$REPO:/repo:ro" \
        -e "TMPDIR=$ROOT/tmp" -e "OA_TEST_ROOT=$ROOT" -e "OA_TEST_PREFIX=$PREFIX" \
        "$IMAGE" sleep infinity >/dev/null
    d exec "$NAME" sh -c 'docker version --format "engine {{.Server.Version}}" && docker compose version && test -f /repo/deploy/linux/install.sh' ||
        die "host container started but docker/the repository is not usable inside it"
    echo "$NAME is up (repo at /repo, shared test tree $ROOT)"
}

cmd_shell() { running || die "$NAME is not running (run: $0 up)"; d exec -it "$NAME" bash; }

cmd_exec() {
    running || die "$NAME is not running (run: $0 up)"
    [ $# -gt 0 ] || die "usage: $0 exec CMD [ARGS...]"
    if [ -t 0 ] && [ -t 1 ]; then d exec -it "$NAME" "$@"; else d exec -i "$NAME" "$@"; fi
}

cmd_snapshot() { # a private copy of the checkout at $ROOT/repo, so the repository can be edited while a test runs
    running || die "$NAME is not running (run: $0 up)"
    d exec -i -e "R=$ROOT/repo" "$NAME" bash -c 'set -e; rm -rf "$R.new"; mkdir -p "$R.new"
        (cd /repo && tar --exclude=./.git --exclude=node_modules --exclude=.venv --exclude=__pycache__ --exclude=./frontend/dist \
            --exclude=./dist --exclude=./data --exclude=./backend/bin -cf - .) | (cd "$R.new" && tar -xf -)
        rm -rf "$R"; mv "$R.new" "$R"; echo "snapshot: $R ($(du -sh "$R" | cut -f1))"'
}

cmd_vm_ls() {
    [ $# -eq 1 ] || die "usage: $0 vm-ls /absolute/path"
    d run --rm -v "$1:/x:ro" alpine:3.21 sh -c 'cd /x && ls -lnaR .'
}

cmd_e2e() {
    cmd_up
    d exec -i "$NAME" bash /repo/deploy/linux/test/real-e2e.sh "$@"
}

cmd_down() {
    local purge=0
    [ "${1:-}" != --purge ] || purge=1
    d rm -f "$NAME" >/dev/null 2>&1 || true
    if [ "$purge" = 1 ]; then
        local ids
        ids="$(d ps -aq --filter "name=^$PREFIX-")"
        [ -z "$ids" ] || d rm -f $ids >/dev/null
        ids="$(d volume ls -q --filter "name=^$PREFIX-")"
        [ -z "$ids" ] || d volume rm $ids >/dev/null
        ids="$(d network ls -q --filter "name=^$PREFIX-")"
        [ -z "$ids" ] || d network rm $ids >/dev/null
        ids="$(d images --format '{{.Repository}}:{{.Tag}}' | grep -E "^$PREFIX[-/]" || true)"
        [ -z "$ids" ] || d rmi -f $ids >/dev/null
        d run --rm -v "$(dirname "$ROOT"):/vmparent" alpine:3.21 rm -rf "/vmparent/$(basename "$ROOT")"
        echo "purged $PREFIX-* containers/volumes/networks/images and $ROOT"
    else
        echo "$NAME removed (installs and $ROOT kept; use --purge to remove them)"
    fi
}

sub="${1:-}"; [ $# -eq 0 ] || shift
case "$sub" in
    build) cmd_build ;;
    up) cmd_up ;;
    shell) cmd_shell ;;
    exec) cmd_exec "$@" ;;
    snapshot) cmd_snapshot ;;
    vm-ls) cmd_vm_ls "$@" ;;
    e2e) cmd_e2e "$@" ;;
    down) cmd_down "$@" ;;
    *) awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "${BASH_SOURCE[0]}"; [ -n "$sub" ] || exit 0; exit 2 ;;
esac
