@echo off
rem ASCII only on purpose (see start.bat). Asks again for the working directory (archive folder) and writes OA_ARCHIVE_ROOT to oa-agent.env.
chcp 65001 >nul
cd /d "%~dp0"
title OA Agent - change the working directory
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0first-run.ps1" -Root "%~dp0." -Change
echo.
pause
