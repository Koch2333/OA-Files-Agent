# Verifies the PE machine types inside an unpacked Windows package (the architecture guard of the release pipeline).
#   powershell -ExecutionPolicy Bypass -File scripts\verify-windows-package.ps1 -Dir <unpacked desktop app | server bundle> -Arch x64|arm64
# Expected: Electron, its native DLLs and oa-agent.exe match -Arch; the document worker and the embedded PostgreSQL are
# x64 in every package (no win_arm64 build exists; on Windows on Arm they run under x64 emulation).
# Exit code 1 and a table when anything is off. ASCII only.
param([Parameter(Mandatory)][string]$Dir, [Parameter(Mandatory)][ValidateSet("x64", "arm64")][string]$Arch)
$ErrorActionPreference = "Stop"
. "$PSScriptRoot\_common.ps1"
$Dir = (Resolve-Path $Dir).Path
$server = Join-Path $Dir "resources\app-server"
$desktop = Test-Path (Join-Path $Dir "OA-Agent-Desktop.exe")
if (-not $desktop) { $server = $Dir }
$checks = @()   # path, expected
if ($desktop) {
  $checks += , @("OA-Agent-Desktop.exe", $Arch)
  foreach ($n in "ffmpeg.dll", "libEGL.dll", "libGLESv2.dll") { if (Test-Path (Join-Path $Dir $n)) { $checks += , @($n, $Arch) } }
}
$checks += , @((Join-Path $server "oa-agent.exe").Substring($Dir.Length).TrimStart("\"), $Arch)
foreach ($p in "worker\document-worker.exe", "pgsql\bin\postgres.exe", "pgsql\bin\pg_ctl.exe", "pgsql\bin\initdb.exe") { $checks += , @((Join-Path $server $p).Substring($Dir.Length).TrimStart("\"), "x64") }
$bad = 0
foreach ($c in $checks) {
  $f = Join-Path $Dir $c[0]
  if (-not (Test-Path $f)) { $got = "MISSING"; } else { $got = Get-PeMachine $f }
  $ok = $got -eq $c[1]
  if (-not $ok) { $bad++ }
  Write-Host ("{0,-4} {1,-45} expected {2,-6} found {3}" -f $(if ($ok) { "ok" } else { "FAIL" }), $c[0], $c[1], $got)
}
if (-not $desktop) { Write-Host "(server bundle: no Electron checks)" }
if ($bad -gt 0) { Write-Host "$bad architecture check(s) failed for -Arch $Arch" -ForegroundColor Red; exit 1 }
Write-Host "architecture check passed ($Arch)" -ForegroundColor Green
