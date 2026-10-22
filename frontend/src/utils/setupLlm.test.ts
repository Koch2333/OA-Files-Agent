import { describe, expect, it } from 'vitest'
import { llmStepState, llmSummary, policyPatchForWizard } from './setupLlm'
import { nextStep, prevStep, restoreStep, stepsFor } from './setup'

describe('wizard LLM step', () => {
  it('state: configured only when mode=llm and state=ok', () => {
    expect(llmStepState(null)).toBe('pending')
    expect(llmStepState({ mode: 'heuristic', state: 'ok' })).toBe('pending')
    expect(llmStepState({ mode: 'llm', state: 'unavailable' })).toBe('pending')
    expect(llmStepState({ mode: 'llm', state: 'ok' })).toBe('configured')
  })
  it('summary explains the skipped case', () => {
    expect(llmSummary(null, false).llm).toContain('未配置')
    expect(llmSummary({ mode: 'llm', state: 'ok', model: 'qwen' }, true)).toEqual({ llm: '已配置（qwen）', personal: '允许员工配置个人大模型 API' })
  })
  it('the policy is written only when the toggle is on', () => {
    expect(policyPatchForWizard(false)).toBeNull()
    expect(policyPatchForWizard(true)).toEqual({ allow_personal_llm: true })
  })
  it('the step sits after MFA and before finish for enterprise; local mode is unchanged', () => {
    const e = stepsFor('enterprise')
    expect(e.slice(-3)).toEqual(['mfa', 'llm', 'finish'])
    expect(nextStep('mfa', 'enterprise')).toBe('llm')
    expect(nextStep('llm', 'enterprise')).toBe('finish')
    expect(prevStep('finish', 'enterprise')).toBe('llm')
    expect(stepsFor('local')).toEqual(['welcome', 'token', 'mode', 'finish'])
  })
  it('a reload on the llm step restarts before account creation (the server was already initialised only after complete)', () => {
    expect(restoreStep('llm', { mode: 'enterprise', password: 'x', token: 't' }, true)).toBe('policy')
  })
})

import { llmStepActions as actions } from './setupLlm'
describe('llmStepActions', () => {
  it('requires an explicit skip while the LLM is not configured', () => {
    expect(actions('pending', null)).toEqual({ canContinue: false, canSkip: true, showRetry: false })
  })
  it('allows continuing once configured', () => {
    expect(actions('configured', null)).toEqual({ canContinue: true, canSkip: false, showRetry: false })
  })
  it('keeps the step open with retry and skip on any error', () => {
    expect(actions('pending', '服务器内部错误')).toEqual({ canContinue: false, canSkip: true, showRetry: true })
    expect(actions('configured', '网络错误')).toEqual({ canContinue: false, canSkip: true, showRetry: true })
  })
})
