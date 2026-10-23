#!/usr/bin/env bash
# Offline test of deploy/linux/build-release.sh: runs it with a fake `go` (no compiler needed) and checks the per-architecture
# tarballs.  Run: bash scripts/test/linux-bundles.sh   (set REAL_GO=1 to use the go on PATH instead and cross-compile for real)
set -Eeuo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
T="$(mktemp -d)"
trap 'rm -rf -- "$T"' EXIT
pass=0; failn=0
ok() { pass=$((pass + 1)); printf 'ok   %s\n' "$1"; }
bad() { failn=$((failn + 1)); printf 'FAIL %s\n' "$1"; }
if [ "${REAL_GO:-0}" != 1 ]; then
    mkdir -p "$T/bin"
    cat >"$T/bin/go" <<'GO'
#!/usr/bin/env bash
# fake go build: writes an ELF-looking file named by -o, recording GOOS/GOARCH
out=''; while [ $# -gt 0 ]; do [ "$1" = -o ] && out="$2"; shift; done
printf 'fake oa-agentctl GOOS=%s GOARCH=%s CGO=%s\n' "$GOOS" "$GOARCH" "$CGO_ENABLED" >"$out"
GO
    chmod +x "$T/bin/go"; export PATH="$T/bin:$PATH"
fi
V=9.8.7
bash "$ROOT/deploy/linux/build-release.sh" --version $V --out "$T/out" >"$T/log" 2>&1 || { cat "$T/log"; exit 1; }
for a in amd64 arm64; do
    f="$T/out/oa-agent-$V-linux-$a.tar.gz"
    [ -s "$f" ] && ok "$a: tarball exists" || { bad "$a: tarball exists"; continue; }
    list="$(tar -tzf "$f")"
    top="oa-agent-$V-linux-$a"
    for e in install.sh oa-agentctl.sh docker-compose.yml docker-compose.https.yml docker-compose.build.yml docker-compose.mounts.example.yml Caddyfile.template .env.example oa-agent.service VERSION README.txt "bin/oa-agentctl-linux-$a" bin/SHA256SUMS; do
        printf '%s\n' "$list" | grep -qx "$top/$e" && ok "$a: contains $e" || bad "$a: contains $e"
    done
    other=amd64; [ "$a" = amd64 ] && other=arm64
    printf '%s\n' "$list" | grep -q "oa-agentctl-linux-$other" && bad "$a: no binary of the other arch" || ok "$a: no binary of the other arch"
    printf '%s\n' "$list" | grep -qv "^$top/" && bad "$a: single top folder" || ok "$a: single top folder"
    mkdir "$T/x$a"; tar -xzf "$f" -C "$T/x$a"
    (cd "$T/x$a/$top/bin" && sha256sum -c SHA256SUMS) >/dev/null 2>&1 && ok "$a: bin checksum verifies" || bad "$a: bin checksum verifies"
    [ -x "$T/x$a/$top/install.sh" ] && [ -x "$T/x$a/$top/bin/oa-agentctl-linux-$a" ] && ok "$a: executables" || bad "$a: executables"
    grep -qx "arch=$a" "$T/x$a/$top/VERSION" && grep -qx "version=$V" "$T/x$a/$top/VERSION" && ok "$a: VERSION file" || bad "$a: VERSION file"
    cmp -s "$ROOT/deploy/linux/install.sh" "$T/x$a/$top/install.sh" && ok "$a: install.sh identical to the repo copy" || bad "$a: install.sh identical"
    if [ "${REAL_GO:-0}" != 1 ]; then grep -q "GOARCH=$a" "$T/x$a/$top/bin/oa-agentctl-linux-$a" && ok "$a: built for $a" || bad "$a: built for $a"; fi
done
for f in install.sh oa-agentctl-linux-amd64 oa-agentctl-linux-arm64 oa-agent-source.tar.gz oa-agent-source.tar.gz.sha256 SHA256SUMS; do
    [ -s "$T/out/$f" ] && ok "out: $f" || bad "out: $f"
done
tar -tzf "$T/out/oa-agent-source.tar.gz" | grep -q 'oa-agent-.*-linux-' && bad 'source tarball does not contain the bundles' || ok 'source tarball does not contain the bundles'
(cd "$T/out" && sha256sum -c SHA256SUMS) >/dev/null 2>&1 && ok 'out: SHA256SUMS verifies' || bad 'out: SHA256SUMS verifies'
printf '\n%s passed, %s failed\n' "$pass" "$failn"
[ "$failn" = 0 ]
