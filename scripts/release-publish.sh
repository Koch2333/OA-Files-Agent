#!/usr/bin/env bash
# Creates or updates the GitHub Release of a tag with the `gh` CLI (GITHUB_TOKEN / GH_TOKEN in the environment).
#   scripts/release-publish.sh --tag v1.2.3 --version 1.2.3 --dir DIR --notes FILE --repo OWNER/REPO [--prerelease]
# Idempotent and never half-public:
#   1. the release is created as a DRAFT (or, when it exists, switched back to draft and its notes refreshed);
#   2. assets that are not part of the contract are deleted, every contracted asset is uploaded with --clobber;
#   3. the asset list is verified against scripts/release-assets.sh (names and count);
#   4. only then the draft is published (--latest unless a pre-release).
# A failed run leaves a draft (invisible to users); re-running the workflow for the same tag simply repeats the steps.
# GH=<command> overrides the gh executable (tests use a fake).
set -Eeuo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TAG=''; V=''; DIR=''; NOTES=''; REPO=''; PRE=false
GH="${GH:-gh}"
while [ $# -gt 0 ]; do
    case "$1" in
        --tag) TAG="${2:?}"; shift 2 ;;
        --version) V="${2:?}"; shift 2 ;;
        --dir) DIR="${2:?}"; shift 2 ;;
        --notes) NOTES="${2:?}"; shift 2 ;;
        --repo) REPO="${2:?}"; shift 2 ;;
        --prerelease) PRE=true; shift ;;
        -h | --help) sed -n '2,11p' "${BASH_SOURCE[0]}"; exit 0 ;;
        *) echo "release-publish: unknown option $1" >&2; exit 2 ;;
    esac
done
[ -n "$TAG" ] && [ -n "$V" ] && [ -d "$DIR" ] && [ -f "$NOTES" ] && [ -n "$REPO" ] || { echo "release-publish: --tag --version --dir --notes --repo are required" >&2; exit 2; }
fail() { echo "release-publish: $*" >&2; exit 1; }
mapfile -t WANT < <(bash "$HERE/release-assets.sh" --version "$V")
for f in "${WANT[@]}"; do [ -s "$DIR/$f" ] || fail "asset $f is missing or empty in $DIR (run release-assemble.sh first)"; done
TITLE="OA 文件智能归档 $V"
latest=true; [ "$PRE" = true ] && latest=false

if "$GH" release view "$TAG" --repo "$REPO" >/dev/null 2>&1; then
    echo "==> release $TAG exists: back to draft, refreshing notes"
    "$GH" release edit "$TAG" --repo "$REPO" --draft --title "$TITLE" --notes-file "$NOTES" --prerelease="$PRE"
    while IFS= read -r a; do
        [ -n "$a" ] || continue
        printf '%s\n' "${WANT[@]}" | grep -qxF -- "$a" || { echo "==> deleting stale asset $a"; "$GH" release delete-asset "$TAG" "$a" --repo "$REPO" --yes; }
    done < <("$GH" release view "$TAG" --repo "$REPO" --json assets --jq '.assets[].name')
else
    echo "==> creating draft release $TAG"
    args=(release create "$TAG" --repo "$REPO" --draft --verify-tag --title "$TITLE" --notes-file "$NOTES")
    [ "$PRE" != true ] || args+=(--prerelease)
    "$GH" "${args[@]}"
fi

echo "==> uploading ${#WANT[@]} assets"
paths=(); for f in "${WANT[@]}"; do paths+=("$DIR/$f"); done
"$GH" release upload "$TAG" --repo "$REPO" --clobber "${paths[@]}"

echo "==> verifying the asset list"
got="$("$GH" release view "$TAG" --repo "$REPO" --json assets --jq '.assets[].name' | LC_ALL=C sort)"
want="$(printf '%s\n' "${WANT[@]}" | LC_ALL=C sort)"
if [ "$got" != "$want" ]; then
    echo "expected:" >&2; echo "$want" >&2; echo "found:" >&2; echo "$got" >&2
    fail "asset list of draft $TAG differs from the contract; the release stays a draft"
fi
[ "$(printf '%s\n' "$got" | wc -l | tr -d ' ')" = "${#WANT[@]}" ] || fail "asset count mismatch; the release stays a draft"

echo "==> publishing $TAG (prerelease=$PRE)"
"$GH" release edit "$TAG" --repo "$REPO" --draft=false --prerelease="$PRE" --latest="$latest"
echo "release-publish: $TAG published with ${#WANT[@]} assets"
