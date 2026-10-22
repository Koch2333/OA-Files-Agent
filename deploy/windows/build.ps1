# Builds the single-folder server-only Windows package: dist\oa-agent (exe + web + worker + embedded PostgreSQL + start.bat).
# Thin wrapper around scripts\build-server-bundle.ps1 (-Extras adds start.bat, manual and demo samples). For the full
# distribution (Electron desktop app, installer, zips, checksums) use scripts\dist-windows.ps1.
#   powershell -ExecutionPolicy Bypass -File deploy\windows\build.ps1  [-Mode debug]  [-Out dist\oa-agent] [-SkipFrontend] [-SkipWorker]
param(
  [string]$Out = "dist\oa-agent",
  [string]$PgJarUrl = "https://repo1.maven.org/maven2/io/zonky/test/postgres/embedded-postgres-binaries-windows-amd64/16.9.0/embedded-postgres-binaries-windows-amd64-16.9.0.jar",
  [ValidateSet("release", "debug")][string]$Mode = "release",
  [switch]$SkipFrontend,
  [switch]$SkipWorker
)
$ErrorActionPreference = "Stop"
$Root = Resolve-Path (Join-Path $PSScriptRoot "..\..")
$a = @{ Mode = $Mode; Out = $Out; PgJarUrl = $PgJarUrl; Extras = $true }
if ($SkipFrontend) { $a.SkipFrontend = $true }
if ($SkipWorker) { $a.SkipWorker = $true }
& (Join-Path $Root "scripts\build-server-bundle.ps1") @a
Write-Host "Package: $Out. Zip the folder to distribute; run start.bat to launch." -ForegroundColor Green
