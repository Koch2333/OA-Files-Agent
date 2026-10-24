import { ApiError, type LlmConfig, type LlmOauthStatus, type LlmPreset } from '../api/types'
import { errMsg } from './errorInfo'

/** “用平台账号登录授权”的纯逻辑（组件 LlmLoginPanel.vue 使用）。 */

export type OauthScope = 'personal' | 'global'
export type OauthStatusName = LlmOauthStatus['status']

/** 终态：不再需要轮询。 */
export function isTerminal(s: OauthStatusName): boolean {
  return s === 'connected' || s === 'failed' || s === 'denied' || s === 'expired'
}

/** 设置页回调标记（?llm_oauth=...）对应的提示；ok 表示服务商已放行，仍需轮询等待服务端完成。 */
export function callbackFlagText(flag: string | null | undefined): { kind: 'ok' | 'error' | 'none'; text: string } {
  switch (flag) {
    case 'ok': return { kind: 'ok', text: '' }
    case 'denied': return { kind: 'error', text: '你在服务商页面拒绝了授权，或授权未完成。可以重新发起，或手动填写 API Key。' }
    case 'expired': return { kind: 'error', text: '登录授权已超时（10 分钟），请重新发起。' }
    case 'used': return { kind: 'error', text: '这次授权已被使用过，请重新发起。' }
    case 'invalid': return { kind: 'error', text: '授权回调无效（可能已过期或不是由本页面发起），请重新发起。' }
    case undefined: case null: case '': return { kind: 'none', text: '' }
    default: return { kind: 'error', text: '登录授权失败，请重新发起。' }
  }
}

/** 轮询结束后给用户看的文案。 */
export function statusText(s: LlmOauthStatus, platform: string): { kind: 'success' | 'warn' | 'error'; text: string } {
  switch (s.status) {
    case 'connected':
      if (s.test && !s.test.ok) return { kind: 'warn', text: s.message || `已连接 ${platform}，但连接测试未通过：${s.test.message}` }
      return { kind: 'success', text: `已通过登录授权连接 ${platform}，连接测试${s.test ? '通过' : '未运行'}。` }
    case 'denied': return { kind: 'error', text: s.message || '你拒绝了授权。' }
    case 'expired': return { kind: 'error', text: s.message || '登录授权已超时，请重新发起。' }
    default: return { kind: 'error', text: s.message || '登录授权失败，请重试。' }
  }
}

/** 是否应显示“登录授权”按钮：预设已实现流程。 */
export function canOauth(p: LlmPreset | undefined): boolean { return !!p && p.oauth_login === true }

/** 仅对 https 的服务商密钥页给出链接（防止预设文件里的奇怪协议被渲染成链接）。 */
export function safeKeyUrl(p: LlmPreset | undefined): string | null {
  const raw = p?.key_url
  if (!raw) return null
  try { const u = new URL(raw); return u.protocol === 'https:' ? u.href : null } catch { return null }
}

export function keySourceLabel(c: Pick<LlmConfig, 'api_key_set' | 'key_source'> | undefined, platform: string): string {
  if (!c?.api_key_set) return ''
  return c.key_source === 'oauth' ? `已通过登录授权连接（${platform}）` : '手动填写的密钥'
}

export function oauthErrorText(e: unknown): string {
  if (e instanceof ApiError && e.code === 'OAUTH_PUBLIC_URL_REQUIRED') return e.message
  return errMsg(e)
}

export interface PollOptions {
  get: (state: string) => Promise<LlmOauthStatus>
  state: string
  /** 毫秒；测试里用很小的值 */
  intervalMs?: number
  /** 总时限（服务端 10 分钟过期，这里略长） */
  maxMs?: number
  isCancelled: () => boolean
  onUpdate?: (s: LlmOauthStatus) => void
  sleep?: (ms: number) => Promise<void>
  now?: () => number
}

/** 轮询到终态；被取消返回 null；超时返回 expired。连续网络错误 5 次后抛出最后一个错误。 */
export async function pollStatus(o: PollOptions): Promise<LlmOauthStatus | null> {
  const interval = o.intervalMs ?? 1500
  const maxMs = o.maxMs ?? 11 * 60_000
  const sleep = o.sleep ?? ((ms: number) => new Promise<void>((r) => setTimeout(r, ms)))
  const now = o.now ?? Date.now
  const start = now()
  let fails = 0
  while (!o.isCancelled()) {
    try {
      const s = await o.get(o.state)
      fails = 0
      o.onUpdate?.(s)
      if (isTerminal(s.status)) return s
    } catch (e) {
      // 状态已不存在（过期/他人）是终态，不重试
      if (e instanceof ApiError && e.status >= 400 && e.status < 500) throw e
      if (++fails >= 5) throw e
    }
    if (now() - start > maxMs) return { status: 'expired', provider: '', scope: '', message: '登录授权已超时，请重新发起。' }
    await sleep(interval)
  }
  return null
}

/** 跨页面跳转（web 流程）时在 sessionStorage 里记住进行中的授权；state 不含密钥材料，PKCE verifier 只在服务端。 */
const KEY = 'oa.llmOauth'
export interface PendingOauth { state: string; scope: OauthScope; provider: string }
export function savePending(p: PendingOauth, store: Pick<Storage, 'setItem'> = sessionStorage) {
  try { store.setItem(KEY, JSON.stringify(p)) } catch { /* 隐私模式：回来后无法续接，用户重新发起即可 */ }
}
export function loadPending(store: Pick<Storage, 'getItem'> = sessionStorage): PendingOauth | null {
  try {
    const v = JSON.parse(store.getItem(KEY) ?? 'null') as Partial<PendingOauth> | null
    if (v && typeof v.state === 'string' && v.state && (v.scope === 'personal' || v.scope === 'global') && typeof v.provider === 'string') return { state: v.state, scope: v.scope, provider: v.provider }
  } catch { /* ignore */ }
  return null
}
export function clearPending(store: Pick<Storage, 'removeItem'> = sessionStorage) {
  try { store.removeItem(KEY) } catch { /* ignore */ }
}
