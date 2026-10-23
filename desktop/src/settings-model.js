'use strict'
// Pure helpers for the desktop settings window (no Electron imports, unit-tested).

/** Options the settings window may change; everything else is refused. */
const OPTIONS = {
  autoStart: { type: 'bool', default: false },
  minimizeToTray: { type: 'bool', default: false },
  // 0 = pick a free port automatically (the last one is reused when it is still free)
  fixedPort: { type: 'port', default: 0 },
}

/** Returns the cleaned value, or throws an Error with a user-facing Chinese message. */
function normalizeOption(key, value) {
  const def = OPTIONS[key]
  if (!def) throw new Error('不支持的设置项。')
  if (def.type === 'bool') {
    if (typeof value !== 'boolean') throw new Error('取值必须是“开”或“关”。')
    return value
  }
  if (def.type === 'port') {
    if (value === '' || value === null || value === undefined) return 0
    const n = Number(value)
    if (!Number.isInteger(n)) throw new Error('端口必须是整数。')
    if (n === 0) return 0
    if (n < 1024 || n > 65535) throw new Error('端口范围为 1024–65535（填 0 表示自动选择）。')
    return n
  }
  throw new Error('不支持的设置项。')
}

const fmtBytes = (n) => {
  if (n === null || n === undefined || Number.isNaN(n)) return ''
  if (n >= 1024 ** 4) return (n / 1024 ** 4).toFixed(1) + ' TB'
  if (n >= 1024 ** 3) return (n / 1024 ** 3).toFixed(1) + ' GB'
  return Math.round(n / 1024 ** 2) + ' MB'
}

module.exports = { OPTIONS, normalizeOption, fmtBytes }
