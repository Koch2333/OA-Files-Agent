# OA 文件智能归档 —— 服务端包首次运行：询问工作目录（归档文件夹），写入 oa-agent.env 的 OA_ARCHIVE_ROOT。
# 由 start.bat 在每次启动前调用；只有「首次运行」才会提问：
#   - 环境变量和 oa-agent.env 里都没有生效的 OA_ARCHIVE_ROOT，并且
#   - 数据目录（OA_DATA_DIR，默认 <程序目录>\data）里还没有数据库（pg\PG_VERSION）和默认归档文件夹 data\archive。
# 写入后不会再问；以后要更改，直接编辑 oa-agent.env（或删除其中的 OA_ARCHIVE_ROOT 行并在数据为空时重新运行）。
#
# 交互：先弹出文件夹选择窗口（System.Windows.Forms.FolderBrowserDialog）；取消或无法显示窗口时改为控制台提问
# （直接回车 = 默认位置）。设置 OA_FIRSTRUN_CONSOLE=1 可跳过窗口（测试/远程会话），OA_FIRSTRUN_SKIP=1 完全跳过。
# 退出码：0 = 已配置或无需配置；3 = 用户放弃或无法写入配置（start.bat 不启动服务）。
# 本文件必须保存为 UTF-8 带 BOM，否则 Windows PowerShell 5 会把中文读成乱码。
param([string]$Root = "", [switch]$Change)
$ErrorActionPreference = "Stop"
Set-StrictMode -Version 2
try { [Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false) } catch { }
try { [Console]::InputEncoding = New-Object System.Text.UTF8Encoding($false) } catch { }

if (-not $Root) { $Root = $PSScriptRoot }
$Root = [System.IO.Path]::GetFullPath($Root.TrimEnd('\', '/', '.') + '\')
$Root = $Root.TrimEnd('\')
$EnvFile = Join-Path $Root "oa-agent.env"

function Say([string]$t = "") { [Console]::Out.WriteLine($t) }

# ---------- 是否需要询问 ----------
function Read-EnvValue([string]$Key) {
  if (-not (Test-Path -LiteralPath $EnvFile)) { return "" }
  foreach ($line in [System.IO.File]::ReadAllLines($EnvFile, [System.Text.Encoding]::UTF8)) {
    $l = $line.Trim().TrimStart([char]0xFEFF)
    if ($l -eq "" -or $l.StartsWith("#")) { continue }
    $i = $l.IndexOf("=")
    if ($i -lt 1) { continue }
    if ($l.Substring(0, $i).Trim() -eq $Key) {
      $v = $l.Substring($i + 1).Trim().Trim('"')
      if ($v) { return $v }
    }
  }
  return ""
}

$dataDir = $env:OA_DATA_DIR
if (-not $dataDir) { $dataDir = Read-EnvValue "OA_DATA_DIR" }
if (-not $dataDir) { $dataDir = Join-Path $Root "data" }
$currentRoot = ""
if ($Change) {
  # change-workdir.bat: ask again at any time (the first-run rules below do not apply)
  if ($env:OA_ARCHIVE_ROOT) {
    Say ("  工作目录由系统环境变量 OA_ARCHIVE_ROOT 指定（" + $env:OA_ARCHIVE_ROOT + "），环境变量优先于 oa-agent.env，请先删除该环境变量再更改。")
    exit 3
  }
  $currentRoot = Read-EnvValue "OA_ARCHIVE_ROOT"
  if (-not $currentRoot) { $currentRoot = Join-Path $dataDir "archive" }
} else {
  if ($env:OA_FIRSTRUN_SKIP -eq "1") { exit 0 }
  if ($env:OA_ARCHIVE_ROOT) { exit 0 }
  if (Read-EnvValue "OA_ARCHIVE_ROOT") { exit 0 }
  if ((Test-Path -LiteralPath (Join-Path $dataDir "pg\PG_VERSION")) -or (Test-Path -LiteralPath (Join-Path $dataDir "archive"))) {
    exit 0 # 已有数据：沿用默认归档位置 data\archive；以后要更改请运行 change-workdir.bat
  }
}

# ---------- 校验 ----------
function Test-Inside([string]$Child, [string]$Parent) {
  if (-not $Parent) { return $false }
  $c = $Child.TrimEnd('\') + '\'
  $p = [System.IO.Path]::GetFullPath($Parent).TrimEnd('\') + '\'
  return $c.StartsWith($p, [System.StringComparison]::OrdinalIgnoreCase)
}

function Nearest-Existing([string]$p) {
  $cur = $p
  while ($cur -and -not (Test-Path -LiteralPath $cur)) {
    $up = Split-Path -Parent $cur
    if (-not $up -or $up -eq $cur) { break }
    $cur = $up
  }
  return $cur
}

# 返回 @{ Ok; Path; Error }
function Test-ArchiveDir([string]$Raw) {
  $r = @{ Ok = $false; Path = ""; Error = "" }
  $s = ("" + $Raw).Trim().Trim('"').Trim()
  if (-not $s) { $r.Error = "请输入一个文件夹路径。"; return $r }
  if ($s -notmatch '^(?:[A-Za-z]:[\\/]|\\\\[^\\/]+[\\/][^\\/]+)') { $r.Error = "请输入完整的绝对路径（例如 D:\OA归档），不能用相对路径。"; return $r }
  if (($s -split '[\\/]+') -contains "..") { $r.Error = "路径中不能包含「..」。"; return $r }
  if ($s.IndexOfAny([System.IO.Path]::GetInvalidPathChars()) -ge 0 -or $s.Substring(2).Contains(":")) { $r.Error = "路径中含有不允许的字符。"; return $r }
  try { $full = [System.IO.Path]::GetFullPath($s).TrimEnd('\') } catch { $r.Error = "无效的路径：$($_.Exception.Message)"; return $r }
  $r.Path = $full
  if ($full -match '^[A-Za-z]:$') { $r.Error = "不能直接使用磁盘根目录，请新建一个专用文件夹（例如 D:\OA归档）。"; return $r }
  foreach ($d in @($env:SystemRoot, $env:windir, $env:ProgramFiles, ${env:ProgramFiles(x86)}, $env:ProgramW6432, $env:ProgramData)) {
    if ($d -and (Test-Inside $full $d)) { $r.Error = "不能使用系统或程序目录（$d）。"; return $r }
  }
  foreach ($d in @($env:USERPROFILE, [Environment]::GetFolderPath("MyDocuments"), [Environment]::GetFolderPath("Desktop"))) {
    if ($d -and ($full.TrimEnd('\') -ieq $d.TrimEnd('\'))) { $r.Error = "请选择专用的子文件夹，而不是 $d 本身。"; return $r }
  }
  if ((Test-Inside $full (Join-Path $Root "data")) -or (Test-Inside $full $dataDir)) { $r.Error = "归档文件夹不能放在数据目录（$dataDir）内。"; return $r }
  foreach ($sub in "web", "worker", "pgsql", "migrations") {
    if (Test-Inside $full (Join-Path $Root $sub)) { $r.Error = "不能放在程序自带的文件夹内（升级时会被覆盖）。"; return $r }
  }
  if ((Test-Path -LiteralPath $full) -and -not (Test-Path -LiteralPath $full -PathType Container)) { $r.Error = "该路径已存在，但不是文件夹。"; return $r }
  $probeDir = Nearest-Existing $full
  if (-not $probeDir -or -not (Test-Path -LiteralPath $probeDir)) { $r.Error = "找不到该磁盘或网络位置。"; return $r }
  $probe = Join-Path $probeDir (".oa-write-test-" + $PID + "-" + [Guid]::NewGuid().ToString("N"))
  try {
    [System.IO.File]::WriteAllText($probe, "x")
    Remove-Item -LiteralPath $probe -Force
  } catch {
    $r.Error = "没有写入权限：$probeDir"
    return $r
  }
  $r.Ok = $true
  return $r
}

function Free-Space([string]$p) {
  try {
    $q = Nearest-Existing $p
    $drive = [System.IO.Path]::GetPathRoot($q)
    $di = New-Object System.IO.DriveInfo($drive)
    return ("{0:N1} GB" -f ($di.AvailableFreeSpace / 1GB))
  } catch { return "" }
}

# ---------- 默认位置 ----------
$default = Join-Path $Root "归档"
if (-not (Test-ArchiveDir $default).Ok) {
  $default = Join-Path ([Environment]::GetFolderPath("MyDocuments")) "OA-Agent\归档"
}
if ($Change) { $default = $currentRoot }

# ---------- 交互 ----------
Say ""
Say "============================================================"
if ($Change) {
  Say "  更改【工作目录】（归档文件夹）"
  Say "  请先关闭正在运行的服务窗口。已有的归档文件不会被删除。"
  Say ("  当前工作目录：" + $currentRoot)
} else {
  Say "  首次运行：请选择【工作目录】（归档文件夹）"
  Say "  归档后的文件会按「部门/类型/日期」存放到这个文件夹，"
  Say "  建议选择空间充足的磁盘。以后随时可运行 change-workdir.bat 更改。"
}
Say "============================================================"
if (-not $Change) { Say ("  默认位置：" + $default) }
$fs = Free-Space $default
if ($fs) { Say ("  该磁盘剩余空间：" + $fs) }
Say ""

function Pick-Gui([string]$Initial) {
  if ($env:OA_FIRSTRUN_CONSOLE -eq "1") { return $null }
  if (-not [Environment]::UserInteractive) { return $null }
  try {
    Add-Type -AssemblyName System.Windows.Forms
    $owner = New-Object System.Windows.Forms.Form
    $owner.TopMost = $true
    $owner.ShowInTaskbar = $false
    $owner.StartPosition = "CenterScreen"
    $owner.Size = New-Object System.Drawing.Size(1, 1)
    $owner.Opacity = 0
    $owner.Show()
    $dlg = New-Object System.Windows.Forms.FolderBrowserDialog
    $dlg.Description = "选择工作目录（归档文件夹）。取消则在命令行窗口中输入，直接回车使用默认位置。"
    $dlg.ShowNewFolderButton = $true
    $start = Nearest-Existing $Initial
    if ($start -and (Test-Path -LiteralPath $start)) { $dlg.SelectedPath = $start }
    Say "  已打开文件夹选择窗口（如果没有看到，请查看任务栏）……"
    $res = $dlg.ShowDialog($owner)
    $owner.Close()
    if ($res -eq [System.Windows.Forms.DialogResult]::OK -and $dlg.SelectedPath) { return $dlg.SelectedPath }
  } catch {
    Say ("  无法显示文件夹选择窗口（" + $_.Exception.Message + "），改为在此输入。")
  }
  return $null
}

$chosen = $null
$picked = Pick-Gui $default
if ($picked) {
  $v = Test-ArchiveDir $picked
  if ($v.Ok) { $chosen = $v.Path } else { Say ("  所选文件夹不能使用：" + $v.Error); Say "" }
}
for ($try = 0; -not $chosen -and $try -lt 5; $try++) {
  if ($Change) { Say "  直接回车 = 保持当前位置不变；或输入/粘贴新的完整路径（例如 D:\OA归档）；输入 Q 取消。" } else { Say "  直接回车 = 使用默认位置；或输入/粘贴完整路径（例如 D:\OA归档）；输入 Q 放弃启动。" }
  [Console]::Out.Write("  工作目录> ")
  $line = [Console]::In.ReadLine()
  if ($null -eq $line) { $line = "" ; $eof = $true } else { $eof = $false }
  $line = $line.Trim()
  if ($line -ieq "q") { Say ($(if ($Change) { "  已取消，未做任何更改。" } else { "  已放弃，服务未启动。" })); exit 3 }
  if ($line -eq "") { $line = $default }
  $v = Test-ArchiveDir $line
  if ($v.Ok) { $chosen = $v.Path; break }
  Say ("  不能使用：" + $v.Error)
  Say ""
  if ($eof) { break }
}
if (-not $chosen) { Say "  未能确定工作目录，服务未启动。请重新运行 start.bat。"; exit 3 }

try {
  New-Item -ItemType Directory -Force -Path $chosen | Out-Null
} catch {
  Say ("  无法创建文件夹：" + $_.Exception.Message); exit 3
}

# ---------- 更改模式：与当前相同则无事可做；旧目录里有文件时询问是否复制（绝不覆盖、绝不删除旧文件） ----------
if ($Change) {
  if ($currentRoot -and ($chosen.TrimEnd('\') -ieq $currentRoot.TrimEnd('\'))) { Say "  与当前工作目录相同，无需更改。"; exit 0 }
  $hasFiles = $currentRoot -and (Test-Path -LiteralPath $currentRoot) -and (@(Get-ChildItem -LiteralPath $currentRoot -Force -ErrorAction SilentlyContinue).Count -gt 0)
  if ($hasFiles) {
    Say ""
    Say ("  旧工作目录里已有归档文件：" + $currentRoot)
    Say "  [1] 复制到新位置并切换（逐文件校验；已存在的同名文件不会被覆盖；旧文件夹保留，可稍后手动删除）【默认】"
    Say "  [2] 只切换，不复制（台账里在新位置找不到的文件会显示为“缺失”；适合新位置已经是现有副本时）"
    Say "  [3] 取消"
    [Console]::Out.Write("  请选择 1/2/3> ")
    $c = [Console]::In.ReadLine()
    if ($null -eq $c) { $c = "3" }
    $c = $c.Trim()
    if ($c -eq "") { $c = "1" }
    if ($c -eq "3" -or ($c -ne "1" -and $c -ne "2")) { Say "  已取消，未做任何更改。"; exit 3 }
    if ($c -eq "1") {
      if ((Test-Inside $chosen $currentRoot) -or (Test-Inside $currentRoot $chosen)) {
        Say "  复制时新旧文件夹不能互相包含。请换一个位置，或选择只切换。"
        exit 3
      }
      Say "  正在复制……（文件多时需要一些时间，请勿关闭窗口）"
      & robocopy.exe $currentRoot $chosen /E /XC /XN /XO /R:1 /W:1 /NFL /NDL /NP /NJH /NJS | Out-Null
      if ($LASTEXITCODE -ge 8) { Say ("  复制失败（robocopy 返回 " + $LASTEXITCODE + "），未做任何更改，旧目录原样保留。"); exit 3 }
      $bad = 0
      foreach ($f in Get-ChildItem -LiteralPath $currentRoot -Recurse -File -Force -ErrorAction SilentlyContinue) {
        $rel = $f.FullName.Substring($currentRoot.TrimEnd('\').Length).TrimStart('\')
        $t = Join-Path $chosen $rel
        if (-not (Test-Path -LiteralPath $t -PathType Leaf) -or ((Get-Item -LiteralPath $t -Force).Length -ne $f.Length)) { $bad++ }
      }
      if ($bad -gt 0) { Say ("  校验失败：有 " + $bad + " 个文件在新位置缺失或大小不一致，未做任何更改，旧目录原样保留。"); exit 3 }
      Say "  复制完成，逐文件校验通过。"
    }
  }
  # drop the previous OA_ARCHIVE_ROOT line (and our own comment above it) so the new one replaces it
  if (Test-Path -LiteralPath $EnvFile) {
    $kept = New-Object System.Collections.Generic.List[string]
    foreach ($l in [System.IO.File]::ReadAllLines($EnvFile, [System.Text.Encoding]::UTF8)) {
      if ($l -match '^\s*OA_ARCHIVE_ROOT\s*=') { continue }
      if ($l -match '^#\s*(首次运行时选择的|更改后的)工作目录') { continue }
      $kept.Add($l)
    }
    $bytes0 = [System.IO.File]::ReadAllBytes($EnvFile)
    $bom0 = $bytes0.Length -ge 3 -and $bytes0[0] -eq 0xEF -and $bytes0[1] -eq 0xBB -and $bytes0[2] -eq 0xBF
    [System.IO.File]::WriteAllText($EnvFile, (($kept -join "`r`n") + "`r`n"), (New-Object System.Text.UTF8Encoding($bom0)))
  }
}

# ---------- 写入 oa-agent.env（UTF-8，保留其它行与注释） ----------
$lines = New-Object System.Collections.Generic.List[string]
$bom = $false
if (Test-Path -LiteralPath $EnvFile) {
  $bytes = [System.IO.File]::ReadAllBytes($EnvFile)
  $bom = $bytes.Length -ge 3 -and $bytes[0] -eq 0xEF -and $bytes[1] -eq 0xBB -and $bytes[2] -eq 0xBF
  foreach ($l in [System.IO.File]::ReadAllLines($EnvFile, [System.Text.Encoding]::UTF8)) { $lines.Add($l) }
}
$stamp = (Get-Date).ToString("yyyy-MM-dd HH:mm")
$label = if ($Change) { "更改后的" } else { "首次运行时选择的" }
$entry = @("# ${label}工作目录（$stamp）。再次更改请运行 change-workdir.bat。", "OA_ARCHIVE_ROOT=$chosen")
$at = -1
for ($i = 0; $i -lt $lines.Count; $i++) { if ($lines[$i] -match '^\s*#\s*OA_ARCHIVE_ROOT\s*=') { $at = $i + 1; break } }
if ($at -ge 0) { $lines.InsertRange($at, [string[]]$entry) } else { $lines.Add(""); $lines.AddRange([string[]]$entry) }
try {
  $tmp = $EnvFile + ".tmp"
  [System.IO.File]::WriteAllText($tmp, (($lines -join "`r`n") + "`r`n"), (New-Object System.Text.UTF8Encoding($bom)))
  Move-Item -LiteralPath $tmp -Destination $EnvFile -Force
} catch {
  Say ("  无法写入 " + $EnvFile + "：" + $_.Exception.Message)
  Say "  请把程序放到可写的位置（不要放在 Program Files 内），或手动在 oa-agent.env 中设置 OA_ARCHIVE_ROOT。"
  exit 3
}
Say ""
Say ("  工作目录已设置为：" + $chosen)
Say ("  （已写入 " + $EnvFile + "）")
if ($Change) { Say "  重新运行 start.bat 后生效。旧工作目录里的文件没有被删除。" }
Say ""
exit 0
