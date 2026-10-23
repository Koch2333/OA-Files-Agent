# Full Windows distribution for ONE architecture: version -> (tests) -> server bundle -> desktop app -> installer + zips + checksums.
#   powershell -ExecutionPolicy Bypass -File scripts\dist-windows.ps1 [-Arch x64|arm64] [-Version x.y.z] [-Mode release|debug] [-SkipTests] [-Clean]
#                                                                    [-SkipBundle] [-SkipFrontend] [-SkipWorker] [-NoServerZip]
# Output: dist\release\<version>\   (names are a release contract, see scripts\release-assets.sh)
#   oa-agent-<ver>-windows-<arch>-setup.exe     NSIS installer (per-user default, all-users option)
#   oa-agent-<ver>-windows-<arch>-portable.zip  portable desktop app (portable.flag)
#   oa-agent-<ver>-windows-<arch>-server.zip    server-only folder (start.bat, no Electron)
#   SHA256SUMS-windows-<arch>.txt               local convenience file ("hash  name"); the release SHA256SUMS is made by CI
# arm64: oa-agent.exe and Electron are native arm64; the document worker and the embedded PostgreSQL are x64 (run emulated on
# Windows on Arm). Everything is built on an x64 machine; no ARM64 hardware is needed to produce the package.
param(
  [ValidateSet("x64", "arm64")][string]$Arch = "x64",
  [string]$Version = "", [ValidateSet("release", "debug")][string]$Mode = "release",
  [switch]$SkipTests, [switch]$Clean, [switch]$SkipBundle, [switch]$SkipFrontend, [switch]$SkipWorker, [switch]$NoServerZip
)
$ErrorActionPreference = "Stop"
. "$PSScriptRoot\_common.ps1"
$sw = [System.Diagnostics.Stopwatch]::StartNew()
Step "Version ($Arch)"
$va = @{}; if ($Version) { $va.Version = $Version }
& (Join-Path $PSScriptRoot "version.ps1") @va
$ver = $env:OA_VERSION
$rel = Join-Path $RepoRoot "dist\release\$ver"
$desk = Join-Path $RepoRoot "dist\desktop\$Arch"
$srv = Join-Path $RepoRoot "dist\server-$Arch"
if ($Clean) { Remove-IfExists $rel; Remove-IfExists $srv; Remove-IfExists $desk }
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
  $b = @{ Mode = $Mode; Version = $ver; Out = "dist\server-$Arch"; Arch = $Arch }
  if ($SkipFrontend) { $b.SkipFrontend = $true }
  if ($SkipWorker) { $b.SkipWorker = $true }
  & (Join-Path $PSScriptRoot "build-server-bundle.ps1") @b
}

& (Join-Path $PSScriptRoot "build-desktop.ps1") -Version $ver -ServerDir "dist\server-$Arch" -Out "dist\desktop\$Arch" -Arch $Arch

Step "Collect artifacts"
Copy-Item (Join-Path $desk "oa-agent-$ver-windows-$Arch-setup.exe") $rel -Force
Copy-Item (Join-Path $desk "oa-agent-$ver-windows-$Arch-portable.zip") $rel -Force
if (-not $NoServerZip) {
  Step "Server-only zip ($Arch)"
  $stage = Join-Path $RepoRoot "dist\server-stage-$Arch"
  Remove-IfExists $stage
  & (Join-Path $PSScriptRoot "build-server-bundle.ps1") -Mode $Mode -Version $ver -Out "dist\server-stage-$Arch\oa-agent" -SkipFrontend -SkipWorker -Extras -Arch $Arch
  & (Join-Path $PSScriptRoot "verify-windows-package.ps1") -Dir (Join-Path $stage "oa-agent") -Arch $Arch
  if ($LASTEXITCODE -ne 0) { Fail "server zip architecture check failed" }
  & (Join-Path $PSScriptRoot "make-zip.ps1") -Source (Join-Path $stage "oa-agent") -Zip (Join-Path $rel "oa-agent-$ver-windows-$Arch-server.zip") -TopFolder "oa-agent-$ver"
  Remove-IfExists $stage
}

Step "Checksums"
$sums = @()
$rows = @()
Get-ChildItem $rel -File -Filter "oa-agent-$ver-windows-$Arch-*" | Sort-Object Name | ForEach-Object {
  $h = (Get-FileHash -Algorithm SHA256 $_.FullName).Hash.ToLower()
  $sums += "$h  $($_.Name)"
  $rows += [pscustomobject]@{ File = $_.Name; Size = (Format-Size $_.Length); SHA256 = $h }
}
Write-Utf8 (Join-Path $rel "SHA256SUMS-windows-$Arch.txt") (($sums -join "`n") + "`n")

Step "Summary ($ver, $Arch, $Mode, $([int]$sw.Elapsed.TotalMinutes) min)"
$rows | Format-Table -AutoSize -Wrap | Out-String -Width 200 | Write-Host
if (-not $env:CSC_LINK -and -not $env:WIN_CSC_LINK) { Write-Host "NOTE: artifacts are UNSIGNED; Windows SmartScreen will show a warning (see docs\构建与发布.md)." -ForegroundColor Yellow }
if ($Arch -eq "arm64") { Write-Host "NOTE: arm64 package = native arm64 app + x64 worker/PostgreSQL (emulated); untested on real ARM hardware." -ForegroundColor Yellow }
Write-Host "Release folder: $rel" -ForegroundColor Green
