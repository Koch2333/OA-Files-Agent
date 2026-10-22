# Resolves the product version and commit and exports them as OA_VERSION / OA_COMMIT (and OA_VERSION_FULL).
#   powershell -File scripts\version.ps1 [-Version 1.2.3] [-Quiet]
# Order: -Version, then $env:OA_VERSION, then the top-level VERSION file, then `git describe` (as 0.0.0-<describe>).
# Dot-source or call with & from another PowerShell script to keep the exported variables.
param([string]$Version = "", [switch]$Quiet)
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot

function Git-Out([string[]]$a) {
  if (-not (Get-Command git -ErrorAction SilentlyContinue)) { return $null }
  $ErrorActionPreference = "Continue" # PS 5.1 turns native stderr into errors under Stop
  $o = & git -C $root @a 2>$null
  if ($LASTEXITCODE -ne 0) { return $null }
  return ($o | Select-Object -First 1)
}

$src = ""
if ($Version) { $v = $Version; $src = "-Version" }
elseif ($env:OA_VERSION) { $v = $env:OA_VERSION; $src = "OA_VERSION" }
else {
  $f = Join-Path $root "VERSION"
  $v = ""
  if (Test-Path $f) { $v = (Get-Content -Raw $f).Trim(); $src = "VERSION file" }
  if (-not $v) {
    $d = Git-Out @("describe", "--tags", "--always", "--dirty")
    if (-not $d) { $d = "unknown" }
    $v = "0.0.0-" + $d; $src = "git describe"
  }
}
$v = $v -replace "^[vV]", ""
if ($v -notmatch '^\d+\.\d+\.\d+([-+][0-9A-Za-z.+-]+)?$') { throw "Version '$v' is not semantic (expected MAJOR.MINOR.PATCH[-suffix])" }

$commit = Git-Out @("rev-parse", "--short", "HEAD"); if (-not $commit) { $commit = "unknown" }
$dirty = $false
if (Get-Command git -ErrorAction SilentlyContinue) {
  $ErrorActionPreference = "Continue"
  $st = & git -C $root status --porcelain 2>$null
  $ErrorActionPreference = "Stop"
  if ($LASTEXITCODE -eq 0 -and $st) { $dirty = $true }
}
$tag = Git-Out @("describe", "--tags", "--exact-match")
if ($tag -and ($tag -replace "^[vV]", "") -ne $v -and $src -eq "VERSION file") {
  Write-Warning "HEAD is tagged '$tag' but VERSION says '$v'. Fix VERSION before releasing."
}
$env:OA_VERSION = $v
$env:OA_COMMIT = $commit
$env:OA_VERSION_FULL = if ($dirty) { "$v+$commit.dirty" } else { "$v+$commit" }
if (-not $Quiet) {
  Write-Host "version : $v  (from $src)"
  Write-Host "commit  : $commit$(if ($dirty) { ' (working tree has uncommitted changes)' })"
}
