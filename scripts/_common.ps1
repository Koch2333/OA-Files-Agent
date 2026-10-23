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

# Target architecture of a Windows package: x64 (amd64) or arm64. Names in artifacts use the x64/arm64 spelling.
function Get-GoArch([string]$Arch) {
  switch ($Arch) { "x64" { return "amd64" } "arm64" { return "arm64" } default { throw "unsupported architecture '$Arch' (x64 or arm64)" } }
}

# PE machine type of an .exe/.dll: "x64" (0x8664), "arm64" (0xAA64), "x86" (0x014C) or "0x...." for anything else.
function Get-PeMachine([string]$Path) {
  $fs = [System.IO.File]::OpenRead($Path)
  try {
    $br = New-Object System.IO.BinaryReader($fs)
    if ($br.ReadUInt16() -ne 0x5A4D) { throw "$Path is not a PE file (no MZ header)" }
    $fs.Position = 0x3C
    $pe = $br.ReadInt32()
    $fs.Position = $pe
    if ($br.ReadUInt32() -ne 0x00004550) { throw "$Path has no PE signature" }
    $m = $br.ReadUInt16()
    switch ($m) { 0x8664 { return "x64" } 0xAA64 { return "arm64" } 0x014C { return "x86" } default { return ("0x{0:X4}" -f $m) } }
  } finally { $fs.Close() }
}
