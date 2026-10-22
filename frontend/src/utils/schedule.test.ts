import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { backoffDelay, createPoller } from './schedule'

describe('backoffDelay', () => {
  it('doubles per failure and caps', () => {
    expect(backoffDelay(5000, 0)).toBe(5000)
    expect(backoffDelay(5000, 1)).toBe(10000)
    expect(backoffDelay(5000, 2)).toBe(20000)
    expect(backoffDelay(5000, 10, 60000)).toBe(60000)
    expect(backoffDelay(5000, 99)).toBe(80000)
  })
})

describe('createPoller', () => {
  beforeEach(() => { vi.useFakeTimers() })
  afterEach(() => { vi.useRealTimers() })

  it('runs on the interval and stops cleanly', async () => {
    const run = vi.fn(async () => undefined)
    const p = createPoller({ run, intervalMs: 1000 })
    p.start()
    await vi.advanceTimersByTimeAsync(3500)
    expect(run).toHaveBeenCalledTimes(3)
    p.stop()
    await vi.advanceTimersByTimeAsync(5000)
    expect(run).toHaveBeenCalledTimes(3)
  })
  it('does not request anything while disabled (logged out / not initialised)', async () => {
    const run = vi.fn(async () => undefined)
    let on = false
    const p = createPoller({ run, intervalMs: 1000, enabled: () => on })
    p.start()
    await vi.advanceTimersByTimeAsync(10000)
    expect(run).not.toHaveBeenCalled()
    on = true
    await vi.advanceTimersByTimeAsync(1000)
    expect(run).toHaveBeenCalledTimes(1)
    p.stop()
  })
  it('backs off after failures and resets after a success', async () => {
    let fail = true
    const run = vi.fn(async () => { if (fail) throw new Error('503') })
    const p = createPoller({ run, intervalMs: 1000, maxIntervalMs: 4000 })
    p.start()
    await vi.advanceTimersByTimeAsync(1000) // 1st failure
    expect(p.failures()).toBe(1)
    await vi.advanceTimersByTimeAsync(1999)
    expect(run).toHaveBeenCalledTimes(1) // next one only after 2 s
    await vi.advanceTimersByTimeAsync(1)
    expect(run).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(4000)
    expect(run).toHaveBeenCalledTimes(3)
    await vi.advanceTimersByTimeAsync(4000) // capped at 4 s
    expect(run).toHaveBeenCalledTimes(4)
    fail = false
    await vi.advanceTimersByTimeAsync(4000)
    expect(p.failures()).toBe(0)
    await vi.advanceTimersByTimeAsync(1000)
    expect(run).toHaveBeenCalledTimes(6)
    p.stop()
  })
  it('pauses while hidden and catches up on resume()', async () => {
    const run = vi.fn(async () => undefined)
    let hidden = true
    const p = createPoller({ run, intervalMs: 1000, hidden: () => hidden })
    p.start()
    await vi.advanceTimersByTimeAsync(10000)
    expect(run).not.toHaveBeenCalled()
    hidden = false
    p.resume()
    await vi.advanceTimersByTimeAsync(0)
    expect(run).toHaveBeenCalledTimes(1)
    p.stop()
  })
  it('never overlaps runs', async () => {
    let release: () => void = () => undefined
    const run = vi.fn(() => new Promise<void>((r) => { release = r }))
    const p = createPoller({ run, intervalMs: 1000 })
    p.start()
    await vi.advanceTimersByTimeAsync(1000)
    p.kick()
    await vi.advanceTimersByTimeAsync(0)
    expect(run).toHaveBeenCalledTimes(1)
    release()
    p.stop()
  })
})
