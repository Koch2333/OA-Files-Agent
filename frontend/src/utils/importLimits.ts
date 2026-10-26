import type { PickedFile } from '../api/types'
import { READABLE_RE, isReadable } from './formats'

/**
 * 选择/校验规则。导入没有单文件、整批或数量上限：大文件走可续传分片上传（utils/uploader.ts），
 * 桌面版还可按本机路径直接复制。任何格式、任何大小（含空文件）都会被接收：
 * 能读取内容的格式（见 formats.ts / contracts/formats.json）按内容识别；其余文件“内容无法读取”，
 * 只依据文件名判断，必定进入人工审核，原件不会被修改。
 */
export const SUPPORTED_RE = READABLE_RE

export type FileVerdict = 'ok' | 'name_only' | 'empty'
export function verdict(f: { name: string; size: number }): FileVerdict {
  if (f.size === 0) return 'empty'
  return isReadable(f.name) ? 'ok' : 'name_only'
}
export const VERDICT_LABEL: Record<FileVerdict, string> = { ok: '可识别内容', name_only: '仅按文件名处理', empty: '空文件，仅按文件名处理' }

export interface PickSummary {
  count: number
  totalBytes: number
  /** 可识别内容的文件数 */
  ok: number
  /** 内容无法读取、只能按文件名处理的文件数（格式不识别 / 空文件）。它们仍会提交并进入人工审核。 */
  nameOnly: number
  nameOnlyList: { path: string; verdict: FileVerdict }[]
  /** 保留字段：已没有数量/大小上限，恒为 false */
  overCount: false
  overBatch: false
  /** 恒为 false：不再有“必须先移除文件才能提交”的情形 */
  blocking: false
}

export function summarize(picked: PickedFile[]): PickSummary {
  const nameOnlyList: PickSummary['nameOnlyList'] = []
  let ok = 0, totalBytes = 0
  for (const p of picked) {
    totalBytes += p.file.size
    const v = verdict(p.file)
    if (v === 'ok') ok++
    else nameOnlyList.push({ path: p.relativePath, verdict: v })
  }
  return { count: picked.length, totalBytes, ok, nameOnly: nameOnlyList.length, nameOnlyList, overCount: false, overBatch: false, blocking: false }
}

/** 合并新选择的文件：按相对路径去重。 */
export function mergePicked(cur: PickedFile[], add: PickedFile[]): PickedFile[] {
  const seen = new Set(cur.map((p) => p.relativePath))
  return [...cur, ...add.filter((p) => !seen.has(p.relativePath) && seen.add(p.relativePath))]
}
