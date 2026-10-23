#!/usr/bin/env bash
# Offline tests for the release scripts (tag/VERSION gate, asset contract, SHA256SUMS assembly, release notes, publish flow
# against a fake gh).  Run:  bash scripts/test/release.sh      Needs only bash, coreutils, awk.
set -Eeuo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
S="$(cd "$HERE/.." && pwd)"
T="$(mktemp -d)"
trap 'rm -rf -- "$T"' EXIT
pass=0; failn=0
ok() { pass=$((pass + 1)); printf 'ok   %s\n' "$1"; }
bad() { failn=$((failn + 1)); printf 'FAIL %s\n' "$1"; }
check() { local name="$1"; shift; if "$@" >/dev/null 2>&1; then ok "$name"; else bad "$name"; fi; }
nocheck() { local name="$1"; shift; if "$@" >/dev/null 2>&1; then bad "$name"; else ok "$name"; fi; }

# ---- release-check.sh ----
echo 1.2.0 >"$T/VERSION"
rc() { bash "$S/release-check.sh" --version-file "$T/VERSION" "$@"; }
check 'check: matching tag' rc --tag v1.2.0
out="$(rc --tag v1.2.0)"; [ "$out" = $'tag=v1.2.0\nversion=1.2.0\nprerelease=false' ] && ok 'check: outputs' || bad 'check: outputs'
echo 1.2.0-rc.1 >"$T/VERSION"
out="$(rc --tag v1.2.0-rc.1)"; case "$out" in *prerelease=true*) ok 'check: rc is prerelease' ;; *) bad 'check: rc is prerelease' ;; esac
echo 1.2.0 >"$T/VERSION"
nocheck 'check: VERSION mismatch fails' rc --tag v1.2.1
msg="$(rc --tag v1.2.1 2>&1 || true)"; case "$msg" in *"Fix: set VERSION to 1.2.1"*) ok 'check: mismatch message tells the fix' ;; *) bad 'check: mismatch message tells the fix' ;; esac
for t in 1.2.0 v1.2 v1.2.0+meta vx v1.2.0-; do nocheck "check: bad tag $t" rc --tag "$t"; done
nocheck 'check: no tag' rc

# ---- release-assets.sh ----
V=1.2.3
n="$(bash "$S/release-assets.sh" --version v$V | wc -l)"; [ "$n" = 14 ] && ok 'assets: 14 files in total' || bad "assets: 14 files in total (got $n)"
for g in windows-x64:3 windows-arm64:3 linux:7 sums:1; do
    n="$(bash "$S/release-assets.sh" --version $V --group "${g%%:*}" | wc -l)"; [ "$n" = "${g##*:}" ] && ok "assets: group ${g%%:*}" || bad "assets: group ${g%%:*} ($n)"
done
nocheck 'assets: non-semver rejected' bash "$S/release-assets.sh" --version latest
want='oa-agent-1.2.3-windows-arm64-portable.zip oa-agent-1.2.3-windows-arm64-server.zip oa-agent-1.2.3-windows-arm64-setup.exe oa-agent-1.2.3-windows-x64-portable.zip oa-agent-1.2.3-windows-x64-server.zip oa-agent-1.2.3-windows-x64-setup.exe oa-agent-1.2.3-linux-amd64.tar.gz oa-agent-1.2.3-linux-arm64.tar.gz'
all="$(bash "$S/release-assets.sh" --version $V)"
miss=0; for f in $want; do printf '%s\n' "$all" | grep -qx "$f" || miss=1; done; [ "$miss" = 0 ] && ok 'assets: contract names' || bad 'assets: contract names'

# ---- release-assemble.sh ----
D="$T/assets"; mkdir -p "$D"
mk() { while IFS= read -r f; do [ "$f" = SHA256SUMS ] || printf 'data of %s\n' "$f" >"$D/$f"; done < <(bash "$S/release-assets.sh" --version $V); }
mk
check 'assemble: complete set' bash "$S/release-assemble.sh" --version $V --dir "$D"
(cd "$D" && sha256sum -c SHA256SUMS) >/dev/null 2>&1 && ok 'assemble: sha256sum -c passes' || bad 'assemble: sha256sum -c passes'
[ "$(wc -l <"$D/SHA256SUMS")" = 13 ] && ok 'assemble: 13 lines (every asset but itself)' || bad 'assemble: 13 lines'
[ "$(cut -d' ' -f3- <"$D/SHA256SUMS" | LC_ALL=C sort -c 2>&1; echo $?)" = 0 ] && ok 'assemble: sorted' || bad 'assemble: sorted'
grep -Eq '^[0-9a-f]{64}  [^ ]+$' "$D/SHA256SUMS" && ok 'assemble: "hash  name" format' || bad 'assemble: format'
check 'assemble: idempotent re-run' bash "$S/release-assemble.sh" --version $V --dir "$D"
mv "$D/oa-agent-$V-windows-arm64-setup.exe" "$T/keep"
nocheck 'assemble: missing asset fails' bash "$S/release-assemble.sh" --version $V --dir "$D"
mv "$T/keep" "$D/oa-agent-$V-windows-arm64-setup.exe"
: >"$D/oa-agent-$V-windows-x64-server.zip"
nocheck 'assemble: empty asset fails' bash "$S/release-assemble.sh" --version $V --dir "$D"
echo x >"$D/oa-agent-$V-windows-x64-server.zip"
echo x >"$D/stray.txt"
nocheck 'assemble: unexpected file fails' bash "$S/release-assemble.sh" --version $V --dir "$D"
rm "$D/stray.txt"
check 'assemble: ok again' bash "$S/release-assemble.sh" --version $V --dir "$D"

# ---- release-notes.sh ----
N="$(bash "$S/release-notes.sh" --version $V --tag v$V --repo o/r --registry reg.example.com/ns)"
miss=''; while IFS= read -r f; do printf '%s' "$N" | grep -qF -- "$f" || miss="$miss $f"; done < <(bash "$S/release-assets.sh" --version $V)
[ -z "$miss" ] && ok 'notes: every asset is mentioned' || bad "notes: not mentioned:$miss"
printf '%s' "$N" | grep -q '@[A-Z_]*@' && bad 'notes: unreplaced placeholder' || ok 'notes: no unreplaced placeholders'
for s in 'sha256sum -c' 'Get-FileHash' 'SmartScreen' '--image-tag 1.2.3' 'docker run --rm reg.example.com/ns/oa-agent-deploy:1.2.3' '实验性' 'releases/download/v1.2.3/install.sh' '`1.2`' 'riscv64'; do
    printf '%s' "$N" | grep -qF -- "$s" && ok "notes: contains $s" || bad "notes: contains $s"
done
printf '%s' "$N" | grep -q '预发布' && bad 'notes: stable has no prerelease banner' || ok 'notes: stable has no prerelease banner'
P="$(bash "$S/release-notes.sh" --version 1.3.0-rc.1)"
printf '%s' "$P" | grep -q '预发布' && ok 'notes: rc banner' || bad 'notes: rc banner'
printf '%s' "$P" | grep -qF '`1.3`' && bad 'notes: rc has no major.minor tag' || ok 'notes: rc has no major.minor tag'
printf '%s' "$P" | grep -qF '<仓库地址>/<命名空间>' && ok 'notes: registry placeholder' || bad 'notes: registry placeholder'

# ---- release-publish.sh against a fake gh ----
FAKE="$T/fake"; mkdir -p "$FAKE"
cat >"$FAKE/gh" <<'GH'
#!/usr/bin/env bash
# fake gh: state in $FAKE_STATE (exists, draft, assets); every call is logged to $FAKE_STATE/calls
set -e
S="$FAKE_STATE"; echo "$*" >>"$S/calls"
[ "$1" = release ] || exit 9
sub="$2"; shift 2
case "$sub" in
    view)
        [ -f "$S/exists" ] || exit 1
        case "$*" in *--json*) cat "$S/assets" 2>/dev/null || true ;; esac ;;
    create) touch "$S/exists"; echo draft >"$S/state"; : >"$S/assets" ;;
    edit)
        case "$*" in *--draft=false*) echo public >"$S/state" ;; *--draft*) echo draft >"$S/state" ;; esac ;;
    upload)
        shift; skip=0; for a in "$@"; do if [ "$skip" = 1 ]; then skip=0; continue; fi; case "$a" in --repo) skip=1 ;; --*) ;; *) b="$(basename "$a")"; [ "$b" = "${FAKE_DROP:-}" ] || { grep -qxF "$b" "$S/assets" || echo "$b" >>"$S/assets"; } ;; esac; done ;;
    delete-asset) grep -vxF "$2" "$S/assets" >"$S/assets.new" || true; mv "$S/assets.new" "$S/assets" ;;
    *) exit 9 ;;
esac
GH
chmod +x "$FAKE/gh"
export GH="$FAKE/gh"
echo notes >"$T/notes.md"
pub() { FAKE_STATE="$ST" bash "$S/release-publish.sh" --tag v$V --version $V --dir "$D" --notes "$T/notes.md" --repo o/r "$@"; }
ST="$T/st1"; mkdir -p "$ST"; export FAKE_STATE="$ST"
bash "$S/release-assemble.sh" --version $V --dir "$D" >/dev/null
check 'publish: new release' pub
[ "$(cat "$ST/state")" = public ] && ok 'publish: ends public' || bad 'publish: ends public'
[ "$(wc -l <"$ST/assets")" = 14 ] && ok 'publish: 14 assets uploaded' || bad 'publish: 14 assets uploaded'
grep -q '^release create v1.2.3 .*--draft' "$ST/calls" && ok 'publish: created as draft' || bad 'publish: created as draft'
grep -q 'release upload .*--clobber' "$ST/calls" && ok 'publish: upload --clobber' || bad 'publish: upload --clobber'
grep -q -- '--latest=true' "$ST/calls" && ok 'publish: stable is latest' || bad 'publish: stable is latest'
# re-run on a published release with a stale asset: back to draft, stale deleted, republished
echo old-asset.zip >>"$ST/assets"; : >"$ST/calls"
check 'publish: re-run idempotent' pub
grep -qxF old-asset.zip "$ST/assets" && bad 'publish: stale asset removed' || ok 'publish: stale asset removed'
first_edit="$(grep -n '^release edit' "$ST/calls" | head -1)"; case "$first_edit" in *--draft\ *|*--draft) ok 'publish: re-run drafts first' ;; *) bad "publish: re-run drafts first ($first_edit)" ;; esac
# half-uploaded: one asset missing after the upload -> failure, never published
ST="$T/st2"; mkdir -p "$ST"; export FAKE_STATE="$ST"
nocheck 'publish: incomplete upload fails' env FAKE_DROP="oa-agent-$V-linux-arm64.tar.gz" bash -c "FAKE_STATE='$ST' bash '$S/release-publish.sh' --tag v$V --version $V --dir '$D' --notes '$T/notes.md' --repo o/r"
[ "$(cat "$ST/state")" = draft ] && ok 'publish: incomplete release stays draft' || bad 'publish: incomplete release stays draft'
# prerelease
ST="$T/st3"; mkdir -p "$ST"; export FAKE_STATE="$ST"
check 'publish: prerelease' pub --prerelease
grep -q -- 'release create .*--prerelease' "$ST/calls" && grep -q -- '--latest=false' "$ST/calls" && ok 'publish: prerelease flags' || bad 'publish: prerelease flags'
# missing asset before anything is touched
ST="$T/st4"; mkdir -p "$ST"; export FAKE_STATE="$ST"
rm "$D/install.sh"
nocheck 'publish: missing local asset fails' pub
[ ! -f "$ST/calls" ] && ok 'publish: nothing called on missing asset' || bad 'publish: nothing called on missing asset'

printf '\n%s passed, %s failed\n' "$pass" "$failn"
[ "$failn" = 0 ]
