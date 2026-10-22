'use strict'
// Preload for the folder-chooser window only (no Node exposed).
const { contextBridge, ipcRenderer } = require('electron')
contextBridge.exposeInMainWorld('oaSetup', {
  init: () => ipcRenderer.invoke('setup:init'),
  browse: (kind) => ipcRenderer.invoke('setup:browse', kind),
  validate: (kind, p) => ipcRenderer.invoke('setup:validate', kind, String(p)),
  submit: (payload) => ipcRenderer.invoke('setup:submit', payload),
  cancel: () => ipcRenderer.invoke('setup:cancel'),
})
