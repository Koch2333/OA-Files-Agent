'use strict'
// Preload for the splash window only: two narrow channels, no Node exposed.
const { contextBridge, ipcRenderer } = require('electron')
contextBridge.exposeInMainWorld('oaSplash', {
  onState: (cb) => ipcRenderer.on('splash:state', (_e, s) => cb(s)),
  action: (name) => ipcRenderer.send('splash:action', String(name)),
})
