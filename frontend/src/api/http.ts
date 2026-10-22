import { ApiError, type ApiErrorBody } from './types'
import { authErrorMessage, parseRetryAfter } from '../utils/authErrors'
import { staleBackendHint } from '../utils/apiLevel'

/** src/api 只处理 HTTP/SSE。基础地址默认同源（开发时由 Vite 代理）。 */
export const API_BASE: string = (import.meta.env.VITE_API_BASE as string | undefined) ?? ''

/**
 * 会话 Cookie（HttpOnly）由浏览器自动携带；CSRF 令牌只保存在内存（登录/me 返回），
 * 绝不写入 localStorage/sessionStorage，刷新页面后通过 GET /v1/auth/me 重新取得。
 */
let csrfToken = ''
export function setCsrfToken(t: string | undefined | null) { csrfToken = t ?? '' }
export function getCsrfToken() { return csrfToken }

export interface AuthHooks {
  /** 会话失效（401 UNAUTHENTICATED，非公开接口） */
  onUnauthenticated?: () => void
  /** 服务端尚未初始化（503 SETUP_REQUIRED） */
  onSetupRequired?: () => void
  /** 向导尚未完成（409 WIZARD_REQUIRED）：应跳转到 /setup */
  onWizardRequired?: () => void
  /** CSRF 令牌失效后重新取得（应调用 GET /v1/auth/me 并 setCsrfToken） */
  refreshCsrf?: () => Promise<void>
}
let hooks: AuthHooks = {}
export function setAuthHooks(h: AuthHooks) { hooks = h }
export function fireUnauthenticated() { hooks.onUnauthenticated?.() }
export function fireSetupRequired() { hooks.onSetupRequired?.() }
export function fireWizardRequired() { hooks.onWizardRequired?.() }

/** 网络层失败（后端不可达/连接被拒绝）：由 utils/backendHealth 订阅，用于显示全局“后端不可用”横幅。 */
let networkHook: (() => void) | null = null
export function setNetworkHook(fn: (() => void) | null) { networkHook = fn }

export function qs(params: object | undefined): string {
  if (!params) return ''
  const sp = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== null && v !== '') sp.set(k, String(v))
  }
  const s = sp.toString()
  return s ? `?${s}` : ''
}

async function toError(res: Response): Promise<ApiError> {
  let body: Partial<ApiErrorBody> | null = null
  try { body = await res.json() } catch { /* 非 JSON（例如代理失败） */ }
  const e = body?.error
  const rid = e?.request_id ?? res.headers.get('X-Request-ID') ?? undefined
  const retryAfter = parseRetryAfter(res.headers.get('Retry-After'))
  let err: ApiError
  if (e) err = new ApiError(res.status, e.code, e.message, rid, e.details)
  else err = new ApiError(res.status, 'HTTP_' + res.status, res.status === 404 ? '后端尚未提供该接口' : `请求失败（HTTP ${res.status}）`, rid)
  err.retryAfter = retryAfter
  if (res.status === 429 || res.status === 423) err.message = authErrorMessage(err)
  return err
}

export interface RawOptions {
  /** 401 不触发全局“会话过期”跳转（登录页、me 探测等） */
  silent?: boolean
}

function withCsrf(init: RequestInit): RequestInit {
  const method = (init.method ?? 'GET').toUpperCase()
  const headers = new Headers(init.headers)
  if (method !== 'GET' && method !== 'HEAD' && method !== 'OPTIONS' && csrfToken) headers.set('X-CSRF-Token', csrfToken)
  return { ...init, headers, credentials: 'same-origin' }
}

async function send(path: string, init: RequestInit): Promise<Response> {
  try {
    return await fetch(API_BASE + path, withCsrf(init))
  } catch {
    networkHook?.()
    throw new ApiError(0, 'NETWORK', '无法连接后端服务，请确认服务已启动')
  }
}

export async function raw(path: string, init: RequestInit = {}, opts: RawOptions = {}): Promise<Response> {
  let res = await send(path, init)
  if (!res.ok) {
    let err = await toError(res)
    // CSRF 令牌失效（例如另一标签页重新登录）：取一次新令牌并重试一次
    if (err.status === 403 && err.code === 'CSRF_INVALID' && hooks.refreshCsrf) {
      try {
        await hooks.refreshCsrf()
        res = await send(path, init)
        if (res.ok) return res
        err = await toError(res)
      } catch (e) { if (!(e instanceof ApiError)) throw e }
    }
    if (err.status === 401 && err.code === 'UNAUTHENTICATED' && !opts.silent) hooks.onUnauthenticated?.()
    if (err.status === 503 && err.code === 'SETUP_REQUIRED') hooks.onSetupRequired?.()
    if (err.status === 409 && err.code === 'WIZARD_REQUIRED') hooks.onWizardRequired?.()
    const stale = staleBackendHint(err.status, path)
    if (stale) err.message = stale
    throw err
  }
  return res
}

export async function json<T>(path: string, init: RequestInit & { body?: BodyInit | null } = {}, jsonBody?: unknown, opts: RawOptions = {}): Promise<T> {
  const headers = new Headers(init.headers)
  headers.set('Accept', 'application/json')
  if (jsonBody !== undefined) {
    headers.set('Content-Type', 'application/json')
    init = { ...init, body: JSON.stringify(jsonBody) }
  }
  const res = await raw(path, { ...init, headers }, opts)
  if (res.status === 204) return undefined as T
  const body = await res.json()
  // 契约约定成功响应为 { data: ... }；兼容直接返回对象
  return (body && typeof body === 'object' && 'data' in body ? body.data : body) as T
}

export function filenameFromDisposition(h: string | null, fallback: string): string {
  if (!h) return fallback
  const star = /filename\*=UTF-8''([^;]+)/i.exec(h)
  if (star) { try { return decodeURIComponent(star[1]) } catch { /* fallthrough */ } }
  const plain = /filename="?([^";]+)"?/i.exec(h)
  return plain ? plain[1] : fallback
}
