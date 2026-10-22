// Shared helpers for the browser E2E scripts (ui_ai.mjs, ui_diag.mjs). puppeteer-core + Edge/Chrome, no extra dependencies.
import puppeteer from 'puppeteer-core'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { totp } from './totp.mjs'

export const env = {
  base: process.env.UI_BASE ?? 'http://127.0.0.1:8080',
  exe: process.env.UI_BROWSER ?? 'C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe',
  samples: process.env.UI_SAMPLES ?? '../tests/samples',
  user: process.env.UI_ADMIN_USER ?? '',
  pass: process.env.UI_ADMIN_PASSWORD ?? '',
  totpSecret: process.env.UI_TOTP_SECRET ?? '',
  /** 通过 Vite 开发服务器访问真实后端时设为 1：强制关闭演示数据（localStorage oa.useMock=0） */
  dev: process.env.UI_DEV === '1',
}
export const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

export function createRunner(name) {
  const shots = path.resolve('e2e/screenshots')
  fs.mkdirSync(shots, { recursive: true })
  let failed = 0
  return {
    shots,
    check(label, ok, detail = '') { console.log(`${ok ? 'PASS' : 'FAIL'}  ${label} ${ok ? '' : detail}`); if (!ok) failed++ },
    skip(label, why) { console.log(`SKIP  ${label} (${why})`) },
    get failed() { return failed },
    finish(errors = []) {
      const real = errors.filter((e) => !/401|403|409|422|423|429|404|Failed to load resource/.test(e))
      if (real.length) { console.log(`WARN  ${real.length} console/page errors:`); real.slice(0, 10).forEach((e) => console.log('   ' + e.slice(0, 300))) }
      console.log(failed ? `\n${name}: ${failed} check(s) FAILED` : `\n${name}: all checks passed`)
      process.exitCode = failed ? 1 : 0
    },
  }
}

export async function launch({ downloadDir } = {}) {
  const browser = await puppeteer.launch({ executablePath: env.exe, headless: 'new', args: ['--no-sandbox'], defaultViewport: { width: 1440, height: 900 } })
  const page = await browser.newPage()
  const errors = []
  page.on('pageerror', (e) => errors.push(String(e)))
  page.on('console', (m) => { if (m.type() === 'error') errors.push(m.text()) })
  if (env.dev) await page.evaluateOnNewDocument(() => { try { localStorage.setItem('oa.useMock', '0') } catch { /* ignore */ } })
  if (downloadDir) {
    const cdp = await page.createCDPSession()
    await cdp.send('Browser.setDownloadBehavior', { behavior: 'allow', downloadPath: downloadDir, eventsEnabled: true }).catch(() => cdp.send('Page.setDownloadBehavior', { behavior: 'allow', downloadPath: downloadDir }))
  }
  return { browser, page, errors }
}

export const mkTmp = (prefix) => fs.mkdtempSync(path.join(os.tmpdir(), prefix))

export async function clickText(page, sel, text, timeout = 15000) {
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
export async function waitText(page, text, timeout = 30000) {
  try { await page.waitForFunction((t) => document.body.innerText.replace(/\s+/g, '').includes(t.replace(/\s+/g, '')), { timeout }, text); return true } catch { return false }
}
export async function waitGone(page, selector, timeout = 10000) {
  try { await page.waitForFunction((s) => !document.querySelector(s), { timeout }, selector); return true } catch { return false }
}
export async function setValue(page, sel, text) {
  await page.waitForSelector(sel)
  await page.$eval(sel, (el) => { el.focus(); el.select?.() })
  await page.keyboard.press('Backspace')
  await page.type(sel, text)
}

/** 登录（企业模式）；未提供 UI_ADMIN_USER 时认为是本机模式（免登录）。 */
export async function login(page, check) {
  if (!env.user) { await page.goto(env.base + '/', { waitUntil: 'networkidle0' }); return true }
  await page.goto(env.base + '/login', { waitUntil: 'networkidle0' })
  await page.type('#login-user', env.user)
  await page.type('#login-pw', env.pass)
  await page.click('button[type=submit]')
  if (env.totpSecret) {
    const hasOtp = await page.waitForSelector('#mfa-code', { timeout: 8000 }).then(() => true, () => false)
    if (hasOtp) await page.type('#mfa-code', totp(env.totpSecret))
  }
  const ok = await page.waitForFunction(() => !location.pathname.startsWith('/login'), { timeout: 20000 }).then(() => true, () => false)
  check('logged in', ok)
  return ok
}

/** 在页面里带会话与 CSRF 调用后端（测试准备/还原用）：返回 { status, data }。 */
export async function apiCall(page, method, url, body) {
  return page.evaluate(async (method, url, body) => {
    const me = await fetch('/v1/auth/me', { credentials: 'same-origin' }).then((r) => (r.ok ? r.json() : null)).catch(() => null)
    const csrf = me?.data?.csrf_token ?? me?.csrf_token ?? ''
    const res = await fetch(url, { method, credentials: 'same-origin', headers: { 'Content-Type': 'application/json', Accept: 'application/json', ...(method === 'GET' ? {} : { 'X-CSRF-Token': csrf }) }, body: body === undefined ? undefined : JSON.stringify(body) })
    let data = null
    try { data = (await res.json())?.data ?? null } catch { /* 204 等 */ }
    return { status: res.status, data }
  }, method, url, body)
}
