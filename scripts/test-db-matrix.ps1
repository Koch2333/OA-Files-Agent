# Windows wrapper: runs scripts/test-db-matrix.sh through WSL or Git Bash. Usage: .\test-db-matrix.ps1 [postgres|mysql|mssql ...]
$ErrorActionPreference = 'Stop'
$sh = Join-Path $PSScriptRoot 'test-db-matrix.sh'
$bash = (Get-Command bash -ErrorAction SilentlyContinue)
if (-not $bash) { throw 'bash (Git Bash or WSL) is required' }
& bash $sh @args
exit $LASTEXITCODE
