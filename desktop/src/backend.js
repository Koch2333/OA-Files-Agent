'use strict'
// Owns the oa-agent.exe child (which supervises the document worker and the embedded PostgreSQL).
//
// Shutdown contract: the backend is started with OA_EXIT_ON_STDIN_EOF=1, so closing its stdin makes it stop the worker and
// PostgreSQL (pg_ctl stop -m fast) and exit by itself; the same happens if this process dies. If it has not exited
// in time we fall back to `taskkill /T /F` on the child PID and then stop the database by its own postmaster.pid.
const { spawn, execFile } = require('child_process')
const EventEmitter = require('events')
const fs = require('fs')
const http = require('http')
const net = require('net')
const path = require('path')
const { diagnose } = require('./diagnose')

const ENV_ALLOW = [
  'SystemRoot', 'windir', 'SystemDrive', 'ComSpec', 'PATHEXT', 'PATH', 'Path', 'TEMP', 'TMP', 'USERPROFILE', 'APPDATA', 'LOCALAPPDATA',
  'ProgramData', 'ProgramFiles', 'ProgramFiles(x86)', 'ProgramW6432', 'CommonProgramFiles', 'HOMEDRIVE', 'HOMEPATH', 'USERNAME', 'USERDOMAIN',
  'COMPUTERNAME', 'OS', 'PROCESSOR_ARCHITECTURE', 'NUMBER_OF_PROCESSORS', 'PUBLIC', 'ALLUSERSPROFILE',
  // network settings the LLM/cloud calls may need
  'HTTP_PROXY', 'HTTPS_PROXY', 'NO_PROXY', 'http_proxy', 'https_proxy', 'no_proxy', 'SSL_CERT_FILE',
  // harmless OA_ switches an admin may set on purpose
  'OA_LOG_LEVEL', 'OA_STORAGE', 'OA_LLM_PRESETS_FILE', 'OA_LLM_OAUTH_OPENROUTER_AUTHORIZE_URL', 'OA_LLM_OAUTH_OPENROUTER_EXCHANGE_URL',
]

function cleanEnv(extra) {
  const env = {}
  const lower = new Map(Object.keys(process.env).map((k) => [k.toLowerCase(), k]))
  for (const k of ENV_ALLOW) {
    const real = lower.get(k.toLowerCase())
    if (real && process.env[real] !== undefined) env[real] = process.env[real]
  }
  return Object.assign(env, extra)
}

function freePort(preferred) {
  const tryPort = (p) =>
    new Promise((resolve) => {
      const s = net.createServer()
      s.once('error', () => resolve(0))
      s.listen(p, '127.0.0.1', () => {
        const got = s.address().port
        s.close(() => resolve(got))
      })
    })
  return (async () => {
    if (preferred) {
      const p = await tryPort(preferred)
      if (p) return p
    }
    return tryPort(0)
  })()
}

function httpJson(method, url, body, timeoutMs = 5000) {
  return new Promise((resolve, reject) => {
    const u = new URL(url)
    const payload = body === undefined ? null : Buffer.from(JSON.stringify(body))
    const req = http.request(
      { method, hostname: u.hostname, port: u.port, path: u.pathname + u.search, timeout: timeoutMs, headers: payload ? { 'Content-Type': 'application/json', 'Content-Length': payload.length } : {} },
      (res) => {
        const chunks = []
        res.on('data', (c) => chunks.push(c))
        res.on('end', () => {
          const text = Buffer.concat(chunks).toString('utf8')
          let json
          try {
            json = JSON.parse(text)
          } catch {
            /* not json */
          }
          resolve({ status: res.statusCode, json, text, headers: res.headers })
        })
      },
    )
    req.on('timeout', () => req.destroy(new Error('timeout')))
    req.on('error', reject)
    if (payload) req.write(payload)
    req.end()
  })
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

class Backend extends EventEmitter {
  constructor({ serverDir, dataDir, archiveRoot, logger, preferredPort, startTimeoutMs }) {
    super()
    this.serverDir = serverDir
    this.dataDir = dataDir
    this.archiveRoot = archiveRoot
    this.log = logger
    this.preferredPort = preferredPort
    this.startTimeoutMs = startTimeoutMs || 180000
    this.child = null
    this.port = 0
    this.tail = []
    this.stopping = false
    this.version = ''
  }

  get origin() {
    return `http://127.0.0.1:${this.port}`
  }

  get exe() {
    return path.join(this.serverDir, 'oa-agent.exe')
  }

  recentLog() {
    return this.tail.join('\n')
  }

  pushLog(chunk) {
    const text = chunk.toString('utf8')
    for (const line of text.split(/\r?\n/)) {
      if (!line) continue
      this.log.raw(line)
      this.emit('line', line)
      this.tail.push(line)
      if (this.tail.length > 80) this.tail.shift()
      const m = /oa-agent (\S+) \(/.exec(line)
      if (m) this.version = m[1]
    }
  }

  // Resolves when /healthz answers; rejects with {title, hint, detail}.
  async start() {
    this.stopping = false
    this.tail = []
    if (!fs.existsSync(this.exe)) {
      throw Object.assign(new Error('missing'), { info: { title: '安装文件不完整', hint: `找不到后台程序：${this.exe}。请重新安装。`, detail: '' } })
    }
    fs.mkdirSync(this.dataDir, { recursive: true })
    fs.mkdirSync(this.archiveRoot, { recursive: true })
    this.port = await freePort(this.preferredPort)
    const env = cleanEnv({
      OA_STANDALONE: '1',
      OA_LISTEN_ADDR: `127.0.0.1:${this.port}`,
      OA_OPEN_BROWSER: '0',
      OA_ALLOW_REVEAL: '1',
      OA_ARCHIVE_ROOT: this.archiveRoot,
      OA_DATA_DIR: this.dataDir,
      OA_EXIT_ON_STDIN_EOF: '1',
    })
    this.log.info(`starting backend ${this.exe} on ${this.origin} (data ${this.dataDir}, archive ${this.archiveRoot})`)
    const child = spawn(this.exe, [], { cwd: this.serverDir, env, windowsHide: true, stdio: ['pipe', 'pipe', 'pipe'] })
    this.child = child
    this.lastPid = child.pid
    child.stdin.on('error', () => {})
    child.stdout.on('data', (d) => this.pushLog(d))
    child.stderr.on('data', (d) => this.pushLog(d))
    let exited = null
    child.once('error', (e) => {
      this.pushLog(Buffer.from(`spawn error: ${e.message} (${e.code || ''})`))
      exited = exited || { code: -1, spawnError: e }
    })
    child.once('exit', (code, signal) => {
      exited = { code, signal }
      this.log.info(`backend exited code=${code} signal=${signal}`)
      if (this.child === child) this.child = null
      if (!this.stopping && this.ready) {
        this.ready = false
        this.emit('crashed', { code, signal })
      }
    })
    this.ready = false
    const deadline = Date.now() + this.startTimeoutMs
    while (Date.now() < deadline) {
      if (exited) {
        const detail = this.recentLog()
        throw Object.assign(new Error('backend exited'), { info: { ...diagnose(detail), detail: `退出码 ${exited.code}\n${detail}` } })
      }
      try {
        const r = await httpJson('GET', `${this.origin}/healthz`, undefined, 2000)
        if (r.status === 200) {
          this.ready = true
          return
        }
      } catch {
        /* not up yet */
      }
      await sleep(300)
    }
    const detail = this.recentLog()
    await this.stop()
    throw Object.assign(new Error('timeout'), { info: { ...diagnose(detail), detail: `启动超过 ${Math.round(this.startTimeoutMs / 1000)} 秒仍未就绪\n${detail}` } })
  }

  // First run: complete the backend setup in `local` mode (single user, no login). Request comes from the loopback
  // address, which is the precondition for local mode. Returns Set-Cookie headers (if any) for the UI session.
  async ensureLocalSetup() {
    let st
    try {
      st = await httpJson('GET', `${this.origin}/v1/setup/status`)
    } catch (e) {
      throw new Error(`setup status failed: ${e.message}`)
    }
    if (st.status === 404) {
      this.log.info('backend has no /v1/setup/status (pre-auth build): nothing to set up')
      return []
    }
    if (st.status !== 200) throw new Error(`GET /v1/setup/status -> ${st.status} ${st.text.slice(0, 200)}`)
    const data = (st.json && st.json.data) || {}
    if (data.initialized) {
      this.log.info(`backend already initialised (mode=${data.mode || '?'})`)
      return []
    }
    this.log.info('first run: completing setup in local mode')
    const r = await httpJson('POST', `${this.origin}/v1/setup/complete`, { mode: 'local' }, 30000)
    if (r.status === 409) return [] // lost a race / already set up
    if (r.status < 200 || r.status >= 300) throw new Error(`POST /v1/setup/complete -> ${r.status} ${r.text.slice(0, 300)}`)
    const sc = r.headers['set-cookie']
    return Array.isArray(sc) ? sc : sc ? [sc] : []
  }

  // Graceful stop, with a bounded fallback. Always resolves.
  async stop(graceMs = 90000) {
    const child = this.child
    this.stopping = true
    this.ready = false
    if (child && child.exitCode === null) {
      const done = new Promise((r) => child.once('exit', r))
      try {
        child.stdin.end()
      } catch {
        /* ignore */
      }
      const timedOut = await Promise.race([done.then(() => false), sleep(graceMs).then(() => true)])
      if (timedOut) {
        this.log.warn(`backend did not exit within ${graceMs} ms: taskkill /T /F ${child.pid}`)
        await taskkillTree(child.pid)
        await Promise.race([done, sleep(5000)])
      }
    }
    await this.sweepDatabase()
  }

  // The postmaster is detached from our tree (pg_ctl exits) and a backend that died with log.Fatal leaves its worker
  // behind, so after any exit make sure neither survives. Strays are matched by the old parent PID and install path.
  async sweepDatabase() {
    await this.sweepWorkers()
    const pidFile = path.join(this.dataDir, 'pg', 'postmaster.pid')
    let pid = 0
    try {
      pid = parseInt(fs.readFileSync(pidFile, 'utf8').split(/\r?\n/)[0], 10) || 0
    } catch {
      return
    }
    if (!pid || !pidAlive(pid)) return
    this.log.warn(`postmaster ${pid} still running: pg_ctl stop`)
    const pgctl = path.join(this.serverDir, 'pgsql', 'bin', 'pg_ctl.exe')
    await new Promise((resolve) => execFile(pgctl, ['-D', path.join(this.dataDir, 'pg'), '-m', 'fast', '-w', '-t', '30', 'stop'], { windowsHide: true, timeout: 40000 }, () => resolve()))
    if (pidAlive(pid)) await taskkillTree(pid)
  }

  async sweepWorkers() {
    if (!this.lastPid) return
    const dir = this.serverDir.replace(/'/g, "''")
    const ps = `Get-CimInstance Win32_Process -Filter 'ParentProcessId=${this.lastPid}' | Where-Object { $_.ExecutablePath -and $_.ExecutablePath -like '${dir}*' } | ForEach-Object { Stop-Process -Id $_.ProcessId -Force }`
    await new Promise((resolve) => execFile('powershell.exe', ['-NoProfile', '-NonInteractive', '-Command', ps], { windowsHide: true, timeout: 20000 }, () => resolve()))
  }
}

function pidAlive(pid) {
  try {
    process.kill(pid, 0)
    return true
  } catch (e) {
    return e.code === 'EPERM'
  }
}

function taskkillTree(pid) {
  return new Promise((resolve) => {
    if (!pid) return resolve()
    execFile('taskkill', ['/PID', String(pid), '/T', '/F'], { windowsHide: true, timeout: 15000 }, () => resolve())
  })
}

module.exports = { Backend, cleanEnv, freePort, httpJson }
