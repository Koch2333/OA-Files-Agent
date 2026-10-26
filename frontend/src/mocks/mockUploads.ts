import { uuid } from '../utils/uuid'
import { ApiError, type CreateUploadInput, type FromPathsInput, type ImportBatchStatus, type ImportConfig, type ImportJob, type ImportOptions, type PathImportStatus, type PickedFile, type UploadSession } from '../api/types'

/** 演示用的分片上传/暂存批次：语义与后端一致（offset 校验、重复分片幂等、complete 校验大小），没有任何大小/数量限制。 */
interface Staged { s: UploadSession; received: number }

export function createMockUploads(commit: (picked: PickedFile[], opts: { templateId?: string | null; idempotencyKey?: string; options?: ImportOptions }) => Promise<ImportJob>) {
  const sessions = new Map<string, Staged>()
  const batches = new Map<string, { ids: string[]; committed?: ImportJob; paths?: PathImportStatus }>()
  const jobsByKey = new Map<string, ImportJob>()
  const nowIso = () => new Date().toISOString()
  const view = (st: Staged): UploadSession => ({ ...st.s, offset: st.s.complete ? st.s.size : st.received })
  const get = (id: string) => { const s = sessions.get(id); if (!s) throw new ApiError(404, 'NOT_FOUND', '上传会话不存在或已过期'); return s }
  const fakeFile = (name: string, size: number) => ({ name, size, type: '', lastModified: 0 }) as unknown as File

  const config: ImportConfig = {
    chunk_size: 8 * 1024 * 1024, max_upload_bytes: null, max_batch_files: null, session_ttl_seconds: 86400, disk_reserve_bytes: 512 * 1024 * 1024,
    disk_free_bytes: 200 * 1024 ** 3, local_path_import: false, supported_extensions: ['.docx', '.xlsx', '.pdf', '.png', '.jpg', '.jpeg'],
  }

  return {
    async getImportConfig() { return { ...config } },
    async createUpload(input: CreateUploadInput): Promise<UploadSession> {
      if (!input.name || input.size < 0) throw new ApiError(400, 'INVALID_ARGUMENT', '参数无效')
      const batch_id = input.batch_id ?? uuid()
      const id = `${batch_id}_${uuid()}`
      const t = nowIso()
      const s: UploadSession = { id, batch_id, name: input.name, path: input.path || input.name, size: input.size, offset: 0, complete: false, source: 'upload', created_at: t, updated_at: t, expires_at: t, chunk_size: config.chunk_size }
      sessions.set(id, { s, received: 0 })
      const b = batches.get(batch_id) ?? { ids: [] }
      b.ids.push(id); batches.set(batch_id, b)
      return view(sessions.get(id)!)
    },
    async getUpload(id: string) { return view(get(id)) },
    async putUploadChunk(id: string, offset: number, chunk: Blob): Promise<UploadSession> {
      const st = get(id)
      if (offset > st.received) throw new ApiError(409, 'OFFSET_MISMATCH', 'offset 与服务器已接收的位置不一致', undefined, { offset: st.received })
      const end = offset + chunk.size
      if (end > st.s.size) throw new ApiError(422, 'CHUNK_EXCEEDS_SIZE', '上传内容超过声明的文件大小')
      st.received = Math.max(st.received, end) // 重复/重叠分片幂等
      return view(st)
    },
    async completeUpload(id: string) {
      const st = get(id)
      if (st.received !== st.s.size) throw new ApiError(409, 'OFFSET_MISMATCH', '上传尚未完成', undefined, { offset: st.received })
      st.s.complete = true
      st.s.sha256 = st.s.sha256 || Array.from({ length: 64 }, () => '0123456789abcdef'[Math.floor(Math.random() * 16)]).join('')
      return view(st)
    },
    async abortUpload(id: string) { get(id); sessions.delete(id) },
    async getImportBatch(batchId: string): Promise<ImportBatchStatus> {
      const b = batches.get(batchId)
      if (!b) throw new ApiError(404, 'NOT_FOUND', '导入批次不存在或已过期')
      return { batch_id: batchId, files: b.ids.filter((i) => sessions.has(i)).map((i) => view(sessions.get(i)!)), committed: !!b.committed, ...(b.paths ? { path_import: b.paths } : {}) }
    },
    async discardImportBatch(batchId: string) {
      const b = batches.get(batchId)
      if (!b) throw new ApiError(404, 'NOT_FOUND', '导入批次不存在或已过期')
      b.ids.forEach((i) => sessions.delete(i)); batches.delete(batchId)
    },
    async commitImportBatch(batchId: string, opts: { templateId?: string | null; idempotencyKey?: string; options?: ImportOptions }): Promise<ImportJob> {
      if (opts.idempotencyKey && jobsByKey.has(opts.idempotencyKey)) return jobsByKey.get(opts.idempotencyKey)!
      const b = batches.get(batchId)
      if (!b) throw new ApiError(404, 'NOT_FOUND', '导入批次不存在或已过期')
      const list = b.ids.map((i) => sessions.get(i)).filter((x): x is Staged => !!x)
      if (!list.length) throw new ApiError(400, 'INVALID_PATHS', '批次中没有文件')
      if (list.some((x) => !x.s.complete)) throw new ApiError(409, 'UPLOADS_INCOMPLETE', '仍有文件未上传完成，请先完成或取消')
      const job = await commit(list.map((x) => ({ file: fakeFile(x.s.name, x.s.size), relativePath: x.s.path })), opts)
      b.committed = job
      list.forEach((x) => sessions.delete(x.s.id))
      if (opts.idempotencyKey) jobsByKey.set(opts.idempotencyKey, job)
      return job
    },
    async importFromPaths(_input: FromPathsInput): Promise<PathImportStatus> {
      throw new ApiError(403, 'LOCAL_ONLY', '演示模式不支持本机路径导入')
    },
  }
}
