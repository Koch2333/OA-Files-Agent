import type { ImportJob } from '../api/types'
import { shortId } from './format'

/** 工作台“待你处理”：由任务列表与待确认文件数推导（不新增接口）。 */
export type AttentionKind = 'approve' | 'auto_left' | 'execute' | 'review' | 'failed'
export interface AttentionItem {
  key: string
  kind: AttentionKind
  title: string
  meta: string
  taskId?: string
  /** 主操作的目标路由 */
  to: string
  actionLabel: string
  /** 失败类条目：可点“重试” */
  retryable?: boolean
  icon: string
  tone: 'attention' | 'error'
}

const RECENT_FAIL_MS = 14 * 86400_000
const ORDER: Record<AttentionKind, number> = { approve: 0, auto_left: 1, execute: 2, review: 3, failed: 4 }

export function deriveAttention(tasks: ImportJob[], reviewFiles: number, now: number = Date.now(), canWrite = true): AttentionItem[] {
  const out: AttentionItem[] = []
  for (const t of tasks) {
    const id = shortId(t.id)
    const link = t.plan_id ? `/plans/${t.plan_id}` : `/tasks/${t.id}`
    if (t.status === 'awaiting_review') {
      const auto = t.automation === 'auto'
      out.push({
        key: 'a-' + t.id, kind: auto ? 'auto_left' : 'approve', taskId: t.id, to: link, icon: auto ? 'bolt' : 'pending_actions', tone: 'attention',
        title: auto ? `全自动归档留下了需要你确认的文件（任务 ${id}）` : `归档计划待审批（任务 ${id}）`,
        meta: auto ? '可确定的文件已自动归档，其余需要你逐项确认' : `${t.total_files} 个文件已识别，核对后批准即可归档`,
        actionLabel: canWrite ? (auto ? '去确认' : '去审批') : '查看',
      })
    } else if (t.status === 'approved') {
      out.push({ key: 'e-' + t.id, kind: 'execute', taskId: t.id, to: link, icon: 'verified', tone: 'attention', title: `计划已批准，等待执行（任务 ${id}）`, meta: `${t.total_files} 个文件，点击执行后才会真正归档`, actionLabel: canWrite ? '去执行' : '查看' })
    } else if ((t.status === 'failed' || t.status === 'partial_failure') && now - Date.parse(t.updated_at) < RECENT_FAIL_MS) {
      const partial = t.status === 'partial_failure'
      out.push({
        key: 'f-' + t.id, kind: 'failed', taskId: t.id, to: `/tasks/${t.id}`, icon: 'error', tone: 'error', retryable: canWrite,
        title: partial ? `${t.failure_count} 个文件处理失败（任务 ${id}）` : `任务处理失败（任务 ${id}）`,
        meta: partial ? `其余 ${t.success_count} 个文件正常，可查看失败原因或重试` : '查看失败原因，或重新排队重试',
        actionLabel: '查看',
      })
    }
  }
  if (reviewFiles > 0) out.push({ key: 'review', kind: 'review', to: '/files?review=1', icon: 'rule', tone: 'attention', title: `${reviewFiles} 个文件需要人工确认`, meta: '部门、类型或日期无法确定，系统不会替你猜测', actionLabel: canWrite ? '去确认' : '查看' })
  return out.sort((a, b) => ORDER[a.kind] - ORDER[b.kind])
}
