// Drag-and-drop in the REAL Electron window (packaged/unpacked exe), driven over CDP (puppeteer-core, Input.dispatchDragEvent with
// real file paths). Regression for "nothing happens when a file is dragged onto the import zone".
//   DROP_EXE         OA-Agent-Desktop.exe (required)          DROP_WORK  scratch dir (required, wiped)
//   DROP_SERVER_DIR  backend bundle dir override (e.g. a copy whose web/ has a freshly built frontend)
//   DROP_PORT        remote debugging port (default 19350)    DROP_EXPECT  'fixed' (default) or 'broken' (documents the old behaviour)
import { spawn, spawnSync } from 'node:child_process'
import fs from 'node:fs'
import path from 'node:path'
import { createRequire } from 'node:module'
import { fileURLToPath } from 'node:url'

const require = createRequire(import.meta.url)
const here = path.dirname(fileURLToPath(import.meta.url))
const puppeteer = require(path.resolve(here, '..', '..', 'frontend', 'node_modules', 'puppeteer-core'))
const exe = process.env.DROP_EXE
if (!exe || !process.env.DROP_WORK) throw new Error('DROP_EXE and DROP_WORK are required')
const work = path.resolve(process.env.DROP_WORK)
const port = Number(process.env.DROP_PORT || 19350)
const expectFixed = (process.env.DROP_EXPECT || 'fixed') === 'fixed'
fs.rmSync(work, { recursive: true, force: true })
fs.mkdirSync(path.join(work, 'files', '合同', '2024'), { recursive: true })
const f = (rel) => { const p = path.join(work, 'files', rel); fs.writeFileSync(p, 'hello'); return p }
const a = f('a.docx'), b = f('b.pdf'); f('合同/c.xlsx'); f('合同/2024/d.pdf')
const folder = path.join(work, 'files', '合同')

let failed = 0
const check = (n, ok, x = '') => { console.log(`${ok ? 'PASS' : 'FAIL'}  ${n} ${x}`); if (!ok) failed++ }
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
const env = { ...process.env, OA_AGENT_BOOTSTRAP_DIR: path.join(work, 'bootstrap'), OA_AGENT_DATA_DIR: path.join(work, 'data'), OA_AGENT_ARCHIVE_ROOT: path.join(work, 'archive') }
fs.mkdirSync(path.join(work, 'tmp'), { recursive: true })
env.TEMP = env.TMP = path.join(work, 'tmp') // isolated temp: never touch the user's profile temp
if (process.env.DROP_SERVER_DIR) env.OA_AGENT_SERVER_DIR = process.env.DROP_SERVER_DIR
const child = spawn(exe, [`--remote-debugging-port=${port}`], { env, stdio: 'ignore' })
let browser, page
try {
  const end = Date.now() + 240000
  while (Date.now() < end && !page) {
    try {
      browser = browser || (await puppeteer.connect({ browserURL: `http://127.0.0.1:${port}`, defaultViewport: null }))
      page = (await browser.pages()).find((x) => /^http:\/\/127\.0\.0\.1:\d+\//.test(x.url()))
    } catch (e) { if (!globalThis.__w) { globalThis.__w = 1; console.log('connect:', String(e.message).slice(0, 100)) } }
    if (!page) await sleep(500)
  }
  check('main window loaded from 127.0.0.1', !!page, page?.url())
  await page.waitForSelector('[data-testid="home-dropzone"]', { timeout: 60000 })
  await sleep(1000)
  const origin = new URL(page.url()).origin
  let navigated = false
  page.on('framenavigated', (fr) => { if (fr === page.mainFrame() && !fr.url().startsWith(origin)) navigated = true })
  const cdp = await page.createCDPSession()
  const drag = async (files, at, release = true) => {
    const data = { items: [], files, dragOperationsMask: 1 }
    for (const type of release ? ['dragEnter', 'dragOver', 'drop'] : ['dragEnter', 'dragOver']) await cdp.send('Input.dispatchDragEvent', { type, x: at.x, y: at.y, data })
  }
  const centre = async () => { const r = await (await page.$('[data-testid="home-dropzone"]')).boundingBox(); return { x: r.x + r.width / 2, y: r.y + r.height / 2 } }
  const brief = () => page.$eval('[data-testid="dz-brief"]', (e) => e.textContent.trim()).catch(() => null)
  const reload = async () => { await page.reload({ waitUntil: 'networkidle0' }); await page.waitForSelector('[data-testid="home-dropzone"]') }

  if (!expectFixed) {
    await drag([a], await centre()); await sleep(800)
    check('OLD build: single file drop does nothing (bug reproduced)', (await brief()) === null)
  } else {
    await drag([a], await centre(), false); await sleep(300)
    check('drag-over overlay + label', (await page.$eval('[data-testid="drop-overlay"]', (e) => e.textContent).catch(() => '')).includes('松开即可上传'))
    await page.screenshot({ path: path.join(work, 'dragover.png') })
    await reload()
    await drag([a], await centre()); await sleep(800)
    check('single file accepted', (await brief())?.startsWith('1 个文件') === true, String(await brief()))
    await reload(); await drag([a, b], await centre()); await sleep(800)
    check('two files accepted', (await brief())?.startsWith('2 个文件') === true, String(await brief()))
    await reload(); await drag([folder], await centre()); await sleep(1200)
    check('folder walked recursively', (await brief())?.startsWith('2 个文件') === true, String(await brief()))
    await reload(); await drag([a], { x: 600, y: 20 }); await sleep(800)
    check('drop outside the zone is accepted too', (await brief())?.startsWith('1 个文件') === true, String(await brief()))
    check('window did not navigate away', !navigated && page.url().startsWith(origin))
    const info = await page.evaluate(async () => ({ elevated: await window.oaDesktop?.elevated?.() }))
    // shells built before the elevation bridge existed have no window.oaDesktop.elevated: only assert when present
    if (info.elevated === undefined) console.log('SKIP  elevation bridge (this build predates it)')
    else check('bridge reports elevation (false when started normally)', info.elevated === false, JSON.stringify(info))
  }
} catch (e) {
  check('no exception', false, String(e && e.stack || e))
} finally {
  try { await browser?.disconnect() } catch { /* ignore */ }
  try { spawnSync('taskkill', ['/PID', String(child.pid), '/T', '/F']) } catch { /* ignore */ }
}
console.log(failed ? `\n${failed} check(s) FAILED` : '\nall checks passed')
// graceful quit of the app we started: ask the main process tree to end; the harness only kills the PID it spawned
process.exit(failed ? 1 : 0)
