import { describe, expect, it } from 'vitest'
import { canAdvance, type SetupFormLike } from './setup'

const st = { needs_token: false, loopback: false, checks: [] }
const form = (over: Partial<SetupFormLike> = {}): SetupFormLike => ({
  mode: 'enterprise', token: '', username: 'admin', displayName: 'A', password: '', password2: '',
  rpId: 'VM-32-16-debian', origins: ['http://106.55.162.82:8080'], minLen: 12, sessionIdle: 60, sessionMax: 12, ...over,
})

describe('policy step without Passkey (plain HTTP / IP address)', () => {
  it('asks for a matching RP ID when Passkey works', () => {
    expect(canAdvance('policy', form(), st, true)).toMatch(/主机名必须等于 RP ID/)
  })

  it('does not require RP ID or origins when Passkey is unavailable', () => {
    expect(canAdvance('policy', form({ rpId: '', origins: [] }), st, false)).toBeNull()
    expect(canAdvance('policy', form(), st, false)).toBeNull()
  })

  it('still checks the session limits', () => {
    expect(canAdvance('policy', form({ sessionIdle: 1 }), st, false)).toMatch(/空闲超时/)
  })
})
