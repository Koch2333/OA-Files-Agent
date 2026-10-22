import { ApiError } from '../api/types'

/** 后端不可达时的说明：同时给出独立版与开发环境的排查提示。 */
export const NETWORK_HELP = '无法连接到后端服务。独立版请确认 oa-agent 进程仍在运行（可重新启动）；开发环境请确认后端已监听在 OA_API_TARGET（默认 http://127.0.0.1:8080），或在 设置 中切换为演示数据。页面会每 5 秒自动重试，恢复后提示自动消失。'
export const SERVER_ERROR_TEXT = '服务器内部错误，已记录，请联系管理员并提供请求编号'

export type ErrorKind = 'network' | 'server' | 'client' | 'unknown'

/** “（请求编号 xxx）”后缀；没有编号返回空串。 */
export function requestIdSuffix(id?: string | null): string { return id ? `（请求编号 ${id}）` : '' }

/** 新增业务错误码的中文说明（AI 工作台 / 全自动 / 个人大模型）。 */
export function knownErrorMessage(e: { status: number; code: string }): string | null {
  switch (e.code) {
    case 'AUTO_ARCHIVE_DISABLED': return '管理员已关闭“全自动归档”，请改用人工审核模式。'
    case 'AUTO_ACK_REQUIRED': return '全自动模式需要先阅读并确认风险提示：请重新选择“全自动”并勾选确认。'
    case 'LLM_REQUIRED': return '此操作需要已配置的大模型。请先在 设置 → 大模型 中配置（或联系管理员）。'
    case 'PERSONAL_LLM_DISABLED': return '管理员未开放个人大模型 API，请使用全局大模型，或联系管理员开放。'
  }
  return null
}

export function isNetworkError(e: unknown): boolean {
  if (e instanceof ApiError) return e.status === 0 || e.code === 'NETWORK'
  return e instanceof TypeError && /fetch|network|load failed/i.test(e.message)
}

export interface ErrorInfo { message: string; requestId?: string; kind: ErrorKind; status?: number; code?: string }

/** 把任意异常整理成可展示的信息（不含任何请求/响应正文）。 */
export function describeError(e: unknown): ErrorInfo {
  if (isNetworkError(e)) return { message: NETWORK_HELP, kind: 'network', status: 0, code: 'NETWORK' }
  if (e instanceof ApiError) {
    const known = knownErrorMessage(e)
    if (known) return { message: known, requestId: e.requestId, kind: 'client', status: e.status, code: e.code }
    if (e.status >= 500) return { message: SERVER_ERROR_TEXT, requestId: e.requestId, kind: 'server', status: e.status, code: e.code }
    return { message: e.message, requestId: e.requestId, kind: 'client', status: e.status, code: e.code }
  }
  return { message: e instanceof Error ? e.message : String(e), kind: 'unknown' }
}

/** 面向用户的单行错误文案：消息 + 请求编号。 */
export function errMsg(e: unknown): string {
  const i = describeError(e)
  return i.message + requestIdSuffix(i.requestId)
}

/** “复制错误信息”的内容：只含消息、编号、状态码、时间、页面路径，便于向管理员反馈。 */
export function copyableError(e: unknown, now: Date = new Date(), path = ''): string {
  const i = describeError(e)
  const lines = [`错误：${i.message}`]
  if (i.requestId) lines.push(`请求编号：${i.requestId}`)
  if (i.status) lines.push(`状态：HTTP ${i.status}${i.code ? ' ' + i.code : ''}`)
  lines.push(`时间：${now.toISOString()}`)
  if (path) lines.push(`页面：${path}`)
  return lines.join('\n')
}

/** 从已格式化的文案里取出请求编号（用于只拿到字符串的错误横幅）。 */
export function extractRequestId(text: string | null | undefined): string | undefined {
  const m = /请求编号\s*([A-Za-z0-9._-]+)/.exec(text ?? '')
  return m?.[1]
}

/** 把“…（请求编号 xxx）”拆成正文与编号。 */
export function splitRequestId(text: string): { text: string; id?: string } {
  const id = extractRequestId(text)
  return id ? { text: text.replace(/\s*[（(]请求编号\s*[A-Za-z0-9._-]+[）)]/, '').trim(), id } : { text }
}
