'use strict'
// Preload of the MAIN window (the web UI). It exposes window.oaDesktop so the web Settings page and user menu can offer the
// desktop-only settings. Only fixed channels; the main process checks that the caller is the app window on the app origin.
const { contextBridge, ipcRenderer } = require('electron')
contextBridge.exposeInMainWorld('oaDesktop', {
  isDesktop: true,
  /** { version, archiveRoot, dataDir, portable } */
  info: () => ipcRenderer.invoke('desktop:info'),
  openSettings: () => ipcRenderer.invoke('desktop:open-settings'),
  openFolder: (what) => ipcRenderer.invoke('desktop:open-folder', String(what)),
})
