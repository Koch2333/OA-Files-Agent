import type { TaskStatus } from '../api/types'

export function fmtBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1048576) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / 1048576).toFixed(1)} MB`
}
export function fmtTime(s: string | null | undefined): string {
  if (!s) return '—'
  const d = new Date(s)
  return isNaN(+d) ? s : d.toLocaleString('zh-CN', { hour12: false, month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })
}
export function shortId(id: string) { return id.slice(0, 8) }

export const STATUS_LABEL: Record<TaskStatus, string> = {
  queued: '排队中', scanning: '扫描中', extracting: '解析/OCR', analyzing: 'Agent 识别', planning: '生成计划',
  awaiting_review: '待审批', approved: '已批准', archiving: '归档中', completed: '已完成',
  partial_failure: '部分失败', failed: '失败', cancelled: '已取消',
}
export type Tone = 'neutral' | 'progress' | 'attention' | 'success' | 'error'
export const STATUS_TONE: Record<TaskStatus, Tone> = {
  queued: 'neutral', scanning: 'progress', extracting: 'progress', analyzing: 'progress', planning: 'progress',
  awaiting_review: 'attention', approved: 'attention', archiving: 'progress', completed: 'success',
  partial_failure: 'error', failed: 'error', cancelled: 'neutral',
}
export const ACTIVE: TaskStatus[] = ['queued', 'scanning', 'extracting', 'analyzing', 'planning', 'archiving']
/** 主流程阶段（异常状态不在其中） */
export const FLOW: { key: TaskStatus; label: string }[] = [
  { key: 'scanning', label: '扫描' }, { key: 'extracting', label: '解析/OCR' }, { key: 'analyzing', label: '识别' },
  { key: 'planning', label: '计划' }, { key: 'awaiting_review', label: '审批' }, { key: 'archiving', label: '归档' }, { key: 'completed', label: '完成' },
]
export function flowIndex(s: TaskStatus): number {
  if (s === 'queued') return -1
  if (s === 'approved') return 4.5
  const i = FLOW.findIndex((f) => f.key === s)
  return i < 0 ? FLOW.length : i
}
