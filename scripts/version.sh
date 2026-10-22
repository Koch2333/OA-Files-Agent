#!/usr/bin/env bash
# Resolve the product version: --version arg, $OA_VERSION, VERSION file, then git describe (0.0.0-<describe>).
#   scripts/version.sh [--version X] [--export]     (--export prints shell assignments for eval)
set -Eeuo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
V=""; EXPORT=0
while [ $# -gt 0 ]; do
    case "$1" in
        --version) V="${2:?}"; shift 2 ;;
        --export) EXPORT=1; shift ;;
        *) echo "unknown option $1" >&2; exit 2 ;;
    esac
done
SRC="--version"
if [ -z "$V" ]; then V="${OA_VERSION:-}"; SRC="OA_VERSION"; fi
if [ -z "$V" ] && [ -f "$ROOT/VERSION" ]; then V="$(tr -d '[:space:]' <"$ROOT/VERSION")"; SRC="VERSION file"; fi
if [ -z "$V" ]; then V="0.0.0-$(git -C "$ROOT" describe --tags --always --dirty 2>/dev/null || echo unknown)"; SRC="git describe"; fi
V="${V#v}"; V="${V#V}"
[[ "$V" =~ ^[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.+-]+)?$ ]] || { echo "version '$V' is not semantic (MAJOR.MINOR.PATCH[-suffix])" >&2; exit 1; }
C="$(git -C "$ROOT" rev-parse --short HEAD 2>/dev/null || echo unknown)"
if [ "$EXPORT" = 1 ]; then printf 'export OA_VERSION=%q OA_COMMIT=%q\n' "$V" "$C"; else echo "version : $V  (from $SRC)"; echo "commit  : $C"; fi
