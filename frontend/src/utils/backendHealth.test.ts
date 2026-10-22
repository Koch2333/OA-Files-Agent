import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createHealthMonitor } from './backendHealth'

describe('backend health monitor', () => {
  beforeEach(() => { vi.useFakeTimers() })
  afterEach(() => { vi.useRealTimers() })
  it('marks down, probes with backoff (5 s, then 10 s, ...) and clears when the backend recovers', async () => {
    const changes: boolean[] = []
    let alive = false
    const probe = vi.fn(async () => alive)
    const m = createHealthMonitor({ probe, onChange: (d) => changes.push(d) })
    m.markDown()
    m.markDown() // 重复标记无副作用
    expect(changes).toEqual([true])
    await vi.advanceTimersByTimeAsync(5000)
    expect(probe).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(5000) // 退避：第二次要等 10 s
    expect(probe).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(5000)
    expect(probe).toHaveBeenCalledTimes(2)
    expect(m.isDown()).toBe(true)
    alive = true
    await vi.advanceTimersByTimeAsync(20000)
    expect(changes).toEqual([true, false])
    expect(m.isDown()).toBe(false)
    const calls = probe.mock.calls.length
    await vi.advanceTimersByTimeAsync(120000)
    expect(probe).toHaveBeenCalledTimes(calls) // 恢复后停止轮询
  })
  it('treats a throwing probe as still down', async () => {
    const m = createHealthMonitor({ probe: async () => { throw new Error('refused') }, onChange: () => undefined })
    m.markDown()
    await vi.advanceTimersByTimeAsync(5000)
    expect(m.isDown()).toBe(true)
    m.stop()
  })
  it('sends nothing while the backend was never marked down', async () => {
    const probe = vi.fn(async () => true)
    createHealthMonitor({ probe, onChange: () => undefined })
    await vi.advanceTimersByTimeAsync(120000)
    expect(probe).not.toHaveBeenCalled()
  })
})
