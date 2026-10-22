/** 初始化向导步骤逻辑。 */
export type SetupStepId = 'welcome' | 'token' | 'mode' | 'admin' | 'policy' | 'mfa' | 'llm' | 'finish'
export const SETUP_STEPS: { id: SetupStepId; label: string }[] = [
  { id: 'welcome', label: '环境检查' }, { id: 'token', label: '初始化令牌' }, { id: 'mode', label: '运行模式' },
  { id: 'admin', label: '管理员' }, { id: 'policy', label: '域名与策略' }, { id: 'mfa', label: '多因素认证' }, { id: 'llm', label: '大模型' }, { id: 'finish', label: '完成' },
]

export type SetupMode = 'enterprise' | 'local'
export interface SetupFormLike {
  mode: SetupMode | ''
  token: string
  username: string; displayName: string; password: string; password2: string
  rpId: string; origins: string[]
  minLen: number
  sessionIdle: number; sessionMax: number
}
export interface StatusLike { needs_token: boolean; loopback: boolean; checks: { ok: boolean }[] }

/** local 模式不需要管理员/域名/MFA（也没有大模型一步：完成后可在设置中配置）：直接从模式选择进入确认。 */
export function stepsFor(mode: SetupMode | ''): SetupStepId[] {
  return mode === 'local' ? ['welcome', 'token', 'mode', 'finish'] : SETUP_STEPS.map((s) => s.id)
}

export const USERNAME_RE = /^[A-Za-z0-9._@-]{3,64}$/
export function usernameError(u: string): string | null {
  if (!u) return '请输入用户名'
  return USERNAME_RE.test(u) ? null : '3–64 位，仅限字母、数字与 . _ @ -'
}

/** 来源必须是精确 origin：协议 + 主机[:端口]，无路径、无通配符。 */
export function originError(o: string): string | null {
  try {
    const u = new URL(o)
    if (u.protocol !== 'https:' && u.protocol !== 'http:') return '必须以 http:// 或 https:// 开头'
    if (u.origin !== o.replace(/\/$/, '') || o.endsWith('/') || u.pathname !== '/' || u.search || u.hash) return '只能是精确的来源（协议+主机+端口），不含路径'
    return null
  } catch { return '不是有效的网址' }
}
export function rpIdError(rp: string, origins: string[]): string | null {
  if (!rp) return '请输入 RP ID（域名）'
  if (!/^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$/i.test(rp)) return 'RP ID 只能是域名（不含协议、端口、路径）'
  for (const o of origins) {
    try { const h = new URL(o).hostname; if (h !== rp && !h.endsWith('.' + rp)) return `来源 ${o} 的主机名必须等于 RP ID 或是其子域` } catch { /* originError 提示 */ }
  }
  return null
}

export function canAdvance(step: SetupStepId, f: SetupFormLike, st: StatusLike): string | null {
  switch (step) {
    case 'welcome': return null // 失败的检查只警告，不阻塞
    case 'token': return st.needs_token && !f.token.trim() ? '请输入初始化令牌' : null
    case 'mode':
      if (!f.mode) return '请选择运行模式'
      if (f.mode === 'local' && !st.loopback) return '本机模式只能从服务器本机访问时选择'
      return null
    case 'admin': {
      const u = usernameError(f.username); if (u) return u
      if (!f.displayName.trim()) return '请输入显示名'
      if (f.password.length < f.minLen) return `密码至少 ${f.minLen} 位`
      if (f.password !== f.password2) return '两次输入的密码不一致'
      return null
    }
    case 'policy': {
      if (!f.origins.length) return '至少填写一个允许的来源'
      for (const o of f.origins) { const e = originError(o); if (e) return `${o}：${e}` }
      const r = rpIdError(f.rpId, f.origins); if (r) return r
      if (f.sessionIdle < 5 || f.sessionIdle > 1440) return '空闲超时需在 5–1440 分钟之间'
      if (f.sessionMax < 1 || f.sessionMax > 720) return '会话最长时长需在 1–720 小时之间'
      return null
    }
    default: return null
  }
}

export function nextStep(step: SetupStepId, mode: SetupMode | ''): SetupStepId {
  const list = stepsFor(mode); const i = list.indexOf(step)
  return list[Math.min(list.length - 1, i + 1)]
}
export function prevStep(step: SetupStepId, mode: SetupMode | ''): SetupStepId {
  const list = stepsFor(mode); const i = list.indexOf(step)
  return list[Math.max(0, i - 1)]
}

/** 刷新后恢复到哪一步：密码/令牌不落盘，所以不能停留在需要它们之后的步骤。complete 之后的步骤不恢复（服务端已初始化）。 */
export function restoreStep(saved: SetupStepId | undefined, f: Pick<SetupFormLike, 'mode' | 'password' | 'token'>, needsToken: boolean): SetupStepId {
  if (!saved || !SETUP_STEPS.some((s) => s.id === saved)) return 'welcome'
  const order = SETUP_STEPS.map((s) => s.id)
  const target: SetupStepId = saved === 'mfa' || saved === 'llm' || saved === 'finish' ? (f.mode === 'local' ? 'mode' : 'policy') : saved
  const idx = order.indexOf(target)
  if (needsToken && !f.token && idx > order.indexOf('token')) return 'token'
  if (f.mode === 'enterprise' && idx > order.indexOf('admin') && !f.password) return 'admin'
  return target
}

/** RP ID 必须是当前访问主机名本身或其父域（后端同样校验）。 */
export function rpIdMatchesHost(rpId: string, host: string): boolean {
  const r = rpId.toLowerCase(), h = host.toLowerCase()
  return !!r && (h === r || h.endsWith('.' + r))
}
export function originsText(list: string[]): string { return list.join('\n') }
export function parseOrigins(text: string): string[] {
  return Array.from(new Set(text.split(/[\s,]+/).map((s) => s.trim().replace(/\/+$/, '')).filter(Boolean)))
}

/** complete 之后的向导阶段：只有这三步，可从服务端状态恢复。 */
export type ResumeStepId = 'mfa' | 'llm' | 'finish'
export interface ResumeInput {
  /** me.scope：enroll_mfa 表示策略要求绑定 MFA 但尚未绑定 */
  scope: 'full' | 'enroll_mfa'
  /** GET /v1/auth/security：策略是否要求 MFA、是否已绑定 TOTP/Passkey */
  mfaRequiredByPolicy: boolean
  hasMfa: boolean
  /** 本标签页最近到达的阶段（sessionStorage，只是步骤编号，非敏感） */
  saved?: string | null
}
export interface ResumeState { step: ResumeStepId; mfaRequired: boolean; mfaDone: boolean }

/**
 * 刷新/重新打开 /setup 时从服务端状态推导该回到哪一步（不依赖内存标志）：
 * 需要 MFA 而未绑定 → 绑定步骤（必须完成）；已绑定 MFA → 绑定步骤的“已绑定”视图（可重新生成恢复码，因为首次展示的恢复码刷新后无法找回）；
 * 本标签页记得更靠后的阶段（大模型/完成）且 MFA 条件已满足 → 回到该阶段。
 */
export function resumeStep(i: ResumeInput): ResumeState {
  const needMfa = i.scope === 'enroll_mfa' || (i.mfaRequiredByPolicy && !i.hasMfa)
  if (needMfa) return { step: 'mfa', mfaRequired: true, mfaDone: false }
  const mfaRequired = i.mfaRequiredByPolicy
  if (i.saved === 'llm' || i.saved === 'finish') return { step: i.saved, mfaRequired, mfaDone: i.hasMfa }
  return { step: 'mfa', mfaRequired, mfaDone: i.hasMfa }
}

/** POST /v1/setup/finish 失败后怎么办：策略要求 MFA 而未绑定 → 回到 MFA 步；其它错误留在“完成”步重试。 */
export function finishFailureStep(e: { status?: number; code?: string } | null | undefined): ResumeStepId {
  return e?.status === 409 && e.code === 'MFA_REQUIRED_BY_POLICY' ? 'mfa' : 'finish'
}
