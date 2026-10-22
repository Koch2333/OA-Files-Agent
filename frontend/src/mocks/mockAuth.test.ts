import { describe, expect, it } from 'vitest'
import { createMockAuth } from './mockAuth'
import { ApiError } from '../api/types'

const mk = () => createMockAuth(() => undefined)
const code = async (p: Promise<unknown>) => { try { await p; return 'ok' } catch (e) { return e instanceof ApiError ? e.code : 'other' } }

describe('mock auth', () => {
  it('starts logged in as the demo admin without fresh authentication', async () => {
    const a = mk()
    const me = await a.getMe()
    expect(me.user.role).toBe('admin')
    expect(me.fresh).toBe(false)
    expect(me.policy.mfa_policy).toBe('admins')
  })
  it('requires MFA for users that have it and rejects bad codes', async () => {
    const a = mk()
    await a.logout()
    expect(await code(a.getMe({ silent: true }))).toBe('UNAUTHENTICATED')
    const r = await a.login('admin', 'demo-password')
    expect(r.status).toBe('mfa_required')
    expect(await code(a.mfaTotp(r.mfa_token!, '000000'))).toBe('INVALID_CREDENTIALS')
    expect((await a.mfaTotp(r.mfa_token!, '123456')).status).toBe('ok')
  })
  it('locks accounts after repeated failures and unknown users look the same as bad passwords', async () => {
    const a = mk()
    await a.logout()
    expect(await code(a.login('nobody', 'x'))).toBe('INVALID_CREDENTIALS')
    for (let i = 0; i < 5; i++) await code(a.login('viewer', 'wrong'))
    expect(await code(a.login('viewer', 'demo-password'))).toBe('ACCOUNT_LOCKED')
  })
  it('sensitive actions need reauth, then succeed', async () => {
    const a = mk()
    try { await a.totpDisable(); throw new Error('should fail') } catch (e) { expect((e as ApiError).details?.reason).toBe('reauth_required') }
    await a.reauth('demo-password')
    await a.totpDisable().catch((e: ApiError) => expect(e.code).not.toBe('FORBIDDEN'))
  })
  it('protects the last admin and non-admins cannot use admin APIs', async () => {
    const a = mk()
    const users = (await a.adminListUsers({ page_size: 50 })).items
    const admin = users.find((u) => u.username === 'admin')!
    expect(await code(a.adminUpdateUser(admin.id, { role: 'viewer' }))).toBe('LAST_ADMIN')
    expect(await code(a.adminDeleteUser(admin.id))).toBe('CONFLICT')
    await a.logout()
    const r = await a.login('viewer', 'demo-password')
    expect(r.status).toBe('ok')
    expect(await code(a.adminListUsers())).toBe('FORBIDDEN')
  })
  it('shows temporary passwords once and invite paths', async () => {
    const a = mk()
    const c = await a.adminCreateUser({ username: 'new.user', display_name: '新', role: 'operator', credential: 'password' })
    expect(c.temporary_password).toBeTruthy()
    const i = await a.adminCreateUser({ username: 'pk.user', display_name: 'P', role: 'viewer', credential: 'passkey_invite' })
    expect(i.invite?.path).toMatch(/^\/invite\//)
    expect(await code(a.adminCreateUser({ username: 'NEW.user', display_name: 'x', role: 'viewer', credential: 'password' }))).toBe('CONFLICT')
  })
  it('requires confirmation to change rp id', async () => {
    const a = mk()
    expect(await code(a.adminUpdateSecuritySettings({ rp_id: 'example.com' }))).toBe('PASSKEY_RESET_REQUIRED')
    expect((await a.adminUpdateSecuritySettings({ rp_id: 'example.com', confirm_passkey_reset: true })).rp_id).toBe('example.com')
  })
})
