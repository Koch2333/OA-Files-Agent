// The wizard is a gate: with an UNFINISHED wizard (admin created, MFA/LLM/finish still pending) no route may show the app.
//   UI_BASE=http://localhost:5173 UI_SESSION=<oa_session cookie value of the freshly created admin> node e2e/ui_wizard_gate.mjs
// Prepare the session with curl: POST /v1/setup/complete (enterprise) against the API and copy the oa_session cookie.
import puppeteer from 'puppeteer-core'

const base = process.env.UI_BASE ?? 'http://localhost:5173'
const exe = process.env.UI_BROWSER ?? 'C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe'
const session = process.env.UI_SESSION
if (!session) { console.error('UI_SESSION is required'); process.exit(2) }
let failed = 0
const check = (n, ok, d = '') => { console.log(`${ok ? 'PASS' : 'FAIL'}  ${n} ${ok ? '' : d}`); if (!ok) failed++ }
const browser = await puppeteer.launch({ executablePath: exe, headless: 'new', args: ['--no-sandbox'] })
const page = await browser.newPage()
await page.setViewport({ width: 1280, height: 900 })
await page.setCookie({ name: 'oa_session', value: session, url: base, httpOnly: true, sameSite: 'Strict' })
const path = () => page.evaluate(() => location.pathname)
const waitPath = (p, t = 15000) => page.waitForFunction((x) => location.pathname === x, { timeout: t }, p).then(() => true, () => false)
for (const target of ['/', '/tasks', '/ledger', '/ai', '/admin/diagnostics', '/settings']) {
  await page.goto(base + target, { waitUntil: 'networkidle0' })
  check(`unfinished wizard: ${target} → /setup`, await waitPath('/setup'), await path())
}
const body = await page.evaluate(() => document.body.innerText)
check('wizard resumes at the MFA step', /多因素|MFA|Passkey|验证器/.test(body), body.slice(0, 160))
check('no dashboard chrome (nav) is visible', !(await page.$('[data-testid=nav-settings-toggle]')))
await page.reload({ waitUntil: 'networkidle0' })
check('after reload still on /setup', await waitPath('/setup'))
const api = await page.evaluate(async () => { const r = await fetch('/v1/tasks'); const j = await r.json().catch(() => ({})); return { status: r.status, code: j.error?.code } })
// an enrol-only session (MFA still missing) is stopped by the scope gate (403 FORBIDDEN); a full session by the wizard gate (409)
check('server gate: GET /v1/tasks is refused', (api.status === 403 && api.code === 'FORBIDDEN') || (api.status === 409 && api.code === 'WIZARD_REQUIRED'), JSON.stringify(api))
await browser.close()
console.log(failed ? `${failed} check(s) FAILED` : 'all checks passed')
process.exit(failed ? 1 : 0)
