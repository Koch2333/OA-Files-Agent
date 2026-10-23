// End-to-end smoke test of the desktop shell with puppeteer-core over CDP (Windows, run with node).
//   SMOKE_EXE      Electron/packaged exe (required)         SMOKE_APP   app dir to pass as first argument (dev run only)
//   SMOKE_WORK     scratch directory (required, wiped)      SMOKE_SERVER_DIR  override backend bundle dir (optional)
//   SMOKE_PORT     remote debugging port (default 19333)     SMOKE_SHOTS screenshot dir (default <work>/shots)
// Asserts: splash appears before the main window, main window loads the UI from 127.0.0.1, splash disappears after,
// data dir layout, a second launch keeps a single instance, quitting leaves no oa-agent/postgres/worker processes.
import { spawn, execFileSync } from 'node:child_process'
import fs from 'node:fs'
import path from 'node:path'
import { createRequire } from 'node:module'
import { fileURLToPath } from 'node:url'

const require = createRequire(import.meta.url)
const here = path.dirname(fileURLToPath(import.meta.url))
const puppeteer = require(path.resolve(here, '..', '..', 'frontend', 'node_modules', 'puppeteer-core'))

const exe = process.env.SMOKE_EXE
const work = path.resolve(process.env.SMOKE_WORK || '')
if (!exe || !process.env.SMOKE_WORK) throw new Error('SMOKE_EXE and SMOKE_WORK are required')
const port = Number(process.env.SMOKE_PORT || 19333)
const shots = process.env.SMOKE_SHOTS || path.join(work, 'shots')
const portable = process.env.SMOKE_PORTABLE === '1'
const firstRun = process.env.SMOKE_FIRSTRUN === '1'
const dataDir = portable ? path.join(path.dirname(exe), 'data') : path.join(work, 'data')
const archive = path.join(work, 'archive')
fs.rmSync(work, { recursive: true, force: true })
fs.mkdirSync(shots, { recursive: true })

const results = []
const check = (name, ok, extra = '') => {
  results.push({ name, ok })
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${extra ? '  ' + extra : ''}`)
}
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
const env = { ...process.env, OA_AGENT_BOOTSTRAP_DIR: path.join(work, 'bootstrap') }
if (!portable) env.OA_AGENT_DATA_DIR = dataDir
if (!firstRun) env.OA_AGENT_ARCHIVE_ROOT = archive
if (process.env.SMOKE_SERVER_DIR) env.OA_AGENT_SERVER_DIR = process.env.SMOKE_SERVER_DIR
const args = [...(process.env.SMOKE_APP ? [process.env.SMOKE_APP] : []), `--remote-debugging-port=${port}`]

async function targets() {
  try {
    const r = await fetch(`http://127.0.0.1:${port}/json/list`)
    return (await r.json()).filter((t) => t.type === 'page')
  } catch {
    return null
  }
}

function procsUnder(dir) {
  const ps = `Get-CimInstance Win32_Process | Where-Object { $_.ExecutablePath -and $_.ExecutablePath -like '${dir.replace(/'/g, "''")}*' } | ForEach-Object { "$($_.ProcessId) $($_.Name)" }`
  return execFileSync('powershell.exe', ['-NoProfile', '-Command', ps], { encoding: 'utf8' }).split(/\r?\n/).filter(Boolean)
}
const serverRoot = process.env.SMOKE_SERVER_DIR || path.join(path.dirname(exe), 'resources', 'app-server')

const t0 = Date.now()
const child = spawn(exe, args, { env, stdio: 'ignore', windowsHide: false })
let exited = false
child.on('exit', () => (exited = true))

// 1. timeline of page targets
const timeline = []
let splashSeenAt = 0
let mainSeenAt = 0
let splashGoneAt = 0
let mainUrl = ''
let browser = null
let splashShot = false
let setupDone = false
const deadline = Date.now() + 300000
while (Date.now() < deadline && !exited && !splashGoneAt) {
  const ts = await targets()
  if (ts) {
    const setup = ts.find((t) => /setup\.html/.test(t.url))
    if (setup && firstRun && !setupDone) {
      setupDone = true
      browser = browser || (await puppeteer.connect({ browserURL: `http://127.0.0.1:${port}`, defaultViewport: null }))
      let sp
      for (let i = 0; i < 40 && !sp; i++) {
        sp = (await browser.pages()).find((x) => { try { return /setup\.html/.test(x.url()) } catch { return false } })
        if (!sp) await sleep(250)
      }
      await sp.waitForSelector('#arch')
      await sleep(800)
      await sp.screenshot({ path: path.join(shots, 'setup-first-run.png') })
      const bad = await sp.evaluate(() => { const i = document.getElementById('arch'); i.value = 'relative\\path'; i.dispatchEvent(new Event('input')); return true })
      await sleep(700)
      const okDisabled = await sp.$eval('#b-ok', (b) => b.disabled)
      check('first-run dialog rejects a relative path', okDisabled)
      await sp.$eval('#arch', (i, v) => { i.value = v; i.dispatchEvent(new Event('input')) }, archive)
      await sp.waitForFunction(() => !document.getElementById('b-ok').disabled, { timeout: 8000 })
      await sp.screenshot({ path: path.join(shots, 'setup-first-run-valid.png') })
      await sp.click('#b-ok')
      timeline.push('first-run folder chosen')
    }
    const splash = ts.find((t) => /splash\.html/.test(t.url))
    const main = ts.find((t) => /^http:\/\/127\.0\.0\.1:\d+/.test(t.url))
    if (splash && !splashSeenAt) {
      splashSeenAt = Date.now() - t0
      timeline.push(`splash seen @${splashSeenAt}ms`)
    }
    if (main && !mainSeenAt) {
      mainSeenAt = Date.now() - t0
      mainUrl = main.url
      timeline.push(`main seen @${mainSeenAt}ms ${main.url}`)
    }
    if (splash && !splashShot) {
      splashShot = true
      try {
        browser = browser || (await puppeteer.connect({ browserURL: `http://127.0.0.1:${port}`, defaultViewport: null }))
        await sleep(1200)
        const p = (await browser.pages()).find((x) => /splash\.html/.test(x.url()))
        if (p) await p.screenshot({ path: path.join(shots, 'splash-1.png') })
      } catch (e) {
        console.log('splash screenshot failed:', e.message)
      }
    }
    if (splash && mainSeenAt === 0 && !fs.existsSync(path.join(shots, 'splash-2.png')) && Date.now() - t0 > 8000) {
      try {
        const p = (await browser.pages()).find((x) => /splash\.html/.test(x.url()))
        if (p) await p.screenshot({ path: path.join(shots, 'splash-2.png') })
      } catch {}
    }
    if (splashSeenAt && mainSeenAt && !splash) {
      splashGoneAt = Date.now() - t0
      timeline.push(`splash gone @${splashGoneAt}ms`)
    }
  }
  await sleep(40)
}
console.log(timeline.join('\n'))
check('splash window appeared', splashSeenAt > 0, `${splashSeenAt}ms after spawn`)
check('splash appeared before the main window', splashSeenAt > 0 && (mainSeenAt === 0 || splashSeenAt <= mainSeenAt))
check('main window loads the UI from 127.0.0.1', /^http:\/\/127\.0\.0\.1:\d+/.test(mainUrl), mainUrl)
check('splash closed after the main window loaded', splashGoneAt > 0)

if (mainUrl) {
  browser = browser || (await puppeteer.connect({ browserURL: `http://127.0.0.1:${port}`, defaultViewport: null }))
  const page = (await browser.pages()).find((x) => /^http:\/\/127\.0\.0\.1/.test(x.url()))
  await sleep(2500)
  const info = await page.evaluate(() => ({ title: document.title, text: document.body.innerText.slice(0, 200), nodeLeak: typeof require !== 'undefined' || typeof process !== 'undefined' }))
  console.log('UI:', JSON.stringify(info))
  check('UI rendered content', info.text.length > 0 || info.title.length > 0)
  check('renderer has no Node access', !info.nodeLeak)
  await page.screenshot({ path: path.join(shots, 'main.png') })
  // individual edition: the shell completes the backend setup in local mode, so the UI is already signed in and never shows login/setup
  const who = await page.evaluate(async () => {
    const r = await fetch('/v1/auth/me')
    return { status: r.status, body: r.ok ? await r.json() : null, path: location.pathname }
  })
  check('local mode: session is the built-in local user, no login/setup page', who.status === 200 && who.body?.data?.user?.username === 'local' && !/^\/(login|setup)/.test(who.path), JSON.stringify(who).slice(0, 160))
  const cspBlocks = await page.evaluate(
    () =>
      new Promise((resolve) => {
        document.addEventListener('securitypolicyviolation', () => resolve(true), { once: true })
        const s = document.createElement('script')
        s.textContent = 'window.__inline_ran = true'
        document.head.appendChild(s)
        setTimeout(() => resolve(!!window.__inline_ran === false), 700)
      }),
  )
  check('CSP active (inline script blocked)', cspBlocks)
}
check('data dir created (pg/logs/electron)', ['pg', 'logs', 'electron'].every((d) => fs.existsSync(path.join(dataDir, d))), dataDir)
check('archive dir created', fs.existsSync(archive))
if (firstRun) check('first-run choice persisted in desktop-config.json', JSON.parse(fs.readFileSync(path.join(dataDir, 'desktop-config.json'), 'utf8')).archiveRoot.toLowerCase() === archive.toLowerCase())
check('desktop.log written', fs.existsSync(path.join(dataDir, 'logs', 'desktop.log')))

// 2. single instance
const second = spawn(exe, args, { env, stdio: 'ignore' })
const secondExit = await Promise.race([new Promise((r) => second.on('exit', (c) => r(c))), sleep(20000).then(() => 'timeout')])
check('second launch exits immediately (single instance)', secondExit === 0, `exit=${secondExit}`)
check('first instance still running', !exited)

// 3. graceful quit
const before = procsUnder(serverRoot)
console.log('processes under server dir before quit:', before.join(' | '))
check('backend + postgres + worker were running', before.some((l) => /oa-agent\.exe/i.test(l)) && before.some((l) => /postgres\.exe/i.test(l)) && before.some((l) => /document-worker\.exe/i.test(l)))
const tq = Date.now()
try {
  await browser.close()
} catch {}
for (let i = 0; i < 1300 && !exited; i++) await sleep(100)
check('app exited after quit', exited, `${Date.now() - tq}ms`)
await sleep(1500)
const after = procsUnder(serverRoot)
check('no orphan oa-agent/postgres/worker processes', after.length === 0, after.join(' | '))
if (!exited) child.kill()
const failed = results.filter((r) => !r.ok).length
console.log(failed ? `${failed} check(s) FAILED` : 'ALL CHECKS PASSED')
process.exit(failed ? 1 : 0)
