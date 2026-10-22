import { describe, expect, it } from 'vitest'
import type { LlmStatus } from '../api/types'
import { automationGate, chipText, effectiveSourceLine, statusView } from './aiStatus'

const base: LlmStatus = {
  configured: true, source: 'global', mode: 'llm', model: 'deepseek-flash', provider_label: 'DeepSeek', global_configured: true, personal_configured: false,
  personal_allowed: false, allow_personal_private_hosts: false, auto_archive_allowed: true, auto_ack_version: 'v1', auto_ack_text: '风险', setup_hint: 'none',
}
const none = (hint: LlmStatus['setup_hint']): LlmStatus => ({ ...base, configured: false, source: 'none', mode: 'heuristic', model: '', global_configured: false, auto_archive_allowed: false, setup_hint: hint })

describe('status → banner / CTA decision', () => {
  it('configured: shows the chip with source and model, no CTA', () => {
    const v = statusView(base)
    expect(v.ok).toBe(true)
    expect(v.chip).toBe('将使用：全局 · deepseek-flash')
    expect(v.cta).toBeNull()
    expect(chipText({ ...base, source: 'personal', model: 'qwen3.7-plus' })).toBe('将使用：个人 · qwen3.7-plus')
  })
  it('admin_global → primary button to /settings#llm', () => {
    const v = statusView(none('admin_global'))
    expect(v.ok).toBe(false)
    expect(v.cta).toEqual({ label: '去配置全局大模型', to: '/settings#llm', primary: true })
  })
  it('personal_or_admin → personal CTA and a hint about contacting the admin', () => {
    const v = statusView(none('personal_or_admin'))
    expect(v.cta?.label).toBe('配置我的个人大模型')
    expect(v.cta?.to).toBe('/settings#llm')
    expect(v.extra).toContain('联系管理员')
  })
  it('contact_admin → no button, only the instruction', () => {
    const v = statusView(none('contact_admin'))
    expect(v.cta).toBeNull()
    expect(v.extra).toContain('请联系管理员')
  })
  it('unknown status (not loaded yet) is not ok and has no CTA', () => {
    expect(statusView(null)).toMatchObject({ ok: false, cta: null })
  })
  it('effective source line for the settings page', () => {
    expect(effectiveSourceLine(base)).toContain('全局大模型（deepseek-flash）')
    expect(effectiveSourceLine({ ...base, source: 'personal', model: 'm' })).toContain('个人大模型（m）')
    expect(effectiveSourceLine(none('admin_global'))).toContain('AI 工作台不可用')
  })
})

describe('automation gating', () => {
  it('allowed only when the server says so for an operator+', () => {
    expect(automationGate(base, 'operator').allowed).toBe(true)
    expect(automationGate(base, 'admin').allowed).toBe(true)
  })
  it('explains why it is disabled: role, no LLM, policy', () => {
    expect(automationGate(base, 'viewer')).toMatchObject({ allowed: false, reason: 'role' })
    expect(automationGate(none('admin_global'), 'admin')).toMatchObject({ allowed: false, reason: 'no_llm' })
    const off = automationGate({ ...base, auto_archive_allowed: false }, 'operator')
    expect(off).toMatchObject({ allowed: false, reason: 'policy' })
    expect(off.text).toContain('管理员已关闭')
  })
  it('is disabled while the status is unknown', () => {
    expect(automationGate(null, 'admin')).toMatchObject({ allowed: false, reason: 'loading' })
  })
})
