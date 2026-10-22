#!/usr/bin/env bash
# Build the Linux release artefacts (no Makefile needed):
#   <out>/oa-agent-source.tar.gz         source tree + deploy files + bin/oa-agentctl-linux-{amd64,arm64} + bin/SHA256SUMS
#   <out>/oa-agent-source.tar.gz.sha256  checksum install.sh verifies when downloading
#   <out>/install.sh                     the installer (for  curl -fsSL <url>/install.sh | bash)
#   <out>/oa-agentctl-linux-{amd64,arm64}, <out>/SHA256SUMS  (all artefacts)
# oa-agentctl is cross-compiled with CGO_ENABLED=0 using local Go, or a golang container when Go is not installed.
# Usage: deploy/linux/build-release.sh [--version V] [--out DIR] [--archs "amd64 arm64"]
set -Eeuo pipefail
umask 022

VERSION=dev
ARCHS="amd64 arm64"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
OUT="$ROOT/dist/release"
GO_IMAGE="${GO_IMAGE:-golang:1.26}"

die() { printf 'build-release: %s\n' "$*" >&2; exit 1; }
while [ $# -gt 0 ]; do
    case "$1" in
        --version) VERSION="${2:?}"; shift 2 ;;
        --out) OUT="${2:?}"; shift 2 ;;
        --archs) ARCHS="${2:?}"; shift 2 ;;
        -h | --help) sed -n '2,10p' "${BASH_SOURCE[0]}"; exit 0 ;;
        *) die "unknown option $1" ;;
    esac
done
[[ "$VERSION" =~ ^[A-Za-z0-9._-]{1,64}$ ]] || die "invalid version"
[ -d "$ROOT/backend/cmd/oa-agentctl" ] || die "backend/cmd/oa-agentctl is missing (the Go TUI is built by a separate change)"
for a in $ARCHS; do case "$a" in amd64 | arm64) ;; *) die "unsupported arch $a" ;; esac; done

STAGE="$(mktemp -d)"
trap 'rm -rf -- "$STAGE"' EXIT
mkdir -p "$OUT" "$STAGE/oa-agent/bin"
COMMIT="$(cd "$ROOT" && git rev-parse --short HEAD 2>/dev/null || echo unknown)"
PKG=oa-agent/backend/internal/buildinfo
LDFLAGS="-s -w -X $PKG.Version=$VERSION -X $PKG.Commit=$COMMIT"

for a in $ARCHS; do
    bin="oa-agentctl-linux-$a"
    echo "==> building $bin"
    if command -v go >/dev/null 2>&1; then
        (cd "$ROOT/backend" && CGO_ENABLED=0 GOOS=linux GOARCH="$a" go build -trimpath -ldflags "$LDFLAGS" -o "$STAGE/oa-agent/bin/$bin" ./cmd/oa-agentctl)
    elif command -v docker >/dev/null 2>&1; then
        docker run --rm -e CGO_ENABLED=0 -e GOOS=linux -e "GOARCH=$a" -e GOCACHE=/tmp/gocache \
            -v "$ROOT:/src:ro" -v "$STAGE/oa-agent/bin:/out" -w /src/backend "$GO_IMAGE" \
            go build -trimpath -ldflags "$LDFLAGS" -o "/out/$bin" ./cmd/oa-agentctl
    else
        die "need Go or Docker to build oa-agentctl"
    fi
    [ -s "$STAGE/oa-agent/bin/$bin" ] || die "$bin was not produced"
    chmod 755 "$STAGE/oa-agent/bin/$bin"
done
(cd "$STAGE/oa-agent/bin" && sha256sum oa-agentctl-linux-* >SHA256SUMS)

echo "==> collecting sources"
(cd "$ROOT" && tar --exclude=./.git --exclude=node_modules --exclude=.venv --exclude=__pycache__ --exclude=./frontend/dist \
    --exclude=./dist --exclude=./data --exclude=./.tools --exclude=./.idea --exclude=./backend/bin --exclude=./tests/samples \
    --exclude='.env' -cf - .) | (cd "$STAGE/oa-agent" && tar -xf -)

echo "==> packing"
rm -f "$OUT/oa-agent-source.tar.gz" "$OUT/oa-agent-source.tar.gz.sha256" "$OUT/SHA256SUMS"
tar -C "$STAGE" -czf "$OUT/oa-agent-source.tar.gz" oa-agent
cp "$HERE/install.sh" "$OUT/install.sh"
for a in $ARCHS; do cp "$STAGE/oa-agent/bin/oa-agentctl-linux-$a" "$OUT/"; done
(cd "$OUT" && sha256sum oa-agent-source.tar.gz >oa-agent-source.tar.gz.sha256 &&
    sha256sum oa-agent-source.tar.gz install.sh oa-agentctl-linux-* >SHA256SUMS)
echo "==> done: $OUT"
ls -l "$OUT"
