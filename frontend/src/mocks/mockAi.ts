/**
 * 演示用 AI 能力：当前用户的大模型状态、个人大模型配置、管理员 AI 策略，以及导入选项（instruction / automation / auto_ack）的服务端规则。
 * 规则与契约一致：错误码 AUTO_ARCHIVE_DISABLED(403) / AUTO_ACK_REQUIRED(422) / LLM_REQUIRED(422) / PERSONAL_LLM_DISABLED(403)，个人地址规则更严格，测试限速 429。
 * 地址 ?mock_llm=none|global|personal 选择初始状态（默认 global）。
 */
import {
  ApiError, type AiPolicy, type Api, type ImportOptions, type LlmConfig, type LlmPreset, type LlmStatus, type LlmTestInput, type LlmTestResult, type Role,
} from '../api/types'
import { isLocalHost, keyHint, validateBaseUrl } from '../utils/llm'
import { rateLimitedMessage } from '../utils/authErrors'
import { roleAtLeast } from '../utils/authz'

const delay = (ms = 200) => new Promise((r) => setTimeout(r, ms))
const now = () => new Date().toISOString()

export const MOCK_ACK_VERSION = '2026-10-v1'
export const MOCK_ACK_TEXT = `全自动归档模式由大模型自动决定每个文件的部门、类型、日期、新文件名与归档位置，并直接发布到归档目录，无需任何人工审批。
大模型可能误判、遗漏或错误命名文件；无法确认的文件会留待人工处理，但已归档的内容不会事先经过人工核对。
你可以在任务页或 AI 工作台一键撤销本次全自动归档（仅当归档文件未被改动时），但在撤销前，错误归档的文件可能已被他人查看或引用。
所有全自动归档都会在台账中标记为“全自动”并记录所用模型，并写入审计日志。
继续即表示你已知悉上述风险，并同意由你及所在组织承担由此产生的后果。`

export type MockAiApi = Pick<Api, 'getLlmStatus' | 'getPersonalLlm' | 'savePersonalLlm' | 'deletePersonalLlm' | 'testPersonalLlm' | 'listPersonalLlmModels' | 'getAiPolicy' | 'updateAiPolicy'>

export interface ResolvedImport { automation: 'review' | 'auto'; instruction: string; llm_source: 'none' | 'global' | 'personal'; llm_model: string }
export interface MockAiCtl { resolveImport(options: ImportOptions | undefined): ResolvedImport; status(): LlmStatus }

interface Deps {
  role: () => Role | null
  globalCfg: () => LlmConfig
  presets: LlmPreset[]
  models: (baseUrl: string) => string[]
  audit: (actor: string, action: string, target: string, summary: string) => void
}

const emptyPersonal = (): LlmConfig => ({ mode: 'heuristic', preset_id: '', base_url: '', model: '', api_key_set: false, api_key_hint: '', vision_model: '', allow_vision: false, state: 'unconfigured', updated_at: null })

export function createMockAi(d: Deps, initial: 'none' | 'global' | 'personal'): { api: MockAiApi; ctl: MockAiCtl; seedPersonal: () => LlmConfig } {
  let policy: AiPolicy = { allow_personal_llm: true, allow_personal_private_hosts: false, allow_auto_archive: true, updated_at: null }
  let personal: LlmConfig = initial === 'personal'
    ? { mode: 'llm', preset_id: 'qwen', base_url: 'https://dashscope.aliyuncs.com/compatible-mode/v1', model: 'qwen3.7-plus', api_key_set: true, api_key_hint: '••••demo', vision_model: '', allow_vision: false, state: 'ok', updated_at: now() }
    : emptyPersonal()
  let personalKey = initial === 'personal' ? 'sk-personal-demo' : ''
  const testHits: number[] = []

  const role = () => d.role() ?? 'viewer'
  const g = () => d.globalCfg()
  const usable = (c: LlmConfig) => c.mode === 'llm' && c.state === 'ok' && !!c.model
  const personalActive = () => policy.allow_personal_llm && roleAtLeast(role(), 'operator') && usable(personal)

  function effective(): { source: 'none' | 'global' | 'personal'; model: string; label: string } {
    if (personalActive()) return { source: 'personal', model: personal.model, label: d.presets.find((p) => p.id === personal.preset_id)?.name ?? '自定义' }
    if (usable(g())) return { source: 'global', model: g().model, label: d.presets.find((p) => p.id === g().preset_id)?.name ?? '自定义' }
    return { source: 'none', model: '', label: '' }
  }
  function status(): LlmStatus {
    const e = effective()
    const configured = e.source !== 'none'
    const personalAllowed = policy.allow_personal_llm && roleAtLeast(role(), 'operator')
    return {
      configured, source: e.source, mode: configured ? 'llm' : 'heuristic', model: e.model, provider_label: e.label,
      global_configured: usable(g()), personal_configured: policy.allow_personal_llm && usable(personal), personal_allowed: personalAllowed, allow_personal_private_hosts: policy.allow_personal_private_hosts,
      auto_archive_allowed: policy.allow_auto_archive && configured && roleAtLeast(role(), 'operator'),
      auto_ack_version: MOCK_ACK_VERSION, auto_ack_text: MOCK_ACK_TEXT,
      setup_hint: configured ? 'none' : role() === 'admin' ? 'admin_global' : personalAllowed ? 'personal_or_admin' : 'contact_admin',
    }
  }

  function needPersonal() {
    if (!roleAtLeast(d.role(), 'operator')) throw new ApiError(403, 'FORBIDDEN', '需要操作员或更高权限', 'mock-req')
    if (!policy.allow_personal_llm) throw new ApiError(403, 'PERSONAL_LLM_DISABLED', '管理员未开放个人大模型 API', 'mock-req')
  }
  function personalUrl(raw: string) {
    const e = validateBaseUrl(raw)
    if (e) throw new ApiError(422, 'VALIDATION_FAILED', e, 'mock-req')
    if (!policy.allow_personal_private_hosts) {
      const u = new URL(raw.trim())
      if (u.protocol !== 'https:' || isLocalHost(u.hostname)) throw new ApiError(422, 'VALIDATION_FAILED', '个人 API 不允许使用内网/本机地址或 http', 'mock-req')
    }
  }
  function fakeTest(input: LlmTestInput): LlmTestResult {
    const key = input.api_key?.trim() || personalKey
    if (key === 'bad') return { ok: false, latency_ms: 420, message: '密钥无效（401）', tool_calling: null, vision_ok: null }
    return { ok: true, latency_ms: 420, message: '连接正常', tool_calling: !/reasoner/i.test(input.model), vision_ok: input.vision_model?.trim() ? !/bad/i.test(input.vision_model) : null }
  }

  const api: MockAiApi = {
    async getLlmStatus() { await delay(120); return status() },
    async getPersonalLlm() { await delay(120); needPersonal(); return { ...personal } },
    async savePersonalLlm(input) {
      await delay(300); needPersonal()
      if (input.mode === 'heuristic') { personal = emptyPersonal(); personalKey = ''; return { ...personal } }
      const base_url = (input.base_url ?? '').trim(), model = (input.model ?? '').trim()
      personalUrl(base_url)
      if (!model) throw new ApiError(422, 'VALIDATION_FAILED', '模型名称必填', 'mock-req')
      const preset_id = input.preset_id || 'custom'
      const preset = d.presets.find((p) => p.id === preset_id)
      const typed = input.api_key?.trim()
      const key = typed || (personal.api_key_set && !input.clear_api_key ? personalKey : '')
      if (preset?.needs_api_key && !key) throw new ApiError(422, 'VALIDATION_FAILED', '该服务需要 API Key', 'mock-req')
      personalKey = key
      personal = { mode: 'llm', preset_id, base_url, model, api_key_set: !!key, api_key_hint: keyHint(key), vision_model: (input.vision_model ?? '').trim(), allow_vision: !!input.allow_vision, state: 'ok', updated_at: now() }
      d.audit('演示用户', 'llm.personal.update', preset_id, `个人大模型设置为 ${model}`)
      return { ...personal }
    },
    async deletePersonalLlm() { await delay(200); needPersonal(); personal = emptyPersonal(); personalKey = ''; d.audit('演示用户', 'llm.personal.delete', '', '删除个人大模型配置') },
    async testPersonalLlm(input) {
      await delay(500); needPersonal()
      const t = Date.now()
      while (testHits.length && t - testHits[0] > 60_000) testHits.shift()
      if (testHits.length >= 6) { const e = new ApiError(429, 'RATE_LIMITED', rateLimitedMessage(20), 'mock-req'); e.retryAfter = 20; throw e }
      testHits.push(t)
      personalUrl(input.base_url)
      return fakeTest(input)
    },
    async listPersonalLlmModels(input) {
      await delay(300); needPersonal()
      personalUrl(input.base_url)
      return d.models(input.base_url)
    },
    async getAiPolicy() {
      await delay(120)
      if (role() !== 'admin') throw new ApiError(403, 'FORBIDDEN', '需要管理员权限', 'mock-req')
      return { ...policy }
    },
    async updateAiPolicy(patch) {
      await delay(200)
      if (role() !== 'admin') throw new ApiError(403, 'FORBIDDEN', '需要管理员权限', 'mock-req')
      for (const [k, v] of Object.entries(patch)) if (typeof v !== 'boolean') throw new ApiError(400, 'INVALID_ARGUMENT', `${k} 必须是布尔值`, 'mock-req')
      policy = { ...policy, ...patch, updated_at: now() }
      d.audit('演示管理员', 'ai.policy.update', '', `AI 使用策略：${JSON.stringify(patch)}`)
      return { ...policy }
    },
  }

  const ctl: MockAiCtl = {
    status,
    resolveImport(o) {
      const instruction = (o?.instruction ?? '').trim()
      if (instruction.length > 2000) throw new ApiError(400, 'INVALID_ARGUMENT', '任务说明不能超过 2000 字', 'mock-req')
      const automation = o?.automation ?? 'review'
      const st = status()
      if (automation === 'auto' && !policy.allow_auto_archive) throw new ApiError(403, 'AUTO_ARCHIVE_DISABLED', '管理员已关闭全自动归档', 'mock-req')
      const wanted = o?.llm_source ?? 'auto'
      const needLlm = !!instruction || automation === 'auto' || wanted !== 'auto'
      let source = st.source
      if (wanted === 'global') source = usable(g()) ? 'global' : 'none'
      if (wanted === 'personal') source = personalActive() ? 'personal' : 'none'
      if (needLlm && source === 'none') throw new ApiError(422, 'LLM_REQUIRED', '该操作需要已配置的大模型', 'mock-req')
      if (automation === 'auto') {
        if (!roleAtLeast(d.role(), 'operator')) throw new ApiError(403, 'FORBIDDEN', '需要操作员或更高权限', 'mock-req')
        if (!o?.auto_ack || o.auto_ack.accepted !== true || o.auto_ack.version !== MOCK_ACK_VERSION) throw new ApiError(422, 'AUTO_ACK_REQUIRED', '全自动模式需要确认风险提示（版本必须匹配）', 'mock-req')
      }
      const model = source === 'personal' ? personal.model : source === 'global' ? g().model : ''
      return { automation, instruction, llm_source: source, llm_model: model }
    },
  }
  return { api, ctl, seedPersonal: () => personal }
}
