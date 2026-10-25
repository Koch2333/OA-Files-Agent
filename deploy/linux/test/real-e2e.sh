#!/usr/bin/env bash
# real-e2e.sh - installer acceptance run against a REAL Docker engine, executed INSIDE the host container made by docker-host.sh
# (deploy/linux/test/docker-host.sh e2e). Start the host with  OA_HOST_DOCKER_ARGS="--network host"  so that ss sees the ports other
# containers publish. Prerequisites (built by the caller, nothing is pulled from the internet):
#   - a registry on localhost:15000 holding  localhost:15000/koch2333/oa-agent-{api,worker,deploy}:<TAG1> and :<TAG2>
#     (api carries the current oa-admin with db-check/db-init; deploy carries this checkout's install.sh/compose files)
#   - the image postgres:16-alpine (used for the "existing database server")
# Scenarios (each prints PASS/FAIL lines; the exit code is the number of failures):
#   1 fresh interactive install through a pty, piped like "cat install.sh | bash" (answers come from /dev/tty), a busy port
#     held by a dummy container is detected, named and refused, the review screen edit works
#   2 re-run on the healthy installation: fast status path (no pull, no recreate)
#   3 port conflict detection: --check names the container that owns the port, exit 3
#   4 upgrade decisions: newer tag without --upgrade stays; --upgrade moves; a downgrade is refused
#   5 existing PostgreSQL (a postgres:16-alpine container): wrong password loop, isolation, DDL preview + approval, install
#   6 uninstall keeping data (containers removed, data and .env kept, state entry marked removed)
# Environment: OA_TEST_ROOT (shared tree), OA_TEST_PREFIX (default inst), TAG1 (default 1.0.0), TAG2 (default 1.1.0)
set -u
PREFIX="${OA_TEST_PREFIX:-inst}"; ROOT="${OA_TEST_ROOT:-/var/lib/oa-inst-test}"
TAG1="${TAG1:-1.0.0}"; TAG2="${TAG2:-1.1.0}"; REG="localhost:15000/koch2333"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
P1="$PREFIX-oa"; P2="$PREFIX-ext"
A="$ROOT/a/opt/oa-agent"; B="$ROOT/b/opt/oa-agent"
FAILS=0; LOG="$ROOT/e2e.log"; mkdir -p "$ROOT/tmp"; : >"$LOG"
export TMPDIR="$ROOT/tmp" OA_LANG=en
pass() { printf '  PASS %s\n' "$1" | tee -a "$LOG"; }
fail() { printf '  FAIL %s\n' "$1" | tee -a "$LOG"; FAILS=$((FAILS + 1)); }
check() { local why="$1"; shift; if "$@"; then pass "$why"; else fail "$why"; fi; }
has() { grep -Eq -- "$2" "$1"; }
checkgrep() { check "$1" has "$2" "$3"; }
step() { printf '\n== %s\n' "$*" | tee -a "$LOG"; }
inst() { # inst ARGS...: the installer taken from the deploy image, exactly as a customer would run it
    bash "$ROOT/install.sh" --lang en --registry "$REG" "$@"
}

step "0 prepare: installer from the deploy image, dummy container holding port 18080"
docker run --rm "$REG/oa-agent-deploy:$TAG1" >"$ROOT/install.sh" && pass "docker run --rm deploy image > install.sh ($(wc -c <"$ROOT/install.sh") bytes)" || fail "cannot get install.sh from the registry"
docker rm -fv "$PREFIX-dummy" >/dev/null 2>&1; docker run -d --name "$PREFIX-dummy" -p 18080:80 alpine:3.21 sleep 36000 >/dev/null && pass "dummy container holds 18080" || fail "dummy container"
mkdir -p "$ROOT/a/srv/archive" "$ROOT/a/srv/nas" "$ROOT/b/srv/archive"

sel() { [ -z "${ONLY:-}" ] || [ "$ONLY" = "$1" ]; }   # ONLY=5 runs a single scenario (scenario 0 always runs)
if sel 1; then
step "1 fresh interactive install through a pty (piped), busy port refused, review edit"
export INSTALLER="$ROOT/install.sh" PROJECT="$P1" ROOTDIR="$ROOT" REGISTRY="$REG" TAG="$TAG1" PORT=18080 PORT2=18081 LANG_UI=en
expect -f "$HERE/interactive.exp" >"$ROOT/e2e-1.txt" 2>&1; rc=$?
tail -n 45 "$ROOT/e2e-1.txt" | sed 's/\x1b\[[0-9;]*m//g' | tee -a "$LOG" >/dev/null
check "expect-driven wizard finished (rc $rc)" test "$rc" = 0
checkgrep "the busy port is named as a Docker container" "$ROOT/e2e-1.txt" "port 18080 is already in use by Docker container $PREFIX-dummy"
check "api/worker/postgres of project $P1 are running" test "$(docker ps --filter "label=com.docker.compose.project=$P1" --format '{{.Names}}' | wc -l)" = 3
check "api container is healthy" test "$(docker inspect -f '{{.State.Health.Status}}' "$P1-api-1" 2>/dev/null)" = healthy
check ".env is mode 600" test "$(stat -c %a "$A/.env")" = 600
checkgrep "state file records the install" /etc/oa-agent/install.json "\"dir\":\"$A\".*\"version\":\"$TAG1\""
checkgrep ".env keeps the chosen port 18081" "$A/.env" '^OA_PORT=18081$'
check "api answers /healthz on 18081" curl -fsS -m 10 http://127.0.0.1:18081/healthz

fi; if sel 2; then
step "2 re-run on the healthy installation: fast status path"
ids1="$(docker ps -q --filter "label=com.docker.compose.project=$P1" | sort | tr '\n' ' ')"; t0=$(date +%s)
inst --yes --dir "$A" >"$ROOT/e2e-2.txt" 2>&1; rc=$?; t1=$(date +%s)
check "exit 0" test "$rc" = 0
checkgrep "says it is already installed and healthy" "$ROOT/e2e-2.txt" 'Existing installation detected: .*all services are healthy'
check "no pull / no recreate" test "$ids1" = "$(docker ps -q --filter "label=com.docker.compose.project=$P1" | sort | tr '\n' ' ')"
check "took under 30 seconds ($((t1 - t0))s)" test $((t1 - t0)) -lt 30
check "no image pulling in the output" bash -c "! grep -q 'Pulling images' '$ROOT/e2e-2.txt'"
echo "--- status path output ---" >>"$LOG"; sed 's/\x1b\[[0-9;]*m//g' "$ROOT/e2e-2.txt" | tail -n 25 >>"$LOG"

fi; if sel 3; then
step "3 port conflict detection (--check)"
inst --check --dir "$ROOT/c/opt/oa-agent" --port 18080 >"$ROOT/e2e-3.txt" 2>&1; rc=$?
check "exit 3" test "$rc" = 3
checkgrep "owner named" "$ROOT/e2e-3.txt" "Port 18080 is already in use \(Docker container $PREFIX-dummy"
checkgrep "free port suggested" "$ROOT/e2e-3.txt" 'suggestion: 1808[1-9]'
check "nothing created" test ! -e "$ROOT/c"

fi; if sel 4; then
step "4 upgrade decisions ($TAG1 -> $TAG2)"
inst --yes --dir "$A" --image-tag "$TAG2" >"$ROOT/e2e-4a.txt" 2>&1; rc=$?
check "newer tag without --upgrade: stays ($rc)" test "$rc" = 0
checkgrep "says the tag stays" "$ROOT/e2e-4a.txt" "installed image tag is $TAG1 and stays"
inst --yes --dir "$A" --upgrade --image-tag "$TAG2" >"$ROOT/e2e-4b.txt" 2>&1; rc=$?
check "--upgrade succeeds ($rc)" test "$rc" = 0
checkgrep ".env now at $TAG2" "$A/.env" "^OA_VERSION=$TAG2\$"
check "api runs the $TAG2 image" bash -c "docker inspect -f '{{.Config.Image}}' $P1-api-1 | grep -q ':$TAG2\$'"
checkgrep "state file follows the upgrade" /etc/oa-agent/install.json "\"dir\":\"$A\".*\"version\":\"$TAG2\""
inst --yes --dir "$A" --upgrade --image-tag "$TAG1" >"$ROOT/e2e-4c.txt" 2>&1; rc=$?
check "downgrade refused (exit 2)" test "$rc" = 2
checkgrep "explains why" "$ROOT/e2e-4c.txt" 'Refusing to downgrade: installed 1.1.0, requested 1.0.0'
check "api still healthy after the refused downgrade" test "$(docker inspect -f '{{.State.Health.Status}}' "$P1-api-1" 2>/dev/null)" = healthy

fi; if sel 5; then
step "5 existing PostgreSQL (container from postgres:16-alpine): wrong password loop, DDL approval"
docker rm -fv "$PREFIX-pgext" >/dev/null 2>&1; docker run -d --name "$PREFIX-pgext" -p 55442:5432 -e POSTGRES_PASSWORD=pgadmin postgres:16-alpine >/dev/null
for _ in $(seq 1 30); do docker exec "$PREFIX-pgext" pg_isready -U postgres >/dev/null 2>&1 && break; sleep 1; done; sleep 2
docker exec "$PREFIX-pgext" psql -U postgres -v ON_ERROR_STOP=1 -c "CREATE ROLE oa LOGIN PASSWORD 'ExtPw12345' NOSUPERUSER NOCREATEDB NOCREATEROLE" -c "CREATE DATABASE oa OWNER oa" -c "CREATE DATABASE dirty OWNER oa" >/dev/null
docker exec "$PREFIX-pgext" psql -U oa -d dirty -c "CREATE TABLE orders (id int)" >/dev/null
DBIP=host.docker.internal   # a database server reachable the way a real one is: through the host (published port 55442), by the check container and by the compose network alike
printf '  database server %s\n' "$DBIP" | tee -a "$LOG"
export DBPORT=55442 INSTALLER="$ROOT/install.sh" PROJECT="$P2" ROOTDIR="$ROOT" REGISTRY="$REG" TAG="$TAG1" DBIP DBPW_WRONG=wrongpassword DBPW=ExtPw12345 PORTX=18090 INSTDIR="$B" ARCHIVEDIR="$ROOT/b/srv/archive"
expect -f "$HERE/interactive-db.exp" >"$ROOT/e2e-5.txt" 2>&1; rc=$?
tail -n 60 "$ROOT/e2e-5.txt" | sed 's/\x1b\[[0-9;]*m//g' | tee -a "$LOG" >/dev/null
check "external-database wizard finished (rc $rc)" test "$rc" = 0
checkgrep "wrong password: precise reason shown" "$ROOT/e2e-5.txt" 'password authentication failed for user "oa"'
checkgrep "non-isolated database refused with the foreign tables named" "$ROOT/e2e-5.txt" 'orders'
check "only api and worker run for $P2 (no bundled postgres)" test "$(docker ps --filter "label=com.docker.compose.project=$P2" --format '{{.Names}}' | sort | tr '\n' ' ')" = "$P2-api-1 $P2-worker-1 "
check "tables were created in the external database" bash -c "[ \"\$(docker exec $PREFIX-pgext psql -U oa -d oa -tAc \"select count(*) from information_schema.tables where table_schema='public'\")\" -gt 5 ]"
checkgrep ".env uses the external layer" "$B/.env" '^COMPOSE_FILE=.*docker-compose.external-db.yml$'
checkgrep ".env has the password in a 0600 file only" "$B/.env" "^OA_DB_PASSWORD='ExtPw12345'\$"
check ".env mode 600" test "$(stat -c %a "$B/.env")" = 600
check "no PostgreSQL data was created (the empty directory comes from oa-agentctl mounts apply)" test ! -e "$B/data/pg/pgdata"
check "the password is in no log, state file or screen capture" bash -c "! grep -rqF ExtPw12345 '$ROOT/e2e-5.txt' /etc/oa-agent/install.json /var/log/oa-agent-install.log"
check "the password is not in any process argument of the check container runs" bash -c "! docker ps -a --no-trunc --format '{{.Command}}' | grep -qF ExtPw12345"
check "api of $P2 healthy" test "$(docker inspect -f '{{.State.Health.Status}}' "$P2-api-1" 2>/dev/null)" = healthy
check "api of $P2 talks to the external database (healthz)" curl -fsS -m 10 http://127.0.0.1:18090/healthz
inst --yes --dir "$B" >"$ROOT/e2e-5b.txt" 2>&1; check "re-run on the external-database install is the fast status path" has "$ROOT/e2e-5b.txt" 'database external:postgres'

fi; if sel 6; then
step "6 uninstall keeping data"
inst --yes --dir "$B" --uninstall >"$ROOT/e2e-6b.txt" 2>&1; rc=$?; check "external install removed (rc $rc)" test "$rc" = 0
inst --yes --dir "$A" --uninstall >"$ROOT/e2e-6a.txt" 2>&1; rc=$?; check "first install removed (rc $rc)" test "$rc" = 0
check "no container of either project is left" test -z "$(docker ps -aq --filter "label=com.docker.compose.project=$P1" --filter "label=com.docker.compose.project=$P2")"
check ".env and data directories were kept" test -f "$A/.env" -a -d "$A/data/pg" -a -f "$B/.env"
checkgrep "state entries marked removed" /etc/oa-agent/install.json "\"dir\":\"$A\".*\"status\":\"removed\""
inst --yes --dir "$A" >"$ROOT/e2e-6c.txt" 2>&1; check "re-running on the removed install repairs it (data and key intact, rc $?)" test -f "$A/.env"
check "the old data was reused: api healthy again" bash -c "sleep 5; [ \"\$(docker inspect -f '{{.State.Health.Status}}' $P1-api-1 2>/dev/null)\" = healthy ]"

fi

printf '\nreal-e2e: %d failure(s); log %s\n' "$FAILS" "$LOG"
exit "$FAILS"
