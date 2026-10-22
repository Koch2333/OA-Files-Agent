#!/usr/bin/env bash
# Builds the Go backend in debug or release mode.
#   scripts/build-backend.sh [debug|release] [output-dir]      (default: release, backend/bin)
# debug  : -tags debug (enables logx.Debugf lines), optimisations/inlining off for debuggers (dlv), symbols kept.
# release: -trimpath, symbols stripped, no debug logging code in the binary.
set -euo pipefail
mode="${1:-release}"
root="$(cd "$(dirname "$0")/.." && pwd)"
out="${2:-$root/backend/bin}"
ext=""; [ "$(go env GOOS)" = "windows" ] && ext=".exe"
version="${OA_VERSION:-$(git -C "$root" describe --tags --always --dirty 2>/dev/null || echo dev)}"
commit="$(git -C "$root" rev-parse --short HEAD 2>/dev/null || echo unknown)"
pkg="oa-agent/backend/internal/buildinfo"
mkdir -p "$out"
cd "$root/backend"
case "$mode" in
  debug)
    go build -tags debug -gcflags "all=-N -l" -ldflags "-X $pkg.Version=$version -X $pkg.Commit=$commit" -o "$out/oa-agent-debug$ext" ./cmd/oa-agent
    echo "built $out/oa-agent-debug$ext (debug logging on; OA_LOG_LEVEL=info silences it)" ;;
  release)
    go build -trimpath -ldflags "-s -w -X $pkg.Version=$version -X $pkg.Commit=$commit" -o "$out/oa-agent$ext" ./cmd/oa-agent
    echo "built $out/oa-agent$ext" ;;
  *) echo "usage: $0 [debug|release] [output-dir]" >&2; exit 2 ;;
esac
