// LLM HTTP error + one-click retry (mock mode): task card, task detail, plan, global banner, light/dark.
//   UI_BASE=http://127.0.0.1:5196 node e2e/ui_retry.mjs        (dev server started with VITE_USE_MOCK=1)
import puppeteer from 'puppeteer-core'
import { mkdirSync, mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
const base = process.env.UI_BASE ?? 'http://127.0.0.1:5196'
const exe = process.env.UI_BROWSER ?? 'C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe'
const out = fileURLToPath(new URL('./screenshots/retry/', import.meta.url))
mkdirSync(out, { recursive: true })
const dir = mkdtempSync(join(tmpdir(), 'rt-'))
const mk = (n) => { const p = join(dir, n); writeFileSync(p, '%PDF-1.4 demo'); return p }
const wait = (ms) => new Promise((r) => setTimeout(r, ms))
let fails = 0
const check = (name, ok) => { console.log(ok ? 'PASS' : 'FAIL', name); if (!ok) fails++ }
const browser = await puppeteer.launch({ executablePath: exe, headless: 'new', args: ['--no-sandbox'] })

async function waitFor(page, sel, ms = 30000) { try { await page.waitForSelector(sel, { timeout: ms }); return true } catch { return false } }

for (const scheme of ['light', 'dark']) {
  const page = await browser.newPage()
  await page.emulateMediaFeatures([{ name: 'prefers-color-scheme', value: scheme }])
  await page.setViewport({ width: 1280, height: 900 })
  const errs = []
  page.on('pageerror', (e) => errs.push(String(e)))
  for (const kind of ['429', '401']) {
    await page.goto(base + '/ai', { waitUntil: 'networkidle0' })
    await (await page.$('input[type=file][multiple]')).uploadFile(mk(`llm${kind}-采购合同2026-03-01.pdf`), mk('财务报销2026-02-02.pdf'))
    await wait(400)
    await page.click('[data-testid="ai-submit"]')
    check(`${scheme}/${kind}: retry notice appears on the task card`, await waitFor(page, '[data-testid="retry-notice"]', 40000))
    await wait(600)
    check(`${scheme}/${kind}: HTTP status chip`, await page.$eval('[data-testid="retry-status"]', (e, k) => e.textContent.includes(k), kind))
    const primary = await page.$eval('[data-testid="retry-notice"]', (e) => e.getAttribute('data-primary'))
    check(`${scheme}/${kind}: primary action`, primary === (kind === '401' ? 'check_key' : 'retry'))
    await page.screenshot({ path: `${out}workbench-${kind}-${scheme}.png` })
    await page.evaluate(() => window.scrollTo(0, 0)); await page.evaluate(() => document.querySelector('main, .page, body').scrollTo?.(0, 0)); await wait(300)
    check(`${scheme}/${kind}: global banner shows the failure`, !!(await page.$('[data-testid="llm-failure-banner"]')))
    await page.$eval('[data-testid="llm-failure-banner"]', (e) => e.scrollIntoView({ block: 'center' })); await wait(200)
    await page.screenshot({ path: `${out}banner-${kind}-${scheme}.png` })
    if (kind === '429') {
      check('429: countdown shown and retry disabled', !!(await page.$('[data-testid="retry-countdown"]')) && (await page.$eval('[data-testid="retry-btn"]', (b) => b.disabled)))
      await page.click('a[href^="/tasks/"]'); await waitFor(page, '[data-testid="retry-notice"]'); await wait(500)
      await page.screenshot({ path: `${out}task-429-${scheme}.png` })
      await wait(21000)
      check('429: retry enabled after the countdown', await page.$eval('[data-testid="retry-btn"]', (b) => !b.disabled))
      await page.click('[data-testid="retry-btn"]')
      await page.click('[data-testid="retry-btn"]').catch(() => {}) // double click: harmless
      check('retry notice disappears after the re-run succeeds', await (async () => { for (let i = 0; i < 40; i++) { if (!(await page.$('[data-testid="retry-notice"]'))) return true; await wait(500) } return false })())
      await wait(6000)
      await page.screenshot({ path: `${out}task-429-after-retry-${scheme}.png` })
      const rows = await page.$$eval('table.tbl tbody tr', (r) => r.length)
      check('both files are recognised after the retry', rows === 2 && !(await page.evaluate(() => document.body.innerText.includes('模型出错'))))
    } else {
      check('401: check-key link goes to settings', await page.$eval('[data-testid="retry-check-key"]', (a) => a.getAttribute('href').includes('/settings')))
      check('401: retry still available as secondary', await page.$eval('[data-testid="retry-btn"]', (b) => !b.disabled))
    }
  }
  console.log(scheme, 'page errors:', errs.length, errs.slice(0, 3))
  await page.close()
}
await browser.close()
process.exit(fails ? 1 : 0)
