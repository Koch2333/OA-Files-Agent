import type { ContentInfo } from '../api/types'

/**
 * 内容可读性（后端 content / basis）。内容无法读取的文件不是失败项：只依据文件名判断，恒需人工审核。
 * 原因代码见 contracts/openapi.yaml 的 ContentInfo。
 */
const UNREADABLE: Record<string, string> = {
  encrypted: '已加密', damaged: '文件损坏或无法解析', unsupported: '格式不支持读取内容', too_large: '文件超过解析大小上限',
  timeout: '解析超时', ocr_empty: '未能识别出文字', ocr_unavailable: 'OCR 组件不可用', empty: '文件为空',
}
const PARTIAL: Record<string, string> = { capped: '内容过长，只读取了前面部分', damaged: '文件部分损坏，已尽力恢复文字', encoding: '文本编码为推测结果，可能有乱码' }

export function reasonLabel(reason: string | null | undefined): string {
  return (reason && (UNREADABLE[reason] ?? PARTIAL[reason])) || '原因未知'
}
export const isNameOnly = (c: Pick<ContentInfo, 'basis'> | null | undefined) => c?.basis === 'filename_only'

export interface ContentChip { text: string; tone: 'attention' | 'info'; title: string }
/** 列表/详情里的标记：仅依据文件名、部分内容、扩展名与内容不符。 */
export function contentChips(c: ContentInfo | null | undefined): ContentChip[] {
  if (!c) return []
  const out: ContentChip[] = []
  if (c.basis === 'filename_only') out.push({ text: '仅依据文件名', tone: 'attention', title: `内容无法读取（${reasonLabel(c.reason)}），仅依据文件名判断` })
  else if (c.status === 'partial') out.push({ text: '部分内容', tone: 'info', title: reasonLabel(c.reason) })
  if (c.format_mismatch) out.push({ text: '扩展名与内容不符', tone: 'info', title: `实际内容是 ${c.format_mismatch}，已按实际格式读取` })
  return out
}

/** 计划条目：basis + 说明（后端已给出完整句子）。 */
export function planChip(basis: string | undefined | null, note: string | null | undefined): ContentChip | null {
  return basis === 'filename_only' ? { text: '仅依据文件名', tone: 'attention', title: note || '内容无法读取，仅依据文件名判断' } : null
}

export const nameOnlyCount = (files: readonly { content?: ContentInfo | null }[] | null | undefined) => (files ?? []).filter((f) => isNameOnly(f.content)).length
export const nameOnlySummary = (n: number) => `${n} 个文件无法读取内容，已按文件名处理`
