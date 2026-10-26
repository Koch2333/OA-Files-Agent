import { afterEach, describe, expect, it, vi } from 'vitest'
import { uuid } from './uuid'

const V4 = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/

describe('uuid', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('returns a v4 uuid', () => {
    expect(uuid()).toMatch(V4)
  })

  it('works without crypto.randomUUID (plain-HTTP origin)', () => {
    const real = globalThis.crypto
    vi.stubGlobal('crypto', { getRandomValues: (a: Uint8Array) => real.getRandomValues(a) })
    expect(uuid()).toMatch(V4)
    expect(uuid()).not.toBe(uuid())
  })

  it('works without any crypto', () => {
    vi.stubGlobal('crypto', undefined)
    expect(uuid()).toMatch(V4)
  })
})
