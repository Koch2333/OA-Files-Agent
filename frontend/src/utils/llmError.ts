import { ApiError, type FileRecord, type LlmTestResult } from '../api/types'
import { errMsg } from './errorInfo'

/**
 * 调用模型服务商失败（测试连接、获取模型列表、登录后自动测试……）时的统一分类与文案。
 * 目标：用大白话说清“发生了什么”并带上 HTTP 状态/原因，同时告诉界面能不能“重试”以及要等多久。
 * 纯函数、无副作用；组件只负责显示（LlmFailureBanner.vue）。
 */
export type LlmFailureKind = 'auth' | 'rate_limit' | 'server' | 'timeout' | 'unreachable' | 'not_found' | 'rejected' | 'policy' | 'unknown'

export interface LlmFailure {
  kind: LlmFailureKind
  /** 上游或本站 HTTP 状态（能识别时） */
  status?: number
  /** 大白话文案，已含状态/原因 */
  message: string
  /** 是否显示“重试”：配置/权限类（需要先改设置）为 false，其余都可以原样再来一次 */
  retryable: boolean
  /** 建议等待秒数（来自 Retry-After，仅 429） */
  retryAfter?: number
}

/** 从后端文案里取 HTTP 状态：“（HTTP 401）”。 */
export function statusFromText(text: string | undefined): number | undefined {
  const m = /HTTP\s*(\d{3})/i.exec(text ?? '')
  return m ? Number(m[1]) : undefined
}

function byStatus(status: number, retryAfter?: number): LlmFailure {
  if (status === 401 || status === 403) return { kind: 'auth', status, retryable: true, message: `Key 无效，请重新复制（HTTP ${status}）：请确认复制完整、没有多余空格；也可能已过期，可到服务商页面重新创建一把。` }
  if (status === 404) return { kind: 'not_found', status, retryable: true, message: '服务商找不到这个接口或模型（HTTP 404）：请检查模型名称，或在“高级设置”里确认服务地址。' }
  if (status === 400 || status === 422) return { kind: 'rejected', status, retryable: true, message: `服务商拒绝了这次请求（HTTP ${status}）：所选模型可能不可用，请换一个模型。` }
  if (status === 429) return { kind: 'rate_limit', status, retryable: true, retryAfter, message: `请求太频繁或额度用完了（HTTP 429）：${retryAfter ? `请等待 ${retryAfter} 秒后重试` : '请稍等片刻再试'}，如反复出现请到服务商页面检查余额。` }
  if (status === 408 || status === 504) return { kind: 'timeout', status, retryable: true, message: `服务商响应超时（HTTP ${status}），请稍后重试。` }
  if (status >= 500) return { kind: 'server', status, retryable: true, message: `服务商那边暂时出问题了（HTTP ${status}），通常过一会儿就好，请稍后重试。` }
  return { kind: 'unknown', status, retryable: true, message: `请求没有成功（HTTP ${status}），请重试。` }
}

function byText(text: string): LlmFailure {
  const status = statusFromText(text)
  if (status) return byStatus(status)
  if (/超时|timeout|timed out/i.test(text)) return { kind: 'timeout', retryable: true, message: '连接超时：服务商没有及时回应，请检查网络后重试。' }
  if (/无法连接|unreachable|refused|dns|网络/i.test(text)) return { kind: 'unreachable', retryable: true, message: '连不上服务商：请检查电脑的网络（或代理）后重试。' }
  return { kind: 'unknown', retryable: true, message: text ? `没有成功：${text}` : '没有成功，请重试。' }
}

/** 测试连接返回 ok=false 时（HTTP 200 但服务商那边失败）。ok=true 返回 null。 */
export function classifyTestResult(r: Pick<LlmTestResult, 'ok' | 'message'>): LlmFailure | null {
  return r.ok ? null : byText(r.message ?? '')
}

/** 接口本身抛出的错误（获取模型列表的 502、本站限流 429、网络中断等）。 */
export function classifyError(e: unknown): LlmFailure {
  if (e instanceof ApiError) {
    if (e.status === 429) return { ...byStatus(429, e.retryAfter), message: byStatus(429, e.retryAfter).message }
    if (e.status === 502 || e.status === 504) {
      const f = byText(e.message)
      return f.status ? f : { ...f, status: e.status, message: f.kind === 'unknown' ? `服务商没有正常回应（HTTP ${e.status}）：${e.message}` : f.message }
    }
    if (e.status === 403 || e.status === 422 || e.status === 400) return { kind: 'policy', status: e.status, retryable: false, message: errMsg(e) }
    if (e.status >= 500) return { kind: 'server', status: e.status, retryable: true, message: `本应用的服务出错了（HTTP ${e.status}），请重试；如持续出现请联系管理员。` }
    return { kind: 'unknown', status: e.status, retryable: true, message: errMsg(e) }
  }
  return { kind: 'unreachable', retryable: true, message: '连不上本应用的服务，请检查网络后重试。' }
}

/** 重试按钮上的倒计时文案（秒数 <=0 即可点击）。 */
export function retryLabel(waitSeconds: number, busy: boolean): string {
  if (busy) return '重试中…'
  return waitSeconds > 0 ? `重试（${waitSeconds} 秒）` : '重试'
}

/* ---------------------------------------------------------------------------------------------
 * 任务/计划/Agent 轨迹里的“大模型调用失败”（后端 FileRecord.llm_error，已含状态码与自动重试次数）。
 * 一键重试 = POST /v1/tasks/{id}/retry：后端只重新识别失败的文件，已成功的结果保留。
 * ------------------------------------------------------------------------------------------- */
export type FileLlmError = NonNullable<FileRecord['llm_error']>
/** 主按钮：retry = 重试；check_key = 去设置检查 API Key（401/403/404/400 这类重试没有意义的原因）。 */
export type LlmPrimary = 'retry' | 'check_key'

export interface LlmErrorView {
  code: FileLlmError['code']
  status: number
  /** 面向用户的原因，例如“服务商返回 429：请求太频繁或额度不足，已自动重试 3 次” */
  message: string
  retryable: boolean
  attempts: number
  /** 服务商给出的 Retry-After 秒数；未知为 0 */
  retryAfter: number
  primary: LlmPrimary
}

export const CHECK_KEY_LABEL = '检查 API Key'
export const CHECK_KEY_TO = '/settings#llm'

/** 后端结构 → 视图。主按钮由“能不能靠重试解决”决定：不可重试（Key/模型问题）时主按钮是“检查 API Key”，重试降为次要。 */
export function viewOfLlmError(e: FileLlmError): LlmErrorView {
  const primary: LlmPrimary = e.retryable ? 'retry' : 'check_key'
  return { code: e.code, status: e.status, message: e.message, retryable: e.retryable, attempts: e.attempts, retryAfter: Math.max(0, e.retry_after_seconds || 0), primary }
}

/** Agent 事件（provider_error）的 details → 视图；没有 code 时视为不可识别（返回 null）。 */
export function viewOfProviderEvent(summary: string, d: Record<string, unknown> | null | undefined): LlmErrorView | null {
  if (!d || typeof d.code !== 'string' || !d.code.startsWith('llm_')) return null
  const n = (k: string) => (typeof d[k] === 'number' ? (d[k] as number) : 0)
  return viewOfLlmError({ code: d.code as FileLlmError['code'], status: n('status'), retryable: d.retryable === true, retry_after_seconds: n('retry_after_seconds'), attempts: n('attempts'), message: summary })
}

/** 一个任务里所有因大模型失败的文件。 */
export function llmFailedFiles(files: readonly FileRecord[] | null | undefined): FileRecord[] {
  return (files ?? []).filter((f) => !!f.error && !!f.llm_error)
}

export interface LlmTaskFailure { view: LlmErrorView; count: number; files: string[] }

/** 汇总：以第一个“不可重试”的原因为准（先解决 Key 问题），否则取等待最长的 Retry-After。没有失败返回 null。 */
export function summarizeLlmFailures(files: readonly FileRecord[] | null | undefined): LlmTaskFailure | null {
  const bad = llmFailedFiles(files)
  if (!bad.length) return null
  const views = bad.map((f) => viewOfLlmError(f.llm_error!))
  const first = views.find((v) => !v.retryable) ?? views[0]
  const retryAfter = Math.max(...views.map((v) => v.retryAfter))
  const view = { ...first, retryAfter: first.retryable ? retryAfter : 0 }
  if (bad.length > 1) view.message = `${bad.length} 个文件识别失败：${view.message}`
  return { view, count: bad.length, files: bad.map((f) => f.source_name) }
}

export interface RetryUi { primary: LlmPrimary; retryDisabled: boolean; retryLabel: string; showCountdown: boolean; showCheckKey: boolean }

/** RetryNotice 的状态机（纯函数）：remaining = 还需等待的 Retry-After 秒数。 */
export function retryUi(view: Pick<LlmErrorView, 'primary'>, s: { busy: boolean; remaining: number }): RetryUi {
  const primaryRetry = view.primary === 'retry'
  const waiting = primaryRetry && s.remaining > 0
  return {
    primary: view.primary,
    // 次要“重试”（401/403/404）不受倒计时限制：用户改完设置后仍可立即再试
    retryDisabled: s.busy || waiting,
    retryLabel: retryLabel(primaryRetry ? s.remaining : 0, s.busy),
    showCountdown: waiting,
    showCheckKey: !primaryRetry,
  }
}
