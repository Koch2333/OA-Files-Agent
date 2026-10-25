// Browser E2E against a RUNNING backend that serves the built frontend (OA_FRONTEND_DIR=dist).
//   node e2e/ui.mjs http://127.0.0.1:8080 "D:\path\to\tests\samples" "C:\path\to\msedge.exe"
// Optional login for enterprise-mode servers: UI_ADMIN_USER, UI_ADMIN_PASSWORD, UI_TOTP_SECRET (Base32).
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
await page.evaluateOnNewDocument(() => { try { localStorage.setItem('oa.llm.ui', 'advanced') } catch { /* ignore */ } }) // LLM settings default to simple mode
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

// 0. Optional login (server in enterprise mode): UI_ADMIN_USER / UI_ADMIN_PASSWORD [+ UI_TOTP_SECRET when the account has TOTP]
if (process.env.UI_ADMIN_USER) {
  await page.goto(base + '/login', { waitUntil: 'networkidle0' })
  await page.type('#login-user', process.env.UI_ADMIN_USER)
  await page.type('#login-pw', process.env.UI_ADMIN_PASSWORD ?? '')
  await page.click('button[type=submit]')
  if (process.env.UI_TOTP_SECRET) {
    const { totp } = await import('./lib/totp.mjs')
    const hasOtp = await page.waitForSelector('#mfa-code', { timeout: 8000 }).then(() => true, () => false)
    if (hasOtp) { await page.type('#mfa-code', totp(process.env.UI_TOTP_SECRET)) }
  }
  check('logged in', await page.waitForFunction(() => !location.pathname.startsWith('/login'), { timeout: 15000 }).then(() => true, () => false))
}

// 1. Dashboard
await page.goto(base + '/', { waitUntil: 'networkidle0' })
check('dashboard renders (no mock banner in real mode)', (await page.title()).includes('工作台') && !(await page.evaluate(() => document.body.innerText.includes('演示数据模式'))))
await shot('01-dashboard')

// 2. Import with real files
await page.goto(base + '/import', { waitUntil: 'networkidle0' })
const names = fs.readdirSync(samples, { withFileTypes: true }).filter((e) => e.isFile() && !e.name.endsWith('.json')).map((e) => e.name)
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

// 6. File manager: browse → mkdir → rename → move via dialog → trash → restore
await page.setViewport({ width: 1440, height: 900 })
await page.goto(base + '/manage', { waitUntil: 'networkidle0' })
check('file manager lists archive folders', await waitText('文件管理') && await page.waitForFunction(() => document.querySelectorAll('tbody tr').length > 0, { timeout: 15000 }).then(() => true, () => false))
await shot('12-file-manager')
check('open new-folder dialog', await clickText('button', '新建文件夹'))
await page.waitForSelector('#fm-name')
await page.type('#fm-name', 'UI测试夹')
check('create folder', await clickText('.dlg button', '创建') && await waitText('UI测试夹'))
check('open rename dialog', await clickText('button[aria-label="重命名 UI测试夹"]', ''))
await page.waitForSelector('#fm-rename')
await page.$eval('#fm-rename', (el) => { el.focus(); el.select() })
await page.type('#fm-rename', 'UI测试夹2')
check('rename folder', await clickText('.dlg button', '重命名') && await waitText('UI测试夹2'))
// move a file of the current folder into the new folder via the dialog (skipped when the root holds no files)
const moveBtn = await page.$('tbody tr:not(:has(.dir)) button[aria-label^="移动 "]')
if (moveBtn) {
  await moveBtn.click()
  await page.waitForSelector('#fm-dest')
  await page.click('#fm-dest', { clickCount: 3 })
  await page.type('#fm-dest', 'UI测试夹2')
  check('move file into folder', await clickText('.dlg button', '移动到此处') && await waitText('已移动'))
} else {
  console.log('SKIP  no file in the root folder to move')
}
check('open delete dialog', await clickText('button[aria-label="删除 UI测试夹2"]', ''))
check('confirm delete → recycle bin', await clickText('.dlg button', '删除') && await waitText('已将“UI测试夹2”移入回收站'))
await clickText('.seg button', '回收站')
check('trash lists deleted folder', await waitText('UI测试夹2'))
check('restore from trash', await clickText('button[aria-label="还原 UI测试夹2"]', '') && await waitText('已还原到'))
await shot('13-file-manager-restored')

// 7. Settings → 模型 Provider: preset selection, free Base URL / model edit, leave the backend in offline mode
await page.goto(base + '/settings', { waitUntil: 'networkidle0' })
check('settings shows 模型 Provider card', await waitText('全局大模型') && await waitText('识别方式'))
check('switch to 大模型', await clickText('[data-mode]', '大模型'))
await page.waitForSelector('#llm-base')
check('DeepSeek preselected', await page.waitForFunction(() => document.querySelector('[data-preset="deepseek"]')?.getAttribute('aria-checked') === 'true', { timeout: 10000 }).then(() => true, () => false))
check('choose 通义千问 → Base URL becomes DashScope', await page.click('[data-preset="qwen"]').then(() => page.waitForFunction(() => document.querySelector('#llm-base')?.value.includes('dashscope.aliyuncs.com'), { timeout: 10000 })).then(() => true, () => false))
await page.$eval('#llm-model', (el) => { el.focus(); el.select() })
await page.type('#llm-model', 'my-custom-model')
check('model field accepts a custom id', await page.$eval('#llm-model', (el) => el.value === 'my-custom-model'))
await shot('14-settings-llm')
check('switch back to 离线规则 → nothing to save (button disabled)', await clickText('[data-mode]', '离线规则') && await page.evaluate(() => { const b = [...document.querySelectorAll('button')].find((x) => x.textContent.includes('保存并生效')); return !!b && b.disabled }))
// real save round trip: store a DeepSeek config with a dummy key (no network call happens on save), then go back offline
check('switch to 大模型 and pick DeepSeek', await clickText('[data-mode]', '大模型') && await page.click('[data-preset="deepseek"]').then(() => true, () => false))
await page.waitForSelector('#llm-key')
await page.type('#llm-key', 'sk-ui-test-00000000')
check('save LLM config', await clickText('button', '保存并生效') && await waitText('已生效'))
check('engine chip names DeepSeek', await waitText('当前：DeepSeek'))
check('saved key is never displayed', await waitText('已保存') && !(await page.evaluate(() => document.body.innerText.includes('sk-ui-test') || [...document.querySelectorAll('input')].some((i) => i.value.includes('sk-ui-test')))))
check('switch back to offline and save', await clickText('[data-mode]', '离线规则') && await clickText('button', '保存并生效') && await waitText('当前：离线规则'))

// 7. In-app preview of an archived file (xlsx/docx) from the file manager
await page.goto(base + '/manage', { waitUntil: 'networkidle0' })
{
  let cur = ''
  for (let depth = 0; depth < 6; depth++) {
    const hasFile = await page.evaluate(() => [...document.querySelectorAll('tbody tr')].some((r) => /\.(xlsx|docx)\b/i.test(r.getAttribute('aria-label') ?? '')))
    if (hasFile) break
    const opened = await page.evaluate(() => { const r = [...document.querySelectorAll('tbody tr')].find((x) => (x.getAttribute('aria-label') ?? '').startsWith('文件夹')); if (r) { r.click(); return true } return false })
    if (!opened) break
    await sleep(600)
  }
  const clicked = await page.evaluate(() => { const r = [...document.querySelectorAll('tbody tr')].find((x) => /\.(xlsx|docx)\b/i.test(x.getAttribute('aria-label') ?? '')); const b = r?.querySelector('[data-testid=fm-preview]'); if (b) { b.click(); return true } return false })
  check('archived xlsx/docx has a preview action', clicked)
  if (clicked) {
    check('preview dialog opens', await page.waitForSelector('[data-testid=file-preview]', { timeout: 15000 }).then(() => true, () => false))
    const rendered = await page.waitForFunction(() => { const d = document.querySelector('[data-testid=file-preview]'); return !!d && (!!d.querySelector('[data-testid=preview-document]') || !!d.querySelector('[data-testid=preview-sheet] table') || !!d.querySelector('[data-testid=preview-unsupported]')) && d.innerText.trim().length > 20 }, { timeout: 20000 }).then(() => true, () => false)
    check('preview dialog renders content', rendered)
    await shot('15-file-preview')
    await page.keyboard.press('Escape')
    check('Esc closes the preview', await page.waitForFunction(() => !document.querySelector('[data-testid=file-preview]'), { timeout: 5000 }).then(() => true, () => false))
  }
}

// 8. Storage connections (optional feature): nav item exists and the page renders one of its states
await page.goto(base + '/storage', { waitUntil: 'networkidle0' })
check('nav has 存储连接', await page.evaluate(() => [...document.querySelectorAll('nav a, a.item')].some((a) => a.textContent.includes('存储连接'))))
check('storage page renders (empty state, list or feature-off)', await page.waitForFunction(() => {
  const t = document.body.innerText
  return t.includes('还没有存储连接') || t.includes('存储连接功能未启用') || !!document.querySelector('[data-conn]')
}, { timeout: 15000 }).then(() => true, () => false))
await shot('16-storage')

check('no console/page errors', errors.length === 0, errors.slice(0, 3).join(' | '))
await browser.close()
console.log(failed ? `${failed} FAILED` : 'all UI checks passed')
process.exit(failed ? 1 : 0)
