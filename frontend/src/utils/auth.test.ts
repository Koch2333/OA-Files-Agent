import { describe, expect, it } from 'vitest'
import { guardDecision, isReauthRequired, roleAtLeast, safeRedirect, type GuardRoute, type GuardState } from './authz'
import { authErrorMessage, parseRetryAfter, rateLimitedMessage } from './authErrors'
import { passwordStrength } from './password'
import { isUserCancel, passkeyUnavailableReason, webauthnErrorMessage } from './webauthn'
import { formatManualKey, isCompleteTotp, normalizeRecoveryCode, normalizeTotpInput, shouldAutoSubmit } from './totp'
import { canClose, markCopied, openOneTime, recoveryCodesText, setAcknowledged, wipe } from './oneTime'
import { canAdvance, nextStep, originError, prevStep, restoreStep, rpIdError, stepsFor, usernameError, type SetupFormLike } from './setup'

const route = (path: string, meta: GuardRoute['meta'] = {}): GuardRoute => ({ path, fullPath: path, meta })
const sess = (o: Partial<NonNullable<GuardState['session']>> = {}) => ({ scope: 'full' as const, role: 'admin' as const, must_change_password: false, ...o })
const st = (o: Partial<GuardState> = {}): GuardState => ({ initialized: true, wizardFinished: true, session: null, ...o })

describe('roleAtLeast', () => {
  it('orders roles', () => {
    expect(roleAtLeast('admin', 'operator')).toBe(true)
    expect(roleAtLeast('operator', 'operator')).toBe(true)
    expect(roleAtLeast('viewer', 'operator')).toBe(false)
    expect(roleAtLeast('operator', 'admin')).toBe(false)
    expect(roleAtLeast(null, 'viewer')).toBe(false)
  })
})

describe('guardDecision', () => {
  it('redirects everything to /setup before initialisation', () => {
    expect(guardDecision(route('/'), st({ initialized: false }))).toEqual({ path: '/setup' })
    expect(guardDecision(route('/login', { public: true }), st({ initialized: false }))).toEqual({ path: '/setup' })
    expect(guardDecision(route('/setup', { public: true }), st({ initialized: false }))).toBe(true)
  })
  it('never shows /setup once the wizard is finished', () => {
    expect(guardDecision(route('/setup'), st())).toEqual({ path: '/login' })
    expect(guardDecision(route('/setup'), st({ session: sess() }))).toEqual({ path: '/' })
  })
  it('requires a session for private routes and keeps the return path', () => {
    expect(guardDecision({ path: '/tasks', fullPath: '/tasks?x=1', meta: {} }, st())).toEqual({ path: '/login', query: { redirect: '/tasks?x=1' } })
    expect(guardDecision(route('/tasks'), st({ session: sess({ role: 'viewer' }) }))).toBe(true)
  })
  it('lets public routes through and bounces logged-in users from /login', () => {
    expect(guardDecision(route('/invite/abc', { public: true }), st())).toBe(true)
    expect(guardDecision(route('/login', { public: true }), st({ session: sess() }))).toEqual({ path: '/' })
  })
  it('forces MFA enrolment and password change', () => {
    expect(guardDecision(route('/tasks'), st({ session: sess({ scope: 'enroll_mfa' }) }))).toEqual({ path: '/account/security', query: { enroll: '1' } })
    expect(guardDecision(route('/account/security', { allowRestricted: true }), st({ session: sess({ scope: 'enroll_mfa' }) }))).toBe(true)
    expect(guardDecision(route('/tasks'), st({ session: sess({ must_change_password: true }) }))).toEqual({ path: '/account/security', query: { change_password: '1' } })
  })
  it('enforces minRole', () => {
    const m = { minRole: 'admin' as const }
    expect(guardDecision(route('/admin/users', m), st({ session: sess({ role: 'operator' }) }))).toEqual({ path: '/forbidden' })
    expect(guardDecision(route('/admin/users', m), st({ session: sess() }))).toBe(true)
    expect(guardDecision(route('/settings', { minRole: 'admin' }), st({ session: sess({ role: 'viewer' }) }))).toEqual({ path: '/forbidden' })
  })
})

describe('safeRedirect', () => {
  it('allows only same-site paths', () => {
    expect(safeRedirect('/tasks?a=1')).toBe('/tasks?a=1')
    expect(safeRedirect('//evil.com')).toBe('/')
    expect(safeRedirect('https://evil.com')).toBe('/')
    expect(safeRedirect('/\\evil')).toBe('/')
    expect(safeRedirect('/login')).toBe('/')
    expect(safeRedirect(undefined)).toBe('/')
  })
})

describe('isReauthRequired', () => {
  it('detects the reauth signal', () => {
    expect(isReauthRequired({ status: 403, code: 'FORBIDDEN', details: { reason: 'reauth_required' } })).toBe(true)
    expect(isReauthRequired({ status: 403, code: 'FORBIDDEN' }, false)).toBe(true)
    expect(isReauthRequired({ status: 403, code: 'FORBIDDEN' }, true)).toBe(false)
    expect(isReauthRequired({ status: 403, code: 'CSRF_INVALID' }, false)).toBe(false)
    expect(isReauthRequired({ status: 401, code: 'UNAUTHENTICATED' })).toBe(false)
  })
})

describe('error mapping', () => {
  const e = (code: string, status: number, extra: object = {}) => ({ status, code, message: 'raw', ...extra })
  it('maps known codes', () => {
    expect(authErrorMessage(e('INVALID_CREDENTIALS', 401))).toContain('用户名或密码')
    expect(authErrorMessage(e('LAST_ADMIN', 409))).toContain('最后一个')
    expect(authErrorMessage(e('ACCOUNT_LOCKED', 423))).toContain('锁定')
    expect(authErrorMessage(e('SOMETHING', 500))).toBe('raw')
  })
  it('includes Retry-After', () => {
    expect(authErrorMessage(e('RATE_LIMITED', 429, { retryAfter: 30 }))).toContain('30 秒')
    expect(rateLimitedMessage(600)).toContain('10 分钟')
    expect(rateLimitedMessage()).toContain('稍后')
  })
  it('parses Retry-After headers', () => {
    expect(parseRetryAfter('42')).toBe(42)
    expect(parseRetryAfter(null)).toBeUndefined()
    expect(parseRetryAfter('Wed, 21 Oct 2026 07:28:30 GMT', Date.parse('Wed, 21 Oct 2026 07:28:00 GMT'))).toBe(30)
  })
})

describe('passwordStrength', () => {
  it('rejects short and weak passwords', () => {
    expect(passwordStrength('', 12).ok).toBe(false)
    expect(passwordStrength('Ab1!', 12).ok).toBe(false)
    expect(passwordStrength('password1234', 12).ok).toBe(false)
    expect(passwordStrength('aaaaaaaaaaaaaaaa', 12).ok).toBe(false)
    expect(passwordStrength('alice-secret-key', 12, 'alice').ok).toBe(false)
  })
  it('accepts a strong password', () => {
    const s = passwordStrength('Tr0ub4dor&3-horse', 12)
    expect(s.ok).toBe(true)
    expect(s.score).toBeGreaterThanOrEqual(3)
  })
  it('honours the configured minimum', () => {
    expect(passwordStrength('Abcdef1!xyz', 16).ok).toBe(false)
  })
})

describe('webauthn messages', () => {
  it('maps NotAllowedError / InvalidStateError per ceremony', () => {
    expect(webauthnErrorMessage({ name: 'NotAllowedError' }, 'register')).toContain('注册已取消')
    expect(webauthnErrorMessage({ name: 'NotAllowedError' }, 'login')).toContain('没有可用的 Passkey')
    expect(webauthnErrorMessage({ name: 'InvalidStateError' }, 'register')).toContain('已经注册')
    expect(webauthnErrorMessage({ name: 'SecurityError' })).toContain('RP ID')
  })
  it('understands simplewebauthn error codes and causes', () => {
    expect(webauthnErrorMessage({ code: 'ERROR_AUTHENTICATOR_PREVIOUSLY_REGISTERED', name: 'WebAuthnError' }, 'register')).toContain('已经注册')
    expect(webauthnErrorMessage({ name: 'WebAuthnError', cause: { name: 'NotAllowedError' } }, 'login')).toContain('取消')
    expect(webauthnErrorMessage(new Error('boom'))).toContain('boom')
  })
  it('detects user cancellation', () => {
    expect(isUserCancel({ name: 'NotAllowedError' })).toBe(true)
    expect(isUserCancel({ name: 'SecurityError' })).toBe(false)
  })
  it('explains unavailability', () => {
    expect(passkeyUnavailableReason({ secureContext: true, supported: true })).toBeNull()
    expect(passkeyUnavailableReason({ secureContext: false, supported: true })).toContain('HTTPS')
    expect(passkeyUnavailableReason({ secureContext: true, supported: false })).toContain('不支持')
    expect(passkeyUnavailableReason({ secureContext: true, supported: true, serverAvailable: false })).toContain('服务器')
  })
})

describe('totp input', () => {
  it('keeps digits only, max 6', () => {
    expect(normalizeTotpInput('12a 34-56789')).toBe('123456')
    expect(isCompleteTotp('123456')).toBe(true)
    expect(isCompleteTotp('12345')).toBe(false)
  })
  it('auto-submits once on the sixth digit', () => {
    expect(shouldAutoSubmit('12345', '123456', false)).toBe(true)
    expect(shouldAutoSubmit('123456', '123456', false)).toBe(false)
    expect(shouldAutoSubmit('12345', '123456', true)).toBe(false)
    expect(shouldAutoSubmit('', '123456', false)).toBe(true) // paste
  })
  it('normalises recovery codes and manual keys', () => {
    expect(normalizeRecoveryCode(' abcde-fghij ')).toBe('ABCDE-FGHIJ')
    expect(formatManualKey('JBSWY3DPEHPK3PXP')).toBe('JBSW Y3DP EHPK 3PXP')
  })
})

describe('one-time secrets', () => {
  it('recovery codes require explicit acknowledgement', () => {
    let s = openOneTime('recovery', 't', ['A', 'B'])
    expect(canClose(s)).toBe(false)
    s = markCopied(s)
    expect(canClose(s)).toBe(false)
    s = setAcknowledged(s, true)
    expect(canClose(s)).toBe(true)
  })
  it('temporary passwords can close after copy', () => {
    const s = markCopied(openOneTime('password', 't', ['pw']))
    expect(canClose(s)).toBe(true)
  })
  it('wipe removes plaintext', () => {
    const w = wipe(openOneTime('invite', 't', ['https://x/invite/secret']))
    expect(w.values).toEqual([''])
    expect(canClose(w)).toBe(false)
  })
  it('builds a download text', () => {
    expect(recoveryCodesText(['A-B', 'C-D'], 'alice')).toContain('A-B\nC-D')
  })
})

describe('setup logic', () => {
  const form = (o: Partial<SetupFormLike> = {}): SetupFormLike => ({ mode: 'enterprise', token: '', username: 'admin', displayName: '管理员', password: 'Tr0ub4dor&3-horse', password2: 'Tr0ub4dor&3-horse', rpId: 'oa.example.com', origins: ['https://oa.example.com'], minLen: 12, sessionIdle: 60, sessionMax: 12, ...o })
  const status = { needs_token: true, loopback: false, checks: [{ ok: true }] }
  it('local mode skips admin/policy/mfa', () => {
    expect(stepsFor('local')).toEqual(['welcome', 'token', 'mode', 'finish'])
    expect(nextStep('mode', 'local')).toBe('finish')
    expect(nextStep('mode', 'enterprise')).toBe('admin')
    expect(prevStep('welcome', 'enterprise')).toBe('welcome')
  })
  it('validates each step', () => {
    expect(canAdvance('token', form(), status)).toBe('请输入初始化令牌')
    expect(canAdvance('token', form({ token: 'X' }), status)).toBeNull()
    expect(canAdvance('token', form(), { ...status, needs_token: false })).toBeNull()
    expect(canAdvance('mode', form({ mode: 'local' }), status)).toContain('本机')
    expect(canAdvance('mode', form({ mode: 'local' }), { ...status, loopback: true })).toBeNull()
    expect(canAdvance('mode', form({ mode: '' }), status)).toContain('选择')
    expect(canAdvance('admin', form({ password2: 'x' }), status)).toContain('不一致')
    expect(canAdvance('admin', form({ password: 'short', password2: 'short' }), status)).toContain('至少')
    expect(canAdvance('admin', form({ username: 'a b' }), status)).toContain('3–64')
    expect(canAdvance('admin', form(), status)).toBeNull()
    expect(canAdvance('policy', form({ origins: [] }), status)).toContain('来源')
    expect(canAdvance('policy', form({ origins: ['https://oa.example.com/path'] }), status)).toContain('精确')
    expect(canAdvance('policy', form({ rpId: 'other.com' }), status)).toContain('RP ID')
    expect(canAdvance('policy', form(), status)).toBeNull()
  })
  it('validates origins and rp ids', () => {
    expect(originError('https://a.com:8443')).toBeNull()
    expect(originError('https://a.com/')).not.toBeNull()
    expect(originError('ftp://a.com')).not.toBeNull()
    expect(originError('*')).not.toBeNull()
    expect(rpIdError('example.com', ['https://sub.example.com'])).toBeNull()
    expect(rpIdError('https://x', [])).not.toBeNull()
    expect(usernameError('ab')).not.toBeNull()
    expect(usernameError('first.last@corp')).toBeNull()
  })
  it('restores the step without secrets', () => {
    expect(restoreStep('policy', { mode: 'enterprise', password: '', token: '' }, false)).toBe('admin')
    expect(restoreStep('policy', { mode: 'enterprise', password: 'x', token: '' }, true)).toBe('token')
    expect(restoreStep('policy', { mode: 'enterprise', password: 'x', token: 't' }, true)).toBe('policy')
    expect(restoreStep('mfa', { mode: 'enterprise', password: 'x', token: 't' }, true)).toBe('policy')
    expect(restoreStep(undefined, { mode: '', password: '', token: '' }, false)).toBe('welcome')
    expect(restoreStep('mode', { mode: '', password: '', token: '' }, false)).toBe('mode')
  })
})

import { parseOrigins, rpIdMatchesHost } from './setup'
describe('setup helpers', () => {
  it('matches rp id to host', () => {
    expect(rpIdMatchesHost('example.com', 'oa.example.com')).toBe(true)
    expect(rpIdMatchesHost('example.com', 'example.com')).toBe(true)
    expect(rpIdMatchesHost('example.com', 'badexample.com')).toBe(false)
    expect(rpIdMatchesHost('localhost', 'localhost')).toBe(true)
  })
  it('parses origins', () => {
    expect(parseOrigins('https://a.com/\nhttps://a.com, https://b.com:8443 ')).toEqual(['https://a.com', 'https://b.com:8443'])
  })
})

describe('wizard gate (initialized but wizard unfinished)', () => {
  const gated = (o: Partial<GuardState> = {}) => st({ wizardFinished: false, ...o })
  const kinds: [string, ReturnType<typeof route>][] = [
    ['home', route('/')], ['tasks', route('/tasks')], ['admin', route('/admin/users', { minRole: 'admin' })],
    ['restricted', route('/account/security', { allowRestricted: true })], ['detail', { path: '/tasks/abc', fullPath: '/tasks/abc', meta: {} }],
  ]
  for (const [name, r] of kinds) {
    it(`sends ${name} to /setup for a signed-in admin (any scope)`, () => {
      expect(guardDecision(r, gated({ session: sess() }))).toEqual({ path: '/setup' })
      expect(guardDecision(r, gated({ session: sess({ scope: 'enroll_mfa' }) }))).toEqual({ path: '/setup' })
    })
    it(`sends ${name} to /login?redirect=/setup when signed out`, () => {
      expect(guardDecision(r, gated())).toEqual({ path: '/login', query: { redirect: '/setup' } })
    })
  }
  it('keeps /setup reachable with a session and /login and invitations public', () => {
    expect(guardDecision(route('/setup'), gated({ session: sess() }))).toBe(true)
    expect(guardDecision(route('/setup'), gated())).toEqual({ path: '/login', query: { redirect: '/setup' } })
    expect(guardDecision(route('/login', { public: true }), gated())).toBe(true)
    expect(guardDecision(route('/invite/abc', { public: true }), gated())).toBe(true)
  })
  it('opens the application once the wizard is finished, and when the flag is unknown', () => {
    expect(guardDecision(route('/'), st({ wizardFinished: true, session: sess() }))).toBe(true)
    expect(guardDecision(route('/'), st({ wizardFinished: null, session: sess() }))).toBe(true)
    expect(guardDecision(route('/setup'), st({ wizardFinished: true, session: sess() }))).toEqual({ path: '/' })
  })
  it('does not gate before initialisation', () => {
    expect(guardDecision(route('/'), { initialized: false, wizardFinished: false, session: null })).toEqual({ path: '/setup' })
  })
})
