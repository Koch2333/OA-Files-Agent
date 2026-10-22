'use strict'
// Pure(ish) Node logic for the working (archive) directory and the data directory: validation, copy-with-verify and
// the switch/rollback orchestration. No Electron imports so it can be unit-tested.
const fs = require('fs')
const fsp = require('fs/promises')
const path = require('path')
const { execFile } = require('child_process')

const isInside = (child, parent) => {
  const rel = path.relative(path.resolve(parent), path.resolve(child))
  return rel === '' || (!rel.startsWith('..') && !path.isAbsolute(rel))
}
const same = (a, b) => path.resolve(a).toLowerCase() === path.resolve(b).toLowerCase()

function nearestExisting(p) {
  let cur = path.resolve(p)
  for (;;) {
    if (fs.existsSync(cur)) return cur
    const up = path.dirname(cur)
    if (up === cur) return cur
    cur = up
  }
}

function freeBytes(p) {
  try {
    const s = fs.statfsSync(nearestExisting(p))
    return Number(s.bavail) * Number(s.bsize)
  } catch {
    return null
  }
}

function driveKind(p) {
  return new Promise((resolve) => {
    if (/^\\\\/.test(p)) return resolve('network')
    const m = /^([A-Za-z]):/.exec(p)
    if (!m || process.platform !== 'win32') return resolve('fixed')
    const ps = `(Get-CimInstance Win32_LogicalDisk -Filter "DeviceID='${m[1].toUpperCase()}:'").DriveType`
    execFile('powershell.exe', ['-NoProfile', '-NonInteractive', '-Command', ps], { windowsHide: true, timeout: 8000 }, (err, out) => {
      const t = parseInt(String(out).trim(), 10)
      resolve(err ? 'fixed' : t === 4 ? 'network' : t === 2 ? 'removable' : 'fixed')
    })
  })
}

/**
 * Validate a candidate folder. ctx: { installDirs: string[], dataDir, protectedDirs?: string[], exactProtected?: string[], kind: 'archive'|'data' }
 * Returns { ok, errors[], warnings[], freeBytes, resolved }.
 */
async function validateDir(input, ctx = {}) {
  const errors = []
  const warnings = []
  const raw = String(input || '').trim().replace(/^"|"$/g, '')
  if (!raw) return { ok: false, errors: ['请选择一个文件夹。'], warnings, freeBytes: null, resolved: '' }
  if (!path.isAbsolute(raw) || (process.platform === 'win32' && !/^([A-Za-z]:[\\/]|\\\\[^\\]+\\[^\\]+)/.test(raw))) errors.push('请输入完整的绝对路径（例如 D:\\OA归档）。')
  if (raw.split(/[\\/]+/).includes('..')) errors.push('路径中不能包含“..”。')
  const resolved = path.resolve(raw)
  if (!errors.length) {
    if (path.parse(resolved).root.toLowerCase() === resolved.toLowerCase() || path.parse(resolved).root.toLowerCase() === resolved.toLowerCase() + path.sep) errors.push('不能直接使用磁盘根目录，请新建一个专用文件夹。')
    for (const d of ctx.protectedDirs || []) if (d && isInside(resolved, d)) errors.push(`不能使用系统或程序目录（${d}）。`)
    for (const d of ctx.exactProtected || []) if (d && same(resolved, d)) errors.push(`请选择专用的子文件夹，而不是 ${d} 本身。`)
    for (const d of ctx.installDirs || []) if (d && isInside(resolved, d)) errors.push('不能放在程序安装目录内（升级或卸载时可能被清除）。')
    if (ctx.kind === 'archive' && ctx.dataDir && isInside(resolved, ctx.dataDir)) errors.push('归档文件夹不能放在数据目录内。')
    if (ctx.kind === 'data' && ctx.archiveRoot && (isInside(resolved, ctx.archiveRoot) || isInside(ctx.archiveRoot, resolved))) errors.push('数据目录与归档文件夹不能互相包含。')
  }
  let free = null
  if (!errors.length) {
    // writable? create nothing permanent: probe in the nearest existing ancestor (or the folder itself)
    const probeDir = nearestExisting(resolved)
    try {
      const probe = path.join(probeDir, `.oa-write-test-${process.pid}-${Date.now()}`)
      fs.writeFileSync(probe, 'x')
      fs.rmSync(probe, { force: true })
    } catch (e) {
      errors.push(`没有写入权限：${e.code || e.message}`)
    }
    if (fs.existsSync(resolved) && !fs.statSync(resolved).isDirectory()) errors.push('该路径已存在且不是文件夹。')
    free = freeBytes(resolved)
    if (free !== null && free < 1024 ** 3) warnings.push('所在磁盘剩余空间不足 1 GB。')
    const kind = await driveKind(resolved)
    if (kind === 'network') warnings.push('这是网络位置：断网或休眠后可能无法访问，速度也较慢。')
    if (kind === 'removable') warnings.push('这是可移动磁盘：拔出后归档文件将不可用。')
  }
  return { ok: errors.length === 0, errors, warnings, freeBytes: free, resolved }
}

async function walk(root) {
  const out = []
  const stack = [''] // relative dirs
  while (stack.length) {
    const rel = stack.pop()
    const ents = await fsp.readdir(path.join(root, rel), { withFileTypes: true })
    for (const e of ents) {
      const r = path.join(rel, e.name)
      if (e.isDirectory()) {
        out.push({ rel: r, dir: true })
        stack.push(r)
      } else if (e.isFile()) {
        const st = await fsp.stat(path.join(root, r))
        out.push({ rel: r, dir: false, size: st.size, mtime: st.mtime })
      } // symlinks/special files are skipped on purpose
    }
  }
  return out
}

/**
 * Copy src -> dst (dst must not exist or be empty), verifying every file's size and a final full pass.
 * Never deletes anything. onProgress({files, totalFiles, bytes, totalBytes}).
 */
async function copyVerify(src, dst, { onProgress, signal, exclude } = {}) {
  src = path.resolve(src)
  dst = path.resolve(dst)
  if (isInside(dst, src) || isInside(src, dst)) throw new Error('源文件夹与目标文件夹不能互相包含。')
  if (!fs.existsSync(src)) {
    await fsp.mkdir(dst, { recursive: true })
    return { files: 0, bytes: 0 }
  }
  if (fs.existsSync(dst) && (await fsp.readdir(dst)).length > 0) throw Object.assign(new Error('目标文件夹不是空的。'), { code: 'TARGET_NOT_EMPTY' })
  const entries = (await walk(src)).filter((e) => !exclude || !exclude(e.rel))
  const files = entries.filter((e) => !e.dir)
  const totalBytes = files.reduce((n, f) => n + f.size, 0)
  const free = freeBytes(dst)
  if (free !== null && free < totalBytes * 1.02 + 64 * 1024 * 1024) throw Object.assign(new Error('目标磁盘剩余空间不足。'), { code: 'NO_SPACE' })
  await fsp.mkdir(dst, { recursive: true })
  for (const d of entries.filter((e) => e.dir)) await fsp.mkdir(path.join(dst, d.rel), { recursive: true })
  let n = 0
  let bytes = 0
  for (const f of files) {
    if (signal && signal.aborted) throw new Error('已取消。')
    const to = path.join(dst, f.rel)
    await fsp.copyFile(path.join(src, f.rel), to, fs.constants.COPYFILE_EXCL)
    const st = await fsp.stat(to)
    if (st.size !== f.size) throw new Error(`校验失败（大小不一致）：${f.rel}`)
    await fsp.utimes(to, f.mtime, f.mtime).catch(() => {})
    n += 1
    bytes += f.size
    if (onProgress) onProgress({ files: n, totalFiles: files.length, bytes, totalBytes })
  }
  // final independent verification pass
  for (const f of files) {
    const st = await fsp.stat(path.join(dst, f.rel)).catch(() => null)
    if (!st || st.size !== f.size) throw new Error(`最终校验失败：${f.rel}`)
  }
  return { files: n, bytes }
}

/**
 * Switch the working directory with rollback. Callbacks (all async):
 *   stopBackend(), copy(old,new), setRoot(path), startBackend() -> boolean
 * mode: 'migrate' | 'use'. Never deletes the old directory. Returns { ok, stage?, error?, restored? }.
 */
async function switchWorkdir({ oldRoot, newRoot, mode, stopBackend, copy, setRoot, startBackend }) {
  await stopBackend()
  if (mode === 'migrate') {
    try {
      await copy(oldRoot, newRoot)
    } catch (error) {
      const restored = await startBackend()
      return { ok: false, stage: 'copy', error, restored }
    }
  }
  setRoot(newRoot)
  let started = false
  try {
    started = await startBackend()
  } catch {
    started = false
  }
  if (!started) {
    setRoot(oldRoot)
    const restored = await startBackend()
    return { ok: false, stage: 'start', error: new Error('使用新目录启动失败'), restored }
  }
  return { ok: true }
}

module.exports = { validateDir, copyVerify, switchWorkdir, isInside, freeBytes, nearestExisting, walk }
