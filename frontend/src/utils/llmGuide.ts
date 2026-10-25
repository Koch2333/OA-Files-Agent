import type { LlmConfig, LlmPreset } from '../api/types'

/** “简单模式”大模型设置向导的纯逻辑：服务商卡片文案、密钥格式识别、剪贴板提示、步骤状态机、连接状态摘要。 */

export type ConnectKind = 'login' | 'paste' | 'local'

export interface ProviderGuide {
  id: string
  /** 一句话大白话介绍 */
  tagline: string
  kind: ConnectKind
  recommended?: boolean
  /** 密钥常见格式；不匹配只给提示（服务商随时可能改格式），不阻止尝试 */
  keyPattern?: RegExp
  keyExample?: string
}

/** 简单模式展示的服务商（顺序即卡片顺序）；自定义/其他服务只在高级设置里。 */
export const GUIDES: ProviderGuide[] = [
  { id: 'deepseek', tagline: '国内可直接使用，价格实惠，中文公文识别效果好。', kind: 'paste', recommended: true, keyPattern: /^sk-[A-Za-z0-9]{20,64}$/, keyExample: 'sk-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx' },
  { id: 'qwen', tagline: '阿里云出品，国内可直接使用，适合已有阿里云账号的企业。', kind: 'paste', keyPattern: /^sk-[A-Za-z0-9]{20,64}$/, keyExample: 'sk-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx' },
  { id: 'openrouter', tagline: '用一个账号调用多家模型；登录一下就自动设置好，不用复制密钥。国内网络可能需要代理。', kind: 'login', keyPattern: /^sk-or-v1-[A-Za-z0-9]{32,}$/, keyExample: 'sk-or-v1-xxxxxxxx…' },
  { id: 'openai', tagline: 'ChatGPT 的官方接口。国内网络可能需要代理。', kind: 'paste', keyPattern: /^sk-(?:proj-|svcacct-|admin-)[A-Za-z0-9_-]{20,}$|^sk-[A-Za-z0-9]{20,}$/, keyExample: 'sk-proj-xxxxxxxx…' },
  { id: 'ollama', tagline: '在你自己的电脑上运行，文档不会发送出去，无需密钥；需要先安装 Ollama。', kind: 'local' },
]

export function guideFor(id: string | null | undefined): ProviderGuide | undefined { return GUIDES.find((g) => g.id === id) }

export function badgeText(k: ConnectKind): string { return k === 'login' ? '一键登录' : k === 'paste' ? '粘贴密钥' : '本机运行' }

/** 简单模式的卡片：只列出后端预设里存在的服务商。 */
export function simpleCards(presets: LlmPreset[], opts: { allowLocal: boolean }): Array<{ guide: ProviderGuide; preset: LlmPreset }> {
  const out: Array<{ guide: ProviderGuide; preset: LlmPreset }> = []
  for (const g of GUIDES) {
    if (g.kind === 'local' && !opts.allowLocal) continue
    const p = presets.find((x) => x.id === g.id)
    if (p) out.push({ guide: g, preset: p })
  }
  return out
}

/** 简短名称（去掉括号里的补充说明）。 */
export function shortName(p: Pick<LlmPreset, 'name'> | undefined): string { return (p?.name ?? '').replace(/[（(].*$/, '').trim() }

// ---------- 密钥格式 ----------

/** 粘贴内容里常见的“带前缀/引号”的杂质，只做首尾清理。 */
export function cleanKey(raw: string): string {
  return raw.trim().replace(/^["'“”‘’`]+|["'“”‘’`]+$/g, '').replace(/^Bearer\s+/i, '').trim()
}

/** 根据格式判断这串密钥属于哪家（仅用于“选错服务商”提示）；DeepSeek 与通义千问格式相同，无法区分。 */
export function detectKeyOwner(key: string): 'openrouter' | 'openai' | 'sk' | null {
  if (/^sk-or-/.test(key)) return 'openrouter'
  if (/^sk-(proj|svcacct|admin)-/.test(key)) return 'openai'
  if (/^sk-/.test(key)) return 'sk'
  return null
}

export interface KeyCheck {
  /** empty 未输入；ok 格式符合；warn 不太常见但可以尝试；bad 明显不对（不允许提交） */
  level: 'empty' | 'ok' | 'warn' | 'bad'
  message: string
}

export function checkKeyFormat(presetId: string, raw: string): KeyCheck {
  const key = cleanKey(raw)
  if (!key) return { level: 'empty', message: '' }
  if (/\s/.test(key)) return { level: 'bad', message: '里面有空格或换行，请只复制密钥本身。' }
  if (/[^\x21-\x7e]/.test(key)) return { level: 'bad', message: '看起来不是密钥（含有中文或特殊字符），请重新复制。' }
  if (key.length < 12) return { level: 'bad', message: '密钥太短，请确认复制完整。' }
  const g = guideFor(presetId)
  const owner = detectKeyOwner(key)
  if (owner === 'openrouter' && presetId !== 'openrouter') return { level: 'warn', message: '这像是 OpenRouter 的密钥，请确认选对了服务商。' }
  if (owner === 'openai' && presetId !== 'openai' && presetId !== 'openrouter') return { level: 'warn', message: '这像是 OpenAI 的密钥，请确认选对了服务商。' }
  if (g?.keyPattern && !g.keyPattern.test(key)) return { level: 'warn', message: '格式和常见的不太一样，不过可以先试试连接。' }
  return { level: 'ok', message: '格式正确。' }
}

export function maskKey(raw: string): string {
  const k = cleanKey(raw)
  const head = /^sk-[a-z]+-(v1-)?/i.exec(k)?.[0] ?? (k.startsWith('sk-') ? 'sk-' : '')
  return `${head}••••${k.slice(-4)}`
}

/** 剪贴板提示：只有当剪贴板内容“强匹配”所选服务商的密钥格式、且当前输入框为空/不同时才给出。 */
export interface ClipboardOffer { key: string; masked: string }
export function clipboardOffer(text: string | null | undefined, presetId: string, current: string, dismissed?: string | null): ClipboardOffer | null {
  if (!text || text.length > 400) return null
  const key = cleanKey(text)
  if (!key || key === cleanKey(current) || key === dismissed) return null
  if (checkKeyFormat(presetId, key).level !== 'ok') return null
  return { key, masked: maskKey(key) }
}

/** 安全读取剪贴板：不支持/被拒绝/无焦点都返回 null，绝不抛出。 */
export async function readClipboardSafe(nav: { clipboard?: { readText?: () => Promise<string> } } | undefined = typeof navigator === 'undefined' ? undefined : navigator): Promise<string | null> {
  try {
    if (!nav?.clipboard?.readText) return null
    return await nav.clipboard.readText()
  } catch { return null }
}

/** 浏览器已授予读剪贴板权限时才在“回到窗口”时自动读取；否则只提供手动“从剪贴板粘贴”按钮（点击即用户手势）。 */
export async function canAutoReadClipboard(nav: { permissions?: { query?: (d: { name: PermissionName }) => Promise<{ state: string }> } } | undefined = typeof navigator === 'undefined' ? undefined : navigator): Promise<boolean> {
  try {
    if (!nav?.permissions?.query) return false
    return (await nav.permissions.query({ name: 'clipboard-read' as PermissionName })).state === 'granted'
  } catch { return false }
}

// ---------- 模型推荐 ----------

/** 自动选模型：优先预设推荐；预设没有推荐（如 OpenAI）时从服务商返回的列表里挑一个性价比型号，再退到第一个。 */
export function pickRecommendedModel(preset: Pick<LlmPreset, 'default_model' | 'models'> | undefined, live: string[]): string {
  const def = preset?.default_model ?? ''
  if (live.length === 0) return def
  if (def && live.includes(def)) return def
  for (const m of preset?.models ?? []) if (live.includes(m.id)) return m.id
  const pref = live.find((m) => /^gpt-.*mini/i.test(m)) ?? live.find((m) => /^gpt-/i.test(m))
  return pref || live[0]
}

/** Ollama：主机地址 → OpenAI 兼容的 Base URL。 */
export function ollamaBase(host: string): string {
  const h = host.trim().replace(/\/+$/, '')
  return /\/v1$/.test(h) ? h : h + '/v1'
}
export const OLLAMA_DEFAULT_HOST = 'http://127.0.0.1:11434'
export function ollamaHost(baseUrl: string | undefined): string {
  return (baseUrl ?? '').replace(/\/v1\/?$/, '') || OLLAMA_DEFAULT_HOST
}

// ---------- 步骤状态机 ----------

export type GuideStep = 'choose' | 'connect' | 'verified'
export type CheckState = 'idle' | 'working' | 'ok' | 'failed'
export interface GuideState { step: GuideStep; presetId: string | null; check: CheckState; saving: boolean; saved: boolean }
export type GuideEvent =
  | { type: 'choose'; presetId: string }
  | { type: 'back' }
  | { type: 'keyEdited' }
  | { type: 'checkStart' }
  | { type: 'checkOk' }
  | { type: 'checkFail' }
  | { type: 'saveStart' }
  | { type: 'saveDone' }
  | { type: 'saveFail' }

export const initialGuide = (presetId: string | null = null): GuideState => ({ step: presetId ? 'connect' : 'choose', presetId, check: 'idle', saving: false, saved: false })

export function guideReducer(s: GuideState, e: GuideEvent): GuideState {
  switch (e.type) {
    case 'choose': return { step: 'connect', presetId: e.presetId, check: 'idle', saving: false, saved: false }
    case 'back': return s.saving ? s : { ...initialGuide(), presetId: null }
    case 'keyEdited': return s.step === 'choose' || s.saving ? s : { ...s, step: 'connect', check: 'idle', saved: false }
    case 'checkStart': return s.step === 'choose' || s.saving ? s : { ...s, step: 'connect', check: 'working' }
    case 'checkOk': return s.check === 'working' ? { ...s, step: 'verified', check: 'ok' } : s
    case 'checkFail': return s.check === 'working' ? { ...s, step: 'connect', check: 'failed' } : s
    case 'saveStart': return s.step === 'verified' && !s.saving ? { ...s, saving: true } : s
    case 'saveDone': return s.saving ? { ...s, saving: false, saved: true } : s
    case 'saveFail': return s.saving ? { ...s, saving: false } : s
  }
}
export const canSave = (s: GuideState): boolean => s.step === 'verified' && s.check === 'ok' && !s.saving

// ---------- 顶部连接状态 ----------

export interface ConnectionSummary {
  state: 'connected' | 'problem' | 'offline' | 'none'
  title: string
  detail: string
  /** 下一步提示（未设置/有问题时高亮） */
  next?: string
}

/** 密钥的显示名：登录授权取得的名称优先，其次尾号提示。 */
export function keyDisplay(c: Pick<LlmConfig, 'api_key_set' | 'api_key_hint' | 'key_label'> | undefined): string {
  if (!c?.api_key_set) return ''
  return c.key_label ? `${c.key_label}（${c.api_key_hint}）` : c.api_key_hint || '••••'
}

export function connectionSummary(cfg: LlmConfig | undefined, presets: LlmPreset[], scope: 'global' | 'personal'): ConnectionSummary {
  const personal = scope === 'personal'
  // 从未保存过（updated_at 为空）的全局离线规则也算“还没设置”；管理员明确保存过离线规则才显示“当前使用离线规则”
  if (!cfg || (cfg.mode === 'heuristic' && (personal || !cfg.updated_at)) || (cfg.mode === 'llm' && cfg.state === 'unconfigured')) {
    return personal
      ? { state: 'none', title: '还没有设置个人大模型', detail: '目前使用管理员配置的全局大模型（如已配置）。', next: '想用自己的账号？在下面选一个服务商。' }
      : { state: 'none', title: '还没有设置', detail: '智能识别需要先连接一个大模型。', next: '第一步：在下面选择一个服务商。' }
  }
  if (cfg.mode === 'heuristic') return { state: 'offline', title: '当前使用：离线规则', detail: '不联网、不需要密钥；识别能力有限。想要更准确，可改用 AI 大模型。' }
  const name = shortName(presets.find((p) => p.id === cfg.preset_id)) || cfg.preset_id
  const parts = [name, cfg.model]
  const k = keyDisplay(cfg)
  if (k) parts.push(`密钥 ${k}`)
  if (cfg.state === 'unavailable') return { state: 'problem', title: `连接有问题：${parts.join(' · ')}`, detail: '已保存的配置暂时无法使用（密钥无法解密或服务不可用）。', next: '请重新粘贴密钥或重新登录授权。' }
  return { state: 'connected', title: `已连接：${parts.join(' · ')}`, detail: personal ? '你提交的任务将使用这个模型。' : '全员的任务默认使用这个模型。' }
}
