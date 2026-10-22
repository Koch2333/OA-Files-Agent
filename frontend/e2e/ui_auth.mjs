// Browser E2E for login / accounts against a RUNNING stack that serves the built frontend (OA_FRONTEND_DIR=dist).
//   UI_SETUP_TOKEN=... UI_ADMIN_PASSWORD=... node e2e/ui_auth.mjs
// Env: UI_BASE (default http://127.0.0.1:8080), UI_SETUP_TOKEN (fresh server), UI_ADMIN_USER (default admin),
//      UI_ADMIN_PASSWORD (must satisfy the password policy; a random one is generated if unset and printed only on failure),
//      UI_BROWSER (Edge/Chrome path). If the server is already initialised, set UI_ADMIN_USER/UI_ADMIN_PASSWORD and UI_TOTP_SECRET
//      of an existing admin; the Passkey-wizard parts are then skipped.
// Uses the DevTools WebAuthn virtual authenticator (CTAP2, internal, resident key, UV) — no real hardware needed.
import puppeteer from 'puppeteer-core'
import crypto from 'node:crypto'
import fs from 'node:fs'
import path from 'node:path'
import { selfTest, totp } from './lib/totp.mjs'

const base = process.env.UI_BASE ?? 'http://127.0.0.1:8080'
const exe = process.env.UI_BROWSER ?? 'C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe'
const adminUser = process.env.UI_ADMIN_USER ?? 'admin'
const adminPass = process.env.UI_ADMIN_PASSWORD ?? 'E2e-' + crypto.randomBytes(9).toString('base64url') + '-9!'
const setupToken = process.env.UI_SETUP_TOKEN ?? ''
const viewerUser = 'viewer.' + crypto.randomBytes(3).toString('hex')
const viewerPass2 = 'Viewer-' + crypto.randomBytes(9).toString('base64url') + '-7!'
const shots = path.resolve('e2e/screenshots')
fs.mkdirSync(shots, { recursive: true })
let failed = 0
const check = (name, ok, d = '') => { console.log(`${ok ? 'PASS' : 'FAIL'}  ${name} ${ok ? '' : d}`); if (!ok) failed++ }
const skip = (name, why) => console.log(`SKIP  ${name} (${why})`)
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
check('totp helper matches RFC 6238 vector', selfTest())

const browser = await puppeteer.launch({ executablePath: exe, headless: 'new', args: ['--no-sandbox'], defaultViewport: { width: 1440, height: 900 } })
const errors = []
async function newPage(ctx = browser, virtualAuth = false) {
  const p = await ctx.newPage()
  if (process.env.UI_DEBUG) p.on('response', (r) => { if (/\/v1\/(auth|setup)/.test(r.url())) console.log('  net', r.request().method(), r.url().replace(base, ''), r.status()) })
  p.on('pageerror', (e) => errors.push(String(e)))
  p.on('console', (m) => { if (m.type() === 'error' && !/401|403|409|422|423|429|404|Failed to load resource/.test(m.text())) errors.push(m.text()) })
  if (virtualAuth) {
    const cdp = await p.createCDPSession()
    await cdp.send('WebAuthn.enable')
    await cdp.send('WebAuthn.addVirtualAuthenticator', { options: { protocol: 'ctap2', transport: 'internal', hasResidentKey: true, hasUserVerification: true, isUserVerified: true, automaticPresenceSimulation: true } })
    p.cdp = cdp
  }
  return p
}
const page = await newPage(browser, true)
const shot = (n, p = page) => p.screenshot({ path: path.join(shots, 'auth-' + n + '.png') })

async function clickText(p, sel, text, timeout = 15000) {
  const end = Date.now() + timeout
  while (Date.now() < end) {
    const ok = await p.evaluate((sel, text) => {
      const el = [...document.querySelectorAll(sel)].find((e) => e.textContent.replace(/\s+/g, '').includes(text.replace(/\s+/g, '')) && !e.disabled && e.getAttribute('aria-disabled') !== 'true')
      if (el) { el.click(); return true }
      return false
    }, sel, text)
    if (ok) return true
    await sleep(200)
  }
  return false
}
async function waitText(p, text, timeout = 15000) {
  try { await p.waitForFunction((t) => document.body.innerText.replace(/\s+/g, '').includes(t.replace(/\s+/g, '')), { timeout }, text); return true } catch { return false }
}
async function waitGone(p, text, timeout = 15000) {
  try { await p.waitForFunction((t) => !document.body.innerText.replace(/\s+/g, '').includes(t.replace(/\s+/g, '')), { timeout }, text); return true } catch { return false }
}
async function setValue(p, sel, text) {
  await p.waitForSelector(sel, { timeout: 15000 })
  await p.$eval(sel, (el) => { el.focus(); el.select?.() })
  await p.keyboard.press('Backspace')
  await p.type(sel, text)
}
const path_ = (p) => p.evaluate(() => location.pathname)
async function waitPath(p, re, timeout = 15000) { try { await p.waitForFunction((s) => new RegExp(s).test(location.pathname), { timeout }, re.source); return true } catch { return false } }
async function checkByLabel(p, text) {
  return p.evaluate((t) => {
    const l = [...document.querySelectorAll('label')].find((x) => x.textContent.includes(t))
    const i = l?.querySelector('input[type=checkbox]')
    if (i && !i.checked) i.click()
    return !!i
  }, text)
}
// TOTP replay protection: never reuse a 30s step; wait for the next window when needed.
let lastStep = 0
async function freshTotp(secret) {
  let step = Math.floor(Date.now() / 30000)
  while (step <= lastStep) { await sleep(1000); step = Math.floor(Date.now() / 30000) }
  lastStep = step
  return totp(secret)
}
async function readCodes(p) { return p.$$eval('ul[aria-label=恢复码] li', (l) => l.map((x) => x.textContent.trim())) }
async function userMenu(p) { await p.click('[data-testid=user-menu]'); await p.waitForSelector('[role=menu]') }
async function logout(p) {
  await userMenu(p)
  await p.click('[data-testid=logout]')
  return waitPath(p, /^\/login/)
}
async function loginPassword(p, user, pass) {
  await p.goto(base + '/login', { waitUntil: 'networkidle0' })
  await setValue(p, '#login-user', user)
  await setValue(p, '#login-pw', pass)
  await p.click('button[type=submit]')
}

let adminTotp = process.env.UI_TOTP_SECRET ?? ''
let recoveryCodes = []
let setupDone = false

try {
  // ---------- 1. Setup wizard (fresh server) ----------
  await page.goto(base + '/', { waitUntil: 'networkidle0' })
  const onSetup = (await path_(page)) === '/setup'
  if (onSetup) {
    check('uninitialised server redirects to /setup', true)
    await shot('01-setup-welcome')
    check('welcome step lists environment checks', await waitText(page, '环境检查') && await waitText(page, 'Passkey'))
    await page.click('[data-testid=setup-next]')
    check('token step shown', await waitText(page, '初始化令牌'))
    if (setupToken) await setValue(page, '#setup-token', setupToken)
    else skip('setup token', 'UI_SETUP_TOKEN not set (only OK for loopback without token)')
    await shot('02-setup-token')
    await page.click('[data-testid=setup-next]')
    check('mode step shown', await waitText(page, '运行模式'))
    await page.waitForSelector('input[name=mode][value=enterprise]', { timeout: 15000 })
    await page.click('input[name=mode][value=enterprise]')
    await shot('03-setup-mode')
    await page.click('[data-testid=setup-next]')
    await page.waitForSelector('#su-user')
    await setValue(page, '#su-user', adminUser)
    await setValue(page, '#su-name', 'E2E 管理员')
    await setValue(page, '#su-pw', adminPass)
    await setValue(page, '#su-pw2', adminPass)
    check('password strength indicator shown', await waitText(page, '强度'))
    await shot('04-setup-admin')
    await page.click('[data-testid=setup-next]')
    await page.waitForSelector('#su-rp')
    check('policy step is prefilled with RP ID', (await page.$eval('#su-rp', (e) => e.value)).length > 0)
    await shot('05-setup-policy')
    await page.click('[data-testid=setup-next]')
    check('MFA enrolment step (Passkey)', await waitText(page, '添加 Passkey', 30000))
    await setValue(page, '#pk-name', 'E2E virtual key')
    await shot('06-setup-mfa')
    check('click add Passkey', await clickText(page, 'button', '添加 Passkey'))
    check('recovery codes shown once', await page.waitForSelector('ul[aria-label=恢复码] li', { timeout: 30000 }).then(() => true, () => false))
    recoveryCodes = await readCodes(page)
    check('10 recovery codes', recoveryCodes.length === 10, String(recoveryCodes.length))
    await shot('07-setup-recovery')
    check('continue is blocked until saved', await page.evaluate(() => { const b = [...document.querySelectorAll('button')].find((x) => x.textContent.trim() === '继续'); return !!b && b.disabled }))
    await checkByLabel(page, '已安全保存')
    check('continue after acknowledging', await clickText(page, 'button', '继续'))
    // 向导新增的“配置全局大模型”一步：此脚本不配置大模型，直接“稍后配置”（配置流程见 e2e/ui_ai.mjs）
    check('LLM step appears after MFA and can be skipped', await page.waitForSelector('[data-testid=setup-llm]', { timeout: 15000 }).then(() => true, () => false))
    await shot('07b-setup-llm')
    await page.click('[data-testid=setup-llm-skip]')
    await page.waitForSelector('[data-testid=setup-enter]')
    await shot('08-setup-finish')
    await page.click('[data-testid=setup-enter]')
    check('enters the app after setup', await waitPath(page, /^\/$/))
    setupDone = true
    await page.goto(base + '/setup', { waitUntil: 'networkidle0' })
    check('/setup never shows again once initialised', (await path_(page)) !== '/setup')
  } else {
    skip('setup wizard', 'server already initialised')
    await loginPassword(page, adminUser, adminPass)
    if (adminTotp) {
      await page.waitForSelector('#mfa-code', { timeout: 10000 })
      await page.type('#mfa-code', await freshTotp(adminTotp))
    }
    check('logged in as existing admin', await waitPath(page, /^\/(?!login)/))
  }

  // ---------- 2. Passkey passwordless login ----------
  if (setupDone) {
    check('logout from user menu', await logout(page))
    // With a virtual authenticator the login page's conditional UI (autofill) signs in on its own; a real browser waits for the user's pick.
    if (!(await waitPath(page, /^\/(?!login)/, 4000))) {
      await page.waitForSelector('[data-testid=passkey-login]', { timeout: 15000 })
      await page.click('[data-testid=passkey-login]')
    }
    check('passwordless Passkey login (no username)', await waitPath(page, /^\/(?!login)/, 30000))
    await userMenu(page)
    check('role chip shows admin', (await page.$eval('[data-testid=role-chip]', (e) => e.textContent)).includes('管理员'))
    await shot('10-user-menu')
    await page.keyboard.press('Escape')
  }

  // ---------- 3. TOTP enrolment ----------
  await page.goto(base + '/account/security', { waitUntil: 'networkidle0' })
  check('account security page', await waitText(page, '账号安全'))
  if (!adminTotp) {
    check('open TOTP enrolment', await clickText(page, 'button', '绑定验证器'))
    await page.waitForSelector('[data-testid=totp-secret]', { timeout: 15000 })
    check('QR image rendered locally', await page.$eval('img.qr', (i) => i.src.startsWith('data:image/png')).catch(() => false))
    adminTotp = (await page.$eval('[data-testid=totp-secret]', (e) => e.textContent)).replace(/\s+/g, '')
    await shot('11-totp-enrol')
    await page.type('#totp-confirm', await freshTotp(adminTotp))
    check('TOTP enabled after confirming code', await waitText(page, '已启用'))
  }
  await shot('12-account-security')
  // recovery codes regeneration (may ask for recent authentication)
  check('regenerate recovery codes', await clickText(page, 'button', '重新生成'))
  await clickText(page, '.dlg button', '重新生成')
  if (await page.waitForSelector('#reauth-pw', { timeout: 3000 }).then(() => true, () => false)) {
    await page.type('#reauth-pw', adminPass)
    await page.keyboard.press('Enter')
    await shot('13-reauth')
  }
  check('new recovery codes displayed', await page.waitForSelector('ul[aria-label=恢复码] li', { timeout: 15000 }).then(() => true, () => false))
  recoveryCodes = await readCodes(page)
  await checkByLabel(page, '已安全保存')
  await clickText(page, '.dlg button', '完成')

  // Passkey parts are done: remove the virtual authenticator so the login page's conditional UI cannot sign in by itself.
  await page.cdp?.send('WebAuthn.disable')
  // ---------- 4. Password + TOTP login ----------
  check('logout', await logout(page))
  await loginPassword(page, adminUser, adminPass)
  check('MFA challenge shown', await page.waitForSelector('#mfa-code', { timeout: 10000 }).then(() => true, () => false))
  await shot('14-mfa-challenge')
  await page.type('#mfa-code', await freshTotp(adminTotp)) // auto-submits at 6 digits
  check('password + TOTP login succeeds (auto-submit)', await waitPath(page, /^\/(?!login)/, 20000))

  // wrong password shows a neutral error; wrong TOTP is rejected
  await logout(page)
  await loginPassword(page, adminUser, adminPass + 'x')
  check('wrong password shows error', await page.waitForSelector('[data-testid=login-error]', { timeout: 10000 }).then(() => true, () => false))

  // ---------- 5. Recovery code login (single use) ----------
  if (recoveryCodes.length) {
    const rc = recoveryCodes[0]
    await loginPassword(page, adminUser, adminPass)
    await page.waitForSelector('#mfa-code')
    check('switch to recovery code', await clickText(page, '.seg button', '恢复码'))
    await setValue(page, '#mfa-recovery', rc)
    await page.click('button[type=submit]:not([hidden])').catch(() => page.keyboard.press('Enter'))
    check('recovery code login succeeds', await waitPath(page, /^\/(?!login)/, 20000))
    await logout(page)
    await loginPassword(page, adminUser, adminPass)
    await page.waitForSelector('#mfa-code')
    await clickText(page, '.seg button', '恢复码')
    await setValue(page, '#mfa-recovery', rc)
    await page.keyboard.press('Enter')
    check('used recovery code is rejected', await page.waitForSelector('[data-testid=mfa-error]', { timeout: 10000 }).then(() => true, () => false))
    await shot('15-recovery-reused')
  } else skip('recovery login', 'no codes captured')

  // ---------- 6. Admin creates a viewer ----------
  await loginPassword(page, adminUser, adminPass)
  await page.waitForSelector('#mfa-code')
  await page.type('#mfa-code', await freshTotp(adminTotp))
  await waitPath(page, /^\/(?!login)/, 20000)
  await page.goto(base + '/admin/users', { waitUntil: 'networkidle0' })
  check('admin users page', await waitText(page, '用户管理') && await waitText(page, adminUser))
  await shot('16-admin-users')
  await page.click('[data-testid=user-create]')
  await page.waitForSelector('#nu-user')
  await setValue(page, '#nu-user', viewerUser)
  await setValue(page, '#nu-name', 'E2E 只读')
  await page.select('#nu-role', 'viewer')
  await clickText(page, '.seg button', '临时密码')
  await clickText(page, '.dlg button', '创建')
  check('one-time temporary password dialog', await page.waitForSelector('textarea[aria-label=一次性内容]', { timeout: 15000 }).then(() => true, () => false))
  const tempPw = await page.$eval('textarea[aria-label=一次性内容]', (e) => e.value)
  check('temporary password is non-empty', tempPw.length >= 8)
  await shot('17-temp-password')
  check('cannot close before acknowledging', await page.evaluate(() => { const b = [...document.querySelectorAll('.dlg button')].find((x) => x.textContent.trim() === '完成'); return !!b && b.disabled }))
  await checkByLabel(page, '已复制并保存')
  await clickText(page, '.dlg button', '完成')
  check('new user appears in the list', await waitText(page, viewerUser))

  // ---------- 7. Viewer: forced password change + role-based UI ----------
  const vctx = await browser.createBrowserContext()
  const vpage = await newPage(vctx)
  await loginPassword(vpage, viewerUser, tempPw)
  check('viewer is forced to account security to change password', await waitPath(vpage, /^\/account\/security/, 20000))
  check('change-password dialog shown', await waitText(vpage, '请先修改初始密码'))
  await setValue(vpage, '#cp-cur', tempPw)
  await setValue(vpage, '#cp-new', viewerPass2)
  await setValue(vpage, '#cp-new2', viewerPass2)
  await shot('18-forced-password-change', vpage)
  await clickText(vpage, '.dlg button', '修改密码')
  check('password change accepted', await waitGone(vpage, '请先修改初始密码', 20000))
  await vpage.goto(base + '/', { waitUntil: 'networkidle0' })
  const navText = await vpage.$eval('nav[aria-label=主导航]', (n) => n.innerText)
  check('viewer nav hides 智能归档/存储连接/设置/管理', !/(^|\n)(智能归档|存储连接|设置|用户管理|安全策略)(\n|$)/.test(navText), navText)
  await userMenu(vpage)
  check('viewer role chip', (await vpage.$eval('[data-testid=role-chip]', (e) => e.textContent)).includes('只读'))
  await vpage.keyboard.press('Escape')
  await vpage.goto(base + '/admin/users', { waitUntil: 'networkidle0' })
  check('viewer cannot open /admin/users', await waitText(vpage, '无权访问'))
  await vpage.goto(base + '/import', { waitUntil: 'networkidle0' })
  check('viewer cannot open /import', await waitText(vpage, '无权访问'))
  await vpage.goto(base + '/templates', { waitUntil: 'networkidle0' })
  check('viewer sees no 新建模板 button', !(await vpage.evaluate(() => [...document.querySelectorAll('button')].some((b) => b.textContent.includes('新建模板')))))
  await vpage.goto(base + '/manage', { waitUntil: 'networkidle0' })
  check('viewer sees no 新建文件夹 button', !(await vpage.evaluate(() => [...document.querySelectorAll('button')].some((b) => b.textContent.includes('新建文件夹')))))
  await shot('19-viewer-manage', vpage)

  // ---------- 8. Admin pages smoke ----------
  for (const [p, text, name] of [['/admin/users', '用户管理', '20-admin-users'], ['/admin/security', '安全策略', '21-admin-security'], ['/admin/sessions', '会话管理', '22-admin-sessions'], ['/admin/audit', '审计日志', '23-admin-audit']]) {
    await page.goto(base + p, { waitUntil: 'networkidle0' })
    check(`admin page ${p} renders`, await waitText(page, text))
    await shot(name)
  }
  await page.goto(base + '/admin/audit', { waitUntil: 'networkidle0' })
  check('audit log records the viewer creation', await waitText(page, viewerUser))

  // ---------- 9. Session revoke ----------
  await page.goto(base + '/admin/sessions', { waitUntil: 'networkidle0' })
  check('viewer session is listed', await waitText(page, viewerUser))
  const revoked = await page.evaluate((u) => {
    const tr = [...document.querySelectorAll('tbody tr')].find((r) => r.textContent.includes(u))
    const b = tr && [...tr.querySelectorAll('button')].find((x) => x.textContent.includes('吊销'))
    if (b) { b.click(); return true }
    return false
  }, viewerUser)
  check('click revoke on viewer session', revoked)
  await clickText(page, '.dlg button', '吊销')
  check('revocation confirmed', await waitText(page, '会话已吊销', 10000))
  await vpage.goto(base + '/tasks', { waitUntil: 'networkidle0' })
  check('revoked viewer is sent back to /login', await waitPath(vpage, /^\/login/, 15000))
  await shot('24-session-revoked', vpage)
  await vctx.close()
} catch (e) {
  check('script completed without exception', false, String(e?.stack ?? e))
  await shot('99-failure').catch(() => undefined)
}

check('no unexpected console/page errors', errors.length === 0, errors.slice(0, 5).join(' | '))
await browser.close()
console.log(failed ? `\n${failed} check(s) FAILED` : '\nall checks passed')
process.exit(failed ? 1 : 0)
