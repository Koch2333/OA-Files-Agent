// End-to-end test of the working directory (archive folder) lifecycle of the PACKAGED desktop app, driven over CDP
// (renderer windows, puppeteer-core) and the Node inspector (main process: menu clicks, native message boxes).
// Windows only; run with node from the repository (puppeteer-core is resolved from frontend/node_modules).
//
//   E2E_EXE        packaged OA-Agent-Desktop.exe (portable folder or installed dir)            (required)
//   E2E_WORK       scratch directory, wiped at start                                          (required)
//   E2E_PORTABLE=1 the exe has portable.flag: data lives in <exe dir>\data (wiped at start)
//   E2E_DATA_ENV=1 set OA_AGENT_DATA_DIR=<work>\data (documented test override); otherwise the data dir follows the
//                  bootstrap dir (OA_AGENT_BOOTSTRAP_DIR=<work>\bootstrap is always set, so %LOCALAPPDATA% is never used)
//   E2E_ADV_DATA=1 on first run also choose a custom data directory in the dialog's advanced section (app relaunches)
//   E2E_SAMPLES    tests\samples folder (default: <repo>\tests\samples)
//   E2E_SHOTS      screenshot folder (default <work>\shots)
//   E2E_PORT / E2E_INSPECT   CDP / inspector ports (default 19340 / 19341; 9158-9466 is often a Hyper-V excluded range)
//
// Phases: A first launch + dialog validation + choice persisted + local mode; B archive samples, files only inside the
// chosen folder; C graceful quit (no orphans, run lock removed) + second launch (no prompt, same folder); D 文件 →
// 更改工作目录…: cancel, cancel at confirmation, migrate, use existing, forced copy failure, forced restart failure;
// E hard kill of the Electron tree + relaunch (leftover processes, data intact, unclean-exit diagnostics).
import { spawn, execFileSync } from 'node:child_process'
import crypto from 'node:crypto'
import fs from 'node:fs'
import path from 'node:path'
import { createRequire } from 'node:module'
import { fileURLToPath } from 'node:url'

const require = createRequire(import.meta.url)
const here = path.dirname(fileURLToPath(import.meta.url))
const repo = path.resolve(here, '..', '..')
const puppeteer = require(path.join(repo, 'frontend', 'node_modules', 'puppeteer-core'))

const exe = process.env.E2E_EXE
if (!exe || !process.env.E2E_WORK) throw new Error('E2E_EXE and E2E_WORK are required')
const work = path.resolve(process.env.E2E_WORK)
const exeDir = path.dirname(exe)
const portable = process.env.E2E_PORTABLE === '1'
const dataEnv = process.env.E2E_DATA_ENV === '1'
const advData = process.env.E2E_ADV_DATA === '1' && !portable && !dataEnv
const samplesDir = process.env.E2E_SAMPLES || path.join(repo, 'tests', 'samples')
const cdpPort = Number(process.env.E2E_PORT || 19340)
const inspectPort = Number(process.env.E2E_INSPECT || 19341)
const bootstrap = path.join(work, 'bootstrap')
let dataDir = portable ? path.join(exeDir, 'data') : dataEnv ? path.join(work, 'data') : bootstrap
const serverRoot = path.join(exeDir, 'resources', 'app-server')
const shots = process.env.E2E_SHOTS || path.join(work, 'shots')
const docs = path.join(process.env.USERPROFILE || '', 'Documents')
const expectedDefault = portable ? path.join(exeDir, '归档') : path.join(docs, 'OA-Agent', '归档')

function aclReset(p) {
  try { execFileSync('icacls', [p, '/reset', '/T', '/C', '/Q'], { stdio: 'ignore' }) } catch {}
}
function denyWrite(p) {
  const u = process.env.USERNAME
  execFileSync('icacls', [p, '/deny', `${u}:(OI)(CI)(W,AD,WD)`], { stdio: 'ignore' })
}
for (const p of [path.join(work, 'ro'), path.join(work, 'fail-copy'), path.join(work, 'fail-start')]) if (fs.existsSync(p)) aclReset(p)
fs.rmSync(work, { recursive: true, force: true })
if (portable) for (const d of ['data', '归档']) fs.rmSync(path.join(exeDir, d), { recursive: true, force: true })
fs.mkdirSync(shots, { recursive: true })

const results = []
const check = (name, ok, extra = '') => {
  results.push({ name, ok: !!ok })
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${extra ? '  ' + String(extra).slice(0, 400) : ''}`)
}
const info = (...a) => console.log('INFO ', ...a)
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
const same = (a, b) => !!a && !!b && path.resolve(a).toLowerCase() === path.resolve(b).toLowerCase()
async function until(fn, ms, step = 300) {
  const end = Date.now() + ms
  for (;;) {
    try {
      const v = await fn()
      if (v) return v
    } catch {}
    if (Date.now() > end) return null
    await sleep(step)
  }
}

const env = { ...process.env, OA_AGENT_BOOTSTRAP_DIR: bootstrap }
delete env.OA_AGENT_ARCHIVE_ROOT
delete env.OA_AGENT_DATA_DIR
if (dataEnv) env.OA_AGENT_DATA_DIR = dataDir
const args = [`--remote-debugging-port=${cdpPort}`, `--inspect=${inspectPort}`]

// ---------- process helpers ----------
function procsUnder(dir) {
  const ps = `Get-CimInstance Win32_Process | Where-Object { $_.ExecutablePath -and $_.ExecutablePath -like '${dir.replace(/'/g, "''")}*' } | ForEach-Object { "$($_.ProcessId) $($_.Name)" }`
  return execFileSync('powershell.exe', ['-NoProfile', '-Command', ps], { encoding: 'utf8' }).split(/\r?\n/).filter(Boolean)
}
let child = null
let electronPid = 0
function launch() {
  child = spawn(exe, args, { env, stdio: 'ignore', windowsHide: false })
  electronPid = child.pid
  child.exited = false
  child.on('exit', () => (child.exited = true))
  return child
}
async function targets() {
  try {
    const r = await fetch(`http://127.0.0.1:${cdpPort}/json/list`)
    return (await r.json()).filter((t) => t.type === 'page')
  } catch {
    return null
  }
}
let browser = null
async function connect() {
  if (browser && browser.connected) return browser
  browser = await puppeteer.connect({ browserURL: `http://127.0.0.1:${cdpPort}`, defaultViewport: null, protocolTimeout: 120000 })
  return browser
}
async function pageBy(re, ms = 20000) {
  return until(async () => {
    const b = await connect()
    for (const p of await b.pages()) {
      try {
        if (re.test(p.url())) return p
      } catch {}
    }
    return null
  }, ms, 250)
}

// ---------- main-process inspector ----------
let insp = null
async function inspector() {
  if (insp && insp.ws.readyState === 1) return insp
  const list = await until(async () => {
    const r = await fetch(`http://127.0.0.1:${inspectPort}/json/list`)
    const j = await r.json()
    return j.length ? j : null
  }, 30000)
  if (!list) throw new Error('main-process inspector not reachable (is --inspect disabled by a fuse?)')
  const ws = new WebSocket(list[0].webSocketDebuggerUrl)
  await new Promise((res, rej) => {
    ws.onopen = res
    ws.onerror = rej
  })
  const pending = new Map()
  let id = 0
  ws.onmessage = (m) => {
    const d = JSON.parse(m.data)
    if (d.id && pending.has(d.id)) {
      pending.get(d.id)(d)
      pending.delete(d.id)
    }
  }
  insp = {
    ws,
    send: (method, params) =>
      new Promise((res) => {
        const i = ++id
        pending.set(i, res)
        ws.send(JSON.stringify({ id: i, method, params }))
      }),
  }
  await insp.send('Runtime.enable')
  return insp
}
async function mainEval(expr) {
  const i = await inspector()
  const r = await i.send('Runtime.evaluate', { expression: expr, awaitPromise: true, returnByValue: true })
  if (r.result && r.result.exceptionDetails) throw new Error('main eval: ' + JSON.stringify(r.result.exceptionDetails).slice(0, 400))
  return r.result && r.result.result ? r.result.result.value : undefined
}
// Replace dialog.showMessageBox with a recorder that answers from a queue of button indexes (or JS function sources).
async function hookDialogs() {
  await mainEval(`(() => {
    const req = process.mainModule.require
    const { dialog } = req('electron')
    globalThis.__oa = globalThis.__oa || { log: [], replies: [] }
    if (!globalThis.__oaHooked) {
      globalThis.__oaHooked = true
      dialog.showMessageBox = async (a, b) => {
        const o = b || a || {}
        const entry = { message: o.message, detail: o.detail, buttons: o.buttons, type: o.type, at: Date.now() }
        globalThis.__oa.log.push(entry)
        let r = globalThis.__oa.replies.shift()
        if (typeof r === 'string') { try { r = await (new Function('req', 'o', r))(req, o) } catch (e) { entry.hookError = String(e) ; r = 0 } }
        entry.response = r === undefined ? (o.cancelId ?? 0) : r
        return { response: entry.response, checkboxChecked: false }
      }
    }
    return true
  })()`)
}
const queueReplies = (arr) => mainEval(`(globalThis.__oa.replies.push(...${JSON.stringify(arr)}), globalThis.__oa.replies.length)`)
const dialogLog = () => mainEval('globalThis.__oa.log')
const clearLog = () => mainEval('(globalThis.__oa.log = [], globalThis.__oa.replies = [], true)')
const clickMenu = (label) =>
  mainEval(`(() => { const { Menu } = process.mainModule.require('electron'); const it = Menu.getApplicationMenu().items[0].submenu.items.find((i) => i.label === ${JSON.stringify(label)}); if (!it) return false; setTimeout(() => it.click(), 10); return true })()`)
const mainPid = () => mainEval('process.pid')

// ---------- backend API ----------
let origin = ''
async function refreshOrigin(ms = 240000) {
  const t = await until(async () => {
    const ts = await targets()
    const m = ts && ts.find((x) => /^http:\/\/127\.0\.0\.1:\d+\//.test(x.url) && !/splash|setup/.test(x.url))
    if (!m) return null
    const o = new URL(m.url).origin
    const h = await fetch(o + '/healthz').catch(() => null)
    return h && h.ok ? o : null
  }, ms, 500)
  origin = t || ''
  return origin
}
async function api(method, p, body, headers = {}) {
  const r = await fetch(origin + p, { method, headers: { ...(body && !(body instanceof FormData) ? { 'Content-Type': 'application/json' } : {}), ...headers }, body: body instanceof FormData ? body : body ? JSON.stringify(body) : undefined })
  const buf = Buffer.from(await r.arrayBuffer())
  let json = null
  try { json = JSON.parse(buf.toString('utf8')) } catch {}
  return { status: r.status, json, buf, headers: r.headers }
}
const archivePath = async () => (await api('GET', '/v1/admin/system')).json?.data?.disk?.archive_path
const sha = (b) => crypto.createHash('sha256').update(b).digest('hex')

// Waits for the main UI after a (re)launch; handles the first-run dialog with `onSetup` if it appears.
async function waitForUi({ onSetup, label }) {
  const t0 = Date.now()
  let splashAt = 0
  let setupSeen = false
  let mainAt = 0
  const end = Date.now() + 300000
  while (Date.now() < end) {
    const ts = await targets()
    if (!ts && Date.now() - t0 > 60000) throw new Error(`no CDP endpoint on port ${cdpPort} after 60 s (app exited or port reserved)`)
    if (ts) {
      if (!splashAt && ts.some((t) => /splash\.html/.test(t.url))) {
        splashAt = Date.now() - t0
        if (label === 'first') {
          try {
            const sp = await pageBy(/splash\.html/, 3000)
            await sleep(600)
            await sp.screenshot({ path: path.join(shots, 'A-splash.png') })
          } catch {}
        }
      }
      if (!setupSeen && ts.some((t) => /setup\.html/.test(t.url))) {
        setupSeen = true
        if (onSetup) await onSetup(await pageBy(/setup\.html/))
      }
      const m = ts.find((t) => /^http:\/\/127\.0\.0\.1:\d+\//.test(t.url))
      if (m && !ts.some((t) => /splash\.html/.test(t.url))) {
        mainAt = Date.now() - t0
        break
      }
    }
    await sleep(100)
  }
  await refreshOrigin()
  return { splashAt, setupSeen, mainAt }
}

async function quitApp(label) {
  const before = procsUnder(serverRoot)
  const pid = electronPid
  try { await mainEval(`(setTimeout(() => process.mainModule.require('electron').app.quit(), 50), true)`) } catch {}
  insp = null
  try { browser && browser.disconnect() } catch {}
  browser = null
  const gone = await until(() => !pidAlive(pid), 150000, 300)
  await sleep(1500)
  const after = procsUnder(serverRoot)
  check(`${label}: app exited after quit`, gone)
  check(`${label}: backend+postgres+worker were running before quit`, ['oa-agent.exe', 'postgres.exe', 'document-worker.exe'].every((n) => before.some((l) => l.toLowerCase().includes(n))), before.join(' | '))
  check(`${label}: no orphan oa-agent/postgres/worker processes`, after.length === 0, after.join(' | '))
  check(`${label}: run lock removed`, !fs.existsSync(path.join(dataDir, 'logs', 'run.lock')))
}
function pidAlive(pid) {
  try {
    process.kill(pid, 0)
    return true
  } catch (e) {
    return e.code === 'EPERM'
  }
}

// ---------- setup dialog helpers ----------
async function setArch(p, value) {
  await p.$eval('#m-arch', (el) => (el.innerHTML = ''))
  await p.$eval('#arch', (i, v) => { i.value = v; i.dispatchEvent(new Event('input')) }, value)
  await p.waitForFunction(() => document.getElementById('m-arch').innerText.trim().length > 0, { timeout: 15000 })
  await sleep(150)
  return p.evaluate(() => ({ disabled: document.getElementById('b-ok').disabled, msg: document.getElementById('m-arch').innerText }))
}

// =====================================================================================================================
const chosen = path.join(work, '我的归档')
const probeRo = path.join(work, 'ro')
fs.mkdirSync(probeRo, { recursive: true })
denyWrite(probeRo)
let customData = path.join(work, '自定义数据')

info(`exe=${exe} portable=${portable} dataEnv=${dataEnv} advData=${advData} data=${dataDir}`)
// ---------- A. first launch ----------
launch()
let relaunched = false
const a = await waitForUi({
  label: 'first',
  onSetup: async (p) => {
    await p.waitForSelector('#arch')
    await p.waitForFunction(() => document.getElementById('m-arch').innerText.trim().length > 0, { timeout: 20000 })
    await p.screenshot({ path: path.join(shots, 'A-setup-first-run.png') })
    const ui = await p.evaluate(() => ({
      title: document.getElementById('title').innerText, lead: document.getElementById('lead').innerText, label: document.querySelector('label[for=arch]').innerText,
      value: document.getElementById('arch').value, msg: document.getElementById('m-arch').innerText, adv: getComputedStyle(document.getElementById('adv')).display !== 'none',
      buttons: [...document.querySelectorAll('.actions button')].map((b) => b.innerText), win: document.title,
    }))
    info('first-run dialog: ' + JSON.stringify(ui))
    check('A first-run dialog: Chinese title/explanation about the archive (working) folder', /归档文件夹/.test(ui.title) && /归档/.test(ui.lead) && /更改工作目录/.test(ui.lead), ui.title + ' / ' + ui.lead)
    check('A default location shown', same(ui.value, expectedDefault), ui.value)
    check('A default is valid and free space shown', /可用/.test(ui.msg) && /剩余空间/.test(ui.msg), ui.msg)
    check('A buttons 使用默认位置/取消/确定', ['使用默认位置', '取消', '确定'].every((b) => ui.buttons.includes(b)), ui.buttons.join(','))
    check(`A advanced data-directory option ${portable || dataEnv ? 'hidden (data dir fixed)' : 'available'}`, ui.adv === !(portable || dataEnv))
    const cases = [
      ['relative path', 'relative\\path', /绝对路径/],
      ['C:\\Windows', 'C:\\Windows\\OA归档', /系统或程序目录/],
      ['install dir', portable ? path.join(exeDir, 'resources', 'x') : path.join(exeDir, '归档'), /安装目录/],
      ['non-writable dir', path.join(probeRo, 'sub'), /写入权限/],
      ['drive root', 'D:\\', /根目录/],
      ['data dir', path.join(dataDir, 'archive'), /数据目录/],
    ]
    for (const [name, v, re] of cases) {
      const r = await setArch(p, v)
      check(`A validation rejects ${name}`, r.disabled && re.test(r.msg), r.msg.replace(/\n/g, ' '))
      if (name === 'non-writable dir') await p.screenshot({ path: path.join(shots, 'A-setup-invalid.png') })
    }
    const ok = await setArch(p, chosen)
    check('A custom folder accepted (free space shown)', !ok.disabled && /剩余空间/.test(ok.msg), ok.msg)
    if (advData) {
      await p.evaluate(() => (document.getElementById('adv').open = true))
      await p.$eval('#data', (i, v) => { i.value = v; i.dispatchEvent(new Event('input')) }, customData)
      await p.waitForFunction(() => document.getElementById('m-data').innerText.trim().length > 0, { timeout: 15000 })
      const d = await p.evaluate(() => document.getElementById('m-data').innerText)
      check('A advanced: custom data directory accepted', /可用/.test(d), d)
    }
    await p.screenshot({ path: path.join(shots, 'A-setup-valid.png') })
    await p.click('#b-ok')
    if (advData) {
      // the app writes datadir.json and relaunches itself: follow the new process
      relaunched = true
      await until(() => child.exited, 60000)
      browser = null
      insp = null
      await sleep(1500)
    }
  },
})
if (relaunched) {
  dataDir = customData
  electronPid = await until(() => mainPid().catch(() => 0), 60000)
  info('relaunched electron pid ' + electronPid)
  const pointer = JSON.parse(fs.readFileSync(path.join(bootstrap, 'datadir.json'), 'utf8'))
  check('A advanced: datadir.json points at the custom data directory', same(pointer.dataDir, customData), pointer.dataDir)
}
check('A splash shown first', a.splashAt > 0, `${a.splashAt} ms`)
check('A first-run dialog appeared', a.setupSeen)
check('A main window loaded', !!origin, origin)
await hookDialogs()
const cfg = JSON.parse(fs.readFileSync(path.join(dataDir, 'desktop-config.json'), 'utf8'))
check('A choice persisted in desktop-config.json', same(cfg.archiveRoot, chosen), cfg.archiveRoot)
check('A backend uses the chosen folder (GET /v1/admin/system disk.archive_path)', same(await archivePath(), chosen), await archivePath())
check('A data directory in the expected place (pg, logs, electron)', ['pg', 'logs', 'electron'].every((d) => fs.existsSync(path.join(dataDir, d))), dataDir)
const st = (await api('GET', '/v1/setup/status')).json?.data || {}
const feLevel = Number(/REQUIRED_API_LEVEL\s*=\s*(\d+)/.exec(fs.readFileSync(path.join(repo, 'frontend', 'src', 'utils', 'apiLevel.ts'), 'utf8'))[1])
check('A setup status: initialized, wizard_finished, local mode', st.initialized === true && st.wizard_finished === true && st.mode === 'local', JSON.stringify(st).slice(0, 200))
check('A api_level matches the frontend', st.api_level === feLevel, `${st.api_level} vs ${feLevel}`)
{
  const page = await pageBy(/^http:\/\/127\.0\.0\.1/)
  await sleep(2500)
  const ui = await page.evaluate(async () => ({ path: location.pathname, banner: !!document.querySelector('[data-testid=api-level-banner]'), text: document.body.innerText.slice(0, 300), me: await (await fetch('/v1/auth/me')).json() }))
  await page.screenshot({ path: path.join(shots, 'A-main.png') })
  check('A UI opens in local single-user mode: no login, no setup wizard', !/^\/(login|setup)/.test(ui.path) && !/初始化向导|登录/.test(ui.text.slice(0, 80)) && ui.me?.data?.user?.username === 'local', `${ui.path} ${JSON.stringify(ui.me).slice(0, 120)}`)
  check('A ApiLevelBanner not shown', !ui.banner)
}

// ---------- B. archive samples ----------
const pick = ['关于2026年国庆节放假安排的通知.docx', '办公电脑采购合同.pdf', '采购部_2026年Q1设备采购报表.xlsx']
const fd = new FormData()
fd.append('paths', JSON.stringify(pick))
for (const n of pick) fd.append('files', new Blob([fs.readFileSync(path.join(samplesDir, n))]), n)
const imp = await api('POST', '/v1/imports', fd, { 'Idempotency-Key': 'e2e-' + Date.now() })
check('B import accepted', imp.status === 200 || imp.status === 201 || imp.status === 202, `${imp.status} ${imp.buf.toString().slice(0, 200)}`)
const taskId = imp.json?.data?.id
const task = await until(async () => {
  const t = (await api('GET', '/v1/tasks/' + taskId)).json?.data
  return t && ['awaiting_review', 'failed', 'cancelled', 'completed', 'partial_failure'].includes(t.status) ? t : null
}, 600000, 1500)
check('B task reached review', task && task.status === 'awaiting_review' && task.plan_id, JSON.stringify(task).slice(0, 200))
let plan = (await api('GET', '/v1/archive-plans/' + task.plan_id)).json.data
const fix = plan.items.filter((i) => i.review_required).map((i) => ({ file_id: i.file_id, department: i.department || '行政部', document_type: i.document_type || '通知', document_date: i.document_date || '2026-01-01' }))
if (fix.length) plan = (await api('PATCH', '/v1/archive-plans/' + plan.id, { version: plan.version, items: fix })).json.data
const ap = await api('POST', `/v1/archive-plans/${plan.id}/approve`, { version: plan.version })
check('B plan approved', ap.status === 200, ap.status)
const ex = await api('POST', `/v1/archive-plans/${plan.id}/execute`, null, { 'Idempotency-Key': 'e2e-exec-' + plan.id })
check('B plan executed', ex.status === 200 && ex.json?.data?.status === 'executed', `${ex.status} ${ex.json?.data?.status}`)
await until(async () => ['completed', 'partial_failure'].includes((await api('GET', '/v1/tasks/' + taskId)).json?.data?.status), 120000, 1000)
const ledger = (await api('GET', '/v1/ledger?page_size=200')).json.data.items
info('ledger: ' + ledger.map((l) => l.archive_path).join(' | '))
check('B ledger has the archived files', ledger.length === pick.length, ledger.length)
const inChosen = ledger.every((l) => {
  const f = path.join(chosen, l.archive_path)
  return fs.existsSync(f) && sha(fs.readFileSync(f)) === l.sha256
})
check('B archived files exist inside the chosen folder (sha256 matches the ledger)', inChosen)
function findByName(root, names, skip = []) {
  const hits = []
  if (!fs.existsSync(root)) return hits
  const stack = [root]
  while (stack.length) {
    const d = stack.pop()
    let ents = []
    try { ents = fs.readdirSync(d, { withFileTypes: true }) } catch { continue }
    for (const e of ents) {
      const p = path.join(d, e.name)
      if (skip.some((s) => same(s, p))) continue
      if (e.isDirectory()) stack.push(p)
      else if (names.has(e.name)) hits.push(p)
    }
  }
  return hits
}
const names = new Set(ledger.map((l) => path.basename(l.archive_path)))
const elsewhere = [...findByName(work, names, [chosen]), ...findByName(dataDir, names), ...findByName(exeDir, names, [path.join(exeDir, 'data')]), ...findByName(path.join(docs, 'OA-Agent'), names)]
check('B archived files exist nowhere else (scratch, data dir, program dir, Documents\\OA-Agent)', elsewhere.length === 0, elsewhere.join(' | '))
const dl = await api('GET', '/v1/fs/content?download=1&path=' + encodeURIComponent(ledger[0].archive_path))
check('B archived file downloadable through the API', dl.status === 200 && sha(dl.buf) === ledger[0].sha256, dl.status)

async function filesAccessible(root, label) {
  const ap2 = await archivePath()
  let ok = same(ap2, root)
  const errs = []
  for (const l of ledger) {
    const r = await api('GET', '/v1/fs/content?download=1&path=' + encodeURIComponent(l.archive_path))
    if (r.status !== 200 || sha(r.buf) !== l.sha256) { ok = false; errs.push(`${l.archive_path}:${r.status}`) }
  }
  const pv = await api('GET', `/v1/fs/preview?path=${encodeURIComponent(ledger.find((l) => l.archive_path.endsWith('.pdf'))?.archive_path || ledger[0].archive_path)}`)
  check(`${label}: backend uses ${path.basename(root)}; all archived files download intact through the API`, ok, `${ap2} ${errs.join(' ')}`)
  check(`${label}: preview endpoint answers`, pv.status === 200, pv.status)
}

// ---------- C. graceful quit + second launch ----------
await quitApp('C first quit')
launch()
const c = await waitForUi({ label: 'second', onSetup: async (p) => { await p.screenshot({ path: path.join(shots, 'C-unexpected-setup.png') }) } })
check('C second launch: no first-run prompt', !c.setupSeen)
await hookDialogs()
await filesAccessible(chosen, 'C second launch')

// ---------- D. 文件 → 更改工作目录… ----------
async function openChange() {
  await clearLog()
  check('D menu item 文件 → 更改工作目录… exists', await clickMenu('更改工作目录…'))
  const p = await pageBy(/setup\.html/)
  await p.waitForSelector('#arch')
  await sleep(500)
  return p
}
async function waitDialog(re, ms = 300000) {
  return until(async () => (await dialogLog()).find((d) => re.test(d.message || '')), ms, 500)
}
// D1 cancel in the folder dialog
{
  const p = await openChange()
  const ui = await p.evaluate(() => ({ title: document.getElementById('title').innerText, lead: document.getElementById('lead').innerText, adv: getComputedStyle(document.getElementById('adv')).display !== 'none' }))
  await p.screenshot({ path: path.join(shots, 'D-change-dialog.png') })
  check('D change dialog shows the current folder', /更改工作目录/.test(ui.title) && ui.lead.includes(chosen), ui.lead)
  await p.click('#b-cancel')
  await until(async () => !(await targets()).some((t) => /setup\.html/.test(t.url)), 10000)
  check('D1 cancel: folder unchanged', same(await archivePath(), chosen))
}
const migTarget = path.join(work, '迁移目标')
// D2 cancel at the confirmation box
{
  const p = await openChange()
  const r = await setArch(p, migTarget)
  const modes = await p.evaluate(() => getComputedStyle(document.getElementById('modes')).display)
  await p.screenshot({ path: path.join(shots, 'D-change-modes.png') })
  check('D change dialog offers 迁移 / 直接使用新位置 for a valid new folder', !r.disabled && modes === 'block')
  await queueReplies([1])
  await p.click('#b-ok')
  const conf = await waitDialog(/迁移工作目录/, 20000)
  check('D2 migrate confirmation explains copy + verification + old folder kept', conf && /逐文件校验/.test(conf.detail) && /旧文件夹会保留/.test(conf.detail), conf && conf.detail)
  await sleep(1500)
  check('D2 cancel at confirmation: folder unchanged, nothing copied', same(await archivePath(), chosen) && !fs.existsSync(migTarget))
}
// D3 migrate
{
  const p = await openChange()
  await setArch(p, migTarget)
  await queueReplies([0, 0]) // 开始迁移, 保留旧文件夹
  await p.click('#b-ok')
  const done = await waitDialog(/迁移完成|迁移失败|启动失败/)
  check('D3 migrate finished', done && /迁移完成/.test(done.message), done && done.message + ' ' + done.detail)
  await refreshOrigin()
  const copiedOk = ledger.every((l) => fs.existsSync(path.join(migTarget, l.archive_path)) && sha(fs.readFileSync(path.join(migTarget, l.archive_path))) === l.sha256)
  const oldKept = ledger.every((l) => fs.existsSync(path.join(chosen, l.archive_path)))
  check('D3 files copied and verified in the new folder; old folder kept', copiedOk && oldKept)
  check('D3 config switched', same(JSON.parse(fs.readFileSync(path.join(dataDir, 'desktop-config.json'), 'utf8')).archiveRoot, migTarget))
  await filesAccessible(migTarget, 'D3 after migrate')
}
// D4 use an existing location (back to the original folder, which still holds the files)
{
  const p = await openChange()
  await setArch(p, chosen)
  await p.$eval('input[name=mode][value=use]', (el) => el.click())
  await queueReplies([0])
  await p.click('#b-ok')
  const done = await waitDialog(/工作目录已切换为|启动失败/)
  check('D4 use existing location: switched', done && /工作目录已切换为/.test(done.message), done && done.message)
  await refreshOrigin()
  await filesAccessible(chosen, 'D4 after use-existing')
}
// D5 forced failure: the target becomes unwritable between the confirmation and the copy -> stays on the old folder
{
  const failCopy = path.join(work, 'fail-copy')
  const p = await openChange()
  await setArch(p, failCopy)
  const user = process.env.USERNAME
  await queueReplies([`const cp = req('child_process'); const fs = req('fs'); fs.mkdirSync(${JSON.stringify(failCopy)}, { recursive: true }); cp.execFileSync('icacls', [${JSON.stringify(failCopy)}, '/deny', ${JSON.stringify(user + ':(OI)(CI)(W,AD,WD)')}]); return 0`])
  await p.click('#b-ok')
  const done = await waitDialog(/迁移完成|迁移失败|启动失败/)
  check('D5 copy failure reported, previous folder kept', done && /迁移失败，仍在使用原目录/.test(done.message), done && done.message + ' | ' + done.detail)
  await refreshOrigin()
  check('D5 config still points at the previous folder', same(JSON.parse(fs.readFileSync(path.join(dataDir, 'desktop-config.json'), 'utf8')).archiveRoot, chosen))
  await filesAccessible(chosen, 'D5 after failed migrate')
  aclReset(failCopy)
}
// D6 forced failure: the backend cannot start with the new folder -> rolled back and restarted on the old one
{
  const failStart = path.join(work, 'fail-start')
  fs.mkdirSync(failStart, { recursive: true })
  const target = path.join(failStart, 'sub')
  const p = await openChange()
  await setArch(p, target)
  await p.$eval('input[name=mode][value=use]', (el) => el.click())
  const user = process.env.USERNAME
  await queueReplies([`req('child_process').execFileSync('icacls', [${JSON.stringify(failStart)}, '/deny', ${JSON.stringify(user + ':(OI)(CI)(W,AD,WD)')}]); return 0`])
  await p.click('#b-ok')
  const done = await waitDialog(/工作目录已切换为|启动失败|迁移失败/)
  check('D6 restart failure reported and rolled back', done && /新目录启动失败，已恢复原目录/.test(done.message), done && done.message + ' | ' + done.detail)
  await refreshOrigin()
  check('D6 config restored to the previous folder', same(JSON.parse(fs.readFileSync(path.join(dataDir, 'desktop-config.json'), 'utf8')).archiveRoot, chosen))
  await filesAccessible(chosen, 'D6 after rollback')
  aclReset(failStart)
}
const dl6 = await dialogLog()
info('dialogs in D6: ' + JSON.stringify(dl6.map((d) => d.message)))

// ---------- E. quit, hard kill, relaunch ----------
await quitApp('E graceful quit')
launch()
await waitForUi({ label: 'pre-kill' })
await hookDialogs()
const pid = electronPid
const beforeKill = procsUnder(serverRoot)
execFileSync('taskkill', ['/F', '/T', '/PID', String(pid)], { stdio: 'ignore' })
insp = null
browser = null
await until(() => !pidAlive(pid), 20000)
await sleep(3000)
const leftover = procsUnder(serverRoot)
info('before kill: ' + beforeKill.join(' | '))
check('E hard kill: no helper process survives the Electron tree (Job Object)', leftover.length === 0, leftover.join(' | '))
const lockAfterKill = fs.existsSync(path.join(dataDir, 'logs', 'run.lock'))
info('run.lock present after hard kill: ' + lockAfterKill)
launch()
const e = await waitForUi({ label: 'after-kill' })
check('E relaunch after hard kill: UI loads without prompting', !!origin && !e.setupSeen)
await filesAccessible(chosen, 'E after hard kill')
const sys = (await api('GET', '/v1/admin/system')).json?.data || {}
const reps = (await api('GET', '/v1/admin/error-reports?source=supervisor')).json?.data
info('system: last_exit_unclean=' + sys.last_exit_unclean + ' reports=' + JSON.stringify(reps).slice(0, 300))
check('E diagnostics show the unclean previous exit (last_exit_unclean)', sys.last_exit_unclean === true, `last_exit_unclean=${sys.last_exit_unclean}, run.lock after kill=${lockAfterKill}`)
const led2 = (await api('GET', '/v1/ledger?page_size=200')).json.data.items
check('E no data loss: ledger intact', led2.length === ledger.length, led2.length)
await quitApp('E final quit')

const failed = results.filter((r) => !r.ok)
console.log(`\n${results.length - failed.length}/${results.length} passed`)
for (const f of failed) console.log('FAILED: ' + f.name)
for (const p of [probeRo]) aclReset(p)
process.exit(failed.length ? 1 : 0)
