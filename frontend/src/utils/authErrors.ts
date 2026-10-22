/** 登录/账号相关错误的友好文案。 */
export function rateLimitedMessage(retryAfterSec?: number): string {
  if (retryAfterSec && retryAfterSec > 0) {
    const t = retryAfterSec >= 90 ? `${Math.ceil(retryAfterSec / 60)} 分钟` : `${Math.ceil(retryAfterSec)} 秒`
    return `操作过于频繁，请约 ${t}后再试。`
  }
  return '操作过于频繁，请稍后再试。'
}

export function parseRetryAfter(h: string | null | undefined, now = Date.now()): number | undefined {
  if (!h) return undefined
  if (/^\d+$/.test(h.trim())) return Number(h.trim())
  const t = Date.parse(h)
  return Number.isNaN(t) ? undefined : Math.max(0, Math.ceil((t - now) / 1000))
}

export interface ErrLike { status: number; code: string; message: string; details?: Record<string, unknown>; retryAfter?: number }

/** 把后端错误翻译成面向用户的中文；未知错误保留后端 message。 */
export function authErrorMessage(e: ErrLike): string {
  switch (e.code) {
    case 'INVALID_CREDENTIALS': return '用户名或密码不正确。'
    case 'ACCOUNT_LOCKED': {
      const until = typeof e.details?.locked_until === 'string' ? new Date(e.details.locked_until) : null
      return until && !Number.isNaN(until.getTime())
        ? `账户已被临时锁定，请在 ${until.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' })} 后重试，或联系管理员解锁。`
        : '账户已被临时锁定（连续登录失败），请稍后重试或联系管理员解锁。'
    }
    case 'RATE_LIMITED': return rateLimitedMessage(e.retryAfter)
    case 'LAST_ADMIN': return '不能停用、删除或降级最后一个启用的管理员。请先指定另一位管理员。'
    case 'ALREADY_SETUP': return '系统已完成初始化。'
    case 'SETUP_REQUIRED': return '系统尚未初始化，请先完成安装向导。'
    case 'CSRF_INVALID': return '页面安全令牌已失效，请刷新页面后重试。'
    case 'UNAUTHENTICATED': return '登录已过期，请重新登录。'
  }
  if (e.status === 423) return '账户已被临时锁定，请稍后重试或联系管理员。'
  if (e.status === 429) return rateLimitedMessage(e.retryAfter)
  return e.message
}
