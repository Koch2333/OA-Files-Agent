/** 角色与路由守卫的纯逻辑。后端才是权威；这里只决定界面是否“提供”动作。 */
export type Role = 'admin' | 'operator' | 'viewer'
const RANK: Record<Role, number> = { viewer: 1, operator: 2, admin: 3 }

export function roleAtLeast(role: Role | null | undefined, min: Role): boolean {
  return !!role && RANK[role] >= RANK[min]
}

export const ROLE_LABEL: Record<Role, string> = { admin: '管理员', operator: '操作员', viewer: '只读' }

export interface GuardSession { scope: 'full' | 'enroll_mfa'; role: Role; must_change_password: boolean }
export interface GuardState {
  /** 初始化状态是否已知（未知时不拦截，由调用方先获取） */
  initialized: boolean | null
  /** 初始化向导是否已完成（服务端闸门）：false 时除 /setup、/login、邀请页外一律回到 /setup；null/true 不拦截 */
  wizardFinished?: boolean | null
  session: GuardSession | null
}
export interface GuardRoute { path: string; fullPath: string; meta: { public?: boolean; minRole?: Role; allowRestricted?: boolean } }
export type GuardResult = true | { path: string; query?: Record<string, string> }

/** 只允许站内相对路径作为登录后的返回地址，防止开放重定向。 */
export function safeRedirect(p: unknown): string {
  if (typeof p !== 'string' || !p.startsWith('/') || p.startsWith('//') || p.includes('\\')) return '/'
  if (/^\/(login|setup|invite)(\/|\?|$)/.test(p)) return '/'
  return p
}

export function guardDecision(to: GuardRoute, s: GuardState): GuardResult {
  const isSetup = to.path === '/setup'
  if (s.initialized === false && !isSetup) return { path: '/setup' }
  const gated = s.initialized === true && s.wizardFinished === false
  if (gated) {
    // 服务端闸门：向导未完成时只有 /setup、/login、公开的邀请页可达；其余一律回 /setup（未登录先去登录）
    if (isSetup) return s.session ? true : { path: '/login', query: { redirect: '/setup' } }
    if (to.meta.public) return true
    return s.session ? { path: '/setup' } : { path: '/login', query: { redirect: '/setup' } }
  }
  if (isSetup) {
    if (s.initialized === true) return { path: s.session ? '/' : '/login' }
    return true
  }
  if (to.meta.public) {
    if (to.path === '/login' && s.session?.scope === 'full' && !s.session.must_change_password) return { path: '/' }
    return true
  }
  if (!s.session) return { path: '/login', query: to.fullPath === '/' ? {} : { redirect: to.fullPath } }
  if (s.session.scope === 'enroll_mfa' && !to.meta.allowRestricted) return { path: '/account/security', query: { enroll: '1' } }
  if (s.session.must_change_password && !to.meta.allowRestricted) return { path: '/account/security', query: { change_password: '1' } }
  if (to.meta.minRole && !roleAtLeast(s.session.role, to.meta.minRole)) return { path: '/forbidden' }
  return true
}

/** 重新认证信号：契约只说 403；设计约定 details.reason == 'reauth_required'。同时兼容 code。 */
export function isReauthRequired(e: { status?: number; code?: string; details?: Record<string, unknown> } | null | undefined, fresh?: boolean): boolean {
  if (!e || e.status !== 403) return false
  if (e.details?.reason === 'reauth_required') return true
  if (e.code === 'REAUTH_REQUIRED' || e.code === 'FRESH_AUTH_REQUIRED') return true
  return e.code === 'FORBIDDEN' && fresh === false && !e.details?.reason
}
