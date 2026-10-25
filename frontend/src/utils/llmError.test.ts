import { describe, expect, it } from 'vitest'
import { ApiError } from '../api/types'
import { classifyError, classifyTestResult, retryLabel, statusFromText } from './llmError'

const api = (status: number, code: string, msg: string, retryAfter?: number) => { const e = new ApiError(status, code, msg, 'rid'); if (retryAfter) e.retryAfter = retryAfter; return e }

describe('provider failures in plain Chinese with status and retry', () => {
  it('maps backend test messages by HTTP status', () => {
    expect(classifyTestResult({ ok: true, message: '连接成功' })).toBeNull()
    expect(classifyTestResult({ ok: false, message: '密钥无效或没有权限（HTTP 401）' })).toMatchObject({ kind: 'auth', status: 401, retryable: true, message: expect.stringContaining('Key 无效，请重新复制') })
    expect(classifyTestResult({ ok: false, message: '请求过于频繁或额度不足（HTTP 429）' })).toMatchObject({ kind: 'rate_limit', status: 429, message: expect.stringContaining('HTTP 429') })
    expect(classifyTestResult({ ok: false, message: '服务商暂时不可用（HTTP 503）' })).toMatchObject({ kind: 'server', status: 503, message: expect.stringContaining('服务商那边暂时出问题了') })
    expect(classifyTestResult({ ok: false, message: '接口或模型不存在（HTTP 404）：请检查' })).toMatchObject({ kind: 'not_found', status: 404 })
    expect(classifyTestResult({ ok: false, message: '服务商拒绝了请求（HTTP 400）：模型名称可能不正确' })).toMatchObject({ kind: 'rejected', status: 400 })
    expect(classifyTestResult({ ok: false, message: '连接超时' })).toMatchObject({ kind: 'timeout', retryable: true })
    expect(classifyTestResult({ ok: false, message: '无法连接服务商：请检查 Base URL 与网络' })).toMatchObject({ kind: 'unreachable', retryable: true })
    expect(classifyTestResult({ ok: false, message: '' })).toMatchObject({ kind: 'unknown', retryable: true })
  })
  it('maps API errors from the model list and honours Retry-After', () => {
    expect(classifyError(api(502, 'UPSTREAM_ERROR', '服务商暂时不可用（HTTP 503）'))).toMatchObject({ kind: 'server', status: 503, retryable: true })
    expect(classifyError(api(502, 'UPSTREAM_ERROR', '无法连接服务商：请检查 Base URL 与网络'))).toMatchObject({ kind: 'unreachable', retryable: true })
    const rl = classifyError(api(429, 'RATE_LIMITED', 'x', 12))
    expect(rl).toMatchObject({ kind: 'rate_limit', retryAfter: 12, retryable: true, message: expect.stringContaining('12 秒') })
    expect(classifyError(api(429, 'RATE_LIMITED', 'x')).retryAfter).toBeUndefined()
    expect(classifyError(api(500, 'INTERNAL', 'boom'))).toMatchObject({ kind: 'server', status: 500, retryable: true })
    expect(classifyError(new TypeError('Failed to fetch'))).toMatchObject({ kind: 'unreachable', retryable: true })
  })
  it('configuration/permission errors are not retryable', () => {
    expect(classifyError(api(422, 'VALIDATION_FAILED', '地址不合法'))).toMatchObject({ kind: 'policy', retryable: false })
    expect(classifyError(api(403, 'PERSONAL_LLM_DISABLED', '管理员未开放'))).toMatchObject({ retryable: false })
  })
  it('extracts status codes and labels the button', () => {
    expect(statusFromText('foo（HTTP 502）')).toBe(502)
    expect(statusFromText('nothing')).toBeUndefined()
    expect(retryLabel(0, false)).toBe('重试')
    expect(retryLabel(5, false)).toBe('重试（5 秒）')
    expect(retryLabel(0, true)).toBe('重试中…')
    expect(retryLabel(5, true)).toBe('重试中…')
  })
})
