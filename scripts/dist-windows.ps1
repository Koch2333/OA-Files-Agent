# Full Windows distribution: version -> (tests) -> server bundle -> desktop app -> installer + zips + SHA256SUMS.
#   powershell -ExecutionPolicy Bypass -File scripts\dist-windows.ps1 [-Version x.y.z] [-Mode release|debug] [-SkipTests] [-Clean]
#                                                                    [-SkipBundle] [-SkipFrontend] [-SkipWorker] [-NoServerZip]
# Output: dist\release\<version>\
#   oa-agent-<ver>-windows-x64-setup.exe   NSIS installer (per-user default, all-users option)
#   oa-agent-<ver>-windows-x64.zip         portable desktop app (portable.flag)
#   oa-agent-<ver>-windows-x64-server.zip  server-only folder (start.bat, no Electron)
#   SHA256SUMS.txt
param(
  [string]$Version = "", [ValidateSet("release", "debug")][string]$Mode = "release",
  [switch]$SkipTests, [switch]$Clean, [switch]$SkipBundle, [switch]$SkipFrontend, [switch]$SkipWorker, [switch]$NoServerZip
)
$ErrorActionPreference = "Stop"
. "$PSScriptRoot\_common.ps1"
$sw = [System.Diagnostics.Stopwatch]::StartNew()
Step "Version"
$va = @{}; if ($Version) { $va.Version = $Version }
& (Join-Path $PSScriptRoot "version.ps1") @(if ($Version) { @("-Version", $Version) }) (Join-Path $PSScriptRoot "version.ps1") @va
$ver = $env:OA_VERSION
$rel = Join-Path $RepoRoot "dist\release\$ver"
if ($Clean) { Remove-IfExists $rel; Remove-IfExists (Join-Path $RepoRoot "dist\server"); Remove-IfExists (Join-Path $RepoRoot "dist\desktop") }
New-Item -ItemType Directory -Force $rel | Out-Null

if (-not $SkipTests) {
  Step "Tests (go vet/test, frontend, worker)"
  Invoke-Native "go" @("vet", "./...") (Join-Path $RepoRoot "backend")
  Invoke-Native "go" @("test", "./...") (Join-Path $RepoRoot "backend")
  $fe = Join-Path $RepoRoot "frontend"
  if (-not (Test-Path (Join-Path $fe "node_modules"))) { Invoke-Native "npm" @("ci", "--no-audit", "--no-fund") $fe }
  Invoke-Native "npm" @("test") $fe
  $py = Join-Path $RepoRoot "workers\document-ai\.venv\Scripts\python.exe"
  if (Test-Path $py) { Invoke-Native $py @("-m", "unittest") (Join-Path $RepoRoot "workers\document-ai") } else { Write-Warning "worker venv missing: worker tests skipped" }
}

if (-not $SkipBundle) {
  $b = @{ Mode = $Mode; Version = $ver; Out = "dist\server" }
  if ($SkipFrontend) { $b.SkipFrontend = $true }
  if ($SkipWorker) { $b.SkipWorker = $true }
  & (Join-Path $PSScriptRoot "build-server-bundle.ps1") @b
}

& (Join-Path $PSScriptRoot "build-desktop.ps1") -Version $ver -ServerDir "dist\server" -Out "dist\desktop"

Step "Collect artifacts"
$d = Join-Path $RepoRoot "dist\desktop"
Copy-Item (Join-Path $d "oa-agent-$ver-windows-x64-setup.exe") $rel -Force
Copy-Item (Join-Path $d "oa-agent-$ver-windows-x64.zip") $rel -Force
if (-not $NoServerZip) {
  Step "Server-only zip"
  $stage = Join-Path $RepoRoot "dist\server-stage"
  Remove-IfExists $stage
  & (Join-Path $PSScriptRoot "build-server-bundle.ps1") -Mode $Mode -Version $ver -Out "dist\server-stage\oa-agent" -SkipFrontend -SkipWorker -Extras
  & (Join-Path $PSScriptRoot "make-zip.ps1") -Source (Join-Path $stage "oa-agent") -Zip (Join-Path $rel "oa-agent-$ver-windows-x64-server.zip") -TopFolder "oa-agent-$ver"
  Remove-IfExists $stage
}

Step "Checksums"
$sums = @()
$rows = @()
Get-ChildItem $rel -File | Where-Object { $_.Name -ne "SHA256SUMS.txt" } | Sort-Object Name | ForEach-Object {
  $h = (Get-FileHash -Algorithm SHA256 $_.FullName).Hash.ToLower()
  $sums += "$h *$($_.Name)"
  $rows += [pscustomobject]@{ File = $_.Name; Size = (Format-Size $_.Length); SHA256 = $h }
}
Write-Utf8 (Join-Path $rel "SHA256SUMS.txt") (($sums -join "`n") + "`n")

Step "Summary ($ver, $Mode, $([int]$sw.Elapsed.TotalMinutes) min)"
$rows | Format-Table -AutoSize -Wrap | Out-String -Width 200 | Write-Host
if (-not $env:CSC_LINK -and -not $env:WIN_CSC_LINK) { Write-Host "NOTE: artifacts are UNSIGNED; Windows SmartScreen will show a warning (see docs\构建与发布.md)." -ForegroundColor Yellow }
Write-Host "Release folder: $rel" -ForegroundColor Green
