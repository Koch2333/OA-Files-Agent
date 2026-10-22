// Browser E2E for the AI workbench, fully automatic mode, global/personal LLM settings and the ledger decision badge.
// Runs against a RUNNING stack that serves the built frontend (OA_FRONTEND_DIR=dist), real backend + worker + PostgreSQL.
//
//   UI_BASE=http://127.0.0.1:8080 UI_ADMIN_USER=admin UI_ADMIN_PASSWORD=... UI_TOTP_SECRET=BASE32 \
//   UI_FAKE_LLM_URL=http://127.0.0.1:18080/v1 UI_SAMPLES="D:\path\to\tests\samples" node e2e/ui_ai.mjs
//
// Env: UI_BASE, UI_BROWSER, UI_SAMPLES (folder with sample documents), UI_ADMIN_USER / UI_ADMIN_PASSWORD / UI_TOTP_SECRET (omit all three for a
//      local-mode server), UI_FAKE_LLM_URL (REQUIRED: base URL of an OpenAI-compatible fake server, e.g. http://127.0.0.1:18080/v1, answering
//      GET /models and POST /chat/completions incl. tool calls), UI_FAKE_LLM_MODEL (default fake-model), UI_FAKE_LLM_KEY (default sk-fake-e2e),
//      UI_DEV=1 when UI_BASE is a Vite dev server proxying to the real backend (forces real data instead of demo data).
// SIDE EFFECTS: the script switches the GLOBAL LLM to "heuristic" first (to see the unconfigured state), then configures the fake one, toggles
//      the AI policy and restores the policy afterwards. Use a disposable instance.
import fs from 'node:fs'
import path from 'node:path'
import { apiCall, clickText, createRunner, env, launch, login, setValue, sleep, waitGone, waitText } from './lib/common.mjs'

const { check, skip, shots, finish } = createRunner('ui_ai')
const fakeUrl = process.env.UI_FAKE_LLM_URL
if (!fakeUrl) { console.error('UI_FAKE_LLM_URL is required (OpenAI-compatible fake server, e.g. http://127.0.0.1:18080/v1)'); process.exit(2) }
const fakeModel = process.env.UI_FAKE_LLM_MODEL ?? 'fake-model'
const fakeKey = process.env.UI_FAKE_LLM_KEY ?? 'sk-fake-e2e'
const { browser, page, errors } = await launch()
const shot = (n) => page.screenshot({ path: path.join(shots, 'ai-' + n + '.png') })
const go = (p) => page.goto(env.base + p, { waitUntil: 'networkidle0' })
const sel = (id) => `[data-testid="${id}"]`
let originalPolicy = null

try {
  await login(page, check)

  // ---- 1. Not configured: unmissable card, disabled composer, primary CTA for the admin
  const reset = await apiCall(page, 'PUT', '/v1/llm/config', { mode: 'heuristic' })
  check('global LLM reset to heuristic (setup for the "not configured" case)', reset.status === 200, String(reset.status))
  await go('/ai')
  check('no-LLM card is shown', await page.waitForSelector(sel('ai-nollm-card'), { timeout: 15000 }).then(() => true, () => false))
  check('composer is disabled when no LLM', await page.$eval(sel('ai-instruction'), (el) => el.matches(':disabled')))
  check('submit is disabled when no LLM', await page.$eval(sel('ai-submit'), (el) => el.disabled))
  check('CTA text for admin_global', await waitText(page, '去配置全局大模型'))
  await shot('01-no-llm')
  await page.click(sel('ai-nollm-cta'))
  check('CTA goes to /settings#llm', await page.waitForFunction(() => location.pathname === '/settings' && location.hash === '#llm', { timeout: 10000 }).then(() => true, () => false))

  // ---- 2. Configure the global LLM against the fake server
  await page.waitForSelector('[data-scope=global] [data-mode=llm]', { timeout: 15000 })
  check('settings shows both titles for the global card', await waitText(page, '全局大模型（全员共用）'))
  await page.click('[data-scope=global] [data-mode=llm]')
  await page.click('[data-scope=global] [data-preset=custom]')
  await setValue(page, '#llm-base', fakeUrl)
  await setValue(page, '#llm-model', fakeModel)
  await setValue(page, '#llm-key', fakeKey)
  check('test connection', await clickText(page, '[data-scope=global] button', '测试连接'))
  check('test shows success', await page.waitForSelector('[data-scope=global] [data-testid=llm-test]', { timeout: 30000 }).then(() => true, () => false) && await waitText(page, '连接成功'))
  check('save and apply', await clickText(page, '[data-scope=global] button', '保存并生效'))
  check('saved toast', await waitText(page, '已生效'))
  await shot('02-settings-llm')
  const st = await apiCall(page, 'GET', '/v1/llm/status')
  check('status API: configured via global', st.data?.configured === true && st.data?.source !== 'none', JSON.stringify(st.data))
  const ackText = st.data?.auto_ack_text ?? ''
  const ackVersion = st.data?.auto_ack_version ?? ''

  // ---- 3. Workbench in review mode
  await go('/ai')
  check('chip shows the source and model', await waitText(page, `将使用：`) && await waitText(page, fakeModel))
  check('composer enabled', await page.$eval(sel('ai-instruction'), (el) => !el.disabled))
  const names = fs.readdirSync(env.samples, { withFileTypes: true }).filter((e) => e.isFile() && /\.(docx|xlsx|pdf|png|jpe?g)$/i.test(e.name)).slice(0, 6).map((e) => path.join(env.samples, e.name))
  const input = await page.$('input[type=file][multiple]')
  await input.uploadFile(...names)
  check('picked files listed', await waitText(page, `${names.length} 个文件`))
  const instr = '只处理采购相关文件，按项目名归档'
  await setValue(page, sel('ai-instruction'), instr)
  check('counter shows the length', await page.$eval(sel('instr-count'), (el) => el.textContent.replace(/\s/g, '') === `${instr.length}/2000`))
  check('example chip fills the text', await (async () => { await page.click(sel('ai-example')); return page.$eval(sel('ai-instruction'), (el) => el.value.length > instr.length) })())
  check('review mode is the default', await page.$eval(sel('mode-review'), (el) => el.getAttribute('aria-pressed') === 'true'))
  await shot('03-composer')
  await page.click(sel('ai-submit'))
  check('task card appears with the instruction and review badge', await page.waitForFunction((t) => [...document.querySelectorAll('[data-testid=task-instruction]')].some((e) => e.textContent.includes(t)), { timeout: 30000 }, instr).then(() => true, () => false))
  check('card shows 人工审核 and model/source', await page.evaluate(() => { const c = document.querySelector('[data-testid=ai-task-list] article'); return !!c && c.innerText.includes('人工审核') && /全局|个人/.test(c.innerText) }))
  await shot('04-review-task')

  // ---- 4. Automatic mode: warning dialog
  await go('/ai')
  await (await page.$('input[type=file][multiple]')).uploadFile(...names)
  await page.waitForSelector(sel('mode-auto'))
  check('auto option enabled (policy on, LLM configured)', await page.$eval(sel('mode-auto'), (el) => !el.disabled))
  await page.click(sel('mode-auto'))
  check('warning dialog is an alertdialog', await page.waitForSelector('[role=alertdialog]', { timeout: 5000 }).then(() => true, () => false))
  check('dialog heading', await waitText(page, '由大模型自动决策，可能出错'))
  if (ackText) check('ack text shown verbatim', await page.$eval(sel('auto-ack-text'), (el, t) => el.textContent === t, ackText))
  else skip('ack text verbatim', 'status had no auto_ack_text')
  check('confirm disabled initially', await page.$eval(sel('auto-confirm'), (el) => el.disabled))
  await page.keyboard.press('Escape')
  check('Esc cancels and the dialog closes', await waitGone(page, '[role=alertdialog]'))
  check('mode reverted to review', await page.$eval(sel('mode-review'), (el) => el.getAttribute('aria-pressed') === 'true'))
  await page.click(sel('mode-auto'))
  await page.waitForSelector('[role=alertdialog]')
  await page.click(sel('auto-ack-check'))
  check('still disabled right after ticking (3 s delay)', await page.$eval(sel('auto-confirm'), (el) => el.disabled))
  await shot('05-auto-dialog')
  await sleep(3300)
  check('enabled after 3 s with the box ticked', await page.$eval(sel('auto-confirm'), (el) => !el.disabled))
  await page.click(sel('auto-confirm'))
  check('auto mode selected', await page.waitForFunction(() => document.querySelector('[data-testid=mode-auto]')?.getAttribute('aria-pressed') === 'true', { timeout: 5000 }).then(() => true, () => false))
  check('acknowledgement is not stored in browser storage', await page.evaluate((v) => { const dump = JSON.stringify({ ...localStorage }) + JSON.stringify({ ...sessionStorage }); return !dump.includes(v || '\u0000') && !/auto_ack/i.test(dump) }, ackVersion))

  // ---- 5. Automatic run + result summary
  const before = await page.$$eval('[data-testid=ai-task-list] article', (a) => a.length)
  await page.click(sel('ai-submit'))
  check('an automatic task card appears', await page.waitForFunction((n) => document.querySelectorAll('[data-testid=ai-task-list] article').length > n && document.querySelector('[data-testid=ai-task-list] article [data-testid=mode-badge]')?.textContent.includes('全自动'), { timeout: 30000 }, before).then(() => true, () => false))
  check('automatic result summary appears (archived N / left for review M)', await page.waitForSelector(sel('auto-result'), { timeout: 240000 }).then(() => true, () => false))
  check('summary text', await waitText(page, '已归档') && await waitText(page, '留待人工审核'))
  await shot('06-auto-result')
  const autoTaskLink = await page.$eval('[data-testid=ai-task-list] article a[href^="/tasks/"]', (a) => a.getAttribute('href')).catch(() => null)
  if (autoTaskLink) {
    await go(autoTaskLink)
    check('task page shows the persistent automatic-mode banner', await page.waitForSelector(sel('auto-banner'), { timeout: 15000 }).then(() => true, () => false) && await waitText(page, '本任务为全自动模式：由模型'))
    await shot('07-task-auto-banner')
  }

  // ---- 6. Ledger badge and the "only automatic" filter
  await go('/ledger')
  check('ledger has the decision column', await waitText(page, '决策方式'))
  check('ledger shows an automatic badge', await page.waitForFunction(() => [...document.querySelectorAll('[data-testid=decision-badge]')].some((b) => b.textContent.includes('全自动')), { timeout: 20000 }).then(() => true, () => false))
  await page.click(sel('ledger-only-auto'))
  await sleep(800)
  check('only-auto filter leaves only automatic rows', await page.$$eval('[data-testid=decision-badge]', (b) => b.length > 0 && b.every((x) => x.textContent.includes('全自动'))))
  await shot('08-ledger-auto')

  // ---- 7. Undo from the workbench (confirmation dialog)
  await go('/ai')
  if (await page.waitForSelector(sel('auto-undo'), { timeout: 15000 }).then(() => true, () => false)) {
    await page.click(sel('auto-undo'))
    check('rollback confirmation dialog opens', await waitText(page, '撤销本次全自动归档？'))
    await page.keyboard.press('Escape')
    check('Esc closes the confirmation without undoing', await waitGone(page, '[role=dialog]'))
  } else skip('undo shortcut', 'no archived files in the automatic task (everything left for review)')

  // ---- 8. Personal LLM card visibility follows the policy
  const pol = await apiCall(page, 'GET', '/v1/admin/ai-policy')
  originalPolicy = pol.data
  await apiCall(page, 'PATCH', '/v1/admin/ai-policy', { allow_personal_llm: false })
  await go('/settings#llm')
  await waitText(page, '全局大模型（全员共用）')
  check('personal card hidden when the policy forbids it', !(await page.$('[data-testid=personal-llm-card]')))
  check('AI policy card visible for admin', !!(await page.$('[data-testid=ai-policy-card]')))
  await apiCall(page, 'PATCH', '/v1/admin/ai-policy', { allow_personal_llm: true })
  await go('/settings#llm')
  check('personal card visible when allowed', await page.waitForSelector('[data-testid=personal-llm-card]', { timeout: 15000 }).then(() => true, () => false))
  check('personal card explains the stricter address rule', await waitText(page, '地址规则比全局更严格'))
  await page.waitForSelector('#pllm-base')
  await page.click('[data-scope=personal] [data-preset=custom]')
  await setValue(page, '#pllm-base', 'http://127.0.0.1:9/v1')
  await setValue(page, '#pllm-model', 'x')
  await clickText(page, '[data-scope=personal] button', '测试连接')
  check('private address is rejected for personal API by default (422 explanation)', await waitText(page, '内网/本机地址', 20000))
  await shot('09-personal-llm')
} catch (e) {
  check('script ran to completion', false, String(e?.stack ?? e))
  await shot('99-failure').catch(() => undefined)
} finally {
  if (originalPolicy) await apiCall(page, 'PATCH', '/v1/admin/ai-policy', { allow_personal_llm: originalPolicy.allow_personal_llm, allow_personal_private_hosts: originalPolicy.allow_personal_private_hosts, allow_auto_archive: originalPolicy.allow_auto_archive }).catch(() => undefined)
  await browser.close()
  finish(errors)
}
