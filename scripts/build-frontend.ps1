# Builds the Vue frontend (npm ci + npm run build) into frontend\dist.
#   powershell -ExecutionPolicy Bypass -File scripts\build-frontend.ps1 [-Clean] [-SkipInstall]
param([switch]$Clean, [switch]$SkipInstall)
$ErrorActionPreference = "Stop"
. "$PSScriptRoot\_common.ps1"
Require-Tool npm "Install Node.js 22+ from https://nodejs.org"
$fe = Join-Path $RepoRoot "frontend"
Step "Frontend build"
if ($Clean) { Remove-IfExists (Join-Path $fe "dist"); Remove-IfExists (Join-Path $fe "node_modules") }
# npm ci deletes node_modules first: on a tree that a running Vite dev server holds open it fails half-way (EPERM on the
# native rolldown binding) and leaves the dev environment broken. Skip it when node_modules already matches the lock file.
$lock = Join-Path $fe "package-lock.json"
$installed = Join-Path $fe "node_modules\.package-lock.json"
$current = (Test-Path $installed) -and ((Get-Item $installed).LastWriteTimeUtc -ge (Get-Item $lock).LastWriteTimeUtc)
if ($SkipInstall) { }
elseif ($current -and -not $Clean) { Info "node_modules is up to date with package-lock.json: npm ci skipped (use -Clean to force)" }
else { Invoke-Native "npm" @("ci", "--no-audit", "--no-fund") $fe }
Invoke-Native "npm" @("run", "build") $fe
if (-not (Test-Path (Join-Path $fe "dist\index.html"))) { Fail "frontend\dist\index.html missing after build" }
Info ("frontend\dist: " + (Format-Size (Get-DirSize (Join-Path $fe "dist"))))
