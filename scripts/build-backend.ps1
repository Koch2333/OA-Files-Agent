# Builds the Go backend in debug or release mode.
#   powershell -ExecutionPolicy Bypass -File scripts\build-backend.ps1 [-Mode debug|release] [-Out backend\bin]
# debug  : -tags debug (enables logx.Debugf lines), optimisations/inlining off for debuggers (dlv), symbols kept.
# release: -trimpath, symbols stripped, no debug logging code in the binary.
param(
  [ValidateSet("debug", "release")][string]$Mode = "release",
  [string]$Out = ""
)
$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent $PSScriptRoot
if (-not $Out) { $Out = Join-Path $Root "backend\bin" }
$version = if ($env:OA_VERSION) { $env:OA_VERSION } else { "dev" }
$commit = "unknown"
if (Get-Command git -ErrorAction SilentlyContinue) {
  $v = & git -C $Root describe --tags --always --dirty 2>$null
  if ($LASTEXITCODE -eq 0 -and -not $env:OA_VERSION) { $version = $v }
  $c = & git -C $Root rev-parse --short HEAD 2>$null
  if ($LASTEXITCODE -eq 0) { $commit = $c }
}
$pkg = "oa-agent/backend/internal/buildinfo"
New-Item -ItemType Directory -Force -Path $Out | Out-Null
Push-Location (Join-Path $Root "backend")
try {
  if ($Mode -eq "debug") {
    $exe = Join-Path $Out "oa-agent-debug.exe"
    & go build -tags debug -gcflags "all=-N -l" -ldflags "-X $pkg.Version=$version -X $pkg.Commit=$commit" -o $exe ./cmd/oa-agent
    if ($LASTEXITCODE -ne 0) { throw "go build failed" }
    Write-Host "built $exe (debug logging on; OA_LOG_LEVEL=info silences it)"
  } else {
    $exe = Join-Path $Out "oa-agent.exe"
    & go build -trimpath -ldflags "-s -w -X $pkg.Version=$version -X $pkg.Commit=$commit" -o $exe ./cmd/oa-agent
    if ($LASTEXITCODE -ne 0) { throw "go build failed" }
    Write-Host "built $exe"
  }
} finally { Pop-Location }
