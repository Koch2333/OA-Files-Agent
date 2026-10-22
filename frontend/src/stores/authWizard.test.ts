import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

const status = (finished: boolean) => ({ initialized: true, wizard_finished: finished, server_version: 't', api_level: 3, needs_token: false, loopback: true, https: false, passkey_available: false, min_password_length: 12, checks: [] })
const api = vi.hoisted(() => ({
  getSetupStatus: vi.fn(), finishSetup: vi.fn(), getMe: vi.fn(),
}))
vi.mock('../api', async (orig) => ({ ...(await orig<typeof import('../api')>()), api }))

import { useAuth } from './auth'
import { ApiError, type Me, type SetupStatus } from '../api'

const me = { user: { id: 'u', username: 'admin', display_name: 'a', role: 'admin', must_change_password: false }, csrf_token: 'c', scope: 'full', mfa: 'totp', fresh: true, session_expires_at: new Date(Date.now() + 1e6).toISOString(), policy: { mode: 'enterprise', mfa_policy: 'off', allow_password_login: true, passkey_available: false } }

describe('auth store wizard gate', () => {
  beforeEach(() => { setActivePinia(createPinia()); vi.clearAllMocks() })
  it('finishWizard calls POST /setup/finish then refreshes status and me', async () => {
    const a = useAuth()
    a.setup = status(false) as unknown as SetupStatus
    api.finishSetup.mockResolvedValue(status(true))
    api.getMe.mockResolvedValue(me)
    await a.finishWizard()
    expect(api.finishSetup).toHaveBeenCalledTimes(1)
    expect(api.getMe).toHaveBeenCalledTimes(1)
    expect(a.setup?.wizard_finished).toBe(true)
    expect(a.me?.user.username).toBe('admin')
    expect(a.guard({ path: '/', fullPath: '/', meta: {} })).toBe(true)
  })
  it('keeps the gate closed when finish fails', async () => {
    const a = useAuth()
    a.setup = status(false) as unknown as SetupStatus
    api.finishSetup.mockRejectedValue(new ApiError(409, 'MFA_REQUIRED_BY_POLICY', 'x'))
    await expect(a.finishWizard()).rejects.toMatchObject({ code: 'MFA_REQUIRED_BY_POLICY' })
    expect(a.setup?.wizard_finished).toBe(false)
    a.me = me as unknown as Me
    expect(a.guard({ path: '/tasks', fullPath: '/tasks', meta: {} })).toEqual({ path: '/setup' })
  })
  it('WIZARD_REQUIRED marks the wizard unfinished so the guard redirects to /setup', () => {
    const a = useAuth()
    a.setup = status(true) as unknown as SetupStatus
    a.me = me as unknown as Me
    expect(a.guard({ path: '/tasks', fullPath: '/tasks', meta: {} })).toBe(true)
    a.markWizardUnfinished()
    expect(a.guard({ path: '/tasks', fullPath: '/tasks', meta: {} })).toEqual({ path: '/setup' })
  })
})
