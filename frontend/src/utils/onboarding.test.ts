import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { markTourSeen, TOUR_KEY, tourAutoShow, tourSeen, tourSteps } from './onboarding'

describe('onboarding tour', () => {
  let m: Map<string, string>
  beforeEach(() => {
    m = new Map()
    vi.stubGlobal('localStorage', { getItem: (k: string) => m.get(k) ?? null, setItem: (k: string, v: string) => m.set(k, v) })
  })
  afterEach(() => vi.unstubAllGlobals())

  it('operators see the whole path, read-only accounts only what they can use', () => {
    expect(tourSteps('operator', true).map((s) => s.id)).toEqual(['import', 'recognize', 'review', 'auto', 'ledger'])
    expect(tourSteps('viewer', false).map((s) => s.id)).toEqual(['ledger'])
    expect(tourSteps(null, false)).toEqual([])
  })

  it('every step is explained, and every link is an absolute app route with a label', () => {
    for (const s of tourSteps('admin', true)) {
      expect(s.title.length).toBeGreaterThan(2)
      expect(s.body.length).toBeGreaterThan(0)
      if (s.to) { expect(s.to.startsWith('/')).toBe(true); expect(s.toLabel).toBeTruthy() }
    }
  })

  it('is shown once: closing it in any way marks it as seen', () => {
    expect(tourSeen()).toBe(false)
    markTourSeen()
    expect(m.get(TOUR_KEY)).toBe('1')
    expect(tourSeen()).toBe(true)
  })

  it('never nags when the browser storage is unavailable', () => {
    vi.stubGlobal('localStorage', { getItem: () => { throw new Error('blocked') }, setItem: () => { throw new Error('blocked') } })
    expect(tourSeen()).toBe(true)
    expect(() => markTourSeen()).not.toThrow()
  })
})

describe('auto-show', () => {
  afterEach(() => vi.unstubAllGlobals())
  it('appears for a person who has not seen it, but never inside an automated browser', () => {
    vi.stubGlobal('localStorage', { getItem: () => null, setItem: () => undefined })
    vi.stubGlobal('navigator', { webdriver: false })
    expect(tourAutoShow()).toBe(true)
    vi.stubGlobal('navigator', { webdriver: true })
    expect(tourAutoShow()).toBe(false)
    vi.stubGlobal('navigator', { webdriver: false })
    vi.stubGlobal('localStorage', { getItem: () => '1', setItem: () => undefined })
    expect(tourAutoShow()).toBe(false)
  })
})
