/**
 * 演示用登录/账号实现（仅内存，刷新页面即重置）。
 * 演示账号：admin / operator / viewer，密码一律 demo-password；TOTP 验证码 123456；恢复码 DEMO1-DEMO2。
 * 默认已自动以 admin 登录（便于浏览管理页）；退出后可用上述账号登录，演示各角色的界面差异。
 * WebAuthn 在演示模式下不调用浏览器：begin 返回占位选项，finish 接受任意凭据。
 * 地址加 ?mock_setup=1 可演示初始化向导（完成后即视为已初始化）。
 */
import { uuid } from '../utils/uuid'
import {
  ApiError, type AdminUser, type Api, type LoginResult, type SetupStatus, type Me, type Page, type Passkey, type Policy, type Role,
  type SecuritySettings, type SessionInfo,
} from '../api/types'
import { fireUnauthenticated, setCsrfToken } from '../api/http'
import { REQUIRED_API_LEVEL } from '../utils/apiLevel'

const delay = (ms = 200) => new Promise((r) => setTimeout(r, ms))
const uid = () => uuid()
const iso = (offsetMs = 0) => new Date(Date.now() + offsetMs).toISOString()
const DEMO_PASSWORD = 'demo-password'
const DEMO_TOTP = '123456'
const FRESH_MS = 10 * 60_000

interface MUser {
  id: string; username: string; display_name: string; role: Role; status: 'active' | 'disabled'
  password: string | null; must_change_password: boolean; totp: boolean; passkeys: Passkey[]; recovery: number
  failed_logins: number; locked_until: string | null; last_login_at: string | null; created_at: string
}
interface MSession { id: string; userId: string; created_at: string; last_seen_at: string; expires_at: string; ip: string; user_agent: string; mfa: SessionInfo['mfa']; scope: 'full' | 'enroll_mfa' }

export type MockAuthApi = Pick<Api,
  | 'getSetupStatus' | 'verifySetupToken' | 'completeSetup' | 'finishSetup' | 'login' | 'logout' | 'getMe' | 'mfaTotp' | 'mfaRecovery' | 'mfaPasskeyBegin' | 'mfaPasskeyFinish'
  | 'passkeyLoginBegin' | 'passkeyLoginFinish' | 'reauth' | 'reauthPasskeyBegin' | 'reauthPasskeyFinish' | 'changePassword' | 'getMySecurity'
  | 'totpBegin' | 'totpConfirm' | 'totpDisable' | 'passkeyRegisterBegin' | 'passkeyRegisterFinish' | 'renamePasskey' | 'deletePasskey'
  | 'regenerateRecoveryCodes' | 'listMySessions' | 'revokeMySession' | 'getInvitation' | 'invitationPasskeyBegin' | 'invitationPasskeyFinish'
  | 'adminListUsers' | 'adminCreateUser' | 'adminGetUser' | 'adminUpdateUser' | 'adminDeleteUser' | 'adminResetPassword' | 'adminResetMfa'
  | 'adminUnlockUser' | 'adminRevokeSessions' | 'adminCreateInvite' | 'adminListSessions' | 'adminRevokeSession' | 'adminGetSecuritySettings'
  | 'adminUpdateSecuritySettings'>

const fakePasskey = (name: string): Passkey => ({ id: uid().replace(/-/g, ''), name, created_at: iso(), last_used_at: null, backup_eligible: true, backup_state: true, transports: ['internal'] })
const codes = () => Array.from({ length: 10 }, (_, i) => `DEMO${i}-${Math.random().toString(36).slice(2, 7).toUpperCase()}`)
const tempPassword = () => 'Tmp-' + Math.random().toString(36).slice(2, 8) + '-' + Math.random().toString(36).slice(2, 8)

/** 其他演示模块读取当前会话角色的出口（仅演示层内部使用，不属于 Api）。 */
export interface MockAuthCtl { currentRole?: () => Role | null }

export function createMockAuth(audit: (actor: string, action: string, target: string, summary: string) => void, ctl?: MockAuthCtl): MockAuthApi {
  let initialized = typeof location === 'undefined' || !new URLSearchParams(location.search).has('mock_setup')
  // 向导闸门演示：?mock_setup=1 从头走向导；?mock_wizard=1 已初始化但向导未完成（管理员已登录，演示“恢复”）；?mock_wizard=mfa 同上且管理员尚未绑定 MFA
  const wizardParam = typeof location === 'undefined' ? null : new URLSearchParams(location.search).get('mock_wizard')
  let wizardFinished = initialized && !wizardParam
  const users = new Map<string, MUser>()
  const mk = (p: Partial<MUser> & Pick<MUser, 'username' | 'display_name' | 'role'>): MUser => {
    const u: MUser = { id: uid(), status: 'active', password: DEMO_PASSWORD, must_change_password: false, totp: false, passkeys: [], recovery: 0, failed_logins: 0, locked_until: null, last_login_at: iso(-3_600_000), created_at: iso(-30 * 864e5), ...p }
    users.set(u.id, u)
    return u
  }
  const admin = mk({ username: 'admin', display_name: '演示管理员', role: 'admin', totp: true, passkeys: [fakePasskey('演示 Passkey')], recovery: 10 })
  mk({ username: 'operator', display_name: '演示操作员', role: 'operator' })
  mk({ username: 'viewer', display_name: '演示只读用户', role: 'viewer' })
  mk({ username: 'zhang.wei', display_name: '张伟', role: 'operator', totp: true, recovery: 10 })
  mk({ username: 'locked.user', display_name: '已锁定用户', role: 'viewer', failed_logins: 5, locked_until: iso(10 * 60_000) })
  mk({ username: 'ex.employee', display_name: '已停用用户', role: 'viewer', status: 'disabled' })

  const settings: SecuritySettings = {
    mode: 'enterprise', rp_id: 'localhost', rp_name: 'OA 文件智能归档', origins: ['http://localhost:5173'], mfa_policy: 'admins', allow_password_login: true,
    session_idle_minutes: 60, session_max_hours: 12, password_min_length: 12, lockout_threshold: 5, lockout_minutes: 15, passkey_available: true,
  }
  const sessions = new Map<string, MSession>()
  let current: MSession | null = null
  let freshUntil = 0
  const totpPending = new Map<string, string>()

  const mkSession = (u: MUser, mfa: MSession['mfa'], scope: MSession['scope'] = 'full'): MSession => {
    const s: MSession = { id: uid(), userId: u.id, created_at: iso(), last_seen_at: iso(), expires_at: iso(settings.session_max_hours * 3_600_000), ip: '127.0.0.1', user_agent: navigator.userAgent, mfa, scope }
    sessions.set(s.id, s)
    current = s
    freshUntil = Date.now() + FRESH_MS
    u.last_login_at = iso(); u.failed_logins = 0; u.locked_until = null
    return s
  }
  // 自动以 admin 登录；刻意不设“近期认证”，便于演示重新认证对话框
  mkSession(admin, 'totp'); freshUntil = 0
  sessions.set('demo-other', { id: 'demo-other', userId: admin.id, created_at: iso(-5 * 3_600_000), last_seen_at: iso(-20 * 60_000), expires_at: iso(6 * 3_600_000), ip: '192.168.1.23', user_agent: 'Mozilla/5.0 (Macintosh) Safari/17', mfa: 'passkey', scope: 'full' })
  if (wizardParam === 'mfa') { admin.totp = false; admin.passkeys = []; admin.recovery = 0; if (current) (current as MSession).scope = 'enroll_mfa' }

  const userOf = (s: MSession) => users.get(s.userId) as MUser
  if (ctl) ctl.currentRole = () => (current && sessions.has(current.id) && current.scope === 'full' ? userOf(current).role : null)
  const policy = (): Policy => ({ mode: settings.mode, mfa_policy: settings.mfa_policy, allow_password_login: settings.allow_password_login, passkey_available: settings.passkey_available, password_min_length: settings.password_min_length })
  const sessionUser = (u: MUser) => ({ id: u.id, username: u.username, display_name: u.display_name, role: u.role, must_change_password: u.must_change_password })
  const needsMfa = (u: MUser) => settings.mfa_policy === 'all' || (settings.mfa_policy === 'admins' && u.role === 'admin')
  const hasMfa = (u: MUser) => u.totp || u.passkeys.length > 0
  const isLocked = (u: MUser) => !!u.locked_until && Date.parse(u.locked_until) > Date.now()

  function me(): Me {
    const s = need()
    return { user: sessionUser(userOf(s)), csrf_token: 'mock-csrf', scope: s.scope, mfa: s.mfa, fresh: Date.now() < freshUntil, session_expires_at: s.expires_at, policy: policy() }
  }
  function need(): MSession {
    if (!current || !sessions.has(current.id)) { current = null; fireUnauthenticated(); throw new ApiError(401, 'UNAUTHENTICATED', '请先登录') }
    return current
  }
  function needFull(): MUser {
    const s = need()
    if (s.scope !== 'full') throw new ApiError(403, 'FORBIDDEN', '请先完成多因素认证绑定')
    return userOf(s)
  }
  function needAdmin(): MUser {
    const u = needFull()
    if (u.role !== 'admin') throw new ApiError(403, 'FORBIDDEN', '需要管理员权限')
    return u
  }
  function needFresh() {
    if (Date.now() >= freshUntil) throw new ApiError(403, 'FORBIDDEN', '该操作需要近期认证，请重新输入密码', undefined, { reason: 'reauth_required' })
  }
  const adminView = (u: MUser): AdminUser => ({
    id: u.id, username: u.username, display_name: u.display_name, role: u.role, status: isLocked(u) ? 'locked' : u.status, has_password: !!u.password, totp_enabled: u.totp,
    passkey_count: u.passkeys.length, recovery_codes_remaining: u.recovery, must_change_password: u.must_change_password, failed_logins: u.failed_logins,
    locked_until: isLocked(u) ? u.locked_until : null, last_login_at: u.last_login_at, created_at: u.created_at,
  })
  const activeAdmins = () => [...users.values()].filter((u) => u.role === 'admin' && u.status === 'active')
  function guardLastAdmin(u: MUser, becomesNonAdmin: boolean) {
    if (u.role === 'admin' && u.status === 'active' && becomesNonAdmin && activeAdmins().length <= 1) throw new ApiError(409, 'LAST_ADMIN', '不能停用、删除或降级最后一个启用的管理员')
  }
  const findUser = (id: string): MUser => { const u = users.get(id); if (!u) throw new ApiError(404, 'NOT_FOUND', '用户不存在'); return u }
  const paginate = <T,>(arr: T[], q?: { page?: number; page_size?: number }): Page<T> => {
    const page = q?.page ?? 1, size = q?.page_size ?? 20
    return { items: arr.slice((page - 1) * size, page * size), total: arr.length, page, page_size: size }
  }
  const sessionInfo = (s: MSession): SessionInfo => ({ id: s.id, user_id: s.userId, username: userOf(s).username, created_at: s.created_at, last_seen_at: s.last_seen_at, expires_at: s.expires_at, ip: s.ip, user_agent: s.user_agent, mfa: s.mfa, current: s.id === current?.id })
  const revokeFor = (userId: string, keepCurrent = false) => { let n = 0; for (const [id, s] of sessions) if (s.userId === userId && !(keepCurrent && s.id === current?.id)) { sessions.delete(id); n++ }; if (current && !sessions.has(current.id)) current = null; return n }
  const newInvite = () => { const token = 'demo-' + uid().slice(0, 18); return { token, path: '/invite/' + token, expires_at: iso(24 * 3_600_000) } }
  const wa = async () => { await delay(100); return { challenge_id: uid(), options: { mock: true } } }

  function loginOutcome(u: MUser, mfa: MSession['mfa']): LoginResult {
    if (needsMfa(u) && !hasMfa(u) && mfa === 'none') {
      const s = mkSession(u, 'none', 'enroll_mfa')
      setCsrfToken('mock-csrf')
      return { status: 'enroll_mfa', user: sessionUser(u), csrf_token: 'mock-csrf', session_expires_at: s.expires_at }
    }
    const s = mkSession(u, mfa)
    setCsrfToken('mock-csrf')
    audit(u.username, 'auth.login', u.username, `登录成功（${mfa}）`)
    return { status: 'ok', user: sessionUser(u), csrf_token: 'mock-csrf', session_expires_at: s.expires_at }
  }
  const mfaUser = (token: string): MUser => {
    const id = token.startsWith('mock-mfa:') ? token.slice(9) : ''
    const u = users.get(id)
    if (!u) throw new ApiError(401, 'UNAUTHENTICATED', '验证已过期，请重新登录')
    return u
  }
  const enrolled = (u: MUser, first: boolean, passkey?: Passkey) => {
    const recovery_codes = first ? codes() : []
    if (first) u.recovery = recovery_codes.length
    if (current && current.scope === 'enroll_mfa') current.scope = 'full'
    if (current && current.mfa === 'none') current.mfa = passkey ? 'passkey' : 'totp'
    return { recovery_codes, ...(passkey ? { passkey } : {}), me: me() }
  }

  const setupStatus = (): SetupStatus => ({
    initialized, wizard_finished: initialized && wizardFinished, server_version: 'mock', api_level: REQUIRED_API_LEVEL, ...(initialized ? { mode: settings.mode } : {}), needs_token: false, loopback: true, https: false, passkey_available: true,
    rp_id_suggestion: 'localhost', origin_suggestion: location.origin, min_password_length: 12,
    checks: [
      { name: '数据库', ok: true, message: '演示数据，无需数据库' }, { name: '文档解析 Worker', ok: true, message: '演示模式' },
      { name: 'HTTPS', ok: false, message: '演示：当前为 http://localhost，Passkey 仅限 localhost' }, { name: 'Passkey', ok: true, message: 'localhost 允许使用 Passkey' },
    ],
  })

  return {
    async getSetupStatus() {
      await delay(120)
      return setupStatus()
    },
    async verifySetupToken(token) { await delay(); if (!token.trim()) throw new ApiError(401, 'UNAUTHENTICATED', '初始化令牌不正确'); return true },
    async completeSetup(req) {
      await delay(400)
      if (initialized) throw new ApiError(409, 'ALREADY_SETUP', '系统已完成初始化')
      initialized = true
      wizardFinished = req.mode === 'local'
      settings.mode = req.mode
      if (req.rp_id) settings.rp_id = req.rp_id
      if (req.origins) settings.origins = req.origins
      if (req.mfa_policy) settings.mfa_policy = req.mfa_policy
      if (req.password_min_length) settings.password_min_length = req.password_min_length
      const a = req.admin ? mk({ username: req.admin.username, display_name: req.admin.display_name, role: 'admin', password: req.admin.password }) : admin
      current = null
      audit(a.username, 'setup.complete', a.username, '初始化完成')
      return loginOutcome(a, 'none')
    },
    async finishSetup() {
      await delay(250)
      const u = needFull()
      if (u.role !== 'admin') throw new ApiError(403, 'FORBIDDEN', '需要管理员权限')
      if (!wizardFinished) {
        if (needsMfa(u) && !hasMfa(u)) throw new ApiError(409, 'MFA_REQUIRED_BY_POLICY', '安全策略要求先绑定第二因素（TOTP 或 Passkey）')
        wizardFinished = true
        audit(u.username, 'setup.finished', u.id, '完成初始化向导')
      }
      return setupStatus()
    },

    async login(username, password) {
      await delay(300)
      const u = [...users.values()].find((x) => x.username.toLowerCase() === username.trim().toLowerCase())
      if (u && isLocked(u)) throw new ApiError(423, 'ACCOUNT_LOCKED', '账户已被临时锁定', undefined, { locked_until: u.locked_until })
      if (!u || u.status !== 'active' || !u.password || (!settings.allow_password_login) || u.password !== password) {
        if (u) { u.failed_logins++; if (u.failed_logins >= settings.lockout_threshold) u.locked_until = iso(settings.lockout_minutes * 60_000) }
        throw new ApiError(401, 'INVALID_CREDENTIALS', '用户名或密码不正确')
      }
      if (hasMfa(u)) return { status: 'mfa_required', mfa_token: 'mock-mfa:' + u.id, methods: [...(u.totp ? ['totp' as const] : []), ...(u.passkeys.length ? ['passkey' as const] : []), 'recovery' as const] }
      return loginOutcome(u, 'none')
    },
    async logout() { await delay(80); if (current) sessions.delete(current.id); current = null; freshUntil = 0; setCsrfToken('') },
    async getMe(opts) {
      await delay(60)
      if (!current || !sessions.has(current.id)) { current = null; if (!opts?.silent) fireUnauthenticated(); throw new ApiError(401, 'UNAUTHENTICATED', '请先登录') }
      setCsrfToken('mock-csrf')
      return me()
    },
    async mfaTotp(token, code) {
      await delay(250); const u = mfaUser(token)
      if (code !== DEMO_TOTP) throw new ApiError(401, 'INVALID_CREDENTIALS', '验证码不正确（演示验证码为 123456）')
      return loginOutcome(u, 'totp')
    },
    async mfaRecovery(token, code) {
      await delay(250); const u = mfaUser(token)
      if (code.toUpperCase() !== 'DEMO1-DEMO2') throw new ApiError(401, 'INVALID_CREDENTIALS', '恢复码不正确（演示恢复码为 DEMO1-DEMO2）')
      u.recovery = Math.max(0, u.recovery - 1)
      return loginOutcome(u, 'recovery')
    },
    async mfaPasskeyBegin(token) { mfaUser(token); return wa() },
    async mfaPasskeyFinish(token) { await delay(200); return loginOutcome(mfaUser(token), 'passkey') },
    passkeyLoginBegin: wa,
    async passkeyLoginFinish() {
      await delay(200)
      const u = [...users.values()].find((x) => x.status === 'active' && x.passkeys.length > 0)
      if (!u) throw new ApiError(401, 'INVALID_CREDENTIALS', '没有可用的 Passkey')
      return loginOutcome(u, 'passkey')
    },

    async reauth(password) {
      await delay(200); const s = need()
      if (userOf(s).password !== password) throw new ApiError(401, 'INVALID_CREDENTIALS', '密码不正确')
      freshUntil = Date.now() + FRESH_MS
      return me()
    },
    reauthPasskeyBegin: wa,
    async reauthPasskeyFinish() { await delay(200); need(); freshUntil = Date.now() + FRESH_MS; return me() },

    async changePassword(cur, next) {
      await delay(300); const u = needFull()
      if (u.password !== cur) throw new ApiError(401, 'INVALID_CREDENTIALS', '当前密码不正确')
      if (next.length < settings.password_min_length) throw new ApiError(422, 'VALIDATION_FAILED', `新密码至少 ${settings.password_min_length} 位`)
      u.password = next; u.must_change_password = false
      for (const [id, s] of sessions) if (s.userId === u.id && s.id !== current?.id) sessions.delete(id)
      audit(u.username, 'auth.password.change', u.username, '修改密码')
    },
    async getMySecurity() {
      await delay(100); const s = need(); const u = userOf(s)
      return { has_password: !!u.password, totp_enabled: u.totp, passkeys: u.passkeys.map((p) => ({ ...p })), recovery_codes_remaining: u.recovery, mfa_required_by_policy: needsMfa(u) }
    },
    async totpBegin() {
      await delay(150); const u = userOf(need())
      if (u.totp) throw new ApiError(409, 'CONFLICT', '已绑定 TOTP，请先关闭')
      const secret = 'JBSWY3DPEHPK3PXP'
      const challenge_id = uid(); totpPending.set(challenge_id, secret)
      return { challenge_id, secret, otpauth_uri: `otpauth://totp/OA%20Agent:${encodeURIComponent(u.username)}?secret=${secret}&issuer=OA%20Agent&algorithm=SHA1&digits=6&period=30`, digits: 6, period: 30, algorithm: 'SHA1' }
    },
    async totpConfirm(challengeId, code) {
      await delay(250); const u = userOf(need())
      if (!totpPending.has(challengeId)) throw new ApiError(422, 'VALIDATION_FAILED', '绑定请求已过期，请重新开始')
      if (code !== DEMO_TOTP) throw new ApiError(422, 'VALIDATION_FAILED', '验证码不正确（演示验证码为 123456）')
      totpPending.delete(challengeId)
      const first = !hasMfa(u); u.totp = true
      audit(u.username, 'auth.totp.enable', u.username, '绑定 TOTP')
      return enrolled(u, first)
    },
    async totpDisable() {
      await delay(200); const u = userOf(need()); needFresh()
      if (needsMfa(u) && u.passkeys.length === 0) throw new ApiError(409, 'CONFLICT', '策略要求多因素认证，且这是最后一个 MFA')
      u.totp = false; if (!hasMfa(u)) u.recovery = 0
      audit(u.username, 'auth.totp.disable', u.username, '关闭 TOTP')
    },
    passkeyRegisterBegin: wa,
    async passkeyRegisterFinish(_c, _cred, name) {
      await delay(250); const u = userOf(need())
      const first = !hasMfa(u)
      const p = fakePasskey(name?.trim() || '演示 Passkey'); u.passkeys.push(p)
      audit(u.username, 'auth.passkey.add', u.username, `注册 Passkey ${p.name}`)
      return enrolled(u, first, p)
    },
    async renamePasskey(id, name) {
      await delay(150); const u = userOf(need()); const p = u.passkeys.find((x) => x.id === id)
      if (!p) throw new ApiError(404, 'NOT_FOUND', 'Passkey 不存在')
      p.name = name; return { ...p }
    },
    async deletePasskey(id) {
      await delay(150); const u = userOf(need()); needFresh()
      if (!u.passkeys.some((x) => x.id === id)) throw new ApiError(404, 'NOT_FOUND', 'Passkey 不存在')
      if (needsMfa(u) && !u.totp && u.passkeys.length === 1) throw new ApiError(409, 'CONFLICT', '策略要求多因素认证，不能删除最后一个 MFA')
      if (!u.password && u.passkeys.length === 1) throw new ApiError(409, 'CONFLICT', '这是你唯一的登录方式，不能删除')
      u.passkeys = u.passkeys.filter((x) => x.id !== id); if (!hasMfa(u)) u.recovery = 0
      audit(u.username, 'auth.passkey.delete', u.username, '删除 Passkey')
    },
    async regenerateRecoveryCodes() {
      await delay(200); const u = userOf(need()); needFresh()
      if (!hasMfa(u)) throw new ApiError(409, 'CONFLICT', '请先绑定 TOTP 或 Passkey')
      const c = codes(); u.recovery = c.length; audit(u.username, 'auth.recovery.regenerate', u.username, '重新生成恢复码'); return c
    },
    async listMySessions() { await delay(100); const s = need(); return [...sessions.values()].filter((x) => x.userId === s.userId).map(sessionInfo) },
    async revokeMySession(id) {
      await delay(120); const s = need(); const t = sessions.get(id)
      if (!t || t.userId !== s.userId) throw new ApiError(404, 'NOT_FOUND', '会话不存在')
      sessions.delete(id); if (current?.id === id) { current = null; fireUnauthenticated() }
    },

    async getInvitation(token) {
      await delay(150)
      if (token.length < 6 || token === 'expired') throw new ApiError(404, 'NOT_FOUND', '邀请链接无效或已过期')
      return { username: 'new.colleague', display_name: '新同事', expires_at: iso(20 * 3_600_000), passkey_available: true }
    },
    async invitationPasskeyBegin() { return wa() },
    async invitationPasskeyFinish(_t, _c, _cred, name) {
      await delay(300)
      const u = mk({ username: 'new.colleague' + Math.floor(Math.random() * 100), display_name: '新同事', role: 'viewer', password: null, passkeys: [fakePasskey(name?.trim() || '演示 Passkey')] })
      return loginOutcome(u, 'passkey')
    },

    async adminListUsers(q) {
      await delay(150); needAdmin()
      const needle = q?.q?.trim().toLowerCase()
      const rows = [...users.values()].map(adminView).filter((u) => (!needle || u.username.toLowerCase().includes(needle) || u.display_name.toLowerCase().includes(needle)) && (!q?.role || u.role === q.role) && (!q?.status || u.status === q.status))
      return paginate(rows, q)
    },
    async adminCreateUser(input) {
      await delay(300); const a = needAdmin()
      if (!/^[A-Za-z0-9._@-]{3,64}$/.test(input.username)) throw new ApiError(422, 'VALIDATION_FAILED', '用户名需为 3–64 位字母、数字或 . _ @ -')
      if ([...users.values()].some((u) => u.username.toLowerCase() === input.username.toLowerCase())) throw new ApiError(409, 'CONFLICT', '用户名已存在')
      const pw = input.credential === 'password' ? tempPassword() : null
      const u = mk({ username: input.username, display_name: input.display_name, role: input.role, password: pw, must_change_password: !!pw, last_login_at: null, created_at: iso() })
      audit(a.username, 'user.create', u.username, `创建用户（${input.role}）`)
      return { user: adminView(u), ...(pw ? { temporary_password: pw } : { invite: newInvite() }) } as never
    },
    async adminGetUser(id) { await delay(100); needAdmin(); return adminView(findUser(id)) },
    async adminUpdateUser(id, patch) {
      await delay(200); const a = needAdmin(); const u = findUser(id)
      if (patch.role && patch.role !== 'admin') guardLastAdmin(u, true)
      if (patch.status === 'disabled') guardLastAdmin(u, true)
      if (patch.role) u.role = patch.role
      if (patch.status) { u.status = patch.status; if (patch.status === 'disabled') revokeFor(u.id) }
      if (patch.display_name !== undefined) u.display_name = patch.display_name
      audit(a.username, 'user.update', u.username, '修改用户')
      return adminView(u)
    },
    async adminDeleteUser(id) {
      await delay(200); const a = needAdmin(); const u = findUser(id)
      if (u.id === a.id) throw new ApiError(409, 'CONFLICT', '不能删除自己')
      guardLastAdmin(u, true)
      revokeFor(u.id); users.delete(id); audit(a.username, 'user.delete', u.username, '删除用户')
    },
    async adminResetPassword(id) {
      await delay(250); const a = needAdmin(); const u = findUser(id)
      const pw = tempPassword(); u.password = pw; u.must_change_password = true; revokeFor(u.id)
      audit(a.username, 'user.reset_password', u.username, '重置密码'); return pw
    },
    async adminResetMfa(id) {
      await delay(250); const a = needAdmin(); const u = findUser(id)
      u.totp = false; u.passkeys = []; u.recovery = 0; revokeFor(u.id)
      audit(a.username, 'user.reset_mfa', u.username, '重置 MFA'); return adminView(u)
    },
    async adminUnlockUser(id) {
      await delay(150); const a = needAdmin(); const u = findUser(id)
      u.failed_logins = 0; u.locked_until = null; audit(a.username, 'user.unlock', u.username, '解锁用户'); return adminView(u)
    },
    async adminRevokeSessions(id) { await delay(150); const a = needAdmin(); const u = findUser(id); const n = revokeFor(u.id); audit(a.username, 'user.revoke_sessions', u.username, `吊销 ${n} 个会话`); return n },
    async adminCreateInvite(id) {
      await delay(200); needAdmin(); const u = findUser(id)
      if (u.password) throw new ApiError(409, 'CONFLICT', '该用户已有密码，邀请仅用于仅 Passkey 用户')
      return newInvite()
    },
    async adminListSessions(userId) {
      await delay(150); needAdmin()
      return [...sessions.values()].filter((s) => !userId || s.userId === userId).map(sessionInfo)
    },
    async adminRevokeSession(id) {
      await delay(120); needAdmin(); const s = sessions.get(id)
      if (!s) throw new ApiError(404, 'NOT_FOUND', '会话不存在')
      sessions.delete(id); if (current?.id === id) { current = null; fireUnauthenticated() }
    },
    async adminGetSecuritySettings() { await delay(100); needAdmin(); return { ...settings, origins: [...settings.origins] } },
    async adminUpdateSecuritySettings(patch) {
      await delay(250); const a = needAdmin()
      const { confirm_passkey_reset, ...rest } = patch
      const rpChanged = (rest.rp_id !== undefined && rest.rp_id !== settings.rp_id) || (rest.origins !== undefined && rest.origins.join('|') !== settings.origins.join('|'))
      if (rpChanged && !confirm_passkey_reset) throw new ApiError(409, 'PASSKEY_RESET_REQUIRED', '修改 RP ID / 来源会使已注册的 Passkey 失效，请确认后重试')
      if (rest.allow_password_login === false && [...users.values()].some((u) => u.status === 'active' && !u.passkeys.length)) throw new ApiError(409, 'CONFLICT', '仍有账号没有 Passkey，不能关闭密码登录')
      if (rpChanged) for (const u of users.values()) u.passkeys = []
      Object.assign(settings, rest)
      audit(a.username, 'security.update', 'settings', '修改安全策略')
      return { ...settings, origins: [...settings.origins] }
    },
  }
}
