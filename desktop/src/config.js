'use strict'
// Small JSON config in the data directory (written atomically; unreadable file = defaults).
const fs = require('fs')
const path = require('path')

class Config {
  constructor(dataDir) {
    this.file = path.join(dataDir, 'desktop-config.json')
    this.data = {}
    try {
      const parsed = JSON.parse(fs.readFileSync(this.file, 'utf8'))
      if (parsed && typeof parsed === 'object') this.data = parsed
    } catch {
      /* first run or damaged file */
    }
  }

  get(key, fallback) {
    return this.data[key] === undefined ? fallback : this.data[key]
  }

  set(key, value) {
    this.data[key] = value
    this.save()
  }

  save() {
    try {
      fs.mkdirSync(path.dirname(this.file), { recursive: true })
      const tmp = this.file + '.tmp'
      fs.writeFileSync(tmp, JSON.stringify(this.data, null, 2), 'utf8')
      fs.renameSync(tmp, this.file)
    } catch (e) {
      console.error('config save failed:', e && e.message)
    }
  }
}

module.exports = { Config }
