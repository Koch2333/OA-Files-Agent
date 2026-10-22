import type { ClientErrorInput } from '../api/types'
import { ApiError } from '../api/types'

export const DEDUPE_MS = 60_000
export const RATE_LIMIT = 5
export const RATE_WINDOW_MS = 60_000
const MAX = { message: 500, stack: 4000, route: 200, component: 120 }

const IGNORED = [/ResizeObserver loop/i, /^Script error\.?$/i, /Non-Error promise rejection captured/i]

/** 脱敏：去掉 URL 查询串/片段、令牌与口令样式的片段、过长的随机串（可能是令牌/哈希）。 */
export function scrub(text: string): string {
  return text
    .replace(/((?:https?:\/\/[^\s?#)"']+)|(?:\/[\w.%~-]+(?:\/[\w.%~-]+)*))[?#][^\s)"']*/g, '$1')
    .replace(/Bearer\s+[A-Za-z0-9._~+/=-]+/gi, 'Bearer [redacted]')
    .replace(/\b(token|password|passwd|secret|api[_-]?key|authorization|csrf|session|cookie)(["']?\s*[=:]\s*)(["']?)[^\s,;&"']+/gi, '$1$2$3[redacted]')
    .replace(/[A-Za-z0-9_-]{32,}/g, '[redacted]')
}
export function truncate(s: string, n: number): string { return s.length > n ? s.slice(0, n - 1) + '…' : s }
/** 路由只保留路径模式，不含查询串；邀请链接令牌替换为占位。 */
export function safeRoute(path: string): string {
  return truncate(path.split(/[?#]/)[0].replace(/\/invite\/[^/]+/, '/invite/:token'), MAX.route)
}

export interface ErrCtx { component?: string }
export interface ReporterDeps {
  send: (input: ClientErrorInput) => Promise<{ report_id: string }>
  now?: () => number
  enabled: () => boolean
  route: () => string
  appVersion: string
  /** 非阻塞提示（toast）。reportId 缺失表示上报失败/被限流之外的情况 */
  notify: (info: { reportId?: string; message: string; copy: string }) => void
}

export function shouldIgnore(err: unknown, message: string): boolean {
  if (err instanceof ApiError) return true // 服务端已有对应记录（带请求编号），不重复上报
  if (err instanceof TypeError && /Failed to fetch|NetworkError|Load failed/i.test(err.message)) return true
  return IGNORED.some((r) => r.test(message))
}

/** 仅包含 message/stack/route/component/app_version（绝不含表单值、令牌或响应正文）。 */
export function buildPayload(err: unknown, route: string, appVersion: string, ctx: ErrCtx = {}): ClientErrorInput {
  const e = err instanceof Error ? err : null
  const message = scrub(e ? e.message || e.name : typeof err === 'string' ? err : 'Unknown error')
  const p: ClientErrorInput = { message: truncate(message || 'Unknown error', MAX.message), route: safeRoute(route), app_version: truncate(appVersion, 40) }
  if (e?.stack) p.stack = truncate(scrub(e.stack), MAX.stack)
  if (ctx.component) p.component = truncate(scrub(ctx.component), MAX.component)
  return p
}

export function createReporter(d: ReporterDeps) {
  const now = d.now ?? Date.now
  const seen = new Map<string, { at: number; id?: string }>()
  let sent: number[] = []

  function copyText(p: ClientErrorInput, id?: string): string {
    return [`页面错误：${p.message}`, id ? `报告编号：${id}` : '', `页面：${p.route ?? ''}`, p.component ? `组件：${p.component}` : '', `版本：${p.app_version ?? ''}`, `时间：${new Date(now()).toISOString()}`].filter(Boolean).join('\n')
  }

  /** 上报并提示；返回报告编号（去重命中时返回之前的编号；被忽略/限流/失败为 undefined）。 */
  async function report(err: unknown, ctx: ErrCtx = {}, opts: { silent?: boolean } = {}): Promise<string | undefined> {
    if (!d.enabled()) return undefined
    const payload = buildPayload(err, d.route(), d.appVersion, ctx)
    if (shouldIgnore(err, payload.message)) return undefined
    const t = now()
    for (const [k, v] of seen) if (t - v.at > DEDUPE_MS) seen.delete(k)
    const key = payload.message + '|' + (payload.stack?.split('\n')[1] ?? '')
    const hit = seen.get(key)
    if (hit) return hit.id
    sent = sent.filter((x) => t - x < RATE_WINDOW_MS)
    if (sent.length >= RATE_LIMIT) return undefined
    sent.push(t)
    const entry: { at: number; id?: string } = { at: t }
    seen.set(key, entry)
    try {
      const r = await d.send(payload)
      entry.id = r.report_id
      if (!opts.silent) d.notify({ reportId: r.report_id, message: `页面出现了问题，已记录（编号 ${r.report_id}）`, copy: copyText(payload, r.report_id) })
      return r.report_id
    } catch {
      if (!opts.silent) d.notify({ message: '页面出现了问题，但未能自动上报；请复制详情反馈给管理员', copy: copyText(payload) })
      return undefined
    }
  }
  return { report, buildPayload: (e: unknown, c?: ErrCtx) => buildPayload(e, d.route(), d.appVersion, c) }
}
export type Reporter = ReturnType<typeof createReporter>
