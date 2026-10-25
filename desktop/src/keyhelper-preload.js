'use strict'
// Preload of the key-helper PANEL only (never of the provider page, which has no preload at all). Fixed channels; the
// main process checks the sender and the action name again.
const { contextBridge, ipcRenderer } = require('electron')
contextBridge.exposeInMainWorld('oaKeyHelper', {
  init: () => ipcRenderer.invoke('keyhelper:init'),
  /** next | prev | use (arg = optional key name) | ignore | reload | back | system-browser | clear-data | cancel | toggle */
  act: (name, arg) => ipcRenderer.invoke('keyhelper:act', String(name), arg === undefined ? '' : String(arg)),
  onState: (cb) => ipcRenderer.on('keyhelper:state', (_e, s) => cb(s)),
})
