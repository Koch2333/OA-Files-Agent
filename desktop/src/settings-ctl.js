'use strict'
// "设置" window of the desktop edition: working directory, data directory, start-up and window options, ports and
// diagnostics in one place. Everything is persisted (desktop-config.json / OS login item); nothing has to be chosen
// again on the next launch. All work happens in the main process through the callbacks in `actions`.
const path = require('path')
const { BrowserWindow, ipcMain } = require('electron')
const { normalizeOption } = require('./settings-model')

const CHANNELS = ['settings:state', 'settings:change-archive', 'settings:migrate-data', 'settings:open', 'settings:set', 'settings:copy-diagnostics', 'settings:close']

/**
 * actions: { getState(), changeArchive(), migrateData(), open(what), setOption(key, value), copyDiagnostics(), parent() }
 * Returns { show(), refresh(), isOpen() }.
 */
function createSettings(actions) {
  let win = null

  const mine = (e) => !!win && !win.isDestroyed() && e.sender === win.webContents
  const guarded = (fn) => async (e, ...args) => {
    if (!mine(e)) return null
    try {
      return { ok: true, data: await fn(...args) }
    } catch (err) {
      return { ok: false, error: (err && err.message) || String(err) }
    }
  }

  function registerHandlers() {
    ipcMain.handle('settings:state', guarded(() => actions.getState()))
    ipcMain.handle('settings:change-archive', guarded(async () => {
      await actions.changeArchive()
      return actions.getState()
    }))
    ipcMain.handle('settings:migrate-data', guarded(async () => {
      await actions.migrateData()
      return actions.getState()
    }))
    ipcMain.handle('settings:open', guarded((what) => actions.open(what)))
    ipcMain.handle('settings:set', guarded(async (key, value) => {
      const v = normalizeOption(key, value) // refuses unknown keys and bad values
      await actions.setOption(key, v)
      return actions.getState()
    }))
    ipcMain.handle('settings:copy-diagnostics', guarded(() => actions.copyDiagnostics()))
    ipcMain.handle('settings:close', guarded(() => win && win.close()))
  }

  function show() {
    if (win && !win.isDestroyed()) {
      if (win.isMinimized()) win.restore()
      win.show()
      win.focus()
      return
    }
    const parent = actions.parent && actions.parent()
    win = new BrowserWindow({
      width: 720, height: 780, minWidth: 560, minHeight: 520, title: '设置',
      parent: parent && !parent.isDestroyed() ? parent : undefined,
      center: true, autoHideMenuBar: true, show: false,
      webPreferences: { preload: path.join(__dirname, 'settings-preload.js'), contextIsolation: true, nodeIntegration: false, sandbox: true, webSecurity: true, devTools: false },
    })
    win.removeMenu()
    registerHandlers()
    win.webContents.on('will-navigate', (e) => e.preventDefault())
    win.webContents.setWindowOpenHandler(() => ({ action: 'deny' }))
    win.once('ready-to-show', () => win.show())
    win.on('closed', () => {
      for (const ch of CHANNELS) ipcMain.removeHandler(ch)
      win = null
    })
    win.loadFile(path.join(__dirname, 'settings.html'))
  }

  return {
    show,
    isOpen: () => !!win && !win.isDestroyed(),
    /** Tell an open window that the state changed behind its back (backend restarted, directory switched…). */
    refresh: () => { if (win && !win.isDestroyed()) win.webContents.send('settings:changed') },
  }
}

module.exports = { createSettings }
