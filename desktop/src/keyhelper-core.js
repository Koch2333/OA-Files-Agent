'use strict'
// Pure logic of the guided key-creation window ("密钥向导窗口"): host allow-list matching, plan building from a preset,
// clipboard key detection, the step state machine and IPC sender helpers. No Electron here, so it is unit-tested
// (test/keyhelper-core.test.js). The window itself is keyhelper-ctl.js.
//
// Privacy contract (docs/LLM账号登录配置.md): the provider page is never read, scripted or scraped; the only thing the app
// looks at is clipboard TEXT that matches the provider's key format, and it uses it only after the user confirms.

const PRESET_ID_RE = /^[a-z0-9][a-z0-9-]{0,39}$/
// Suffix entries must keep at least two labels (".aliyun.com"); never a bare TLD or a short public suffix like ".com.cn".
const SHORT_SUFFIX_FIRST_LABELS = new Set(['com', 'net', 'org', 'co', 'gov', 'edu', 'ac'])

/** Validates the preset's key_hosts. Entries: exact host, or ".suffix" (suffix itself and any sub-domain). Returns the list or throws. */
function normalizeHostEntries(list) {
  if (!Array.isArray(list) || list.length === 0 || list.length > 12) throw new Error('key_hosts missing')
  const out = []
  for (const raw of list) {
    if (typeof raw !== 'string' || raw !== raw.toLowerCase()) throw new Error('bad host entry')
    const suffix = raw.startsWith('.')
    const bare = suffix ? raw.slice(1) : raw
    if (!/^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$/.test(bare)) throw new Error('bad host entry')
    if (/^\d+(\.\d+){3}$/.test(bare)) throw new Error('IP addresses are not allowed')
    const labels = bare.split('.')
    if (suffix && labels.length === 2 && SHORT_SUFFIX_FIRST_LABELS.has(labels[0])) throw new Error('suffix too broad')
    out.push(raw)
  }
  return out
}

function hostMatches(host, entries) {
  if (typeof host !== 'string' || !host || host.endsWith('.')) return false
  const h = host.toLowerCase()
  return entries.some((e) => (e.startsWith('.') ? h === e.slice(1) || h.endsWith(e) : h === e))
}

/**
 * Test hook (OA_KEYHELPER_TEST_HOSTS="127.0.0.1:9911,localhost:9912"): loopback host:port pairs that may be loaded over
 * plain http from a local fake provider page. Only read from the process environment of the MAIN process, which a web
 * page cannot touch, and ignored completely in packaged builds.
 */
function parseTestHosts(value, packaged) {
  const out = new Set()
  if (packaged || typeof value !== 'string') return out
  for (const part of value.split(',')) {
    const m = /^(127\.0\.0\.1|localhost):(\d{2,5})$/.exec(part.trim().toLowerCase())
    if (m && Number(m[2]) > 0 && Number(m[2]) < 65536) out.add(`${m[1]}:${Number(m[2])}`)
  }
  return out
}

/**
 * The only function that decides what the provider view may load or navigate to. https, no user info, default port,
 * exact host / listed suffix; trailing-dot and IP hosts never match. Returns the normalised href or null.
 */
function checkNavUrl(raw, entries, testHosts = new Set()) {
  if (typeof raw !== 'string' || raw.length > 4096) return null
  let u
  try {
    u = new URL(raw)
  } catch {
    return null
  }
  if (u.username || u.password) return null
  if (testHosts.size > 0 && (u.protocol === 'http:' || u.protocol === 'https:') && testHosts.has(u.host.toLowerCase())) return u.href
  if (u.protocol !== 'https:' || u.port) return null
  if (!hostMatches(u.hostname, entries)) return null
  return u.href
}

const MAX_STEPS = 8
function cleanText(s, max) {
  return typeof s === 'string' ? s.replace(/[\u0000-\u001f\u007f]/g, ' ').trim().slice(0, max) : ''
}

/**
 * Builds the window plan from one preset (as returned by GET /v1/llm/presets of the local backend). Throws a message
 * for presets that cannot be shown in the guided window. The start URL is always the preset's key_url, validated
 * against the preset's own key_hosts.
 */
function buildPlan(preset, testHosts = new Set()) {
  if (!preset || typeof preset !== 'object' || !PRESET_ID_RE.test(String(preset.id))) throw new Error('unknown provider')
  const hosts = normalizeHostEntries(preset.key_hosts)
  const startUrl = checkNavUrl(preset.key_url, hosts, testHosts)
  if (!startUrl) throw new Error('key_url is not on the allow-list')
  let pattern = ''
  if (typeof preset.key_pattern === 'string' && preset.key_pattern.length > 0) {
    if (preset.key_pattern.length > 300) throw new Error('key_pattern too long')
    new RegExp(preset.key_pattern) // throws when invalid
    pattern = preset.key_pattern
  }
  const g = preset.key_guide
  if (!g || !Array.isArray(g.steps) || g.steps.length === 0 || g.steps.length > MAX_STEPS) throw new Error('key_guide missing')
  const steps = g.steps.map((s) => ({ title: cleanText(s.title, 60), hint: cleanText(s.hint, 400), warn: cleanText(s.warn, 300) }))
  if (steps.some((s) => !s.title)) throw new Error('key_guide step without title')
  return {
    id: preset.id,
    name: cleanText(String(preset.name || preset.id).replace(/[（(].*$/, ''), 40),
    startUrl,
    hosts,
    pattern,
    partition: `persist:oa-keyhelper-${preset.id}`,
    keyPathHint: lastSegment(startUrl),
    guide: { loginSteps: Array.isArray(g.login_steps) ? g.login_steps.slice(0, MAX_STEPS).map((s) => ({ title: cleanText(s && s.title, 60), hint: cleanText(s && s.hint, 400), warn: cleanText(s && s.warn, 300) })).filter((s) => s.title) : [], suggestedName: cleanText(g.suggested_name, 40) || 'OA Agent', loginNote: cleanText(g.login_note, 400), costNote: cleanText(g.cost_note, 300), steps },
  }
}

function lastSegment(href) {
  try {
    return new URL(href).pathname.split('/').filter(Boolean).pop() || ''
  } catch {
    return ''
  }
}

// ---------- key detection ----------

/** Same clean-up as the web UI (frontend/src/utils/llmGuide.ts cleanKey). */
function cleanKey(raw) {
  return String(raw).trim().replace(/^["'“”‘’`]+|["'“”‘’`]+$/g, '').replace(/^Bearer\s+/i, '').trim()
}

function maskKey(key) {
  const k = cleanKey(key)
  const m = /^sk-[a-z]+-(v1-)?/i.exec(k)
  const head = m ? m[0] : k.startsWith('sk-') ? 'sk-' : ''
  return `${head}••••${k.slice(-4)}`
}

/** Returns { key, masked } when the clipboard text is exactly one key of the provider's format, else null. */
function detectKey(text, patternSrc) {
  if (typeof text !== 'string' || !patternSrc || text.length === 0 || text.length > 400) return null
  const key = cleanKey(text)
  if (!key || /\s/.test(key) || /[^\x21-\x7e]/.test(key) || key.length < 12) return null
  let re
  try {
    re = new RegExp(patternSrc)
  } catch {
    return null
  }
  if (!re.test(key)) return null
  return { key, masked: maskKey(key) }
}

/** Optional key name typed by the user: printable, <= 80 chars (the backend sanitises again). */
function sanitizeLabel(s) {
  return cleanText(s, 80)
}

// ---------- step state machine (pure) ----------

const initialSteps = (total) => ({ step: 0, total, detected: false })

function stepReducer(s, e) {
  const last = s.total - 1
  switch (e.type) {
    case 'next': return s.detected ? s : { ...s, step: Math.min(s.step + 1, last) }
    case 'prev': return s.detected ? { ...s, detected: false, step: Math.max(last - 1, 0) } : { ...s, step: Math.max(s.step - 1, 0) }
    case 'goto': return Number.isInteger(e.step) && e.step >= 0 && e.step <= last ? { ...s, step: e.step, detected: false } : s
    case 'onKeyPage': return s.detected || s.step >= 2 ? s : { ...s, step: Math.min(2, last - 1) } // the key list page is open: skip the "log in" and "find the page" steps
    case 'detected': return { ...s, detected: true, step: last }
    case 'cleared': return s.detected ? { ...s, detected: false, step: Math.max(last - 1, 0) } : s
    default: return s
  }
}

// ---------- panel header ----------

/** What the panel header shows: host, path (no query/hash), lock. */
function describeUrl(href) {
  try {
    const u = new URL(href)
    return { host: u.host, path: u.pathname.length > 60 ? u.pathname.slice(0, 57) + '…' : u.pathname, secure: u.protocol === 'https:' }
  } catch {
    return { host: '', path: '', secure: false }
  }
}

// ---------- IPC sender checks ----------

/** True when the IPC event comes from exactly this webContents and its main frame URL passes `urlOk`. */
function senderIs(e, wc, urlOk) {
  if (!wc || (typeof wc.isDestroyed === 'function' && wc.isDestroyed()) || !e || e.sender !== wc) return false
  const f = e.senderFrame
  if (!f || typeof f.url !== 'string') return false
  return urlOk(f.url)
}

const PANEL_ACTIONS = new Set([
  'next', 'prev', 'use', 'ignore', 'reload', 'back', 'system-browser', 'clear-data', 'cancel', 'toggle',
  'manage-test', 'manage-replace', 'manage-switch', 'manage-remove-ask', 'manage-remove-cancel', 'manage-remove',
])

function validPanelAction(name) {
  return typeof name === 'string' && PANEL_ACTIONS.has(name)
}

// ---------- sign-in (OAuth) page of the platform, shown in the same sandboxed view ----------
const OAUTH_CALLBACK_PATH = '/v1/llm/oauth/callback'

/** The loopback API callback of the app (the redirect target of the PKCE flow): exact origin of the local backend + exact path. */
function isOauthCallback(raw, appOrigin) {
  if (typeof raw !== 'string' || typeof appOrigin !== 'string' || !appOrigin) return false
  try {
    const u = new URL(raw)
    return u.origin === appOrigin && u.pathname === OAUTH_CALLBACK_PATH && !u.username && !u.password
  } catch {
    return false
  }
}

/**
 * The authorize URL the web UI got from POST /v1/llm/oauth/start: must be on the provider's own allow-list and its
 * callback_url must be exactly this app's loopback callback. Anything else is refused (the renderer is not trusted to
 * send arbitrary URLs).
 */
function checkOauthUrl(raw, entries, testHosts, appOrigin) {
  const href = checkNavUrl(raw, entries, testHosts)
  if (!href) return null
  const cb = new URL(href).searchParams.get('callback_url')
  return cb && isOauthCallback(cb, appOrigin) ? href : null
}

// ---------- management view (existing key): state machine + sanitising of what the main window reports ----------
const initialManage = () => ({ loading: true, busy: '', confirm: false, info: null, message: '', ok: true })

function manageReducer(s, e) {
  switch (e.type) {
    case 'loaded': return { ...s, loading: false, info: e.info, busy: '' }
    case 'failed': return { ...s, loading: false, busy: '', confirm: false, ok: false, message: e.message || '操作没有成功。' }
    case 'testStart': return s.busy ? s : { ...s, busy: 'test', message: '', ok: true, confirm: false }
    case 'testDone': return { ...s, busy: '', ok: !!(e.test && e.test.ok), message: '', info: s.info ? { ...s.info, test: e.test } : s.info }
    case 'removeAsk': return s.busy || !s.info || !s.info.configured ? s : { ...s, confirm: true, message: '' }
    case 'removeCancel': return { ...s, confirm: false }
    case 'removeStart': return s.confirm && !s.busy ? { ...s, confirm: false, busy: 'remove' } : s // a remove without the confirmation step does nothing
    case 'removeDone': return { ...s, busy: '', ok: true, message: e.message || '已删除密钥。', info: s.info ? { ...s.info, configured: false, keyLabel: '', hint: '', test: null } : s.info }
    default: return s
  }
}

function str(v, max) {
  return typeof v === 'string' ? cleanText(v, max) : ''
}
/** Whitelist + clip what the main window answers to a status request (it is data for display only). */
function sanitizeInfo(d) {
  if (!d || typeof d !== 'object') return null
  const t = d.test && typeof d.test === 'object' ? { ok: d.test.ok === true, message: str(d.test.message, 200), at: Number.isFinite(d.test.at) ? d.test.at : 0 } : null
  const presetId = typeof d.presetId === 'string' && PRESET_ID_RE.test(d.presetId) ? d.presetId : ''
  return {
    provider: str(d.provider, 60), presetId, keyLabel: str(d.keyLabel, 80), hint: str(d.hint, 20), model: str(d.model, 120),
    source: d.source === 'oauth' ? 'oauth' : 'manual', configured: d.configured === true, state: str(d.state, 20), test: t,
  }
}
function sanitizeResult(d) {
  if (!d || typeof d !== 'object') return { ok: false, message: '' }
  return { ok: d.ok === true, message: str(d.message, 200), test: d.test ? sanitizeInfo({ test: d.test }).test : null }
}

module.exports = {
  OAUTH_CALLBACK_PATH, isOauthCallback, checkOauthUrl, initialManage, manageReducer, sanitizeInfo, sanitizeResult,

  normalizeHostEntries, hostMatches, parseTestHosts, checkNavUrl, buildPlan, cleanKey, maskKey, detectKey, sanitizeLabel,
  initialSteps, stepReducer, describeUrl, senderIs, validPanelAction, PANEL_ACTIONS,
}
