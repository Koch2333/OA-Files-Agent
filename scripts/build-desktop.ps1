# Builds the Electron desktop app and packages it: NSIS installer + portable ZIP.
#   powershell -ExecutionPolicy Bypass -File scripts\build-desktop.ps1 [-Version x.y.z] [-ServerDir dist\server] [-Out dist\desktop]
#                                                                     [-Clean] [-SkipInstall] [-NoInstaller] [-NoZip]
# Consumes the server bundle (scripts\build-server-bundle.ps1 -> dist\server). Outputs in -Out:
#   oa-agent-<ver>-windows-x64-setup.exe, oa-agent-<ver>-windows-x64.zip, win-unpacked\
# Signing (optional): set CSC_LINK + CSC_KEY_PASSWORD (PFX file/base64) before running; unsigned builds work (SmartScreen warns).
# China mirrors: $env:ELECTRON_MIRROR="https://npmmirror.com/mirrors/electron/" ; $env:ELECTRON_BUILDER_BINARIES_MIRROR="https://npmmirror.com/mirrors/electron-builder-binaries/"
param(
  [string]$Version = "", [string]$ServerDir = "dist\server", [string]$Out = "dist\desktop",
  [switch]$Clean, [switch]$SkipInstall, [switch]$NoInstaller, [switch]$NoZip
)
$ErrorActionPreference = "Stop"
. "$PSScriptRoot\_common.ps1"
Initialize-Version $Version
Require-Tool npm "Install Node.js 22+ from https://nodejs.org"
if (-not [System.IO.Path]::IsPathRooted($ServerDir)) { $ServerDir = Join-Path $RepoRoot $ServerDir }
if (-not [System.IO.Path]::IsPathRooted($Out)) { $Out = Join-Path $RepoRoot $Out }
if (-not (Test-Path (Join-Path $ServerDir "oa-agent.exe"))) { Fail "Server bundle not found in $ServerDir (run scripts\build-server-bundle.ps1 first)" }
$desk = Join-Path $RepoRoot "desktop"
$ver = $env:OA_VERSION

Step "Desktop app $ver"
if ($Clean) { Remove-IfExists $Out; Remove-IfExists (Join-Path $desk "node_modules") }
if (-not $SkipInstall) { Invoke-Native "npm" @("ci", "--no-audit", "--no-fund") $desk }
Invoke-Native "node" @("--test", "test/diagnose.test.js", "test/workdir.test.js") $desk

Step "electron-builder (NSIS installer)"
Remove-IfExists (Join-Path $Out "win-unpacked")
Get-ChildItem $Out -Filter "oa-agent-*" -ErrorAction SilentlyContinue | Remove-Item -Force
# electron-builder resolves extraResources relative to desktop\; keep the bundle inside the repo so a relative path works.
if (-not $ServerDir.StartsWith($RepoRoot, [System.StringComparison]::OrdinalIgnoreCase)) {
  $copy = Join-Path $RepoRoot "dist\server-copy"
  Remove-IfExists $copy
  Copy-Item -Recurse $ServerDir $copy
  $ServerDir = $copy
}
$env:OA_SERVER_BUNDLE = "../" + ($ServerDir.Substring($RepoRoot.Length).TrimStart("\", "/") -replace "\\", "/")
if (-not $env:CSC_LINK -and -not $env:WIN_CSC_LINK) {
  $env:CSC_IDENTITY_AUTO_DISCOVERY = "false"
  Info "no CSC_LINK set: building UNSIGNED (Windows SmartScreen will warn on first run)"
} else { Info "code signing enabled (CSC_LINK present)" }
$bld = Join-Path $desk "node_modules\.bin\electron-builder.cmd"
$ebArgs = @("--win", "--x64", "-c.extraMetadata.version=$ver", "-c.directories.output=$Out")
if ($NoInstaller) { $ebArgs += "--dir" }
Invoke-Native $bld $ebArgs $desk

$unpacked = Join-Path $Out "win-unpacked"
if (-not (Test-Path (Join-Path $unpacked "OA-Agent-Desktop.exe"))) { Fail "electron-builder produced no win-unpacked\OA-Agent-Desktop.exe" }
if (-not $NoZip) {
  Step "Portable ZIP"
  $stage = Join-Path $RepoRoot "dist\zip-stage\OA-Agent"
  Remove-IfExists (Join-Path $RepoRoot "dist\zip-stage")
  New-Item -ItemType Directory -Force (Split-Path -Parent $stage) | Out-Null
  Copy-Item -Recurse $unpacked $stage
  # portable.flag: keep all data (database, settings, logs) in <folder>\data instead of %LOCALAPPDATA%
  Write-Utf8 (Join-Path $stage "portable.flag") "Delete this file to store data in %LOCALAPPDATA%\OA-Agent instead of next to the program.`r`n"
  Write-Utf8 (Join-Path $stage "README-便携版.txt") "OA 文件智能归档 $ver 便携版`r`n`r`n解压到任意可写文件夹，双击 OA-Agent-Desktop.exe。数据保存在本文件夹的 data 子目录（删除 portable.flag 则改用 %LOCALAPPDATA%\OA-Agent）。`r`n不要解压到 Program Files 等只读位置。`r`n"
  & (Join-Path $PSScriptRoot "make-zip.ps1") -Source $stage -Zip (Join-Path $Out "oa-agent-$ver-windows-x64.zip") -TopFolder "OA-Agent-$ver"
  Remove-IfExists (Join-Path $RepoRoot "dist\zip-stage")
}
Step "Desktop done"
Get-ChildItem $Out -File -Filter "oa-agent-*" | ForEach-Object { Info ("{0}  {1}" -f $_.Name, (Format-Size $_.Length)) }
