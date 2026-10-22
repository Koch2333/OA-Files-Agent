# One-command development environment for native Windows: PostgreSQL + document worker + API (debug build) + Vite.
#   powershell -ExecutionPolicy Bypass -File scripts\dev.ps1 up [-Mock]      start everything (idempotent; -Mock = frontend only, demo data)
#   powershell -ExecutionPolicy Bypass -File scripts\dev.ps1 down [-All]     stop API, worker and Vite (-All also stops PostgreSQL)
#   powershell -ExecutionPolicy Bypass -File scripts\dev.ps1 status
#   powershell -ExecutionPolicy Bypass -File scripts\dev.ps1 logs api|worker|web|pg      tail a log (Ctrl+C to stop)
#   powershell -ExecutionPolicy Bypass -File scripts\dev.ps1 token           setup-wizard token from the API log
#   powershell -ExecutionPolicy Bypass -File scripts\dev.ps1 reset           DROP the dev schema and clear dev data (asks first)
# PostgreSQL: set OA_DEV_DATABASE_URL to use your own server; otherwise a private PostgreSQL 16 is downloaded once (embedded
# binaries from Maven Central, as in the Windows package build) into data\dev and run on 127.0.0.1:54320 (no admin rights).
# State lives in data\dev\ (git-ignored). Open http://localhost:5173 (passkeys need the name localhost, not an IP address).
# Vite is bound to IPv4 127.0.0.1 on purpose: some hosts (security software) refuse connections to [::1].
param(
  [Parameter(Position = 0)][string]$Command = "help",
  [Parameter(Position = 1)][string]$Arg = "",
  [switch]$Mock,
  [switch]$All
)
$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent $PSScriptRoot
$Dev = Join-Path $Root "data\dev"
$Logs = Join-Path $Dev "logs"
$Pids = Join-Path $Dev "pids"
foreach ($d in $Dev, $Logs, $Pids, (Join-Path $Dev "spool"), (Join-Path $Dev "archive")) { New-Item -ItemType Directory -Force $d | Out-Null }

function EnvInt([string]$Name, [int]$Default) { $v = [Environment]::GetEnvironmentVariable($Name); if ($v) { [int]$v } else { $Default } }
$ApiPort = EnvInt "OA_DEV_API_PORT" 8080
$WorkerPort = EnvInt "OA_DEV_WORKER_PORT" 8090
$WebPort = EnvInt "OA_DEV_WEB_PORT" 5173
$PgPort = EnvInt "OA_DEV_PG_PORT" 54320
$PgUrl = "https://repo1.maven.org/maven2/io/zonky/test/postgres/embedded-postgres-binaries-windows-amd64/16.9.0/embedded-postgres-binaries-windows-amd64-16.9.0.jar"

function Say([string]$m) { Write-Host "==> $m" -ForegroundColor Cyan }
function Die([string]$m) { Write-Host "error: $m" -ForegroundColor Red; exit 1 }

# ---- helpers ----------------------------------------------------------------------------------------------------------
function Port-Pid([int]$Port) {
  $c = Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue | Select-Object -First 1
  if ($c) { $c.OwningProcess } else { $null }
}
function Wait-Port([int]$Port, [int]$Seconds = 60) {
  for ($i = 0; $i -lt $Seconds; $i++) { if (Port-Pid $Port) { return $true }; Start-Sleep -Seconds 1 }
  $false
}
function Stop-Tree([int]$ProcId) { & taskkill.exe /F /T /PID $ProcId 2>&1 | Out-Null }
function Find-Tool([string]$Name, [string[]]$Fallbacks) {
  $c = Get-Command $Name -ErrorAction SilentlyContinue
  if ($c) { return $c.Source }
  foreach ($f in $Fallbacks) { if (Test-Path $f) { return $f } }
  $null
}
function Start-Bg([string]$Exe, [string[]]$ArgList, [string]$WorkDir, [string]$LogName, [string]$PidName) {
  $out = Join-Path $Logs "$LogName.log"
  $err = Join-Path $Logs "$LogName.err.log"
  $sp = @{ FilePath = $Exe; WorkingDirectory = $WorkDir; WindowStyle = "Hidden"; PassThru = $true; RedirectStandardOutput = $out; RedirectStandardError = $err }
  if ($ArgList.Count -gt 0) { $sp.ArgumentList = $ArgList }
  $p = Start-Process @sp
  Set-Content -Path (Join-Path $Pids "$PidName.pid") -Value $p.Id
  $p
}
$Go = Find-Tool "go" @("C:\Program Files\Go\bin\go.exe", (Join-Path $env:USERPROFILE "sdk\go\bin\go.exe"))
$Npm = Find-Tool "npm.cmd" @("C:\Program Files\nodejs\npm.cmd")
$WorkerPy = Join-Path $Root "workers\document-ai\.venv\Scripts\python.exe"

# ---- PostgreSQL --------------------------------------------------------------------------------------------------------
$PgBin = $null
$Dsn = if ($env:OA_DEV_DATABASE_URL) { $env:OA_DEV_DATABASE_URL } else { "postgres://oa@127.0.0.1:$PgPort/postgres?sslmode=disable" }
function Get-PgBin {
  if ($env:OA_DEV_PG_DIR -and (Test-Path (Join-Path $env:OA_DEV_PG_DIR "bin\pg_ctl.exe"))) { return (Join-Path $env:OA_DEV_PG_DIR "bin") }
  $x = Join-Path $Dev "pg-cache\pgx"
  if (-not (Test-Path (Join-Path $x "bin\pg_ctl.exe"))) {
    Say "downloading PostgreSQL 16 (one time, ~50 MB)"
    $cache = Join-Path $Dev "pg-cache"
    New-Item -ItemType Directory -Force $cache, $x | Out-Null
    $jar = Join-Path $cache "pg-win.jar"
    if (-not (Test-Path $jar)) { Invoke-WebRequest -Uri $PgUrl -OutFile $jar -UseBasicParsing }
    & tar.exe -xf $jar -C $x
    if ($LASTEXITCODE -ne 0) { Die "cannot unpack the PostgreSQL archive" }
    & tar.exe -xf (Join-Path $x "postgres-windows-x86_64.txz") -C $x
    if ($LASTEXITCODE -ne 0) { Die "cannot unpack the PostgreSQL binaries" }
  }
  Join-Path $x "bin"
}
function Pg-Running { [bool](Port-Pid $PgPort) }
function Start-Pg {
  if ($env:OA_DEV_DATABASE_URL) { Say "using OA_DEV_DATABASE_URL"; return }
  if (Pg-Running) { Say "PostgreSQL already listening on :$PgPort"; return }
  $bin = Get-PgBin
  $data = Join-Path $Dev "pgdata"
  if (-not (Test-Path (Join-Path $data "PG_VERSION"))) {
    Say "initialising the dev database cluster"
    & (Join-Path $bin "initdb.exe") -D $data -U oa -A trust -E UTF8 --locale=C 2>&1 | Out-File (Join-Path $Logs "pg-init.log")
    if ($LASTEXITCODE -ne 0) { Die "initdb failed (see data\dev\logs\pg-init.log)" }
  }
  Say "starting PostgreSQL on :$PgPort"
  $pgArgs = "-D `"$data`" -l `"$(Join-Path $Logs 'pg-win.log')`" -o `"-c listen_addresses=127.0.0.1 -c port=$PgPort`" -w start"
  # No -Wait and no output redirection: the postmaster inherits the handles and Start-Process would wait for it forever.
  Start-Process -FilePath (Join-Path $bin "pg_ctl.exe") -ArgumentList $pgArgs -WindowStyle Hidden | Out-Null
  if (-not (Wait-Port $PgPort 60)) { Die "PostgreSQL did not start; see data\dev\logs\pg-win.log" }
}
function Stop-Pg {
  if ($env:OA_DEV_DATABASE_URL -or -not (Pg-Running)) { return }
  Say "stopping PostgreSQL"
  $bin = Get-PgBin
  & (Join-Path $bin "pg_ctl.exe") -D (Join-Path $Dev "pgdata") stop -m fast 2>&1 | Out-Null
}

# ---- worker / API / web ------------------------------------------------------------------------------------------------
$TokenFile = Join-Path $Dev "worker.token"
if (-not (Test-Path $TokenFile) -or (Get-Item $TokenFile).Length -lt 32) {
  $bytes = New-Object byte[] 24
  [System.Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($bytes)
  Set-Content -Path $TokenFile -Value (($bytes | ForEach-Object { $_.ToString("x2") }) -join "") -NoNewline
}
$WorkerToken = (Get-Content $TokenFile -Raw).Trim()

function Start-Worker {
  if (Port-Pid $WorkerPort) { Say "worker already listening on :$WorkerPort"; return }
  if (-not (Test-Path $WorkerPy)) {
    $py = Find-Tool "python.exe" @()
    if (-not $py) { $py = Find-Tool "py.exe" @() }
    if (-not $py) { Die "Python 3.12 not found (needed once to create workers\document-ai\.venv)" }
    Say "creating the worker virtualenv (one time, installs OCR dependencies; takes a few minutes)"
    $wd = Join-Path $Root "workers\document-ai"
    & $py -m venv (Join-Path $wd ".venv")
    & $WorkerPy -m pip install --quiet -r (Join-Path $wd "requirements.txt")
    if ($LASTEXITCODE -ne 0) { Die "pip install failed" }
  }
  Say "starting document worker on :$WorkerPort"
  $env:OA_WORKER_TOKEN = $WorkerToken; $env:OA_WORKER_PORT = "$WorkerPort"
  $env:OA_SPOOL_ROOT = Join-Path $Dev "spool"; $env:OA_ALLOWED_ROOTS = Join-Path $Dev "archive"
  Start-Bg $WorkerPy @("server.py") (Join-Path $Root "workers\document-ai") "worker" "worker" | Out-Null
  if (-not (Wait-Port $WorkerPort 60)) { Die "worker did not start; see data\dev\logs\worker.err.log" }
}

function Start-Api {
  if (Port-Pid $ApiPort) { Say "API already listening on :$ApiPort"; return }
  if (-not $Go) { Die "Go not found (need 1.26+)" }
  Say "building API (debug build)"
  $exe = Join-Path $Dev "oa-agent-dev.exe"
  Push-Location (Join-Path $Root "backend")
  try { & $Go build -tags debug -o $exe ./cmd/oa-agent; if ($LASTEXITCODE -ne 0) { Die "go build failed" } } finally { Pop-Location }
  $dist = Join-Path $Root "frontend\dist"
  Say "starting API on :$ApiPort (log: data\dev\logs\api.err.log)"
  $env:OA_DATABASE_URL = $Dsn; $env:OA_WORKER_TOKEN = $WorkerToken; $env:OA_WORKER_URL = "http://127.0.0.1:$WorkerPort"
  $env:OA_SPOOL_ROOT = Join-Path $Dev "spool"; $env:OA_ARCHIVE_ROOT = Join-Path $Dev "archive"
  $env:OA_MIGRATIONS_DIR = Join-Path $Root "migrations"; $env:OA_AUTO_MIGRATE = "1"
  $env:OA_LISTEN_ADDR = "127.0.0.1:$ApiPort"; $env:OA_PUBLIC_URL = "http://localhost:$WebPort"
  $env:OA_FRONTEND_DIR = if (Test-Path (Join-Path $dist "index.html")) { $dist } else { "" }
  Start-Bg $exe @() $Dev "api" "api" | Out-Null
  if (-not (Wait-Port $ApiPort 60)) { Get-Content (Join-Path $Logs "api.err.log") -Tail 15 -ErrorAction SilentlyContinue; Die "API did not start; see data\dev\logs\api.err.log" }
}

function Start-Web([bool]$UseMock) {
  if (Port-Pid $WebPort) { Say "Vite already listening on :$WebPort"; return }
  if (-not $Npm) { Die "Node.js/npm not found" }
  $fe = Join-Path $Root "frontend"
  if (-not (Test-Path (Join-Path $fe "node_modules"))) {
    Say "npm ci (first run)"
    Push-Location $fe
    try { & $Npm ci; if ($LASTEXITCODE -ne 0) { Die "npm ci failed" } } finally { Pop-Location }
  }
  $mode = if ($UseMock) { "mock/demo data" } else { "real backend :$ApiPort" }
  Say "starting Vite on :$WebPort ($mode)"
  $env:VITE_USE_MOCK = if ($UseMock) { "true" } else { "false" }; $env:OA_API_TARGET = "http://127.0.0.1:$ApiPort"
  Start-Bg $Npm @("run", "dev", "--", "--host", "127.0.0.1", "--port", "$WebPort", "--strictPort") $fe "web" "web" | Out-Null
  if (-not (Wait-Port $WebPort 60)) { Die "Vite did not start; see data\dev\logs\web.log / web.err.log" }
}

function Get-SetupToken {
  $f = Join-Path $Logs "api.err.log"
  if (-not (Test-Path $f)) { return $null }
  $m = Select-String -Path $f -Pattern "SETUP TOKEN: ([A-Za-z0-9_-]+)" | Select-Object -Last 1
  if ($m) { $m.Matches[0].Groups[1].Value } else { $null }
}

function Cmd-Up {
  if (-not $Mock) { Start-Pg; Start-Worker; Start-Api }
  Start-Web ([bool]$Mock)
  Write-Host ""
  Say "ready:  http://localhost:$WebPort   (use localhost, not 127.0.0.1: passkeys need it)"
  if (-not $Mock) {
    Write-Host "        API    http://localhost:$ApiPort   logs: scripts\dev.ps1 logs api"
    $t = Get-SetupToken
    if ($t) { Write-Host "        first run: complete the setup wizard with token  $t" }
  } else { Write-Host "        demo logins: admin / operator / viewer, password demo-password, TOTP 123456" }
}

function Cmd-Down {
  # Only processes this script started (recorded in data\dev\pids) are stopped, never "whatever listens on the port":
  # another instance (an installed or extracted OA Agent on :8080, say) must not be killed by a dev command.
  $expect = @{ api = @("oa-agent-dev"); worker = @("python*"); web = @("cmd", "node", "npm*") }
  foreach ($n in "web", "api", "worker") {
    $f = Join-Path $Pids "$n.pid"
    if (-not (Test-Path $f)) { continue }
    $id = [int](Get-Content $f)
    $proc = Get-Process -Id $id -ErrorAction SilentlyContinue
    if ($proc -and ($expect[$n] | Where-Object { $proc.ProcessName -like $_ })) { Say "stopping $n (pid $id)"; Stop-Tree $id }
    Remove-Item $f -Force
  }
  foreach ($p in $WebPort, $ApiPort, $WorkerPort) {
    $o = Port-Pid $p
    if ($o) { Write-Host "note: :$p is still in use by pid $o, which this script did not start; left alone" -ForegroundColor Yellow }
  }
  if ($All) { Stop-Pg }
}

function Cmd-Status {
  $rows = @(@("PostgreSQL", $PgPort), @("worker", $WorkerPort), @("API", $ApiPort), @("web (Vite)", $WebPort))
  foreach ($r in $rows) {
    $id = Port-Pid $r[1]
    if ($id) { "  {0,-12} up    :{1,-5} pid {2}" -f $r[0], $r[1], $id } else { "  {0,-12} DOWN  :{1}" -f $r[0], $r[1] }
  }
}

function Cmd-Reset {
  $a = Read-Host "This DROPS all dev data (schema 'public' in $Dsn and data\dev\spool+archive). Type 'reset' to continue"
  if ($a -ne "reset") { Die "aborted" }
  Cmd-Down
  Start-Pg
  foreach ($d in "spool", "archive") { Remove-Item -Recurse -Force (Join-Path $Dev $d) -ErrorAction SilentlyContinue; New-Item -ItemType Directory -Force (Join-Path $Dev $d) | Out-Null }
  $env:OA_DATABASE_URL = $Dsn; $env:OA_ALLOW_RESET = "1"
  Push-Location (Join-Path $Root "backend")
  try { & $Go run ./cmd/migrate -reset -dir (Join-Path $Root "migrations"); if ($LASTEXITCODE -ne 0) { Die "reset failed" } } finally { Pop-Location; Remove-Item Env:\OA_ALLOW_RESET -ErrorAction SilentlyContinue }
  Say "reset done; run  scripts\dev.ps1 up"
}

switch ($Command) {
  "up" { Cmd-Up }
  "down" { Cmd-Down }
  "status" { Cmd-Status }
  "logs" {
    $n = if ($Arg) { $Arg } else { "api" }
    if ($n -notin "api", "worker", "web", "pg") { Die "logs: api|worker|web|pg" }
    $f = if ($n -eq "pg") { Join-Path $Logs "pg-win.log" } else { Join-Path $Logs "$n.err.log" }
    if (-not (Test-Path $f)) { $f = Join-Path $Logs "$n.log" }
    Get-Content $f -Tail 80 -Wait
  }
  "token" { $t = Get-SetupToken; if ($t) { $t } else { Write-Host "no setup token in the log (the system is already initialised, or the API has not started)"; exit 1 } }
  "reset" { Cmd-Reset }
  default { Get-Content $PSCommandPath -TotalCount 12 | ForEach-Object { $_ -replace '^# ?', '' } | Select-Object -Skip 1; exit 2 }
}
