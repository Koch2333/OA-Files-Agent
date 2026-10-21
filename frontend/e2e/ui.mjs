// Browser E2E against a RUNNING backend that serves the built frontend (OA_FRONTEND_DIR=dist).
//   node e2e/ui.mjs http://127.0.0.1:8080 "D:\path\to\tests\samples" "C:\path\to\msedge.exe"
// Drives the real UI: import → agent progress → plan → approve → execute → ledger search → Excel download → template → theme.
import puppeteer from 'puppeteer-core'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

const argv = process.argv.slice(2)
const base = process.env.UI_BASE ?? argv[0] ?? 'http://127.0.0.1:8080'
const samples = process.env.UI_SAMPLES ?? argv[1] ?? '../tests/samples'
const exe = process.env.UI_BROWSER ?? argv[2] ?? 'C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe'
const shots = path.resolve('e2e/screenshots')
fs.mkdirSync(shots, { recursive: true })
const dl = fs.mkdtempSync(path.join(os.tmpdir(), 'oa-dl-'))
let failed = 0
const check = (name, ok, d = '') => { console.log(`${ok ? 'PASS' : 'FAIL'}  ${name} ${ok ? '' : d}`); if (!ok) failed++ }
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

const browser = await puppeteer.launch({ executablePath: exe, headless: 'new', args: ['--no-sandbox'], defaultViewport: { width: 1440, height: 900 } })
const page = await browser.newPage()
const errors = []
page.on('pageerror', (e) => errors.push(String(e)))
page.on('console', (m) => { if (m.type() === 'error') errors.push(m.text()) })
const cdp = await page.createCDPSession()
await cdp.send('Browser.setDownloadBehavior', { behavior: 'allow', downloadPath: dl, eventsEnabled: true }).catch(() => cdp.send('Page.setDownloadBehavior', { behavior: 'allow', downloadPath: dl }))

async function clickText(sel, text, timeout = 15000) {
  const end = Date.now() + timeout
  while (Date.now() < end) {
    const ok = await page.evaluate((sel, text) => {
      const el = [...document.querySelectorAll(sel)].find((e) => e.textContent.replace(/\s+/g, '').includes(text.replace(/\s+/g, '')) && !e.disabled && e.getAttribute('aria-disabled') !== 'true')
      if (el) { el.click(); return true }
      return false
    }, sel, text)
    if (ok) return true
    await sleep(200)
  }
  return false
}
async function waitText(text, timeout = 60000) {
  try { await page.waitForFunction((t) => document.body.innerText.replace(/\s+/g, '').includes(t.replace(/\s+/g, '')), { timeout }, text); return true } catch { return false }
}
const shot = (n) => page.screenshot({ path: path.join(shots, n + '.png') })

// 1. Dashboard
await page.goto(base + '/', { waitUntil: 'networkidle0' })
check('dashboard renders (no mock banner in real mode)', (await page.title()).includes('工作台') && !(await page.evaluate(() => document.body.innerText.includes('演示数据模式'))))
await shot('01-dashboard')

// 2. Import with real files
await page.goto(base + '/import', { waitUntil: 'networkidle0' })
const names = fs.readdirSync(samples).filter((f) => !f.endsWith('.json'))
const input = await page.$('input[type=file][multiple]')
await input.uploadFile(...names.map((n) => path.join(samples, n)))
check('file list shows picked files', await waitText(`${names.length} 个文件`), names.length)
await shot('02-import-picked')
check('start button clickable', await clickText('button', '开始智能归档'))
check('navigates to task detail', await page.waitForFunction(() => /\/tasks\/[0-9a-f-]{36}/.test(location.pathname), { timeout: 20000 }).then(() => true, () => false))
check('agent finished → approval button appears', await waitText('预览并审批归档计划', 120000))
await shot('03-task-detail')
check('trace shows live stream state', await page.evaluate(() => /实时|已断开|重连中/.test(document.body.innerText)))
check('failed files surfaced as parse errors', await page.evaluate(() => document.body.innerText.includes('解析失败')))

// 3. Plan → approve → execute
await clickText('a,button', '预览并审批归档计划')
check('plan page shows diff', await waitText('归档计划与审批') && await waitText('2026'))
await shot('04-plan')
check('execute disabled before approval', await page.evaluate(() => { const b = [...document.querySelectorAll('button')].find((x) => x.textContent.includes('执行归档')); return !!b && b.disabled }))
check('approve plan', await clickText('button', '批准计划'))
check('status becomes approved', await waitText('已批准，待执行'))
check('open confirm dialog', await clickText('button', '执行归档'))
check('confirm execute', await clickText('.dlg button', '确认执行'))
check('plan executed', await waitText('归档已完成', 60000))
await shot('05-plan-executed')

// 4. Ledger search + export
await page.goto(base + '/ledger', { waitUntil: 'networkidle0' })
check('ledger lists archived files', await waitText('采购部'))
await page.type('input[aria-label=关键词搜索]', '采购')
await sleep(900)
const rows = await page.$$eval('tbody tr', (r) => r.map((x) => x.innerText))
check('keyword search narrows results', rows.length > 0 && rows.every((t) => t.includes('采购')), rows.length)
await shot('06-ledger-search')
await clickText('button', '导出 Excel')
let xlsx = null
for (let i = 0; i < 40 && !xlsx; i++) { await sleep(250); xlsx = fs.readdirSync(dl).find((f) => f.endsWith('.xlsx')) }
check('Excel downloaded via UI', !!xlsx && fs.statSync(path.join(dl, xlsx)).size > 2000, xlsx)

// 5. Files library + templates + settings
await page.goto(base + '/files', { waitUntil: 'networkidle0' })
check('file library lists records', await waitText('已归档'))
await shot('07-files')
await page.goto(base + '/templates', { waitUntil: 'networkidle0' })
check('create template via UI', await clickText('button', '新建模板'))
await page.type('#t-n', 'UI 测试模板')
await page.type('#t-f', '合同')
check('template saved', await clickText('.dlg button', '保存') && await waitText('UI 测试模板'))
await shot('08-templates')
await page.goto(base + '/settings', { waitUntil: 'networkidle0' })
check('settings shows live service status', await waitText('文档解析Worker') && await waitText('在线'))
await clickText('.seg button', '深色')
await sleep(400)
await shot('09-settings-dark')
check('dark theme applied', await page.evaluate(() => document.documentElement.dataset.dark === '1'))
await page.setViewport({ width: 1366, height: 768 }); await page.goto(base + '/', { waitUntil: 'networkidle0' }); await shot('10-dashboard-1366-dark')
await page.setViewport({ width: 800, height: 900 }); await page.goto(base + '/tasks', { waitUntil: 'networkidle0' }); await shot('11-tasks-narrow')
check('no horizontal page overflow at 800px', await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1))

check('no console/page errors', errors.length === 0, errors.slice(0, 3).join(' | '))
await browser.close()
console.log(failed ? `${failed} FAILED` : 'all UI checks passed')
process.exit(failed ? 1 : 0)
