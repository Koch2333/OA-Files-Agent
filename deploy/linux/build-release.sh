#!/usr/bin/env bash
# Build the Linux release artefacts (no Makefile needed):
#   <out>/oa-agent-source.tar.gz         source tree + deploy files + bin/oa-agentctl-linux-{amd64,arm64} + bin/SHA256SUMS
#   <out>/oa-agent-source.tar.gz.sha256  checksum install.sh verifies when downloading
#   <out>/oa-agent-<version>-linux-{amd64,arm64}.tar.gz  per-architecture installer bundle (same layout as the oa-agent-deploy image's /deploy,
#                                        but only the matching bin/oa-agentctl-linux-<arch>): install.sh oa-agentctl.sh compose files
#                                        Caddyfile.template .env.example oa-agent.service bin/ VERSION README.txt, inside oa-agent-<version>-linux-<arch>/
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

echo "==> per-architecture installer bundles"
for a in $ARCHS; do
    name="oa-agent-$VERSION-linux-$a"
    b="$STAGE/bundle/$name"
    mkdir -p "$b/bin"
    for f in install.sh oa-agentctl.sh; do install -m 0755 "$HERE/$f" "$b/$f"; done
    for f in docker-compose.yml docker-compose.https.yml docker-compose.build.yml docker-compose.external-db.yml docker-compose.mounts.example.yml Caddyfile.template .env.example oa-agent.service; do
        install -m 0644 "$HERE/$f" "$b/$f"
    done
    install -m 0755 "$STAGE/oa-agent/bin/oa-agentctl-linux-$a" "$b/bin/"
    (cd "$b/bin" && sha256sum "oa-agentctl-linux-$a" >SHA256SUMS)
    printf 'version=%s\ncommit=%s\narch=%s\n' "$VERSION" "$COMMIT" "$a" >"$b/VERSION"
    cat >"$b/README.txt" <<README
OA Agent $VERSION - Linux $a installer bundle / Linux $a 安装包

  sudo bash install.sh                      interactive install (images are pulled from the registry; Docker required)
  sudo bash install.sh --yes --domain oa.example.com --image-tag $VERSION

The bundle contains the installer, oa-agentctl.sh, the compose files and the prebuilt bin/oa-agentctl-linux-$a
(checksum in bin/SHA256SUMS). Verify the download with the release SHA256SUMS first (sha256sum -c --ignore-missing).
Platforms: amd64 and arm64 only. Docs: docs/部署指南-Linux.md and docs/oa-agentctl.md in the source tarball.
VERSION lists version, commit and architecture of this bundle.
README
    chmod 0644 "$b/README.txt"
    (cd "$STAGE/bundle" && tar --sort=name --owner=0 --group=0 --numeric-owner -czf "$OUT/$name.tar.gz" "$name")
done

echo "==> packing"
rm -f "$OUT/oa-agent-source.tar.gz" "$OUT/oa-agent-source.tar.gz.sha256" "$OUT/SHA256SUMS"
tar -C "$STAGE" -czf "$OUT/oa-agent-source.tar.gz" oa-agent
cp "$HERE/install.sh" "$OUT/install.sh"
for a in $ARCHS; do cp "$STAGE/oa-agent/bin/oa-agentctl-linux-$a" "$OUT/"; done
(cd "$OUT" && sha256sum oa-agent-source.tar.gz >oa-agent-source.tar.gz.sha256 &&
    sha256sum oa-agent-source.tar.gz install.sh oa-agentctl-linux-* oa-agent-"$VERSION"-linux-*.tar.gz >SHA256SUMS)
echo "==> done: $OUT"
ls -l "$OUT"
