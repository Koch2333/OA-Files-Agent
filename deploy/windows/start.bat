@echo off
rem ASCII only on purpose: cmd.exe mis-parses multi-byte characters in batch files after chcp 65001.
rem The Chinese texts live in start-banner.txt / start-stopped.txt and are printed with "type".
chcp 65001 >nul
cd /d "%~dp0"
title OA Agent (close this window to stop the service)
set OA_STANDALONE=1
type "%~dp0start-banner.txt"
oa-agent.exe
set "RC=%ERRORLEVEL%"
echo.
type "%~dp0start-stopped.txt"
echo (exit code %RC%)
pause
