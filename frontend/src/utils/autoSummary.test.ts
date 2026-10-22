import { describe, expect, it } from 'vitest'
import type { FileRecord } from '../api/types'
import { autoBannerText, autoSummary, decisionLabel, llmLabel, modeBadge } from './autoSummary'

const file = (id: string, over: Partial<FileRecord> = {}) => ({ id, archive_status: 'pending', review_required: false, error: null, ...over }) as FileRecord

describe('automatic run summary', () => {
  it('counts archived / left for review / failed and exposes the operation id', () => {
    const files = [file('1', { archive_status: 'archived' }), file('2', { archive_status: 'archived' }), file('3', { review_required: true }), file('4', { archive_status: 'failed', error: 'x' })]
    expect(autoSummary(files, { items: [{ file_id: '3' } as never], operation_id: 'op1' })).toEqual({ archived: 2, review: 1, failed: 1, operationId: 'op1' })
    expect(autoSummary([], null)).toEqual({ archived: 0, review: 0, failed: 0, operationId: null })
  })
  it('labels', () => {
    expect(modeBadge({ automation: 'auto' })).toEqual({ label: '全自动', tone: 'attention' })
    expect(modeBadge({ automation: 'review' }).label).toBe('人工审核')
    expect(modeBadge({}).label).toBe('人工审核')
    expect(llmLabel({ llm_source: 'personal', llm_model: 'm' })).toBe('个人 · m')
    expect(llmLabel({ llm_source: 'none' })).toBe('离线规则')
    expect(autoBannerText({ automation: 'auto', llm_model: 'deepseek-flash' })).toBe('本任务为全自动模式：由模型 deepseek-flash 决定，无人工审批')
    expect(autoBannerText({ automation: 'review' })).toBeNull()
    expect(decisionLabel('ai_auto', 'm')).toEqual({ label: '全自动 · m', tone: 'attention' })
    expect(decisionLabel(undefined)).toEqual({ label: '人工审核', tone: 'neutral' })
  })
})
