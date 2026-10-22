import type { ErrorReportQuery, ErrorSeverity, ErrorSource, SystemInfo } from '../api/types'

export const SOURCE_LABEL: Record<ErrorSource, string> = { api: '接口', pipeline: '归档流水线', worker: '文档 Worker', storage: '存储', client: '浏览器', supervisor: '进程守护', startup: '启动', llm: '大模型' }
export const SEVERITY_LABEL: Record<ErrorSeverity, string> = { error: '错误', panic: 'Panic', fatal: '致命' }
export const SEVERITY_TONE: Record<ErrorSeverity, 'attention' | 'error'> = { error: 'attention', panic: 'error', fatal: 'error' }
export const SOURCES = Object.keys(SOURCE_LABEL) as ErrorSource[]
export const SEVERITIES = Object.keys(SEVERITY_LABEL) as ErrorSeverity[]

export type ResolvedFilter = 'open' | 'resolved' | 'all'
export interface DiagFilters { source: ErrorSource | ''; severity: ErrorSeverity | ''; state: ResolvedFilter; q: string }
export const DEFAULT_FILTERS: DiagFilters = { source: '', severity: '', state: 'open', q: '' }

/** 筛选 → 查询参数：空项不发送；“全部”不带 resolved。 */
export function reportQuery(f: DiagFilters, page: number, pageSize: number): ErrorReportQuery {
  const q: ErrorReportQuery = { page, page_size: pageSize }
  if (f.source) q.source = f.source
  if (f.severity) q.severity = f.severity
  if (f.state !== 'all') q.resolved = f.state === 'resolved'
  if (f.q.trim()) q.q = f.q.trim()
  return q
}

/** 从路由 query 还原筛选（非法值回落默认）。 */
export function filtersFromQuery(query: Record<string, unknown>): DiagFilters {
  const one = (v: unknown) => (Array.isArray(v) ? String(v[0] ?? '') : v == null ? '' : String(v))
  const source = one(query.source), severity = one(query.severity), state = one(query.state)
  return {
    source: (SOURCES as string[]).includes(source) ? (source as ErrorSource) : '',
    severity: (SEVERITIES as string[]).includes(severity) ? (severity as ErrorSeverity) : '',
    state: state === 'resolved' || state === 'all' || state === 'open' ? state : 'open',
    q: one(query.q),
  }
}

/** 导航角标：0 不显示；超过 99 显示 99+。 */
export function badgeText(n: number | null | undefined): string {
  if (!n || n <= 0) return ''
  return n > 99 ? '99+' : String(n)
}

const GIB = 1024 ** 3
export interface DiskView { freePct: number; usedPct: number; warn: boolean; text: string }
/** 磁盘余量：低于 10% 或低于 10 GiB 告警。 */
export function diskView(d: SystemInfo['disk']): DiskView {
  const total = Math.max(0, d.total_bytes), free = Math.max(0, Math.min(d.free_bytes, total || d.free_bytes))
  const freePct = total ? (free / total) * 100 : 0
  const fmt = (b: number) => (b >= GIB ? `${(b / GIB).toFixed(1)} GiB` : `${(b / 1048576).toFixed(0)} MiB`)
  return { freePct, usedPct: total ? 100 - freePct : 0, warn: total === 0 || freePct < 10 || free < 10 * GIB, text: `可用 ${fmt(free)} / 共 ${fmt(total)}（${freePct.toFixed(1)}%）` }
}

export function fmtUptime(sec: number): string {
  const s = Math.max(0, Math.floor(sec))
  const d = Math.floor(s / 86400), h = Math.floor((s % 86400) / 3600), m = Math.floor((s % 3600) / 60)
  if (d) return `${d} 天 ${h} 小时`
  if (h) return `${h} 小时 ${m} 分`
  if (m) return `${m} 分钟`
  return `${s} 秒`
}

/** 上次异常结束横幅：存在则返回标题与跳转到相关报告的路由（契约没有报告 ID，按 supervisor 来源过滤）。 */
export function uncleanBanner(s: Pick<SystemInfo, 'last_exit_unclean'> | null | undefined): { text: string; to: { path: string; query: Record<string, string> } } | null {
  if (!s?.last_exit_unclean) return null
  return { text: '上次运行异常结束（可能是崩溃或被强制结束），已记录一条进程守护错误报告。', to: { path: '/admin/diagnostics', query: { source: 'supervisor', state: 'all' } } }
}

/** 报告是否应在复制详情里带上栈：只拼已脱敏字段。 */
export function reportClipboard(r: { id: string; source: string; severity: string; code: string; message: string; count: number; first_seen: string; last_seen: string; request_id: string; stack?: string; details?: Record<string, unknown> }): string {
  const lines = [`报告 ${r.id}`, `来源：${r.source}  级别：${r.severity}  错误码：${r.code}`, `消息：${r.message}`, `次数：${r.count}  首次：${r.first_seen}  最近：${r.last_seen}`, `最近请求编号：${r.request_id}`]
  if (r.details && Object.keys(r.details).length) lines.push('详情：' + JSON.stringify(r.details))
  if (r.stack) lines.push('调用栈（已脱敏）：', r.stack)
  return lines.join('\n')
}
