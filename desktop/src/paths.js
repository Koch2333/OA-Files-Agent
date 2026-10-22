'use strict'
// Where things live. Everything is derived from environment/CLI so tests can run in a scratch directory.
const fs = require('fs')
const path = require('path')
const { app } = require('electron')

const APP_DIR_NAME = 'OA-Agent'

function exeDir() {
  return path.dirname(process.execPath)
}

// Portable mode: a file named portable.flag next to the executable keeps all data inside the extracted folder.
function isPortable() {
  try {
    return fs.existsSync(path.join(exeDir(), 'portable.flag'))
  } catch {
    return false
  }
}

// Fixed per-user location for the tiny bootstrap files (data-dir pointer, installer preset). Never moves.
function bootstrapDir() {
  const base = process.env.OA_AGENT_BOOTSTRAP_DIR || process.env.LOCALAPPDATA || path.join(app.getPath('home'), 'AppData', 'Local')
  return process.env.OA_AGENT_BOOTSTRAP_DIR ? base : path.join(base, APP_DIR_NAME)
}

function pointerFile() {
  return path.join(bootstrapDir(), 'datadir.json')
}

function readPointer() {
  try {
    const p = JSON.parse(fs.readFileSync(pointerFile(), 'utf8')).dataDir
    return p && path.isAbsolute(p) && fs.existsSync(p) ? p : null
  } catch {
    return null
  }
}

function writePointer(dir) {
  fs.mkdirSync(bootstrapDir(), { recursive: true })
  fs.writeFileSync(pointerFile(), JSON.stringify({ dataDir: dir }, null, 2), 'utf8')
}

// True when the data directory is fixed by the environment or by portable mode (cannot be moved from the UI).
function dataDirLocked() {
  return !!process.env.OA_AGENT_DATA_DIR || isPortable()
}

function resolveDataDir() {
  if (process.env.OA_AGENT_DATA_DIR) return path.resolve(process.env.OA_AGENT_DATA_DIR)
  if (isPortable()) return path.join(exeDir(), 'data')
  return readPointer() || defaultDataDir()
}

function defaultDataDir() {
  return bootstrapDir().endsWith(APP_DIR_NAME) || process.env.OA_AGENT_BOOTSTRAP_DIR ? bootstrapDir() : path.join(bootstrapDir(), APP_DIR_NAME)
}

// install-preset.ini is written by the NSIS installer's "working directory" page (or /ARCHIVE=<dir>): [oa] archiveRoot=...
function readInstallPreset() {
  for (const f of [path.join(bootstrapDir(), 'install-preset.ini'), path.join(exeDir(), 'install-preset.ini')]) {
    try {
      const buf = fs.readFileSync(f)
      const txt = buf[0] === 0xff && buf[1] === 0xfe ? buf.toString('utf16le') : buf.toString('utf8')
      const m = /^\s*archiveRoot\s*=\s*(.+?)\s*$/im.exec(txt.replace(/^\ufeff/, ''))
      if (m && path.isAbsolute(m[1])) return m[1]
    } catch {
      /* none */
    }
  }
  return null
}

// Folder holding oa-agent.exe, web/, worker/, pgsql/, migrations/.
function resolveServerDir() {
  if (process.env.OA_AGENT_SERVER_DIR) return path.resolve(process.env.OA_AGENT_SERVER_DIR)
  if (app.isPackaged) return path.join(process.resourcesPath, 'app-server')
  return path.resolve(__dirname, '..', '..', 'dist', 'server')
}

function defaultArchiveRoot() {
  if (isPortable()) return path.join(exeDir(), '归档')
  let docs
  try {
    docs = app.getPath('documents')
  } catch {
    docs = path.join(app.getPath('home'), 'Documents')
  }
  return path.join(docs, APP_DIR_NAME, '归档')
}

module.exports = {
  APP_DIR_NAME, isPortable, resolveDataDir, defaultDataDir, resolveServerDir, defaultArchiveRoot, exeDir,
  bootstrapDir, readPointer, writePointer, dataDirLocked, readInstallPreset,
}
