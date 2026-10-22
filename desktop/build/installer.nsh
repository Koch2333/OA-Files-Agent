; Custom NSIS hooks for electron-builder (see electron-builder.yml: nsis.include).
; Keep this file UTF-8 with BOM so the Chinese strings survive.
!include nsDialogs.nsh
!include LogicLib.nsh
!include "getProcessInfo.nsh"
Var pid

!ifdef BUILD_UNINSTALLER
Var oaDeleteData
Var oaDeleteCheck
!endif

; Wait (up to 60 s) for the app's own processes (backend, PostgreSQL, worker) under $INSTDIR to finish their orderly
; shutdown, then force-stop whatever is still left, so no file stays locked.
!macro oaStopStrays
  nsExec::Exec `"$SYSDIR\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -NonInteractive -Command "$$d='$INSTDIR'; if(-not $$d){exit 0}; for($$i=0;$$i -lt 60;$$i++){ $$p=@(Get-CimInstance Win32_Process | Where-Object { $$_.ExecutablePath -and $$_.ExecutablePath.StartsWith($$d,[System.StringComparison]::OrdinalIgnoreCase) -and $$_.ProcessId -ne $$PID }); if($$p.Count -eq 0){exit 0}; Start-Sleep -Seconds 1 }; $$p | ForEach-Object { Stop-Process -Id $$_.ProcessId -Force -ErrorAction SilentlyContinue }"`
  Pop $0
!macroend

; Installer: stop a running instance (built-in check closes the main window), then make sure helpers are gone.
!macro customCheckAppRunning
  !insertmacro IS_POWERSHELL_AVAILABLE
  !insertmacro _CHECK_APP_RUNNING
  !insertmacro oaStopStrays
!macroend

!macro customInstall
  WriteRegStr SHELL_CONTEXT "Software\OA-Agent" "Version" "${VERSION}"
  WriteRegStr SHELL_CONTEXT "Software\OA-Agent" "InstallDir" "$INSTDIR"
  WriteRegStr SHELL_CONTEXT "Software\OA-Agent" "Publisher" "OA Agent"
  !insertmacro oaWritePresetFile
!macroend

; Uninstaller: custom first page with the "delete my data" checkbox (default: keep data).
!macro oaUnPageFuncs
Function un.oaDataPageCreate
  nsDialogs::Create 1018
  Pop $0
  ${If} $0 == error
    Abort
  ${EndIf}
  ${NSD_CreateLabel} 0 0 100% 36u "即将卸载 OA 文件智能归档。$\r$\n你的数据（数据库、设置、日志）保存在 $LOCALAPPDATA\OA-Agent，默认会保留，重新安装后可继续使用。"
  Pop $0
  ${NSD_CreateCheckbox} 0 46u 100% 12u "同时删除我的数据（数据库、设置、日志，不可恢复）"
  Pop $oaDeleteCheck
  ${NSD_CreateLabel} 0 64u 100% 24u "归档文件夹中已归档的文件不受影响，不会被删除。"
  Pop $0
  nsDialogs::Show
FunctionEnd

Function un.oaDataPageLeave
  ${NSD_GetState} $oaDeleteCheck $oaDeleteData
FunctionEnd
!macroend

!macro customUnWelcomePage
  !insertmacro oaUnPageFuncs
  UninstPage custom un.oaDataPageCreate un.oaDataPageLeave
!macroend

!macro customUnInstall
  !insertmacro oaStopStrays
  DeleteRegKey SHELL_CONTEXT "Software\OA-Agent"
  ${If} $oaDeleteData == ${BST_CHECKED}
    RMDir /r "$LOCALAPPDATA\OA-Agent"
  ${EndIf}
!macroend

; ---- Installer page: working (archive) directory -------------------------------------------------------------
; Writes %LOCALAPPDATA%\OA-Agent\install-preset.ini (UTF-16LE with BOM) which the app reads on first run.
; Silent installs (/S) skip the page and write nothing unless  /ARCHIVE="D:\some dir"  is given.
!ifndef BUILD_UNINSTALLER
Var oaArchive
Var oaArchiveText
Var oaWritePreset
!endif

!macro oaArchivePageFuncs
Function oaArchivePageCreate
  nsDialogs::Create 1018
  Pop $0
  ${If} $0 == error
    Abort
  ${EndIf}
  !insertmacro MUI_HEADER_TEXT "工作目录" "选择归档文件的存放位置"
  ${NSD_CreateLabel} 0 0 100% 30u "归档后的文件会存放在这个文件夹，建议选择空间充足的磁盘（不要放在程序安装目录内）。以后可在程序菜单“文件 → 更改工作目录”中修改。"
  Pop $0
  ${NSD_CreateText} 0 40u 78% 12u "$oaArchive"
  Pop $oaArchiveText
  ${NSD_CreateBrowseButton} 80% 39u 20% 14u "浏览…"
  Pop $0
  ${NSD_OnClick} $0 oaArchiveBrowse
  nsDialogs::Show
FunctionEnd

Function oaArchiveBrowse
  nsDialogs::SelectFolderDialog "选择归档文件夹" "$oaArchive"
  Pop $0
  ${If} $0 != error
    ${NSD_SetText} $oaArchiveText $0
  ${EndIf}
FunctionEnd

Function oaArchivePageLeave
  ${NSD_GetText} $oaArchiveText $oaArchive
  ${If} $oaArchive == ""
    MessageBox MB_OK|MB_ICONEXCLAMATION "请选择归档文件夹。"
    Abort
  ${EndIf}
  StrCpy $oaWritePreset "1"
FunctionEnd
!macroend

!macro customInit
  StrCpy $oaArchive "$DOCUMENTS\OA-Agent\归档"
  StrCpy $oaWritePreset "0"
  ClearErrors
  ${GetParameters} $R0
  ${GetOptions} $R0 "/ARCHIVE=" $R1
  ${IfNot} ${Errors}
    ${If} $R1 != ""
      StrCpy $oaArchive $R1
      StrCpy $oaWritePreset "1"
    ${EndIf}
  ${EndIf}
!macroend

!macro customPageAfterChangeDir
  !insertmacro oaArchivePageFuncs
  Page custom oaArchivePageCreate oaArchivePageLeave
!macroend

!macro oaWritePresetFile
  ${If} $oaWritePreset == "1"
    CreateDirectory "$LOCALAPPDATA\OA-Agent"
    FileOpen $0 "$LOCALAPPDATA\OA-Agent\install-preset.ini" w
    FileWriteUTF16LE /BOM $0 "[oa]$\r$\narchiveRoot=$oaArchive$\r$\n"
    FileClose $0
  ${EndIf}
!macroend
