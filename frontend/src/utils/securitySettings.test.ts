import { describe, expect, it } from 'vitest'
import { buildPatch, rpChanged, toForm, validateSettings } from './securitySettings'
import type { SecuritySettings } from '../api'

const base: SecuritySettings = { mode: 'enterprise', rp_id: 'oa.example.com', rp_name: 'OA', origins: ['https://oa.example.com'], mfa_policy: 'admins', allow_password_login: true, session_idle_minutes: 60, session_max_hours: 12, password_min_length: 12, lockout_threshold: 5, lockout_minutes: 15, passkey_available: true }

describe('security settings form', () => {
  it('round-trips without changes', () => {
    const f = toForm(base)
    expect(validateSettings(f)).toEqual({})
    expect(buildPatch(base, f, false)).toEqual({})
    expect(rpChanged(base, f)).toBe(false)
  })
  it('validates ranges and relationships', () => {
    const f = toForm(base)
    f.idle = 3; f.max = 0; f.lockThreshold = 2; f.minLen = 200
    const e = validateSettings(f)
    expect(Object.keys(e).sort()).toEqual(['idle', 'lockThreshold', 'max', 'minLen'])
    const g = toForm(base); g.idle = 1000; g.max = 5
    expect(validateSettings(g).idle).toContain('不能大于')
  })
  it('requires confirmation flag only when rp/origins change', () => {
    const f = toForm(base)
    f.rpId = 'example.com'
    expect(rpChanged(base, f)).toBe(true)
    expect(buildPatch(base, f, false)).toEqual({ rp_id: 'example.com' })
    expect(buildPatch(base, f, true)).toEqual({ rp_id: 'example.com', confirm_passkey_reset: true })
    const g = toForm(base); g.mfaPolicy = 'all'
    expect(buildPatch(base, g, true)).toEqual({ mfa_policy: 'all' })
  })
  it('rejects origins outside the rp id', () => {
    const f = toForm(base); f.originsText = 'https://evil.com'
    expect(validateSettings(f).rpId).toBeDefined()
  })
})
