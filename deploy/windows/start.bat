@echo off
rem ASCII only on purpose: cmd.exe mis-parses multi-byte characters in batch files after chcp 65001.
rem The Chinese texts live in start-banner.txt / start-stopped.txt (printed with "type") and in first-run.ps1
rem (UTF-8 with BOM), which asks for the working directory (archive folder) on the first run only.
chcp 65001 >nul
cd /d "%~dp0"
title OA Agent (close this window to stop the service)
set OA_STANDALONE=1
if exist "%~dp0first-run.ps1" (
  powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0first-run.ps1" -Root "%~dp0."
  if errorlevel 3 if not errorlevel 4 goto aborted
)
type "%~dp0start-banner.txt"
oa-agent.exe
set "RC=%ERRORLEVEL%"
echo.
type "%~dp0start-stopped.txt"
echo (exit code %RC%)
pause
exit /b %RC%
:aborted
echo.
type "%~dp0start-stopped.txt"
pause
exit /b 3
