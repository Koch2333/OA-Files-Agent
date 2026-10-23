#!/usr/bin/env bash
# Linux release (Docker Compose edition): thin wrapper around deploy/linux/build-release.sh.
#   scripts/dist-linux.sh [--version V] [--out DIR] [--archs "amd64 arm64"]
# Defaults: version from scripts/version.sh, out dist/release/<version>.
set -Eeuo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"
REL="$ROOT/deploy/linux/build-release.sh"
[ -f "$REL" ] || { echo "dist-linux: $REL is missing (the Linux release script is provided by deploy/linux)." >&2; exit 1; }
VERSION=""; OUT=""; PASS=()
while [ $# -gt 0 ]; do
    case "$1" in
        --version) VERSION="${2:?}"; shift 2 ;;
        --out) OUT="${2:?}"; shift 2 ;;
        -h | --help) sed -n '2,5p' "${BASH_SOURCE[0]}"; exit 0 ;;
        *) PASS+=("$1"); shift ;;
    esac
done
if [ -z "$VERSION" ]; then eval "$(bash "$HERE/version.sh" --export)"; VERSION="${OA_VERSION:?version.sh did not print OA_VERSION}"; fi  # run through bash: a Windows checkout has no exec bit
OUT="${OUT:-$ROOT/dist/release/$VERSION}"
echo "==> Linux release $VERSION -> $OUT"
bash "$REL" --version "$VERSION" --out "$OUT" "${PASS[@]}"
ls -la "$OUT"
