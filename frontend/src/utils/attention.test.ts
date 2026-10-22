import { describe, expect, it } from 'vitest'
import type { ImportJob, TaskStatus } from '../api/types'
import { deriveAttention } from './attention'

const NOW = Date.parse('2026-10-22T12:00:00Z')
const task = (id: string, status: TaskStatus, o: Partial<ImportJob> = {}): ImportJob => ({
  id: id + '-0000-0000-0000-000000000000', status, total_files: 5, processed_files: 5, success_count: 5, failure_count: 0, plan_id: null,
  created_at: '2026-10-22T10:00:00Z', updated_at: '2026-10-22T10:30:00Z', ...o,
})

describe('deriveAttention', () => {
  it('is empty when nothing needs the user', () => {
    expect(deriveAttention([task('aaaaaaaa', 'completed'), task('bbbbbbbb', 'archiving')], 0, NOW)).toEqual([])
  })
  it('lists plans awaiting approval with a direct plan link', () => {
    const [i] = deriveAttention([task('aaaaaaaa', 'awaiting_review', { plan_id: 'p1' })], 0, NOW)
    expect(i.kind).toBe('approve')
    expect(i.to).toBe('/plans/p1')
    expect(i.actionLabel).toBe('去审批')
    expect(i.meta).toContain('5 个文件')
  })
  it('falls back to the task page without a plan and shows 查看 for read-only users', () => {
    const [i] = deriveAttention([task('aaaaaaaa', 'awaiting_review')], 0, NOW, false)
    expect(i.to).toBe('/tasks/aaaaaaaa-0000-0000-0000-000000000000')
    expect(i.actionLabel).toBe('查看')
  })
  it('separates automatic leftovers, approved-not-executed and review files', () => {
    const items = deriveAttention([task('aaaaaaaa', 'awaiting_review', { automation: 'auto', plan_id: 'p' }), task('bbbbbbbb', 'approved', { plan_id: 'q' })], 3, NOW)
    expect(items.map((x) => x.kind)).toEqual(['auto_left', 'execute', 'review'])
    expect(items[2].title).toBe('3 个文件需要人工确认')
    expect(items[2].to).toBe('/files?review=1')
  })
  it('orders approvals first and failures last, and only shows recent failures', () => {
    const old = task('cccccccc', 'failed', { updated_at: '2026-08-01T00:00:00Z' })
    const items = deriveAttention([task('dddddddd', 'failed'), task('aaaaaaaa', 'awaiting_review', { plan_id: 'p' }), old, task('eeeeeeee', 'partial_failure', { failure_count: 2 })], 1, NOW)
    expect(items.map((x) => x.kind)).toEqual(['approve', 'review', 'failed', 'failed'])
    expect(items.filter((x) => x.kind === 'failed').every((x) => x.tone === 'error' && x.retryable)).toBe(true)
    expect(items.find((x) => x.title.includes('2 个文件处理失败'))).toBeTruthy()
  })
  it('does not offer retry to read-only users', () => {
    expect(deriveAttention([task('dddddddd', 'failed')], 0, NOW, false)[0].retryable).toBe(false)
  })
})
