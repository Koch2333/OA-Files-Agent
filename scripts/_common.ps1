# Shared helpers for the build scripts (dot-source:  . "$PSScriptRoot\_common.ps1").
$ErrorActionPreference = "Stop"
Set-StrictMode -Version 2
$script:RepoRoot = Split-Path -Parent $PSScriptRoot

function Step([string]$Message) {
  Write-Host ""
  Write-Host ("==> " + $Message) -ForegroundColor Cyan
}
function Info([string]$Message) { Write-Host ("    " + $Message) }
function Fail([string]$Message) { throw $Message }

# Run a native command in a directory; throws on a non-zero exit code.
function Invoke-Native {
  param([Parameter(Mandatory)][string]$Exe, [string[]]$ArgList = @(), [string]$WorkDir = $script:RepoRoot)
  Push-Location $WorkDir
  try {
    & $Exe @ArgList
    if ($LASTEXITCODE -ne 0) { throw "$Exe $($ArgList -join ' ') failed with exit code $LASTEXITCODE" }
  } finally { Pop-Location }
}

function Require-Tool([string]$Name, [string]$Hint) {
  if (-not (Get-Command $Name -ErrorAction SilentlyContinue)) { throw "'$Name' not found on PATH. $Hint" }
}

function Format-Size([long]$Bytes) {
  if ($Bytes -ge 1GB) { return ("{0:N2} GB" -f ($Bytes / 1GB)) }
  return ("{0:N1} MB" -f ($Bytes / 1MB))
}

function Get-DirSize([string]$Path) {
  $m = Get-ChildItem -Recurse -Force -File $Path -ErrorAction SilentlyContinue | Measure-Object Length -Sum
  if ($null -eq $m.Sum) { return 0 }
  return [long]$m.Sum
}

# Resolve the version once per process (scripts/version.ps1 exports OA_VERSION / OA_COMMIT).
function Initialize-Version([string]$Version) {
  $args2 = @{ Quiet = $true }
  if ($Version) { $args2.Version = $Version }
  & (Join-Path $PSScriptRoot "version.ps1") @args2
}

function Remove-IfExists([string]$Path) {
  if (Test-Path $Path) { Remove-Item -Recurse -Force $Path }
}

# Windows PowerShell 5.1 safe UTF-8 (no BOM) text writer.
function Write-Utf8([string]$Path, [string]$Text) {
  [System.IO.File]::WriteAllText($Path, $Text, (New-Object System.Text.UTF8Encoding($false)))
}
