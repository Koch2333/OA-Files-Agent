import { describe, expect, it } from 'vitest'
import type { LlmConfig, LlmPreset } from '../api/types'
import { mockApi } from '../mocks/mockApi'
import { canFetchModels, modelGroups, applyPreset, buildInput, engineLabel, formFromConfig, isDirty, keyHint, modelOptions, validateBaseUrl, validateForm, visionOptions, type LlmForm } from './llm'

const deepseek: LlmPreset = { id: 'deepseek', name: 'DeepSeek', description: '', oauth_login: false, base_url: 'https://api.deepseek.com', models: [{ id: 'deepseek-flash', label: '通用' }], default_model: 'deepseek-flash', needs_api_key: true, vision_models: [], docs_url: '', note: '', checked_at: '2026-10-22' }
const qwen: LlmPreset = { id: 'qwen', name: '通义千问', description: '', oauth_login: false, base_url: 'https://dashscope.aliyuncs.com/compatible-mode/v1', models: [], default_model: 'qwen3.7-plus', needs_api_key: true, vision_models: [], docs_url: '', note: '', checked_at: '2026-10-22' }
const custom: LlmPreset = { id: 'custom', name: '自定义', description: '', oauth_login: false, base_url: '', models: [], default_model: '', needs_api_key: false, vision_models: [], docs_url: '', note: '', checked_at: '2026-10-22' }
const cfg = (o: Partial<LlmConfig> = {}): LlmConfig => ({ mode: 'llm', preset_id: 'deepseek', base_url: deepseek.base_url, model: 'deepseek-flash', api_key_set: true, api_key_hint: '••••abcd', vision_model: '', allow_vision: false, state: 'ok', updated_at: null, ...o })
const form = (o: Partial<LlmForm> = {}): LlmForm => ({ mode: 'llm', preset_id: 'deepseek', base_url: deepseek.base_url, model: 'deepseek-flash', api_key: '', clear_key: false, vision_model: '', allow_vision: false, ...o })

describe('validateBaseUrl', () => {
  it('requires a value', () => expect(validateBaseUrl('  ')).toMatch(/Base URL/))
  it('accepts https', () => expect(validateBaseUrl('https://api.deepseek.com')).toBeNull())
  it('rejects public http', () => expect(validateBaseUrl('http://api.example.com/v1')).not.toBeNull())
  it('allows http for local and private hosts', () => {
    for (const u of ['http://127.0.0.1:11434/v1', 'http://localhost:11434/v1', 'http://192.168.1.5/v1', 'http://10.0.0.2/v1', 'http://172.20.0.1/v1', 'http://[::1]:8000/v1']) expect(validateBaseUrl(u), u).toBeNull()
    expect(validateBaseUrl('http://172.32.0.1/v1')).not.toBeNull()
  })
  it('rejects garbage and other schemes', () => { expect(validateBaseUrl('not a url')).not.toBeNull(); expect(validateBaseUrl('ftp://x.com')).not.toBeNull() })
})

describe('keyHint', () => {
  it('masks all but the last four', () => expect(keyHint('sk-1234567890abcd')).toBe('••••abcd'))
  it('is empty for no key', () => expect(keyHint('')).toBe(''))
})

describe('applyPreset', () => {
  it('fills base url and model when switching preset', () => {
    const f = applyPreset(form({ base_url: 'https://x.test/v1', model: 'mine' }), qwen)
    expect(f).toMatchObject({ preset_id: 'qwen', base_url: qwen.base_url, model: 'qwen3.7-plus' })
  })
  it('does not overwrite edits when re-picking the same preset', () => {
    const f = applyPreset(form({ base_url: 'https://proxy.test/v1', model: 'custom-id' }), deepseek)
    expect(f).toMatchObject({ base_url: 'https://proxy.test/v1', model: 'custom-id' })
  })
  it('fills empty values on the same preset', () => expect(applyPreset(form({ base_url: '', model: '' }), deepseek)).toMatchObject({ base_url: deepseek.base_url, model: 'deepseek-flash' }))
  it('keeps typed values for the custom preset', () => expect(applyPreset(form(), custom)).toMatchObject({ preset_id: 'custom', base_url: deepseek.base_url }))
})

describe('formFromConfig / isDirty', () => {
  it('preselects the first preset when nothing is saved', () => expect(formFromConfig(undefined, [deepseek, qwen])).toMatchObject({ mode: 'heuristic', preset_id: 'deepseek', model: 'deepseek-flash' }))
  it('unchanged form is not dirty', () => expect(isDirty(formFromConfig(cfg(), [deepseek]), cfg())).toBe(false))
  it('detects edits, new key and clear', () => {
    expect(isDirty(form({ model: 'other' }), cfg())).toBe(true)
    expect(isDirty(form({ api_key: 'sk-new' }), cfg())).toBe(true)
    expect(isDirty(form({ clear_key: true }), cfg())).toBe(true)
  })
  it('offline mode only compares mode', () => {
    expect(isDirty(form({ mode: 'heuristic', model: 'zzz' }), cfg({ mode: 'heuristic' }))).toBe(false)
    expect(isDirty(form({ mode: 'heuristic' }), cfg())).toBe(true)
  })
})

describe('validateForm / buildInput', () => {
  it('needs nothing in offline mode', () => expect(validateForm(form({ mode: 'heuristic', base_url: '' }), deepseek, undefined)).toEqual({}))
  it('requires key unless a usable saved one exists', () => {
    expect(validateForm(form(), deepseek, undefined).api_key).toBeTruthy()
    expect(validateForm(form(), deepseek, cfg())).toEqual({})
    expect(validateForm(form({ preset_id: 'qwen' }), qwen, cfg()).api_key).toBeTruthy()
    expect(validateForm(form({ clear_key: true }), deepseek, cfg()).api_key).toBeTruthy()
    expect(validateForm(form({ api_key: 'sk-x' }), deepseek, undefined)).toEqual({})
  })
  it('flags url and model', () => expect(validateForm(form({ base_url: '', model: ' ' }), custom, undefined)).toMatchObject({ base_url: expect.any(String), model: expect.any(String) }))
  it('builds input without sending an unchanged key', () => {
    expect(buildInput(form({ model: ' m ' }))).toEqual({ mode: 'llm', preset_id: 'deepseek', base_url: deepseek.base_url, model: 'm', allow_vision: false, vision_model: '' })
    expect(buildInput(form({ api_key: ' k ' })).api_key).toBe('k')
    expect(buildInput(form({ clear_key: true })).clear_api_key).toBe(true)
    expect(buildInput(form({ mode: 'heuristic' }))).toEqual({ mode: 'heuristic' })
  })
})

describe('modelOptions / engineLabel', () => {
  it('lists preset models first and dedupes fetched ones', () => expect(modelOptions(deepseek, ['x', 'deepseek-flash']).map((o) => o.id)).toEqual(['deepseek-flash', 'x']))
  it('labels the engine', () => {
    expect(engineLabel(cfg({ mode: 'heuristic' }), [deepseek])).toBe('离线规则')
    expect(engineLabel(cfg(), [deepseek])).toBe('DeepSeek · deepseek-flash')
  })
})

describe('mock llm api', () => {
  it('lists presets with DeepSeek first and Qwen second', async () => {
    const p = await mockApi.listLlmPresets()
    expect(p.slice(0, 2).map((x) => x.id)).toEqual(['deepseek', 'qwen'])
    expect(p[p.length - 1].id).toBe('custom')
  })
  it('validates like the contract', async () => {
    await expect(mockApi.saveLlmConfig({ mode: 'llm', preset_id: 'custom', base_url: 'http://example.com/v1', model: 'm' })).rejects.toMatchObject({ status: 422 })
    await expect(mockApi.saveLlmConfig({ mode: 'llm', preset_id: 'custom', base_url: 'https://example.com/v1', model: '' })).rejects.toMatchObject({ status: 422 })
    await expect(mockApi.saveLlmConfig({ mode: 'llm', preset_id: 'deepseek', base_url: deepseek.base_url, model: 'deepseek-flash' })).rejects.toMatchObject({ status: 422 })
  })
  it('never returns the key, only a hint; keeps and clears it', async () => {
    const c = await mockApi.saveLlmConfig({ mode: 'llm', preset_id: 'deepseek', base_url: deepseek.base_url, model: 'deepseek-flash', api_key: 'sk-secret-abcd' })
    expect(c).toMatchObject({ api_key_set: true, api_key_hint: '••••abcd', mode: 'llm' })
    expect(JSON.stringify(c)).not.toContain('secret')
    const kept = await mockApi.saveLlmConfig({ mode: 'llm', preset_id: 'deepseek', base_url: deepseek.base_url, model: 'deepseek-v4-pro' })
    expect(kept.api_key_hint).toBe('••••abcd')
    const cleared = await mockApi.saveLlmConfig({ mode: 'llm', preset_id: 'ollama', base_url: 'http://127.0.0.1:11434/v1', model: 'qwen3.8:27b', clear_api_key: true })
    expect(cleared).toMatchObject({ api_key_set: false, api_key_hint: '' })
    expect(await mockApi.saveLlmConfig({ mode: 'heuristic' })).toMatchObject({ mode: 'heuristic' })
  })
  it('test: ok by default, bad key fails', async () => {
    expect(await mockApi.testLlm({ base_url: deepseek.base_url, model: 'deepseek-flash', api_key: 'good' })).toMatchObject({ ok: true })
    expect(await mockApi.testLlm({ base_url: deepseek.base_url, model: 'deepseek-flash', api_key: 'bad' })).toMatchObject({ ok: false, message: '密钥无效或没有权限（HTTP 401）' })
  })
  it('models returns the preset models', async () => expect(await mockApi.listLlmModels({ base_url: deepseek.base_url })).toEqual(['deepseek-flash', 'deepseek-v4-pro']))
})

describe('vision settings', () => {
  it('requires a vision model when enabled', () => {
    expect(validateForm(form({ allow_vision: true }), deepseek, cfg()).vision_model).toMatch(/视觉模型/)
    expect(validateForm(form({ allow_vision: true, vision_model: 'qwen3.7-plus' }), deepseek, cfg()).vision_model).toBeUndefined()
    expect(validateForm(form({ allow_vision: false }), deepseek, cfg()).vision_model).toBeUndefined()
  })
  it('round-trips through form and input', () => {
    const f = formFromConfig(cfg({ vision_model: 'gpt-4o', allow_vision: true }), [deepseek])
    expect(f).toMatchObject({ vision_model: 'gpt-4o', allow_vision: true })
    expect(buildInput(f)).toMatchObject({ vision_model: 'gpt-4o', allow_vision: true })
  })
  it('detects vision edits as dirty', () => {
    expect(isDirty(form({ allow_vision: true, vision_model: 'x' }), cfg())).toBe(true)
    expect(isDirty(form({ vision_model: 'x' }), cfg())).toBe(true)
  })
  it('suggests preset vision models', () => {
    const p: LlmPreset = { ...deepseek, vision_models: [{ id: 'a', label: 'A' }, { id: 'a', label: 'dup' }, { id: 'b', label: 'b' }] }
    expect(visionOptions(p)).toEqual([{ id: 'a', label: 'a · A' }, { id: 'b', label: 'b' }])
    expect(visionOptions(undefined)).toEqual([])
  })
})

describe('mock vision', () => {
  it('presets expose vision models; openai has none', async () => {
    const ps = await mockApi.listLlmPresets()
    expect(ps.find((p) => p.id === 'qwen')?.vision_models.map((m) => m.id)).toContain('qwen3.7-plus')
    expect(ps.find((p) => p.id === 'deepseek')?.vision_models.map((m) => m.id)).toEqual(['deepseek-flash'])
    expect(ps.find((p) => p.id === 'openai')?.vision_models).toEqual([])
  })
  it('round-trips config and tests vision', async () => {
    const saved = await mockApi.saveLlmConfig({ mode: 'llm', preset_id: 'ollama', base_url: 'http://127.0.0.1:11434/v1', model: 'qwen3.8:27b', allow_vision: true, vision_model: 'qwen3.8:27b' })
    expect(saved).toMatchObject({ allow_vision: true, vision_model: 'qwen3.8:27b' })
    expect((await mockApi.getLlmConfig()).vision_model).toBe('qwen3.8:27b')
    const t = { base_url: 'http://127.0.0.1:11434/v1', model: 'm' }
    expect((await mockApi.testLlm({ ...t, vision_model: 'qwen3.8:27b' })).vision_ok).toBe(true)
    expect((await mockApi.testLlm({ ...t, vision_model: 'bad-vl' })).vision_ok).toBe(false)
    expect((await mockApi.testLlm(t)).vision_ok).toBeNull()
  })
})

describe('modelGroups', () => {
  const sug = [{ id: 'a', label: 'a · 推荐' }, { id: 'b', label: 'b' }]
  it('puts live models first and does not repeat them under suggestions', () => {
    const g = modelGroups(['x', 'a'], sug)
    expect(g.map((x) => x.id)).toEqual(['live', 'preset'])
    expect(g[0].items).toEqual([{ id: 'x', label: 'x' }, { id: 'a', label: 'a · 推荐' }])
    expect(g[1].items.map((o) => o.id)).toEqual(['b'])
  })
  it('works with no suggestions and no live list', () => {
    expect(modelGroups([], [])).toEqual([])
    expect(modelGroups(['x'], []).map((x) => x.id)).toEqual(['live'])
  })
  it('filters as you type and drops empty groups', () => {
    expect(modelGroups(['gpt-x', 'o3'], sug, 'GPT').flatMap((g) => g.items.map((o) => o.id))).toEqual(['gpt-x'])
    expect(modelGroups(['gpt-x'], sug, '推荐').map((g) => g.id)).toEqual(['preset'])
  })
})

describe('canFetchModels', () => {
  it('needs llm mode, a valid url and a key', () => {
    expect(canFetchModels(form({ api_key: 'k' }), deepseek, cfg({ api_key_set: false }))).toBe(true)
    expect(canFetchModels(form(), deepseek, cfg())).toBe(true)
    expect(canFetchModels(form(), deepseek, cfg({ api_key_set: false }))).toBe(false)
    expect(canFetchModels(form({ api_key: 'k', base_url: 'http://example.com' }), deepseek, undefined)).toBe(false)
    expect(canFetchModels(form({ api_key: 'k', mode: 'heuristic' }), deepseek, undefined)).toBe(false)
  })
  it('does not need a key for keyless providers', () => {
    expect(canFetchModels(form({ preset_id: 'custom', base_url: 'http://127.0.0.1:11434/v1' }), custom, undefined)).toBe(true)
  })
})

describe('mock presets', () => {
  it('match the catalog: no qwen-intl, openai without suggestions, verified date', async () => {
    const p = await mockApi.listLlmPresets()
    expect(p.map((x) => x.id)).toEqual(['deepseek', 'qwen', 'openai', 'ollama', 'openrouter', 'custom'])
    expect(p.every((x) => x.checked_at === '2026-10-22' && typeof x.note === 'string')).toBe(true)
    const openai = p.find((x) => x.id === 'openai')!
    expect(openai.models).toEqual([]); expect(openai.default_model).toBe('')
    expect(p.find((x) => x.id === 'qwen')).toMatchObject({ default_model: 'qwen3.7-plus', base_url: 'https://dashscope.aliyuncs.com/compatible-mode/v1' })
    expect(p.find((x) => x.id === 'deepseek')).toMatchObject({ base_url: 'https://api.deepseek.com', default_model: 'deepseek-flash' })
    expect(p.find((x) => x.id === 'ollama')?.default_model).toBe('qwen3.8:27b')
  })
})
