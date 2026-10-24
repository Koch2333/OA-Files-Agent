#!/usr/bin/env bash
# Runs the Go test suite against real PostgreSQL 16, MySQL 8.0 and SQL Server 2019, one engine at a time, reusing the
# dbm-* containers (created on first use with tmpfs data: nothing persists, no anonymous volumes) and stopping them
# afterwards. Usage: scripts/test-db-matrix.sh [postgres|mysql|mssql ...]   (default: all)
#   KEEP=1  leave containers running      REMOVE=1  remove the containers (docker rm -v) when done
set -euo pipefail
cd "$(dirname "$0")/../backend"
DOCKER=${DOCKER:-docker}
PGPW=dbmpass; MYPW=dbmroot; MSPW='Dbm_Passw0rd!x'
engines=("$@"); [ ${#engines[@]} -gt 0 ] || engines=(postgres mysql mssql)

ensure() { # name image port-map tmpfs-dir env...
  local name=$1 image=$2 port=$3 tmp=$4; shift 4
  if $DOCKER inspect "$name" >/dev/null 2>&1; then $DOCKER start "$name" >/dev/null
  else $DOCKER run -d --name "$name" -p "$port" --tmpfs "$tmp" "$@" "$image" >/dev/null; fi
}
wait_for() { local n=0; until "$@" >/dev/null 2>&1; do n=$((n+1)); [ $n -lt 90 ] || { echo "timeout waiting for $*" >&2; return 1; }; sleep 2; done; }

run() {
  case $1 in
    postgres)
      ensure dbm-pg16 postgres:16 127.0.0.1:55432:5432 /var/lib/postgresql/data -e POSTGRES_PASSWORD=$PGPW -e POSTGRES_USER=oa -e POSTGRES_DB=oa_test
      wait_for $DOCKER exec dbm-pg16 pg_isready -U oa -d oa_test
      OA_TEST_DATABASE_URL="postgres://oa:$PGPW@127.0.0.1:55432/oa_test?sslmode=disable" go test -count=1 -p 1 ./... ;;
    mysql)
      ensure dbm-mysql80 mysql:8.0 127.0.0.1:53307:3306 /var/lib/mysql -e MYSQL_ROOT_PASSWORD=$MYPW
      wait_for $DOCKER exec dbm-mysql80 mysqladmin -uroot -p$MYPW ping
      OA_TEST_MYSQL_URL="mysql://root:$MYPW@127.0.0.1:53307/" go test -count=1 -p 1 ./... ;;
    mssql)
      ensure dbm-mssql2019 mcr.microsoft.com/mssql/server:2019-latest 127.0.0.1:51433:1433 /var/opt/mssql -e ACCEPT_EULA=Y -e "MSSQL_SA_PASSWORD=$MSPW" -e MSSQL_PID=Developer
      wait_for $DOCKER exec dbm-mssql2019 /opt/mssql-tools18/bin/sqlcmd -C -S localhost -U sa -P "$MSPW" -Q "select 1"
      OA_TEST_MSSQL_URL="sqlserver://sa:Dbm_Passw0rd%21x@127.0.0.1:51433" go test -count=1 -p 1 ./... ;;
    *) echo "unknown engine $1" >&2; return 2 ;;
  esac
}
stop() { case $1 in postgres) n=dbm-pg16;; mysql) n=dbm-mysql80;; mssql) n=dbm-mssql2019;; esac
  if [ "${REMOVE:-0}" = 1 ]; then $DOCKER rm -f -v "$n" >/dev/null 2>&1 || true
  elif [ "${KEEP:-0}" != 1 ]; then $DOCKER stop "$n" >/dev/null 2>&1 || true; fi; }

rc=0
for e in "${engines[@]}"; do
  echo "=== $e ==="
  run "$e" || rc=1
  stop "$e"
done
exit $rc
