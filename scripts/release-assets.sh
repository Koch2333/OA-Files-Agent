#!/usr/bin/env bash
# The release asset contract: the exact file names of a GitHub Release, one per line. Single source of truth for the build jobs
# (what to upload), release-assemble.sh (what must exist, nothing else may), release-notes.sh and release-publish.sh.
#   scripts/release-assets.sh --version 1.2.3 [--group all|windows-x64|windows-arm64|linux|sums]
# Groups: windows-<arch> = setup.exe, portable.zip, server.zip;  linux = tarballs, install.sh, oa-agentctl-*, source tarball (+ .sha256);
# all = every group plus SHA256SUMS.  Names never carry the leading "v" of the tag.
set -Eeuo pipefail
V=''; G=all
while [ $# -gt 0 ]; do
    case "$1" in
        --version) V="${2:?}"; shift 2 ;;
        --group) G="${2:?}"; shift 2 ;;
        -h | --help) sed -n '2,7p' "${BASH_SOURCE[0]}"; exit 0 ;;
        *) echo "release-assets: unknown option $1" >&2; exit 2 ;;
    esac
done
V="${V#v}"
[[ "$V" =~ ^[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.+-]+)?$ ]] || { echo "release-assets: --version must be semantic (got '$V')" >&2; exit 2; }
winset() { local a="$1"; printf 'oa-agent-%s-windows-%s-setup.exe\noa-agent-%s-windows-%s-portable.zip\noa-agent-%s-windows-%s-server.zip\n' "$V" "$a" "$V" "$a" "$V" "$a"; }
linux() {
    printf 'oa-agent-%s-linux-amd64.tar.gz\noa-agent-%s-linux-arm64.tar.gz\n' "$V" "$V"
    printf '%s\n' install.sh oa-agentctl-linux-amd64 oa-agentctl-linux-arm64 oa-agent-source.tar.gz oa-agent-source.tar.gz.sha256
}
case "$G" in
    windows-x64) winset x64 ;;
    windows-arm64) winset arm64 ;;
    linux) linux ;;
    sums) echo SHA256SUMS ;;
    all) { winset x64; winset arm64; linux; echo SHA256SUMS; } | LC_ALL=C sort ;;
    *) echo "release-assets: unknown group '$G'" >&2; exit 2 ;;
esac
