import type { PickedFile } from '../api/types'

/** 与契约一致的上传限制：每批 ≤100 个文件、单文件 ≤20 MiB、整批 ≤128 MiB。 */
export const MAX_FILES = 100
export const MAX_FILE_BYTES = 20 * 1024 * 1024
export const MAX_BATCH_BYTES = 128 * 1024 * 1024

export const SUPPORTED_RE = /\.(docx|xlsx|pdf|png|jpe?g)$/i
export const LEGACY_RE = /\.(doc|xls)$/i

export type FileVerdict = 'ok' | 'legacy' | 'unsupported' | 'too_large' | 'empty'
export function verdict(f: { name: string; size: number }): FileVerdict {
  if (!SUPPORTED_RE.test(f.name)) return LEGACY_RE.test(f.name) ? 'legacy' : 'unsupported'
  if (f.size === 0) return 'empty'
  if (f.size > MAX_FILE_BYTES) return 'too_large'
  return 'ok'
}
export const VERDICT_LABEL: Record<FileVerdict, string> = { ok: '可处理', legacy: '旧版 Office，需转换', unsupported: '不支持', too_large: '超过 20 MiB', empty: '空文件' }

export interface PickSummary {
  count: number
  totalBytes: number
  ok: number
  /** 会作为失败项计入任务（不让整批失败）的文件数：不支持 / 旧版 / 过大 / 空文件 */
  rejected: number
  rejectedList: { path: string; verdict: FileVerdict }[]
  overCount: boolean
  overBatch: boolean
  /** 批量超限：必须先移除一些文件才能提交 */
  blocking: boolean
}

export function summarize(picked: PickedFile[]): PickSummary {
  const rejectedList: PickSummary['rejectedList'] = []
  let ok = 0, totalBytes = 0
  for (const p of picked) {
    totalBytes += p.file.size
    const v = verdict(p.file)
    if (v === 'ok') ok++
    else rejectedList.push({ path: p.relativePath, verdict: v })
  }
  const overCount = picked.length > MAX_FILES
  const overBatch = totalBytes > MAX_BATCH_BYTES
  return { count: picked.length, totalBytes, ok, rejected: rejectedList.length, rejectedList, overCount, overBatch, blocking: overCount || overBatch }
}

/** 合并新选择的文件：按相对路径去重。 */
export function mergePicked(cur: PickedFile[], add: PickedFile[]): PickedFile[] {
  const seen = new Set(cur.map((p) => p.relativePath))
  return [...cur, ...add.filter((p) => !seen.has(p.relativePath) && seen.add(p.relativePath))]
}
