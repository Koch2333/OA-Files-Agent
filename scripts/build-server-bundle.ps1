# Builds the server bundle: oa-agent.exe + web + PyInstaller worker + embedded PostgreSQL + migrations.
# This is the payload of the Electron desktop app (resources\app-server) and of the server-only zip.
#   powershell -ExecutionPolicy Bypass -File scripts\build-server-bundle.ps1 [-Mode release|debug] [-Out dist\server] [-Version x.y.z]
#                                                                           [-Clean] [-SkipFrontend] [-SkipWorker] [-Extras]
# -SkipFrontend / -SkipWorker reuse frontend\dist and dist\worker-dist from a previous build.
# -Extras adds start.bat, the demo samples and the user manual (the old single-folder "standalone" package).
param(
  [ValidateSet("release", "debug")][string]$Mode = "release",
  [string]$Out = "dist\server",
  [string]$Version = "",
  [string]$PgJarUrl = "https://repo1.maven.org/maven2/io/zonky/test/postgres/embedded-postgres-binaries-windows-amd64/16.9.0/embedded-postgres-binaries-windows-amd64-16.9.0.jar",
  [switch]$Clean, [switch]$SkipFrontend, [switch]$SkipWorker, [switch]$Extras
)
$ErrorActionPreference = "Stop"
. "$PSScriptRoot\_common.ps1"
Initialize-Version $Version
if (-not [System.IO.Path]::IsPathRooted($Out)) { $Out = Join-Path $RepoRoot $Out }
$cache = Join-Path $RepoRoot "dist\cache"
New-Item -ItemType Directory -Force $cache | Out-Null
Write-Host "server bundle: version $env:OA_VERSION ($env:OA_COMMIT), mode $Mode -> $Out" -ForegroundColor Green

Step "Cleaning $Out"
Remove-IfExists $Out
New-Item -ItemType Directory -Force $Out | Out-Null

Step "Go backend ($Mode)"
$beOut = Join-Path $RepoRoot "dist\backend-build"
Remove-IfExists $beOut
Invoke-Native "powershell" @("-NoProfile", "-ExecutionPolicy", "Bypass", "-File", (Join-Path $PSScriptRoot "build-backend.ps1"), "-Mode", $Mode, "-Out", $beOut) $RepoRoot
$beExe = if ($Mode -eq "debug") { "oa-agent-debug.exe" } else { "oa-agent.exe" }
Copy-Item (Join-Path $beOut $beExe) (Join-Path $Out "oa-agent.exe")

if (-not $SkipFrontend) { & (Join-Path $PSScriptRoot "build-frontend.ps1") }
if (-not (Test-Path (Join-Path $RepoRoot "frontend\dist\index.html"))) { Fail "frontend\dist is missing (run without -SkipFrontend)" }
Copy-Item -Recurse (Join-Path $RepoRoot "frontend\dist") (Join-Path $Out "web")

if (-not $SkipWorker) { & (Join-Path $PSScriptRoot "build-worker.ps1") }
$wdist = Join-Path $RepoRoot "dist\worker-dist\document-worker"
if (-not (Test-Path (Join-Path $wdist "document-worker.exe"))) { Fail "dist\worker-dist\document-worker is missing (run without -SkipWorker)" }
Copy-Item -Recurse $wdist (Join-Path $Out "worker")

Step "Embedded PostgreSQL"
$jar = Join-Path $cache "pg-win.jar"
if (-not (Test-Path $jar)) { Invoke-WebRequest -Uri $PgJarUrl -OutFile $jar }
$pgx = Join-Path $cache "pgx"
if (-not (Test-Path (Join-Path $pgx "bin\pg_ctl.exe"))) {
  Remove-IfExists $pgx
  New-Item -ItemType Directory -Force $pgx | Out-Null
  Invoke-Native "tar" @("-xf", $jar) $pgx
  Invoke-Native "tar" @("-xf", "postgres-windows-x86_64.txz") $pgx
}
$pg = Join-Path $Out "pgsql"
New-Item -ItemType Directory -Force $pg | Out-Null
foreach ($d in "bin", "lib", "share") { Copy-Item -Recurse (Join-Path $pgx $d) (Join-Path $pg $d) }
# Trim what the app never uses (GUI toolkit DLLs, embedded-SQL libs, test plugin).
Get-ChildItem (Join-Path $pg "bin") -File | Where-Object { $_.Name -match '^(wx.*|testplug|libecpg.*|libpgtypes)\.dll$' } | Remove-Item -Force

Step "Assemble"
Copy-Item -Recurse (Join-Path $RepoRoot "migrations") (Join-Path $Out "migrations")
Get-ChildItem (Join-Path $Out "migrations") -Filter "*.down.sql" | Remove-Item
Copy-Item (Join-Path $RepoRoot "deploy\windows\oa-agent.env.template") (Join-Path $Out "oa-agent.env")
Copy-Item (Join-Path $RepoRoot "docs\THIRD-PARTY-LICENSES.md") $Out -ErrorAction SilentlyContinue
Copy-Item (Join-Path $RepoRoot "LICENSE") (Join-Path $Out "LICENSE.txt") -ErrorAction SilentlyContinue
Write-Utf8 (Join-Path $Out "VERSION.txt") ("version=$env:OA_VERSION`r`ncommit=$env:OA_COMMIT`r`nmode=$Mode`r`n")
if ($Extras) {
  Copy-Item (Join-Path $RepoRoot "deploy\windows\start.bat") $Out
  foreach ($f in "start-banner.txt", "start-stopped.txt", "first-run.ps1", "change-workdir.bat") { Copy-Item (Join-Path $RepoRoot "deploy\windows\$f") $Out }
  Copy-Item (Join-Path $RepoRoot "docs\用户手册.md") $Out -ErrorAction SilentlyContinue
  Copy-Item -Recurse (Join-Path $RepoRoot "tests\samples") (Join-Path $Out "演示样本") -ErrorAction SilentlyContinue
}

Step "Done"
Write-Host ("Server bundle: $Out  (" + (Format-Size (Get-DirSize $Out)) + ")") -ForegroundColor Green
