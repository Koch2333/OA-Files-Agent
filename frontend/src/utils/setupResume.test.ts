import { describe, expect, it } from 'vitest'
import { finishFailureStep, resumeStep } from './setup'

const base = { scope: 'full' as const, mfaRequiredByPolicy: false, hasMfa: false }

describe('wizard resume step (derived from server state)', () => {
  it('goes to the mandatory MFA step when the session is restricted', () => {
    expect(resumeStep({ ...base, scope: 'enroll_mfa', mfaRequiredByPolicy: true })).toEqual({ step: 'mfa', mfaRequired: true, mfaDone: false })
  })
  it('goes to MFA when the policy requires it and none is enrolled, even if a later phase was saved', () => {
    expect(resumeStep({ ...base, mfaRequiredByPolicy: true, saved: 'llm' })).toEqual({ step: 'mfa', mfaRequired: true, mfaDone: false })
  })
  it('shows the enrolled MFA view (regenerate recovery codes) after a reload', () => {
    expect(resumeStep({ ...base, mfaRequiredByPolicy: true, hasMfa: true })).toEqual({ step: 'mfa', mfaRequired: true, mfaDone: true })
  })
  it('starts at the optional MFA step when nothing is known', () => {
    expect(resumeStep(base)).toEqual({ step: 'mfa', mfaRequired: false, mfaDone: false })
  })
  it('returns to a later phase this tab already reached', () => {
    expect(resumeStep({ ...base, hasMfa: true, saved: 'llm' }).step).toBe('llm')
    expect(resumeStep({ ...base, saved: 'finish' }).step).toBe('finish')
    expect(resumeStep({ ...base, saved: 'welcome' }).step).toBe('mfa')
  })
})

describe('finishFailureStep', () => {
  it('sends the user back to MFA when the policy still requires it', () => {
    expect(finishFailureStep({ status: 409, code: 'MFA_REQUIRED_BY_POLICY' })).toBe('mfa')
  })
  it('stays on the finish step for any other failure', () => {
    expect(finishFailureStep({ status: 0, code: 'NETWORK' })).toBe('finish')
    expect(finishFailureStep({ status: 409, code: 'WIZARD_REQUIRED' })).toBe('finish')
    expect(finishFailureStep(null)).toBe('finish')
  })
})
