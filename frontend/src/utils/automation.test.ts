import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import type { LlmStatus } from '../api/types'
import type { Role } from './authz'
import { AUTO_CONFIRM_DELAY_MS, buildImportOptions, canConfirmAuto, forgetAcknowledged, hasAcknowledged, useAutomation } from './automation'

const status = (over: Partial<LlmStatus> = {}): LlmStatus => ({
  configured: true, source: 'global', mode: 'llm', model: 'deepseek-flash', provider_label: 'DeepSeek', global_configured: true, personal_configured: false,
  personal_allowed: false, allow_personal_private_hosts: false, auto_archive_allowed: true, auto_ack_version: 'v1', auto_ack_text: '原文', setup_hint: 'none', ...over,
})

describe('buildImportOptions', () => {
  it('returns undefined when nothing non-default is set (no options field is sent)', () => {
    expect(buildImportOptions({})).toBeUndefined()
    expect(buildImportOptions({ instruction: '   ', automation: 'review', llmSource: 'auto' })).toBeUndefined()
  })
  it('carries a trimmed, length-limited instruction', () => {
    expect(buildImportOptions({ instruction: '  只处理采购  ' })).toEqual({ instruction: '只处理采购' })
    expect(buildImportOptions({ instruction: 'x'.repeat(2500) })?.instruction).toHaveLength(2000)
  })
  it('auto mode carries automation + auto_ack {version, accepted:true}', () => {
    expect(buildImportOptions({ automation: 'auto', ackVersion: 'v7', instruction: 'a' })).toEqual({ instruction: 'a', automation: 'auto', auto_ack: { version: 'v7', accepted: true } })
  })
  it('refuses to build an auto payload without an acknowledgement version', () => {
    expect(() => buildImportOptions({ automation: 'auto' })).toThrow()
  })
  it('only sends llm_source when it is not "auto"', () => {
    expect(buildImportOptions({ llmSource: 'personal' })).toEqual({ llm_source: 'personal' })
  })
})

describe('confirm gating', () => {
  it('needs both the checkbox and 3 seconds', () => {
    expect(canConfirmAuto(AUTO_CONFIRM_DELAY_MS, false)).toBe(false)
    expect(canConfirmAuto(AUTO_CONFIRM_DELAY_MS - 1, true)).toBe(false)
    expect(canConfirmAuto(AUTO_CONFIRM_DELAY_MS, true)).toBe(true)
  })
})

describe('useAutomation state machine', () => {
  beforeEach(() => { vi.useFakeTimers(); forgetAcknowledged() })
  afterEach(() => { vi.useRealTimers(); forgetAcknowledged() })
  const make = (s: LlmStatus | null = status(), role: Role = 'operator') => {
    const st = ref<LlmStatus | null>(s), r = ref<Role | null>(role)
    return { st, r, a: useAutomation(st, r) }
  }

  it('selecting auto opens the dialog; mode is not changed until confirmed', () => {
    const { a } = make()
    a.select('auto')
    expect(a.dialogOpen.value).toBe(true)
    expect(a.mode.value).toBe('review')
  })
  it('confirm is blocked for 3 s even when the box is ticked, and without the box afterwards', () => {
    const { a } = make()
    a.select('auto')
    a.checked.value = true
    vi.advanceTimersByTime(2900)
    expect(a.canConfirm.value).toBe(false)
    expect(a.confirm()).toBe(false)
    expect(a.remainingSec.value).toBe(1)
    vi.advanceTimersByTime(200)
    expect(a.canConfirm.value).toBe(true)
    a.checked.value = false
    expect(a.canConfirm.value).toBe(false)
    a.checked.value = true
    expect(a.confirm()).toBe(true)
    expect(a.mode.value).toBe('auto')
    expect(a.dialogOpen.value).toBe(false)
  })
  it('cancel / Esc reverts to review', () => {
    const { a } = make()
    a.select('auto'); a.cancel()
    expect(a.mode.value).toBe('review')
    expect(a.dialogOpen.value).toBe(false)
  })
  it('acknowledgement is remembered in memory for the session only (same version), not in storage', () => {
    const store: Record<string, string> = {}
    vi.stubGlobal('localStorage', { getItem: (k: string) => store[k] ?? null, setItem: (k: string, v: string) => { store[k] = v } })
    vi.stubGlobal('sessionStorage', { getItem: (k: string) => store[k] ?? null, setItem: (k: string, v: string) => { store[k] = v } })
    const { a } = make()
    a.select('auto'); a.checked.value = true; vi.advanceTimersByTime(3100); a.confirm()
    expect(hasAcknowledged('v1')).toBe(true)
    expect(Object.keys(store)).toEqual([])
    a.select('review')
    a.select('auto') // 已确认过：不再弹窗
    expect(a.dialogOpen.value).toBe(false)
    expect(a.mode.value).toBe('auto')
    vi.unstubAllGlobals()
    // 新版本的提示需要重新确认；“新的页面加载”（清空内存）同样重新询问
    const b = make(status({ auto_ack_version: 'v2' }))
    b.a.select('auto')
    expect(b.a.dialogOpen.value).toBe(true)
    b.a.cancel(); forgetAcknowledged()
    const c = make()
    c.a.select('auto')
    expect(c.a.dialogOpen.value).toBe(true)
  })
  it('does nothing when automation is not allowed (policy / role / no LLM)', () => {
    for (const [s, role] of [[status({ auto_archive_allowed: false }), 'operator'], [status(), 'viewer'], [status({ configured: false, auto_archive_allowed: false }), 'admin']] as const) {
      const { a } = make(s, role)
      a.select('auto')
      expect(a.dialogOpen.value).toBe(false)
      expect(a.mode.value).toBe('review')
    }
  })
  it('falls back to review if the policy is switched off while auto is selected', () => {
    const { a, st } = make()
    a.select('auto'); a.checked.value = true; vi.advanceTimersByTime(3100); a.confirm()
    expect(a.mode.value).toBe('auto')
    st.value = status({ auto_archive_allowed: false })
    a.revalidate()
    expect(a.mode.value).toBe('review')
  })
  it('options() carries the status ack version only in auto mode', () => {
    const { a } = make()
    expect(a.options('hi')).toEqual({ instruction: 'hi' })
    a.select('auto'); a.checked.value = true; vi.advanceTimersByTime(3100); a.confirm()
    expect(a.options('')).toEqual({ automation: 'auto', auto_ack: { version: 'v1', accepted: true } })
  })
})
