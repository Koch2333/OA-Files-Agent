// Material 3 visual sweep (mock mode): screenshots of the main pages, dialogs, auth pages in light and dark,
// desktop and narrow widths, into e2e/screenshots/m3/.   UI_BASE=http://127.0.0.1:5199 node e2e/ui_m3.mjs
import puppeteer from 'puppeteer-core'
import { mkdirSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
const base = process.env.UI_BASE ?? 'http://127.0.0.1:5199'
const exe = process.env.UI_BROWSER ?? 'C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe'
const out = fileURLToPath(new URL('./screenshots/m3/', import.meta.url))
mkdirSync(out, { recursive: true })
const browser = await puppeteer.launch({ executablePath: exe, headless: 'new', args: ['--no-sandbox'] })
const wait = (ms) => new Promise((r) => setTimeout(r, ms))
const pages = [['dashboard', '/'], ['import', '/import'], ['ai', '/ai'], ['tasks', '/tasks'], ['files', '/files'], ['manage', '/manage'], ['ledger', '/ledger'], ['templates', '/templates'], ['storage', '/storage'], ['settings', '/settings'], ['account', '/account/security'], ['users', '/admin/users'], ['security', '/admin/security'], ['sessions', '/admin/sessions'], ['audit', '/admin/audit'], ['diagnostics', '/admin/diagnostics'], ['login', '/login'], ['setup', '/?mock_setup=1'], ['notfound', '/nope']]
const variants = [['light', 1440, 900], ['dark', 1440, 900], ['light', 820, 900], ['dark', 390, 800]]
for (const [scheme, w, h] of variants) {
  const page = await browser.newPage()
  await page.emulateMediaFeatures([{ name: 'prefers-color-scheme', value: scheme }])
  await page.setViewport({ width: w, height: h })
  const errs = []
  page.on('pageerror', (e) => errs.push(String(e)))
  for (const [name, url] of pages) {
    await page.goto(base + url, { waitUntil: 'networkidle0' })
    await wait(600)
    await page.screenshot({ path: `${out}${name}-${scheme}-${w}.png` })
  }
  if (w === 1440) {

    await page.goto(base + '/tasks?status=awaiting_review', { waitUntil: 'networkidle0' }); await wait(500)
    await page.click('tr.clickable').catch(() => {}); await wait(800)
    const pl = await page.$('a[href^="/plans/"]'); if (pl) { await pl.click(); await wait(900) }
    const rev = await page.evaluateHandle(() => [...document.querySelectorAll('button')].find((b) => b.textContent.trim() === '修订'))
    if (rev.asElement()) { await rev.asElement().click(); await wait(500); await page.screenshot({ path: `${out}dialog-revision-${scheme}.png` }) }
    await page.keyboard.press('Escape')
    await page.goto(base + '/', { waitUntil: 'networkidle0' }); await wait(500)
    await page.click('[data-testid="user-menu"]').catch(() => {}); await wait(400)
    await page.screenshot({ path: `${out}usermenu-${scheme}.png` })
  }
  console.log(scheme, w, 'page errors:', errs.length, errs.slice(0, 3))
  await page.close()
}
await browser.close()
