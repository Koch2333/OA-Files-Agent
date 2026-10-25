'use strict'
// Preload of the MAIN window (the web UI). It exposes window.oaDesktop so the web Settings page and user menu can offer the
// desktop-only settings. Only fixed channels; the main process checks that the caller is the app window on the app origin.
const { contextBridge, ipcRenderer, webUtils } = require('electron')
contextBridge.exposeInMainWorld('oaDesktop', {
  isDesktop: true,
  /** { version, archiveRoot, dataDir, portable } */
  info: () => ipcRenderer.invoke('desktop:info'),
  /** true when the app runs as administrator (Windows blocks drag-and-drop from Explorer into such a window). */
  elevated: () => ipcRenderer.invoke('desktop:elevated'),
  openSettings: () => ipcRenderer.invoke('desktop:open-settings'),
  openFolder: (what) => ipcRenderer.invoke('desktop:open-folder', String(what)),
  /** Opens an allow-listed https provider login page in the system browser (the main process validates it). */
  openExternal: (url) => ipcRenderer.invoke('desktop:open-external', String(url)),
  /** Opens the in-app key window for a provider preset id. opts: { mode: 'create' | 'login' (+ authorizeUrl) | 'manage' }. Resolves { ok, message? }. */
  openKeyHelper: (providerId, opts) => ipcRenderer.invoke('desktop:open-key-helper', String(providerId), opts && typeof opts === 'object' ? { mode: String(opts.mode || 'create'), authorizeUrl: opts.authorizeUrl ? String(opts.authorizeUrl) : '' } : {}),
  /** Answer to a { type:'request', id, op } event of the key window (status / test / remove of the saved key). */
  keyHelperReply: (id, data) => ipcRenderer.invoke('desktop:keyhelper-reply', Number(id), data),
  /** Events from the key window: key | closed | oauth-callback | opened | replace | switch-provider | request (see utils/desktop.ts). Returns an unsubscribe function. */
  onKeyHelper: (cb) => {
    const h = (_e, d) => { try { cb(d) } catch { /* the page's callback must not break the bridge */ } }
    ipcRenderer.on('keyhelper:event', h)
    return () => ipcRenderer.removeListener('keyhelper:event', h)
  },
  /** Local absolute paths of File objects from a drop / file input (webUtils.getPathForFile); "" for files without a path. Synchronous. */
  pathsForFiles: (files) => {
    const out = []
    for (const f of Array.from(files || [])) {
      try { out.push(webUtils.getPathForFile(f) || '') } catch { out.push('') }
    }
    return out
  },
  /** Native choose-files / choose-folder dialog; resolves to absolute paths ([] when cancelled). */
  pickPaths: (opts) => ipcRenderer.invoke('desktop:pick-paths', { folders: !!(opts && opts.folders === true) }),
})
