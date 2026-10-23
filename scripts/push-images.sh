#!/usr/bin/env bash
# Build (and push) the three OA Agent images: api, worker, deploy. Used by hand and for local registry tests; the GitHub
# workflow .github/workflows/images.yml uses docker/metadata-action for the tags and calls this script only for the shared
# version/commit rules (--print-meta) and a tag-parity check (--print-tags), so the rules live here once.
#
#   scripts/push-images.sh --registry HOST[/NS] [--push | --load] [options]
#
#   --registry R          R/oa-agent-<image>:<tag>  (default $OA_IMAGE_REGISTRY)
#   --images "a b c"      subset of: api worker deploy (default all three)
#   --ref REF             refs/heads/<branch> | refs/tags/v<semver> (default: current git ref)
#   --sha SHA             commit (default: git HEAD)
#   --default-branch B    branch that also gets "latest" (default main)
#   --version V           override OA_VERSION (default: tag without v, or <VERSION file>+<sha7>)
#   --extra-tag T         additional tag for every image (repeatable), e.g. a second tag for upgrade tests
#   --label K=V           additional image label (repeatable)
#   --platforms P         default linux/amd64 (worker on arm64 needs QEMU; api/deploy cross-compile)
#   --push | --load       push to the registry (default) | load into the local daemon (single platform only)
#   --print-tags          only print the tags (one per line) and exit;  --print-meta: OA_VERSION= / OA_COMMIT= lines
#   --dry-run             print the docker commands instead of running them
# Tags: sha-<7> always; <branch> (sanitised) on branch refs; latest on the default branch; <version> and <major>.<minor> on
# v<semver> tags (major.minor not for pre-releases). Build args: OA_VERSION, OA_COMMIT, GOPROXY / NPM_REGISTRY / PIP_INDEX_URL
# when OA_BUILD_GOPROXY / OA_BUILD_NPM_REGISTRY / OA_BUILD_PIP_INDEX are set. Registry credentials are never handled here:
# run "docker login" yourself (in CI: docker/login-action with repository secrets).
set -Eeuo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REG="${OA_IMAGE_REGISTRY:-}"; IMAGES="api worker deploy"; REF=''; SHA=''; DEFBR=main; VER=''; PLATFORMS=linux/amd64
MODE=--push; PRINT=''; DRY=0; EXTRA_TAGS=(); LABELS=()
read -r -a DOCKER_CMD <<<"${DOCKER:-docker}"
die() { printf 'push-images: %s\n' "$*" >&2; exit 1; }
while [ $# -gt 0 ]; do
    case "$1" in
        --registry) REG="${2:?}"; shift 2 ;;
        --images) IMAGES="${2:?}"; shift 2 ;;
        --ref) REF="${2:?}"; shift 2 ;;
        --sha) SHA="${2:?}"; shift 2 ;;
        --default-branch) DEFBR="${2:?}"; shift 2 ;;
        --version) VER="${2:?}"; shift 2 ;;
        --extra-tag) EXTRA_TAGS+=("${2:?}"); shift 2 ;;
        --label) LABELS+=("${2:?}"); shift 2 ;;
        --platforms) PLATFORMS="${2:?}"; shift 2 ;;
        --push | --load) MODE="$1"; shift ;;
        --print-tags) PRINT=tags; shift ;;
        --print-meta) PRINT=meta; shift ;;
        --dry-run) DRY=1; shift ;;
        -h | --help) sed -n '2,24p' "${BASH_SOURCE[0]}"; exit 0 ;;
        *) die "unknown option $1" ;;
    esac
done
[ -n "$REF" ] || REF="$(git -C "$ROOT" symbolic-ref -q HEAD 2>/dev/null || echo "refs/heads/$DEFBR")"
[ -n "$SHA" ] || SHA="$(git -C "$ROOT" rev-parse HEAD 2>/dev/null || echo 0000000000000000000000000000000000000000)"
[[ "$SHA" =~ ^[0-9a-f]{7,40}$ ]] || die "invalid --sha"
SHORT="${SHA:0:7}"

sanitize() { # docker tag charset [A-Za-z0-9_.-], no leading . or -, max 128
    local s="${1//[^A-Za-z0-9_.-]/-}"; s="${s#"${s%%[!.-]*}"}"; printf '%s' "${s:0:128}"
}
semver_re='^v?([0-9]+)\.([0-9]+)\.([0-9]+)(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$'
TAGS=("sha-$SHORT")
OAV=''
case "$REF" in
    refs/heads/*)
        br="${REF#refs/heads/}"; TAGS+=("$(sanitize "$br")")
        [ "$br" != "$DEFBR" ] || TAGS+=(latest)
        base="$(tr -d '[:space:]' <"$ROOT/VERSION" 2>/dev/null || true)"; OAV="${base:-0.0.0}+$SHORT" ;;
    refs/tags/*)
        tg="${REF#refs/tags/}"
        if [[ "$tg" == v* ]] && [[ "$tg" =~ $semver_re ]]; then
            full="${tg#v}"; full="${full%%+*}"; TAGS+=("$full")
            [ -n "${BASH_REMATCH[4]}" ] || TAGS+=("${BASH_REMATCH[1]}.${BASH_REMATCH[2]}")
            OAV="$full"
        else TAGS+=("$(sanitize "$tg")"); OAV="$(sanitize "${tg#v}")"; fi ;;
    *) die "unsupported --ref $REF" ;;
esac
[ -z "$VER" ] || OAV="$VER"
TAGS+=(${EXTRA_TAGS[@]+"${EXTRA_TAGS[@]}"})
mapfile -t TAGS < <(printf '%s\n' "${TAGS[@]}" | awk 'NF && !seen[$0]++')
case "$PRINT" in
    tags) printf '%s\n' "${TAGS[@]}"; exit 0 ;;
    meta) printf 'OA_VERSION=%s\nOA_COMMIT=%s\n' "$OAV" "$SHORT"; exit 0 ;;
esac

[ -n "$REG" ] || die "--registry (or OA_IMAGE_REGISTRY) is required"
[[ "$REG" =~ ^[A-Za-z0-9._-]+(:[0-9]{1,5})?(/[a-z0-9._/-]+)?$ ]] || die "invalid registry '$REG' (host[:port][/namespace], no scheme)"
REG="${REG%/}"
run() { if [ "$DRY" = 1 ]; then printf '+'; printf ' %q' "$@"; printf '\n'; else "$@"; fi; }
cd "$ROOT"
for img in $IMAGES; do
    case "$img" in
        api) df=deploy/docker/Dockerfile.api; args=(OA_VERSION="$OAV" OA_COMMIT="$SHORT" ${OA_BUILD_GOPROXY:+GOPROXY="$OA_BUILD_GOPROXY"} ${OA_BUILD_NPM_REGISTRY:+NPM_REGISTRY="$OA_BUILD_NPM_REGISTRY"}) ;;
        worker) df=deploy/docker/Dockerfile.worker; args=(OA_VERSION="$OAV" ${OA_BUILD_PIP_INDEX:+PIP_INDEX_URL="$OA_BUILD_PIP_INDEX"}) ;;
        deploy) df=deploy/docker/Dockerfile.deploy; args=(OA_VERSION="$OAV" OA_COMMIT="$SHORT" OA_REGISTRY_HINT="$REG" ${OA_BUILD_GOPROXY:+GOPROXY="$OA_BUILD_GOPROXY"}) ;;
        *) die "unknown image '$img' (api worker deploy)" ;;
    esac
    cmd=("${DOCKER_CMD[@]}" buildx build -f "$df" --platform "$PLATFORMS" --provenance=false --sbom=false "$MODE")
    for t in "${TAGS[@]}"; do cmd+=(-t "$REG/oa-agent-$img:$t"); done
    for a in "${args[@]}"; do cmd+=(--build-arg "$a"); done
    cmd+=(--label "org.opencontainers.image.revision=$SHA" --label "org.opencontainers.image.source=https://github.com/Koch2333/OA-Files-Agent")
    for l in ${LABELS[@]+"${LABELS[@]}"}; do cmd+=(--label "$l"); done
    echo "==> $img: $REG/oa-agent-$img:{$(IFS=,; echo "${TAGS[*]}")}  (OA_VERSION=$OAV, OA_COMMIT=$SHORT)"
    run "${cmd[@]}" .
done
