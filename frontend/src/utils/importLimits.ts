import type { PickedFile } from '../api/types'

/**
 * 选择/校验规则。导入没有单文件、整批或数量上限：大文件走可续传分片上传（utils/uploader.ts），
 * 桌面版还可按本机路径直接复制。只有格式（docx/xlsx/pdf/png/jpg）与空文件会在接收时作为失败项标出。
 */
export const SUPPORTED_RE = /\.(docx|xlsx|pdf|png|jpe?g)$/i
export const LEGACY_RE = /\.(doc|xls)$/i

export type FileVerdict = 'ok' | 'legacy' | 'unsupported' | 'empty'
export function verdict(f: { name: string; size: number }): FileVerdict {
  if (!SUPPORTED_RE.test(f.name)) return LEGACY_RE.test(f.name) ? 'legacy' : 'unsupported'
  if (f.size === 0) return 'empty'
  return 'ok'
}
export const VERDICT_LABEL: Record<FileVerdict, string> = { ok: '可处理', legacy: '旧版 Office，需转换', unsupported: '不支持', empty: '空文件' }

export interface PickSummary {
  count: number
  totalBytes: number
  ok: number
  /** 会作为失败项计入任务（不让整批失败）的文件数：不支持 / 旧版 / 空文件 */
  rejected: number
  rejectedList: { path: string; verdict: FileVerdict }[]
  /** 保留字段：已没有数量/大小上限，恒为 false */
  overCount: false
  overBatch: false
  /** 恒为 false：不再有“必须先移除文件才能提交”的情形 */
  blocking: false
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
  return { count: picked.length, totalBytes, ok, rejected: rejectedList.length, rejectedList, overCount: false, overBatch: false, blocking: false }
}

/** 合并新选择的文件：按相对路径去重。 */
export function mergePicked(cur: PickedFile[], add: PickedFile[]): PickedFile[] {
  const seen = new Set(cur.map((p) => p.relativePath))
  return [...cur, ...add.filter((p) => !seen.has(p.relativePath) && seen.add(p.relativePath))]
}
