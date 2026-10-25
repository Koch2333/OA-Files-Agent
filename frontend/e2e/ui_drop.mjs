// Drag-and-drop regression (real Chromium/Edge, real drag events via CDP Input.dispatchDragEvent with real file paths):
// single file, several files, a folder, drops outside the zone (must not navigate), the drag-over state and the toast.
//   VITE_USE_MOCK=1 npx vite --port 5197   then   UI_BASE=http://127.0.0.1:5197 node e2e/ui_drop.mjs
import puppeteer from 'puppeteer-core'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { createRunner, sleep } from './lib/common.mjs'

const base = process.env.UI_BASE ?? 'http://127.0.0.1:5197'
const exe = process.env.UI_BROWSER ?? 'C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe'
const run = createRunner('drop')
const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'oa-drop-'))
const mk = (rel, body = 'hello') => { const p = path.join(tmp, rel); fs.mkdirSync(path.dirname(p), { recursive: true }); fs.writeFileSync(p, body); return p }
const a = mk('a.docx'), b = mk('b.pdf'), x = mk('note.exe')
mk('合同/c.xlsx'); mk('合同/2024/d.pdf')
const folder = path.join(tmp, '合同')

const browser = await puppeteer.launch({ executablePath: exe, headless: 'new', args: ['--no-sandbox'] })
const page = await browser.newPage()
await page.setViewport({ width: 1280, height: 900 })
const errs = []
page.on('pageerror', (e) => errs.push(String(e)))
const cdp = await page.createCDPSession()
await page.goto(base + '/', { waitUntil: 'networkidle0' })
let navigated = false
page.on('framenavigated', (f) => { if (f === page.mainFrame() && !f.url().startsWith(base)) navigated = true })

async function drag(files, at, { release = true } = {}) {
  const data = { items: [], files, dragOperationsMask: 1 }
  await cdp.send('Input.dispatchDragEvent', { type: 'dragEnter', x: at.x, y: at.y, data })
  await cdp.send('Input.dispatchDragEvent', { type: 'dragOver', x: at.x, y: at.y, data })
  if (release) await cdp.send('Input.dispatchDragEvent', { type: 'drop', x: at.x, y: at.y, data })
}
const text = (sel) => page.$eval(sel, (el) => el.textContent.trim()).catch(() => null)
const zoneCentre = async () => { const r = await (await page.$('[data-testid="home-dropzone"]')).boundingBox(); return { x: r.x + r.width / 2, y: r.y + r.height / 2 } }
const reset = async () => { await page.goto(base + '/', { waitUntil: 'networkidle0' }); await page.waitForSelector('[data-testid="home-dropzone"]') }

// 1. drag-over state
await page.waitForSelector('[data-testid="home-dropzone"]')
await drag([a], await zoneCentre(), { release: false }); await sleep(200)
run.check('drag-over shows overlay', !!(await page.$('[data-testid="drop-overlay"]')))
run.check('drag-over label "松开即可上传"', (await text('[data-testid="drop-overlay"]'))?.includes('松开即可上传') === true)
await page.screenshot({ path: path.join(run.shots, 'drop-dragover.png') })
await cdp.send('Input.dispatchDragEvent', { type: 'dragCancel', x: 5, y: 5, data: { items: [], files: [a], dragOperationsMask: 1 } }).catch(() => {})
await reset()

// 2. single file
await drag([a], await zoneCentre()); await sleep(500)
run.check('single file: confirm bar with 1 file', (await text('[data-testid="dz-brief"]'))?.startsWith('1 个文件') === true, String(await text('[data-testid="dz-brief"]')))
run.check('overlay gone after drop', !(await page.$('[data-testid="drop-overlay"]')))
await reset()

// 3. several files
await drag([a, b], await zoneCentre()); await sleep(500)
run.check('two files', (await text('[data-testid="dz-brief"]'))?.startsWith('2 个文件') === true, String(await text('[data-testid="dz-brief"]')))
await reset()

// 4. folder (entries walked recursively)
await drag([folder], await zoneCentre()); await sleep(800)
run.check('folder: 2 files found recursively', (await text('[data-testid="dz-brief"]'))?.startsWith('2 个文件') === true, String(await text('[data-testid="dz-brief"]')))
await reset()

// 5. drop OUTSIDE the zone (page header area): accepted page-wide, and never navigates away
await drag([a], { x: 640, y: 30 }); await sleep(500)
run.check('drop on page header still adds the file', (await text('[data-testid="dz-brief"]'))?.startsWith('1 个文件') === true, String(await text('[data-testid="dz-brief"]')))
run.check('no navigation away', !navigated && page.url().startsWith(base))
await reset()

// 6. unsupported type: visible toast, not silent
await drag([x], await zoneCentre()); await sleep(500)
const toasts = await page.$$eval('[role="status"], .toast, [data-testid="toast"]', (n) => n.map((e) => e.textContent)).catch(() => [])
run.check('unsupported file gives a visible hint', toasts.some((t) => /格式不支持/.test(t)) || /格式不支持|将被拒绝/.test((await page.evaluate(() => document.body.innerText))), JSON.stringify(toasts))

// 7. import page (FilePicker) is page-wide too
await page.goto(base + '/import', { waitUntil: 'networkidle0' })
await drag([a, b], { x: 700, y: 500 }); await sleep(500)
run.check('import page: drop adds files', (await page.evaluate(() => document.body.innerText)).includes('2 个文件'))
run.check('import page: no navigation', !navigated && page.url().startsWith(base))

await browser.close(); fs.rmSync(tmp, { recursive: true, force: true })
run.finish(errs)
