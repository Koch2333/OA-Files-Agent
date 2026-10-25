'use strict'
const test = require('node:test')
const assert = require('node:assert/strict')
const fs = require('node:fs')
const path = require('node:path')
const c = require('../src/keyhelper-core')

const presets = JSON.parse(fs.readFileSync(path.join(__dirname, '..', '..', 'backend', 'internal', 'llm', 'presets.json'), 'utf8'))
const preset = (id) => presets.find((p) => p.id === id)

test('host entries: exact hosts and sane suffixes only', () => {
  assert.deepEqual(c.normalizeHostEntries(['a.example.com', '.aliyun.com']), ['a.example.com', '.aliyun.com'])
  for (const bad of [[], null, ['.com'], ['.com.cn'], ['*.example.com'], ['Example.com'], ['example'], ['1.2.3.4'], ['a.com/path'], ['a.com:8443'], ['u@a.com'], [''], ['.']]) {
    assert.throws(() => c.normalizeHostEntries(bad), undefined, JSON.stringify(bad))
  }
})

test('allow-list: exact hosts and suffixes, no look-alikes', () => {
  const hosts = ['platform.deepseek.com', '.aliyun.com']
  const ok = (u) => c.checkNavUrl(u, hosts) !== null
  assert.ok(ok('https://platform.deepseek.com/api_keys'))
  assert.ok(ok('https://bailian.console.aliyun.com/cn-beijing/model/settings/api-key'))
  assert.ok(ok('https://account.aliyun.com/login/login.htm?oauth_callback=https%3A%2F%2Fexample.com'))
  assert.ok(ok('https://aliyun.com/'))
  for (const u of [
    'https://evil-deepseek.com/', 'https://platform.deepseek.com.evil.example/', 'https://deepseek.com/', 'https://x.platform.deepseek.com/',
    'https://evilaliyun.com/', 'https://aliyun.com.evil.example/', 'https://notaliyun.com/',
    'http://platform.deepseek.com/api_keys', 'https://platform.deepseek.com:8443/', 'https://platform.deepseek.com:443@evil.example/',
    'https://user@platform.deepseek.com/', 'https://user:pw@platform.deepseek.com/', 'https://evil.example\\@platform.deepseek.com/',
    'https://platform.deepseek.com.', 'https://platform.deepseek.com%2eevil.example/',
    'https://xn--platform-deepseek-9ib.com/', 'https://pla\u0442form.deepseek.com/', 'https://１２７.０.０.１/',
    'javascript:alert(1)', 'file:///C:/Windows/win.ini', 'data:text/html,hi', 'blob:https://platform.deepseek.com/x', 'about:blank', 'ftp://platform.deepseek.com/',
    '', null, undefined, 5, 'not a url', 'https://' + 'a'.repeat(5000) + '.aliyun.com/',
  ]) {
    assert.equal(c.checkNavUrl(u, hosts), null, String(u).slice(0, 80))
  }
})

test('test hosts: loopback host:port only, never in packaged builds', () => {
  assert.equal(c.parseTestHosts('127.0.0.1:9911, localhost:9912', true).size, 0)
  assert.equal(c.parseTestHosts(undefined, false).size, 0)
  assert.deepEqual([...c.parseTestHosts('127.0.0.1:9911, localhost:9912, evil.example:80, 10.0.0.1:80, 127.0.0.1, 127.0.0.1:0, 127.0.0.1:99999', false)].sort(), ['127.0.0.1:9911', 'localhost:9912'])
  const th = c.parseTestHosts('127.0.0.1:9911', false)
  assert.ok(c.checkNavUrl('http://127.0.0.1:9911/keys', ['platform.deepseek.com'], th))
  assert.equal(c.checkNavUrl('http://127.0.0.1:9912/keys', ['platform.deepseek.com'], th), null)
  assert.equal(c.checkNavUrl('http://127.0.0.1:9911/keys', ['platform.deepseek.com']), null)
  assert.equal(c.checkNavUrl('http://user@127.0.0.1:9911/keys', ['platform.deepseek.com'], th), null)
})

test('shipped presets: plan for deepseek, qwen and openai starts at key_url on their own allow-list', () => {
  for (const id of ['deepseek', 'qwen', 'openai']) {
    const p = preset(id)
    const plan = c.buildPlan(p)
    assert.equal(plan.startUrl, new URL(p.key_url).href, id)
    assert.equal(plan.partition, `persist:oa-keyhelper-${id}`)
    assert.ok(plan.guide.steps.length >= 4, id)
    assert.equal(plan.guide.suggestedName, 'OA Agent')
    assert.ok(plan.pattern, id)
  }
  assert.deepEqual(preset('deepseek').key_hosts, ['platform.deepseek.com'])
  assert.deepEqual(preset('openai').key_hosts, ['platform.openai.com', 'auth.openai.com'])
  assert.deepEqual(preset('qwen').key_hosts, ['.aliyun.com'])
})

test('plan refuses presets whose key_url is not on their key_hosts, or with broken data', () => {
  const base = { id: 'x', name: 'X', key_url: 'https://a.example.com/k', key_hosts: ['a.example.com'], key_guide: { suggested_name: 'OA Agent', steps: [{ title: 't', hint: 'h' }] } }
  assert.ok(c.buildPlan(base))
  assert.throws(() => c.buildPlan({ ...base, key_url: 'https://evil.test/k' }))
  assert.throws(() => c.buildPlan({ ...base, key_url: 'http://a.example.com/k' }))
  assert.throws(() => c.buildPlan({ ...base, key_hosts: undefined }))
  assert.throws(() => c.buildPlan({ ...base, key_hosts: ['.com'] }))
  assert.throws(() => c.buildPlan({ ...base, id: '../evil' }))
  assert.throws(() => c.buildPlan({ ...base, id: 'persist:x' }))
  assert.throws(() => c.buildPlan({ ...base, key_pattern: '(' }))
  assert.throws(() => c.buildPlan({ ...base, key_guide: { steps: [] } }))
  assert.throws(() => c.buildPlan({ ...base, key_guide: { steps: [{ title: '', hint: '' }] } }))
  assert.throws(() => c.buildPlan(null))
  assert.equal(c.buildPlan({ ...base, key_guide: { suggested_name: 'x', steps: [{ title: 'a\u0000b', hint: 'h' }] } }).guide.steps[0].title, 'a b')
})

test('key detection: provider formats, clean-up and masking (full key never in the mask)', () => {
  const ds = preset('deepseek').key_pattern
  const key = 'sk-' + 'a1B2c3D4'.repeat(4)
  const d = c.detectKey(`  "${key}"\n`, ds)
  assert.equal(d.key, key)
  assert.equal(d.masked, 'sk-••••' + key.slice(-4))
  assert.ok(!d.masked.includes(key.slice(3, 20)))
  assert.equal(c.detectKey('Bearer ' + key, ds).key, key)
  for (const t of ['', 'hello world', key + ' ' + key, key + '\n' + key, 'sk-short', 'x'.repeat(500), 'sk-' + 'ä'.repeat(30), null, undefined, 42]) assert.equal(c.detectKey(t, ds), null, String(t).slice(0, 20))
  assert.equal(c.detectKey(key, ''), null)
  assert.equal(c.detectKey(key, '('), null)
  const oa = preset('openai').key_pattern
  assert.ok(c.detectKey('sk-proj-' + 'Ab_-'.repeat(12), oa))
  assert.ok(c.detectKey('sk-' + 'Ab12'.repeat(8), oa))
  assert.equal(c.detectKey('sk-or-v1-' + 'ab12'.repeat(10), oa), null, 'an OpenRouter key is not an OpenAI key')
  assert.equal(c.maskKey('sk-proj-' + 'z'.repeat(30) + 'WXYZ'), 'sk-proj-••••WXYZ')
  assert.equal(c.maskKey('sk-or-v1-' + 'z'.repeat(30) + 'WXYZ'), 'sk-or-v1-••••WXYZ')
})

test('key label is printable and short', () => {
  assert.equal(c.sanitizeLabel('  OA Agent\n'), 'OA Agent')
  assert.equal(c.sanitizeLabel('a\u0000b\u0007c'), 'a b c')
  assert.equal(c.sanitizeLabel('x'.repeat(200)).length, 80)
  assert.equal(c.sanitizeLabel(undefined), '')
})

test('step machine: manual next/prev, automatic jumps, detection', () => {
  let s = c.initialSteps(5)
  assert.deepEqual(s, { step: 0, total: 5, detected: false })
  s = c.stepReducer(s, { type: 'next' })
  assert.equal(s.step, 1)
  s = c.stepReducer(s, { type: 'prev' })
  s = c.stepReducer(s, { type: 'prev' })
  assert.equal(s.step, 0, 'prev stops at the first step')
  s = c.stepReducer(s, { type: 'onKeyPage' })
  assert.equal(s.step, 2, 'the key page is open: skip login and find-the-page')
  s = c.stepReducer(c.stepReducer(s, { type: 'onKeyPage' }), { type: 'onKeyPage' })
  assert.equal(s.step, 2)
  s = c.stepReducer({ ...s, step: 3 }, { type: 'onKeyPage' })
  assert.equal(s.step, 3, 'never moves backwards')
  s = c.stepReducer(s, { type: 'next' })
  assert.equal(s.step, 4)
  s = c.stepReducer(s, { type: 'next' })
  assert.equal(s.step, 4, 'clamped')
  s = c.stepReducer(c.initialSteps(5), { type: 'detected' })
  assert.deepEqual(s, { step: 4, total: 5, detected: true })
  assert.equal(c.stepReducer(s, { type: 'next' }), s, 'next is ignored while a key is detected')
  s = c.stepReducer(s, { type: 'cleared' })
  assert.deepEqual(s, { step: 3, total: 5, detected: false })
  assert.equal(c.stepReducer(c.initialSteps(5), { type: 'goto', step: 9 }).step, 0)
  assert.equal(c.stepReducer(c.initialSteps(5), { type: 'goto', step: 2 }).step, 2)
  assert.equal(c.stepReducer(c.initialSteps(5), { type: 'nonsense' }).step, 0)
})

test('url header: host and path only, no query or hash, lock follows https', () => {
  assert.deepEqual(c.describeUrl('https://platform.openai.com/api-keys?token=SECRET#frag'), { host: 'platform.openai.com', path: '/api-keys', secure: true })
  assert.equal(c.describeUrl('http://127.0.0.1:9/x').secure, false)
  assert.deepEqual(c.describeUrl('garbage'), { host: '', path: '', secure: false })
})

test('ipc sender validation: exact webContents and exact frame URL', () => {
  const wc = { isDestroyed: () => false }
  const ok = (u) => u === 'file:///app/keyhelper.html'
  assert.equal(c.senderIs({ sender: wc, senderFrame: { url: 'file:///app/keyhelper.html' } }, wc, ok), true)
  assert.equal(c.senderIs({ sender: {}, senderFrame: { url: 'file:///app/keyhelper.html' } }, wc, ok), false, 'other webContents (e.g. the provider page)')
  assert.equal(c.senderIs({ sender: wc, senderFrame: { url: 'https://platform.deepseek.com/' } }, wc, ok), false, 'panel navigated away')
  assert.equal(c.senderIs({ sender: wc, senderFrame: null }, wc, ok), false)
  assert.equal(c.senderIs({ sender: wc }, wc, ok), false)
  assert.equal(c.senderIs(null, wc, ok), false)
  assert.equal(c.senderIs({ sender: wc, senderFrame: { url: 'file:///app/keyhelper.html' } }, null, ok), false)
  assert.equal(c.senderIs({ sender: wc, senderFrame: { url: 'file:///app/keyhelper.html' } }, { isDestroyed: () => true }, ok), false)
  for (const a of ['use', 'next', 'cancel']) assert.equal(c.validPanelAction(a), true)
  for (const a of ['eval', 'executeJavaScript', '', null, 5, 'USE', '__proto__', 'toString']) assert.equal(c.validPanelAction(a), false, String(a))
})

test('window controller source: sandboxed provider view, no scraping, no certificate bypass, no key in logs', () => {
  const src = fs.readFileSync(path.join(__dirname, '..', 'src', 'keyhelper-ctl.js'), 'utf8')
  for (const forbidden of ['executeJavaScript', 'insertCSS', 'capturePage', 'certificate-error', 'setCertificateVerifyProc', 'ignore-certificate', 'session.defaultSession', 'cookies.', 'webSecurity: false', 'nodeIntegration: true', 'sandbox: false', 'contextIsolation: false', 'setUserAgent']) {
    assert.ok(!src.includes(forbidden), `must not contain ${forbidden}`)
  }
  // the provider view is the one built with `session: ses` and has no preload
  const view = /pageView = new WebContentsView\(\{[\s\S]*?\}\)\n/.exec(src)[0]
  assert.ok(view.includes('session: ses') && view.includes('sandbox: true') && view.includes('contextIsolation: true') && view.includes('nodeIntegration: false'))
  assert.ok(!view.includes('preload'))
  // logging never mentions the key variable
  for (const line of src.split('\n').filter((l) => /\blog\.(info|warn|error)\(/.test(l))) assert.ok(!/\$\{[^}]*(key|raw|pending|text)[^}]*\}/i.test(line), line)
  assert.ok(src.includes("will-download") && src.includes('e.preventDefault()'))
  assert.ok(src.includes("permission === 'clipboard-sanitized-write'"))
  assert.ok(src.includes('stopPolling()'))
  const preload = fs.readFileSync(path.join(__dirname, '..', 'src', 'keyhelper-preload.js'), 'utf8')
  assert.ok(!preload.includes('ipcRenderer.send') && !/ipcRenderer\.on\('(?!keyhelper:state)/.test(preload))
})

test('main process: key helper IPC checks the sender and test hosts ignore packaged builds', () => {
  const main = fs.readFileSync(path.join(__dirname, '..', 'src', 'main.js'), 'utf8')
  const h = /ipcMain\.handle\('desktop:open-key-helper'[\s\S]*?\n\}\)/.exec(main)[0]
  assert.ok(h.includes('fromApp(e)'))
  assert.ok(main.includes('parseTestHosts(process.env.OA_KEYHELPER_TEST_HOSTS, app.isPackaged)'))
})

const APP = 'http://127.0.0.1:18080'
const CB = APP + '/v1/llm/oauth/callback'

test('sign-in window: callback recognition is exact (origin + path)', () => {
  assert.ok(c.isOauthCallback(CB + '?code=abc&state=xyz', APP))
  for (const u of [APP + '/settings', APP + '/v1/llm/oauth/callback/x', 'http://127.0.0.1:18081/v1/llm/oauth/callback', 'https://127.0.0.1:18080/v1/llm/oauth/callback',
    'http://evil.example/v1/llm/oauth/callback', 'http://u@127.0.0.1:18080/v1/llm/oauth/callback', 'http://127.0.0.1.evil.example:18080/v1/llm/oauth/callback', '', null, 5]) {
    assert.equal(c.isOauthCallback(u, APP), false, String(u))
  }
  assert.equal(c.isOauthCallback(CB, ''), false)
  assert.equal(c.isOauthCallback(CB, undefined), false)
})

test('sign-in window: authorize URL must be on the provider list and call back to this app only', () => {
  const hosts = ['.openrouter.ai']
  const good = 'https://openrouter.ai/auth?callback_url=' + encodeURIComponent(CB) + '&code_challenge=x&code_challenge_method=S256'
  assert.equal(c.checkOauthUrl(good, hosts, new Set(), APP), new URL(good).href)
  for (const bad of [
    'https://openrouter.ai/auth?callback_url=' + encodeURIComponent('https://evil.example/v1/llm/oauth/callback'),
    'https://openrouter.ai/auth?callback_url=' + encodeURIComponent(APP + '/settings'),
    'https://openrouter.ai/auth?callback_url=' + encodeURIComponent('http://127.0.0.1:1/v1/llm/oauth/callback'),
    'https://openrouter.ai/auth',
    'https://evil.example/auth?callback_url=' + encodeURIComponent(CB),
    'https://openrouter.ai.evil.example/auth?callback_url=' + encodeURIComponent(CB),
    'http://openrouter.ai/auth?callback_url=' + encodeURIComponent(CB),
    '', null,
  ]) assert.equal(c.checkOauthUrl(bad, hosts, new Set(), APP), null, String(bad))
  const th = c.parseTestHosts('127.0.0.1:9911', false)
  assert.ok(c.checkOauthUrl('http://127.0.0.1:9911/auth?callback_url=' + encodeURIComponent(CB), hosts, th, APP))
})

test('shipped presets: OpenRouter has a login view and a key guide; the key-page hosts cover the authorize host', () => {
  const p = preset('openrouter')
  const plan = c.buildPlan(p)
  assert.equal(plan.guide.loginSteps.length, 3)
  assert.ok(plan.guide.steps.length >= 4)
  assert.ok(c.checkNavUrl('https://openrouter.ai/auth', plan.hosts))
  assert.ok(c.checkNavUrl('https://clerk.openrouter.ai/v1/client', plan.hosts))
  assert.equal(c.checkNavUrl('https://accounts.google.com/o/oauth2', plan.hosts), null, 'SSO providers are not allow-listed: system-browser fallback')
  assert.equal(c.buildPlan(preset('deepseek')).guide.loginSteps.length, 0)
})

test('manage view: remove needs the confirmation step, test is single-flight, results are kept', () => {
  const info = c.sanitizeInfo({ provider: 'DeepSeek', presetId: 'deepseek', keyLabel: 'OA Agent', hint: '••••abcd', model: 'deepseek-flash', source: 'manual', configured: true, state: 'ok', test: { ok: true, message: '连接成功', at: 1 } })
  let s = c.manageReducer(c.initialManage(), { type: 'loaded', info })
  assert.equal(s.loading, false)
  assert.equal(c.manageReducer(s, { type: 'removeStart' }).busy, '', 'no remove without the confirmation step')
  s = c.manageReducer(s, { type: 'removeAsk' })
  assert.equal(s.confirm, true)
  assert.equal(c.manageReducer(s, { type: 'removeCancel' }).confirm, false)
  const started = c.manageReducer(s, { type: 'removeStart' })
  assert.equal(started.busy, 'remove')
  assert.equal(started.confirm, false)
  assert.equal(c.manageReducer(started, { type: 'removeStart' }).busy, 'remove')
  const done = c.manageReducer(started, { type: 'removeDone' })
  assert.deepEqual([done.info.configured, done.info.keyLabel, done.info.hint, done.info.test], [false, '', '', null])
  s = c.manageReducer(c.manageReducer(c.initialManage(), { type: 'loaded', info }), { type: 'testStart' })
  assert.equal(s.busy, 'test')
  assert.equal(c.manageReducer(s, { type: 'testStart' }), s, 'single flight')
  assert.equal(c.manageReducer(s, { type: 'removeAsk' }), s, 'cannot ask while busy')
  s = c.manageReducer(s, { type: 'testDone', test: { ok: false, message: '401', at: 5 } })
  assert.deepEqual([s.busy, s.ok, s.info.test.message], ['', false, '401'])
  const none = c.manageReducer(c.manageReducer(c.initialManage(), { type: 'loaded', info: { ...info, configured: false } }), { type: 'removeAsk' })
  assert.equal(none.confirm, false, 'nothing to remove')
})

test('what the settings page reports is whitelisted and clipped', () => {
  const i = c.sanitizeInfo({ provider: 'x'.repeat(500), presetId: '../evil', keyLabel: 'a\u0000b', hint: 'y'.repeat(99), source: 'bogus', configured: 'yes', extra: { key: 'sk-secret' }, test: { ok: 1, message: 'm'.repeat(999), at: 'now' } })
  assert.equal(i.provider.length, 60)
  assert.equal(i.presetId, '')
  assert.equal(i.keyLabel, 'a b')
  assert.equal(i.hint.length, 20)
  assert.equal(i.source, 'manual')
  assert.equal(i.configured, false)
  assert.equal(i.test.ok, false)
  assert.equal(i.test.message.length, 200)
  assert.equal(i.test.at, 0)
  assert.ok(!('extra' in i) && !JSON.stringify(i).includes('sk-secret'))
  assert.equal(c.sanitizeInfo(null), null)
  assert.equal(c.sanitizeInfo('x'), null)
  assert.deepEqual(c.sanitizeResult(null), { ok: false, message: '' })
  assert.equal(c.sanitizeResult({ ok: true, message: 'z'.repeat(300) }).message.length, 200)
  for (const a of ['manage-test', 'manage-replace', 'manage-switch', 'manage-remove-ask', 'manage-remove-cancel', 'manage-remove']) assert.equal(c.validPanelAction(a), true, a)
  assert.equal(c.validPanelAction('manage-anything'), false)
})

test('window controller: the system browser is only opened by the explicit action or the shell allow-list', () => {
  const src = fs.readFileSync(path.join(__dirname, '..', 'src', 'keyhelper-ctl.js'), 'utf8')
  const uses = src.split('\n').filter((l) => l.includes('deps.openExternal('))
  assert.equal(uses.length, 2, uses.join('\n'))
  assert.ok(src.includes("case 'system-browser'"))
  assert.ok(src.includes('manage.busy !== \'remove\'')) // remove only runs after the confirmation step
  assert.ok(src.includes("ipcMain.handle('desktop:keyhelper-reply', (e, id, data) => {\n    if (!deps.fromApp(e)"))
})
