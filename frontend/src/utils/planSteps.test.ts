import { afterEach, describe, expect, it, vi } from 'vitest'
import type { ArchivePlanItem } from '../api/types'
import { coachSeen, markCoachSeen, planStepView } from './planSteps'

const item = (o: Partial<ArchivePlanItem> = {}): ArchivePlanItem => ({ file_id: Math.random().toString(), original_name: 'a', suggested_name: 'a', source_path: 'a', destination_path: 'x/a', department: '财务部', document_type: '合同', document_date: '2026-01-01', warnings: [], review_required: false, ...o })
const plan = (status: string, items: ArchivePlanItem[] = [item()]) => ({ status: status as never, items })

describe('planStepView', () => {
  it('unapproved: step 1 active, step 2 locked with a one-line reason', () => {
    const v = planStepView(plan('awaiting_review'))
    expect([v.phase, v.approve, v.execute]).toEqual(['unapproved', 'active', 'locked'])
    expect(v.executeHint).toBe('请先批准计划，再执行归档')
    expect(v.approveHint).toBeNull()
  })
  it('blocked by review items, counting fallback 其他 items separately', () => {
    const v = planStepView(plan('awaiting_review', [item({ review_required: true, fallback_fields: ['department'] }), item({ review_required: true }), item()]))
    expect([v.phase, v.approve, v.execute]).toEqual(['blocked', 'blocked', 'locked'])
    expect([v.blocking, v.fallback]).toEqual([2, 1])
    expect(v.approveHint).toBe('还有 2 项待确认，确认后才能批准')
    expect(v.executeHint).toContain('2 项')
  })
  it('approved: step 1 done, step 2 ready', () => {
    const v = planStepView(plan('approved'))
    expect([v.phase, v.approve, v.execute, v.executeHint]).toEqual(['approved', 'done', 'ready', null])
  })
  it('executing / executed / failed', () => {
    expect(planStepView(plan('executing'))).toMatchObject({ phase: 'executing', approve: 'done', execute: 'running' })
    expect(planStepView(plan('executed'))).toMatchObject({ phase: 'done', approve: 'done', execute: 'done' })
    expect(planStepView(plan('failed'))).toMatchObject({ phase: 'failed', execute: 'locked' })
  })
  it('busy flags show progress', () => {
    expect(planStepView(plan('awaiting_review'), 'approve').approve).toBe('busy')
    expect(planStepView(plan('approved'), 'execute').execute).toBe('running')
  })
  it('works without a plan', () => { expect(planStepView(undefined).phase).toBe('unapproved') })
})

describe('coach mark storage', () => {
  afterEach(() => vi.unstubAllGlobals())
  it('remembers dismissal', () => {
    const m = new Map<string, string>()
    vi.stubGlobal('localStorage', { getItem: (k: string) => m.get(k) ?? null, setItem: (k: string, v: string) => m.set(k, v) })
    expect(coachSeen()).toBe(false)
    markCoachSeen()
    expect(coachSeen()).toBe(true)
  })
  it('never throws and counts as seen when storage is unavailable', () => {
    vi.stubGlobal('localStorage', { getItem: () => { throw new Error('blocked') }, setItem: () => { throw new Error('blocked') } })
    expect(coachSeen()).toBe(true)
    expect(() => markCoachSeen()).not.toThrow()
  })
})
