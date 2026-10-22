# Creates a maximum-compression ZIP of a folder, optionally under a top-level folder name.
#   powershell -File scripts\make-zip.ps1 -Source <dir> -Zip <file.zip> [-TopFolder name]
# Uses the worker venv's Python (zipfile, deflate level 9) when present, else .NET Optimal compression.
param([Parameter(Mandatory)][string]$Source, [Parameter(Mandatory)][string]$Zip, [string]$TopFolder = "")
$ErrorActionPreference = "Stop"
. "$PSScriptRoot\_common.ps1"
$Source = (Resolve-Path $Source).Path
if (-not [System.IO.Path]::IsPathRooted($Zip)) { $Zip = Join-Path (Get-Location) $Zip }
New-Item -ItemType Directory -Force (Split-Path -Parent $Zip) | Out-Null
Remove-IfExists $Zip
$py = Join-Path $RepoRoot "workers\document-ai\.venv\Scripts\python.exe"
if (-not (Test-Path $py)) { $c = Get-Command python -ErrorAction SilentlyContinue; if ($c) { $py = $c.Source } else { $py = $null } }
if ($py) {
  $code = @'
import sys, os, zipfile
src, out, top = sys.argv[1:4]
with zipfile.ZipFile(out, "w", zipfile.ZIP_DEFLATED, compresslevel=9) as z:
    for root, dirs, files in os.walk(src):
        for n in files:
            p = os.path.join(root, n)
            arc = os.path.relpath(p, src)
            z.write(p, os.path.join(top, arc) if top else arc)
'@
  $tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("oa-zip-" + [guid]::NewGuid().ToString("N") + ".py")
  Write-Utf8 $tmp $code
  try { Invoke-Native $py @($tmp, $Source, $Zip, $TopFolder) $RepoRoot } finally { Remove-Item $tmp -Force -ErrorAction SilentlyContinue }
} else {
  Add-Type -AssemblyName System.IO.Compression.FileSystem
  [System.IO.Compression.ZipFile]::CreateFromDirectory($Source, $Zip, [System.IO.Compression.CompressionLevel]::Optimal, ($TopFolder -ne ""))
}
Info ("zip: $Zip (" + (Format-Size (Get-Item $Zip).Length) + ")")
