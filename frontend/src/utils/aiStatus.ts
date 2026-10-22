import type { LlmStatus } from '../api/types'
import { roleAtLeast, type Role } from './authz'

/** AI 工作台顶部状态横幅的展示决策（纯函数，便于测试）。 */
export interface StatusCta { label: string; to: string; primary: boolean }
export interface StatusView {
  /** true = 可用（显示“将使用：…”小标签）；false = 整个编辑区禁用并显示醒目提示卡 */
  ok: boolean
  /** 可用时的小标签文案 */
  chip: string
  /** 不可用时的标题与说明 */
  title: string
  detail: string
  cta: StatusCta | null
  /** 额外提示（如“也可联系管理员”） */
  extra: string
}

export const SOURCE_LABEL: Record<LlmStatus['source'], string> = { none: '未配置', global: '全局', personal: '个人' }

export function chipText(s: Pick<LlmStatus, 'source' | 'model' | 'provider_label'>): string {
  return `将使用：${s.source === 'personal' ? '个人' : '全局'} · ${s.model || s.provider_label || '未命名模型'}`
}

export function statusView(s: LlmStatus | null | undefined): StatusView {
  if (!s) return { ok: false, chip: '', title: '正在读取大模型状态…', detail: '', cta: null, extra: '' }
  if (s.configured) return { ok: true, chip: chipText(s), title: '', detail: '', cta: null, extra: '' }
  const title = 'AI 工作台需要先配置大模型'
  const detail = 'AI 工作台按你的文字说明让大模型决定归档方式，没有大模型时无法使用（离线规则不理解任务说明）。你仍然可以在“智能归档”页使用离线规则处理文件。'
  switch (s.setup_hint) {
    case 'admin_global': return { ok: false, chip: '', title, detail, cta: { label: '去配置全局大模型', to: '/settings#llm', primary: true }, extra: '配置后全体员工都可以使用；员工也可以配置自己的个人大模型（可在设置中开放）。' }
    case 'personal_or_admin': return { ok: false, chip: '', title, detail, cta: { label: '配置我的个人大模型', to: '/settings#llm', primary: true }, extra: '也可以联系管理员配置全局大模型，全员即可使用。' }
    default: return { ok: false, chip: '', title, detail, cta: null, extra: '请联系管理员配置大模型。' }
  }
}

export type AutoBlockReason = 'role' | 'no_llm' | 'policy' | 'loading'
export interface AutoGate { allowed: boolean; reason?: AutoBlockReason; text: string }

/** “全自动”是否可选：服务端的 auto_archive_allowed 为准；不可用时给出原因（角色 / 无大模型 / 策略关闭）。 */
export function automationGate(s: LlmStatus | null | undefined, role: Role | null | undefined): AutoGate {
  if (!s) return { allowed: false, reason: 'loading', text: '正在读取状态…' }
  if (!roleAtLeast(role, 'operator')) return { allowed: false, reason: 'role', text: '只读账号不能使用全自动归档。' }
  if (!s.configured) return { allowed: false, reason: 'no_llm', text: '全自动归档由大模型决策，需要先配置大模型。' }
  if (!s.auto_archive_allowed) return { allowed: false, reason: 'policy', text: '管理员已关闭“全自动归档”（设置 → AI 使用策略）。' }
  return { allowed: true, text: '' }
}

/** 设置页状态行：当前用户的任务会使用哪个配置。 */
export function effectiveSourceLine(s: LlmStatus | null | undefined): string {
  if (!s) return '正在读取…'
  if (!s.configured) return '当前账号尚无可用的大模型：AI 工作台不可用。'
  const name = s.model || s.provider_label || ''
  return s.source === 'personal' ? `你的任务将使用你的个人大模型（${name}）。` : `你的任务将使用全局大模型（${name}）。`
}
