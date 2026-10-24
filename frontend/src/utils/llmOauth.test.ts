import { describe, expect, it } from 'vitest'
import { ApiError, type LlmOauthStatus, type LlmPreset } from '../api/types'
import { callbackFlagText, canOauth, clearPending, isTerminal, keySourceLabel, loadPending, pollStatus, safeKeyUrl, savePending, statusText } from './llmOauth'

const st = (status: LlmOauthStatus['status'], extra: Partial<LlmOauthStatus> = {}): LlmOauthStatus => ({ status, provider: 'openrouter', scope: 'personal', ...extra })
const mem = () => { const m = new Map<string, string>(); return { getItem: (k: string) => m.get(k) ?? null, setItem: (k: string, v: string) => void m.set(k, v), removeItem: (k: string) => void m.delete(k) } }

describe('llmOauth', () => {
  it('terminal states', () => {
    expect(['connected', 'failed', 'denied', 'expired'].every((s) => isTerminal(s as never))).toBe(true)
    expect(isTerminal('pending')).toBe(false)
    expect(isTerminal('connecting')).toBe(false)
  })
  it('callback flags', () => {
    expect(callbackFlagText('ok').kind).toBe('ok')
    expect(callbackFlagText(null).kind).toBe('none')
    for (const f of ['denied', 'expired', 'used', 'invalid', 'weird']) expect(callbackFlagText(f).kind).toBe('error')
  })
  it('status text distinguishes a failed connection test', () => {
    expect(statusText(st('connected', { test: { ok: true, latency_ms: 1, message: '', tool_calling: true, vision_ok: null } }), 'OpenRouter').kind).toBe('success')
    expect(statusText(st('connected', { test: { ok: false, latency_ms: 1, message: 'x', tool_calling: null, vision_ok: null } }), 'OpenRouter').kind).toBe('warn')
    expect(statusText(st('failed', { message: '授权码无效' }), 'OpenRouter')).toEqual({ kind: 'error', text: '授权码无效' })
  })
  it('only https key pages become links; only implemented flows get the login button', () => {
    expect(safeKeyUrl({ key_url: 'https://openrouter.ai/settings/keys' } as LlmPreset)).toBe('https://openrouter.ai/settings/keys')
    expect(safeKeyUrl({ key_url: 'javascript:alert(1)' } as LlmPreset)).toBeNull()
    expect(safeKeyUrl({ key_url: 'http://x.example' } as LlmPreset)).toBeNull()
    expect(safeKeyUrl({} as LlmPreset)).toBeNull()
    expect(canOauth({ oauth_login: true } as LlmPreset)).toBe(true)
    expect(canOauth({ oauth_login: false } as LlmPreset)).toBe(false)
    expect(canOauth(undefined)).toBe(false)
  })
  it('key source label', () => {
    expect(keySourceLabel({ api_key_set: false }, 'X')).toBe('')
    expect(keySourceLabel({ api_key_set: true, key_source: 'oauth' }, 'OpenRouter')).toContain('登录授权')
    expect(keySourceLabel({ api_key_set: true, key_source: 'manual' }, 'OpenRouter')).toBe('手动填写的密钥')
  })
  it('polls until connected', async () => {
    const seq = [st('pending'), st('connecting'), st('connected')]
    let i = 0
    const seen: string[] = []
    const r = await pollStatus({ get: async () => seq[i++], state: 's', intervalMs: 0, sleep: async () => {}, isCancelled: () => false, onUpdate: (s) => seen.push(s.status) })
    expect(r?.status).toBe('connected')
    expect(seen).toEqual(['pending', 'connecting', 'connected'])
  })
  it('stops on cancel, throws on 4xx, tolerates transient errors, times out', async () => {
    let c = false
    expect(await pollStatus({ get: async () => { c = true; return st('pending') }, state: 's', sleep: async () => {}, isCancelled: () => c })).toBeNull()
    await expect(pollStatus({ get: async () => { throw new ApiError(422, 'OAUTH_UNKNOWN_STATE', '不存在', 'r') }, state: 's', sleep: async () => {}, isCancelled: () => false })).rejects.toThrow('不存在')
    let n = 0
    const r = await pollStatus({ get: async () => { if (n++ < 3) throw new Error('net'); return st('connected') }, state: 's', sleep: async () => {}, isCancelled: () => false })
    expect(r?.status).toBe('connected')
    await expect(pollStatus({ get: async () => { throw new Error('down') }, state: 's', sleep: async () => {}, isCancelled: () => false })).rejects.toThrow('down')
    let t = 0
    const e = await pollStatus({ get: async () => st('pending'), state: 's', sleep: async () => {}, isCancelled: () => false, maxMs: 1000, now: () => (t += 600) })
    expect(e?.status).toBe('expired')
  })
  it('pending storage round trip and garbage tolerance', () => {
    const m = mem()
    expect(loadPending(m)).toBeNull()
    savePending({ state: 'abc', scope: 'global', provider: 'openrouter' }, m)
    expect(loadPending(m)).toEqual({ state: 'abc', scope: 'global', provider: 'openrouter' })
    m.setItem('oa.llmOauth', '{"state":"x","scope":"root"}')
    expect(loadPending(m)).toBeNull()
    m.setItem('oa.llmOauth', 'not json')
    expect(loadPending(m)).toBeNull()
    clearPending(m)
    expect(loadPending(m)).toBeNull()
  })
})
