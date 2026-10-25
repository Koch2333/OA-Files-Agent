import type { CreateUploadInput, PickedFile, UploadSession } from '../api/types'
import { ApiError } from '../api/types'

/**
 * 可续传的分片上传器（无文件大小/数量上限）。
 * - 内存安全：只用 File.slice 取分片交给 fetch，绝不整体读入文件；
 * - 不在浏览器端计算 SHA-256（会要求读完整个文件）：以服务端摘要为准；
 * - 队列并发（默认 3 个文件并行，每个文件内分片顺序上传）；
 * - 失败自动退避重试；重试前先查询服务端 offset，因此重复分片是幂等的；
 * - 页面刷新/网络中断后可续传：会话 id 按“文件指纹”记在 localStorage（可注入），再次添加同一文件即从服务端 offset 继续；
 * - 单文件失败不影响其它文件；全部完成后才由调用方提交批次。
 */

export const DEFAULT_CHUNK = 8 * 1024 * 1024

export interface UploadTransport {
  create(input: CreateUploadInput): Promise<UploadSession>
  query(id: string): Promise<UploadSession>
  put(id: string, offset: number, chunk: Blob, signal?: AbortSignal): Promise<UploadSession>
  complete(id: string): Promise<UploadSession>
  abort(id: string): Promise<void>
}

export interface ResumeStore {
  get(key: string): { uploadId: string; batchId: string } | null
  set(key: string, v: { uploadId: string; batchId: string }): void
  remove(key: string): void
}

export type FileStatus = 'queued' | 'uploading' | 'retrying' | 'complete' | 'failed' | 'aborted'

export interface FileState {
  key: string
  name: string
  path: string
  size: number
  sent: number
  status: FileStatus
  attempts: number
  error?: string
  uploadId?: string
  /** 该文件是从上次中断处续传的 */
  resumed?: boolean
}

export type UploaderState = 'idle' | 'running' | 'done' | 'failed' | 'aborted'

export interface UploadProgress {
  state: UploaderState
  files: FileState[]
  totalBytes: number
  sentBytes: number
  /** 近几秒的平滑速度（字节/秒） */
  speed: number
  /** 预计剩余秒数；速度未知时 null */
  etaSeconds: number | null
  doneCount: number
  failedCount: number
  activeCount: number
  batchId: string | null
}

export interface UploaderOptions {
  concurrency?: number
  chunkSize?: number
  maxRetries?: number
  backoffMs?: number
  maxBackoffMs?: number
  sleep?: (ms: number, signal?: AbortSignal) => Promise<void>
  now?: () => number
  resume?: ResumeStore | null
  onProgress?: (p: UploadProgress) => void
}

export interface UploadResult { batchId: string; allComplete: boolean; failed: FileState[] }

/** 文件指纹：名称+相对路径+大小+修改时间。同一文件再次添加时用它找到旧会话。 */
export const fingerprint = (p: PickedFile) => `${p.relativePath}|${p.file.size}|${p.file.lastModified ?? 0}`

export function localResumeStore(storage?: Storage | null, prefix = 'oa.upload.'): ResumeStore {
  const st = (): Storage | null => { try { return storage ?? globalThis.localStorage ?? null } catch { return null } }
  return {
    get(key) { try { const raw = st()?.getItem(prefix + key); return raw ? JSON.parse(raw) : null } catch { return null } },
    set(key, v) { try { st()?.setItem(prefix + key, JSON.stringify(v)) } catch { /* 隐私模式/配额：不影响上传，只是不能跨刷新续传 */ } },
    remove(key) { try { st()?.removeItem(prefix + key) } catch { /* ignore */ } },
  }
}

const defaultSleep = (ms: number, signal?: AbortSignal) => new Promise<void>((resolve) => {
  const t = setTimeout(resolve, ms)
  signal?.addEventListener('abort', () => { clearTimeout(t); resolve() }, { once: true })
})

const uuid = () => (globalThis.crypto?.randomUUID?.() ?? '00000000-0000-4000-8000-000000000000'.replace(/0/g, () => ((Math.random() * 16) | 0).toString(16))).toLowerCase()

/** 可重试：网络错误、超时、限速、5xx（507 磁盘不足除外，那是需要人处理的状况）。 */
export function retryable(e: unknown): boolean {
  if (!(e instanceof ApiError)) return !(e instanceof DOMException && e.name === 'AbortError')
  if (e.status === 0 || e.status === 408 || e.status === 429) return true
  if (e.status === 507) return false
  return e.status >= 500
}

export function describeUploadError(e: unknown): string {
  if (e instanceof ApiError) {
    if (e.status === 507) return e.message
    if (e.status === 0) return '网络中断，可稍后重试，将从中断处继续'
    return e.message
  }
  return e instanceof Error ? e.message : '上传失败'
}

class Aborted extends Error {}

export class ImportUploader {
  private files: { pick: PickedFile; st: FileState }[] = []
  private state: UploaderState = 'idle'
  private batchId: string | null = null
  private ctl = new AbortController()
  private samples: { t: number; bytes: number }[] = []
  private readonly o: Required<Omit<UploaderOptions, 'resume' | 'onProgress'>> & { resume: ResumeStore | null; onProgress?: (p: UploadProgress) => void }

  constructor(private readonly tr: UploadTransport, opts: UploaderOptions = {}) {
    this.o = {
      concurrency: Math.max(1, opts.concurrency ?? 3),
      chunkSize: Math.max(64 * 1024, opts.chunkSize ?? DEFAULT_CHUNK),
      maxRetries: opts.maxRetries ?? 6,
      backoffMs: opts.backoffMs ?? 500,
      maxBackoffMs: opts.maxBackoffMs ?? 15000,
      sleep: opts.sleep ?? defaultSleep,
      now: opts.now ?? (() => Date.now()),
      resume: opts.resume === undefined ? localResumeStore() : opts.resume,
      onProgress: opts.onProgress,
    }
  }

  add(items: PickedFile[]) {
    for (const pick of items) {
      const key = fingerprint(pick)
      if (this.files.some((f) => f.st.key === key)) continue
      this.files.push({ pick, st: { key, name: pick.file.name, path: pick.relativePath, size: pick.file.size, sent: 0, status: 'queued', attempts: 0 } })
    }
    this.emit()
  }

  snapshot(): UploadProgress {
    const total = this.files.reduce((n, f) => n + f.st.size, 0)
    const sent = this.files.reduce((n, f) => n + f.st.sent, 0)
    const speed = this.speed()
    const remaining = total - sent
    return {
      state: this.state, files: this.files.map((f) => ({ ...f.st })), totalBytes: total, sentBytes: sent, speed,
      etaSeconds: speed > 0 && remaining > 0 ? Math.ceil(remaining / speed) : remaining === 0 ? 0 : null,
      doneCount: this.files.filter((f) => f.st.status === 'complete').length,
      failedCount: this.files.filter((f) => f.st.status === 'failed').length,
      activeCount: this.files.filter((f) => f.st.status === 'uploading' || f.st.status === 'retrying').length,
      batchId: this.batchId,
    }
  }

  private speed(): number {
    const s = this.samples
    if (s.length < 2) return 0
    const a = s[0], b = s[s.length - 1]
    const dt = (b.t - a.t) / 1000
    return dt > 0 ? Math.max(0, (b.bytes - a.bytes) / dt) : 0
  }

  private emit() {
    const now = this.o.now()
    const sent = this.files.reduce((n, f) => n + f.st.sent, 0)
    this.samples.push({ t: now, bytes: sent })
    while (this.samples.length > 2 && now - this.samples[0].t > 6000) this.samples.shift() // 6 秒滑动窗口
    this.o.onProgress?.(this.snapshot())
  }

  /** 取消：中止在途请求，并删除服务端已接收的数据（暂存批次由调用方另行丢弃）。 */
  async abort() {
    if (this.state === 'aborted') return
    this.state = 'aborted'
    this.ctl.abort()
    await Promise.all(this.files.map(async (f) => {
      if (f.st.status !== 'complete') f.st.status = 'aborted'
      if (f.st.uploadId) { try { await this.tr.abort(f.st.uploadId) } catch { /* 已过期/已删除 */ } }
      this.o.resume?.remove(f.st.key)
    }))
    this.emit()
  }

  /** 开始（或在失败后再次调用以续传）。永不抛出：结果在返回值里。 */
  async start(batchId?: string): Promise<UploadResult> {
    this.state = 'running'
    this.ctl = new AbortController()
    this.batchId = batchId ?? this.batchId
    // 复用上次中断留下的批次 id（任何一个文件有记录即可）
    if (!this.batchId) for (const f of this.files) { const r = this.o.resume?.get(f.st.key); if (r) { this.batchId = r.batchId; break } }
    if (!this.batchId) this.batchId = uuid()
    for (const f of this.files) if (f.st.status === 'failed' || f.st.status === 'aborted') { f.st.status = 'queued'; f.st.error = undefined }
    const queue = this.files.filter((f) => f.st.status !== 'complete')
    this.emit()
    const worker = async () => {
      for (;;) {
        if (this.ctl.signal.aborted) return
        const next = queue.shift()
        if (!next) return
        await this.uploadFile(next)
      }
    }
    await Promise.all(Array.from({ length: Math.min(this.o.concurrency, queue.length) }, worker))
    const failed = this.files.filter((f) => f.st.status === 'failed').map((f) => ({ ...f.st }))
    const allComplete = this.files.every((f) => f.st.status === 'complete')
    if ((this.state as UploaderState) !== 'aborted') this.state = allComplete ? 'done' : 'failed'
    this.emit()
    return { batchId: this.batchId, allComplete, failed }
  }

  private async uploadFile(f: { pick: PickedFile; st: FileState }) {
    const { st } = f
    try {
      st.status = 'uploading'
      let session = await this.openSession(f)
      let attempts = 0
      for (;;) {
        if (this.ctl.signal.aborted) throw new Aborted()
        if (session.complete) break
        let offset = session.offset
        st.sent = offset
        // 全部字节已到达服务端（size 为 0 的文件也走这里）→ 完成
        if (offset >= st.size) {
          try { session = await this.tr.complete(session.id); break } catch (e) {
            const o = offsetOf(e)
            if (o !== null) { session = { ...session, offset: o }; continue }
            throw e
          }
        }
        const end = Math.min(offset + this.o.chunkSize, st.size)
        try {
          session = await this.tr.put(session.id, offset, f.pick.file.slice(offset, end), this.ctl.signal)
          st.sent = session.offset
          st.status = 'uploading'
          attempts = 0
          st.attempts = 0
          this.emit()
        } catch (e) {
          if (this.ctl.signal.aborted) throw new Aborted()
          const o = offsetOf(e)
          if (o !== null) { // 服务端位置与我们不一致：以服务端为准继续（不算失败）
            session = { ...session, offset: o }
            st.sent = o
            this.emit()
            continue
          }
          if (e instanceof ApiError && e.status === 404) { // 会话过期/被清理：从头重建一次
            this.o.resume?.remove(st.key)
            st.uploadId = undefined
            session = await this.openSession(f, true)
            continue
          }
          if (!retryable(e) || ++attempts > this.o.maxRetries) throw e
          st.attempts = attempts
          st.status = 'retrying'
          this.emit()
          await this.o.sleep(Math.min(this.o.maxBackoffMs, this.o.backoffMs * 2 ** (attempts - 1)), this.ctl.signal)
          if (this.ctl.signal.aborted) throw new Aborted()
          // 重试前先问服务端实际收到多少（上一个请求可能已经成功）
          try { session = await this.tr.query(session.id) } catch (qe) { if (qe instanceof ApiError && qe.status === 404) continue; /* 网络仍不通：下一轮重试 */ }
        }
      }
      st.sent = st.size
      st.status = 'complete'
      this.o.resume?.remove(st.key)
      this.emit()
    } catch (e) {
      if (e instanceof Aborted || this.ctl.signal.aborted) { st.status = 'aborted'; this.emit(); return }
      st.status = 'failed'
      st.error = describeUploadError(e)
      this.emit()
    }
  }

  /** 找回旧会话（刷新后续传）或创建新会话。 */
  private async openSession(f: { pick: PickedFile; st: FileState }, fresh = false): Promise<UploadSession> {
    const { st } = f
    if (!fresh) {
      const rec = this.o.resume?.get(st.key)
      if (rec && (!this.batchId || rec.batchId === this.batchId)) {
        try {
          const s = await this.tr.query(rec.uploadId)
          if (s.size === st.size) { st.uploadId = s.id; st.resumed = s.offset > 0 || s.complete; st.sent = s.complete ? s.size : s.offset; return s }
        } catch { /* 会话已不存在：新建 */ }
        this.o.resume?.remove(st.key)
      }
    }
    const s = await this.retryCreate(f)
    st.uploadId = s.id
    this.o.resume?.set(st.key, { uploadId: s.id, batchId: s.batch_id })
    return s
  }

  private async retryCreate(f: { pick: PickedFile; st: FileState }): Promise<UploadSession> {
    let attempts = 0
    for (;;) {
      try {
        return await this.tr.create({ batch_id: this.batchId ?? undefined, name: f.pick.file.name, path: f.pick.relativePath, size: f.pick.file.size })
      } catch (e) {
        if (this.ctl.signal.aborted) throw new Aborted()
        if (!retryable(e) || ++attempts > this.o.maxRetries) throw e
        f.st.status = 'retrying'; f.st.attempts = attempts; this.emit()
        await this.o.sleep(Math.min(this.o.maxBackoffMs, this.o.backoffMs * 2 ** (attempts - 1)), this.ctl.signal)
      }
    }
  }
}

function offsetOf(e: unknown): number | null {
  if (e instanceof ApiError && e.status === 409 && e.code === 'OFFSET_MISMATCH') {
    const o = e.details?.offset
    return typeof o === 'number' ? o : null
  }
  return null
}

export const fmtSpeed = (bps: number) => {
  if (!(bps > 0)) return '—'
  const u = ['B/s', 'KiB/s', 'MiB/s', 'GiB/s']
  let i = 0, v = bps
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++ }
  return `${v >= 100 || i === 0 ? v.toFixed(0) : v.toFixed(1)} ${u[i]}`
}

export const fmtEta = (s: number | null) => {
  if (s === null) return '计算中…'
  if (s < 60) return `${s} 秒`
  if (s < 3600) return `${Math.floor(s / 60)} 分 ${s % 60} 秒`
  return `${Math.floor(s / 3600)} 小时 ${Math.floor((s % 3600) / 60)} 分`
}
