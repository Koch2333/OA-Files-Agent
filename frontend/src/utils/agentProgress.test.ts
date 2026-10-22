import { describe, expect, it } from 'vitest'
import type { TaskStatus } from '../api/types'
import { progressFor, type StepState } from './agentProgress'

const base = { total_files: 30, processed_files: 12, failure_count: 0, plan_id: null as string | null }
const states = (status: TaskStatus, extra: Record<string, unknown> = {}) => progressFor({ ...base, status, ...extra }).steps.map((s) => s.state)

describe('progressFor', () => {
  const table: [TaskStatus, StepState[]][] = [
    ['queued', ['pending', 'pending', 'pending', 'pending', 'pending']],
    ['scanning', ['active', 'pending', 'pending', 'pending', 'pending']],
    ['extracting', ['active', 'pending', 'pending', 'pending', 'pending']],
    ['analyzing', ['done', 'active', 'pending', 'pending', 'pending']],
    ['planning', ['done', 'done', 'active', 'pending', 'pending']],
    ['awaiting_review', ['done', 'done', 'done', 'waiting', 'pending']],
    ['approved', ['done', 'done', 'done', 'done', 'waiting']],
    ['archiving', ['done', 'done', 'done', 'done', 'active']],
    ['completed', ['done', 'done', 'done', 'done', 'done']],
  ]
  for (const [s, exp] of table) it(`maps ${s}`, () => expect(states(s)).toEqual(exp))

  it('covers every TaskStatus with five steps and a plain-text summary', () => {
    const all: TaskStatus[] = ['queued', 'scanning', 'extracting', 'analyzing', 'planning', 'awaiting_review', 'approved', 'archiving', 'completed', 'partial_failure', 'failed', 'cancelled']
    for (const s of all) {
      const v = progressFor({ ...base, status: s })
      expect(v.steps).toHaveLength(5)
      expect(v.summary.startsWith('Agent 执行进度：')).toBe(true)
    }
  })

  it('shows counts on the active step only', () => {
    const v = progressFor({ ...base, status: 'extracting' })
    expect(v.steps[0].note).toBe('12 / 30')
    expect(v.steps[1].note).toBe('')
    expect(v.focus).toBe(0)
    expect(progressFor({ ...base, status: 'analyzing' }).steps[1].note).toBe('12 / 30')
    expect(progressFor({ ...base, status: 'extracting', total_files: 0, processed_files: 0 }).steps[0].note).toBe('进行中')
  })

  it('highlights waiting for the user at approval', () => {
    const v = progressFor({ ...base, status: 'awaiting_review' })
    expect(v.needsYou).toBe(true)
    expect(v.steps[3].note).toBe('等待你审批')
    expect(v.focus).toBe(3)
    expect(progressFor({ ...base, status: 'approved' }).needsYou).toBe(true)
    expect(progressFor({ ...base, status: 'planning' }).needsYou).toBe(false)
  })

  it('skips approval for automatic tasks', () => {
    expect(states('planning', { automation: 'auto' })[3]).toBe('skipped')
    expect(progressFor({ ...base, status: 'planning', automation: 'auto' }).steps[3].note).toBe('全自动')
    expect(states('completed', { automation: 'auto' })).toEqual(['done', 'done', 'done', 'skipped', 'done'])
  })

  it('automatic task with leftovers waits for the user', () => {
    const v = progressFor({ ...base, status: 'awaiting_review', automation: 'auto' })
    expect(v.steps[3].state).toBe('waiting')
    expect(v.steps[3].note).toBe('余项待你确认')
    expect(v.needsYou).toBe(true)
  })

  it('places failure by how far the task got', () => {
    expect(states('failed')).toEqual(['error', 'pending', 'pending', 'pending', 'pending'])
    expect(states('failed', { processed_files: 30 })).toEqual(['done', 'done', 'error', 'pending', 'pending'])
    expect(states('failed', { plan_id: 'p', processed_files: 30 })).toEqual(['done', 'done', 'done', 'done', 'error'])
    expect(states('partial_failure', { plan_id: 'p', failure_count: 2 })).toEqual(['done', 'done', 'done', 'done', 'error'])
    expect(progressFor({ ...base, status: 'partial_failure', failure_count: 2 }).steps[4].note).toBe('2 个失败')
  })

  it('marks cancellation', () => {
    expect(states('cancelled')).toEqual(['cancelled', 'pending', 'pending', 'pending', 'pending'])
    expect(states('cancelled', { plan_id: 'p', processed_files: 30 })).toEqual(['done', 'done', 'done', 'cancelled', 'pending'])
    expect(progressFor({ ...base, status: 'cancelled' }).summary).toContain('已取消')
  })
})
