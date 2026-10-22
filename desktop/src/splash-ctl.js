'use strict'
// Splash window controller: frameless 480x320, always on top, staged progress with a minimum visible time per stage.
const path = require('path')
const { BrowserWindow, ipcMain } = require('electron')

const STAGES = [
  { text: '正在启动…', pct: 5 },
  { text: '正在启动数据库', pct: 18 },
  { text: '正在启动文档识别服务', pct: 42 },
  { text: '正在准备数据', pct: 64 },
  { text: '正在启动服务', pct: 82 },
  { text: '正在加载界面', pct: 94 },
]
const MIN_STAGE_MS = 450
const SLOW_HINT_MS = 20000
const ACTIONS = new Set(['retry', 'logs', 'data', 'quit'])

// Maps backend log lines to a stage index (higher = later).
function stageForLine(line) {
  if (/starting embedded PostgreSQL/i.test(line)) return 1
  if (/starting document worker/i.test(line)) return 2
  if (/database migrations applied/i.test(line)) return 3
  if (/OA API listening/i.test(line)) return 4
  return -1
}

class Splash {
  constructor({ version, onAction }) {
    this.version = version
    this.onAction = onAction
    this.target = 0
    this.shown = -1
    this.lastChange = 0
    this.timer = null
    this.slowTimer = null
    this.errored = false
    this.win = null
    this.closedByUs = false
    this.onUserClose = null
    this.handler = (e, name) => {
      if (this.win && e.sender === this.win.webContents && ACTIONS.has(name)) this.onAction(name)
    }
    ipcMain.on('splash:action', this.handler)
  }

  show(initial) {
    this.initial = initial || null
    this.frozen = false
    if (this.win && !this.win.isDestroyed()) return
    this.errored = false
    this.shown = -1
    this.target = 0
    this.closedByUs = false
    this.win = new BrowserWindow({
      width: 480, height: 320, frame: false, resizable: false, movable: true, minimizable: false, maximizable: false, fullscreenable: false,
      center: true, show: false, alwaysOnTop: true, skipTaskbar: true, transparent: false, hasShadow: true, title: 'OA 文件智能归档',
      backgroundColor: '#f6f7fb',
      webPreferences: { preload: path.join(__dirname, 'splash-preload.js'), contextIsolation: true, nodeIntegration: false, sandbox: true, webSecurity: true, devTools: false },
    })
    const wc = this.win.webContents
    wc.on('will-navigate', (e) => e.preventDefault())
    wc.setWindowOpenHandler(() => ({ action: 'deny' }))
    this.win.once('ready-to-show', () => this.win && this.win.show())
    this.win.on('closed', () => {
      clearTimeout(this.timer)
      clearTimeout(this.slowTimer)
      this.win = null
      if (!this.closedByUs && this.onUserClose) this.onUserClose()
    })
    this.loaded = false
    wc.once('did-finish-load', () => {
      this.loaded = true
      this.send({ version: this.version })
      this.shown = -1
      if (this.initial) this.send({ text: this.initial.text, pct: this.initial.pct, hint: this.initial.hint || '' })
      else this.advance(true)
    })
    this.win.loadFile(path.join(__dirname, 'splash.html'))
    this.slowTimer = setTimeout(() => this.send({ hint: '首次启动需要初始化数据库，可能需要一两分钟，请耐心等待。', text: STAGES[Math.max(this.shown, 0)].text, pct: STAGES[Math.max(this.shown, 0)].pct }), SLOW_HINT_MS)
  }

  send(state) {
    if (this.win && !this.win.isDestroyed() && this.loaded) this.win.webContents.send('splash:state', state)
  }

  // Request a stage; stages never go backwards and each stays visible for at least MIN_STAGE_MS.
  setStage(i) {
    if (this.frozen) return
    if (this.errored) return
    if (i > this.target) this.target = Math.min(i, STAGES.length - 1)
    this.advance(false)
  }

  noteLine(line) {
    const i = stageForLine(line)
    if (i > 0) this.setStage(i)
  }

  advance(force) {
    clearTimeout(this.timer)
    if (this.errored || this.shown >= this.target && !force) return
    const wait = this.lastChange + MIN_STAGE_MS - Date.now()
    if (wait > 0 && !force) {
      this.timer = setTimeout(() => this.advance(false), wait)
      return
    }
    if (this.shown < this.target) this.shown += 1
    if (this.shown < 0) this.shown = 0
    this.lastChange = Date.now()
    const st = STAGES[this.shown]
    this.send({ text: st.text, pct: st.pct, hint: '' })
    if (this.shown < this.target) this.timer = setTimeout(() => this.advance(false), MIN_STAGE_MS)
  }

  showError(info) {
    this.errored = true
    clearTimeout(this.timer)
    clearTimeout(this.slowTimer)
    if (!this.win || this.win.isDestroyed()) {
      this.show()
      this.errored = true
    }
    this.win.setAlwaysOnTop(false)
    this.win.setSkipTaskbar(false)
    this.win.setTitle('OA 文件智能归档')
    const push = () => {
      this.send({ version: this.version })
      this.send({ error: { title: info.title, hint: info.hint } })
      if (!this.win.isVisible()) this.win.show()
    }
    if (!this.loaded) this.win.webContents.once('did-finish-load', push)
    else push()
  }

  // Finish the bar, then close.
  async close() {
    clearTimeout(this.timer)
    clearTimeout(this.slowTimer)
    if (!this.win || this.win.isDestroyed()) return
    this.send({ text: '完成', pct: 100, hint: '' })
    await new Promise((r) => setTimeout(r, 350))
    this.closedByUs = true
    if (this.win && !this.win.isDestroyed()) this.win.close()
  }

  // Free-form progress (folder migration): freezes the stage logic until the next reset().
  showProgress(text, pct, hint) {
    if (!this.win || this.win.isDestroyed()) this.show()
    this.frozen = true
    clearTimeout(this.timer)
    clearTimeout(this.slowTimer)
    this.errored = false
    this.win.setAlwaysOnTop(true)
    if (!this.win.isVisible()) this.win.show()
    if (this.loaded) this.send({ text, pct, hint: hint || '' })
    else this.initial = { text, pct, hint }
  }

  reset() {
    this.frozen = false
    if (!this.win || this.win.isDestroyed()) return this.show()
    this.errored = false
    this.initial = null
    this.shown = -1
    this.target = 0
    this.win.setAlwaysOnTop(true)
    this.win.setSkipTaskbar(true)
    this.win.show()
    this.slowTimer = setTimeout(() => this.send({ hint: '首次启动需要初始化数据库，可能需要一两分钟，请耐心等待。' }), SLOW_HINT_MS)
    this.advance(true)
  }

  hide() {
    if (this.win && !this.win.isDestroyed()) this.win.hide()
  }

  unhide() {
    if (this.win && !this.win.isDestroyed() && !this.errored) this.win.show()
  }

  isOpen() {
    return !!this.win && !this.win.isDestroyed()
  }

  setText(text, hint) {
    this.send({ text, hint: hint || '' })
  }
}

module.exports = { Splash, STAGES, stageForLine }
