#!/usr/bin/env bash
# save-images.sh - pack the OA Agent images of one tag into ONE tarball (+ SHA-256) for hosts without registry access.
# 把一个标签的 OA Agent 镜像打成单个 tar（含 SHA-256），用于不能访问镜像仓库的主机（install.sh --offline）。
#
# Run it on a machine that CAN reach the registry (or already holds the images):
#   scripts/save-images.sh --registry HOST[/NS] --tag sha-0a1b2c3 [--out DIR] [--pull] [--arch amd64|arm64]
#                          [--no-postgres] [--with-caddy]
# Produces in DIR (default ./oa-agent-images):
#   oa-agent-images-<tag>-<arch>.tar          docker save of oa-agent-api, oa-agent-worker, oa-agent-deploy (+ the PostgreSQL base image,
#                                              + the Caddy image with --with-caddy, needed when the server will use a domain/HTTPS)
#   oa-agent-images-<tag>-<arch>.tar.sha256   checksum, verified here and again by install.sh --offline
#   install.sh                                the installer taken from the deploy image of that tag
# Copy the three files to the server, then:   sudo bash install.sh --offline oa-agent-images-<tag>-<arch>.tar [options]
#
#   --registry R     R/oa-agent-<image>:<tag> (default $OA_IMAGE_REGISTRY, else the production registry)
#   --tag T          image tag (default latest; pin sha-<7 chars> or a version for anything you keep)
#   --out DIR        output directory
#   --pull           pull fresh copies even when they exist locally (default: pull only what is missing)
#   --arch A         amd64|arm64: pull that platform (default: what the images already are / the local platform)
#   --no-postgres    leave out the PostgreSQL base image (OA_PG_IMAGE, default postgres:16-alpine)
#   --with-caddy     add the Caddy image (OA_CADDY_IMAGE, default caddy:2-alpine)
# The tarball holds no secrets (the images never do); registry credentials are never handled here: run "docker login" yourself.
set -Eeuo pipefail
umask 022

DEFAULT_REGISTRY='crpi-d6fmgwmrnsa2kgp8.cn-shanghai.personal.cr.aliyuncs.com/koch2333'
REG="${OA_IMAGE_REGISTRY:-$DEFAULT_REGISTRY}"; TAG=latest; OUT=./oa-agent-images; PULL=0; ARCH=''; WITH_PG=1; WITH_CADDY=0
read -r -a DOCKER_CMD <<<"${DOCKER:-docker}"
die() { printf 'save-images: %s\n' "$*" >&2; exit 1; }
d() { "${DOCKER_CMD[@]}" "$@"; }
usage() { awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "${BASH_SOURCE[0]}"; }
while [ $# -gt 0 ]; do
    case "$1" in
        --registry) REG="${2:?--registry needs a value}"; shift 2 ;;
        --tag) TAG="${2:?--tag needs a value}"; shift 2 ;;
        --out) OUT="${2:?--out needs a value}"; shift 2 ;;
        --arch) ARCH="${2:?--arch needs a value}"; shift 2 ;;
        --pull) PULL=1; shift ;;
        --no-postgres) WITH_PG=0; shift ;;
        --with-caddy) WITH_CADDY=1; shift ;;
        -h | --help) usage; exit 0 ;;
        *) usage >&2; die "unknown option $1" ;;
    esac
done
REG="${REG%/}"
[[ "$REG" =~ ^[A-Za-z0-9._-]+(:[0-9]{1,5})?(/[a-z0-9._/-]+)?$ ]] || die "invalid registry (host[:port][/namespace], no https://): $REG"
[[ "$TAG" =~ ^[A-Za-z0-9._-]{1,64}$ ]] || die "invalid tag: $TAG"
case "$ARCH" in '' | amd64 | arm64) ;; *) die "--arch must be amd64 or arm64" ;; esac
command -v "${DOCKER_CMD[0]}" >/dev/null 2>&1 || die "docker not found"
command -v sha256sum >/dev/null 2>&1 || die "sha256sum not found"
[ "$TAG" != latest ] || printf 'save-images: WARNING: tag "latest" moves; pin sha-<7 chars> or a version for a bundle you will keep\n' >&2

API="$REG/oa-agent-api:$TAG"; WORKER="$REG/oa-agent-worker:$TAG"; DEPLOY="$REG/oa-agent-deploy:$TAG"
IMAGES=("$API" "$WORKER" "$DEPLOY")
[ "$WITH_PG" = 0 ] || IMAGES+=("${OA_PG_IMAGE:-postgres:16-alpine}")
[ "$WITH_CADDY" = 0 ] || IMAGES+=("${OA_CADDY_IMAGE:-caddy:2-alpine}")

pull_one() { # three attempts: a flaky link is common, and docker resumes finished layers
    local img="$1" i=1 args=()
    [ -z "$ARCH" ] || args=(--platform "linux/$ARCH")
    while :; do
        if d pull ${args[@]+"${args[@]}"} "$img"; then return 0; fi
        [ "$i" -lt 3 ] || die "cannot pull $img (check the address/tag, docker login for private repositories, and the network)"
        printf 'save-images: pull of %s failed (attempt %s/3), retrying in %ss ...\n' "$img" "$i" "$((i * ${OA_RETRY_BASE:-5}))" >&2
        sleep "$((i * ${OA_RETRY_BASE:-5}))"; i=$((i + 1))
    done
}

for img in "${IMAGES[@]}"; do
    if [ "$PULL" = 1 ] || ! d image inspect "$img" >/dev/null 2>&1; then
        printf 'save-images: pulling %s\n' "$img"
        pull_one "$img"
    else
        printf 'save-images: using the local image %s (add --pull to refresh)\n' "$img"
    fi
done

# the three OA images must share one architecture, and it is part of the file name
arch_of() { d image inspect --format '{{.Architecture}}' "$1" 2>/dev/null || true; }
A1="$(arch_of "$API")"; A2="$(arch_of "$WORKER")"; A3="$(arch_of "$DEPLOY")"
[ -n "$A1" ] && [ "$A1" = "$A2" ] && [ "$A1" = "$A3" ] || die "api/worker/deploy are not the same architecture ($A1/$A2/$A3); use --arch with --pull"
[ -z "$ARCH" ] || [ "$ARCH" = "$A1" ] || die "the local images are $A1, not $ARCH; use --pull --arch $ARCH"
ARCH="$A1"

mkdir -p "$OUT"
NAME="oa-agent-images-$TAG-$ARCH.tar"
TARTMP="$OUT/.$NAME.tmp"
trap 'rm -f -- "$TARTMP" "$OUT/.install.sh.tmp"' EXIT
printf 'save-images: saving %s images into %s (this takes a while) ...\n' "${#IMAGES[@]}" "$OUT/$NAME"
d save -o "$TARTMP" "${IMAGES[@]}" || die "docker save failed"
[ -s "$TARTMP" ] || die "docker save produced an empty file"

# verify BEFORE publishing the final names: the tar lists every image, and the checksum round-trips
MANIFEST="$(tar -xOf "$TARTMP" manifest.json 2>/dev/null || true)"
[ -n "$MANIFEST" ] || die "the tarball has no manifest.json; refusing to publish it"
for img in "${IMAGES[@]}"; do
    case "$MANIFEST" in *"\"$img\""* | *"\"docker.io/library/$img\""*) ;; *) die "the tarball does not list $img; refusing to publish it" ;; esac
done
mv -f "$TARTMP" "$OUT/$NAME"
(cd "$OUT" && sha256sum "$NAME" >"$NAME.sha256" && sha256sum -c "$NAME.sha256" >/dev/null) || { rm -f -- "$OUT/$NAME" "$OUT/$NAME.sha256"; die "checksum verification failed"; }

if d run --rm "$DEPLOY" >"$OUT/.install.sh.tmp" 2>/dev/null && [ -s "$OUT/.install.sh.tmp" ]; then
    mv -f "$OUT/.install.sh.tmp" "$OUT/install.sh"; chmod 755 "$OUT/install.sh"
else
    printf 'save-images: WARNING: could not extract install.sh from %s; get it with: docker run --rm %s > install.sh\n' "$DEPLOY" "$DEPLOY" >&2
fi

printf '\nsave-images: done.\n  %s\n  %s\n  %s\n' "$OUT/$NAME" "$OUT/$NAME.sha256" "$OUT/install.sh"
printf 'On the server (copy the files together):\n  sudo bash install.sh --offline %s [--domain ... --archive-dir ... --yes]\n' "$NAME"
printf 'Upgrade later: save a new tarball and run  sudo bash install.sh --offline <new tar> --upgrade\n'
