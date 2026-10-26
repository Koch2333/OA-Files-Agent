# Builds the document worker with PyInstaller (onedir) into dist\worker-dist\document-worker.
#   powershell -ExecutionPolicy Bypass -File scripts\build-worker.ps1 [-Clean]
# Needs workers\document-ai\.venv with requirements.txt + pyinstaller installed:
#   python -m venv workers\document-ai\.venv ; workers\document-ai\.venv\Scripts\pip install -r workers\document-ai\requirements.txt pyinstaller
# The worker is ALWAYS an x64 build, also inside the windows-arm64 packages (opencv-python and PyMuPDF publish no win_arm64
# wheels; Windows 11 on Arm runs it under x64 emulation). The venv Python must therefore be x64 (python -c "import platform").
param([switch]$Clean)
$ErrorActionPreference = "Stop"
. "$PSScriptRoot\_common.ps1"
$wk = Join-Path $RepoRoot "workers\document-ai"
$py = Join-Path $wk ".venv\Scripts\python.exe"
if (-not (Test-Path $py)) { Fail "Create the venv first: python -m venv workers\document-ai\.venv ; .venv\Scripts\pip install -r requirements.txt pyinstaller" }
$machine = (& $py -c "import platform; print(platform.machine())").Trim()
if ($machine -ne "AMD64") { Fail "The worker venv Python is $machine; the worker must be built with an x64 (AMD64) Python 3.12 (see the comment at the top of this script)" }
& $py -c "import PyInstaller" 2>$null
if ($LASTEXITCODE -ne 0) { Fail "PyInstaller missing in the worker venv: workers\document-ai\.venv\Scripts\pip install pyinstaller" }
$dist = Join-Path $RepoRoot "dist\worker-dist"
$work = Join-Path $RepoRoot "dist\worker-build"
Step "Document worker (PyInstaller onedir)"
if ($Clean) { Remove-IfExists $dist; Remove-IfExists $work }
Invoke-Native $py @("-m", "PyInstaller", "--noconfirm", "--clean", "--onedir", "--name", "document-worker",
  "--distpath", $dist, "--workpath", $work, "--specpath", $work,
  "--collect-all", "rapidocr_onnxruntime", "--collect-all", "pymupdf", "--collect-all", "docx", "--collect-all", "openpyxl",
  "--collect-submodules", "xlrd", "--collect-submodules", "olefile",
  "--hidden-import", "extract_core", "--hidden-import", "server", "--hidden-import", "formats_data", "--hidden-import", "fmt_common",
  "--hidden-import", "sniff", "--hidden-import", "ole_formats", "--hidden-import", "text_formats", "--hidden-import", "zip_formats",
  "--paths", ".", "worker_main.py") $wk
$exe = Join-Path $dist "document-worker\document-worker.exe"
if (-not (Test-Path $exe)) { Fail "worker build produced no $exe" }
Info ("worker: " + (Format-Size (Get-DirSize (Join-Path $dist "document-worker"))))
