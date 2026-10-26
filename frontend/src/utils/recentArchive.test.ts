import { describe, expect, it } from 'vitest'
import type { LedgerEntry } from '../api/types'
import { archiveRow, normDate, relTime, truncateName } from './recentArchive'

const NOW = Date.parse('2026-10-22T12:00:00Z')
const entry = (o: Partial<LedgerEntry> = {}): LedgerEntry => ({
  id: 'l1', original_name: 'scan_0012.pdf', canonical_name: '2026-03-12_采购部_合同_2026年度采购合同.docx', department: '采购部', document_type: '合同', document_date: '2026-03-12',
  archive_path: '归档/采购部/2026/合同/x.docx', sha256: 'ab', task_id: 't', status: 'archived', executed_at: '2026-10-22T11:30:00Z', ...o,
})

describe('archiveRow', () => {
  it('shows readable name, chips and relative time, never original name or path', () => {
    const r = archiveRow(entry(), NOW, 80)
    expect(r.title).toBe('2026-03-12_采购部_合同_2026年度采购合同.docx')
    expect(r.department.text).toBe('采购部')
    expect(r.type.text).toBe('合同')
    expect(r.date.text).toBe('2026-03-12')
    expect(r.when).toBe('30 分钟前')
    expect(JSON.stringify(r)).not.toContain('scan_0012')
    expect(JSON.stringify(r)).not.toContain('归档/采购部')
  })
  it('truncates long names in the middle and keeps the extension and the full name for the tooltip', () => {
    const long = '2026-03-12_采购部_合同_' + '很长的项目名称'.repeat(8) + '.docx'
    const r = archiveRow(entry({ canonical_name: long }))
    expect(Array.from(r.title).length).toBeLessThanOrEqual(36)
    expect(r.title.endsWith('.docx')).toBe(true)
    expect(r.title).toContain('…')
    expect(r.fullTitle).toBe(long)
  })
  it('shows empty or unknown department/type as 其他 and marks the missing date; keeps the decision', () => {
    const r = archiveRow(entry({ department: '', document_type: ' ', document_date: '', decision: 'ai_auto', decision_model: 'm1' }), NOW)
    expect(r.department).toEqual({ text: '其他', missing: false })
    expect(r.type).toEqual({ text: '其他', missing: false })
    expect(archiveRow(entry({ department: '待审核' })).department.text).toBe('其他')
    expect(r.date).toEqual({ text: '日期未知', missing: true })
    expect(r.decision).toBe('ai_auto')
    expect(r.decisionModel).toBe('m1')
    expect(archiveRow(entry()).decision).toBe('manual')
  })
})
describe('helpers', () => {
  it('truncateName leaves short names alone', () => {
    expect(truncateName('a.pdf')).toBe('a.pdf')
    expect(truncateName('x'.repeat(60)).length).toBeLessThanOrEqual(36)
  })
  it('normDate normalises common spellings', () => {
    expect(normDate('2026-3-5')).toBe('2026-03-05')
    expect(normDate('2026/03/05')).toBe('2026-03-05')
    expect(normDate('2026年3月5日')).toBe('2026-03-05')
    expect(normDate('2026-13-01')).toBe('')
    expect(normDate(null)).toBe('')
  })
  it('relTime buckets', () => {
    const at = (s: number) => new Date(NOW - s * 1000).toISOString()
    expect(relTime(at(5), NOW)).toBe('刚刚')
    expect(relTime(at(3 * 3600), NOW)).toBe('3 小时前')
    expect(relTime(at(30 * 3600), NOW)).toBe('昨天')
    expect(relTime(at(5 * 86400), NOW)).toBe('5 天前')
    expect(relTime(at(90 * 86400), NOW)).toMatch(/^\d{4}-\d{2}-\d{2}$/)
    expect(relTime(undefined, NOW)).toBe('—')
  })
})
