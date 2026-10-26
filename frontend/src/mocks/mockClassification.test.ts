import { describe, expect, it } from 'vitest'
import { mockApi } from './mockApi'
import { OTHER } from '../utils/classification'

async function seededPlan() {
  const tasks = await mockApi.listTasks({ page_size: 100 })
  for (const t of tasks.items) {
    if (!t.plan_id) continue
    const p = await mockApi.getPlan(t.plan_id)
    if (p.items.some((i) => (i.fallback_fields ?? []).length)) return p
  }
  throw new Error('no plan with fallback items in the demo data')
}

describe('mock: fallback 其他 and classification options', () => {
  it('keeps flagged items in review until confirmed explicitly', async () => {
    const p = await seededPlan()
    const it = p.items.find((i) => (i.fallback_fields ?? []).length)!
    expect(it.department).toBe(OTHER)
    expect(it.review_required).toBe(true)
    const confirmed = await mockApi.patchPlan(p.id, p.version, [{ file_id: it.file_id, department: OTHER, document_type: OTHER }])
    const after = confirmed.items.find((i) => i.file_id === it.file_id)!
    expect(after.fallback_fields).toEqual([])
    expect(after.warnings.some((w) => w.includes('已暂填'))).toBe(false)
  })
  it('rejects unsafe department values like the backend (422)', async () => {
    const t = (await mockApi.listTasks({ page_size: 100 })).items.find((x) => x.plan_id)!
    const p = await mockApi.getPlan(t.plan_id!)
    await expect(mockApi.patchPlan(p.id, p.version, [{ file_id: p.items[0].file_id, department: 'a/b' }])).rejects.toMatchObject({ status: 422 })
  })
  it('options end with 其他 and have no duplicates', async () => {
    const o = await mockApi.getClassificationOptions()
    for (const list of [o.departments, o.types]) {
      expect(list.at(-1)?.value).toBe(OTHER)
      const keys = list.map((x) => x.value.toLowerCase())
      expect(new Set(keys).size).toBe(keys.length)
    }
  })
  it('ledger filter 其他 matches explicit and empty values', async () => {
    const all = await mockApi.listLedger({ page_size: 200, department: OTHER })
    expect(all.items.every((l) => l.department === OTHER)).toBe(true)
  })
})
