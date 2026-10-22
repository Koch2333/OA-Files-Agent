'use strict'
// Folder-chooser window (first run, "change working directory"). Validation runs in the main process.
const path = require('path')
const { BrowserWindow, ipcMain, dialog } = require('electron')
const { validateDir } = require('./workdir')

/**
 * opts: { mode: 'first'|'change', defaultArchive, currentArchive, currentData, canData, ctxFor(kind) -> validate ctx }
 * Resolves { archive, data, mode } or null when cancelled/closed.
 */
function runSetup(opts) {
  return new Promise((resolve) => {
    const win = new BrowserWindow({
      width: 640, height: opts.mode === 'change' ? 470 : 500, resizable: false, minimizable: false, maximizable: false, title: opts.mode === 'change' ? '更改工作目录' : '选择归档文件夹',
      center: true, autoHideMenuBar: true, show: false, alwaysOnTop: true,
      webPreferences: { preload: path.join(__dirname, 'setup-preload.js'), contextIsolation: true, nodeIntegration: false, sandbox: true, webSecurity: true, devTools: false },
    })
    win.removeMenu()
    const mine = (e) => e.sender === win.webContents
    let done = false
    const finish = (v) => {
      if (done) return
      done = true
      for (const ch of ['setup:init', 'setup:browse', 'setup:validate', 'setup:submit', 'setup:cancel']) ipcMain.removeHandler(ch)
      if (!win.isDestroyed()) win.destroy()
      resolve(v)
    }
    ipcMain.handle('setup:init', (e) => mine(e) && { mode: opts.mode, defaultArchive: opts.defaultArchive, currentArchive: opts.currentArchive, currentData: opts.currentData, canData: opts.canData })
    ipcMain.handle('setup:browse', async (e, kind) => {
      if (!mine(e)) return null
      win.setAlwaysOnTop(false)
      const r = await dialog.showOpenDialog(win, { title: kind === 'data' ? '选择数据目录' : '选择归档文件夹', defaultPath: kind === 'data' ? opts.currentData : opts.defaultArchive, properties: ['openDirectory', 'createDirectory'] })
      win.setAlwaysOnTop(true)
      return r.canceled ? null : r.filePaths[0]
    })
    ipcMain.handle('setup:validate', (e, kind, p) => {
      if (!mine(e)) return null
      const k = kind === 'data' ? 'data' : 'archive'
      return validateDir(p, opts.ctxFor(k))
    })
    ipcMain.handle('setup:submit', async (e, payload) => {
      if (!mine(e)) return
      const a = await validateDir(payload.archive, opts.ctxFor('archive'))
      if (!a.ok) return
      let data = opts.currentData
      if (opts.canData && payload.data && path.resolve(payload.data).toLowerCase() !== path.resolve(opts.currentData).toLowerCase()) {
        const d = await validateDir(payload.data, opts.ctxFor('data'))
        if (!d.ok) return
        data = d.resolved
      }
      finish({ archive: a.resolved, data, mode: payload.mode === 'migrate' ? 'migrate' : 'use' })
    })
    ipcMain.handle('setup:cancel', (e) => mine(e) && finish(null))
    win.webContents.on('will-navigate', (e) => e.preventDefault())
    win.webContents.setWindowOpenHandler(() => ({ action: 'deny' }))
    win.once('ready-to-show', () => win.show())
    win.on('closed', () => finish(null))
    win.loadFile(path.join(__dirname, 'setup.html'))
  })
}

module.exports = { runSetup }
