'use strict'
// Preload for the settings window only (no Node exposed).
const { contextBridge, ipcRenderer } = require('electron')
contextBridge.exposeInMainWorld('oaSettings', {
  state: () => ipcRenderer.invoke('settings:state'),
  changeArchive: () => ipcRenderer.invoke('settings:change-archive'),
  migrateData: () => ipcRenderer.invoke('settings:migrate-data'),
  open: (what) => ipcRenderer.invoke('settings:open', String(what)),
  set: (key, value) => ipcRenderer.invoke('settings:set', String(key), value),
  copyDiagnostics: () => ipcRenderer.invoke('settings:copy-diagnostics'),
  close: () => ipcRenderer.invoke('settings:close'),
  onChanged: (cb) => ipcRenderer.on('settings:changed', () => cb()),
})
