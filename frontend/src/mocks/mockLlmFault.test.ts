import { describe, expect, it } from 'vitest'
import { faultKindFor, faultPersists, llmErrorFor } from './mockLlmFault'

describe('mockLlmFault', () => {
  it('matches by file name', () => {
    expect(faultKindFor('LLM429-合同.pdf', false, '')).toBe('429')
    expect(faultKindFor('a_llmtimeout.pdf', false, '')).toBe('timeout')
    expect(faultKindFor('正常.pdf', true, '')).toBeNull()
  })
  it('query switch only hits the first file', () => {
    expect(faultKindFor('a.pdf', true, '?mock_llm_error=500')).toBe('500')
    expect(faultKindFor('b.pdf', false, '?mock_llm_error=500')).toBeNull()
    expect(faultKindFor('a.pdf', true, '?mock_llm_error=418')).toBeNull()
  })
  it('only key/model problems persist', () => {
    expect(faultPersists('401') && faultPersists('404')).toBe(true)
    expect(faultPersists('429') || faultPersists('500') || faultPersists('timeout')).toBe(false)
    expect(llmErrorFor('401').retryable).toBe(false)
    expect(llmErrorFor('429').retry_after_seconds).toBe(20)
  })
})
