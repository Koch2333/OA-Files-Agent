#!/usr/bin/env bash
# Generates the GitHub Release notes (Markdown, stdout) from scripts/release-notes.md.tmpl.
#   scripts/release-notes.sh --version 1.2.3 [--tag v1.2.3] [--repo Koch2333/OA-Files-Agent] [--registry HOST/NS] [--prerelease]
# --registry fills the docker commands (CI passes ACR_REGISTRY/ACR_NAMESPACE when configured); without it a placeholder is printed.
# --prerelease adds the pre-release banner and omits the <major>.<minor> image tag (images.yml/push-images.sh do not push it).
set -Eeuo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
V=''; TAG=''; REPO='Koch2333/OA-Files-Agent'; REG=''; PRE=0
while [ $# -gt 0 ]; do
    case "$1" in
        --version) V="${2:?}"; shift 2 ;;
        --tag) TAG="${2:?}"; shift 2 ;;
        --repo) REPO="${2:?}"; shift 2 ;;
        --registry) REG="${2?}"; shift 2 ;;
        --prerelease) PRE=1; shift ;;
        -h | --help) sed -n '2,5p' "${BASH_SOURCE[0]}"; exit 0 ;;
        *) echo "release-notes: unknown option $1" >&2; exit 2 ;;
    esac
done
V="${V#v}"
[[ "$V" =~ ^([0-9]+)\.([0-9]+)\.[0-9]+(-[0-9A-Za-z.+-]+)?$ ]] || { echo "release-notes: --version must be semantic (got '$V')" >&2; exit 2; }
MAJMIN="${BASH_REMATCH[1]}.${BASH_REMATCH[2]}"
case "$V" in *-*) PRE=1 ;; esac
TAG="${TAG:-v$V}"
[ -n "$REG" ] || REG='<仓库地址>/<命名空间>'
banner=''; mm=" 与 \`$MAJMIN\`"
if [ "$PRE" = 1 ]; then
    banner=$'\n> **预发布版本**：用于测试，不建议用于生产环境。\n'
    mm=''
fi
out="$(cat "$HERE/release-notes.md.tmpl")"
out="${out//@PRERELEASE_BANNER@/$banner}"
out="${out//@MAJMIN_NOTE@/$mm}"
out="${out//@VERSION@/$V}"
out="${out//@TAG@/$TAG}"
out="${out//@REPO@/$REPO}"
out="${out//@REGISTRY@/$REG}"
printf '%s\n' "$out"
