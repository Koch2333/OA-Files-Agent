'use strict'
const fs = require('fs')
const path = require('path')
const os = require('os')
const { app, BrowserWindow, Menu, Tray, dialog, shell, session, screen, nativeImage, Notification, clipboard, ipcMain } = require('electron')
const { resolveDataDir, resolveServerDir, defaultArchiveRoot, isPortable, exeDir, readInstallPreset, writePointer, dataDirLocked } = require('./paths')
const { validateDir, copyVerify, switchWorkdir, isInside, programDirs, freeBytes } = require('./workdir')
const { createSettings } = require('./settings-ctl')
const { runSetup } = require('./setup-ctl')
const { Config } = require('./config')
const { createLogger } = require('./logger')
const { Backend } = require('./backend')
const { Splash } = require('./splash-ctl')

const PRODUCT = 'OA 文件智能归档'
app.setAppUserModelId('com.oaagent.desktop')

// Everything Chromium writes (cache, localStorage, cookies) goes under the data dir too.
const dataDir = resolveDataDir()
fs.mkdirSync(dataDir, { recursive: true })
app.setPath('userData', path.join(dataDir, 'electron'))
app.setPath('sessionData', path.join(dataDir, 'electron'))

if (!app.requestSingleInstanceLock()) {
  app.exit(0)
  return
}

const log = createLogger(path.join(dataDir, 'logs'))
const config = new Config(dataDir)
const serverDir = resolveServerDir()
const iconPath = path.join(__dirname, '..', 'build', 'icon.png')

let win = null
let tray = null
let backend = null
let quitting = false
let appOrigin = ''
let restarts = []
let starting = false
let splash = null

process.on('uncaughtException', (e) => log.error('uncaughtException: ' + (e && e.stack)))
process.on('unhandledRejection', (e) => log.error('unhandledRejection: ' + (e && e.stack ? e.stack : e)))

app.on('second-instance', () => {
  if (!win) return
  if (win.isMinimized()) win.restore()
  win.show()
  win.focus()
})

// ---------- working directory (archive folder) / data directory ----------
function validateCtx(kind) {
  const env = (k) => process.env[k] || ''
  const docs = (() => { try { return app.getPath('documents') } catch { return '' } })()
  // Portable build: the default archive folder is <exe dir>\归档, so only the program's own sub-folders are off limits.
  const prog = programDirs({ portable: isPortable(), exeDir: exeDir(), serverDir })
  return {
    kind,
    installDirs: prog.installDirs,
    dataDir,
    archiveRoot: config.get('archiveRoot'),
    protectedDirs: [env('SystemRoot'), env('ProgramFiles'), env('ProgramFiles(x86)')].filter(Boolean),
    exactProtected: [env('ProgramData'), env('USERPROFILE'), docs, path.join(env('USERPROFILE') || '', 'Desktop'), path.join(env('USERPROFILE') || '', 'Downloads'), ...prog.exact].filter(Boolean),
  }
}

// The saved working directory is used as is on every launch. If it is temporarily unusable (disk unplugged, network
// location offline, no permission) the user gets a retry / change / quit dialog; the setting itself is never cleared and
// the first-run folder dialog is not shown again.
async function ensureArchiveAvailable(root) {
  for (;;) {
    try {
      fs.mkdirSync(root, { recursive: true })
      fs.accessSync(root, fs.constants.W_OK)
      return root
    } catch (e) {
      log.warn(`archive root unavailable: ${root}: ${e.code || e.message}`)
      const r = await dialog.showMessageBox({
        type: 'warning', title: PRODUCT, message: '工作目录暂时无法使用',
        detail: `${root}\n（${e.code || e.message}）\n\n可能是磁盘未连接、网络位置已断开或没有写入权限。你的设置没有被改动。`,
        buttons: ['重试', '更改工作目录…', '退出'], defaultId: 0, cancelId: 2, noLink: true,
      })
      if (r.response === 0) continue
      if (r.response === 2) {
        quitting = true
        app.exit(0)
        return new Promise(() => {})
      }
      const pick = await runSetup({ mode: 'change', defaultArchive: root, currentArchive: root, currentData: dataDir, canData: false, ctxFor: validateCtx })
      if (pick) {
        config.set('archiveRoot', pick.archive)
        root = pick.archive
      }
    }
  }
}

async function resolveArchiveRoot() {
  if (process.env.OA_AGENT_ARCHIVE_ROOT) return ensureArchiveAvailable(path.resolve(process.env.OA_AGENT_ARCHIVE_ROOT))
  let root = config.get('archiveRoot')
  if (root) return ensureArchiveAvailable(root)
  const preset = readInstallPreset() // chosen on the installer's working-directory page (or /ARCHIVE=)
  if (preset && (await validateDir(preset, validateCtx('archive'))).ok) {
    config.set('archiveRoot', preset)
    log.info(`archive root from installer preset: ${preset}`)
    return preset
  }
  const hadData = fs.existsSync(path.join(dataDir, 'pg', 'PG_VERSION'))
  const r = await runSetup({
    mode: 'first',
    defaultArchive: defaultArchiveRoot(),
    currentArchive: '',
    currentData: dataDir,
    canData: !dataDirLocked(),
    ctxFor: validateCtx,
    note: hadData ? '检测到已有数据，但没有找到已保存的工作目录设置。请选择之前使用的归档文件夹（选择后会保存，以后不再询问）。' : '',
  })
  if (!r) {
    log.info('first-run folder dialog cancelled: quitting')
    quitting = true
    app.exit(0)
    return new Promise(() => {})
  }
  if (path.resolve(r.data).toLowerCase() !== path.resolve(dataDir).toLowerCase()) {
    // Different data directory chosen: seed its config, point the bootstrap file at it, and restart the app.
    fs.mkdirSync(r.data, { recursive: true })
    new Config(r.data).set('archiveRoot', r.archive)
    writePointer(r.data)
    log.info(`data dir set to ${r.data}; relaunching`)
    quitting = true
    app.relaunch()
    app.exit(0)
    return new Promise(() => {})
  }
  config.set('archiveRoot', r.archive)
  return ensureArchiveAvailable(r.archive)
}

const fmtBytes = (n) => (n >= 1024 ** 3 ? (n / 1024 ** 3).toFixed(2) + ' GB' : (n / 1024 ** 2).toFixed(1) + ' MB')

async function changeArchiveRoot() {
  if (starting || !backend) return
  const oldRoot = backend.archiveRoot
  if (process.env.OA_AGENT_ARCHIVE_ROOT) {
    dialog.showMessageBox(win || undefined, { type: 'info', message: '工作目录由环境变量 OA_AGENT_ARCHIVE_ROOT 指定，无法在界面中更改。' })
    return
  }
  const r = await runSetup({ mode: 'change', defaultArchive: oldRoot, currentArchive: oldRoot, currentData: dataDir, canData: false, ctxFor: validateCtx })
  if (!r) return
  const newRoot = r.archive
  if (path.resolve(newRoot).toLowerCase() === path.resolve(oldRoot).toLowerCase()) return
  // Copying a folder into itself would never end; merely switching to a nested folder is harmless.
  if (r.mode === 'migrate' && (isInside(newRoot, oldRoot) || isInside(oldRoot, newRoot))) {
    dialog.showMessageBox(win || undefined, { type: 'warning', message: '迁移时新旧文件夹不能互相包含。', detail: '请选择另一个位置，或改用“直接使用新位置”（不复制文件）。' })
    return
  }
  const nonEmpty = fs.existsSync(newRoot) && fs.readdirSync(newRoot).length > 0
  const detail =
    r.mode === 'migrate'
      ? `将把\n${oldRoot}\n中的归档文件复制到\n${newRoot}\n逐文件校验大小，全部成功后才切换；旧文件夹会保留。${nonEmpty ? '\n\n目标文件夹已有内容：会合并进去，已存在的同名同大小文件保持不动；若有同名但内容不同的文件会中止，绝不覆盖。' : ''}\n\n过程中后台服务会暂停，请勿关闭程序。`
      : `将直接使用\n${newRoot}\n不会复制任何文件。\n${nonEmpty ? '' : '\n警告：该文件夹是空的，台账中所有文件都会显示为“缺失”。\n'}\n台账中在新位置找不到的文件会显示为“缺失”。`
  const c = await dialog.showMessageBox(win || undefined, {
    type: nonEmpty || r.mode === 'migrate' ? 'question' : 'warning', title: PRODUCT, message: r.mode === 'migrate' ? '迁移工作目录' : '直接使用新位置', detail,
    buttons: [r.mode === 'migrate' ? '开始迁移' : '确认使用', '取消'], defaultId: 1, cancelId: 1, noLink: true,
  })
  if (c.response !== 0) return

  restarts = []
  let copied = null
  const res = await switchWorkdir({
    oldRoot,
    newRoot,
    mode: r.mode,
    stopBackend: async () => {
      splash.showProgress('正在停止服务…', 2, '')
      await backend.stop()
    },
    copy: async (from, to) => {
      copied = await copyVerify(from, to, {
        onProgress: (p) => splash.showProgress(`正在迁移归档文件（${p.files}/${p.totalFiles}）`, Math.max(3, Math.min(95, Math.round((p.bytes / Math.max(p.totalBytes, 1)) * 90) + 5)), `${fmtBytes(p.bytes)} / ${fmtBytes(p.totalBytes)}，逐文件校验大小`),
      })
    },
    setRoot: (p) => config.set('archiveRoot', p),
    startBackend: () => boot(),
  })
  Menu.setApplicationMenu(buildMenu())
  if (!res.ok) {
    log.error(`switch workdir failed at ${res.stage}: ${res.error && res.error.message}; restored=${res.restored}`)
    await dialog.showMessageBox(win || undefined, {
      type: 'error', title: PRODUCT, message: res.stage === 'copy' ? '迁移失败，仍在使用原目录' : '新目录启动失败，已恢复原目录',
      detail: `${res.error && res.error.message}\n\n当前工作目录：${config.get('archiveRoot')}\n${res.stage === 'copy' ? `新文件夹 ${newRoot} 中可能留有不完整的副本，可手动删除。\n` : ''}${res.restored ? '' : '原目录的服务也未能恢复，请查看日志。'}`,
    })
    return
  }
  log.info(`archive root switched ${oldRoot} -> ${newRoot} (${r.mode})`)
  if (r.mode === 'migrate' && copied) {
    const d = await dialog.showMessageBox(win || undefined, {
      type: 'question', title: PRODUCT, message: '迁移完成', detail: `已复制并校验 ${copied.files} 个文件（${fmtBytes(copied.bytes)}）。\n现在使用：${newRoot}\n\n是否删除旧文件夹？\n${oldRoot}\n（默认保留；删除后无法恢复）`,
      buttons: ['保留旧文件夹', '删除旧文件夹'], defaultId: 0, cancelId: 0, noLink: true,
    })
    if (d.response === 1) {
      try {
        await fs.promises.rm(oldRoot, { recursive: true, force: true })
        log.info(`old archive root deleted: ${oldRoot}`)
      } catch (e) {
        dialog.showMessageBox(win || undefined, { type: 'warning', message: `旧文件夹未能完全删除：${e.message}` })
      }
    }
  } else {
    dialog.showMessageBox(win || undefined, { type: 'info', title: PRODUCT, message: `工作目录已切换为\n${newRoot}` })
  }
}

async function migrateDataDir() {
  if (starting || !backend) return
  if (dataDirLocked()) {
    dialog.showMessageBox(win || undefined, { type: 'info', message: '数据目录由环境变量 OA_AGENT_DATA_DIR 或便携模式固定，无法在界面中迁移。' })
    return
  }
  const pick = await dialog.showOpenDialog(win || undefined, { title: '选择新的数据目录（需为空文件夹）', defaultPath: path.dirname(dataDir), properties: ['openDirectory', 'createDirectory'] })
  if (pick.canceled || !pick.filePaths[0]) return
  const target = pick.filePaths[0]
  const v = await validateDir(target, validateCtx('data'))
  const empty = !fs.existsSync(target) || fs.readdirSync(target).length === 0
  if (!v.ok || !empty || isInside(target, dataDir) || isInside(dataDir, target)) {
    dialog.showMessageBox(win || undefined, { type: 'warning', message: '无法使用该文件夹', detail: [...v.errors, empty ? '' : '文件夹必须为空。', isInside(target, dataDir) || isInside(dataDir, target) ? '新旧数据目录不能互相包含。' : ''].filter(Boolean).join('\n') })
    return
  }
  const c = await dialog.showMessageBox(win || undefined, {
    type: 'question', title: PRODUCT, message: '迁移数据目录',
    detail: `将把数据库、设置与日志从\n${dataDir}\n复制到\n${v.resolved}\n\n先安全停止内置数据库，逐文件校验后才切换并自动重启程序；旧目录会保留，可确认无误后手动删除。${v.warnings.length ? '\n\n注意：' + v.warnings.join('；') : ''}`,
    buttons: ['开始迁移', '取消'], defaultId: 1, cancelId: 1, noLink: true,
  })
  if (c.response !== 0) return
  splash.showProgress('正在停止服务…', 2, '')
  await backend.stop() // PostgreSQL must be stopped cleanly before its files are copied
  try {
    const r = await copyVerify(dataDir, v.resolved, {
      exclude: (rel) => rel.split(path.sep)[0].toLowerCase() === 'electron',
      onProgress: (p) => splash.showProgress(`正在迁移数据目录（${p.files}/${p.totalFiles}）`, Math.max(3, Math.min(95, Math.round((p.bytes / Math.max(p.totalBytes, 1)) * 90) + 5)), `${fmtBytes(p.bytes)} / ${fmtBytes(p.totalBytes)}`),
    })
    writePointer(v.resolved)
    log.info(`data dir migrated ${dataDir} -> ${v.resolved} (${r.files} files); relaunching`)
    await dialog.showMessageBox(win || undefined, { type: 'info', title: PRODUCT, message: '数据目录迁移完成', detail: `已校验 ${r.files} 个文件。程序将重新启动并使用新位置。\n旧目录已保留：${dataDir}` })
    quitting = true
    app.relaunch()
    app.exit(0)
  } catch (e) {
    log.error('data dir migration failed: ' + e.message)
    await dialog.showMessageBox(win || undefined, { type: 'error', title: PRODUCT, message: '数据目录迁移失败，仍使用原目录', detail: `${e.message}\n新文件夹中可能留有不完整的副本，可手动删除。` })
    await boot()
  }
}

// ---------- window ----------
function saneBounds(b) {
  if (!b || !b.width || !b.height) return null
  const ok = screen.getAllDisplays().some((d) => {
    const a = d.workArea
    return b.x + 80 < a.x + a.width && b.x + b.width - 80 > a.x && b.y >= a.y - 10 && b.y < a.y + a.height - 40
  })
  return ok ? b : { width: b.width, height: b.height }
}

function isInternalUrl(u) {
  try {
    return new URL(u).origin === appOrigin
  } catch {
    return false
  }
}

function openExternalSafe(u) {
  try {
    const p = new URL(u)
    if (p.protocol === 'http:' || p.protocol === 'https:') shell.openExternal(p.href)
  } catch {
    /* ignore */
  }
}

function handleAction(url) {
  switch (url.replace('oa-action:', '')) {
    case 'retry':
      restarts = []
      backend && (backend.preferredPort = 0)
      boot()
      break
    case 'logs':
      openLogs()
      break
    case 'data':
      shell.openPath(dataDir)
      break
    case 'quit':
      app.quit()
      break
    case 'settings':
      settings.show()
      break
  }
}

// ---------- settings window + the small window.oaDesktop bridge of the web UI ----------
async function settingsState() {
  const root = (backend && backend.archiveRoot) || config.get('archiveRoot', '') || ''
  const state = {
    version: app.getVersion(),
    portable: isPortable(),
    dataDir,
    dataLocked: dataDirLocked(),
    archiveRoot: root,
    archiveLocked: !!process.env.OA_AGENT_ARCHIVE_ROOT,
    archiveFree: root ? freeBytes(root) : null,
    autoStart: false,
    minimizeToTray: !!config.get('minimizeToTray', false),
    fixedPort: Number(config.get('fixedPort', 0)) || 0,
    port: backend ? backend.port : 0,
    backendVersion: (backend && backend.version) || '',
    logsDir: log.dir,
  }
  try {
    state.autoStart = !!app.getLoginItemSettings().openAtLogin
  } catch {
    /* not supported on this platform */
  }
  return state
}

function diagnosticsText() {
  return [
    `${PRODUCT} ${app.getVersion()}${isPortable() ? '（便携版）' : ''}`,
    backend && backend.version ? `后端：${backend.version}` : '',
    `系统：${os.type()} ${os.release()} ${os.arch()}`,
    `Electron ${process.versions.electron} / Chromium ${process.versions.chrome} / Node ${process.versions.node}`,
    `数据目录：${dataDir}`,
    `工作目录：${(backend && backend.archiveRoot) || config.get('archiveRoot', '')}`,
    `本机服务端口：${backend ? backend.port : '未运行'}`,
    `日志：${log.dir}`,
  ].filter(Boolean).join('\n')
}

const settings = createSettings({
  parent: () => win,
  getState: settingsState,
  changeArchive: async () => {
    await changeArchiveRoot()
    settings.refresh()
  },
  migrateData: migrateDataDir,
  open: (what) => {
    if (what === 'archive' && backend) return shell.openPath(backend.archiveRoot)
    if (what === 'data') return shell.openPath(dataDir)
    if (what === 'logs') return openLogs()
    throw new Error('无法打开。')
  },
  setOption: async (key, value) => {
    if (key === 'autoStart') {
      app.setLoginItemSettings({ openAtLogin: value, path: process.execPath })
      config.set('autoStart', value)
    } else if (key === 'minimizeToTray') {
      config.set('minimizeToTray', value)
    } else if (key === 'fixedPort') {
      config.set('fixedPort', value)
      if (backend && !starting) {
        restarts = []
        boot().then(() => settings.refresh())
      }
    }
  },
  copyDiagnostics: () => clipboard.writeText(diagnosticsText()),
})

// window.oaDesktop (app-preload.js): only the app window on the app origin may call these.
const fromApp = (e) => !!win && !win.isDestroyed() && e.sender === win.webContents && isInternalUrl(e.senderFrame ? e.senderFrame.url : '')
ipcMain.handle('desktop:info', (e) => {
  if (!fromApp(e)) return null
  return { version: app.getVersion(), archiveRoot: (backend && backend.archiveRoot) || config.get('archiveRoot', ''), dataDir, portable: isPortable() }
})
ipcMain.handle('desktop:open-settings', (e) => {
  if (fromApp(e)) settings.show()
})
ipcMain.handle('desktop:open-folder', (e, what) => {
  if (!fromApp(e)) return
  if (what === 'archive' && backend) shell.openPath(backend.archiveRoot)
  else if (what === 'logs') openLogs()
})

function createWindow() {
  const saved = saneBounds(config.get('bounds'))
  win = new BrowserWindow({
    width: 1280,
    height: 820,
    minWidth: 960,
    minHeight: 600,
    ...(saved || {}),
    show: false,
    title: PRODUCT,
    icon: fs.existsSync(iconPath) ? iconPath : undefined,
    backgroundColor: '#f6f7fb',
    autoHideMenuBar: true,
    webPreferences: {
      // The preload only exposes a few fixed, origin-checked IPC calls (window.oaDesktop): open the settings window, read the
      // working directory, open folders. No Node, no file access from the page.
      preload: path.join(__dirname, 'app-preload.js'),
      contextIsolation: true, nodeIntegration: false, sandbox: true, webSecurity: true, spellcheck: false, devTools: !app.isPackaged || process.argv.includes('--devtools'),
    },
  })
  win.removeMenu()
  if (config.get('maximized')) win.maximize()

  const wc = win.webContents
  wc.setZoomLevel(Number(config.get('zoom', 0)) || 0)
  wc.on('will-navigate', (e, url) => {
    if (url.startsWith('oa-action:')) {
      e.preventDefault()
      handleAction(url)
    } else if (!isInternalUrl(url)) {
      e.preventDefault()
      openExternalSafe(url)
    }
  })
  wc.setWindowOpenHandler(({ url }) => {
    if (isInternalUrl(url)) return { action: 'deny' } // the UI is single-window
    openExternalSafe(url)
    return { action: 'deny' }
  })
  wc.on('will-attach-webview', (e) => e.preventDefault())
  wc.on('render-process-gone', (_e, d) => {
    log.error('renderer gone: ' + JSON.stringify(d))
    if (!quitting && appOrigin) wc.loadURL(appOrigin)
  })
  // Keyboard shortcuts (there is no native menu bar any more).
  wc.on('before-input-event', (e, input) => {
    if (input.type !== 'keyDown') return
    const k = String(input.key || '').toLowerCase()
    const ctrl = input.control || input.meta
    const act = (fn) => {
      e.preventDefault()
      fn()
    }
    if (k === 'f5' || (ctrl && k === 'r')) act(() => wc.reload())
    else if (ctrl && (k === '=' || k === '+')) act(() => zoomBy(0.5))
    else if (ctrl && k === '-') act(() => zoomBy(-0.5))
    else if (ctrl && k === '0') act(() => zoomBy(0))
    else if (ctrl && k === ',') act(() => settings.show())
    else if (k === 'f11') act(() => win.setFullScreen(!win.isFullScreen()))
  })

  let t
  const persist = () => {
    clearTimeout(t)
    t = setTimeout(() => {
      if (!win || win.isDestroyed()) return
      config.set('maximized', win.isMaximized())
      if (!win.isMaximized() && !win.isMinimized() && !win.isFullScreen()) config.set('bounds', win.getBounds())
    }, 400)
  }
  win.on('resize', persist)
  win.on('move', persist)
  win.on('close', (e) => {
    if (!quitting && config.get('minimizeToTray', false) && tray) {
      e.preventDefault()
      win.hide()
    }
  })
  win.on('closed', () => {
    win = null
    if (!quitting) app.quit()
  })
}


function openLogs() {
  shell.openPath(log.dir)
}

// ---------- session hardening ----------
function hardenSession(ses) {
  const trusted = (wcUrl) => isInternalUrl(wcUrl)
  const allowed = new Set(['clipboard-sanitized-write', 'notifications'])
  ses.setPermissionRequestHandler((wc, permission, cb) => cb(allowed.has(permission) && trusted(wc.getURL())))
  ses.setPermissionCheckHandler((wc, permission, origin) => allowed.has(permission) && trusted(origin || (wc && wc.getURL())))
  ses.webRequest.onHeadersReceived((details, cb) => {
    const headers = details.responseHeaders || {}
    if (details.resourceType === 'mainFrame' || details.resourceType === 'subFrame') {
      if (isInternalUrl(details.url) && !Object.keys(headers).some((k) => k.toLowerCase() === 'content-security-policy')) {
        headers['Content-Security-Policy'] = [
          "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:; " +
            "connect-src 'self'; frame-src 'self' blob:; object-src 'self' blob:; media-src 'self' blob:; worker-src 'self' blob:; " +
            "base-uri 'self'; form-action 'self'; frame-ancestors 'self'",
        ]
      }
    }
    cb({ responseHeaders: headers })
  })
  ses.on('will-download', (_e, item) => {
    // No savePath set: Electron shows the standard save dialog (Excel export etc.).
    item.once('done', (_ev, state) => {
      if (state === 'completed') {
        const p = item.getSavePath()
        log.info(`download saved: ${p}`)
        if (Notification.isSupported()) {
          const n = new Notification({ title: PRODUCT, body: `已保存：${path.basename(p)}（点击打开所在文件夹）` })
          n.on('click', () => shell.showItemInFolder(p))
          n.show()
        }
      } else if (state === 'interrupted') {
        log.warn('download interrupted')
      }
    })
  })
}

// ---------- menu / tray ----------
// No native menu bar: the window shows only the web UI. Everything that used to live in the File/View/Window menus is
// reachable inside the app (user menu → 桌面版设置, Settings page, tray menu) or by keyboard (see before-input-event).
function buildMenu() {
  return null
}

function zoomBy(delta) {
  if (!win) return
  const wc = win.webContents
  const z = delta === 0 ? 0 : Math.max(-3, Math.min(5, wc.getZoomLevel() + delta))
  wc.setZoomLevel(z)
  config.set('zoom', z)
}

const shortPath = (p) => (p.length > 48 ? p.slice(0, 20) + '…' + p.slice(-26) : p)

function showAbout() {
  const lines = [
    `${PRODUCT}`,
    `版本：${app.getVersion()}${isPortable() ? '（便携版）' : ''}`,
    backend && backend.version ? `后端：${backend.version}` : '',
    `数据文件夹：${dataDir}`,
    backend ? `工作目录（归档文件夹）：${backend.archiveRoot}` : '',
    `日志：${log.dir}`,
    `Electron ${process.versions.electron} / Chromium ${process.versions.chrome}`,
  ].filter(Boolean)
  dialog.showMessageBox(win || undefined, { type: 'info', title: `关于 ${PRODUCT}`, message: PRODUCT, detail: lines.slice(1).join('\n'), buttons: ['确定'] })
}

function createTray() {
  if (!fs.existsSync(iconPath)) return
  tray = new Tray(nativeImage.createFromPath(iconPath).resize({ width: 16, height: 16 }))
  tray.setToolTip(PRODUCT)
  const show = () => {
    if (!win) return
    win.show()
    win.focus()
  }
  tray.setContextMenu(Menu.buildFromTemplate([{ label: '显示窗口', click: show }, { label: '设置…', click: () => settings.show() }, { label: '更改工作目录…', click: changeArchiveRoot }, { label: '打开归档文件夹', click: () => backend && shell.openPath(backend.archiveRoot) }, { label: '查看日志', click: openLogs }, { type: 'separator' }, { label: '退出', click: () => app.quit() }]))
  tray.on('click', show)
}

// ---------- backend lifecycle ----------
async function boot() {
  if (starting) return false
  starting = true
  try {
    if (splash.isOpen()) splash.reset()
    else splash.show()
    splash.setStage(0)
    if (backend) await backend.stop()
    splash.hide()
    const archiveRoot = await resolveArchiveRoot() // may show the first-run folder dialog
    splash.unhide()
    const fixedPort = Number(config.get('fixedPort', 0)) || 0 // chosen in the settings window; 0 = automatic (reuse the last port)
    const preferred = fixedPort || (backend ? backend.preferredPort : Number(config.get('port', 0)) || 0)
    backend = new Backend({
      serverDir,
      dataDir,
      archiveRoot,
      logger: log,
      preferredPort: preferred,
      startTimeoutMs: Number(process.env.OA_AGENT_START_TIMEOUT_MS) || 180000,
    })
    backend.on('crashed', onCrashed)
    backend.on('line', (l) => splash.noteLine(l))
    try {
      await backend.start()
      splash.setStage(4)
      const cookies = await backend.ensureLocalSetup()
      await installCookies(backend.origin, cookies)
    } catch (e) {
      const info = e.info || { title: '初始化失败', hint: '无法完成本机初始化。请查看日志后重试。', detail: String(e && e.message) }
      log.error(`startup failed: ${info.title}: ${(info.detail || '').slice(-500)}`)
      if (backend) await backend.stop()
      splash.showError(info)
      return false
    }
    appOrigin = backend.origin
    config.set('port', backend.port)
    splash.setStage(5)
    log.info(`UI ready at ${appOrigin}`)
    try {
      await loadUi(appOrigin)
    } catch (e) {
      log.error('UI load failed: ' + e.message)
      splash.showError({ title: '界面加载失败', hint: '后台服务已启动，但界面无法加载。请查看日志后重试。' })
      return false
    }
    win.setTitle(PRODUCT)
    settings.refresh()
    if (!win.isVisible()) win.show()
    win.focus()
    await splash.close() // hand-off: only after the UI has finished loading in the (hidden) main window
    return true
  } finally {
    starting = false
  }
}

function loadUi(url) {
  return new Promise((resolve, reject) => {
    const wc = win.webContents
    const ok = () => {
      wc.removeListener('did-fail-load', fail)
      resolve()
    }
    const fail = (_e, code, desc, _u, isMain) => {
      if (!isMain || code === -3) return
      wc.removeListener('did-finish-load', ok)
      reject(new Error(`${desc} (${code})`))
    }
    wc.once('did-finish-load', ok)
    wc.once('did-fail-load', fail)
    wc.loadURL(url).catch(() => {})
  })
}

async function installCookies(origin, setCookies) {
  for (const c of setCookies) {
    try {
      const [pair, ...attrs] = c.split(';').map((s) => s.trim())
      const eq = pair.indexOf('=')
      const cookie = { url: origin, name: pair.slice(0, eq), value: pair.slice(eq + 1), httpOnly: attrs.some((a) => /^httponly$/i.test(a)) }
      const ma = attrs.find((a) => /^max-age=/i.test(a))
      if (ma) cookie.expirationDate = Date.now() / 1000 + parseInt(ma.split('=')[1], 10)
      await session.defaultSession.cookies.set(cookie)
    } catch (e) {
      log.warn('cookie install failed: ' + e.message)
    }
  }
}

async function onCrashed({ code, signal }) {
  if (quitting) return
  const now = Date.now()
  restarts = restarts.filter((t) => now - t < 5 * 60 * 1000)
  restarts.push(now)
  log.error(`backend crashed (code=${code} signal=${signal}); restart #${restarts.length}`)
  if (restarts.length > 3) {
    splash.show()
    splash.showError({ title: '后台服务反复停止', hint: '后台服务在短时间内多次意外停止，已不再自动重启。请查看日志。' })
    return
  }
  await boot()
  if (Notification.isSupported() && appOrigin) new Notification({ title: PRODUCT, body: '后台服务意外停止，已自动重启。' }).show()
}

app.on('before-quit', (e) => {
  if (quitting) return
  quitting = true
  if (!backend) return
  e.preventDefault()
  if (win && !win.isDestroyed()) win.hide()
  if (splash.isOpen()) splash.reset()
  splash.closedByUs = true
  splash.show({ text: '正在安全停止服务…', pct: 100, hint: '正在保存并关闭本地数据库，请稍候。' })
  const hard = setTimeout(() => app.exit(0), 120000)
  backend.stop().finally(() => {
    clearTimeout(hard)
    log.info('shutdown complete')
    app.exit(0)
  })
})

app.whenReady().then(async () => {
  // The splash comes first: it must be on screen within ~300 ms, before anything heavy happens.
  splash = new Splash({ version: app.getVersion(), onAction: handleAction })
  splash.onUserClose = () => app.quit()
  splash.show()
  log.info(`${PRODUCT} ${app.getVersion()} starting; data=${dataDir} server=${serverDir} portable=${isPortable()}`)
  hardenSession(session.defaultSession)
  Menu.setApplicationMenu(buildMenu())
  createWindow()
  createTray()
  await boot()
})

app.on('window-all-closed', () => {
  if (!quitting) app.quit()
})
