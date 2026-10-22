#!/usr/bin/env bash
# Build the Vue frontend (npm ci + npm run build) into frontend/dist.   scripts/build-frontend.sh [--clean] [--skip-install]
set -Eeuo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CLEAN=0; SKIP=0
for a in "$@"; do case "$a" in --clean) CLEAN=1 ;; --skip-install) SKIP=1 ;; *) echo "unknown option $a" >&2; exit 2 ;; esac; done
echo "==> Frontend build"
cd "$ROOT/frontend"
[ "$CLEAN" = 1 ] && rm -rf dist node_modules
[ "$SKIP" = 1 ] || npm ci --no-audit --no-fund
npm run build
[ -f dist/index.html ] || { echo "frontend/dist/index.html missing" >&2; exit 1; }
