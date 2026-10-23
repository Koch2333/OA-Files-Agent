'use strict'
const $ = (id) => document.getElementById(id)
const gb = (n) => (n >= 1024 ** 3 ? (n / 1024 ** 3).toFixed(1) + ' GB' : Math.round(n / 1024 ** 2) + ' MB')
let ctx = null
const state = { arch: { ok: false }, data: { ok: true } }
let timer = {}

async function check(kind) {
  const input = $(kind === 'archive' ? 'arch' : 'data')
  const box = $(kind === 'archive' ? 'm-arch' : 'm-data')
  const key = kind === 'archive' ? 'arch' : 'data'
  if (kind === 'data' && input.value.trim() === ctx.currentData) {
    state.data = { ok: true }
    box.textContent = ''
    return refresh()
  }
  const r = await window.oaSetup.validate(kind, input.value)
  state[key] = r
  const parts = []
  if (r.errors.length) parts.push(`<span class="err">${r.errors.map(esc).join('<br>')}</span>`)
  r.warnings.forEach((w) => parts.push(`<span class="warn">注意：${esc(w)}</span>`))
  if (r.ok) parts.push(`<span class="ok">可用</span>${r.freeBytes != null ? `<span class="muted">　剩余空间 ${gb(r.freeBytes)}</span>` : ''}`)
  box.innerHTML = parts.join('<br>')
  refresh()
}
const esc = (s) => String(s).replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' })[c])
function refresh() {
  $('b-ok').disabled = !(state.arch.ok && state.data.ok)
  if (ctx && ctx.mode === 'change') $('modes').style.display = state.arch.ok && $('arch').value.trim() !== ctx.currentArchive ? 'block' : 'none'
}
function later(kind) {
  clearTimeout(timer[kind])
  timer[kind] = setTimeout(() => check(kind), 250)
}
async function init() {
  ctx = await window.oaSetup.init()
  if (ctx.mode === 'change') {
    $('title').textContent = '更改工作目录'
    $('lead').textContent = `当前：${ctx.currentArchive}。选择新的归档文件夹，然后选择如何处理已有的归档文件。`
    $('b-def').style.display = 'none'
    $('adv').style.display = 'none'
    $('arch').value = ''
  } else {
    $('lead').textContent = (ctx.note ? ctx.note + '\n' : '') + '归档后的文件会按“部门/类型/日期”存放到这个文件夹，建议选择空间充足的磁盘。以后可在应用内“设置”中修改。'
    $('arch').value = ctx.defaultArchive
    $('b-def').addEventListener('click', () => submit(true))
    if (!ctx.canData) $('adv').style.display = 'none'
    $('data').value = ctx.currentData
  }
  $('arch').addEventListener('input', () => later('archive'))
  $('data').addEventListener('input', () => later('data'))
  $('b-arch').addEventListener('click', async () => {
    const p = await window.oaSetup.browse('archive')
    if (p) { $('arch').value = p; check('archive') }
  })
  $('b-data').addEventListener('click', async () => {
    const p = await window.oaSetup.browse('data')
    if (p) { $('data').value = p; check('data') }
  })
  $('b-ok').addEventListener('click', () => submit(false))
  $('b-cancel').addEventListener('click', () => window.oaSetup.cancel())
  if (ctx.mode !== 'change') check('archive')
  refresh()
}
async function submit(useDefault) {
  if (useDefault) {
    $('arch').value = ctx.defaultArchive
    $('data').value = ctx.currentData
    await check('archive')
    if (!state.arch.ok) return
  }
  const mode = (document.querySelector('input[name=mode]:checked') || {}).value || 'use'
  await window.oaSetup.submit({ archive: $('arch').value.trim(), data: $('data').value.trim(), mode })
}
init()
