# Builds the single-folder Windows package: dist\oa-agent (exe + web + worker + embedded PostgreSQL).
# Run on Windows with Go, Node.js and the worker virtualenv (workers\document-ai\.venv) available.
#   powershell -ExecutionPolicy Bypass -File deploy\windows\build.ps1
param(
  [string]$Out = "dist\oa-agent",
  [string]$PgJarUrl = "https://repo1.maven.org/maven2/io/zonky/test/postgres/embedded-postgres-binaries-windows-amd64/16.9.0/embedded-postgres-binaries-windows-amd64-16.9.0.jar",
  [switch]$SkipFrontend,
  [switch]$SkipWorker
)
$ErrorActionPreference = "Stop"
$Root = Resolve-Path (Join-Path $PSScriptRoot "..\..")
Set-Location $Root
$Out = Join-Path $Root $Out
$Cache = Join-Path $Root "dist\cache"
New-Item -ItemType Directory -Force $Cache | Out-Null

function Step($m) { Write-Host "==> $m" -ForegroundColor Cyan }
function Run($exe, $argList, $dir) {
  Push-Location $dir
  try { & $exe @argList; if ($LASTEXITCODE -ne 0) { throw "$exe failed with exit code $LASTEXITCODE" } } finally { Pop-Location }
}

Step "Cleaning $Out"
if (Test-Path $Out) { Remove-Item -Recurse -Force $Out }
New-Item -ItemType Directory -Force $Out | Out-Null

Step "Go backend"
Run "go" @("build", "-trimpath", "-ldflags", "-s -w", "-o", (Join-Path $Out "oa-agent.exe"), "./cmd/oa-agent") (Join-Path $Root "backend")

if (-not $SkipFrontend) {
  Step "Frontend"
  $fe = Join-Path $Root "frontend"
  Run "npm" @("ci") $fe
  Run "npm" @("run", "build") $fe
}
Copy-Item -Recurse (Join-Path $Root "frontend\dist") (Join-Path $Out "web")

if (-not $SkipWorker) {
  Step "Document worker (PyInstaller onedir)"
  $wk = Join-Path $Root "workers\document-ai"
  $py = Join-Path $wk ".venv\Scripts\python.exe"
  if (-not (Test-Path $py)) { throw "Create the venv first: python -m venv workers\document-ai\.venv; .venv\Scripts\pip install -r requirements.txt pyinstaller" }
  $work = Join-Path $Root "dist\worker-build"
  Run $py @("-m", "PyInstaller", "--noconfirm", "--clean", "--onedir", "--name", "document-worker",
    "--distpath", (Join-Path $Root "dist\worker-dist"), "--workpath", $work, "--specpath", $work,
    "--collect-all", "rapidocr_onnxruntime", "--collect-all", "pymupdf", "--collect-all", "docx", "--collect-all", "openpyxl",
    "--hidden-import", "extract_core", "--hidden-import", "server", "--paths", ".", "worker_main.py") $wk
}
Copy-Item -Recurse (Join-Path $Root "dist\worker-dist\document-worker") (Join-Path $Out "worker")

Step "Embedded PostgreSQL"
$jar = Join-Path $Cache "pg-win.jar"
if (-not (Test-Path $jar)) { Invoke-WebRequest -Uri $PgJarUrl -OutFile $jar }
$pgx = Join-Path $Cache "pgx"
if (-not (Test-Path (Join-Path $pgx "bin\pg_ctl.exe"))) {
  New-Item -ItemType Directory -Force $pgx | Out-Null
  Run "tar" @("-xf", $jar) $pgx
  Run "tar" @("-xf", "postgres-windows-x86_64.txz") $pgx
}
$pg = Join-Path $Out "pgsql"
New-Item -ItemType Directory -Force $pg | Out-Null
foreach ($d in "bin", "lib", "share") { Copy-Item -Recurse (Join-Path $pgx $d) (Join-Path $pg $d) }

Step "Assemble"
Copy-Item -Recurse (Join-Path $Root "migrations") (Join-Path $Out "migrations")
Get-ChildItem (Join-Path $Out "migrations") -Filter "*.down.sql" | Remove-Item
Copy-Item (Join-Path $PSScriptRoot "start.bat") $Out
Copy-Item (Join-Path $PSScriptRoot "oa-agent.env.template") (Join-Path $Out "oa-agent.env")
Copy-Item (Join-Path $Root "docs\用户手册.md") $Out -ErrorAction SilentlyContinue
Copy-Item (Join-Path $Root "docs\THIRD-PARTY-LICENSES.md") $Out -ErrorAction SilentlyContinue
Copy-Item -Recurse (Join-Path $Root "tests\samples") (Join-Path $Out "演示样本") -ErrorAction SilentlyContinue

Step "Done"
$size = [math]::Round(((Get-ChildItem -Recurse $Out | Measure-Object Length -Sum).Sum / 1MB))
Write-Host "Package: $Out  ($size MB). Zip the folder to distribute; run start.bat to launch." -ForegroundColor Green
