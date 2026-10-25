'use strict'
// The in-app key window ("密钥窗口") of the desktop edition. Every API-key action happens here, never in the system browser:
//   view 'guide'  create a key at a paste-only provider: the provider's real key page on the left (own sandboxed view),
//                 an app-rendered step-by-step panel on the right;
//   view 'login'  "sign in with the platform" (OpenRouter PKCE): the platform's authorize page in the same kind of view;
//                 the loopback callback is recognised, the window closes and the settings page shows the new key;
//   view 'manage' an existing key: provider, key name, hint, last test; test again / replace / switch provider / remove.
// The app
//   * never reads, scripts, captures or automates the provider page (no preload, no script injection, no cookie access),
//   * only polls the clipboard TEXT (main process, 'guide' view, while this window has focus) for something that has the
//     provider's key format, shows it masked, and hands the full key to the settings form only after the user confirms.
// Pure logic (allow-list, key detection, steps, manage state, sender checks) lives in keyhelper-core.js and is unit-tested.
const path = require('path')
const { pathToFileURL } = require('url')
const { BrowserWindow, WebContentsView, ipcMain, session, clipboard } = require('electron')
const core = require('./keyhelper-core')

const PANEL_FILE = path.join(__dirname, 'keyhelper.html')
const PANEL_URL = pathToFileURL(PANEL_FILE).href
const PANEL_WIDTH = 340
const PANEL_COLLAPSED = 48
const POLL_MS = 700
const ASK_TIMEOUT_MS = 60000
const MODES = { create: 'guide', login: 'login', manage: 'manage' }

/**
 * deps: { appWindow(): BrowserWindow|null, appOrigin(): string, fromApp(e): boolean, fetchPresets(): Promise<object[]>,
 *         testHosts: Set<string>, log, openExternal(url): boolean }
 * Returns { show(providerId, opts), isOpen(), close() }.
 */
function createKeyHelper(deps) {
  const testHosts = deps.testHosts || new Set()
  let win = null
  let pageView = null
  let panelView = null
  let plan = null // null in the manage view
  let view = 'guide'
  let providerId = ''
  let authorizeUrl = '' // login view
  let callbackSeen = false
  let ses = null
  let timer = null
  let lastText = null
  let pending = null // { key, raw } — the full key lives only here (main process memory) until the user confirms
  let ignoredKey = null
  let delivered = false
  let steps = core.initialSteps(1)
  let manage = core.initialManage()
  let ui = { url: { host: '', path: '', secure: true }, loading: false, blocked: null, error: null, notice: '', collapsed: false, canBack: false }
  let closeTimer = null
  const waiting = new Map()
  let rid = 0

  const fromPanel = (e) => !!panelView && core.senderIs(e, panelView.webContents, (u) => u === PANEL_URL)
  const appOrigin = () => deps.appOrigin() || ''

  function snapshot() {
    const stepList = plan ? (view === 'login' ? plan.guide.loginSteps : plan.guide.steps) : []
    return {
      view,
      provider: (plan && plan.name) || (manage.info && manage.info.provider) || '',
      suggestedName: plan ? plan.guide.suggestedName : 'OA Agent',
      loginNote: plan ? plan.guide.loginNote : '',
      costNote: plan ? plan.guide.costNote : '',
      steps: stepList,
      step: steps.step,
      detected: pending ? { masked: core.maskKey(pending.key) } : null, // never the full key
      url: ui.url,
      loading: ui.loading,
      blocked: ui.blocked,
      error: ui.error,
      notice: ui.notice,
      collapsed: ui.collapsed,
      canBack: ui.canBack,
      callbackSeen,
      manage: view === 'manage' ? { loading: manage.loading, busy: manage.busy, confirm: manage.confirm, info: manage.info, message: manage.message, ok: manage.ok } : null,
      testMode: testHosts.size > 0,
    }
  }
  const push = () => {
    if (panelView && !panelView.webContents.isDestroyed()) panelView.webContents.send('keyhelper:state', snapshot())
  }
  const dispatch = (e) => {
    steps = core.stepReducer(steps, e)
    push()
  }
  let noticeTimer = null
  function notify(text) {
    ui.notice = text
    push()
    clearTimeout(noticeTimer)
    noticeTimer = setTimeout(() => { ui.notice = ''; push() }, 6000) // notices are short-lived
  }
  const sendApp = (payload) => {
    const app = deps.appWindow()
    if (app && !app.isDestroyed()) app.webContents.send('keyhelper:event', payload)
  }

  function layout() {
    if (!win || win.isDestroyed() || !panelView) return
    const [w, h] = win.getContentSize()
    if (view === 'manage' || !pageView) {
      if (pageView) pageView.setVisible(false)
      panelView.setBounds({ x: 0, y: 0, width: w, height: h })
      return
    }
    pageView.setVisible(true)
    const pw = ui.collapsed ? PANEL_COLLAPSED : Math.min(PANEL_WIDTH, Math.max(260, Math.floor(w * 0.45)))
    pageView.setBounds({ x: 0, y: 0, width: Math.max(0, w - pw), height: h })
    panelView.setBounds({ x: Math.max(0, w - pw), y: 0, width: Math.min(pw, w), height: h })
  }

  // ---------- the provider page: sandboxed, locked to the allow-list ----------
  /** The one plain-http URL the provider view may load besides the loopback test host: the app's own OAuth callback. */
  const isCallback = (u) => view === 'login' && core.isOauthCallback(u, appOrigin())
  const hostOf = (u) => { try { return new URL(u).host } catch { return '' } }

  function hardenProviderSession(s) {
    s.setPermissionRequestHandler((_wc, permission, cb) => cb(permission === 'clipboard-sanitized-write')) // the site's own "copy" button
    s.setPermissionCheckHandler((_wc, permission) => permission === 'clipboard-sanitized-write')
    s.setDevicePermissionHandler(() => false)
    s.removeAllListeners('will-download')
    s.on('will-download', (e) => {
      e.preventDefault() // no downloads from this window
      notify('这个窗口不允许下载文件。')
    })
    // https only for every request (also sub-resources); plain http is only the loopback test host and the app's own callback.
    s.webRequest.onBeforeRequest((d, cb) => {
      try {
        const u = new URL(d.url)
        const ok = ['https:', 'wss:', 'data:', 'blob:', 'devtools:', 'chrome-extension:'].includes(u.protocol) ||
          (testHosts.has(u.host.toLowerCase()) && (u.protocol === 'http:' || u.protocol === 'ws:')) || isCallback(d.url)
        cb({ cancel: !ok })
      } catch {
        cb({ cancel: true })
      }
    })
    // No handler for certificate problems is installed anywhere: an untrusted certificate is a failed load, never a bypass.
  }

  function wirePage(wc, myPlan) {
    const guardMain = (e, url, isMain) => {
      if (isMain === false) {
        // frames inside the page (captcha, login widgets): https only; the site chooses them, we do not load anything of our own
        let ok = false
        try {
          const u = new URL(url)
          ok = u.protocol === 'https:' || u.protocol === 'about:' || (testHosts.size > 0 && testHosts.has(u.host.toLowerCase()))
        } catch {
          ok = false
        }
        if (!ok) e.preventDefault()
        return
      }
      if (isCallback(url)) {
        callbackSeen = true // the platform sent the user back to the app's loopback callback: the sign-in is finished on its side
        return
      }
      if (!core.checkNavUrl(url, myPlan.hosts, testHosts)) {
        e.preventDefault()
        ui.blocked = { host: hostOf(url) || '（未知）', from: wc.getURL() }
        push()
      }
    }
    wc.on('will-frame-navigate', (e) => guardMain(e, e.url, e.isMainFrame))
    wc.on('will-redirect', (e) => guardMain(e, e.url, e.isMainFrame))
    wc.setWindowOpenHandler(({ url }) => {
      const safe = core.checkNavUrl(url, myPlan.hosts, testHosts)
      if (safe) setImmediate(() => { if (!wc.isDestroyed()) wc.loadURL(safe).catch(() => {}) })
      else if (!deps.openExternal(url)) {
        // not on this provider's list: only the shell's own external allow-list may open it (never silently)
        ui.blocked = { host: hostOf(url) || '（未知）', from: wc.getURL() }
        push()
      }
      return { action: 'deny' }
    })
    wc.on('will-attach-webview', (e) => e.preventDefault())
    const syncNav = () => {
      if (wc.isDestroyed()) return
      ui.url = core.describeUrl(wc.getURL())
      ui.canBack = wc.navigationHistory.canGoBack()
      if (ui.blocked && wc.getURL() !== ui.blocked.from) ui.blocked = null // the page moved on by itself; a blocked attempt stays visible while the page stays put
      if (view === 'guide') {
        try {
          if (myPlan.keyPathHint && new URL(wc.getURL()).pathname.includes(myPlan.keyPathHint)) steps = core.stepReducer(steps, { type: 'onKeyPage' })
        } catch {
          /* ignore */
        }
      }
      if (callbackSeen && !closeTimer && view === 'login') finishLogin()
      push()
    }
    wc.on('did-navigate', syncNav)
    wc.on('did-navigate-in-page', syncNav)
    wc.on('did-start-loading', () => { ui.loading = true; ui.error = null; push() })
    wc.on('did-stop-loading', () => { ui.loading = false; syncNav() })
    wc.on('did-fail-load', (_e, code, desc, _u, isMain) => {
      if (!isMain || code === -3) return // -3 = aborted by a newer navigation
      ui.loading = false
      ui.error = code <= -200 && code > -300 ? '这个网站的安全证书不被信任，已停止打开（为了安全，不能跳过）。' : `页面没有打开（${desc}）。请检查网络，或点“刷新”重试。`
      push()
    })
    wc.on('render-process-gone', () => { ui.error = '网页进程意外退出，请点“刷新”。'; push() })
  }

  /** Login view, callback page loaded: tell the settings page (its status polling finishes the connection) and close shortly. */
  function finishLogin() {
    delivered = true
    steps = core.stepReducer(steps, { type: 'goto', step: steps.total - 1 })
    sendApp({ type: 'oauth-callback', provider: providerId })
    closeTimer = setTimeout(() => { closeTimer = null; if (win && !win.isDestroyed()) win.close() }, 1800)
  }

  // ---------- clipboard: text only, main process, 'guide' view only, only while this window is focused ----------
  // Electron 45's main-process clipboard is asynchronous (readText() returns a Promise); older versions return the string.
  async function readClip() {
    try {
      const t = await clipboard.readText()
      return typeof t === 'string' ? t : null
    } catch {
      return null
    }
  }
  let ticking = false
  async function tick() {
    if (ticking || !win || win.isDestroyed() || !win.isFocused() || delivered || view !== 'guide' || !plan) return
    ticking = true
    let text
    try {
      text = await readClip()
    } finally {
      ticking = false
    }
    if (text === null || !win || win.isDestroyed() || view !== 'guide' || !plan || delivered) return
    if (lastText === null) { lastText = text; return } // what is already on the clipboard when the window opens is not offered
    if (text === lastText) return
    lastText = text
    const d = core.detectKey(text, plan.pattern)
    if (d && d.key !== ignoredKey) {
      pending = { key: d.key, raw: text }
      dispatch({ type: 'detected' })
    } else if (pending) {
      pending = null // the clipboard no longer holds what the panel was showing
      dispatch({ type: 'cleared' })
    }
  }
  function stopPolling() {
    if (timer) clearInterval(timer)
    timer = null
  }

  // ---------- asking the settings page (it holds the session and the CSRF token; the main process does not) ----------
  function askApp(op) {
    return new Promise((resolve) => {
      const app = deps.appWindow()
      if (!app || app.isDestroyed()) return resolve({ ok: false, message: '找不到主窗口。' })
      const id = ++rid
      const t = setTimeout(() => { waiting.delete(id); resolve({ ok: false, message: '主窗口没有响应：请回到“设置”页面再试。' }) }, ASK_TIMEOUT_MS)
      waiting.set(id, (r) => { clearTimeout(t); resolve(r) })
      app.webContents.send('keyhelper:event', { type: 'request', id, op })
    })
  }
  ipcMain.handle('desktop:keyhelper-reply', (e, id, data) => {
    if (!deps.fromApp(e) || !Number.isInteger(id)) return false
    const cb = waiting.get(id)
    if (!cb) return false
    waiting.delete(id)
    cb(data)
    return true
  })

  async function loadManage() {
    manage = core.initialManage()
    push()
    const r = await askApp('status')
    if (view !== 'manage') return
    const info = core.sanitizeInfo(r)
    manage = info ? core.manageReducer(manage, { type: 'loaded', info }) : core.manageReducer(manage, { type: 'failed', message: (r && r.message) || '读取不到当前密钥的信息。' })
    push()
  }

  async function manageAct(name) {
    const apply = (e) => { manage = core.manageReducer(manage, e); push() }
    switch (name) {
      case 'manage-test': {
        apply({ type: 'testStart' })
        if (manage.busy !== 'test') return
        const r = core.sanitizeResult(await askApp('test'))
        if (view !== 'manage') return
        apply({ type: 'testDone', test: r.test || { ok: r.ok, message: r.message, at: Date.now() } })
        return
      }
      case 'manage-remove-ask': return apply({ type: 'removeAsk' })
      case 'manage-remove-cancel': return apply({ type: 'removeCancel' })
      case 'manage-remove': {
        apply({ type: 'removeStart' })
        if (manage.busy !== 'remove') return // no confirmation step before: nothing happens
        const r = core.sanitizeResult(await askApp('remove'))
        if (view !== 'manage') return
        if (r.ok) apply({ type: 'removeDone', message: r.message || '已删除密钥。' })
        else apply({ type: 'failed', message: r.message || '没能删除密钥。' })
        return
      }
      case 'manage-switch':
        sendApp({ type: 'switch-provider' })
        return win.close()
      case 'manage-replace':
        return replaceKey()
    }
  }

  /** "Replace key": paste-only providers continue in this window with the guide; the login provider hands over to the settings page. */
  async function replaceKey() {
    const pid = (manage.info && manage.info.presetId) || providerId
    try {
      const presets = await deps.fetchPresets()
      const preset = Array.isArray(presets) ? presets.find((p) => p && p.id === pid) : null
      if (preset && preset.oauth_login === true) {
        sendApp({ type: 'replace', provider: pid }) // the settings page starts the sign-in (it holds the CSRF token) and re-opens this window in login view
        return
      }
      const next = core.buildPlan(preset, testHosts)
      sendApp({ type: 'opened', provider: pid })
      openPlan(next, 'guide', '')
    } catch (err) {
      deps.log.warn('key window: replace failed: ' + ((err && err.message) || err))
      manage = core.manageReducer(manage, { type: 'failed', message: '这个服务商没有可用的向导，请回到设置页面粘贴密钥。' })
      push()
    }
  }

  // ---------- panel actions (validated sender + whitelisted action names) ----------
  function act(name, arg) {
    if (!win || win.isDestroyed()) return
    if (name.startsWith('manage-')) return void manageAct(name)
    switch (name) {
      case 'next': return dispatch({ type: 'next' })
      case 'prev': return dispatch({ type: 'prev' })
      case 'toggle': ui.collapsed = !ui.collapsed; layout(); return push()
      case 'ignore':
        if (pending) ignoredKey = pending.key
        pending = null
        return dispatch({ type: 'cleared' })
      case 'reload': {
        ui.error = null
        ui.blocked = null
        const target = view === 'login' ? authorizeUrl : plan && plan.startUrl
        if (pageView && target) pageView.webContents.loadURL(target).catch(() => {})
        return
      }
      case 'back': if (pageView && pageView.webContents.navigationHistory.canGoBack()) pageView.webContents.navigationHistory.goBack(); return
      case 'system-browser': {
        // the explicit "use the system browser instead" escape hatch (https + the shell's external allow-list only)
        const target = view === 'login' ? authorizeUrl : plan && plan.startUrl
        if (!target || !deps.openExternal(target)) { notify('无法在系统浏览器中打开这个页面。') }
        return
      }
      case 'clear-data': {
        const target = view === 'login' ? authorizeUrl : plan && plan.startUrl
        if (!ses || !pageView || !target) return
        Promise.all([ses.clearStorageData(), ses.clearCache(), ses.clearAuthCache()]).then(() => {
          notify('已清除本窗口保存的登录信息。')
          return pageView && pageView.webContents.loadURL(target)
        }).catch(() => {})
        return
      }
      case 'cancel': return win.close()
      case 'use': return void use(core.sanitizeLabel(arg))
    }
  }

  async function use(label) {
    if (!pending || view !== 'guide' || !plan) return
    const appWin = deps.appWindow()
    if (!appWin || appWin.isDestroyed()) {
      notify('找不到主窗口，密钥没有填入。请回到主窗口再试一次。')
      return push()
    }
    const { key, raw } = pending
    pending = null
    delivered = true
    let cleared = false
    try {
      if ((await readClip()) === raw) { await clipboard.clear(); cleared = true } // only when it still holds exactly this key
    } catch { /* ignore */ }
    appWin.webContents.send('keyhelper:event', { type: 'key', provider: plan.id, key, label, clipboardCleared: cleared })
    deps.log.info(`key window: key delivered to the settings form (provider ${plan.id}, clipboard cleared: ${cleared})`) // never the key
    win.close()
  }

  // ---------- window ----------
  function closeWindow() {
    if (win && !win.isDestroyed()) win.close()
  }

  function dropPage() {
    if (!pageView) return
    try {
      win.contentView.removeChildView(pageView)
      pageView.webContents.close()
    } catch { /* ignore */ }
    pageView = null
  }

  function ensureWindow() {
    if (win && !win.isDestroyed()) return
    win = new BrowserWindow({
      width: 1180, height: 780, minWidth: 760, minHeight: 520, title: '密钥窗口', center: true, autoHideMenuBar: true, show: false,
      backgroundColor: '#f6f7fb',
      webPreferences: { contextIsolation: true, nodeIntegration: false, sandbox: true, devTools: false },
    })
    win.removeMenu()
    panelView = new WebContentsView({
      webPreferences: { preload: path.join(__dirname, 'keyhelper-preload.js'), contextIsolation: true, nodeIntegration: false, sandbox: true, webSecurity: true, spellcheck: false, devTools: false },
    })
    win.contentView.addChildView(panelView)
    const pw = panelView.webContents
    pw.on('will-navigate', (e) => e.preventDefault())
    pw.setWindowOpenHandler(() => ({ action: 'deny' }))
    pw.on('will-attach-webview', (e) => e.preventDefault())
    win.on('resize', layout)
    win.on('focus', () => tick())
    timer = setInterval(tick, POLL_MS)
    win.once('ready-to-show', () => win.show())
    const myWin = win
    win.on('closed', () => {
      stopPolling() // the clipboard poller never outlives the window
      if (closeTimer) clearTimeout(closeTimer)
      closeTimer = null
      pending = null
      for (const cb of waiting.values()) cb({ ok: false, message: '窗口已关闭。' })
      waiting.clear()
      sendApp({ type: 'closed', provider: providerId, delivered })
      for (const v of [pageView, panelView]) { try { if (v && !v.webContents.isDestroyed()) v.webContents.close() } catch { /* ignore */ } }
      if (win === myWin) { win = null; pageView = null; panelView = null; plan = null }
    })
    pw.loadFile(PANEL_FILE).catch(() => {})
  }

  /** Swap in the provider page for a plan (a new sandboxed view in the provider's own persistent partition). */
  function openPlan(next, nextView, oauthUrl) {
    ensureWindow()
    dropPage()
    plan = next
    view = nextView
    providerId = next.id
    authorizeUrl = oauthUrl
    callbackSeen = false
    delivered = false
    pending = null
    ignoredKey = null
    if (closeTimer) clearTimeout(closeTimer)
    closeTimer = null
    const list = nextView === 'login' ? next.guide.loginSteps : next.guide.steps
    steps = core.initialSteps(Math.max(list.length, 1))
    const first = oauthUrl || next.startUrl
    ui = { url: core.describeUrl(first), loading: true, blocked: null, error: null, notice: '', collapsed: false, canBack: false }
    ses = session.fromPartition(next.partition) // persistent, one per provider: the user stays logged in next time
    hardenProviderSession(ses)
    pageView = new WebContentsView({
      webPreferences: { session: ses, contextIsolation: true, nodeIntegration: false, sandbox: true, webSecurity: true, allowRunningInsecureContent: false, spellcheck: false, devTools: false, nodeIntegrationInSubFrames: false, webviewTag: false },
    })
    win.contentView.addChildView(pageView)
    wirePage(pageView.webContents, next)
    win.setTitle(`${next.name} · ${nextView === 'login' ? '登录授权' : '密钥向导'}`)
    lastText = null // primed by the first poll: what is already on the clipboard is not offered
    layout()
    push()
    pageView.webContents.loadURL(first).catch(() => {})
    deps.log.info(`key window: ${nextView} view for provider ${next.id}`)
  }

  function openManage(pid) {
    ensureWindow()
    dropPage()
    view = 'manage'
    providerId = pid
    plan = null
    authorizeUrl = ''
    callbackSeen = false
    delivered = false
    pending = null
    steps = core.initialSteps(1)
    ui = { url: { host: '', path: '', secure: true }, loading: false, blocked: null, error: null, notice: '', collapsed: false, canBack: false }
    win.setTitle('管理密钥')
    layout()
    deps.log.info(`key window: manage view for provider ${pid}`)
    void loadManage()
  }

  /**
   * opts: { mode: 'create' (default) | 'login' | 'manage', authorizeUrl? }. The provider, its page, host allow-list, key
   * format and guide text come from the preset the local backend serves; the web UI only passes the id (and, for 'login',
   * the authorize URL it got from the backend, which is validated against the preset's hosts and this app's callback).
   */
  async function show(pid, opts) {
    const modeName = opts && typeof opts.mode === 'string' && Object.prototype.hasOwnProperty.call(MODES, opts.mode) ? opts.mode : 'create'
    const mode = MODES[modeName]
    if (typeof pid !== 'string' || !/^[a-z0-9][a-z0-9-]{0,39}$/.test(pid)) return { ok: false, message: '不支持的服务商。' }
    if (win && !win.isDestroyed() && view === mode && providerId === pid && mode !== 'login') {
      if (win.isMinimized()) win.restore()
      win.show()
      win.focus()
      if (mode === 'manage') void loadManage()
      return { ok: true }
    }
    if (mode === 'manage') {
      openManage(pid)
      win.show()
      win.focus()
      return { ok: true }
    }
    let next
    try {
      const presets = await deps.fetchPresets()
      const preset = Array.isArray(presets) ? presets.find((p) => p && p.id === pid) : null
      next = core.buildPlan(preset, testHosts)
    } catch (err) {
      deps.log.warn('key window: cannot open: ' + ((err && err.message) || err))
      return { ok: false, message: '这个服务商暂不支持向导窗口，请用下面的“粘贴密钥”。' }
    }
    let oauthUrl = ''
    if (mode === 'login') {
      oauthUrl = core.checkOauthUrl(opts.authorizeUrl, next.hosts, testHosts, appOrigin()) || ''
      if (!oauthUrl) {
        deps.log.warn('key window: refused an authorize URL that is not on the provider allow-list or has a foreign callback')
        return { ok: false, message: '登录授权地址不可信，已拒绝。' }
      }
    }
    openPlan(next, mode, oauthUrl)
    if (win.isMinimized()) win.restore()
    win.show()
    win.focus()
    return { ok: true }
  }

  ipcMain.handle('keyhelper:init', (e) => (fromPanel(e) ? snapshot() : null))
  ipcMain.handle('keyhelper:act', (e, name, arg) => {
    if (!fromPanel(e) || !core.validPanelAction(name)) return false
    act(name, typeof arg === 'string' ? arg.slice(0, 200) : '')
    return true
  })

  return {
    show,
    isOpen: () => !!win && !win.isDestroyed(),
    close: closeWindow,
  }
}

module.exports = { createKeyHelper, PANEL_URL }
