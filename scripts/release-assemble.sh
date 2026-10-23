#!/usr/bin/env bash
# Final assembly of a release folder: verify that DIR holds exactly the contracted assets (scripts/release-assets.sh) and write
# one SHA256SUMS ("hash  filename", sorted by name, C locale) that covers every asset except itself.
#   scripts/release-assemble.sh --version 1.2.3 --dir DIR
# Fails (exit 1) when an asset is missing or empty, or when an unexpected file is present (a half-built release is never published).
set -Eeuo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
V=''; DIR=''
while [ $# -gt 0 ]; do
    case "$1" in
        --version) V="${2:?}"; shift 2 ;;
        --dir) DIR="${2:?}"; shift 2 ;;
        -h | --help) sed -n '2,6p' "${BASH_SOURCE[0]}"; exit 0 ;;
        *) echo "release-assemble: unknown option $1" >&2; exit 2 ;;
    esac
done
[ -n "$V" ] && [ -d "$DIR" ] || { echo "release-assemble: --version and an existing --dir are required" >&2; exit 2; }
rm -f "$DIR/SHA256SUMS"
mapfile -t WANT < <(bash "$HERE/release-assets.sh" --version "$V" | grep -vx SHA256SUMS)
bad=0
for f in "${WANT[@]}"; do
    if [ ! -f "$DIR/$f" ]; then echo "release-assemble: MISSING $f" >&2; bad=1
    elif [ ! -s "$DIR/$f" ]; then echo "release-assemble: EMPTY $f" >&2; bad=1; fi
done
while IFS= read -r f; do
    f="${f#"$DIR"/}"
    printf '%s\n' "${WANT[@]}" | grep -qxF -- "$f" || { echo "release-assemble: UNEXPECTED $f" >&2; bad=1; }
done < <(find "$DIR" -mindepth 1 | LC_ALL=C sort)
[ "$bad" = 0 ] || { echo "release-assemble: the asset set for $V is not exactly the contract (see above)" >&2; exit 1; }
(cd "$DIR" && printf '%s\n' "${WANT[@]}" | LC_ALL=C sort | xargs sha256sum >SHA256SUMS)
echo "release-assemble: ${#WANT[@]} assets + SHA256SUMS in $DIR"
