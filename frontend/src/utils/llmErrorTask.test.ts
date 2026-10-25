import { describe, expect, it } from 'vitest'
import type { FileRecord } from '../api/types'
import { llmErrorFor } from '../mocks/mockLlmFault'
import { llmFailedFiles, retryUi, summarizeLlmFailures, viewOfLlmError, viewOfProviderEvent } from './llmError'

const file = (name: string, e?: ReturnType<typeof llmErrorFor>): FileRecord => ({ id: name, task_id: 't', source_name: name, error: e ? e.message : null, llm_error: e ?? null } as unknown as FileRecord)

describe('viewOfLlmError', () => {
  it('429 is retryable and carries Retry-After and the Chinese reason', () => {
    const v = viewOfLlmError(llmErrorFor('429'))
    expect(v).toMatchObject({ status: 429, retryable: true, primary: 'retry', retryAfter: 20, attempts: 4 })
    expect(v.message).toContain('429')
    expect(v.message).toContain('已自动重试 3 次')
  })
  it('401/404 switch the primary action to checking the key', () => {
    expect(viewOfLlmError(llmErrorFor('401')).primary).toBe('check_key')
    expect(viewOfLlmError(llmErrorFor('404')).primary).toBe('check_key')
    expect(viewOfLlmError(llmErrorFor('500')).primary).toBe('retry')
    expect(viewOfLlmError(llmErrorFor('timeout')).primary).toBe('retry')
  })
})

describe('summarizeLlmFailures', () => {
  it('returns null without LLM failures (other file errors do not count)', () => {
    expect(summarizeLlmFailures([file('a')])).toBeNull()
    expect(summarizeLlmFailures([{ ...file('b'), error: '不支持的文件格式' }])).toBeNull()
    expect(summarizeLlmFailures(null)).toBeNull()
  })
  it('prefers a non-retryable cause and counts files', () => {
    const s = summarizeLlmFailures([file('a', llmErrorFor('429')), file('b', llmErrorFor('401')), file('c')])!
    expect(s.count).toBe(2)
    expect(s.view.primary).toBe('check_key')
    expect(s.view.message.startsWith('2 个文件识别失败：')).toBe(true)
    expect(llmFailedFiles([file('a', llmErrorFor('429'))])).toHaveLength(1)
  })
  it('keeps the longest Retry-After among retryable failures', () => {
    const slow = { ...llmErrorFor('429'), retry_after_seconds: 60 }
    expect(summarizeLlmFailures([file('a', llmErrorFor('429')), file('b', slow)])!.view.retryAfter).toBe(60)
  })
})

describe('viewOfProviderEvent', () => {
  it('reads the trace event details and ignores unrelated events', () => {
    const v = viewOfProviderEvent('服务商返回 500', { status: 500, code: 'llm_server', retryable: true, attempts: 4, retry_after_seconds: 0 })!
    expect(v.status).toBe(500)
    expect(v.primary).toBe('retry')
    expect(viewOfProviderEvent('x', { tool: 'a' })).toBeNull()
    expect(viewOfProviderEvent('x', null)).toBeNull()
  })
})

describe('retryUi state machine', () => {
  const retry = viewOfLlmError(llmErrorFor('429'))
  const key = viewOfLlmError(llmErrorFor('401'))
  it('counts down, then enables the retry button', () => {
    expect(retryUi(retry, { busy: false, remaining: 12 })).toMatchObject({ retryDisabled: true, showCountdown: true, retryLabel: '重试（12 秒）' })
    expect(retryUi(retry, { busy: false, remaining: 0 })).toMatchObject({ retryDisabled: false, showCountdown: false, retryLabel: '重试' })
  })
  it('is disabled and relabelled while busy (double-click safe)', () => {
    expect(retryUi(retry, { busy: true, remaining: 0 })).toMatchObject({ retryDisabled: true, retryLabel: '重试中…' })
  })
  it('non-retryable: check-key is primary, retry stays available as secondary', () => {
    expect(retryUi(key, { busy: false, remaining: 5 })).toMatchObject({ primary: 'check_key', showCheckKey: true, retryDisabled: false, showCountdown: false })
  })
})
