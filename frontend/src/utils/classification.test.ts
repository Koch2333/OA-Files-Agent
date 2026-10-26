import { describe, expect, it } from 'vitest'
import type { ArchivePlanItem } from '../api/types'
import { buildComboGroups, categoryKey, confirmPatches, displayCategory, fallbackItems, flattenGroups, OTHER, validateCategory, type CategoryOption } from './classification'

const opts: CategoryOption[] = [
  { value: '财务部', source: 'builtin', count: 0 },
  { value: '人事部', source: 'builtin', count: 4 },
  { value: '法务部', source: 'ledger', count: 9 },
  { value: '其他', source: 'reserved', count: 0 },
]
const item = (o: Partial<ArchivePlanItem>): ArchivePlanItem => ({ file_id: 'f', original_name: 'a', suggested_name: 'a', source_path: 'a', destination_path: 'x/a', department: '其他', document_type: '合同', document_date: null, warnings: [], review_required: true, ...o })

describe('displayCategory', () => {
  it('maps empty/unknown to 其他 and keeps real values', () => {
    expect([null, undefined, '', '  ', '待审核', '其他'].map(displayCategory)).toEqual(Array(6).fill(OTHER))
    expect(displayCategory('财务部')).toBe('财务部')
  })
})

describe('validateCategory', () => {
  it('accepts empty and ordinary names, rejects unsafe ones with a message', () => {
    expect(validateCategory('')).toBeNull()
    expect(validateCategory('研发 & 测试')).toBeNull()
    for (const bad of ['a/b', 'a\\b', 'a:b', '..', 'x‮y', '好'.repeat(61)]) expect(validateCategory(bad)).toBeTruthy()
  })
})

describe('buildComboGroups', () => {
  it('orders used (most used first), defined, then 其他 last', () => {
    const g = buildComboGroups(opts, '', { typing: false })
    expect(g.map((x) => x.id)).toEqual(['used', 'defined', 'reserved'])
    expect(g[0].items.map((i) => i.value)).toEqual(['法务部', '人事部'])
    expect(g[1].items.map((i) => i.value)).toEqual(['财务部'])
    expect(flattenGroups(g).at(-1)?.value).toBe(OTHER)
  })
  it('filters while typing, case- and normalisation-insensitively, and offers create for new valid text', () => {
    expect(flattenGroups(buildComboGroups(opts, '务')).map((i) => i.value)).toEqual(['法务部', '财务部', '务'])
    const latin: CategoryOption[] = [{ value: 'R&D 部', source: 'ledger', count: 1 }]
    expect(flattenGroups(buildComboGroups(latin, 'r&d')).map((i) => i.value)).toEqual(['R&D 部', 'r&d'])
  })
  it('create row only for values not in the list and passing validation', () => {
    expect(buildComboGroups(opts, '研发部').at(-1)).toMatchObject({ id: 'create', items: [{ value: '研发部' }] })
    expect(buildComboGroups(opts, '法务部').some((g) => g.id === 'create')).toBe(false)
    expect(buildComboGroups(opts, ' 法务部 ').some((g) => g.id === 'create')).toBe(false)
    expect(buildComboGroups(opts, '其他').some((g) => g.id === 'create')).toBe(false)
    expect(buildComboGroups(opts, 'a/b').some((g) => g.id === 'create')).toBe(false)
  })
  it('keeps the whole list when the field merely shows its current value (not typing)', () => {
    expect(flattenGroups(buildComboGroups(opts, '法务部', { typing: false }))).toHaveLength(4)
  })
  it('hides 其他 when the filter does not match it', () => {
    expect(flattenGroups(buildComboGroups(opts, '财务')).map((i) => i.value)).not.toContain(OTHER)
  })
})

describe('categoryKey', () => {
  it('normalises NFC, trims and lowercases', () => { expect(categoryKey(' É ')).toBe(categoryKey('é')) })
})

describe('fallback helpers', () => {
  const items = [item({ file_id: '1', fallback_fields: ['department'] }), item({ file_id: '2', department: '财务部', fallback_fields: [] }), item({ file_id: '3', fallback_fields: ['department', 'document_type'], document_type: '其他' }), item({ file_id: '4' })]
  it('selects only flagged items', () => { expect(fallbackItems(items).map((i) => i.file_id)).toEqual(['1', '3']) })
  it('builds explicit confirm patches for exactly the flagged fields', () => {
    expect(confirmPatches(items)).toEqual([{ file_id: '1', department: '其他' }, { file_id: '3', department: '其他', document_type: '其他' }])
  })
})
