import { afterEach, describe, expect, it, vi } from 'vitest'
import { json, setAuthHooks, setCsrfToken } from './http'
import { ApiError } from './types'

const res = (status: number, body?: unknown, headers: Record<string, string> = {}) =>
  new Response(body === undefined ? null : JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json', ...headers } })
const err = (code: string, message = 'm', details?: object) => ({ error: { code, message, request_id: 'r1', details } })

afterEach(() => { vi.unstubAllGlobals(); setAuthHooks({}); setCsrfToken('') })

describe('http client auth handling', () => {
  it('sends same-origin credentials and CSRF only on non-GET', async () => {
    const f = vi.fn().mockImplementation(async () => res(200, { data: 1 }))
    vi.stubGlobal('fetch', f)
    setCsrfToken('tok')
    await json('/v1/x')
    await json('/v1/x', { method: 'POST' }, { a: 1 })
    const [get, post] = f.mock.calls.map((c) => c[1] as RequestInit)
    expect(get.credentials).toBe('same-origin')
    expect(new Headers(get.headers).get('X-CSRF-Token')).toBeNull()
    expect(new Headers(post.headers).get('X-CSRF-Token')).toBe('tok')
  })
  it('refreshes the CSRF token once and retries on CSRF_INVALID', async () => {
    const f = vi.fn().mockResolvedValueOnce(res(403, err('CSRF_INVALID'))).mockResolvedValueOnce(res(200, { data: 'ok' }))
    vi.stubGlobal('fetch', f)
    setAuthHooks({ refreshCsrf: async () => { setCsrfToken('new') } })
    setCsrfToken('old')
    expect(await json('/v1/x', { method: 'POST' })).toBe('ok')
    expect(f).toHaveBeenCalledTimes(2)
    expect(new Headers((f.mock.calls[1][1] as RequestInit).headers).get('X-CSRF-Token')).toBe('new')
  })
  it('does not loop when the retry also fails', async () => {
    const f = vi.fn().mockImplementation(async () => res(403, err('CSRF_INVALID')))
    vi.stubGlobal('fetch', f)
    setAuthHooks({ refreshCsrf: async () => undefined })
    await expect(json('/v1/x', { method: 'POST' })).rejects.toMatchObject({ code: 'CSRF_INVALID' })
    expect(f).toHaveBeenCalledTimes(2)
  })
  it('fires onUnauthenticated for UNAUTHENTICATED unless silent', async () => {
    vi.stubGlobal('fetch', vi.fn().mockImplementation(async () => res(401, err('UNAUTHENTICATED'))))
    const h = vi.fn(); setAuthHooks({ onUnauthenticated: h })
    await expect(json('/v1/tasks')).rejects.toBeInstanceOf(ApiError)
    expect(h).toHaveBeenCalledTimes(1)
    await expect(json('/v1/auth/me', {}, undefined, { silent: true })).rejects.toBeInstanceOf(ApiError)
    expect(h).toHaveBeenCalledTimes(1)
  })
  it('does not treat INVALID_CREDENTIALS as an expired session', async () => {
    vi.stubGlobal('fetch', vi.fn().mockImplementation(async () => res(401, err('INVALID_CREDENTIALS'))))
    const h = vi.fn(); setAuthHooks({ onUnauthenticated: h })
    await expect(json('/v1/auth/login', { method: 'POST' }, {})).rejects.toMatchObject({ code: 'INVALID_CREDENTIALS' })
    expect(h).not.toHaveBeenCalled()
  })
  it('routes SETUP_REQUIRED to the wizard', async () => {
    vi.stubGlobal('fetch', vi.fn().mockImplementation(async () => res(503, err('SETUP_REQUIRED'))))
    const h = vi.fn(); setAuthHooks({ onSetupRequired: h })
    await expect(json('/v1/tasks')).rejects.toBeInstanceOf(ApiError)
    expect(h).toHaveBeenCalled()
  })
  it('turns 429 into a friendly message with Retry-After', async () => {
    vi.stubGlobal('fetch', vi.fn().mockImplementation(async () => res(429, err('RATE_LIMITED', 'raw'), { 'Retry-After': '30' })))
    await expect(json('/v1/auth/login', { method: 'POST' }, {})).rejects.toMatchObject({ retryAfter: 30, message: expect.stringContaining('30 秒') })
  })
})

describe('wizard gate and stale backend handling', () => {
  it('fires onWizardRequired for 409 WIZARD_REQUIRED', async () => {
    vi.stubGlobal('fetch', vi.fn().mockImplementation(async () => res(409, err('WIZARD_REQUIRED', '请先完成初始化向导'))))
    const onWizardRequired = vi.fn()
    setAuthHooks({ onWizardRequired })
    await expect(json('/v1/tasks')).rejects.toMatchObject({ status: 409, code: 'WIZARD_REQUIRED' })
    expect(onWizardRequired).toHaveBeenCalledTimes(1)
  })
  it('does not fire it for other 409s', async () => {
    vi.stubGlobal('fetch', vi.fn().mockImplementation(async () => res(409, err('CONFLICT'))))
    const onWizardRequired = vi.fn()
    setAuthHooks({ onWizardRequired })
    await expect(json('/v1/tasks')).rejects.toBeInstanceOf(ApiError)
    expect(onWizardRequired).not.toHaveBeenCalled()
  })
  it('explains a 404 on a known contract path as an outdated backend, but not a missing resource', async () => {
    vi.stubGlobal('fetch', vi.fn().mockImplementation(async () => res(404, err('NOT_FOUND', '接口不存在或尚未实现'))))
    await expect(json('/v1/llm/status')).rejects.toMatchObject({ message: expect.stringContaining('后端可能版本过旧') })
    await expect(json('/v1/tasks/abc')).rejects.toMatchObject({ message: '接口不存在或尚未实现' })
  })
})
