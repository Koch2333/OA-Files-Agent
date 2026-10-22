# Builds the document worker with PyInstaller (onedir) into dist\worker-dist\document-worker.
#   powershell -ExecutionPolicy Bypass -File scripts\build-worker.ps1 [-Clean]
# Needs workers\document-ai\.venv with requirements.txt + pyinstaller installed:
#   python -m venv workers\document-ai\.venv ; workers\document-ai\.venv\Scripts\pip install -r workers\document-ai\requirements.txt pyinstaller
param([switch]$Clean)
$ErrorActionPreference = "Stop"
. "$PSScriptRoot\_common.ps1"
$wk = Join-Path $RepoRoot "workers\document-ai"
$py = Join-Path $wk ".venv\Scripts\python.exe"
if (-not (Test-Path $py)) { Fail "Create the venv first: python -m venv workers\document-ai\.venv ; .venv\Scripts\pip install -r requirements.txt pyinstaller" }
& $py -c "import PyInstaller" 2>$null
if ($LASTEXITCODE -ne 0) { Fail "PyInstaller missing in the worker venv: workers\document-ai\.venv\Scripts\pip install pyinstaller" }
$dist = Join-Path $RepoRoot "dist\worker-dist"
$work = Join-Path $RepoRoot "dist\worker-build"
Step "Document worker (PyInstaller onedir)"
if ($Clean) { Remove-IfExists $dist; Remove-IfExists $work }
Invoke-Native $py @("-m", "PyInstaller", "--noconfirm", "--clean", "--onedir", "--name", "document-worker",
  "--distpath", $dist, "--workpath", $work, "--specpath", $work,
  "--collect-all", "rapidocr_onnxruntime", "--collect-all", "pymupdf", "--collect-all", "docx", "--collect-all", "openpyxl",
  "--hidden-import", "extract_core", "--hidden-import", "server", "--paths", ".", "worker_main.py") $wk
$exe = Join-Path $dist "document-worker\document-worker.exe"
if (-not (Test-Path $exe)) { Fail "worker build produced no $exe" }
Info ("worker: " + (Format-Size (Get-DirSize (Join-Path $dist "document-worker"))))
