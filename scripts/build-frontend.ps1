# Builds the Vue frontend (npm ci + npm run build) into frontend\dist.
#   powershell -ExecutionPolicy Bypass -File scripts\build-frontend.ps1 [-Clean] [-SkipInstall]
param([switch]$Clean, [switch]$SkipInstall)
$ErrorActionPreference = "Stop"
. "$PSScriptRoot\_common.ps1"
Require-Tool npm "Install Node.js 22+ from https://nodejs.org"
$fe = Join-Path $RepoRoot "frontend"
Step "Frontend build"
if ($Clean) { Remove-IfExists (Join-Path $fe "dist"); Remove-IfExists (Join-Path $fe "node_modules") }
if (-not $SkipInstall) { Invoke-Native "npm" @("ci", "--no-audit", "--no-fund") $fe }
Invoke-Native "npm" @("run", "build") $fe
if (-not (Test-Path (Join-Path $fe "dist\index.html"))) { Fail "frontend\dist\index.html missing after build" }
Info ("frontend\dist: " + (Format-Size (Get-DirSize (Join-Path $fe "dist"))))
