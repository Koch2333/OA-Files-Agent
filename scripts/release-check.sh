#!/usr/bin/env bash
# Release gate: the TAG is the source of truth for the release version, and the VERSION file must agree with it.
#   scripts/release-check.sh --tag v1.2.3 [--version-file VERSION]
# Prints  tag= / version= / prerelease=  lines (append them to $GITHUB_OUTPUT).  Exit 1 with a fix-it message when
#  - the tag is not  v<MAJOR>.<MINOR>.<PATCH>[-prerelease]   (no build metadata: "+" is not valid in image tags)
#  - the VERSION file differs from the tag without the leading v  (bump VERSION first, commit, then tag that commit).
# prerelease=true when the version has a "-" suffix (v1.2.0-rc.1).  version is the tag without "v" and is what every build job
# stamps as OA_VERSION, what the asset names contain and what images.yml pushes as the image tag.
set -Eeuo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TAG=''; VF="$ROOT/VERSION"
while [ $# -gt 0 ]; do
    case "$1" in
        --tag) TAG="${2:?}"; shift 2 ;;
        --version-file) VF="${2:?}"; shift 2 ;;
        -h | --help) sed -n '2,9p' "${BASH_SOURCE[0]}"; exit 0 ;;
        *) echo "release-check: unknown option $1" >&2; exit 2 ;;
    esac
done
fail() { echo "release-check: $*" >&2; exit 1; }
[ -n "$TAG" ] || fail "--tag is required"
[[ "$TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || fail "tag '$TAG' is not a release tag. Use v<MAJOR>.<MINOR>.<PATCH> or v<MAJOR>.<MINOR>.<PATCH>-<suffix> (for example v1.2.0 or v1.2.0-rc.1)."
VER="${TAG#v}"
[ -f "$VF" ] || fail "version file $VF is missing"
FILEV="$(tr -d '[:space:]' <"$VF")"
if [ "$FILEV" != "$VER" ]; then
    fail "tag $TAG says version $VER but the VERSION file says '$FILEV'. Fix: set VERSION to $VER, commit it, delete the wrong tag (git tag -d $TAG; git push origin :refs/tags/$TAG) and tag the new commit again."
fi
pre=false
case "$VER" in *-*) pre=true ;; esac
printf 'tag=%s\nversion=%s\nprerelease=%s\n' "$TAG" "$VER" "$pre"
