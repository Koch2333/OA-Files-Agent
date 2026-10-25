import type { FileLlmError } from '../utils/llmError'

/**
 * 演示模式下模拟“大模型 HTTP 错误”：
 *  - 文件名含 llm429 / llm500 / llm401 / llm404 / llmtimeout（不区分大小写，如“llm429-合同.pdf”）的文件；
 *  - 或地址带 ?mock_llm_error=429|500|401|404|timeout：每个任务的第一个文件。
 * 429/500/超时（可重试）只在第一次识别时失败，点“重试”后成功；401/404 每次都失败（演示“检查 API Key”）。
 */
export type FaultKind = '429' | '500' | '401' | '404' | 'timeout'
const KINDS: FaultKind[] = ['429', '500', '401', '404', 'timeout']

export function faultKindFor(sourceName: string, isFirstFile: boolean, query = typeof location === 'undefined' ? '' : location.search): FaultKind | null {
  const m = /llm[-_]?(429|500|401|404|timeout)/i.exec(sourceName)
  if (m) return m[1].toLowerCase() as FaultKind
  const q = new URLSearchParams(query).get('mock_llm_error') as FaultKind | null
  return isFirstFile && q && KINDS.includes(q) ? q : null
}

/** 重试后是否仍失败：只有不可重试的 Key/模型问题会一直失败。 */
export function faultPersists(kind: FaultKind): boolean { return kind === '401' || kind === '404' }

/** 与后端 ProviderError.UserMessage 一致的文案与字段。 */
export function llmErrorFor(kind: FaultKind): FileLlmError {
  switch (kind) {
    case '429': return { code: 'llm_rate_limited', status: 429, retryable: true, retry_after_seconds: 20, attempts: 4, message: '服务商返回 429：请求太频繁或额度不足，已自动重试 3 次' }
    case '500': return { code: 'llm_server', status: 500, retryable: true, retry_after_seconds: 0, attempts: 4, message: '服务商返回 500：服务商暂时不可用，已自动重试 3 次' }
    case 'timeout': return { code: 'llm_timeout', status: 0, retryable: true, retry_after_seconds: 0, attempts: 4, message: '连接服务商超时，已自动重试 3 次' }
    case '401': return { code: 'llm_auth', status: 401, retryable: false, retry_after_seconds: 0, attempts: 1, message: '服务商返回 401：API Key 无效或没有权限' }
    case '404': return { code: 'llm_not_found', status: 404, retryable: false, retry_after_seconds: 0, attempts: 1, message: '服务商返回 404：接口或模型不存在，请检查 Base URL 与模型名称' }
  }
}
