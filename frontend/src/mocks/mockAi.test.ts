import { describe, expect, it } from 'vitest'
import { ApiError, type LlmConfig, type LlmPreset, type Role } from '../api/types'
import { createMockAi, MOCK_ACK_VERSION } from './mockAi'

function make(role: Role = 'operator', initial: 'none' | 'global' | 'personal' = 'global') {
  let r: Role | null = role
  const cfg: LlmConfig = initial === 'none'
    ? { mode: 'heuristic', preset_id: '', base_url: '', model: '', api_key_set: false, api_key_hint: '', vision_model: '', allow_vision: false, state: 'ok', updated_at: null }
    : { mode: 'llm', preset_id: 'deepseek', base_url: 'https://api.deepseek.com', model: 'deepseek-flash', api_key_set: true, api_key_hint: '••••', vision_model: '', allow_vision: false, state: 'ok', updated_at: null }
  const presets = [{ id: 'deepseek', name: 'DeepSeek', needs_api_key: true }, { id: 'custom', name: '自定义', needs_api_key: false }] as LlmPreset[]
  const m = createMockAi({ role: () => r, globalCfg: () => cfg, presets, models: () => ['m1'], audit: () => undefined }, initial)
  return { ...m, setRole: (x: Role | null) => { r = x } }
}
const ack = { version: MOCK_ACK_VERSION, accepted: true }
const code = async (p: Promise<unknown> | (() => unknown)) => { try { await (typeof p === 'function' ? p() : p); return 'ok' } catch (e) { return e instanceof ApiError ? `${e.status} ${e.code}` : String(e) } }

describe('mock AI rules follow the contract', () => {
  it('status variants and setup hints', async () => {
    expect(await make('admin', 'none').api.getLlmStatus()).toMatchObject({ configured: false, setup_hint: 'admin_global', auto_archive_allowed: false })
    expect(await make('operator', 'none').api.getLlmStatus()).toMatchObject({ setup_hint: 'personal_or_admin', personal_allowed: true })
    expect(await make('viewer', 'none').api.getLlmStatus()).toMatchObject({ setup_hint: 'contact_admin', personal_allowed: false })
    expect(await make('operator', 'global').api.getLlmStatus()).toMatchObject({ configured: true, source: 'global', auto_archive_allowed: true })
    expect(await make('operator', 'personal').api.getLlmStatus()).toMatchObject({ source: 'personal', model: 'qwen3.7-plus' })
    expect((await make('viewer', 'global').api.getLlmStatus()).auto_archive_allowed).toBe(false)
  })
  it('import options: AUTO_ARCHIVE_DISABLED / AUTO_ACK_REQUIRED / LLM_REQUIRED', async () => {
    const { api, ctl } = make('admin')
    expect(ctl.resolveImport({ automation: 'auto', auto_ack: ack })).toMatchObject({ automation: 'auto', llm_source: 'global', llm_model: 'deepseek-flash' })
    expect(await code(() => ctl.resolveImport({ automation: 'auto' }))).toBe('422 AUTO_ACK_REQUIRED')
    expect(await code(() => ctl.resolveImport({ automation: 'auto', auto_ack: { version: 'old', accepted: true } }))).toBe('422 AUTO_ACK_REQUIRED')
    expect(await code(() => ctl.resolveImport({ automation: 'auto', auto_ack: { version: MOCK_ACK_VERSION, accepted: false } }))).toBe('422 AUTO_ACK_REQUIRED')
    await api.updateAiPolicy({ allow_auto_archive: false })
    expect(await code(() => ctl.resolveImport({ automation: 'auto', auto_ack: ack }))).toBe('403 AUTO_ARCHIVE_DISABLED')
    const none = make('admin', 'none')
    expect(await code(() => none.ctl.resolveImport({ instruction: '只处理采购' }))).toBe('422 LLM_REQUIRED')
    expect(await code(() => none.ctl.resolveImport({ automation: 'auto', auto_ack: ack }))).toBe('422 LLM_REQUIRED')
    expect(none.ctl.resolveImport(undefined)).toMatchObject({ automation: 'review', llm_source: 'none', llm_model: '' })
  })
  it('personal LLM: disabled by policy → 403 PERSONAL_LLM_DISABLED; stricter address rule → 422; rate limit → 429', async () => {
    const { api, setRole } = make('operator')
    expect(await code(api.savePersonalLlm({ mode: 'llm', preset_id: 'custom', base_url: 'http://127.0.0.1:11434/v1', model: 'x' }))).toBe('422 VALIDATION_FAILED')
    expect(await code(api.savePersonalLlm({ mode: 'llm', preset_id: 'deepseek', base_url: 'https://api.deepseek.com', model: 'm', api_key: 'sk-1' }))).toBe('ok')
    for (let i = 0; i < 6; i++) await api.testPersonalLlm({ base_url: 'https://api.deepseek.com', model: 'm' })
    expect(await code(api.testPersonalLlm({ base_url: 'https://api.deepseek.com', model: 'm' }))).toBe('429 RATE_LIMITED')
    setRole('admin')
    await api.updateAiPolicy({ allow_personal_llm: false })
    setRole('operator')
    expect(await code(api.getPersonalLlm())).toBe('403 PERSONAL_LLM_DISABLED')
    expect(await code(api.savePersonalLlm({ mode: 'llm', base_url: 'https://x.y', model: 'm' }))).toBe('403 PERSONAL_LLM_DISABLED')
  })
  it('private hosts are allowed for personal APIs only when the admin opens the policy', async () => {
    const { api, setRole } = make('admin')
    const input = { mode: 'llm' as const, preset_id: 'custom', base_url: 'http://127.0.0.1:11434/v1', model: 'x' }
    expect(await code(api.savePersonalLlm(input))).toBe('422 VALIDATION_FAILED')
    await api.updateAiPolicy({ allow_personal_private_hosts: true })
    expect(await code(api.savePersonalLlm(input))).toBe('ok')
    setRole('operator')
    expect(await code(api.updateAiPolicy({ allow_auto_archive: false }))).toBe('403 FORBIDDEN')
  })
  it('deleting the personal config falls back to global', async () => {
    const { api } = make('operator', 'personal')
    expect((await api.getLlmStatus()).source).toBe('personal')
    await api.deletePersonalLlm()
    expect((await api.getLlmStatus()).source).toBe('global')
  })
})
