import type { ImportJob, TaskStatus } from '../api/types'

/** Agent 执行进度：把任务状态映射为 5 个步骤的状态（纯函数，组件只负责渲染）。 */
export type StepKey = 'parse' | 'recognize' | 'plan' | 'approve' | 'execute'
/** done 完成 · active 进行中（转圈）· waiting 等你操作 · pending 未开始 · error 出错 · cancelled 已取消 · skipped 跳过（全自动不需要审批） */
export type StepState = 'done' | 'active' | 'waiting' | 'pending' | 'error' | 'cancelled' | 'skipped'

export interface StepView {
  key: StepKey
  label: string
  state: StepState
  /** 步骤下方的短说明，例如“12 / 30”“等待你审批”“全自动” */
  note: string
}
export interface ProgressView {
  steps: StepView[]
  /** 当前焦点步骤（进行中 / 等你 / 出错 / 已取消）的下标，全部完成或未开始为 -1 */
  focus: number
  /** 需要用户操作（待审批、待执行、全自动余项） */
  needsYou: boolean
  /** 纯文本状态，用于 aria-label 与提示 */
  summary: string
}

export const STEP_LABELS: Record<StepKey, string> = { parse: '解析文件', recognize: 'AI 识别', plan: '生成归档计划', approve: '人工审批', execute: '执行归档' }
const KEYS: StepKey[] = ['parse', 'recognize', 'plan', 'approve', 'execute']

type Input = Pick<ImportJob, 'status' | 'total_files' | 'processed_files' | 'failure_count'> & Partial<Pick<ImportJob, 'automation' | 'plan_id'>>

const count = (t: Input) => (t.total_files > 0 ? `${Math.min(t.processed_files, t.total_files)} / ${t.total_files}` : '')

/** 终止状态（失败/取消）发生在哪一步：已有计划 → 审批/执行阶段，否则按处理进度判断。 */
function stopIndex(t: Input): number {
  if (t.plan_id) return t.status === 'cancelled' ? 3 : 4
  if (t.processed_files < t.total_files) return 0
  return 2
}

export function progressFor(t: Input): ProgressView {
  const auto = t.automation === 'auto'
  const st: StepState[] = ['pending', 'pending', 'pending', 'pending', 'pending']
  const notes: string[] = ['', '', '', '', '']
  const doneUpTo = (n: number) => { for (let i = 0; i < n; i++) st[i] = 'done' }
  let needsYou = false
  let summary = ''
  const s: TaskStatus = t.status
  const c = count(t)

  switch (s) {
    case 'queued': notes[0] = '排队中'; summary = '排队中，等待开始'; break
    case 'scanning': st[0] = 'active'; notes[0] = '扫描文件'; summary = '正在扫描文件'; break
    case 'extracting': st[0] = 'active'; notes[0] = c; summary = `正在解析文件${c ? '，已处理 ' + c : ''}`; break
    case 'analyzing': doneUpTo(1); st[1] = 'active'; notes[1] = c; summary = `AI 正在识别${c ? '，已处理 ' + c : ''}`; break
    case 'planning': doneUpTo(2); st[2] = 'active'; notes[2] = '生成中'; summary = '正在生成归档计划'; break
    case 'awaiting_review':
      doneUpTo(3); st[3] = 'waiting'; needsYou = true
      if (auto) { notes[3] = '余项待你确认'; summary = '全自动已处理可确定的文件，其余待你确认'; st[4] = 'pending' }
      else { notes[3] = '等待你审批'; summary = '等待你审批归档计划' }
      break
    case 'approved': doneUpTo(4); st[4] = 'waiting'; needsYou = true; notes[4] = '待执行'; summary = '计划已批准，等待你执行归档'; break
    case 'archiving': doneUpTo(4); st[4] = 'active'; notes[4] = c; summary = `正在执行归档${c ? '，已处理 ' + c : ''}`; break
    case 'completed': doneUpTo(5); summary = '已完成'; break
    case 'partial_failure': doneUpTo(4); st[4] = 'error'; notes[4] = `${t.failure_count} 个失败`; summary = `部分文件失败（${t.failure_count} 个）`; break
    case 'failed': { const i = stopIndex(t); doneUpTo(i); st[i] = 'error'; notes[i] = '失败'; summary = `处理失败（${STEP_LABELS[KEYS[i]]}）`; break }
    case 'cancelled': { const i = stopIndex(t); doneUpTo(i); st[i] = 'cancelled'; notes[i] = '已取消'; summary = '已取消'; break }
  }

  if (auto && st[3] !== 'waiting') { st[3] = 'skipped'; notes[3] = '全自动' }
  const steps = KEYS.map((key, i) => ({ key, label: STEP_LABELS[key], state: st[i], note: notes[i] || DEFAULT_NOTE[st[i]] }))
  const focus = steps.findIndex((x) => x.state === 'active' || x.state === 'waiting' || x.state === 'error' || x.state === 'cancelled')
  return { steps, focus, needsYou, summary: `Agent 执行进度：${summary || '未开始'}` }
}
const DEFAULT_NOTE: Record<StepState, string> = { done: '完成', active: '进行中', waiting: '等你操作', pending: '', error: '失败', cancelled: '已取消', skipped: '' }
