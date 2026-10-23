'use strict'
const $ = (id) => document.getElementById(id)
const gb = (n) => (n >= 1024 ** 4 ? (n / 1024 ** 4).toFixed(1) + ' TB' : n >= 1024 ** 3 ? (n / 1024 ** 3).toFixed(1) + ' GB' : Math.round(n / 1024 ** 2) + ' MB')
let busy = false

function flash(id, text, cls) {
  const el = $(id)
  el.textContent = text || ''
  el.className = 'msg' + (cls ? ' ' + cls : '')
  if (text && cls === 'ok') setTimeout(() => { if (el.textContent === text) el.textContent = '' }, 4000)
}

function render(s) {
  $('arch-path').textContent = s.archiveRoot || '（尚未设置）'
  $('arch-free').textContent = s.archiveFree != null ? `该磁盘剩余空间 ${gb(s.archiveFree)}` : ''
  $('arch-lock').hidden = !s.archiveLocked
  $('b-arch').disabled = busy || s.archiveLocked
  $('b-arch').title = s.archiveLocked ? '工作目录由环境变量 OA_AGENT_ARCHIVE_ROOT 指定，无法在界面中更改。' : ''
  $('data-path').textContent = s.dataDir
  $('data-lock').hidden = !s.dataLocked
  $('b-data').disabled = busy || s.dataLocked
  $('data-note').textContent = s.dataLocked ? (s.portable ? '便携版的数据放在程序旁边的 data 文件夹，随程序一起移动，无需迁移。' : '数据目录由环境变量 OA_AGENT_DATA_DIR 指定，无法在界面中迁移。') : '通常无需更改。迁移会先安全停止内置数据库，校验后自动重启程序。'
  $('o-autostart').checked = !!s.autoStart
  $('o-tray').checked = !!s.minimizeToTray
  if (document.activeElement !== $('o-port')) $('o-port').value = s.fixedPort ? String(s.fixedPort) : ''
  $('port-now').textContent = s.port ? (s.fixedPort && s.fixedPort !== s.port ? `端口 ${s.fixedPort} 被占用，当前改用 ${s.port}` : `当前使用端口 ${s.port}`) : ''
  $('about').textContent = [`版本 ${s.version}${s.portable ? '（便携版）' : ''}`, s.backendVersion ? `后端 ${s.backendVersion}` : '', `日志 ${s.logsDir}`].filter(Boolean).join('　·　')
}

async function call(fn, okMsg, target = 'm-global') {
  if (busy) return null
  busy = true
  flash(target, '请稍候…', 'muted')
  try {
    const r = await fn()
    if (!r) return null
    if (!r.ok) { flash(target, r.error, 'err'); return null }
    if (r.data && typeof r.data === 'object' && 'dataDir' in r.data) render(r.data)
    flash(target, okMsg || '', okMsg ? 'ok' : '')
    return r.data
  } finally {
    busy = false
    const s = await window.oaSettings.state()
    if (s && s.ok) render(s.data)
  }
}

async function init() {
  const s = await window.oaSettings.state()
  if (s && s.ok) render(s.data)
  $('b-arch').addEventListener('click', () => call(() => window.oaSettings.changeArchive()))
  $('b-data').addEventListener('click', () => call(() => window.oaSettings.migrateData()))
  $('b-open-arch').addEventListener('click', () => call(() => window.oaSettings.open('archive')))
  $('b-open-data').addEventListener('click', () => call(() => window.oaSettings.open('data')))
  $('b-logs').addEventListener('click', () => call(() => window.oaSettings.open('logs'), '', 'm-about'))
  $('b-copy').addEventListener('click', () => call(() => window.oaSettings.copyDiagnostics(), '已复制到剪贴板（不含密码或密钥）。', 'm-about'))
  $('o-autostart').addEventListener('change', (e) => call(() => window.oaSettings.set('autoStart', e.target.checked), '已保存。'))
  $('o-tray').addEventListener('change', (e) => call(() => window.oaSettings.set('minimizeToTray', e.target.checked), '已保存。'))
  $('b-port').addEventListener('click', () => {
    const raw = $('o-port').value.trim()
    call(() => window.oaSettings.set('fixedPort', raw === '' ? 0 : Number(raw)), raw === '' || Number(raw) === 0 ? '已改为自动选择端口，服务正在重启。' : '已保存，服务正在使用新端口重启。', 'm-port')
  })
  $('b-close').addEventListener('click', () => window.oaSettings.close())
  window.oaSettings.onChanged(async () => {
    const x = await window.oaSettings.state()
    if (x && x.ok) render(x.data)
  })
  document.addEventListener('keydown', (e) => { if (e.key === 'Escape') window.oaSettings.close() })
}
init()
