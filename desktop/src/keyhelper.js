'use strict'
// Renders the guide panel from the state pushed by the main process. Text only (textContent), no HTML from data.
const $ = (id) => document.getElementById(id)
const el = (tag, cls, text) => {
  const n = document.createElement(tag)
  if (cls) n.className = cls
  if (text !== undefined) n.textContent = text
  return n
}
let labelTouched = false

const act = (n, a) => window.oaKeyHelper.act(n, a)
const fmtTime = (t) => { try { return new Date(t).toLocaleTimeString('zh-CN', { hour12: false }) } catch { return '' } }

function btn(text, cls, fn, disabled) {
  const b = el('button', cls || '', text)
  if (disabled) b.disabled = true
  b.onclick = fn
  return b
}

// Management view: the saved key of the settings page (provider, key name, hint, last test) and the four actions.
function renderManage(s) {
  const m = $('manage')
  m.replaceChildren()
  const h = el('h2', '', s.provider ? `${s.provider} 的密钥` : '管理密钥')
  h.id = 'mtitle'
  m.append(h)
  const info = s.manage.info
  if (s.manage.loading) m.append(el('p', 'wait', '正在读取当前设置……'))
  else if (!info) m.append(el('div', 'banner w', s.manage.message || '读取不到当前密钥的信息。请回到主窗口的“设置”页面再试。'))
  else if (!info.configured) m.append(el('div', 'card', '现在没有保存任何密钥。可以回到主窗口选择服务商，重新连接。'))
  else {
    const card = el('div', 'card')
    const dl = el('dl', 'kv')
    const row = (k, v, cls) => { dl.append(el('dt', '', k)); dl.append(el('dd', cls || '', v)) }
    row('服务商', info.provider || info.presetId)
    row('密钥名称', info.keyLabel || '（没有填写名称）')
    row('密钥尾号', info.hint || '••••', 'mono')
    row('密钥来源', info.source === 'oauth' ? '登录授权自动创建' : '手动粘贴')
    row('当前模型', info.model || '—', 'mono')
    const t = info.test
    row('上次测试', t ? `${t.ok ? '成功' : '没通过'}${t.message ? '：' + t.message : ''}${t.at ? '（' + fmtTime(t.at) + '）' : ''}` : '这次打开后还没测试过', t ? (t.ok ? 'ok' : 'bad') : '')
    card.append(dl)
    m.append(card)
  }
  if (s.manage.message && info) m.append(el('div', s.manage.ok ? 'banner i' : 'banner w', s.manage.message))
  if (s.manage.confirm) {
    const c = el('div', 'confirm')
    c.setAttribute('role', 'alertdialog')
    c.append(el('div', '', '确定删除已保存的密钥吗？删除后需要重新创建或粘贴密钥才能使用大模型（识别会先改用离线规则）。服务商那边的这把密钥不会被作废，需要的话请到它的网站上删除。'))
    const r = el('div', 'row')
    r.append(btn('确定删除', 'pri', () => act('manage-remove')), btn('先不删', '', () => act('manage-remove-cancel')))
    c.append(r)
    m.append(c)
  }
  const configured = !!(info && info.configured)
  const busy = !!s.manage.busy
  const r = el('div', 'row')
  r.append(
    btn(s.manage.busy === 'test' ? '测试中…' : '重新测试', 'pri', () => act('manage-test'), busy || !configured),
    btn('更换密钥', '', () => act('manage-replace'), busy || !info),
    btn('换一个服务商', '', () => act('manage-switch'), busy),
    btn('删除密钥', '', () => act('manage-remove-ask'), busy || !configured),
  )
  m.append(r)
}

function render(s) {
  document.body.classList.toggle('manage-view', s.view === 'manage')
  $('manage').hidden = s.view !== 'manage'
  $('guideBox').hidden = s.view === 'manage'
  if (s.view === 'manage') {
    document.body.classList.remove('collapsed')
    $('title').textContent = '管理密钥'
    $('test').hidden = !s.testMode
    renderManage(s)
    return
  }
  document.body.classList.toggle('collapsed', !!s.collapsed && s.view !== 'manage')
  $('toggle').textContent = s.collapsed ? '‹' : '›'
  $('toggle').setAttribute('aria-label', s.collapsed ? '展开指引' : '收起指引')
  $('title').textContent = s.view === 'login' ? `${s.provider} · 登录授权` : `${s.provider} · 一步步拿到密钥`
  $('callback').hidden = !s.callbackSeen
  $('lock').textContent = s.url.secure ? '🔒' : '⚠'
  $('lock').title = s.url.secure ? '加密连接（https）' : '不是加密连接'
  $('host').textContent = s.url.host || '…'
  $('path').textContent = s.url.path || ''
  $('back').disabled = !s.canBack
  $('loading').hidden = !s.loading
  $('test').hidden = !s.testMode
  const warn = $('warn')
  warn.hidden = !(s.blocked || s.error)
  warn.replaceChildren()
  if (s.blocked) {
    warn.append(el('b', '', '已拦截：'), document.createTextNode(`页面想打开 ${s.blocked.host}，它不在这个服务商的安全名单里。如果登录需要它（例如用 Google 账号登录），请点“改用系统浏览器”，在那里完成；复制密钥后回到本窗口，这里同样会识别。`))
  } else if (s.error) warn.textContent = s.error
  const note = $('notice')
  note.hidden = !s.notice
  note.textContent = s.notice || ''

  const list = $('steps')
  list.replaceChildren()
  s.steps.forEach((step, i) => {
    const cur = i === s.step
    const li = el('li', cur ? 'cur' : i < s.step ? 'done' : '')
    if (cur) li.setAttribute('aria-current', 'step')
    li.append(el('span', 'n', i < s.step ? '✓' : String(i + 1)))
    const body = el('div', 'body')
    body.append(el('div', 'st', step.title))
    if (cur) {
      body.append(el('div', 'hint', step.hint))
      if (step.warn) body.append(el('div', 'warnbox', step.warn))
      const last = i === s.steps.length - 1
      if (!last && !s.detected) {
        const row = el('div', 'row')
        const next = el('button', 'pri', s.view === 'guide' && i === s.steps.length - 2 ? '我已复制了' : '这一步完成了')
        next.onclick = () => window.oaKeyHelper.act('next')
        row.append(next)
        if (i > 0) {
          const prev = el('button', '', '上一步')
          prev.onclick = () => window.oaKeyHelper.act('prev')
          row.append(prev)
        }
        body.append(row)
      }
    }
    li.append(body)
    list.append(li)
  })

  const det = $('detected')
  det.hidden = !s.detected
  if (s.detected) { $('masked').textContent = s.detected.masked; det.scrollIntoView({ block: 'nearest' }) }
  const lab = $('label')
  if (!labelTouched && document.activeElement !== lab) lab.value = s.suggestedName
  det.classList.add('guideonly')
  $('waiting').hidden = s.view === 'login' || !!s.detected || s.step !== s.steps.length - 1
  $('notes').hidden = !(s.loginNote || s.costNote)
  $('loginNote').textContent = s.loginNote || ''
  $('costNote').textContent = s.costNote || ''
}

$('toggle').onclick = () => window.oaKeyHelper.act('toggle')
$('back').onclick = () => window.oaKeyHelper.act('back')
$('reload').onclick = () => window.oaKeyHelper.act('reload')
$('use').onclick = () => window.oaKeyHelper.act('use', $('label').value)
$('ignore').onclick = () => window.oaKeyHelper.act('ignore')
$('sys').onclick = () => window.oaKeyHelper.act('system-browser')
$('clear').onclick = () => window.oaKeyHelper.act('clear-data')
$('cancel').onclick = () => window.oaKeyHelper.act('cancel')
$('label').addEventListener('input', () => { labelTouched = true })
window.oaKeyHelper.onState(render)
window.oaKeyHelper.init().then((s) => { if (s) render(s) })
