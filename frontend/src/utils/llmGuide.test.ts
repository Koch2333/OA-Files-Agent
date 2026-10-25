import { describe, expect, it } from 'vitest'
import type { LlmConfig, LlmPreset } from '../api/types'
import {
  GUIDES, badgeText, canAutoReadClipboard, canSave, checkKeyFormat, cleanKey, clipboardOffer, connectionSummary, detectKeyOwner, guideReducer, initialGuide, keyDisplay, maskKey,
  ollamaBase, ollamaHost, pickRecommendedModel, readClipboardSafe, simpleCards, type GuideState,
} from './llmGuide'

const DS = 'sk-' + 'a1b2c3d4'.repeat(4)
const OR = 'sk-or-v1-' + 'ab12cd34'.repeat(8)
const OAI = 'sk-proj-' + 'AbCd_-12'.repeat(8)

describe('key format per provider', () => {
  it('accepts each provider\'s own format', () => {
    expect(checkKeyFormat('deepseek', DS).level).toBe('ok')
    expect(checkKeyFormat('qwen', DS).level).toBe('ok')
    expect(checkKeyFormat('openrouter', OR).level).toBe('ok')
    expect(checkKeyFormat('openai', OAI).level).toBe('ok')
    expect(checkKeyFormat('openai', 'sk-' + 'x'.repeat(48)).level).toBe('ok')
  })
  it('warns (does not block) on a wrong provider or unusual format', () => {
    expect(checkKeyFormat('deepseek', OR)).toMatchObject({ level: 'warn', message: expect.stringContaining('OpenRouter') })
    expect(checkKeyFormat('deepseek', OAI)).toMatchObject({ level: 'warn', message: expect.stringContaining('OpenAI') })
    expect(checkKeyFormat('openai', OR).level).toBe('warn')
    expect(checkKeyFormat('deepseek', 'abcdefghijklmnopqrstuvwxyz').level).toBe('warn')
  })
  it('blocks clearly wrong input with a friendly message', () => {
    expect(checkKeyFormat('deepseek', '').level).toBe('empty')
    expect(checkKeyFormat('deepseek', 'sk-abc def ghi jkl mno')).toMatchObject({ level: 'bad', message: expect.stringContaining('空格') })
    expect(checkKeyFormat('deepseek', 'sk-请把密钥粘贴到这里这里这里')).toMatchObject({ level: 'bad', message: expect.stringContaining('中文') })
    expect(checkKeyFormat('deepseek', 'sk-123')).toMatchObject({ level: 'bad', message: expect.stringContaining('太短') })
  })
  it('cleans quotes, Bearer prefix and surrounding whitespace', () => {
    expect(cleanKey(`  "${DS}"\n`)).toBe(DS)
    expect(cleanKey(`Bearer ${DS}`)).toBe(DS)
    expect(checkKeyFormat('deepseek', ` ${DS}\n`).level).toBe('ok')
  })
  it('detects key owners and masks keys', () => {
    expect(detectKeyOwner(OR)).toBe('openrouter')
    expect(detectKeyOwner(OAI)).toBe('openai')
    expect(detectKeyOwner(DS)).toBe('sk')
    expect(detectKeyOwner('hello')).toBeNull()
    expect(maskKey(DS)).toBe('sk-••••' + DS.slice(-4))
    expect(maskKey(OR)).toBe('sk-or-v1-••••' + OR.slice(-4))
    expect(maskKey(DS)).not.toContain(DS.slice(3, 20))
  })
})

describe('clipboard chip logic', () => {
  it('offers only a strongly matching key for the selected provider', () => {
    expect(clipboardOffer(DS, 'deepseek', '')).toEqual({ key: DS, masked: 'sk-••••' + DS.slice(-4) })
    expect(clipboardOffer(OR, 'openrouter', '')?.key).toBe(OR)
    expect(clipboardOffer(`\n${DS} `, 'qwen', '')?.key).toBe(DS)
  })
  it('does not offer ordinary text, wrong-provider keys, long text or an unchanged value', () => {
    expect(clipboardOffer('今天开会的通知', 'deepseek', '')).toBeNull()
    expect(clipboardOffer('hello world', 'deepseek', '')).toBeNull()
    expect(clipboardOffer(OR, 'deepseek', '')).toBeNull()
    expect(clipboardOffer('sk-' + 'a'.repeat(500), 'deepseek', '')).toBeNull()
    expect(clipboardOffer(null, 'deepseek', '')).toBeNull()
    expect(clipboardOffer(DS, 'deepseek', DS)).toBeNull()
  })
  it('stays quiet about a key the user already dismissed', () => {
    expect(clipboardOffer(DS, 'deepseek', '', DS)).toBeNull()
    expect(clipboardOffer(DS, 'deepseek', '', 'other')).not.toBeNull()
  })
  it('reading the clipboard never throws and never auto-reads without permission', async () => {
    expect(await readClipboardSafe({ clipboard: { readText: async () => DS } })).toBe(DS)
    expect(await readClipboardSafe({ clipboard: { readText: async () => { throw new Error('NotAllowedError') } } })).toBeNull()
    expect(await readClipboardSafe({})).toBeNull()
    expect(await readClipboardSafe(undefined)).toBeNull()
    expect(await canAutoReadClipboard({ permissions: { query: async () => ({ state: 'granted' }) } })).toBe(true)
    expect(await canAutoReadClipboard({ permissions: { query: async () => ({ state: 'prompt' }) } })).toBe(false)
    expect(await canAutoReadClipboard({ permissions: { query: async () => { throw new Error('unsupported') } } })).toBe(false)
    expect(await canAutoReadClipboard({})).toBe(false)
  })
})

describe('step state machine', () => {
  const run = (s: GuideState, ...evs: Parameters<typeof guideReducer>[1][]) => evs.reduce(guideReducer, s)
  it('walks choose -> connect -> verified -> saved', () => {
    let s = initialGuide()
    expect(s).toMatchObject({ step: 'choose', check: 'idle' })
    s = run(s, { type: 'choose', presetId: 'deepseek' })
    expect(s).toMatchObject({ step: 'connect', presetId: 'deepseek' })
    expect(canSave(s)).toBe(false)
    s = run(s, { type: 'checkStart' })
    expect(s.check).toBe('working')
    s = run(s, { type: 'checkOk' })
    expect(s).toMatchObject({ step: 'verified', check: 'ok' })
    expect(canSave(s)).toBe(true)
    s = run(s, { type: 'saveStart' })
    expect(canSave(s)).toBe(false)
    s = run(s, { type: 'saveDone' })
    expect(s).toMatchObject({ saved: true, saving: false })
  })
  it('a failed check stays on connect and cannot be saved; editing the key invalidates a success', () => {
    let s = run(initialGuide('deepseek'), { type: 'checkStart' }, { type: 'checkFail' })
    expect(s).toMatchObject({ step: 'connect', check: 'failed' })
    expect(canSave(s)).toBe(false)
    s = run(s, { type: 'checkStart' }, { type: 'checkOk' }, { type: 'keyEdited' })
    expect(s).toMatchObject({ step: 'connect', check: 'idle' })
    expect(canSave(s)).toBe(false)
  })
  it('ignores impossible events', () => {
    expect(run(initialGuide(), { type: 'checkStart' }).check).toBe('idle')
    expect(run(initialGuide(), { type: 'saveStart' }).saving).toBe(false)
    expect(run(initialGuide('qwen'), { type: 'checkOk' }).check).toBe('idle') // not started
    const saving = run(initialGuide('qwen'), { type: 'checkStart' }, { type: 'checkOk' }, { type: 'saveStart' })
    expect(guideReducer(saving, { type: 'back' })).toBe(saving)
    expect(run(saving, { type: 'saveFail' }).saving).toBe(false)
  })
  it('choosing another provider resets everything; back returns to the cards', () => {
    const s = run(initialGuide('deepseek'), { type: 'checkStart' }, { type: 'checkOk' }, { type: 'choose', presetId: 'qwen' })
    expect(s).toMatchObject({ presetId: 'qwen', check: 'idle', step: 'connect' })
    expect(run(s, { type: 'back' })).toMatchObject({ step: 'choose', presetId: null })
  })
})

describe('provider cards and models', () => {
  const mk = (id: string, extra: Partial<LlmPreset> = {}) => ({ id, name: id === 'deepseek' ? 'DeepSeek（深度求索）' : id, default_model: '', models: [], needs_api_key: true, ...extra }) as LlmPreset
  it('lists only shipped presets, hides local models when not allowed, and badges login vs paste', () => {
    const presets = ['deepseek', 'qwen', 'openai', 'ollama', 'openrouter', 'custom'].map((i) => mk(i))
    expect(simpleCards(presets, { allowLocal: true }).map((c) => c.guide.id)).toEqual(['deepseek', 'qwen', 'openrouter', 'openai', 'ollama'])
    expect(simpleCards(presets, { allowLocal: false }).map((c) => c.guide.id)).not.toContain('ollama')
    expect(simpleCards([mk('deepseek')], { allowLocal: true })).toHaveLength(1)
    expect(GUIDES.filter((g) => g.recommended).map((g) => g.id)).toEqual(['deepseek'])
    expect(badgeText(GUIDES.find((g) => g.id === 'openrouter')!.kind)).toBe('一键登录')
    expect(badgeText(GUIDES.find((g) => g.id === 'deepseek')!.kind)).toBe('粘贴密钥')
    expect(GUIDES.filter((g) => g.kind === 'login').map((g) => g.id)).toEqual(['openrouter'])
  })
  it('picks the recommended model', () => {
    const ds = { default_model: 'deepseek-flash', models: [{ id: 'deepseek-flash', label: '' }] }
    expect(pickRecommendedModel(ds, [])).toBe('deepseek-flash')
    expect(pickRecommendedModel(ds, ['deepseek-v4-pro', 'deepseek-flash'])).toBe('deepseek-flash')
    expect(pickRecommendedModel(ds, ['other-a', 'other-b'])).toBe('other-a')
    expect(pickRecommendedModel({ default_model: '', models: [] }, ['o1', 'gpt-4o', 'gpt-4o-mini'])).toBe('gpt-4o-mini')
    expect(pickRecommendedModel({ default_model: '', models: [] }, [])).toBe('')
  })
  it('Ollama host handling', () => {
    expect(ollamaBase('http://127.0.0.1:11434')).toBe('http://127.0.0.1:11434/v1')
    expect(ollamaBase(' http://127.0.0.1:11434/ ')).toBe('http://127.0.0.1:11434/v1')
    expect(ollamaBase('http://host:1/v1')).toBe('http://host:1/v1')
    expect(ollamaHost('http://127.0.0.1:11434/v1')).toBe('http://127.0.0.1:11434')
    expect(ollamaHost('')).toBe('http://127.0.0.1:11434')
  })
})

describe('connection summary', () => {
  const cfg = (o: Partial<LlmConfig>): LlmConfig => ({ mode: 'llm', preset_id: 'deepseek', base_url: 'https://api.deepseek.com', model: 'deepseek-flash', api_key_set: true, api_key_hint: '••••abcd', vision_model: '', allow_vision: false, state: 'ok', updated_at: null, ...o })
  const presets = [{ id: 'deepseek', name: 'DeepSeek（深度求索）' }, { id: 'openrouter', name: 'OpenRouter' }] as LlmPreset[]
  it('connected shows provider, model and key name/suffix', () => {
    expect(connectionSummary(cfg({}), presets, 'global')).toMatchObject({ state: 'connected', title: '已连接：DeepSeek · deepseek-flash · 密钥 ••••abcd' })
    const or = connectionSummary(cfg({ preset_id: 'openrouter', model: 'openrouter/auto', key_source: 'oauth', key_label: 'OA Agent', api_key_hint: '••••c0de' }), presets, 'personal')
    expect(or.title).toBe('已连接：OpenRouter · openrouter/auto · 密钥 OA Agent（••••c0de）')
    expect(or.title).not.toMatch(/sk-/)
  })
  it('not set up highlights the next step', () => {
    expect(connectionSummary(cfg({ mode: 'llm', state: 'unconfigured', api_key_set: false }), presets, 'global')).toMatchObject({ state: 'none', title: '还没有设置', next: expect.stringContaining('第一步') })
    expect(connectionSummary(cfg({ mode: 'heuristic' }), presets, 'personal')).toMatchObject({ state: 'none', next: expect.any(String) })
    expect(connectionSummary(cfg({ mode: 'heuristic', updated_at: null }), presets, 'global').state).toBe('none') // never saved
    expect(connectionSummary(undefined, presets, 'global').state).toBe('none')
  })
  it('offline and problem states', () => {
    expect(connectionSummary(cfg({ mode: 'heuristic', updated_at: '2026-10-01T00:00:00Z' }), presets, 'global').state).toBe('offline')
    expect(connectionSummary(cfg({ state: 'unavailable' }), presets, 'global')).toMatchObject({ state: 'problem', next: expect.any(String) })
  })
  it('keyDisplay never needs the key', () => {
    expect(keyDisplay({ api_key_set: false, api_key_hint: '', key_label: undefined })).toBe('')
    expect(keyDisplay({ api_key_set: true, api_key_hint: '••••1234' })).toBe('••••1234')
    expect(keyDisplay({ api_key_set: true, api_key_hint: '••••1234', key_label: 'OA Agent' })).toBe('OA Agent（••••1234）')
  })
})
