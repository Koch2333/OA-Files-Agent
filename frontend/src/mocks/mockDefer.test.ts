import { describe, expect, it } from 'vitest'
import { ApiError } from '../api/types'
import { mockApi } from './mockApi'

describe('mock "review later" mirrors the backend', () => {
  it('splits the chosen items into a queued plan and keeps the rest approvable', async () => {
    const queue = await mockApi.getReviewQueue()
    const entry = queue.find((e) => e.items >= 2)
    expect(entry).toBeTruthy()
    const p = await mockApi.getPlan(entry!.plan_id)
    const [a, ...others] = p.items
    // rejected: stale version, nothing selected, everything selected
    const code = async (f: () => Promise<unknown>) => { try { await f(); return 'ok' } catch (e) { return e instanceof ApiError ? e.status : 'other' } }
    expect(await code(() => mockApi.deferPlanItems(p.id, p.version + 5, [a.file_id]))).toBe(409)
    expect(await code(() => mockApi.deferPlanItems(p.id, p.version, []))).toBe(422)
    expect(await code(() => mockApi.deferPlanItems(p.id, p.version, p.items.map((i) => i.file_id)))).toBe(422)
    const res = await mockApi.deferPlanItems(p.id, p.version, [a.file_id])
    expect(res.plan.items.map((i) => i.file_id)).toEqual(others.map((i) => i.file_id))
    expect(res.plan.version).toBe(p.version + 1)
    const later = await mockApi.getPlan(res.deferred_plan_id)
    expect(later.items.map((i) => i.file_id)).toEqual([a.file_id])
    expect(later.status).toBe('awaiting_review')
    const q2 = await mockApi.getReviewQueue()
    expect(q2.find((e) => e.plan_id === res.deferred_plan_id)).toMatchObject({ deferred: true, items: 1 })
    expect(q2.find((e) => e.plan_id === p.id)).toMatchObject({ deferred: false, items: others.length })
  })
})

describe('mock "return for re-analysis" mirrors the backend', () => {
  it('moves only the chosen files into a new queued task and keeps the rest of the plan', async () => {
    const entry = (await mockApi.getReviewQueue()).find((e) => e.items >= 2)
    expect(entry).toBeTruthy()
    const p = await mockApi.getPlan(entry!.plan_id)
    const [a, ...others] = p.items
    const code = async (f: () => Promise<unknown>) => { try { await f(); return 'ok' } catch (e) { return e instanceof ApiError ? e.status : 'other' } }
    expect(await code(() => mockApi.reanalyzePlanItems(p.id, p.version + 9, [a.file_id]))).toBe(409)
    expect(await code(() => mockApi.reanalyzePlanItems(p.id, p.version, p.items.map((i) => i.file_id)))).toBe(422)
    const res = await mockApi.reanalyzePlanItems(p.id, p.version, [a.file_id], '按项目名归档')
    expect(res.plan.items.map((i) => i.file_id)).toEqual(others.map((i) => i.file_id))
    const t = await mockApi.getTask(res.task_id)
    expect(t).toMatchObject({ status: 'queued', total_files: 1, automation: 'review', instruction: '按项目名归档' })
  })
})
