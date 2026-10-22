import type { ArchivePlan, FileRecord, ImportJob } from '../api/types'

export interface AutoSummary { archived: number; review: number; failed: number; operationId: string | null }

/**
 * 全自动任务的结果摘要（契约没有专门字段，由文件与计划推导）：
 * 已归档 = archive_status 为 archived 的文件；留待人工 = 仍待归档且需确认/在计划中的文件；失败 = 解析失败文件。
 */
export function autoSummary(files: FileRecord[], plan: Pick<ArchivePlan, 'items' | 'operation_id'> | null | undefined): AutoSummary {
  const archived = files.filter((f) => f.archive_status === 'archived').length
  const failed = files.filter((f) => !!f.error || f.archive_status === 'failed').length
  const inPlan = new Set(plan?.items.map((i) => i.file_id) ?? [])
  const review = files.filter((f) => f.archive_status === 'pending' && !f.error && (f.review_required || inPlan.has(f.id))).length
  return { archived, review, failed, operationId: plan?.operation_id ?? null }
}

export type TaskModeBadge = { label: string; tone: 'neutral' | 'attention' }
export function modeBadge(t: Pick<ImportJob, 'automation'>): TaskModeBadge {
  return t.automation === 'auto' ? { label: '全自动', tone: 'attention' } : { label: '人工审核', tone: 'neutral' }
}
export function llmLabel(t: Pick<ImportJob, 'llm_source' | 'llm_model'>): string {
  if (!t.llm_source || t.llm_source === 'none') return '离线规则'
  return `${t.llm_source === 'personal' ? '个人' : '全局'} · ${t.llm_model || '未命名模型'}`
}
export function autoBannerText(t: Pick<ImportJob, 'automation' | 'llm_model'>): string | null {
  return t.automation === 'auto' ? `本任务为全自动模式：由模型 ${t.llm_model || '（未记录）'} 决定，无人工审批` : null
}
export function decisionLabel(decision: 'manual' | 'ai_auto' | undefined, model?: string): { label: string; tone: 'neutral' | 'attention' } {
  return decision === 'ai_auto' ? { label: model ? `全自动 · ${model}` : '全自动', tone: 'attention' } : { label: '人工审核', tone: 'neutral' }
}
