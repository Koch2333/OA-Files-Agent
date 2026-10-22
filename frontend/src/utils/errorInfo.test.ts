import { describe, expect, it } from 'vitest'
import { ApiError } from '../api/types'
import { copyableError, describeError, errMsg, extractRequestId, NETWORK_HELP, SERVER_ERROR_TEXT, splitRequestId } from './errorInfo'
import { llmErrorText } from './llm'

describe('error mapping', () => {
  it.each([
    ['AUTO_ARCHIVE_DISABLED', 403, '关闭'],
    ['AUTO_ACK_REQUIRED', 422, '风险'],
    ['LLM_REQUIRED', 422, '大模型'],
    ['PERSONAL_LLM_DISABLED', 403, '个人大模型'],
  ])('%s gets a clear Chinese message', (code, status, word) => {
    const m = errMsg(new ApiError(status, code, 'raw backend text', 'rid1'))
    expect(m).toContain(word)
    expect(m).not.toContain('raw backend text')
    expect(m).toContain('（请求编号 rid1）')
  })
  it('other client errors keep the backend message and add the request id', () => {
    expect(errMsg(new ApiError(409, 'CONFLICT', '冲突了', 'abc'))).toBe('冲突了（请求编号 abc）')
    expect(errMsg(new ApiError(409, 'CONFLICT', '冲突了'))).toBe('冲突了')
  })
  it('5xx → internal server error text with the request id', () => {
    const m = errMsg(new ApiError(500, 'INTERNAL', 'boom: secret stack', 'r500'))
    expect(m).toBe(`${SERVER_ERROR_TEXT}（请求编号 r500）`)
    expect(describeError(new ApiError(502, 'X', 'bad gateway')).kind).toBe('server')
  })
  it('network failures get the distinct help text (ApiError NETWORK and bare fetch TypeError)', () => {
    expect(describeError(new ApiError(0, 'NETWORK', 'x')).kind).toBe('network')
    expect(errMsg(new TypeError('Failed to fetch'))).toBe(NETWORK_HELP)
    expect(NETWORK_HELP).toContain('OA_API_TARGET')
    expect(NETWORK_HELP).toContain('oa-agent')
  })
})

describe('request id helpers', () => {
  it('extract and split', () => {
    expect(extractRequestId('出错了（请求编号 req-12_ab）')).toBe('req-12_ab')
    expect(splitRequestId('出错了（请求编号 req-1）')).toEqual({ text: '出错了', id: 'req-1' })
    expect(splitRequestId('出错了')).toEqual({ text: '出错了' })
  })
  it('copyable text has message, id, status and time but no bodies', () => {
    const t = copyableError(new ApiError(500, 'INTERNAL', 'x', 'r9'), new Date('2026-10-22T00:00:00Z'), '/ai')
    expect(t).toContain('请求编号：r9')
    expect(t).toContain('HTTP 500 INTERNAL')
    expect(t).toContain('页面：/ai')
    expect(t).toContain('2026-10-22T00:00:00.000Z')
  })
})

describe('LLM settings error text', () => {
  it('personal 422 adds the stricter-address explanation; global does not', () => {
    const e = new ApiError(422, 'VALIDATION_FAILED', '地址不合法', 'r1')
    expect(llmErrorText(e, 'personal')).toContain('内网/本机地址')
    expect(llmErrorText(e, 'personal')).toContain('（请求编号 r1）')
    expect(llmErrorText(e, 'global')).toBe('地址不合法（请求编号 r1）')
  })
  it('personal 403 PERSONAL_LLM_DISABLED and 429 keep their messages', () => {
    expect(llmErrorText(new ApiError(403, 'PERSONAL_LLM_DISABLED', 'x'), 'personal')).toContain('未开放')
    expect(llmErrorText(new ApiError(429, 'RATE_LIMITED', '操作过于频繁，请约 20 秒后再试。'), 'personal')).toContain('过于频繁')
  })
})
