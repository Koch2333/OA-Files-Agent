// End-to-end test of the in-app key window (guide / sign-in / manage views) in REAL Electron, driven over CDP (puppeteer-core)
// and the main-process inspector. Windows only; run with node. The real provider sites need accounts, so a LOCAL fake provider
// (key page + OpenRouter-style authorize/exchange endpoints) and a fake OpenAI-compatible server stand in for them.
//
//   KH_ELECTRON  folder with an Electron runtime: electron.exe (unpacked from a build) and resources\app\ (this script copies
//                desktop\src + package.json there before every run); also OA-Agent-Desktop.exe (packaged name) for phase B   (required)
//   KH_SERVER    backend bundle dir (oa-agent.exe + pgsql + worker + web); its web\ must be a frontend build of this tree      (required)
//   KH_WORK      scratch directory, wiped at start                                                                          (required)
//   KH_SHOTS     screenshot folder (default desktop\test\screenshots\keyhelper)
//   KH_PORT      first of 5 consecutive ports used (CDP, inspector, fake provider, fake LLM, spare); default 19450
//
// Test mode: OA_KEYHELPER_TEST_HOSTS=127.0.0.1:<provider port> (loopback only; main-process environment; ignored by packaged
// builds, which phase B proves by launching the packaged name with the same variable).
import { spawn, execFileSync } from 'node:child_process'
import fs from 'node:fs'
import http from 'node:http'
import path from 'node:path'
import { createRequire } from 'node:module'
import { fileURLToPath } from 'node:url'

const require = createRequire(import.meta.url)
const here = path.dirname(fileURLToPath(import.meta.url))
const repo = path.resolve(here, '..', '..')
const puppeteer = require(path.join(repo, 'frontend', 'node_modules', 'puppeteer-core'))

for (const k of ['KH_ELECTRON', 'KH_SERVER', 'KH_WORK']) if (!process.env[k]) throw new Error(`${k} is required`)
const electronDir = path.resolve(process.env.KH_ELECTRON)
const serverDir = path.resolve(process.env.KH_SERVER)
const work = path.resolve(process.env.KH_WORK)
const shots = process.env.KH_SHOTS || path.join(here, 'screenshots', 'keyhelper')
const base = Number(process.env.KH_PORT || 19450)
const [CDP, INSP, KP, LP] = [base, base + 1, base + 2, base + 3]

try { if (fs.existsSync(work)) execFileSync("icacls", [work, "/reset", "/T", "/C", "/Q"], { stdio: "ignore" }) } catch { /* best effort */ }
fs.rmSync(work, { recursive: true, force: true })
fs.mkdirSync(work, { recursive: true })
fs.mkdirSync(shots, { recursive: true })
for (const f of fs.readdirSync(shots)) if (f.endsWith('.png')) fs.rmSync(path.join(shots, f))

const results = []
const check = (name, ok, extra = '') => {
  results.push({ name, ok: !!ok })
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${extra ? '  ' + extra : ''}`)
}
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
async function until(fn, ms = 15000, step = 150) {
  const end = Date.now() + ms
  while (Date.now() < end) {
    try {
      const v = await fn()
      if (v) return v
    } catch { /* retry */ }
    await sleep(step)
  }
  return null
}

// ---------- fake provider (key page, sign-in page, exchange) ----------
const FAKE_KEY = (n) => 'sk-' + String(n).padStart(2, '0') + 'AbCdEfGhIjKlMnOpQrStUvWx1234567'.slice(0, 30)
const KEY1 = FAKE_KEY(11)
const KEY2 = FAKE_KEY(22)
const KEY3 = FAKE_KEY(33)
const OR_KEY = 'sk-or-v1-' + 'ab12cd34'.repeat(8)
let providerServer = null
function startProvider() {
  providerServer = http.createServer((req, res) => {
    const u = new URL(req.url, `http://127.0.0.1:${KP}`)
    const html = (body) => { res.writeHead(200, { 'content-type': 'text/html; charset=utf-8' }); res.end(`<!doctype html><meta charset=utf-8><body style="font-family:sans-serif;padding:24px">${body}`) }
    if (u.pathname === '/keys') {
      return html(`<h2>Fake provider · API keys</h2><p>Test page standing in for a real key page.</p>
        <button id=create onclick="document.getElementById('out').textContent='${KEY1}'">Create new secret key</button>
        <p>Key: <code id=out></code> <button id=copy onclick="navigator.clipboard.writeText(document.getElementById('out').textContent)">Copy</button></p>
        <p><a id=evil href="https://evil.example/login">sso link</a> <a id=dl href="/file.txt" download>download</a></p>`)
    }
    if (u.pathname === '/file.txt') { res.writeHead(200, { 'content-type': 'application/octet-stream', 'content-disposition': 'attachment; filename=x.txt' }); return res.end('x') }
    if (u.pathname === '/auth') {
      const cb = u.searchParams.get('callback_url') || ''
      const sep = cb.includes('?') ? '&' : '?'
      return html(`<h2>Fake OpenRouter · Authorize OA Agent?</h2><a id=authorize href="${cb}${sep}code=fakecode&state=${encodeURIComponent(u.searchParams.get('state') || '')}">Authorize</a>`)
    }
    if (u.pathname === '/exchange' && req.method === 'POST') {
      req.resume()
      res.writeHead(200, { 'content-type': 'application/json' })
      return res.end(JSON.stringify({ key: OR_KEY }))
    }
    res.writeHead(404); res.end('nope')
  })
  return new Promise((r) => providerServer.listen(KP, '127.0.0.1', r))
}
const stopProvider = () => new Promise((r) => { providerServer.closeAllConnections?.(); providerServer.close(r) })

// ---------- OS clipboard ----------
const ps = (cmd) => execFileSync('powershell.exe', ['-NoProfile', '-NonInteractive', '-Command', cmd], { encoding: 'utf8' })
const setClip = (t) => ps(`Set-Clipboard -Value '${t.replace(/'/g, "''")}'`)
const getClip = () => ps('Get-Clipboard -Raw').replace(/\r?\n$/, '')

// ---------- presets override: deepseek + openrouter point at the fake provider / fake LLM ----------
const presets = JSON.parse(fs.readFileSync(path.join(repo, 'backend', 'internal', 'llm', 'presets.json'), 'utf8'))
for (const p of presets) {
  if (p.id === 'deepseek' || p.id === 'openrouter') {
    p.key_url = `http://127.0.0.1:${KP}/keys`
    p.base_url = `http://127.0.0.1:${LP}/v1`
  }
}
const presetsFile = path.join(work, 'presets.json')
fs.writeFileSync(presetsFile, JSON.stringify(presets))

// ---------- Electron under test ----------
function syncApp() {
  const app = path.join(electronDir, 'resources', 'app')
  fs.rmSync(app, { recursive: true, force: true })
  fs.mkdirSync(path.join(app, 'build'), { recursive: true })
  fs.cpSync(path.join(repo, 'desktop', 'src'), path.join(app, 'src'), { recursive: true })
  fs.copyFileSync(path.join(repo, 'desktop', 'package.json'), path.join(app, 'package.json'))
  const icon = path.join(repo, 'desktop', 'build', 'icon.png')
  if (fs.existsSync(icon)) fs.copyFileSync(icon, path.join(app, 'build', 'icon.png'))
}
let child = null
function launch(exeName, dataName) {
  const dataDir = path.join(work, dataName)
  const env = {
    ...process.env,
    OA_AGENT_BOOTSTRAP_DIR: path.join(work, 'bootstrap-' + dataName),
    OA_AGENT_DATA_DIR: dataDir,
    OA_AGENT_ARCHIVE_ROOT: path.join(work, 'archive-' + dataName),
    OA_AGENT_SERVER_DIR: serverDir,
    OA_KEYHELPER_TEST_HOSTS: `127.0.0.1:${KP}`,
    OA_LLM_PRESETS_FILE: presetsFile,
    OA_LLM_OAUTH_OPENROUTER_AUTHORIZE_URL: `http://127.0.0.1:${KP}/auth`,
    OA_LLM_OAUTH_OPENROUTER_EXCHANGE_URL: `http://127.0.0.1:${KP}/exchange`,
  }
  child = spawn(path.join(electronDir, exeName), [`--remote-debugging-port=${CDP}`, `--inspect=${INSP}`], { env, stdio: 'ignore' })
  child.on('exit', () => { child = null })
  return dataDir
}

const listTargets = async () => {
  try { return (await (await fetch(`http://127.0.0.1:${CDP}/json/list`)).json()).filter((t) => t.type === 'page') } catch { return null }
}
let insp = null
async function inspector() {
  if (insp && insp.ws.readyState === 1) return insp
  const list = await until(async () => { const j = await (await fetch(`http://127.0.0.1:${INSP}/json/list`)).json(); return j.length ? j : null }, 30000)
  if (!list) throw new Error('main-process inspector not reachable')
  const ws = new WebSocket(list[0].webSocketDebuggerUrl)
  await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej })
  const pending = new Map()
  let id = 0
  ws.onmessage = (m) => { const d = JSON.parse(m.data); if (d.id && pending.has(d.id)) { pending.get(d.id)(d); pending.delete(d.id) } }
  insp = { ws, send: (method, params) => new Promise((res) => { const i = ++id; pending.set(i, res); ws.send(JSON.stringify({ id: i, method, params })) }) }
  await insp.send('Runtime.enable')
  return insp
}
async function mainEval(expr) {
  const i = await inspector()
  const r = await i.send('Runtime.evaluate', { expression: expr, awaitPromise: true, returnByValue: true })
  if (r.result && r.result.exceptionDetails) throw new Error('main eval: ' + JSON.stringify(r.result.exceptionDetails).slice(0, 400))
  return r.result && r.result.result ? r.result.result.value : undefined
}
const EL = `process.mainModule.require('electron')`
const helperWin = `${EL}.BrowserWindow.getAllWindows().find((w) => /密钥|管理|登录授权|向导/.test(w.getTitle()))`
const helperCount = () => mainEval(`${EL}.BrowserWindow.getAllWindows().filter((w) => /密钥|管理|登录授权|向导/.test(w.getTitle())).length`)
const helperInfo = () => mainEval(`(() => { const w = ${helperWin}; if (!w) return null; const [cw, ch] = w.getContentSize(); return { title: w.getTitle(), cw, ch, views: w.contentView.children.map((v) => ({ ...v.getBounds(), visible: v.getVisible ? v.getVisible() : true })) } })()`)
async function shot(name) {
  const b64 = await mainEval(`(async () => { const w = ${helperWin}; if (!w) return ''; const b = w.getBounds(); const srcs = await ${EL}.desktopCapturer.getSources({ types: ['window'], thumbnailSize: { width: b.width, height: b.height } }); const s = srcs.find((x) => x.id === w.getMediaSourceId()); return s ? s.thumbnail.toPNG().toString('base64') : '' })()`)
  if (!b64) { console.log('no screenshot for', name); return }
  fs.writeFileSync(path.join(shots, name + '.png'), Buffer.from(b64, 'base64'))
}
const setTheme = (t) => mainEval(`(${EL}.nativeTheme.themeSource = ${JSON.stringify(t)}, true)`)
const resizeHelper = (w, h) => mainEval(`(() => { const x = ${helperWin}; x && x.setContentSize(${w}, ${h}); return true })()`)
const helperFocused = () => mainEval(`(() => { const x = ${helperWin}; return !!x && x.isFocused() })()`)
// Windows refuses to hand the foreground to a background process; AppActivate (the way a user would click the window) and focus() together do it.
const focusHelper = async (hint = 'DeepSeek') => {
  await mainEval(`(() => { const x = ${helperWin}; x && (x.show(), x.focus()); return !!x })()`)
  try { ps(`(New-Object -ComObject WScript.Shell).AppActivate('${hint}') | Out-Null`) } catch { /* best effort */ }
  await sleep(400)
  return helperFocused()
}
const closeHelper = () => mainEval(`(() => { const x = ${helperWin}; x && x.close(); return !!x })()`)

let browser = null
const allPages = async () => (await browser.pages())
async function pageBy(pred, ms = 15000) {
  return until(async () => { for (const p of await allPages()) { try { if (pred(p.url())) return p } catch { /* gone */ } } return null }, ms)
}
const panelPage = (ms) => pageBy((u) => /keyhelper\.html$/.test(u), ms)
const providerPage = (suffix, ms) => pageBy((u) => u.startsWith(`http://127.0.0.1:${KP}${suffix}`), ms)
const panelText = async () => (await (await panelPage(3000)).evaluate(() => document.body.innerText))
const panelHtml = async () => (await (await panelPage(3000)).evaluate(() => document.documentElement.outerHTML))
let mainPage = null
let appOrigin = ''
const events = []

async function clickText(page, sel, text) {
  const ok = await page.evaluate((s, t) => { const b = [...document.querySelectorAll(s)].find((x) => x.textContent.trim().startsWith(t) && !x.disabled); if (b) { b.click(); return true } return false }, sel, text)
  if (!ok) throw new Error(`no enabled ${sel} starting with "${text}"`)
}
const tid = (id) => `[data-testid="${id}"]`
async function configJson() {
  return mainPage.evaluate(async () => (await (await fetch('/v1/llm/config')).json()).data)
}

async function main() {
  syncApp()
  await startProvider()
  const fakeLlm = spawn(process.execPath, [path.join(repo, 'frontend', 'e2e', 'lib', 'fake-llm.mjs'), String(LP)], { stdio: 'ignore' })
  const dataDir = launch('electron.exe', 'dataA')
  try {
    // ---- boot ----
    const main = await until(async () => (await listTargets())?.find((t) => /^http:\/\/127\.0\.0\.1:\d+/.test(t.url) && !t.url.includes(`:${KP}/`)), 240000, 500)
    check('desktop app (unpackaged runtime) started and loaded the UI', !!main, main && main.url)
    appOrigin = new URL(main.url).origin
    browser = await puppeteer.connect({ browserURL: `http://127.0.0.1:${CDP}`, defaultViewport: null })
    mainPage = await pageBy((u) => u.startsWith(appOrigin))
    await mainPage.goto(appOrigin + '/settings#llm', { waitUntil: 'networkidle2' })
    await mainPage.waitForSelector('[data-mode="llm"]', { timeout: 60000 })
    await mainPage.click('[data-mode="llm"]')
    await mainPage.waitForSelector(tid('llm-simple-root'), { timeout: 60000 })
    await mainPage.evaluate(() => { window.__ev = []; window.oaDesktop.onKeyHelper((e) => window.__ev.push({ ...e })) })
    const evs = () => mainPage.evaluate(() => window.__ev.map((e) => ({ type: e.type, provider: e.provider, key: e.key, label: e.label, cleared: e.clipboardCleared, delivered: e.delivered })))
    check('bridge offers the key window (openKeyHelper / onKeyHelper / keyHelperReply)', await mainPage.evaluate(() => ['openKeyHelper', 'onKeyHelper', 'keyHelperReply'].every((k) => typeof window.oaDesktop[k] === 'function')))

    // ---- bridge validation (refused without opening anything) ----
    const refused = await mainPage.evaluate(async (kp, app) => {
      const cb = encodeURIComponent(app + '/v1/llm/oauth/callback')
      return [
        await window.oaDesktop.openKeyHelper('../x'),
        await window.oaDesktop.openKeyHelper('nonexistent-provider'),
        await window.oaDesktop.openKeyHelper('ollama'),
        await window.oaDesktop.openKeyHelper('openrouter', { mode: 'login', authorizeUrl: 'https://evil.example/auth?callback_url=' + cb }),
        await window.oaDesktop.openKeyHelper('openrouter', { mode: 'login', authorizeUrl: `http://127.0.0.1:${kp}/auth?callback_url=` + encodeURIComponent('https://evil.example/v1/llm/oauth/callback') }),
        await window.oaDesktop.openKeyHelper('openrouter', { mode: 'login', authorizeUrl: '' }),
      ]
    }, KP, appOrigin)
    check('bridge refuses bad ids, unsupported providers and untrusted authorize URLs', refused.every((r) => r && r.ok === false), JSON.stringify(refused))
    check('nothing opened by the refused calls', (await helperCount()) === 0)

    // ---- guide view (DeepSeek, fake provider page) ----
    await mainPage.click(tid('llm-simple-card-deepseek'))
    await mainPage.waitForSelector(tid('llm-simple-open-helper'))
    const btnText = await mainPage.$eval(tid('llm-simple-open-helper'), (b) => b.textContent.trim())
    check('primary button text (desktop)', btnText === '打开向导窗口，一步步带你拿到 Key', btnText)
    check('secondary "I already have a key" path exists', !!(await mainPage.$(tid('llm-simple-have-key'))))
    check('mode is stated in the UI (desktop)', (await mainPage.$eval(tid('llm-simple-helper-mode'), (e) => e.textContent)).includes('桌面版'))
    await mainPage.click(tid('llm-simple-open-helper'))
    const panel = await panelPage(20000)
    const prov = await providerPage('/keys', 20000)
    check('key window opened: panel + provider page in one window', !!panel && !!prov && (await helperCount()) === 1)
    await sleep(800)
    const t0 = await panelText()
    check('panel shows provider, numbered steps and the current host', t0.includes('DeepSeek') && /1[\s\S]*登录或注册/.test(t0) && t0.includes(`127.0.0.1:${KP}`), t0.slice(0, 80).replace(/\n/g, ' | '))
    check('window title names the provider', (await helperInfo()).title.includes('DeepSeek'))

    // layout: page and panel side by side, no overlap, together exactly the content area
    let info = await helperInfo()
    const geo = (i) => { const [a, b] = i.views.filter((v) => v.visible); const [l, r] = a.x <= b.x ? [a, b] : [b, a]; return { l, r, ok: l.x === 0 && l.x + l.width === r.x && r.x + r.width === i.cw && l.height === i.ch && r.height === i.ch && r.width >= 260 && l.width > r.width } }
    check('wide layout: page left, panel right, no overlap, fills the window', geo(info).ok, JSON.stringify(info.views))
    await shot('guide-light-wide')
    await resizeHelper(760, 560)
    await sleep(500)
    info = await helperInfo()
    check('narrow layout (minimum width): still no overlap and the page keeps width', geo({ ...info, views: info.views }).l.width >= 400 && geo(info).l.x + geo(info).l.width === geo(info).r.x, JSON.stringify(info.views))
    await shot('guide-light-narrow')
    await resizeHelper(1180, 780)
    await sleep(300)
    await setTheme('dark')
    await sleep(600)
    await shot('guide-dark-wide')

    // sandbox facts from inside the provider page
    const facts = await prov.evaluate(async () => {
      const out = { bridge: typeof window.oaDesktop + '/' + typeof window.oaKeyHelper, node: typeof require + '/' + typeof process }
      try { await navigator.clipboard.readText(); out.read = 'allowed' } catch { out.read = 'denied' }
      try { out.geo = (await navigator.permissions.query({ name: 'geolocation' })).state } catch { out.geo = 'n/a' }
      try { out.notif = Notification.permission } catch { out.notif = 'n/a' }
      return out
    })
    check('provider page: no bridge, no Node', facts.bridge === 'undefined/undefined' && facts.node === 'undefined/undefined', JSON.stringify(facts))
    check('provider page: clipboard read, geolocation and notifications are not granted', facts.read === 'denied' && facts.geo !== 'granted' && facts.notif !== 'granted', JSON.stringify(facts))

    // navigation is locked to the allow-list
    await prov.evaluate(() => { location.href = 'https://evil.example/steal' })
    await sleep(1200)
    const blockedTxt = await panelText()
    check('navigation to a foreign host is blocked and explained in the panel', !!(await providerPage('/keys', 2000)) && blockedTxt.includes('已拦截') && blockedTxt.includes('evil.example'), (await listTargets()).map((t) => t.url).join(' ; ') + ' || ' + blockedTxt.slice(0, 200).replace(/\n/g, ' | '))
    await shot('guide-light-blocked')
    const pagesBefore = (await allPages()).length
    const popup = await prov.evaluate(() => { const w = window.open('https://evil.example/popup'); return w === null ? 'null' : 'window' })
    await sleep(800)
    check('popup to a foreign host is denied (no new window)', (await allPages()).length === pagesBefore && !(await listTargets()).some((t) => t.url.includes('evil.example')), popup)
    await prov.evaluate((kp) => { window.open(`http://127.0.0.1:${kp}/keys?via=popup`) }, KP)
    check('popup to an allowed host loads in the same view', !!(await providerPage('/keys?via=popup', 5000)) && (await allPages()).length === pagesBefore)
    const prov2 = await providerPage('/keys?via=popup', 2000)
    await prov2.click('#dl')
    await sleep(900)
    check('downloads are blocked with a notice', (await panelText()).includes('不允许下载'))
    await prov2.evaluate(() => localStorage.setItem('login', 'session-marker'))

    // steps: manual progression, then error state (provider down) and recovery
    const pp = await panelPage()
    pp.on("pageerror", (e) => console.log("PANEL pageerror:", e.message))
    pp.on("console", (m) => console.log("PANEL console:", m.text()))
    const stepNow = () => pp.evaluate(() => document.querySelector('li.cur .st').textContent)
    check('opening on the key page skips "log in" and "find the page" automatically', (await stepNow()).includes('创建一个新密钥'), await stepNow())
    await clickText(pp, 'button', '这一步完成了')
    await sleep(300)
    check('manual "this step is done" moves to the next step', (await stepNow()).includes('复制密钥'), await stepNow())
    await setTheme('light')
    await sleep(400)
    await shot('steps-light')
    await stopProvider()
    await pp.click('#reload')
    await sleep(1500)
    check('provider unreachable: panel shows an error state', (await panelText()).includes('页面没有打开'))
    await shot('guide-light-error')
    await startProvider()
    await pp.click('#reload')
    check('reload recovers when the provider is back', !!(await providerPage('/keys', 8000)))
    const prov3 = await providerPage('/keys', 2000)

    // collapse / expand
    await pp.click('#toggle')
    await sleep(500)
    info = await helperInfo()
    const collapsed = geo(info)
    check('panel collapses to a narrow strip and the page takes the room', collapsed.r.width <= 60 && collapsed.l.width + collapsed.r.width === info.cw, JSON.stringify(info.views))
    await shot('guide-light-collapsed')
    await pp.click('#toggle')
    await sleep(400)

    // clipboard detection
    const afterOpenClip = await panelText()
    check('nothing offered before anything was copied', !(await pp.evaluate(() => !document.getElementById('detected').hidden)))
    await focusHelper()
    await sleep(500)
    setClip('hello this is not a key')
    await sleep(1800)
    check('non-key clipboard text is ignored', !(await pp.evaluate(() => !document.getElementById('detected').hidden)))
    await prov3.click('#create')
    setClip(KEY2)
    await focusHelper()
    const det = await until(() => pp.evaluate(() => !document.getElementById('detected').hidden), 8000, 200)
    check('a key-shaped clipboard text is detected while the window is focused', !!det, 'focused=' + (await helperFocused()) + ' mainClipLen=' + JSON.stringify(await mainEval(`JSON.stringify({ keys: Object.keys(${EL}.clipboard), proto: Object.getOwnPropertyNames(Object.getPrototypeOf(${EL}.clipboard)), r: String(${EL}.clipboard.readText()).slice(0, 8) })`)) + ' psClipLen=' + getClip().length + ' keyLen=' + KEY2.length)
    const html = await panelHtml()
    check('panel shows the masked key only (tail), never the full key', html.includes('••••' + KEY2.slice(-4)) && !html.includes(KEY2) && !html.includes(KEY2.slice(3, 20)))
    check('the full key is not on the main window either (before confirmation)', !(await mainPage.evaluate(() => document.documentElement.outerHTML)).includes(KEY2))
    await shot('detected-light')
    await setTheme('dark')
    await sleep(500)
    await shot('detected-dark')
    await setTheme('light')
    await pp.click('#ignore')
    await sleep(500)
    check('"Ignore" hides the offer', !(await pp.evaluate(() => !document.getElementById('detected').hidden)))
    setClip(KEY1)
    const det2 = await until(() => pp.evaluate(() => !document.getElementById('detected').hidden), 8000, 200)
    check('a different key is detected again after ignoring', !!det2)
    check('label defaults to the suggested name "OA Agent"', (await pp.$eval('#label', (i) => i.value)) === 'OA Agent')
    check('nothing was delivered before the user confirmed', (await evs()).filter((e) => e.type === 'key').length === 0)
    await pp.click('#use')
    const closed = await until(async () => (await helperCount()) === 0, 6000, 200)
    check('"Use this key" closes the window', !!closed)
    await sleep(500)
    const e1 = await evs()
    const keyEv = e1.find((e) => e.type === 'key')
    check('the settings page received exactly the confirmed key, provider and label', keyEv && keyEv.key === KEY1 && keyEv.provider === 'deepseek' && keyEv.label === 'OA Agent', JSON.stringify(e1.map((e) => ({ ...e, key: e.key ? '(len ' + e.key.length + ')' : undefined }))))
    check('closed event follows with delivered=true', e1.some((e) => e.type === 'closed' && e.delivered === true))
    check('clipboard was cleared (it still held exactly that key)', getClip() === '')
    check('settings page tells the user the clipboard was cleared', (await mainPage.$eval(tid('llm-simple-clip-note'), (e) => e.textContent)).includes('清空'))

    // automatic flow after delivery: models, test against the fake OpenAI server, then save
    const ok = await mainPage.waitForSelector(tid('llm-simple-result-ok'), { timeout: 60000 }).catch(() => null)
    check('connection test ran automatically against the fake OpenAI-compatible server', !!ok)
    check('key name field carries "OA Agent"', (await mainPage.$eval(tid('llm-simple-label'), (i) => i.value)) === 'OA Agent')
    await mainPage.click(tid('llm-simple-save'))
    await mainPage.waitForSelector(tid('llm-simple-saved'), { timeout: 30000 })
    const cfg = await configJson()
    check('saved: key name "OA Agent" and tail hint only (no key in the API view)', cfg.key_label === 'OA Agent' && cfg.api_key_hint === '••••' + KEY1.slice(-4) && cfg.api_key_set === true && !JSON.stringify(cfg).includes(KEY1), JSON.stringify(cfg))
    const savedText = await mainPage.$eval(tid('llm-simple-saved'), (e) => e.textContent)
    check('status line shows the key name', savedText.includes('OA Agent'), savedText.trim().slice(0, 80))
    const logs = []
    const walk = (d) => { for (const e of fs.readdirSync(d, { withFileTypes: true })) { const p = path.join(d, e.name); if (e.isDirectory()) { if (!/pgsql|pg$|^pg|electron$/.test(e.name)) walk(p) } else if (/\.(log|txt|json)$/i.test(e.name) && fs.statSync(p).size < 20e6) logs.push(p) } }
    walk(dataDir)
    check('no key in any desktop/backend log or data text file', logs.length > 0 && logs.every((f) => !fs.readFileSync(f, 'latin1').includes(KEY1) && !fs.readFileSync(f, 'latin1').includes(KEY2)), `${logs.length} files scanned`)

    // poller stops with the window
    setClip(KEY3)
    await sleep(2600)
    check('after the window closed the clipboard is no longer polled (no new key event)', (await evs()).filter((e) => e.type === 'key').length === 1)
    setClip('(test finished)')

    // persistent per-provider session
    await mainPage.click(tid('llm-simple-manage'))
    await panelPage(15000)
    await sleep(1200)
    const mt = await panelText()
    check('manage view: provider, key name, tail and source', mt.includes('DeepSeek') && mt.includes('OA Agent') && mt.includes('••••' + KEY1.slice(-4)) && mt.includes('手动粘贴'), mt.slice(0, 120).replace(/\n/g, ' | '))
    check('manage view is a single full-width panel (no provider page)', (await helperInfo()).views.filter((v) => v.visible).length === 1)
    const mp = await panelPage()
    check('before testing: "not tested yet" or the earlier success', /还没测试过|成功/.test(mt))
    await clickText(mp, 'button', '重新测试')
    const tested = await until(() => mp.evaluate(() => { const dt = [...document.querySelectorAll('dl.kv dt')].find((d) => d.textContent === '上次测试'); return !!dt && dt.nextElementSibling.textContent.startsWith('成功') && /\d{2}:\d{2}/.test(dt.nextElementSibling.textContent) }), 30000, 300)
    check('"Test again" runs the saved key through the settings page and shows the result', !!tested)
    await sleep(300)
    await shot('manage-light')
    await setTheme('dark')
    await sleep(500)
    await shot('manage-dark')
    await setTheme('light')
    await clickText(mp, 'button', '删除密钥')
    await sleep(500)
    check('"Remove key" asks for confirmation first', (await panelText()).includes('确定删除已保存的密钥吗'))
    check('nothing removed before confirming', (await configJson()).api_key_set === true)
    await shot('manage-confirm')
    await clickText(mp, 'button', '先不删')
    await sleep(300)
    check('"先不删" cancels', !(await panelText()).includes('确定删除已保存的密钥吗') && (await configJson()).api_key_set === true)

    // replace key -> same window switches to the guide of that provider
    await clickText(mp, 'button', '更换密钥')
    const prov4 = await providerPage('/keys', 20000)
    await sleep(800)
    check('"Replace key" reopens the guide in the same window', !!prov4 && (await helperCount()) === 1 && (await panelText()).includes('登录或注册'))
    const persisted = await prov4.evaluate(() => localStorage.getItem('login'))
    check('the provider session is persistent per provider (login marker survived the window being closed and reopened)', persisted === 'session-marker', String(persisted))
    await shot('replace-guide')
    await clickText(await panelPage(), 'button', '退出登录并清除本窗口数据')
    await sleep(1500)
    const cleared = await (await providerPage('/keys', 8000)).evaluate(() => localStorage.getItem('login'))
    check('"Sign out / clear this window\'s data" clears the provider session', cleared === null, String(cleared))
    await closeHelper()
    await until(async () => (await helperCount()) === 0, 5000)
    check('replace flow kept the saved key until a new one is confirmed', (await configJson()).api_key_set === true)

    await mainPage.click(tid('llm-simple-manage'))
    const mp2 = await panelPage(15000)
    await until(async () => (await panelText()).includes('••••'), 10000)
    await clickText(mp2, 'button', '删除密钥')
    await sleep(300)
    await clickText(mp2, 'button', '确定删除')
    const removed = await until(async () => (await configJson()).api_key_set === false, 15000, 300)
    const cfg2 = await configJson()
    check('confirmed removal deletes the saved key (offline rules) and the page reflects it', !!removed && cfg2.mode === 'heuristic', JSON.stringify({ mode: cfg2.mode, set: cfg2.api_key_set }))
    check('panel reports the removal', (await panelText()).includes('已删除密钥'))
    await shot('manage-removed')
    await clickText(mp2, 'button', '换一个服务商')
    await until(async () => (await helperCount()) === 0, 5000)
    check('"Switch provider" closes the window', (await helperCount()) === 0)
    check('after removing the global key the settings page shows the offline rules (as the dialog said)', !!(await mainPage.waitForSelector('[data-mode="llm"][aria-checked="false"]', { timeout: 8000 }).catch(() => null)))
    await mainPage.click('[data-mode="llm"]')
    check('back in AI mode the provider cards are shown again', !!(await mainPage.waitForSelector(tid('llm-simple-pick-hint'), { timeout: 8000 }).catch(() => null)))

    // ---- sign-in view (OpenRouter, fake authorize + exchange) ----
    await mainPage.click(tid('llm-simple-card-openrouter'))
    await mainPage.waitForSelector(tid('global-llm-login-start'))
    await mainPage.click(tid('global-llm-login-start'))
    const lp = await panelPage(20000)
    const authPage = await providerPage('/auth', 20000)
    check('sign-in opens in the in-app window (not the system browser): authorize page + panel', !!lp && !!authPage && (await helperCount()) === 1)
    await sleep(800)
    const lt = await panelText()
    check('sign-in view lists its 3 short steps', lt.includes('登录 OpenRouter') && lt.includes('点“授权”') && lt.includes('等它自动完成'), lt.slice(0, 80).replace(/\n/g, ' | '))
    check('window title says sign-in', (await helperInfo()).title.includes('登录授权'))
    await shot('login-light')
    await authPage.click('#authorize')
    const cbSeen = await until(async () => (await panelText()).includes('授权已提交'), 15000, 200)
    check('callback is recognised: panel says the authorisation was submitted', !!cbSeen)
    check('the loopback callback page was shown inside the window only (no redirect into the app)', await (async () => { const t = await listTargets(); return !t.some((x) => /\/settings\?llm_oauth/.test(x.url)) })())
    await shot('login-callback')
    const gone = await until(async () => (await helperCount()) === 0, 8000, 200)
    check('the window closes by itself after the callback', !!gone)
    const created = await mainPage.waitForSelector(tid('global-llm-login-created'), { timeout: 45000 }).catch(() => null)
    check('settings page shows "key created" with the key name', !!created && (await mainPage.$eval(tid('global-llm-login-key-name'), (e) => e.textContent)).length > 0, created ? await mainPage.$eval(tid('global-llm-login-key-name'), (e) => e.textContent) : await mainPage.$eval(tid('global-llm-login-panel'), (e) => e.innerText.replace(/\n/g, ' | ')).catch((e) => String(e)))
    const cfg3 = await configJson()
    check('oauth key stored: source oauth, openrouter, no key in the view', cfg3.key_source === 'oauth' && cfg3.preset_id === 'openrouter' && !JSON.stringify(cfg3).includes(OR_KEY), JSON.stringify({ s: cfg3.key_source, p: cfg3.preset_id, l: cfg3.key_label }))
    const e2 = await evs()
    check('settings page got the oauth-callback event and a closed event with delivered=true', e2.some((e) => e.type === 'oauth-callback') && e2.some((e) => e.type === 'closed' && e.provider === 'openrouter' && e.delivered === true))

    // manage the oauth key (Remove = disconnect)
    await mainPage.click(tid('global-llm-login-manage'))
    const mp3 = await panelPage(15000)
    await until(async () => (await panelText()).includes('登录授权自动创建'), 10000)
    check('manage view marks the key as created by sign-in', (await panelText()).includes('登录授权自动创建'))
    await clickText(mp3, 'button', '删除密钥')
    await sleep(300)
    await clickText(mp3, 'button', '确定删除')
    const disc = await until(async () => (await configJson()).api_key_set === false, 15000, 300)
    check('removing the sign-in key disconnects it', !!disc)
    await closeHelper()
    await until(async () => (await helperCount()) === 0, 5000)

    // closing the sign-in window without finishing stops the waiting state
    await mainPage.click(tid('global-llm-login-start'))
    await panelPage(20000)
    await sleep(800)
    await closeHelper()
    const msg = await until(async () => mainPage.$eval(tid('global-llm-login-result'), (e) => e.textContent).catch(() => ''), 8000, 200)
    check('closing the sign-in window cancels the waiting state with a clear message', !!msg && msg.includes('已关闭登录窗口'), String(msg).trim())

    check('no stray window left', (await helperCount()) === 0)
  } catch (err) {
    try { if (mainPage) { await mainPage.screenshot({ path: path.join(work, "fatal-main.png") }); console.log("URL", mainPage.url()); console.log((await mainPage.evaluate(() => document.body.innerText)).slice(0, 800)) } } catch (x) { console.log("no debug shot", x.message) }
    throw err
  } finally {
    try { await mainEval(`(setTimeout(() => ${EL}.app.quit(), 50), true)`) } catch { /* ignore */ }
    await sleep(2500)
    try { browser && (await browser.disconnect()) } catch { /* ignore */ }
    try { if (child) child.kill() } catch { /* ignore */ }
    try { fakeLlm.kill() } catch { /* ignore */ }
    await sleep(1500)
  }

  // ---- phase B: the packaged executable name must ignore OA_KEYHELPER_TEST_HOSTS ----
  insp = null
  browser = null
  launch('OA-Agent-Desktop.exe', 'dataB')
  try {
    const m = await until(async () => (await listTargets())?.find((t) => /^http:\/\/127\.0\.0\.1:\d+/.test(t.url) && !t.url.includes(`:${KP}/`)), 240000, 500)
    browser = await puppeteer.connect({ browserURL: `http://127.0.0.1:${CDP}`, defaultViewport: null })
    const pg = await pageBy((u) => u.startsWith(new URL(m.url).origin))
    const r = await pg.evaluate(() => window.oaDesktop.openKeyHelper('deepseek', { mode: 'create' }))
    const open = await (async () => { const t = await listTargets(); return t.some((x) => /keyhelper\.html$/.test(x.url)) })()
    check('packaged build ignores OA_KEYHELPER_TEST_HOSTS: a loopback key page is refused', r.ok === false && !open, JSON.stringify(r))
  } finally {
    try { await (await inspector()).send('Runtime.evaluate', { expression: `setTimeout(() => process.mainModule.require('electron').app.quit(), 50)` }) } catch { /* ignore */ }
    await sleep(2500)
    try { browser && (await browser.disconnect()) } catch { /* ignore */ }
    try { if (child) child.kill() } catch { /* ignore */ }
    try { await stopProvider() } catch { /* ignore */ }
  }

  const failed = results.filter((r) => !r.ok)
  console.log(`\n${results.length - failed.length}/${results.length} checks passed`)
  process.exit(failed.length ? 1 : 0)
}

main().catch(async (e) => {
  console.error('FATAL', e)
  try { if (mainPage) { await mainPage.screenshot({ path: path.join(work, 'fatal-main.png') }); console.log('URL', mainPage.url()); console.log((await mainPage.evaluate(() => document.body.innerText)).slice(0, 600)) } } catch (x) { console.log('no debug shot', x.message) }
  try { if (child) child.kill() } catch { /* ignore */ }
  process.exit(2)
})
