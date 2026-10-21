import { ApiError, type ApiErrorBody } from './types'

/** src/api 只处理 HTTP/SSE。基础地址默认同源（开发时由 Vite 代理）。 */
export const API_BASE: string = (import.meta.env.VITE_API_BASE as string | undefined) ?? ''

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
  if (e) return new ApiError(res.status, e.code, e.message, e.request_id ?? res.headers.get('X-Request-ID') ?? undefined, e.details)
  return new ApiError(res.status, 'HTTP_' + res.status, res.status === 404 ? '后端尚未提供该接口' : `请求失败（HTTP ${res.status}）`, res.headers.get('X-Request-ID') ?? undefined)
}

export async function raw(path: string, init: RequestInit = {}): Promise<Response> {
  let res: Response
  try {
    res = await fetch(API_BASE + path, init)
  } catch {
    throw new ApiError(0, 'NETWORK', '无法连接后端服务，请确认服务已启动')
  }
  if (!res.ok) throw await toError(res)
  return res
}

export async function json<T>(path: string, init: RequestInit & { body?: BodyInit | null } = {}, jsonBody?: unknown): Promise<T> {
  const headers = new Headers(init.headers)
  headers.set('Accept', 'application/json')
  if (jsonBody !== undefined) {
    headers.set('Content-Type', 'application/json')
    init = { ...init, body: JSON.stringify(jsonBody) }
  }
  const res = await raw(path, { ...init, headers })
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
