import { ref } from 'vue'
import { api as defaultApi, ApiError, type Api, type ImportJob, type ImportOptions, type PathImportStatus, type PickedFile } from '../api'
import { desktop as defaultDesktop, type DesktopBridge } from './desktop'
import { fingerprint, ImportUploader, localResumeStore, type ResumeStore, type UploadProgress, type UploaderOptions } from './uploader'

/**
 * 导入提交的统一入口（无大小/数量上限）：
 *  - 小批量（≤100 个文件且 ≤32 MiB）仍用一次 multipart 请求（一次往返、原子）；
 *  - 其余用可续传分片上传：逐文件会话 → 全部完成后提交批次，得到一个任务；
 *  - 桌面版（local 模式）拿得到本机路径的文件走“本机路径导入”，完全不经过 HTTP 上传。
 * 失败后再次调用同一批文件即从服务端已收到的位置续传（会话 id 按文件指纹记在 localStorage）。
 */

export const SMALL_BATCH_FILES = 100
export const SMALL_BATCH_BYTES = 32 * 1024 * 1024

export type ImportPhase = 'preparing' | 'uploading' | 'copying' | 'committing' | 'done'
export interface ImportProgress {
  phase: ImportPhase
  upload: UploadProgress | null
  /** 本机路径导入（桌面版）的进度 */
  copy: PathImportStatus | null
}

export interface ImportOpts { templateId?: string | null; options?: ImportOptions; idempotencyKey?: string }
export interface ImportDeps { api?: Api; desktop?: () => DesktopBridge | null; resume?: ResumeStore | null; uploader?: UploaderOptions; pollMs?: number; sleep?: (ms: number) => Promise<void> }

const uuid = () => (globalThis.crypto?.randomUUID?.() ?? '').toLowerCase()

export interface ImportRun { promise: Promise<ImportJob>; cancel(): Promise<void> }

const norm = (p: string) => p.replace(/\\/g, '/')

/** 桌面版：哪些文件有真实路径，且是“单个文件”（相对路径 = 文件名；带目录结构的文件走分片上传以保留结构）。 */
export function splitDesktopPaths(picked: PickedFile[], d: DesktopBridge | null): { paths: string[]; direct: PickedFile[]; rest: PickedFile[] } {
  if (!d?.pathsForFiles) return { paths: [], direct: [], rest: picked }
  let abs: string[] = []
  try { abs = d.pathsForFiles(picked.map((p) => p.file)) } catch { abs = [] }
  const direct: PickedFile[] = [], rest: PickedFile[] = [], paths: string[] = []
  picked.forEach((p, i) => {
    const a = abs[i]
    if (a && norm(a).split('/').pop() === p.file.name && p.relativePath === p.file.name) { direct.push(p); paths.push(a) } else rest.push(p)
  })
  return { paths, direct, rest }
}

export function runImport(picked: PickedFile[], opts: ImportOpts, onProgress: (p: ImportProgress) => void, deps: ImportDeps = {}): ImportRun {
  const api = deps.api ?? defaultApi
  const d = (deps.desktop ?? defaultDesktop)()
  const sleep = deps.sleep ?? ((ms: number) => new Promise<void>((r) => setTimeout(r, ms)))
  const pollMs = deps.pollMs ?? 500
  let uploader: ImportUploader | null = null
  let cancelled = false
  let batchId: string | null = null
  const emit = (phase: ImportPhase, upload: UploadProgress | null, copy: PathImportStatus | null) => onProgress({ phase, upload, copy })

  const promise = (async (): Promise<ImportJob> => {
    emit('preparing', null, null)
    const total = picked.reduce((n, p) => n + p.file.size, 0)
    let cfg = null as Awaited<ReturnType<Api['getImportConfig']>> | null
    try { cfg = await api.getImportConfig() } catch { cfg = null }
    const split = cfg?.local_path_import ? splitDesktopPaths(picked, d) : { paths: [], direct: [], rest: picked }
    if (!split.paths.length && picked.length <= SMALL_BATCH_FILES && total <= SMALL_BATCH_BYTES) {
      emit('uploading', null, null)
      const job = await api.createImport(picked, opts)
      emit('done', null, null)
      return job
    }
    const resume = deps.resume === undefined ? localResumeStore() : deps.resume
    uploader = new ImportUploader({
      create: (i) => api.createUpload(i), query: (id) => api.getUpload(id), complete: (id) => api.completeUpload(id), abort: (id) => api.abortUpload(id),
      put: (id, o, c, s) => api.putUploadChunk(id, o, c, s),
    }, { resume, ...deps.uploader, onProgress: (u) => emit('uploading', u, null) })
    uploader.add(split.rest)
    batchId = null
    for (const p of split.rest) { const r = resume?.get(fingerprint(p)); if (r) { batchId = r.batchId; break } } // 续传：沿用上次中断的批次
    if (!batchId) batchId = uuid() || null
    // 1) 本机路径：后端直接复制，进度来自批次状态
    if (split.paths.length) {
      const st = await api.importFromPaths({ batch_id: batchId ?? undefined, paths: split.paths, commit: false })
      batchId = st.batch_id
      for (;;) {
        const b = await api.getImportBatch(batchId)
        const cp = b.path_import ?? st
        emit('copying', null, cp)
        if (cp.state === 'failed') throw new Error(cp.error || '本机文件复制失败')
        if (cp.state === 'cancelled') throw new Error('已取消')
        if (cp.state === 'done' || cp.state === 'committing') break
        if (cancelled) throw new Error('已取消')
        await sleep(pollMs)
      }
    }
    // 2) 浏览器上传
    if (split.rest.length) {
      const res = await uploader.start(batchId ?? undefined)
      batchId = res.batchId
      if (cancelled) throw new Error('已取消')
      if (!res.allComplete) {
        const first = res.failed[0]
        throw new Error(`${res.failed.length} 个文件上传失败（${first?.name}：${first?.error ?? '未知错误'}）。再次点击提交会从中断处继续。`)
      }
    }
    emit('committing', uploader.snapshot(), null)
    const key = opts.idempotencyKey ?? (batchId ? `commit-${batchId}` : undefined)
    const job = await api.commitImportBatch(batchId!, { templateId: opts.templateId, options: opts.options, idempotencyKey: key })
    emit('done', null, null)
    return job
  })()

  return {
    promise,
    async cancel() {
      cancelled = true
      await uploader?.abort()
      if (batchId) { try { await api.discardImportBatch(batchId) } catch (e) { if (!(e instanceof ApiError)) throw e } }
    },
  }
}

/** 给组件用的包装：响应式进度 + 提交/取消。 */
export function useImportUploader(deps: ImportDeps = {}) {
  const progress = ref<ImportProgress | null>(null)
  const running = ref(false)
  let current: ImportRun | null = null
  async function start(picked: PickedFile[], opts: ImportOpts): Promise<ImportJob> {
    running.value = true
    current = runImport(picked, opts, (p) => { progress.value = p }, deps)
    try { return await current.promise } finally { running.value = false; current = null }
  }
  async function cancel() { await current?.cancel() }
  function reset() { progress.value = null }
  return { progress, running, start, cancel, reset }
}

/** 桌面版“选择文件/文件夹”：把绝对路径交给后端复制并自动提交，轮询到任务创建。返回任务 id。 */
export async function importLocalPaths(paths: string[], opts: ImportOpts, onProgress: (p: PathImportStatus) => void, deps: ImportDeps = {}): Promise<string> {
  const api = deps.api ?? defaultApi
  const sleep = deps.sleep ?? ((ms: number) => new Promise<void>((r) => setTimeout(r, ms)))
  const key = opts.idempotencyKey ?? (uuid() ? `paths-${uuid()}` : undefined)
  let st = await api.importFromPaths({ paths, commit: true, template_id: opts.templateId || undefined, options: opts.options && Object.keys(opts.options).length ? opts.options : undefined, idempotencyKey: key })
  const batch = st.batch_id
  for (;;) {
    onProgress(st)
    if (st.state === 'failed') throw new Error(st.error || '本机文件复制失败')
    if (st.state === 'cancelled') throw new Error('已取消')
    if (st.state === 'done') {
      if (st.task_id) return st.task_id
      throw new Error(st.error || '没有可导入的文件')
    }
    await sleep(deps.pollMs ?? 500)
    st = (await api.getImportBatch(batch)).path_import ?? st
  }
}
