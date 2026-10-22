import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { api, ApiError, type LoginResult, type Me, type Role, type SetupStatus } from '../api'
import { guardDecision, isReauthRequired, roleAtLeast, type GuardRoute, type GuardState, type GuardResult } from '../utils/authz'
import { errMsg } from '../utils/useAsync'

/** 用户取消了“重新认证”对话框：调用方应静默放弃操作。 */
export class ReauthCancelled extends Error { constructor() { super('已取消重新认证') } }

/**
 * 会话与角色状态。会话本身是 HttpOnly Cookie（前端读不到）；CSRF 令牌在 api/http.ts 的内存变量里。
 * 这里只保存 /v1/auth/me 的结果与初始化状态。
 */
export const useAuth = defineStore('auth', () => {
  const setup = ref<SetupStatus | null>(null)
  const me = ref<Me | null>(null)
  const ready = ref(false)
  const initError = ref<string | null>(null)
  const sessionExpired = ref(false)
  const reauthPrompt = ref<{ resolve: () => void; reject: (e: Error) => void } | null>(null)

  const user = computed(() => me.value?.user ?? null)
  const role = computed<Role | null>(() => user.value?.role ?? null)
  const policy = computed(() => me.value?.policy ?? null)
  const loggedIn = computed(() => !!me.value)
  const isLocalMode = computed(() => me.value?.policy.mode === 'local' || me.value?.mfa === 'local')
  /** 角色判断：只用于决定界面是否“提供”动作，后端仍会强制校验。 */
  const can = (min: Role) => roleAtLeast(role.value, min)
  const canWrite = computed(() => can('operator'))
  const isAdmin = computed(() => can('admin'))

  async function loadSetup(): Promise<SetupStatus> {
    setup.value = await api.getSetupStatus()
    return setup.value
  }
  /** 完成向导：服务端解除闸门后刷新公开状态与会话 */
  async function finishWizard(): Promise<void> {
    setup.value = await api.finishSetup()
    await refreshMe()
  }
  /** 收到 409 WIZARD_REQUIRED：本地状态落后于服务端，标记为未完成（路由守卫随即把用户带回 /setup） */
  function markWizardUnfinished() { if (setup.value) setup.value = { ...setup.value, wizard_finished: false } }
  async function refreshMe(): Promise<Me | null> {
    try {
      me.value = await api.getMe({ silent: true })
      sessionExpired.value = false
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) me.value = null
      else throw e
    }
    return me.value
  }
  /** 首次导航前调用：先看是否已初始化，再探测会话。网络失败时保持“未知”，由登录页显示错误并可重试。 */
  async function init(): Promise<void> {
    initError.value = null
    try {
      const s = await loadSetup()
      if (s.initialized) await refreshMe()
      else me.value = null
    } catch (e) {
      initError.value = errMsg(e)
    } finally { ready.value = true }
  }

  /** 登录/MFA/邀请完成后调用：ok 与 enroll_mfa 都已拿到会话 Cookie，重新取 me 作为权威状态。 */
  async function applyLogin(r: LoginResult): Promise<LoginResult['status']> {
    if (r.status !== 'mfa_required') {
      await refreshMe()
      if (setup.value && !setup.value.initialized) setup.value = { ...setup.value, initialized: true }
    }
    return r.status
  }

  async function logout() {
    try { await api.logout() } catch { /* 无论服务端结果如何都清理本地状态 */ }
    me.value = null
  }
  /** 绑定 MFA 成功后：接口附带的 me（含新的 scope/fresh）即为最新会话状态。 */
  function applyEnrolled(r: { me?: Me }) { if (r.me) me.value = r.me }
  function expire() {
    if (!me.value) return
    me.value = null
    sessionExpired.value = true
  }

  let reauthPromise: Promise<void> | null = null
  function requestReauth(): Promise<void> {
    if (reauthPromise) return reauthPromise // 并发的敏感操作共用同一个对话框
    reauthPromise = new Promise<void>((resolve, reject) => { reauthPrompt.value = { resolve, reject } }).finally(() => { reauthPromise = null })
    return reauthPromise
  }
  function reauthDone(m?: Me) { if (m) me.value = m; const p = reauthPrompt.value; reauthPrompt.value = null; p?.resolve() }
  function reauthCancel() { const p = reauthPrompt.value; reauthPrompt.value = null; p?.reject(new ReauthCancelled()) }

  /**
   * 执行敏感操作：me.fresh 为 false 时先要求重新认证；
   * 后端回 403（details.reason=reauth_required）时弹窗重新认证后重试一次。
   */
  async function withReauth<T>(fn: () => Promise<T>, preemptive = true): Promise<T> {
    if (preemptive && me.value && me.value.fresh === false && !isLocalMode.value) await requestReauth()
    try { return await fn() } catch (e) {
      if (e instanceof ApiError && isReauthRequired(e, me.value?.fresh)) { await requestReauth(); return await fn() }
      throw e
    }
  }

  function guard(to: GuardRoute): GuardResult {
    const st: GuardState = {
      initialized: setup.value ? setup.value.initialized : null,
      wizardFinished: setup.value ? setup.value.wizard_finished !== false : null,
      session: me.value ? { scope: me.value.scope, role: me.value.user.role, must_change_password: me.value.user.must_change_password } : null,
    }
    return guardDecision(to, st)
  }

  return { setup, me, ready, initError, sessionExpired, reauthPrompt, user, role, policy, loggedIn, isLocalMode, can, canWrite, isAdmin,
    loadSetup, finishWizard, markWizardUnfinished, refreshMe, init, applyLogin, applyEnrolled, logout, expire, requestReauth, reauthDone, reauthCancel, withReauth, guard }
})
