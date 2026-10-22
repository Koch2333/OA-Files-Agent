#!/usr/bin/env bash
# One-command development environment: PostgreSQL + document worker + API (debug build) + Vite dev server.
#   scripts/dev.sh up [--mock]   start everything (idempotent; --mock = frontend on demo data, no backend needed)
#   scripts/dev.sh down          stop API, worker and Vite (PostgreSQL stays up; use `down --all` to stop it too)
#   scripts/dev.sh status        what is running and where
#   scripts/dev.sh logs [api|worker|web|pg]   tail a log
#   scripts/dev.sh token         print the setup-wizard token from the API log
#   scripts/dev.sh reset         DROP and recreate the dev schema (asks first) and clear dev data
# Works on Linux/macOS (native go, node, python) and in WSL against Windows toolchains (go.exe, node via cmd.exe).
# Vite is bound to IPv4 127.0.0.1 on purpose: on some Windows hosts (security software) connections to [::1] fail with "Bad access".
# State lives in data/dev/ (git-ignored). Passkeys need http://localhost (not 127.0.0.1): open http://localhost:5173.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DEV="$ROOT/data/dev"
API_PORT="${OA_DEV_API_PORT:-8080}"
WORKER_PORT="${OA_DEV_WORKER_PORT:-8090}"
WEB_PORT="${OA_DEV_WEB_PORT:-5173}"
PG_PORT="${OA_DEV_PG_PORT:-5432}"
PG_DIR="${OA_DEV_PG_DIR:-$HOME/pg}"                 # an unpacked PostgreSQL (bin/ lib/ data/), as used on the author's machine
PG_HOST=127.0.0.1
DSN=""
mkdir -p "$DEV/spool" "$DEV/archive" "$DEV/logs" "$DEV/pids"

say() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33mwarning:\033[0m %s\n' "$*" >&2; }
die() { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

# ---- toolchain detection ------------------------------------------------------------------------------------------
WIN=0
GO=""; NPM=""; PY=""
if command -v go >/dev/null 2>&1; then GO=go
else
  for c in go.exe /mnt/c/Users/*/sdk/go/bin/go.exe "/mnt/c/Program Files/Go/bin/go.exe"; do
    if command -v "$c" >/dev/null 2>&1 || [ -x "$c" ]; then GO="$c"; WIN=1; break; fi
  done
fi
[ -n "$GO" ] || die "Go not found (need 1.26+). Install it or put go/go.exe on PATH."
if [ "$WIN" = 1 ]; then
  export PATH="$PATH:$(dirname "$(command -v "$GO" 2>/dev/null || echo "$GO")")"
  for d in "/mnt/c/Program Files/nodejs"; do [ -d "$d" ] && export PATH="$PATH:$d"; done
fi
wpath() { if [ "$WIN" = 1 ]; then wslpath -w "$1"; else printf '%s' "$1"; fi; }
if [ "$WIN" = 1 ]; then PY="$ROOT/workers/document-ai/.venv/Scripts/python.exe"; else PY="$ROOT/workers/document-ai/.venv/bin/python"; fi

# Run a Windows/POSIX command with extra environment variables forwarded (WSLENV for .exe processes).
withenv() { # withenv "VAR1 VAR2" cmd...
  local names="$1"; shift
  if [ "$WIN" = 1 ]; then
    local l=""; for n in $names; do l="$l:$n"; done
    WSLENV="${WSLENV:-}${l}" "$@"
  else "$@"; fi
}

port_pid() { # PID listening on a TCP port (empty when free)
  if [ "$WIN" = 1 ]; then
    cmd.exe /c "netstat -ano" 2>/dev/null | tr -d '\r' | awk -v p=":$1" '$1=="TCP" && $2 ~ p"$" && $4=="LISTENING" {print $5; exit}'
  elif command -v lsof >/dev/null 2>&1; then lsof -tiTCP:"$1" -sTCP:LISTEN 2>/dev/null | head -1
  else ss -ltnp 2>/dev/null | awk -v p=":$1" '$4 ~ p"$" {match($0,/pid=[0-9]+/); print substr($0,RSTART+4,RLENGTH-4); exit}'; fi
}
kill_pid() { if [ "$WIN" = 1 ]; then cmd.exe /c "taskkill /F /T /PID $1" >/dev/null 2>&1 || true; else kill "$1" 2>/dev/null || true; fi; }
wait_port() { local p="$1" t="${2:-60}" i=0; while [ -z "$(port_pid "$p")" ]; do i=$((i + 1)); [ "$i" -gt "$t" ] && return 1; sleep 1; done; }

# ---- shared secrets (generated once; dev only) ----------------------------------------------------------------------
TOKEN_FILE="$DEV/worker.token"
[ -s "$TOKEN_FILE" ] || { head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n' > "$TOKEN_FILE"; chmod 600 "$TOKEN_FILE"; }
WORKER_TOKEN="$(cat "$TOKEN_FILE")"

# ---- components ------------------------------------------------------------------------------------------------------
start_pg() {
  # WSL: Windows processes (go.exe) cannot reach a PostgreSQL bound to WSL's 127.0.0.1, so it also listens on the WSL address and
  # trusts only the Windows host (the default gateway) for user oa. Elsewhere plain 127.0.0.1 is used.
  local listen="127.0.0.1"
  if [ "$WIN" = 1 ] && [ -z "${OA_DEV_DATABASE_URL:-}" ]; then
    PG_HOST="$(hostname -I | awk '{print $1}')"; listen="127.0.0.1,$PG_HOST"
    local gw; gw="$(ip route show default | awk '{print $3; exit}')"
    if [ -n "$gw" ] && ! grep -q "oa-agent dev: Windows host" "$PG_DIR/data/pg_hba.conf" 2>/dev/null; then
      printf '# oa-agent dev: Windows host\nhost all oa %s/32 trust\n' "$gw" >> "$PG_DIR/data/pg_hba.conf"
    fi
  fi
  DSN="${OA_DEV_DATABASE_URL:-postgres://oa@$PG_HOST:$PG_PORT/postgres?sslmode=disable}"
  if [ -n "${OA_DEV_DATABASE_URL:-}" ]; then say "using OA_DEV_DATABASE_URL"; return; fi
  [ -x "$PG_DIR/bin/pg_ctl" ] || die "No PostgreSQL: start one yourself and export OA_DEV_DATABASE_URL, or unpack PostgreSQL 16 to $PG_DIR (bin lib data; user 'oa' with trust auth)."
  export LD_LIBRARY_PATH="$PG_DIR/lib"
  if "$PG_DIR/bin/pg_ctl" -D "$PG_DIR/data" status >/dev/null 2>&1; then
    if [ "$listen" = 127.0.0.1 ] || ss -ltn 2>/dev/null | grep -q "$PG_HOST:$PG_PORT"; then
      "$PG_DIR/bin/pg_ctl" -D "$PG_DIR/data" reload >/dev/null 2>&1 </dev/null; say "PostgreSQL already running"; return
    fi
    say "restarting PostgreSQL to listen on $PG_HOST"; "$PG_DIR/bin/pg_ctl" -D "$PG_DIR/data" stop -m fast >/dev/null 2>&1 </dev/null
  fi
  say "starting PostgreSQL ($PG_DIR, listening on $listen)"
  "$PG_DIR/bin/pg_ctl" -D "$PG_DIR/data" -l "$DEV/logs/pg.log" -o "-c listen_addresses=$listen -c port=$PG_PORT" start >"$DEV/logs/pg-start.out" 2>&1 </dev/null \
    || { cat "$DEV/logs/pg-start.out" >&2; die "pg_ctl failed (see $DEV/logs/pg.log)"; }
}

start_worker() {
  if [ -n "$(port_pid "$WORKER_PORT")" ]; then say "worker already listening on :$WORKER_PORT"; return; fi
  [ -x "$PY" ] || [ -f "$PY" ] || die "worker virtualenv missing: create it with  python -m venv workers/document-ai/.venv && .venv/.../pip install -r workers/document-ai/requirements.txt"
  say "starting document worker on :$WORKER_PORT"
  export OA_WORKER_TOKEN="$WORKER_TOKEN" OA_WORKER_PORT="$WORKER_PORT" OA_SPOOL_ROOT="$(wpath "$DEV/spool")" OA_ALLOWED_ROOTS="$(wpath "$DEV/archive")"
  ( cd "$ROOT/workers/document-ai" && withenv "OA_WORKER_TOKEN OA_WORKER_PORT OA_SPOOL_ROOT OA_ALLOWED_ROOTS" setsid nohup "$PY" server.py >"$DEV/logs/worker.log" 2>&1 </dev/null & )
  wait_port "$WORKER_PORT" 60 || die "worker did not start; see $DEV/logs/worker.log"
  port_pid "$WORKER_PORT" > "$DEV/pids/worker.pid"
}

start_api() {
  if [ -n "$(port_pid "$API_PORT")" ]; then say "API already listening on :$API_PORT"; return; fi
  say "building API (debug build)"
  ( cd "$ROOT/backend" && "$GO" build -tags debug -o "$(wpath "$DEV/oa-agent-dev$([ "$WIN" = 1 ] && echo .exe)")" ./cmd/oa-agent )
  [ -f "$ROOT/frontend/dist/index.html" ] && FE="$(wpath "$ROOT/frontend/dist")" || FE=""
  say "starting API on :$API_PORT (log: data/dev/logs/api.log)"
  export OA_DATABASE_URL="$DSN" OA_WORKER_TOKEN="$WORKER_TOKEN" OA_WORKER_URL="http://127.0.0.1:$WORKER_PORT" \
    OA_SPOOL_ROOT="$(wpath "$DEV/spool")" OA_ARCHIVE_ROOT="$(wpath "$DEV/archive")" OA_MIGRATIONS_DIR="$(wpath "$ROOT/migrations")" \
    OA_AUTO_MIGRATE=1 OA_LISTEN_ADDR="127.0.0.1:$API_PORT" OA_FRONTEND_DIR="$FE" OA_PUBLIC_URL="http://localhost:$WEB_PORT"
  local exe="$DEV/oa-agent-dev$([ "$WIN" = 1 ] && echo .exe)"
  ( cd "$DEV" && withenv "OA_DATABASE_URL OA_WORKER_TOKEN OA_WORKER_URL OA_SPOOL_ROOT OA_ARCHIVE_ROOT OA_MIGRATIONS_DIR OA_AUTO_MIGRATE OA_LISTEN_ADDR OA_FRONTEND_DIR OA_PUBLIC_URL" setsid nohup "$exe" >"$DEV/logs/api.log" 2>&1 </dev/null & )
  wait_port "$API_PORT" 60 || { tail -15 "$DEV/logs/api.log" >&2; die "API did not start; see $DEV/logs/api.log"; }
  port_pid "$API_PORT" > "$DEV/pids/api.pid"
}

start_web() {
  local mock="$1"
  if [ -n "$(port_pid "$WEB_PORT")" ]; then say "Vite already listening on :$WEB_PORT"; return; fi
  [ -d "$ROOT/frontend/node_modules" ] || { say "npm ci (first run)"; npm_run ci; }
  say "starting Vite on :$WEB_PORT ($([ "$mock" = 1 ] && echo 'mock/demo data' || echo "real backend :$API_PORT"))"
  export VITE_USE_MOCK=$([ "$mock" = 1 ] && echo true || echo false) OA_API_TARGET="http://127.0.0.1:$API_PORT"
  ( cd "$ROOT/frontend" && withenv "VITE_USE_MOCK OA_API_TARGET" setsid nohup $(npm_cmd) run dev -- --host 127.0.0.1 --port "$WEB_PORT" --strictPort >"$DEV/logs/web.log" 2>&1 </dev/null & )
  wait_port "$WEB_PORT" 60 || { tail -15 "$DEV/logs/web.log" >&2; die "Vite did not start; see $DEV/logs/web.log"; }
  port_pid "$WEB_PORT" > "$DEV/pids/web.pid"
}
npm_cmd() { if [ "$WIN" = 1 ]; then echo "cmd.exe /c npm"; else echo npm; fi; }
npm_run() { ( cd "$ROOT/frontend" && $(npm_cmd) "$@" ); }

cmd_up() {
  local mock=0; [ "${1:-}" = "--mock" ] && mock=1
  if [ "$mock" = 0 ]; then start_pg; start_worker; start_api; fi
  start_web "$mock"
  echo
  say "ready:  http://localhost:$WEB_PORT   (use localhost, not 127.0.0.1: passkeys need it)"
  if [ "$mock" = 0 ]; then
    echo "        API    http://localhost:$API_PORT   logs: scripts/dev.sh logs api"
    local tok; tok="$(grep -ao 'SETUP TOKEN: [A-Za-z0-9_-]*' "$DEV/logs/api.log" 2>/dev/null | tail -1 | cut -d' ' -f3 || true)"
    if [ -n "$tok" ]; then echo "        first run: complete the setup wizard with token  $tok"; fi
  else echo "        demo logins: admin / operator / viewer, password demo-password, TOTP 123456"; fi
}

cmd_down() {
  # Only stop what this script started (pid recorded at start and still the owner of the port). Anything else that
  # happens to listen there (an installed or extracted OA Agent on :8080, say) is left alone.
  local row n p rec cur
  for row in "web:$WEB_PORT" "api:$API_PORT" "worker:$WORKER_PORT"; do
    n="${row%%:*}"; p="${row##*:}"
    rec="$(cat "$DEV/pids/$n.pid" 2>/dev/null || true)"; cur="$(port_pid "$p")"
    if [ -n "$cur" ] && [ "$cur" = "$rec" ]; then say "stopping $n (pid $cur)"; kill_pid "$cur"
    elif [ -n "$cur" ]; then warn ":$p is in use by pid $cur, which this script did not start; left alone"; fi
    rm -f "$DEV/pids/$n.pid"
  done
  if [ "${1:-}" = "--all" ] && [ -x "$PG_DIR/bin/pg_ctl" ] && LD_LIBRARY_PATH="$PG_DIR/lib" "$PG_DIR/bin/pg_ctl" -D "$PG_DIR/data" status >/dev/null 2>&1; then
    say "stopping PostgreSQL"; LD_LIBRARY_PATH="$PG_DIR/lib" "$PG_DIR/bin/pg_ctl" -D "$PG_DIR/data" stop -m fast >/dev/null 2>&1 </dev/null || true
  fi
}

cmd_status() {
  for row in "PostgreSQL:$PG_PORT" "worker:$WORKER_PORT" "API:$API_PORT" "web (Vite):$WEB_PORT"; do
    local n="${row%%:*}" p="${row##*:}" pid
    if [ "$n" = PostgreSQL ] && [ -z "${OA_DEV_DATABASE_URL:-}" ] && [ -x "$PG_DIR/bin/pg_ctl" ]; then
      if LD_LIBRARY_PATH="$PG_DIR/lib" "$PG_DIR/bin/pg_ctl" -D "$PG_DIR/data" status >/dev/null 2>&1; then printf '  %-12s up    :%-5s (managed by pg_ctl)\n' "$n" "$p"; else printf '  %-12s DOWN  :%s\n' "$n" "$p"; fi
      continue
    fi
    pid="$(port_pid "$p")"
    if [ -n "$pid" ]; then printf '  %-12s up    :%-5s pid %s\n' "$n" "$p" "$pid"; else printf '  %-12s DOWN  :%s\n' "$n" "$p"; fi
  done
}

cmd_reset() {
  start_pg
  read -r -p "This DROPS all dev data (schema 'public' in $DSN and data/dev/spool+archive). Type 'reset' to continue: " a
  [ "$a" = reset ] || die "aborted"
  cmd_down
  rm -rf "$DEV/spool" "$DEV/archive"; mkdir -p "$DEV/spool" "$DEV/archive"
  ( cd "$ROOT/backend" && withenv "OA_DATABASE_URL OA_ALLOW_RESET" env OA_DATABASE_URL="$DSN" OA_ALLOW_RESET=1 "$GO" run ./cmd/migrate -reset -dir "$(wpath "$ROOT/migrations")" )
  say "reset done; run  scripts/dev.sh up"
}

case "${1:-}" in
  up) shift; cmd_up "$@" ;;
  down) shift; cmd_down "$@" ;;
  status) cmd_status ;;
  logs) f="${2:-api}"; case "$f" in api|worker|web|pg) tail -n 80 -f "$DEV/logs/$f.log" ;; *) die "logs: api|worker|web|pg" ;; esac ;;
  token) grep -ao 'SETUP TOKEN: [A-Za-z0-9_-]*' "$DEV/logs/api.log" | tail -1 | cut -d' ' -f3 ;;
  reset) cmd_reset ;;
  *) sed -n '2,12p' "$0"; exit 2 ;;
esac
