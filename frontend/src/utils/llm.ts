import { ApiError, type LlmConfig, type LlmConfigInput, type LlmPreset } from '../api/types'
import { describeError, errMsg, requestIdSuffix } from './errorInfo'

/** 设置页表单状态；api_key 仅是用户刚输入的新密钥，已保存的密钥永不回显。 */
export interface LlmForm {
  mode: 'heuristic' | 'llm'
  preset_id: string
  base_url: string
  model: string
  api_key: string
  clear_key: boolean
  vision_model: string
  allow_vision: boolean
}

export interface LlmErrors { base_url?: string; model?: string; api_key?: string; vision_model?: string }

/** 本机 / 内网地址（允许 http）：localhost、回环、RFC1918、链路本地、*.local、无点主机名。 */
export function isLocalHost(host: string): boolean {
  const h = host.toLowerCase().replace(/^\[|\]$/g, '')
  if (h === 'localhost' || h.endsWith('.localhost') || h.endsWith('.local') || h === '::1') return true
  const m = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(h)
  if (m) {
    const [a, b] = [Number(m[1]), Number(m[2])]
    return a === 127 || a === 10 || (a === 172 && b >= 16 && b <= 31) || (a === 192 && b === 168) || (a === 169 && b === 254)
  }
  if (h.includes(':')) return /^f[cd]/.test(h) || h.startsWith('fe80')
  return !h.includes('.')
}

/** 与契约一致：必填；https，或 http 仅限本机/内网。返回错误文案，合法返回 null。 */
export function validateBaseUrl(raw: string): string | null {
  const v = raw.trim()
  if (!v) return '请填写 Base URL'
  let u: URL
  try { u = new URL(v) } catch { return 'Base URL 格式不正确，应形如 https://api.example.com/v1' }
  if (u.protocol === 'https:') return null
  if (u.protocol === 'http:') return isLocalHost(u.hostname) ? null : '公网地址必须使用 https；http 仅允许本机或内网地址'
  return 'Base URL 必须以 https:// 开头'
}

/** 已保存密钥的提示：••••加最后 4 位。 */
export function keyHint(key: string): string {
  const k = key.trim()
  return k ? '••••' + k.slice(-4) : ''
}

export function formFromConfig(cfg: LlmConfig | undefined, presets: LlmPreset[]): LlmForm {
  if (cfg && cfg.base_url && cfg.model) return { mode: cfg.mode, preset_id: cfg.preset_id, base_url: cfg.base_url, model: cfg.model, api_key: '', clear_key: false, vision_model: cfg.vision_model ?? '', allow_vision: !!cfg.allow_vision }
  const p = presets[0]
  return { mode: cfg?.mode ?? 'heuristic', preset_id: p?.id ?? '', base_url: p?.base_url ?? '', model: p?.default_model ?? '', api_key: '', clear_key: false, vision_model: cfg?.vision_model ?? '', allow_vision: !!cfg?.allow_vision }
}

/**
 * 选择预设：换了预设就填入其 Base URL 与默认模型；再次点选当前预设时只补空值，不覆盖用户已改的内容。
 * 预设自身的 Base URL / 默认模型为空（自定义）时保留现有输入。
 */
export function applyPreset(form: LlmForm, preset: LlmPreset): LlmForm {
  const same = form.preset_id === preset.id
  const base_url = same ? form.base_url || preset.base_url : preset.base_url || form.base_url
  const model = same ? form.model || preset.default_model : preset.default_model || form.model
  return { ...form, preset_id: preset.id, base_url, model }
}

/** 是否有未保存改动；离线模式只比较 mode。 */
export function isDirty(form: LlmForm, saved: LlmConfig | undefined): boolean {
  if (!saved) return true
  if (form.mode !== saved.mode) return true
  if (form.mode === 'heuristic') return false
  return form.preset_id !== saved.preset_id || form.base_url.trim() !== saved.base_url || form.model.trim() !== saved.model
    || form.api_key.trim() !== '' || form.clear_key
    || form.vision_model.trim() !== (saved.vision_model ?? '') || form.allow_vision !== !!saved.allow_vision
}

/** 保存的密钥是否仍可用于当前表单（换预设即视为不再沿用）。 */
export function savedKeyUsable(form: LlmForm, saved: LlmConfig | undefined): boolean {
  return !!saved?.api_key_set && !form.clear_key && saved.preset_id === form.preset_id
}

export function validateForm(form: LlmForm, preset: LlmPreset | undefined, saved: LlmConfig | undefined): LlmErrors {
  const e: LlmErrors = {}
  if (form.mode !== 'llm') return e
  const u = validateBaseUrl(form.base_url)
  if (u) e.base_url = u
  if (!form.model.trim()) e.model = '请填写模型名称'
  if (form.allow_vision && !form.vision_model.trim()) e.vision_model = '开启视觉识别需填写视觉模型'
  if (preset?.needs_api_key && !form.api_key.trim() && !savedKeyUsable(form, saved)) e.api_key = '该服务需要 API Key'
  return e
}

export function buildInput(form: LlmForm): LlmConfigInput {
  if (form.mode === 'heuristic') return { mode: 'heuristic' }
  const input: LlmConfigInput = { mode: 'llm', preset_id: form.preset_id, base_url: form.base_url.trim(), model: form.model.trim(), allow_vision: form.allow_vision, vision_model: form.vision_model.trim() }
  if (form.api_key.trim()) input.api_key = form.api_key.trim()
  else if (form.clear_key) input.clear_api_key = true
  return input
}

/** 视觉模型下拉建议：预设的 vision_models（去重）。 */
export function visionOptions(preset: LlmPreset | undefined): ModelOption[] {
  const seen = new Set<string>()
  const out: ModelOption[] = []
  for (const m of preset?.vision_models ?? []) if (!seen.has(m.id)) { seen.add(m.id); out.push({ id: m.id, label: m.label && m.label !== m.id ? `${m.id} · ${m.label}` : m.id }) }
  return out
}

export interface ModelOption { id: string; label: string }

export interface ModelGroup { id: 'live' | 'preset'; label: string; items: ModelOption[] }

/**
 * 下拉分组：先“服务商返回（实时）”，再“预设建议”（已在实时列表中的不重复，实时项沿用预设的说明）。
 * query 非空时按 ID / 说明过滤（不区分大小写）；空分组不返回。
 */
export function modelGroups(live: string[], suggestions: ModelOption[], query = ''): ModelGroup[] {
  const label = new Map(suggestions.map((s) => [s.id, s.label]))
  const seen = new Set<string>()
  const liveItems: ModelOption[] = []
  for (const id of live) if (!seen.has(id)) { seen.add(id); liveItems.push({ id, label: label.get(id) ?? id }) }
  const presetItems = suggestions.filter((s) => !seen.has(s.id) && seen.add(s.id))
  const q = query.trim().toLowerCase()
  const keep = (o: ModelOption) => !q || o.id.toLowerCase().includes(q) || o.label.toLowerCase().includes(q)
  return ([
    { id: 'live', label: '服务商返回（实时）', items: liveItems.filter(keep) },
    { id: 'preset', label: '预设建议', items: presetItems.filter(keep) },
  ] as ModelGroup[]).filter((g) => g.items.length)
}

/** 是否可自动拉取模型列表：模式为大模型、Base URL 合法，且已有密钥（已保存或刚输入）；无需密钥的服务无此要求。 */
export function canFetchModels(form: LlmForm, preset: LlmPreset | undefined, saved: LlmConfig | undefined): boolean {
  if (form.mode !== 'llm' || validateBaseUrl(form.base_url)) return false
  if (preset && !preset.needs_api_key) return true
  return !!form.api_key.trim() || savedKeyUsable(form, saved)
}

/** 下拉建议：预设模型在前，其后是拉取到的列表（去重）。 */
export function modelOptions(preset: LlmPreset | undefined, fetched: string[]): ModelOption[] {
  const seen = new Set<string>()
  const out: ModelOption[] = []
  for (const m of preset?.models ?? []) if (!seen.has(m.id)) { seen.add(m.id); out.push({ id: m.id, label: m.label && m.label !== m.id ? `${m.id} · ${m.label}` : m.id }) }
  for (const id of fetched) if (!seen.has(id)) { seen.add(id); out.push({ id, label: id }) }
  return out
}

/** 状态芯片文案。 */
export function engineLabel(cfg: LlmConfig | undefined, presets: LlmPreset[]): string {
  if (!cfg || cfg.mode === 'heuristic') return '离线规则'
  const name = presets.find((p) => p.id === cfg.preset_id)?.name ?? cfg.preset_id
  return `${name} · ${cfg.model}`
}

export type LlmScope = 'global' | 'personal'

/**
 * 大模型配置相关错误文案。个人配置的 422（地址/校验）补充“更严格地址规则”的说明；
 * 403 PERSONAL_LLM_DISABLED 与 429 由通用映射处理（见 utils/errorInfo、utils/authErrors）。
 */
export function llmErrorText(e: unknown, scope: LlmScope): string {
  const base = errMsg(e)
  if (scope === 'personal' && e instanceof ApiError && e.status === 422 && e.code !== 'LLM_REQUIRED') {
    const i = describeError(e)
    return `${i.message.replace(/[。.]$/, '')}。个人 API 默认只允许公网 https 地址，不能使用内网/本机地址（管理员可在“AI 使用策略”中放开）。${requestIdSuffix(i.requestId)}`
  }
  return base
}
