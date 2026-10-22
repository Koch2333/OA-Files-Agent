'use strict'
const fs = require('fs')
const path = require('path')

const MAX_BYTES = 5 * 1024 * 1024

// Appends to <logsDir>/desktop.log (rotated once at 5 MB). Never throws.
function createLogger(logsDir) {
  const file = path.join(logsDir, 'desktop.log')
  try {
    fs.mkdirSync(logsDir, { recursive: true })
    if (fs.existsSync(file) && fs.statSync(file).size > MAX_BYTES) fs.renameSync(file, file + '.1')
  } catch {
    /* ignore */
  }
  const write = (level, msg) => {
    const line = `${new Date().toISOString()} [${level}] ${msg}\n`
    try {
      fs.appendFileSync(file, line)
    } catch {
      /* ignore */
    }
  }
  return { file, dir: logsDir, info: (m) => write('info', m), warn: (m) => write('warn', m), error: (m) => write('error', m), raw: (m) => write('backend', m) }
}

module.exports = { createLogger }
