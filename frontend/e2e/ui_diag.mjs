// Browser E2E for 运行诊断 (diagnostics), error reports, the diagnostics bundle download and client-side error reporting.
// Runs against a RUNNING stack. The CLIENT ERROR part needs the dev-only hook window.__oaTestError, which exists only in a Vite DEV
// server (import.meta.env.DEV), never in a production build. Run it like this:
//
//   # terminal 1 (real backend on :8080, e.g. go run ./cmd/oa-agent)
//   cd frontend && OA_API_TARGET=http://127.0.0.1:8080 npm run dev          # Vite on :5173 proxies /v1 /healthz /readyz
//   # terminal 2
//   UI_BASE=http://127.0.0.1:5173 UI_DEV=1 UI_ADMIN_USER=admin UI_ADMIN_PASSWORD=... UI_TOTP_SECRET=BASE32 node e2e/ui_diag.mjs
//
// (UI_DEV=1 makes the page ignore the demo-data default of the dev server.) Against a production build (UI_BASE=http://127.0.0.1:8080)
// the client-error checks are skipped automatically. If the backend rejects the dev origin (CSRF/Origin check), set OA_PUBLIC_URL /
// OA_CORS_ORIGINS on the backend to include http://127.0.0.1:5173.
import fs from 'node:fs'
import path from 'node:path'
import { clickText, createRunner, env, launch, login, mkTmp, setValue, sleep, waitGone, waitText } from './lib/common.mjs'

const { check, skip, shots, finish } = createRunner('ui_diag')
const dl = mkTmp('oa-diag-dl-')
const { browser, page, errors } = await launch({ downloadDir: dl })
const shot = (n) => page.screenshot({ path: path.join(shots, 'diag-' + n + '.png') })
const go = (p) => page.goto(env.base + p, { waitUntil: 'networkidle0' })
const sel = (id) => `[data-testid="${id}"]`

try {
  await login(page, check)
  await go('/admin/diagnostics')
  check('diagnostics page renders', (await page.title()).includes('运行诊断') && await waitText(page, '系统状态'))
  check('system card shows version and uptime', await waitText(page, '版本') && await waitText(page, '已运行'))
  check('database status is shown', await page.waitForSelector(sel('db-status'), { timeout: 15000 }).then(() => true, () => false))
  check('worker status is shown', !!(await page.$(sel('worker-status'))))
  check('disk bar is shown', !!(await page.$(sel('disk'))))
  check('bundle note says it is redacted', await waitText(page, '不包含') && await waitText(page, '密钥'))
  await shot('01-page')

  // ---- client error reporting (dev server only)
  const hook = await page.evaluate(() => typeof window.__oaTestError === 'function')
  const marker = 'e2e-client-error-' + Date.now()
  if (hook) {
    await page.evaluate((m) => window.__oaTestError(m), marker)
    check('toast with the report id appears', await waitText(page, '页面出现了问题，已记录（编号', 20000))
    check('toast has a copy-details button', await page.waitForFunction(() => [...document.querySelectorAll('.toast button')].some((b) => b.textContent.includes('复制详情')), { timeout: 5000 }).then(() => true, () => false))
    await shot('02-client-error-toast')
    // the same message within 60 s must not produce a second report/toast
    await page.evaluate((m) => window.__oaTestError(m), marker)
    await sleep(1500)
    const toasts = await page.$$eval('.toast', (t) => t.filter((x) => x.textContent.includes('已记录（编号')).length)
    check('identical error is deduplicated (one toast)', toasts === 1, String(toasts))
  } else skip('client error reporting', 'window.__oaTestError not present: this is a production build; run against `npm run dev` (see the header of this file)')

  // ---- error report table, filters, drawer
  await go('/admin/diagnostics')
  const rows = await page.$$eval(sel('report-table') + ' tbody tr', (r) => r.length).catch(() => 0)
  if (hook) {
    await setValue(page, sel('diag-search'), marker)
    check('search finds the client report', await waitText(page, marker, 15000))
    check('row shows the browser source', await page.evaluate(() => /浏览器/.test(document.querySelector('[data-testid=report-table] tbody')?.innerText ?? '')))
    await page.click(sel('report-table') + ' tbody tr')
    check('drawer opens', await page.waitForSelector('[role=dialog][aria-label=错误报告详情]', { timeout: 10000 }).then(() => true, () => false))
    check('stack box is shown (monospace scroll box)', await page.waitForSelector(sel('report-stack'), { timeout: 10000 }).then(() => true, () => false))
    check('copy button for the stack', !!(await page.$(sel('copy-stack'))))
    check('occurrences list shows request ids', await waitText(page, '最近') && await waitText(page, '请求编号'))
    await shot('03-drawer')
    check('mark resolved', await page.click(sel('report-resolve')).then(() => true, () => false) && await waitText(page, '已标记为已解决'))
    check('button turns into reopen', await waitText(page, '重新打开'))
    await page.click(sel('report-resolve'))
    check('reopened', await waitText(page, '已重新打开'))
    await page.click(sel('report-delete'))
    check('delete asks for confirmation', await waitText(page, '删除这条错误报告？'))
    check('confirm delete', await clickText(page, '[role=dialog] button', '删除'))
    check('report disappears', await waitText(page, '没有错误报告', 15000))
  } else if (rows === 0) {
    check('empty state is shown when there are no reports', await waitText(page, '没有错误报告'))
  } else {
    await page.click(sel('report-table') + ' tbody tr')
    check('drawer opens for an existing report', await page.waitForSelector('[role=dialog][aria-label=错误报告详情]', { timeout: 10000 }).then(() => true, () => false))
    await page.keyboard.press('Escape')
    check('Esc closes the drawer', await waitGone(page, '[role=dialog][aria-label=错误报告详情]'))
  }

  // ---- filters
  await go('/admin/diagnostics')
  await page.select(sel('diag-severity'), 'fatal')
  await sleep(700)
  check('severity filter applies (all visible rows are 致命)', await page.$$eval(sel('report-table') + ' tbody tr', (r) => r.every((x) => x.innerText.includes('致命'))).catch(() => true))
  await page.select(sel('diag-severity'), '')
  await page.click(sel('diag-state-all'))
  await sleep(500)
  check('"all" state filter is selectable', await page.$eval(sel('diag-state-all'), (el) => el.getAttribute('aria-pressed') === 'true'))

  // ---- bundle download
  await go('/admin/diagnostics')
  await page.waitForSelector(sel('bundle-download'))
  await page.click(sel('bundle-download'))
  console.log(`INFO  busy label seen: ${await waitText(page, '正在生成诊断包', 3000)}`) // may be too fast to observe on a small bundle
  let zip = null
  for (let i = 0; i < 120 && !zip; i++) { await sleep(500); zip = fs.readdirSync(dl).find((f) => f.endsWith('.zip')) }
  check('diagnostics bundle downloaded as .zip', !!zip)
  if (zip) {
    const buf = fs.readFileSync(path.join(dl, zip))
    check('bundle is a real zip (PK header) and non-trivial', buf.length > 200 && buf[0] === 0x50 && buf[1] === 0x4b, String(buf.length))
    const text = buf.toString('latin1')
    check('bundle does not contain the session cookie name or password words', !/oa_session=|password"\s*:\s*"[^*]/i.test(text))
  }
  await shot('04-bundle')

  // ---- nav badge and unclean-exit banner are informational
  const badge = await page.$eval(sel('diag-badge'), (el) => el.textContent).catch(() => null)
  console.log(`INFO  nav badge: ${badge ?? '(none: no open reports)'}`)
  if (await page.$(sel('unclean-banner'))) check('unclean-exit banner links to the related reports', await page.$eval(sel('unclean-banner') + ' a', (a) => a.getAttribute('href').includes('source=supervisor')))
  else skip('unclean-exit banner', 'last exit was clean')
} catch (e) {
  check('script ran to completion', false, String(e?.stack ?? e))
  await shot('99-failure').catch(() => undefined)
} finally {
  await browser.close()
  finish(errors)
}
