// LLM settings (simple + advanced mode) visual sweep in mock mode, into e2e/screenshots/llm/.
//   VITE_USE_MOCK=1 npx vite --port 5198   then   UI_BASE=http://127.0.0.1:5198 node e2e/ui_llm.mjs
// Mock fault keys (src/mocks/mockAi.ts): sk-bad… 401, sk-down… 503, sk-flaky… 503 once, sk-limit… 429 once (Retry-After 5 s).
import puppeteer from 'puppeteer-core'
import { mkdirSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
const base = process.env.UI_BASE ?? 'http://127.0.0.1:5198'
const exe = process.env.UI_BROWSER ?? 'C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe'
const out = fileURLToPath(new URL('./screenshots/llm/', import.meta.url))
mkdirSync(out, { recursive: true })
const wait = (ms) => new Promise((r) => setTimeout(r, ms))
const browser = await puppeteer.launch({ executablePath: exe, headless: 'new', args: ['--no-sandbox'] })
let failures = 0
const check = (name, ok) => { if (!ok) failures++; console.log(ok ? 'PASS' : 'FAIL', name) }
const KEY = 'sk-' + 'a1b2c3d4'.repeat(4)

async function open(scheme, w, h, url, advanced = false) {
  const ctx = await browser.createBrowserContext()
  await ctx.overridePermissions(base, ['clipboard-read', 'clipboard-write']).catch(() => {})
  const page = await ctx.newPage()
  await page.emulateMediaFeatures([{ name: 'prefers-color-scheme', value: scheme }])
  await page.setViewport({ width: w, height: h })
  await page.evaluateOnNewDocument((adv) => { try { localStorage.setItem('oa.llm.ui', adv ? 'advanced' : 'simple') } catch { /* ignore */ } }, advanced)
  const errs = []
  page.on('pageerror', (e) => errs.push(String(e)))
  await page.goto(base + url, { waitUntil: 'networkidle0' })
  await page.waitForSelector('[data-testid=global-llm-card]', { timeout: 15000 })
  await wait(400)
  return { page, ctx, errs }
}
// element crop taken from the top of the page (the page header is sticky and would cover an element screenshot)
const shot = async (page, name, sel = '[data-testid=global-llm-card]') => {
  const vp = page.viewport()
  await page.setViewport({ ...vp, height: 3200 }); await wait(250) // tall viewport: the whole card is in view and the sticky header cannot overlap it
  await page.evaluate(() => window.scrollTo(0, 0)); await wait(150)
  const box = await page.evaluate((s) => { const r = document.querySelector(s).getBoundingClientRect(); return { x: r.x, y: r.y, width: r.width, height: r.height } }, sel)
  await page.screenshot({ path: `${out}${name}.png`, clip: box })
  await page.setViewport(vp)
}
const has = (page, sel) => page.$(sel).then((x) => !!x)
const visible = (page, sel, t = 8000) => page.waitForSelector(sel, { timeout: t, visible: true }).then(() => true, () => false)
const typeKey = async (page, key, prefix = 'llm-simple') => { await page.click(`#${prefix}-key`); await page.type(`#${prefix}-key`, key, { delay: 0 }) }

for (const [scheme, w, h] of [['light', 1280, 900], ['dark', 1280, 900], ['light', 390, 800]]) {
  const tag = `${scheme}-${w}`
  // 1. nothing configured: step 1 cards + "not set up yet"
  let { page, ctx, errs } = await open(scheme, w, h, '/settings?mock_llm=none')
  await page.click('[data-mode=llm]').catch(() => {}); await wait(300)
  check(`${tag} not-set-up status highlights the next step`, await visible(page, '[data-testid=llm-summary-next]'))
  check(`${tag} cards show login/paste badges and a recommended tag`, await page.evaluate(() => { const t = document.body.innerText; return t.includes('一键登录') && t.includes('粘贴密钥') && t.includes('推荐') }))
  await shot(page, `01-choose-${tag}`)

  // 2. paste flow: choose DeepSeek, bad text, then a good key -> auto test -> save
  await page.click('[data-testid=llm-simple-card-deepseek]'); await wait(300)
  await shot(page, `02-deepseek-guide-${tag}`)
  await typeKey(page, 'hello world, not a key'); await wait(300)
  check(`${tag} friendly format message`, await page.$eval('[data-testid=llm-simple-keymsg]', (e) => e.textContent.includes('空格')))
  await page.$eval('#llm-simple-key', (e) => { e.value = ''; e.dispatchEvent(new Event('input')) })
  await typeKey(page, KEY)
  check(`${tag} auto connect success`, await visible(page, '[data-testid=llm-simple-result-ok]', 15000))
  await shot(page, `03-paste-ok-${tag}`)
  await page.click('[data-testid=llm-simple-save]'); await wait(900)
  check(`${tag} saved -> connected summary`, await page.$eval('[data-testid=llm-summary-title]', (e) => e.textContent.includes('已连接：DeepSeek')))
  await shot(page, `04-connected-${tag}`)
  check(`${tag} no page errors`, errs.length === 0)
  await ctx.close()
}

// 3. failure states with retry (light desktop)
{
  const { page, ctx } = await open('light', 1280, 900, '/settings?mock_llm=none')
  await page.click('[data-mode=llm]').catch(() => {})
  await page.click('[data-testid=llm-simple-card-deepseek]'); await wait(200)
  await typeKey(page, 'sk-bad' + '0'.repeat(20))
  check('401 shows "Key 无效，请重新复制" with HTTP 401 and a retry button', await visible(page, '[data-testid=llm-simple-test-failure]', 15000) && await page.$eval('[data-testid=llm-simple-test-failure]', (e) => e.textContent.includes('Key 无效，请重新复制') && e.textContent.includes('HTTP 401')) && await has(page, '[data-testid=llm-simple-test-failure-retry]'))
  await shot(page, '10-error-401')
  await page.$eval('#llm-simple-key', (e) => { e.value = ''; e.dispatchEvent(new Event('input')) })
  await typeKey(page, 'sk-down' + '0'.repeat(20))
  check('503 shows a provider-problem message with retry', await visible(page, '[data-testid=llm-simple-test-failure]', 15000) && await page.$eval('[data-testid=llm-simple-test-failure]', (e) => e.textContent.includes('HTTP 503')))
  await shot(page, '11-error-503')
  await page.$eval('#llm-simple-key', (e) => { e.value = ''; e.dispatchEvent(new Event('input')) })
  await typeKey(page, 'sk-flaky' + '0'.repeat(20))
  check('flaky: 503 first', await visible(page, '[data-testid=llm-simple-models-failure],[data-testid=llm-simple-test-failure]', 15000))
  await shot(page, '12-error-flaky-before-retry')
  // retry whichever failure is shown until the flow is verified
  for (let i = 0; i < 3 && !(await has(page, '[data-testid=llm-simple-result-ok]')); i++) {
    const btn = await page.$('[data-testid$="-failure-retry"]:not([disabled])')
    if (btn) await btn.click()
    await wait(1500)
  }
  check('retry repeats the failed action and succeeds, no re-typing', await visible(page, '[data-testid=llm-simple-result-ok]', 8000))
  await shot(page, '13-error-flaky-after-retry')
  await page.$eval('#llm-simple-key', (e) => { e.value = ''; e.dispatchEvent(new Event('input')) })
  await typeKey(page, 'sk-limit' + '0'.repeat(20))
  check('429 shows countdown on the retry button', await visible(page, '[data-testid=llm-simple-models-failure]', 15000) && await page.$eval('[data-testid=llm-simple-models-failure-retry]', (e) => /\d+ 秒/.test(e.textContent) && e.disabled))
  await shot(page, '14-error-429-countdown')
  await wait(5500)
  check('countdown ends and retry is enabled', await page.$eval('[data-testid=llm-simple-models-failure-retry]', (e) => !e.disabled && e.textContent.trim() === '重试'))
  await ctx.close()
}

// 4. clipboard chip (needs clipboard permission; Edge headless grants it through overridePermissions)
{
  const { page, ctx } = await open('light', 1280, 900, '/settings?mock_llm=none')
  await page.click('[data-mode=llm]').catch(() => {})
  await page.click('[data-testid=llm-simple-card-deepseek]'); await wait(200)
  await page.evaluate(() => { window.open = () => null })
  await page.click('[data-testid=llm-simple-open-key-page]'); await wait(300)
  await page.evaluate((k) => navigator.clipboard.writeText(k), KEY).catch(() => {})
  await page.evaluate(() => window.dispatchEvent(new Event('focus'))); await wait(800)
  const chip = await visible(page, '[data-testid=llm-simple-clip-offer]', 3000)
  console.log(chip ? 'INFO clipboard chip shown' : 'INFO clipboard chip not shown (headless permission); the manual paste button is still available')
  if (chip) {
    check('chip never filled the box on its own', await page.$eval('#llm-simple-key', (e) => e.value === ''))
    await shot(page, '20-clipboard-chip')
    await page.click('[data-testid=llm-simple-clip-accept]')
    check('accepting the chip pastes and auto-tests', await visible(page, '[data-testid=llm-simple-result-ok]', 15000))
  }
  await ctx.close()
}

// 5. OpenRouter sign-in (mock): button, redirect back, "Key created", and the retry for a failed automatic test
for (const [q, tag] of [['', 'ok'], ['&mock_oauth_test=fail', 'testfail']]) {
  const { page, ctx } = await open('light', 1280, 900, '/settings?mock_llm=none' + q)
  await page.click('[data-mode=llm]').catch(() => {})
  await page.click('[data-testid=llm-simple-card-openrouter]'); await wait(300)
  if (tag === 'ok') await shot(page, '30-openrouter-login')
  await Promise.all([page.waitForNavigation({ waitUntil: 'networkidle0' }), page.click('[data-testid=global-llm-login-start]')])
  await page.waitForSelector('[data-testid=global-llm-card]', { timeout: 15000 })
  check(`oauth ${tag}: Key created with its name`, await visible(page, '[data-testid=global-llm-login-key-name]', 15000) && await page.$eval('[data-testid=global-llm-login-key-name]', (e) => e.textContent.includes('OA Agent')))
  await wait(300)
  if (tag === 'testfail') {
    check('oauth auto-test failure has a retry', await visible(page, '[data-testid=global-llm-login-test-failure-retry]'))
    await shot(page, '32-openrouter-test-failed')
    await page.click('[data-testid=global-llm-login-test-failure-retry]'); await wait(1500)
    check('retry succeeds without re-entering anything', !(await has(page, '[data-testid=global-llm-login-test-failure]')))
  }
  await shot(page, tag === 'ok' ? '31-openrouter-connected' : '33-openrouter-retried')
  await ctx.close()
}

// 6. advanced mode and its retry banners; personal scope
{
  const { page, ctx } = await open('light', 1280, 900, '/settings?mock_llm=global', true)
  await shot(page, '40-advanced-connected')
  await page.evaluate(() => [...document.querySelectorAll('[data-scope=global] button')].find((b) => b.textContent.trim() === '更换')?.click()); await wait(200)
  await page.waitForSelector('#llm-key'); await page.type('#llm-key', 'sk-down' + '0'.repeat(20))
  await page.evaluate(() => [...document.querySelectorAll('[data-scope=global] button')].find((b) => b.textContent.includes('测试连接'))?.click())
  check('advanced: test failure shows HTTP status and retry', await visible(page, '[data-testid=llm-test-failure]', 10000) && await has(page, '[data-testid=llm-test-failure-retry]'))
  await shot(page, '41-advanced-test-failure')
  await page.evaluate(() => [...document.querySelectorAll('button')].find((b) => b.getAttribute('aria-label')?.includes('刷新') || b.textContent.includes('刷新'))?.click())
  await wait(1500)
  await shot(page, '42-advanced-after-refresh')
  await ctx.close()
}
{
  const { page, ctx } = await open('light', 1280, 1000, '/settings?mock_llm=global')
  const card = await page.waitForSelector('[data-testid=personal-llm-card]', { timeout: 10000 }).catch(() => null)
  if (card) { await page.click('[data-testid=personal-llm-simple-card-deepseek]').catch(() => {}); await wait(300); await shot(page, '50-personal-simple', '[data-testid=personal-llm-card]') }
  check('personal scope card exists', !!card)
  await ctx.close()
}
await browser.close()
console.log(failures ? `${failures} FAILED` : 'ALL PASSED')
process.exit(failures ? 1 : 0)
