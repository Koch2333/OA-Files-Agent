# Builds everything possible on this OS: the Windows distribution, plus the Linux release when bash is available.
#   powershell -ExecutionPolicy Bypass -File scripts\build-all.ps1 [-Version x.y.z] [-SkipTests] [-Clean] [-SkipLinux]
param([string]$Version = "", [switch]$SkipTests, [switch]$Clean, [switch]$SkipLinux)
$ErrorActionPreference = "Stop"
. "$PSScriptRoot\_common.ps1"
$a = @{}
if ($Version) { $a.Version = $Version }
if ($SkipTests) { $a.SkipTests = $true }
if ($Clean) { $a.Clean = $true }
& (Join-Path $PSScriptRoot "dist-windows.ps1") @a
if ($SkipLinux) { return }
Step "Linux release"
$bash = Get-Command bash -ErrorAction SilentlyContinue
if (-not $bash) { Write-Warning "bash not found (install Git for Windows or WSL): Linux release skipped"; return }
$va = @{ Quiet = $true }; if ($Version) { $va.Version = $Version }
& (Join-Path $PSScriptRoot "version.ps1") -Quiet @(if ($Version) { @("-Version", $Version) }) (Join-Path $PSScriptRoot "version.ps1") @va
$sh = ($RepoRoot -replace "\\", "/") + "/scripts/dist-linux.sh"
& $bash.Source $sh --version $env:OA_VERSION
if ($LASTEXITCODE -ne 0) { Write-Warning "Linux release failed (exit $LASTEXITCODE); the Windows artifacts are still valid." ; exit $LASTEXITCODE }
