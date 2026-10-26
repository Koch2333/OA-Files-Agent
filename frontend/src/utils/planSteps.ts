import type { ArchivePlan } from '../api/types'
import { fallbackItems } from './classification'

/** 计划页的两步流程：① 批准计划 → ② 执行归档。纯函数，状态由计划本身推出。 */
export type StepPhase = 'unapproved' | 'blocked' | 'approved' | 'executing' | 'done' | 'failed'
export type ApproveState = 'active' | 'blocked' | 'done' | 'busy'
export type ExecuteState = 'locked' | 'ready' | 'running' | 'done'

export interface PlanStepView {
  phase: StepPhase
  approve: ApproveState
  execute: ExecuteState
  /** 待人工确认的条目数（review_required） */
  blocking: number
  /** 其中仍是系统暂填「其他」的条目数 */
  fallback: number
  /** 批准按钮被挡住时的一句话；否则 null */
  approveHint: string | null
  /** 执行按钮被锁住时的一句话；否则 null */
  executeHint: string | null
}

export function planStepView(plan: Pick<ArchivePlan, 'status' | 'items'> | undefined, busy: '' | 'approve' | 'execute' | 'save' = ''): PlanStepView {
  const items = plan?.items ?? []
  const blocking = items.filter((i) => i.review_required).length
  const fallback = fallbackItems(items).length
  const status = plan?.status
  let phase: StepPhase
  if (!plan) phase = 'unapproved'
  else if (status === 'executed') phase = 'done'
  else if (status === 'failed') phase = 'failed'
  else if (status === 'executing') phase = 'executing'
  else if (status === 'approved') phase = 'approved'
  else phase = blocking ? 'blocked' : 'unapproved'
  const approve: ApproveState = busy === 'approve' ? 'busy' : phase === 'blocked' ? 'blocked' : phase === 'unapproved' ? 'active' : 'done'
  const execute: ExecuteState = phase === 'executing' || busy === 'execute' ? 'running' : phase === 'done' ? 'done' : phase === 'approved' ? 'ready' : 'locked'
  return {
    phase, approve, execute, blocking, fallback,
    approveHint: phase === 'blocked' ? `还有 ${blocking} 项待确认，确认后才能批准` : null,
    executeHint: execute !== 'locked' ? null : phase === 'failed' ? '上次执行失败，请在任务详情查看原因' : phase === 'blocked' ? `还有 ${blocking} 项待确认；确认并批准后才能执行归档` : '请先批准计划，再执行归档',
  }
}

const COACH_KEY = 'oa.plan.coach'
/** 首次提示是否已看过（本浏览器）。存储不可用时视为已看过，避免每次都弹。 */
export function coachSeen(): boolean { try { return localStorage.getItem(COACH_KEY) === '1' } catch { return true } }
export function markCoachSeen(): void { try { localStorage.setItem(COACH_KEY, '1') } catch { /* 存储不可用：页面照常工作 */ } }
