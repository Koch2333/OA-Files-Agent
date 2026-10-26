/**
 * 演示数据实现。仅在 VITE_USE_MOCK=true 或设置页开启“演示数据”时启用，
 * 界面会常驻“演示数据”标识。状态机语义与契约一致：审批与执行分离，未批准不可执行。
 */
import {
  ApiError, type AgentEvent, type Api, type ArchivePlan, type ArchivePlanItem, type ArchiveRule, type AuditLog,
  type CorrectionProposal, type FileQuery, type FileRecord, type ImportJob, type LedgerEntry, type LedgerQuery,
  type LlmConfig, type LlmPreset, type OATemplate, type Page, type PickedFile, type TaskStatus, type ImportOptions,
} from '../api/types'
import { MockFs } from './mockFs'
import { displayCategory, OTHER, categoryKey } from '../utils/classification'
import { keyHint, validateBaseUrl } from '../utils/llm'
import { mockPageUrl, mockPreview } from './mockPreview'
import { basename } from '../utils/fsPaths'
import { formatOf, isReadable } from '../utils/formats'
import { MockStorage } from './mockStorage'
import { createMockAuth, type MockAuthCtl } from './mockAuth'
import { createMockAi, mockModelsGuard, mockTestFault } from './mockAi'
import { createMockDiag } from './mockDiag'
import { createMockUploads } from './mockUploads'
import { faultKindFor, faultPersists, llmErrorFor } from './mockLlmFault'

const delay = (ms = 250) => new Promise((r) => setTimeout(r, ms))
const uid = () => crypto.randomUUID()
const now = () => new Date().toISOString()
const iso = (daysAgo: number) => new Date(Date.now() - daysAgo * 864e5).toISOString()

const DEPTS = ['财务部', '人事部', '采购部', '行政部']
const TYPES = ['合同', '通知', '报表', '申请']
const UNREADABLE_LABEL: Record<string, string> = { encrypted: '已加密', damaged: '文件损坏或无法解析', unsupported: '格式不支持读取内容', empty: '文件为空' }

function sha(): string { return Array.from({ length: 64 }, () => '0123456789abcdef'[Math.floor(Math.random() * 16)]).join('') }
function mimeOf(name: string) { return formatOf(name)?.mime ?? 'application/octet-stream' }

interface TaskRuntime { events: AgentEvent[]; timers: number[]; listeners: Set<(e: AgentEvent) => void>; seq: number }

const tasks = new Map<string, ImportJob>()
const files = new Map<string, FileRecord>()
const plans = new Map<string, ArchivePlan>()
const ledger: LedgerEntry[] = []
const runtime = new Map<string, TaskRuntime>()
const audit: AuditLog[] = []
/** 归档操作 → 发布的条目（回滚用）。auto=全自动发布：回滚后条目回到计划中等待人工审核。 */
const opStore = new Map<string, { planId: string; taskId: string; items: ArchivePlanItem[]; auto: boolean }>()
const mockFs = new MockFs(ledger)
const storage = new MockStorage({ importFn: (picked, o) => mockApi.createImport(picked, { options: o?.options }), archivedPaths: () => ledger.map((l) => l.archive_path) })
const templates: OATemplate[] = [
  { id: uid(), name: '集团通用 OA 模板', enabled: true, priority: 10, created_at: iso(30), match_rules: { filename_keywords: ['通知', '合同', '报表'], text_keywords: ['文号', '签发'] }, field_mappings: { department: '发文部门', document_type: '文种', date_source: 'ocr_text' } },
  { id: uid(), name: '财务报销单模板', enabled: false, priority: 20, created_at: iso(12), match_rules: { filename_keywords: ['报销', '发票'], text_keywords: ['报销人'] }, field_mappings: { department: '报销部门', document_type: '申请', date_source: 'filename' } },
]
interface PresetSpec { id: string; name: string; description: string; note: string; base_url: string; models: [string, string][]; default_model?: string; needs_api_key?: boolean; vision?: [string, string][]; docs_url?: string; key_url?: string; oauth?: boolean }
// 演示用的向导数据（真实数据在 backend/internal/llm/presets.json，由后端提供）
const mockGuide = (host: string, pattern: string, extra: string): Pick<LlmPreset, 'key_hosts' | 'key_pattern' | 'key_guide'> => ({
  key_hosts: [host], key_pattern: pattern,
  key_guide: { suggested_name: 'OA Agent', login_note: extra, cost_note: '演示数据：是否收费以服务商页面为准。', steps: [
    { title: '登录或注册', hint: '在服务商页面登录；没有账号就先注册。' },
    { title: '进入“API keys”页面', hint: '在页面左侧菜单里找名为“API keys”的入口。' },
    { title: '创建一个新密钥', hint: '找名为“创建 API Key”的按钮，名称建议填 OA Agent。' },
    { title: '复制密钥', hint: '点密钥旁边的“复制”。', warn: '密钥通常只显示一次。' },
    { title: '回到本窗口', hint: '复制成功后这里会自动识别。' },
  ] },
})
const MOCK_KEY_GUIDES: Record<string, Pick<LlmPreset, 'key_hosts' | 'key_pattern' | 'key_guide'>> = {
  deepseek: mockGuide('platform.deepseek.com', '^sk-[A-Za-z0-9]{20,64}$', '需要先登录或注册 DeepSeek 开放平台账号。'),
  qwen: mockGuide('.aliyun.com', '^sk-[A-Za-z0-9]{20,64}$', '需要阿里云账号。'),
  openai: mockGuide('platform.openai.com', '^sk-(?:proj-|svcacct-|admin-)[A-Za-z0-9_-]{20,}$|^sk-[A-Za-z0-9]{20,}$', '国内网络可能需要代理。'),
}
const preset = (p: PresetSpec): LlmPreset => ({
  id: p.id, name: p.name, description: p.description, note: p.note, checked_at: '2026-10-22', base_url: p.base_url,
  models: p.models.map(([id, label]) => ({ id, label })), default_model: p.default_model ?? p.models[0]?.[0] ?? '',
  needs_api_key: p.needs_api_key ?? true, vision_models: (p.vision ?? []).map(([id, label]) => ({ id, label })), docs_url: p.docs_url ?? '', ...(p.key_url ? { key_url: p.key_url } : {}), oauth_login: p.oauth ?? false,
  ...(MOCK_KEY_GUIDES[p.id] ?? {}),
})
const LLM_PRESETS: LlmPreset[] = [
  preset({ id: 'deepseek', name: 'DeepSeek', description: '性价比高，中文公文识别效果好', note: '推荐 deepseek-flash（支持工具调用与图片）；deepseek-v4-pro 不支持图片输入。', base_url: 'https://api.deepseek.com', docs_url: 'https://api-docs.deepseek.com', key_url: 'https://platform.deepseek.com/api_keys',
    models: [['deepseek-flash', '推荐 · 支持工具与图片'], ['deepseek-v4-pro', '不支持图片']], vision: [['deepseek-flash', '支持图片']] }),
  preset({ id: 'qwen', name: '通义千问（百炼）', description: '阿里云百炼', note: '国际站或工作空间的 Base URL 因账号而异，请从百炼控制台复制。', base_url: 'https://dashscope.aliyuncs.com/compatible-mode/v1', docs_url: 'https://help.aliyun.com/zh/model-studio/', key_url: 'https://bailian.console.aliyun.com/cn-beijing/model/settings/api-key',
    models: [['qwen3.8-max', '效果最强'], ['qwen3.7-plus', '推荐 · 均衡'], ['qwen3.8-flash', '速度快']], default_model: 'qwen3.7-plus', vision: [['qwen3.7-plus', '视觉均衡'], ['qwen3.8-max', '视觉最强']] }),
  preset({ id: 'openai', name: 'OpenAI', description: 'OpenAI 官方接口', note: '模型更新很快，未内置建议，请点击刷新获取可用模型或直接输入。', base_url: 'https://api.openai.com/v1', docs_url: 'https://platform.openai.com/docs/models', key_url: 'https://platform.openai.com/api-keys', models: [] }),
  preset({ id: 'ollama', name: 'Ollama 本机', description: '本地模型，文档不出本机，无需密钥', note: '需先在本机 ollama pull 对应模型。', base_url: 'http://127.0.0.1:11434/v1', docs_url: 'https://ollama.com/library',
    models: [['qwen3.8:27b', '本地示例']], needs_api_key: false, vision: [['qwen3.8:27b', '本地示例']] }),
  preset({ id: 'openrouter', name: 'OpenRouter', description: '聚合多家模型的网关；支持用 OpenRouter 账号登录授权', note: '演示：登录授权会返回一把示例 Key。', base_url: 'https://openrouter.ai/api/v1', docs_url: 'https://openrouter.ai/docs', key_url: 'https://openrouter.ai/settings/keys', oauth: true, models: [['openrouter/auto', '自动路由']] }),
  preset({ id: 'custom', name: '自定义', description: '任意 OpenAI 兼容服务', note: '填写服务商的 Base URL 与模型名。', base_url: '', models: [], needs_api_key: false }),
]
let llmKey = ''
// 浏览器里默认“全局已配置”以便直接演示 AI 工作台；没有 location（单元测试）时为未配置，保持既有测试的前提
const MOCK_LLM = ((typeof location === 'undefined' ? 'none' : new URLSearchParams(location.search).get('mock_llm')) || 'global') as 'none' | 'global' | 'personal'
let llmConfig: LlmConfig = MOCK_LLM === 'none'
  ? { mode: 'heuristic', preset_id: 'deepseek', base_url: '', model: '', api_key_set: false, api_key_hint: '', vision_model: '', allow_vision: false, state: 'ok', updated_at: null }
  : { mode: 'llm', preset_id: 'deepseek', base_url: 'https://api.deepseek.com', model: 'deepseek-flash', api_key_set: true, api_key_hint: '••••demo', vision_model: '', allow_vision: false, state: 'ok', updated_at: iso(2) }
if (MOCK_LLM !== 'none') llmKey = 'sk-demo-global'
const authCtl: MockAuthCtl = {}
const mockAi = createMockAi({
  role: () => authCtl.currentRole?.() ?? null, globalCfg: () => llmConfig, presets: LLM_PRESETS,
  setGlobalLlm: (c, k) => { llmConfig = c; llmKey = k },
  models: (url) => url.trim().replace(/\/+$/, '') === 'https://api.openai.com/v1' ? ['gpt-4o', 'gpt-4o-mini', 'gpt-4.1'] : (LLM_PRESETS.find((p) => p.base_url.replace(/\/+$/, '') === url.trim().replace(/\/+$/, ''))?.models ?? []).map((m) => m.id),
  audit: (actor, action, target, summary) => { audit.unshift({ id: uid(), timestamp: now(), actor, action, target, summary }) },
}, MOCK_LLM)
const mockDiag = createMockDiag()

const rules: ArchiveRule[] = [
  { id: uid(), name: '默认命名与目录', naming_pattern: '{date}_{department}_{type}_{original}', directory_pattern: '{department}/{year}/{type}', enabled: true },
]

type Ocr = NonNullable<FileRecord['ocr']>
/** 演示 OCR 摘要：多数为 null（原生文本无需 OCR），一个“一般”（增强重试 2 次），一个“差”。 */
const SEED_OCR: (Ocr | null)[] = [
  null, null,
  { used: true, quality: 'fair', mean_confidence: 0.74, profile: 'enhanced', attempts: 2, vision_used: false },
  null, null,
  { used: true, quality: 'poor', mean_confidence: 0.41, profile: 'binarize', attempts: 3, vision_used: false },
]

function seed() {
  const t: ImportJob = { id: uid(), status: 'completed', total_files: 6, processed_files: 6, success_count: 6, failure_count: 0, plan_id: null, created_at: iso(3), updated_at: iso(3) }
  tasks.set(t.id, t)
  const samples = ['2026年度采购合同.docx', '三月薪酬核算表.xlsx', '关于调整作息的通知.pdf', '差旅报销申请.docx', '一季度财务报表.xlsx', '办公用品申请.pdf']
  samples.forEach((n, i) => {
    const d = DEPTS[[2, 1, 3, 0, 0, 3][i]], ty = TYPES[[0, 2, 1, 3, 2, 3][i]]
    const date = `2026-0${(i % 3) + 1}-${10 + i}`
    const id = uid()
    files.set(id, { id, task_id: t.id, source_name: n, source_path: `导入/${n}`, mime_type: mimeOf(n), sha256: sha(), size_bytes: 40000 + i * 13000, detected_department: d, detected_type: ty, detected_date: date, evidence: [`文件名包含“${n.slice(0, 4)}”`, 'OCR/正文抽取：落款部门及日期匹配'], review_required: false, archive_status: 'archived', error: null, ocr: SEED_OCR[i] ?? null, created_at: t.created_at })
    ledger.push({ id: uid(), status: 'archived' as const, original_name: n, canonical_name: `${date}_${d}_${ty}_${n}`, department: d, document_type: ty, document_date: date, archive_path: `归档/${d}/2026/${ty}/${date}_${d}_${ty}_${n}`, sha256: files.get(id)!.sha256, task_id: t.id, decision: 'manual', executed_at: iso(3) })
  })
  audit.push({ id: uid(), timestamp: iso(3), actor: '演示用户', action: 'plan.execute', target: t.id, summary: '执行归档计划，6 个文件' })
}
seed()

/** 一个已完成的全自动任务：3 个已归档（台账标记“全自动”），1 个留待人工审核，可演示“撤销本次全自动归档”。 */
function seedAuto() {
  const model = llmConfig.mode === 'llm' ? llmConfig.model : 'deepseek-flash'
  const t: ImportJob = { id: uid(), status: 'awaiting_review', total_files: 4, processed_files: 4, success_count: 4, failure_count: 0, plan_id: null, instruction: '只处理采购相关文件，按项目名归档；把扫描件单独标记待审核', automation: 'auto', llm_source: 'global', llm_model: model, created_at: iso(0.2), updated_at: iso(0.2) }
  tasks.set(t.id, t)
  const specs: [string, string, string, string, boolean][] = [['采购合同-A项目.docx', '采购部', '合同', '2026-05-12', false], ['供应商报价单.xlsx', '采购部', '报表', '2026-05-20', false], ['验收通知.pdf', '采购部', '通知', '2026-06-01', false], ['扫描件-0618.pdf', '', '', '', true]]
  const archivedItems: ArchivePlanItem[] = [], left: ArchivePlanItem[] = []
  for (const [n, d, ty, date, review] of specs) {
    const id = uid(), name = review ? n : `${date}_${d}_${ty}_${n}`
    const f: FileRecord = { id, task_id: t.id, source_name: n, source_path: `采购/${n}`, mime_type: mimeOf(n), sha256: sha(), size_bytes: 64000, detected_department: review ? null : d, detected_type: review ? null : ty, detected_date: review ? null : date, evidence: [review ? '按任务说明：扫描件标记为待审核' : `文件名：${n}`], review_required: review, archive_status: review ? 'pending' : 'archived', error: null, ocr: null, created_at: t.created_at }
    files.set(id, f)
    const item: ArchivePlanItem = { file_id: id, original_name: n, suggested_name: name, source_path: f.source_path, destination_path: review ? `归档/待审核/${n}` : `归档/${d}/2026/${ty}/${name}`, department: review ? OTHER : f.detected_department, document_type: review ? OTHER : f.detected_type, document_date: f.detected_date, warnings: review ? ['按任务说明标记为待审核', '未能识别部门，已暂填「其他」，请确认或修改'] : [], review_required: review, fallback_fields: review ? ['department', 'document_type'] : [] }
    if (review) left.push(item)
    else {
      archivedItems.push(item)
      ledger.push({ id: uid(), status: 'archived' as const, original_name: n, canonical_name: name, department: d, document_type: ty, document_date: date, archive_path: item.destination_path, sha256: f.sha256, task_id: t.id, decision: 'ai_auto', decision_model: model, executed_at: iso(0.2) })
    }
  }
  const op = uid()
  const plan: ArchivePlan = { id: uid(), task_id: t.id, items: left, status: 'awaiting_review', version: 1, requires_approval: true, approved_by: null, approved_at: null, operation_id: op }
  plans.set(plan.id, plan)
  tasks.set(t.id, { ...t, plan_id: plan.id })
  opStore.set(op, { planId: plan.id, taskId: t.id, items: archivedItems, auto: true })
  runtime.set(t.id, { events: [], timers: [], listeners: new Set(), seq: 0 })
  emit(t.id, { phase: 'planning', event_type: 'tool_call', summary: `classify_documents · 模型 ${model}（按任务说明）` })
  emit(t.id, { phase: 'archiving', event_type: 'archive_completed', summary: '全自动归档完成：已归档 3 个，1 个留待人工审核' })
}

seedAuto()

/** 一个等待审核的任务，含内容无法读取的文件（已加密 / 格式不识别）：它们不是失败项，只依据文件名判断（“仅依据文件名”标记）。 */
function seedNameOnly() {
  const t: ImportJob = { id: uid(), status: 'awaiting_review', total_files: 3, processed_files: 3, success_count: 3, failure_count: 0, plan_id: null, created_at: iso(0.1), updated_at: iso(0.1) }
  tasks.set(t.id, t)
  const items: ArchivePlanItem[] = []
  for (const n of ['采购部_设备采购合同_2026-03-15.docx', '验收单_2026-04-02_加密.pdf', '场地图纸.dwg']) {
    const id = uid()
    const c = classify(n, `混合/${n}`, 48000)
    const f: FileRecord = { id, task_id: t.id, source_name: n, source_path: `混合/${n}`, mime_type: mimeOf(n), sha256: sha(), size_bytes: 48000, detected_department: null, detected_type: null, detected_date: null, evidence: [], review_required: false, archive_status: 'pending', error: null, ocr: null, created_at: t.created_at, ...c } as FileRecord
    files.set(id, f)
    const dept = f.detected_department ?? OTHER, ty = f.detected_type ?? OTHER, date = f.detected_date ?? '未知日期'
    const fb: ('department' | 'document_type')[] = []
    if (!f.detected_department) fb.push('department')
    if (!f.detected_type) fb.push('document_type')
    const nameOnly = f.content?.basis === 'filename_only'
    const sug = `${date}_${dept}_${ty}_${n}`
    items.push({ file_id: id, original_name: n, suggested_name: sug, source_path: f.source_path, destination_path: `归档/${dept}/${date.slice(0, 4)}/${ty}/${sug}`, department: dept, document_type: ty, document_date: f.detected_date,
      warnings: nameOnly ? ['字段置信度低或信息缺失，需人工确认'] : [], review_required: f.review_required || nameOnly || fb.length > 0 || !f.detected_date, fallback_fields: fb, basis: nameOnly ? 'filename_only' : 'content', basis_note: nameOnly ? f.evidence[0] ?? null : null })
  }
  const plan: ArchivePlan = { id: uid(), task_id: t.id, items, status: 'awaiting_review', version: 1, requires_approval: true, approved_by: null, approved_at: null, operation_id: null }
  plans.set(plan.id, plan)
  tasks.set(t.id, { ...t, plan_id: plan.id })
  runtime.set(t.id, { events: [], timers: [], listeners: new Set(), seq: 0 })
  emit(t.id, { phase: 'analyzing', event_type: 'file_classified', summary: '2 个文件内容无法读取，已按文件名处理（需人工审核）' })
}
seedNameOnly()

/** 开发演示：`?mock_tasks=all` 为每种任务状态各建一个静态任务（含计划），用来查看 AgentProgress 的全部形态。 */
function seedAllStatuses() {
  const all: TaskStatus[] = ['queued', 'scanning', 'extracting', 'analyzing', 'planning', 'awaiting_review', 'approved', 'archiving', 'completed', 'partial_failure', 'failed', 'cancelled']
  all.forEach((status, i) => {
    const total = 10
    const processed = { queued: 0, scanning: 2, extracting: 4, analyzing: 6, planning: 10 }[status as string] ?? 10
    const t: ImportJob = { id: uid(), status, total_files: total, processed_files: processed, success_count: status === 'completed' ? total : 0, failure_count: status === 'partial_failure' ? 2 : status === 'failed' ? 3 : 0, plan_id: null, created_at: iso(0.05 + i * 0.01), updated_at: iso(0.05 + i * 0.01) }
    tasks.set(t.id, t)
    runtime.set(t.id, { events: [], timers: [], listeners: new Set(), seq: 0 })
    if (['awaiting_review', 'approved', 'archiving', 'partial_failure'].includes(status)) {
      const items: ArchivePlanItem[] = ['采购合同-演示.docx', '会议通知-演示.pdf'].map((n) => {
        const id = uid()
        files.set(id, { id, task_id: t.id, source_name: n, source_path: `演示/${status}/${n}`, mime_type: mimeOf(n), sha256: sha(), size_bytes: 52000, detected_department: '采购部', detected_type: '合同', detected_date: '2026-06-01', evidence: [`文件名：${n}`], review_required: false, archive_status: 'pending', error: null, ocr: null, created_at: t.created_at })
        return { file_id: id, original_name: n, suggested_name: `2026-06-01_采购部_合同_${n}`, source_path: `演示/${status}/${n}`, destination_path: `归档/采购部/2026/合同/2026-06-01_采购部_合同_${n}`, department: '采购部', document_type: '合同', document_date: '2026-06-01', warnings: [], review_required: false }
      })
      const plan: ArchivePlan = { id: uid(), task_id: t.id, items, status: status === 'awaiting_review' ? 'awaiting_review' : 'approved', version: 1, requires_approval: true, approved_by: status === 'awaiting_review' ? null : '演示用户', approved_at: status === 'awaiting_review' ? null : now(), operation_id: null }
      plans.set(plan.id, plan)
      tasks.set(t.id, { ...t, plan_id: plan.id })
    }
  })
}
if (typeof location !== 'undefined' && new URLSearchParams(location.search).get('mock_tasks') === 'all') seedAllStatuses()

const PHASES: { status: TaskStatus; phase: string; summary: string }[] = [
  { status: 'scanning', phase: 'scanning', summary: '扫描导入文件并计算哈希' },
  { status: 'extracting', phase: 'extracting', summary: '解析文档与 OCR 文本抽取' },
  { status: 'analyzing', phase: 'analyzing', summary: 'Agent 识别部门、类型与日期' },
  { status: 'planning', phase: 'planning', summary: '按规则生成标准文件名与归档路径' },
]

function emit(taskId: string, partial: Omit<AgentEvent, 'id' | 'task_id' | 'timestamp'>) {
  const rt = runtime.get(taskId)!
  const e: AgentEvent = { id: ++rt.seq, task_id: taskId, timestamp: now(), ...partial }
  rt.events.push(e)
  rt.listeners.forEach((l) => l(e))
}

function setStatus(id: string, status: TaskStatus, patch: Partial<ImportJob> = {}) {
  const t = tasks.get(id)!
  tasks.set(id, { ...t, ...patch, status, updated_at: now() })
}

/** 已因大模型错误而失败过的文件：重试后可重试的错误只会成功（401/404 一直失败）。 */
const llmTried = new Set<string>()

/** only：重试时只重新处理这些文件，其余已完成的识别结果保持不变。 */
function simulate(taskId: string, only?: Set<string>) {
  const rt = runtime.get(taskId)!
  const at = (ms: number, fn: () => void) => rt.timers.push(window.setTimeout(fn, ms))
  const allFs = () => [...files.values()].filter((f) => f.task_id === taskId)
  const fs = () => (only ? allFs().filter((f) => only.has(f.id)) : allFs())
  let ms = 400
  PHASES.forEach((p) => {
    at(ms, () => {
      setStatus(taskId, p.status)
      emit(taskId, { phase: p.phase, event_type: 'phase_started', summary: p.summary })
    })
    if (p.status === 'extracting' || p.status === 'analyzing') {
      fs().forEach((f, i) => at(ms + 350 + i * 450, () => {
        const cur = files.get(f.id)!
        if (p.status === 'extracting') {
          if (cur.error) { emit(taskId, { phase: p.phase, event_type: 'file_failed', file_id: f.id, summary: `${f.source_name}：${cur.error}` }); return }
          emit(taskId, { phase: p.phase, event_type: 'tool_call', file_id: f.id, summary: `read_document · ${f.source_name}`, details: { tool: /image|pdf/.test(f.mime_type) ? 'ocr' : 'parse' } })
        } else if (!cur.error) {
          const tk = tasks.get(taskId)!
          const kind = tk.llm_source && tk.llm_source !== 'none' ? faultKindFor(f.source_name, allFs()[0]?.id === f.id) : null
          if (kind && (faultPersists(kind) || !llmTried.has(f.id))) {
            llmTried.add(f.id)
            const le = llmErrorFor(kind)
            files.set(f.id, { ...cur, error: le.message, llm_error: le, review_required: true, detected_department: null, detected_type: null, detected_date: null })
            emit(taskId, { phase: p.phase, event_type: 'provider_error', file_id: f.id, summary: le.message, details: { status: le.status, code: le.code, retryable: le.retryable, attempts: le.attempts, retry_after_seconds: le.retry_after_seconds } })
            return
          }
          if (tk.llm_source && tk.llm_source !== 'none') emit(taskId, { phase: p.phase, event_type: 'tool_call', file_id: f.id, summary: `llm_classify · ${tk.llm_model}（${tk.llm_source === 'personal' ? '个人' : '全局'}）`, details: { tool: 'llm' } })
          emit(taskId, { phase: p.phase, event_type: 'file_classified', file_id: f.id, summary: `${f.source_name} → ${cur.detected_department ?? '待审核'} / ${cur.detected_type ?? '待审核'} / ${cur.detected_date ?? '待审核'}` })
        }
        const t = tasks.get(taskId)!
        if (p.status === 'extracting') setStatus(taskId, t.status, { processed_files: Math.min(t.total_files, t.processed_files + 1) })
      }))
      ms += 350 + fs().length * 450 + 300
    } else ms += 900
  })
  at(ms, () => {
    const items: ArchivePlanItem[] = allFs().filter((f) => !f.error).map((f) => {
      const date = f.detected_date ?? '未知日期'
      // 计划层规则：无法识别的部门/类型暂填「其他」并标记为系统占位（fallback_fields），仍需人工确认；分析结果本身保持 null
      const fb: ('department' | 'document_type')[] = []
      if (!f.detected_department) fb.push('department')
      if (!f.detected_type) fb.push('document_type')
      const dept = f.detected_department ?? OTHER
      const ty = f.detected_type ?? OTHER
      const name = `${date}_${dept}_${ty}_${f.source_name.split('/').pop()}`
      const warnings: string[] = []
      const flagged = !!tasks.get(taskId)?.instruction && /扫描件|待审核/.test(tasks.get(taskId)!.instruction!) && /pdf|image/.test(f.mime_type) && /扫描|\.(png|jpe?g)$/i.test(f.source_name)
      if (flagged) { files.set(f.id, { ...f, review_required: true }); warnings.push('按任务说明标记为待审核') }
      if (f.review_required) warnings.push('字段置信度低，需人工确认')
      if (fb.includes('department')) warnings.push('未能识别部门，已暂填「其他」，请确认或修改')
      if (fb.includes('document_type')) warnings.push('未能识别文件类型，已暂填「其他」，请确认或修改')
      if (ledger.some((l) => l.canonical_name === name)) warnings.push('目标文件名已存在，将自动追加序号')
      const nameOnly = f.content?.basis === 'filename_only'
      return { file_id: f.id, original_name: f.source_name, suggested_name: name, source_path: f.source_path, destination_path: `归档/${dept}/${date.slice(0, 4)}/${ty}/${name}`, department: dept, document_type: ty, document_date: f.detected_date, warnings, review_required: f.review_required || flagged || fb.length > 0 || !f.detected_date || nameOnly, fallback_fields: fb,
        basis: nameOnly ? 'filename_only' : 'content', basis_note: nameOnly ? f.evidence[0] ?? null : null }
    })
    const plan: ArchivePlan = { id: uid(), task_id: taskId, items, status: 'awaiting_review', version: 1, requires_approval: true, approved_by: null, approved_at: null, operation_id: null }
    plans.set(plan.id, plan)
    const failed = allFs().filter((f) => f.error).length
    setStatus(taskId, 'awaiting_review', { plan_id: plan.id, success_count: items.length, failure_count: failed, processed_files: tasks.get(taskId)!.total_files })
    emit(taskId, { phase: 'planning', event_type: 'plan_ready', summary: `归档计划已生成，共 ${items.length} 项，等待审批` })
    if (tasks.get(taskId)?.automation === 'auto') at(1200, () => autoExecute(taskId, plan))
  })
}

/** 全自动：模型直接归档不需要确认的条目；需要确认的留在计划中等待人工审核。 */
function autoExecute(taskId: string, plan: ArchivePlan) {
  const t = tasks.get(taskId)
  if (!t || t.status !== 'awaiting_review') return
  const done = plan.items.filter((i) => !i.review_required)
  const left = plan.items.filter((i) => i.review_required)
  setStatus(taskId, 'archiving')
  for (const it of done) {
    const f = files.get(it.file_id)!
    files.set(f.id, { ...f, archive_status: 'archived' })
    ledger.push({ id: uid(), status: 'archived' as const, original_name: it.original_name, canonical_name: it.suggested_name, department: it.department ?? '', document_type: it.document_type ?? '', document_date: it.document_date ?? '', archive_path: it.destination_path, sha256: f.sha256, task_id: taskId, decision: 'ai_auto', decision_model: t.llm_model, executed_at: now() })
  }
  const op = uid()
  opStore.set(op, { planId: plan.id, taskId, items: structuredClone(done), auto: true })
  plan.operation_id = op
  if (left.length) { plan.items = left; plan.status = 'awaiting_review' } else plan.status = 'executed'
  plan.version += 1
  const failed = tasks.get(taskId)!.failure_count
  setStatus(taskId, left.length ? 'awaiting_review' : failed ? 'partial_failure' : 'completed')
  emit(taskId, { phase: 'archiving', event_type: 'archive_completed', summary: `全自动归档完成：已归档 ${done.length} 个，${left.length} 个留待人工审核（模型 ${t.llm_model}，无人工审批）` })
  audit.unshift({ id: uid(), timestamp: now(), actor: '演示用户', action: 'plan.auto_execute', target: plan.id, summary: `全自动归档 ${done.length} 个文件，模型 ${t.llm_model}` })
}

/** 与后端一致：内容无法读取的文件不是“失败项”，而是只依据文件名判断（恒需人工审核，证据首条说明原因）。 */
function classify(name: string, path: string, size = 1): Partial<FileRecord> {
  let reason: string | null = null
  if (size === 0) reason = 'empty'
  else if (!isReadable(name)) reason = 'unsupported'
  else if (/加密|encrypted/i.test(name)) reason = 'encrypted' // 演示：文件名含“加密/encrypted”视为密码保护
  else if (/损坏|damaged/i.test(name)) reason = 'damaged'
  const full = name + path
  const dept = (/财|报销|发票|预算/.test(full) && DEPTS[0]) || (/人|薪|招聘|考勤/.test(full) && DEPTS[1]) || (/采购|供应|合同/.test(full) && DEPTS[2]) || (/通知|行政|办公/.test(full) && DEPTS[3]) || null
  const type = (/合同|协议/.test(full) && TYPES[0]) || (/通知|公告/.test(full) && TYPES[1]) || (/报表|统计|核算/.test(full) && TYPES[2]) || (/申请|报销/.test(full) && TYPES[3]) || null
  const m = /(20\d{2})[-年._]?(\d{1,2})[-月._]?(\d{1,2})?/.exec(full)
  const date = m ? `${m[1]}-${m[2].padStart(2, '0')}-${(m[3] ?? '01').padStart(2, '0')}` : null
  const evidence = [`文件名：${name}`]
  if (dept) evidence.push(`关键词映射部门：${dept}`)
  if (type) evidence.push(`关键词映射类型：${type}`)
  if (date) evidence.push(`日期来自文件名：${date}`)
  if (reason) {
    return { detected_department: dept, detected_type: type, detected_date: date, evidence: [`内容无法读取（${UNREADABLE_LABEL[reason]}），仅依据文件名判断`, ...evidence], review_required: true, error: null,
      content: { status: 'unreadable', reason, basis: 'filename_only', format_mismatch: null } }
  }
  if (/\.(png|jpe?g)$/i.test(name)) evidence.push('OCR：识别到 3 行文字，置信度 0.86')
  return { detected_department: dept, detected_type: type, detected_date: date, evidence, review_required: !(dept && type && date),
    content: { status: 'ok', reason: null, basis: 'content', format_mismatch: /网页导出|web_export/i.test(name) && /\.xls$/i.test(name) ? 'html' : null } }
}

function paginate<T>(arr: T[], q?: { page?: number; page_size?: number }): Page<T> {
  const page = q?.page ?? 1, size = q?.page_size ?? 20
  return { items: arr.slice((page - 1) * size, page * size), total: arr.length, page, page_size: size }
}
const textHit = (q: string | undefined, ...vals: (string | null)[]) => !q || vals.some((v) => v?.toLowerCase().includes(q.toLowerCase()))

function filterLedger(q?: LedgerQuery) {
  let r = ledger.filter((l) => textHit(q?.q, l.original_name, l.canonical_name, l.department, l.document_type, l.archive_path)
    && (!q?.department || displayCategory(l.department) === displayCategory(q.department)) && (!q?.type || displayCategory(l.document_type) === displayCategory(q.type))
    && (!q?.date_from || l.document_date >= q.date_from) && (!q?.date_to || l.document_date <= q.date_to)
    && (!q?.decision || (l.decision ?? 'manual') === q.decision))
  const key = (q?.sort ?? 'executed_at') as keyof LedgerEntry
  r = [...r].sort((a, b) => String(a[key]).localeCompare(String(b[key])) * (q?.order === 'asc' ? 1 : -1))
  return r
}

const getOr404 = <T,>(m: Map<string, T>, id: string, what: string): T => {
  const v = m.get(id)
  if (!v) throw new ApiError(404, 'NOT_FOUND', `${what}不存在`)
  return v
}

const uploads = createMockUploads((picked, o) => mockApi.createImport(picked, o))

export const mockApi: Api = {
  mode: 'mock',
  capabilities: { retryTask: true, providerStatus: true, rollback: true, webauthn: false },
  ...uploads,

  async readiness() { await delay(150); return { alive: true, ready: true, database: 'ok', worker: 'ok' } },

  async createImport(picked: PickedFile[], opts?: { templateId?: string | null; idempotencyKey?: string; options?: ImportOptions }) {
    await delay(500)
    if (!picked.length) throw new ApiError(400, 'INVALID_ARGUMENT', '未选择文件')
    const resolved = mockAi.ctl.resolveImport(opts?.options) // 与契约一致的服务端规则：可能抛出 403/422
    const id = uid()
    const job: ImportJob = { id, status: 'queued', total_files: picked.length, processed_files: 0, success_count: 0, failure_count: 0, plan_id: null, ...resolved, created_at: now(), updated_at: now() }
    tasks.set(id, job)
    runtime.set(id, { events: [], timers: [], listeners: new Set(), seq: 0 })
    for (const p of picked) {
      const fid = uid()
      files.set(fid, { id: fid, task_id: id, source_name: p.file.name, source_path: p.relativePath, mime_type: p.file.type || mimeOf(p.file.name), sha256: sha(), size_bytes: p.file.size, detected_department: null, detected_type: null, detected_date: null, evidence: [], review_required: false, archive_status: 'pending', error: null, ocr: /\.(png|jpe?g)$/i.test(p.file.name) ? { used: true, quality: 'good', mean_confidence: 0.86, profile: 'default', attempts: 1, vision_used: false } : null, created_at: now(), ...classify(p.file.name, p.relativePath, p.file.size) })
    }
    audit.unshift({ id: uid(), timestamp: now(), actor: '演示用户', action: 'import.create', target: id, summary: `导入 ${picked.length} 个文件` })
    emit(id, { phase: 'queued', event_type: 'task_created', summary: `任务已创建，共 ${picked.length} 个文件${resolved.automation === 'auto' ? '（全自动模式）' : ''}` })
    if (resolved.instruction) emit(id, { phase: 'queued', event_type: 'phase_started', summary: `已读取任务说明：${resolved.instruction.slice(0, 60)}` })
    simulate(id)
    return job
  },

  async listTasks(q) { await delay(); return paginate([...tasks.values()].filter((t) => !q?.status || t.status === q.status).sort((a, b) => b.created_at.localeCompare(a.created_at)), q) },
  async getTask(id) { await delay(120); return getOr404(tasks, id, '任务') },
  async cancelTask(id) {
    await delay(200)
    const t = getOr404(tasks, id, '任务')
    if (['completed', 'failed', 'cancelled'].includes(t.status)) throw new ApiError(409, 'TASK_NOT_CANCELLABLE', '任务已结束，无法取消')
    runtime.get(id)?.timers.forEach(clearTimeout)
    setStatus(id, 'cancelled')
    emit(id, { phase: 'cancelled', event_type: 'task_cancelled', summary: '任务已被用户取消' })
    return tasks.get(id)!
  },
  async retryTask(id) {
    await delay(200)
    const t = getOr404(tasks, id, '任务')
    // 与后端一致：重复点击（任务已在重新排队/运行）返回当前状态而不是报错
    if (['queued', 'scanning', 'extracting', 'analyzing', 'planning'].includes(t.status)) return t
    const failedFiles = [...files.values()].filter((f) => f.task_id === id && f.error && f.archive_status !== 'archived')
    const retryable = ['failed', 'cancelled', 'partial_failure'].includes(t.status) || (t.status === 'awaiting_review' && failedFiles.length > 0)
    if (!retryable) throw new ApiError(409, 'TASK_NOT_RETRYABLE', '仅失败或已取消的任务可重试')
    // 只重新处理失败的文件，已成功的识别结果保留；旧计划作废后重新生成
    const only = new Set(failedFiles.map((f) => f.id))
    if (!only.size) for (const f of files.values()) if (f.task_id === id && f.archive_status !== 'archived') only.add(f.id)
    for (const fid of only) { const f = files.get(fid)!; files.set(fid, { ...f, error: null, llm_error: null, review_required: false, archive_status: 'pending', ...classify(f.source_name, f.source_path, f.size_bytes) }) }
    if (t.plan_id) plans.delete(t.plan_id)
    setStatus(id, 'queued', { plan_id: null, processed_files: t.total_files - only.size, success_count: t.total_files - only.size, failure_count: 0 })
    audit.unshift({ id: uid(), timestamp: now(), actor: '演示用户', action: 'task.retry', target: id, summary: '重新排队任务' })
    emit(id, { phase: 'queued', event_type: 'task_retry', summary: `任务已重新排队，仅重新识别 ${only.size} 个失败的文件` })
    simulate(id, only)
    return tasks.get(id)!
  },

  subscribeEvents(taskId, onEvent, onState) {
    const rt = runtime.get(taskId)
    if (!rt) { onState('closed'); return () => {} }
    onState('open')
    rt.events.forEach(onEvent)
    rt.listeners.add(onEvent)
    return () => { rt.listeners.delete(onEvent); onState('closed') }
  },

  async listFiles(q?: FileQuery) {
    await delay()
    const r = [...files.values()].filter((f) => textHit(q?.q, f.source_name, f.source_path)
      && (!q?.department || f.detected_department === q.department) && (!q?.type || f.detected_type === q.type)
      && (!q?.archive_status || f.archive_status === q.archive_status)
      && (q?.review_required === undefined || f.review_required === q.review_required)
      && (!q?.date_from || (f.detected_date ?? '') >= q.date_from) && (!q?.date_to || (f.detected_date ?? '9') <= q.date_to))
      .sort((a, b) => b.created_at.localeCompare(a.created_at))
    return paginate(r, { page_size: 200, ...q })
  },
  async listTaskFiles(taskId) { await delay(150); return [...files.values()].filter((f) => f.task_id === taskId) },
  async getFile(id) { await delay(100); return getOr404(files, id, '文件') },

  async getPlan(id) { await delay(150); return structuredClone(getOr404(plans, id, '归档计划')) },
  async patchPlan(id, version, patches) {
    await delay(200)
    const p = getOr404(plans, id, '归档计划')
    if (p.version !== version) throw new ApiError(409, 'VERSION_CONFLICT', '计划已被他人修改，请刷新后重试')
    if (p.status !== 'awaiting_review' && p.status !== 'draft') throw new ApiError(409, 'PLAN_LOCKED', '计划已批准或执行，不能再修改；请先退回')
    for (const patch of patches) {
      const it = p.items.find((i) => i.file_id === patch.file_id)
      if (!it) continue
      for (const k of ['department', 'document_type', 'document_date'] as const) {
        const v = patch[k]
        if (v === undefined) continue
        const t = (v ?? '').trim()
        if (k !== 'document_date' && t && (/[\\/:*?"<>|]/.test(t) || Array.from(t).length > 60 || !t.replace(/[. ]/g, ''))) throw new ApiError(422, 'INVALID_ARGUMENT', '部门/类型不能包含 \\ / : * ? " < > | 或超过 60 个字符')
        it[k] = t || null
        // 人工修订过的字段即视为已确认（即使仍填「其他」）；清空则重新暂填「其他」
        if (k !== 'document_date') it.fallback_fields = (it.fallback_fields ?? []).filter((x) => x !== k)
      }
      if (patch.suggested_name) it.suggested_name = patch.suggested_name
      if (patch.destination_path) it.destination_path = patch.destination_path
      it.fallback_fields = it.fallback_fields ?? []
      if (!it.department) { it.department = OTHER; it.fallback_fields.push('department') }
      if (!it.document_type) { it.document_type = OTHER; it.fallback_fields.push('document_type') }
      it.warnings = it.warnings.filter((w) => !w.includes('置信度') && !w.includes('已暂填'))
      if (it.fallback_fields.includes('department')) it.warnings.push('未能识别部门，已暂填「其他」，请确认或修改')
      if (it.fallback_fields.includes('document_type')) it.warnings.push('未能识别文件类型，已暂填「其他」，请确认或修改')
      it.review_required = !it.document_date || it.fallback_fields.length > 0
      if (!patch.suggested_name || !patch.destination_path) {
        const date = it.document_date ?? '未知日期', dept = it.department ?? OTHER, ty = it.document_type ?? OTHER
        const name = patch.suggested_name ?? `${date}_${dept}_${ty}_${it.original_name.split('/').pop()}`
        it.suggested_name = name
        it.destination_path = patch.destination_path ?? `归档/${dept}/${date.slice(0, 4)}/${ty}/${name}`
      }
    }
    p.version += 1
    return structuredClone(p)
  },
  async approvePlan(id, version) {
    await delay(300)
    const p = getOr404(plans, id, '归档计划')
    if (p.version !== version) throw new ApiError(409, 'VERSION_CONFLICT', '计划已被他人修改，请刷新后重试')
    if (p.items.some((i) => i.review_required)) throw new ApiError(409, 'REVIEW_REQUIRED', '仍有待人工确认的条目，请先修订')
    p.status = 'approved'; p.approved_by = '演示用户'; p.approved_at = now(); p.version += 1
    setStatus(p.task_id, 'approved')
    audit.unshift({ id: uid(), timestamp: now(), actor: '演示用户', action: 'plan.approve', target: id, summary: `批准归档计划 v${p.version}` })
    return structuredClone(p)
  },
  async executePlan(id, key) {
    await delay(900)
    const p = getOr404(plans, id, '归档计划')
    if (p.status === 'executed') return structuredClone(p) // 幂等
    if (p.status !== 'approved') throw new ApiError(409, 'NOT_APPROVED', '计划尚未批准，不能执行')
    void key
    setStatus(p.task_id, 'archiving')
    for (const it of p.items) {
      const f = files.get(it.file_id)!
      files.set(f.id, { ...f, archive_status: 'archived', detected_department: it.department, detected_type: it.document_type, detected_date: it.document_date, review_required: false })
      ledger.push({ id: uid(), status: 'archived' as const, original_name: it.original_name, canonical_name: it.suggested_name, department: it.department ?? '', document_type: it.document_type ?? '', document_date: it.document_date ?? '', archive_path: it.destination_path, sha256: f.sha256, task_id: p.task_id, decision: 'manual', executed_at: now() })
    }
    p.status = 'executed'
    p.operation_id = uid()
    opStore.set(p.operation_id, { planId: p.id, taskId: p.task_id, items: structuredClone(p.items), auto: false })
    const failed = tasks.get(p.task_id)!.failure_count
    setStatus(p.task_id, failed ? 'partial_failure' : 'completed')
    emit(p.task_id, { phase: 'archiving', event_type: 'archive_completed', summary: `已归档 ${p.items.length} 个文件并写入台账` })
    audit.unshift({ id: uid(), timestamp: now(), actor: '演示用户', action: 'plan.execute', target: id, summary: `执行归档，${p.items.length} 个文件` })
    return structuredClone(p)
  },

  async rollbackOperation(opId) {
    await delay(500)
    const op = opStore.get(opId)
    const p = op ? plans.get(op.planId) : undefined
    if (!op || !p || p.operation_id !== opId) throw new ApiError(404, "NOT_FOUND", "归档操作不存在或已恢复")
    for (let i = ledger.length - 1; i >= 0; i--) if (op.items.some((it) => it.suggested_name === ledger[i].canonical_name && ledger[i].task_id === op.taskId)) ledger.splice(i, 1)
    for (const it of op.items) { const f = files.get(it.file_id); if (f) files.set(f.id, { ...f, archive_status: "pending" }) }
    // 全自动：撤销后条目回到计划，等待人工审核；人工执行：回到“已批准”
    if (op.auto) { p.items = [...p.items, ...op.items]; p.status = "awaiting_review" } else p.status = "approved"
    p.operation_id = null; p.version += 1
    opStore.delete(opId)
    setStatus(p.task_id, op.auto ? "awaiting_review" : "approved")
    audit.unshift({ id: uid(), timestamp: now(), actor: "演示用户", action: "operation.rollback", target: opId, summary: op.auto ? "撤销全自动归档" : "恢复归档操作" })
  },

  async getClassificationOptions(taskId) {
    await delay(80)
    const build = (builtin: string[], ledgerVals: string[], planVals: string[]) => {
      const seen = new Map<string, { value: string; source: 'builtin' | 'ledger' | 'plan'; count: number }>()
      const add = (v: string | null | undefined, source: 'builtin' | 'ledger' | 'plan', n: number) => {
        const t = (v ?? '').normalize('NFC').trim()
        if (!t || categoryKey(t) === categoryKey(OTHER) || t === '待审核') return
        const cur = seen.get(categoryKey(t))
        if (cur) cur.count += n; else seen.set(categoryKey(t), { value: t, source, count: n })
      }
      builtin.forEach((v) => add(v, 'builtin', 0))
      const tally = (xs: string[]) => { const m = new Map<string, number>(); xs.forEach((x) => m.set(x, (m.get(x) ?? 0) + 1)); return [...m].sort((a, b) => b[1] - a[1]) }
      tally(ledgerVals).forEach(([v, n]) => add(v, 'ledger', n))
      tally(planVals).forEach(([v, n]) => add(v, 'plan', n))
      return [...seen.values()].slice(0, 199).concat([{ value: OTHER, source: 'reserved' as never, count: 0 }])
    }
    const planItems = taskId ? [...plans.values()].filter((p) => p.task_id === taskId).flatMap((p) => p.items) : []
    return {
      departments: build(['财务部', '人事部', '采购部', '行政部'], ledger.map((l) => l.department), planItems.map((i) => i.department ?? '')),
      types: build(['申请', '通知', '公告', '合同', '报告', '发票', '报表', '会议纪要', '制度', '方案', '请示', '批复', '清单'], ledger.map((l) => l.document_type), planItems.map((i) => i.document_type ?? '')),
      reserved: OTHER,
    }
  },
  async listLedger(q) { await delay(); return paginate(filterLedger(q).map((l) => ({ ...l, department: displayCategory(l.department), document_type: displayCategory(l.document_type) })), q) },
  async exportLedger(q) {
    await delay(400)
    const rows = filterLedger({ ...q, page: undefined }).map((l) => ({ ...l, department: displayCategory(l.department), document_type: displayCategory(l.document_type) }))
    const head = ['原文件名', '规范文件名', '部门', '类型', '日期', '归档路径', 'SHA-256', '任务ID', '决策方式', '执行时间']
    const esc = (s: string) => `"${s.replace(/"/g, '""')}"`
    const csv = '﻿' + [head, ...rows.map((r) => [r.original_name, r.canonical_name, r.department, r.document_type, r.document_date, r.archive_path, r.sha256, r.task_id, r.decision === 'ai_auto' ? `全自动·${r.decision_model ?? ''}` : '人工审核', r.executed_at])].map((r) => r.map(esc).join(',')).join('\r\n')
    // 演示模式只能给 CSV；真实后端返回 .xlsx
    return { blob: new Blob([csv], { type: 'text/csv;charset=utf-8' }), filename: '归档台账（演示）.csv' }
  },

  async listTemplates() { await delay(); return structuredClone(templates).sort((a, b) => a.priority - b.priority) },
  async createTemplate(t) { await delay(); const n: OATemplate = { ...t, id: uid(), created_at: now() }; templates.push(n); return n },
  async updateTemplate(id, t) { await delay(); const cur = getOr404(new Map(templates.map((x) => [x.id, x])), id, '模板'); Object.assign(cur, t); return structuredClone(cur) },
  async deleteTemplate(id) { await delay(); const i = templates.findIndex((x) => x.id === id); if (i < 0) throw new ApiError(404, 'NOT_FOUND', '模板不存在'); templates.splice(i, 1) },
  async testTemplate(id, sample) {
    await delay(700)
    const t = templates.find((x) => x.id === id)
    if (!t) throw new ApiError(404, 'NOT_FOUND', '模板不存在')
    const hit = t.match_rules.filename_keywords.find((k) => sample.name.includes(k))
    const c = classify(sample.name, '')
    return { matched: !!hit, department: hit ? c.detected_department ?? null : null, document_type: hit ? c.detected_type ?? null : null, document_date: hit ? c.detected_date ?? null : null, evidence: hit ? [`文件名命中关键词“${hit}”`, ...(c.evidence ?? [])] : ['未命中任何文件名关键词'] }
  },
  async listRules() { await delay(); return structuredClone(rules) },
  async createRule(r) { await delay(); const n = { ...r, id: uid() }; rules.push(n); return n },
  async listAuditLogs(q) { await delay(); return paginate(audit, q) },

  async proposeCorrection(fileId, instruction) {
    await delay(900)
    const f = getOr404(files, fileId, '文件')
    const dept = DEPTS.find((d) => instruction.includes(d.replace('部', ''))) ?? null
    const ty = TYPES.find((t) => instruction.includes(t)) ?? null
    const date = /(20\d{2})[-年/.](\d{1,2})[-月/.](\d{1,2})/.exec(instruction)
    const changes: CorrectionProposal['changes'] = []
    if (dept && dept !== f.detected_department) changes.push({ field: 'department', label: '部门', before: f.detected_department, after: dept })
    if (ty && ty !== f.detected_type) changes.push({ field: 'document_type', label: '文件类型', before: f.detected_type, after: ty })
    if (date) changes.push({ field: 'document_date', label: '日期', before: f.detected_date, after: `${date[1]}-${date[2].padStart(2, '0')}-${date[3].padStart(2, '0')}` })
    if (!changes.length) throw new ApiError(422, 'UNDERSTAND_FAILED', '未能从描述中识别出可修改的字段，请说明要改成的部门、类型或日期')
    const p: CorrectionProposal = { id: uid(), file_id: fileId, instruction, status: 'proposed', changes, impact: { affected_files: 1, scope: 'single_file', rule_changes: false } }
    pendingCorrections.set(p.id, p)
    return p
  },
  async approveCorrection(id) {
    await delay(500)
    const p = getOr404(pendingCorrections, id, '修改建议')
    const f = files.get(p.file_id)!
    const next = { ...f, review_required: false }
    for (const c of p.changes) {
      if (c.field === 'department') next.detected_department = c.after
      if (c.field === 'document_type') next.detected_type = c.after
      if (c.field === 'document_date') next.detected_date = c.after
    }
    files.set(f.id, next)
    p.status = 'approved'
    audit.unshift({ id: uid(), timestamp: now(), actor: '演示用户', action: 'correction.approve', target: f.id, summary: p.changes.map((c) => `${c.label}→${c.after}`).join('，') })
    return p
  },
  async providerStatus() { await delay(); return [{ name: '演示 Provider', model: 'demo-model', state: 'ok' as const }] },
  async listLlmPresets() { await delay(150); return structuredClone(LLM_PRESETS) },
  async getLlmConfig() { await delay(150); return { ...llmConfig } },
  async saveLlmConfig(input) {
    await delay(300)
    if (input.mode === 'heuristic') {
      llmConfig = { ...llmConfig, mode: 'heuristic', state: 'ok', updated_at: now() }
      return { ...llmConfig }
    }
    const base_url = (input.base_url ?? '').trim()
    const model = (input.model ?? '').trim()
    const urlErr = validateBaseUrl(base_url)
    if (urlErr) throw new ApiError(422, 'VALIDATION_FAILED', urlErr)
    if (!model) throw new ApiError(422, 'VALIDATION_FAILED', '模型名称必填')
    const preset_id = input.preset_id || 'custom'
    const preset = LLM_PRESETS.find((p) => p.id === preset_id)
    const typed = input.api_key?.trim()
    const kept = llmConfig.api_key_set && !input.clear_api_key ? llmKey : ''
    const key = typed || kept
    if (preset?.needs_api_key && !key) throw new ApiError(422, 'VALIDATION_FAILED', '该服务需要 API Key')
    llmKey = key
    llmConfig = { mode: 'llm', preset_id, base_url, model, api_key_set: !!key, api_key_hint: keyHint(key), ...(input.api_key?.trim() && input.key_label?.trim() ? { key_label: input.key_label.trim().slice(0, 80) } : {}), vision_model: (input.vision_model ?? '').trim(), allow_vision: !!input.allow_vision, state: 'ok', updated_at: now() }
    audit.unshift({ id: uid(), timestamp: now(), actor: '演示用户', action: 'llm.config.update', target: preset_id, summary: `识别引擎切换为 ${model}` })
    return { ...llmConfig }
  },
  async testLlm(input) {
    await delay(600)
    const key = input.api_key?.trim() || llmKey
    const fault = mockTestFault(key)
    if (fault) return fault
    const vm = input.vision_model?.trim()
    return { ok: true, latency_ms: 600, message: '连接正常', tool_calling: !/reasoner/i.test(input.model), vision_ok: vm ? !/bad/i.test(vm) : null }
  },
  async listLlmModels(input) {
    await delay(400)
    mockModelsGuard(input.api_key?.trim() || llmKey)
    const url = input.base_url.trim().replace(/\/+$/, '')
    if (url === 'https://api.openai.com/v1') return ['gpt-4o', 'gpt-4o-mini', 'gpt-4.1']
    return (LLM_PRESETS.find((p) => p.base_url.replace(/\/+$/, '') === url)?.models ?? []).map((m) => m.id)
  },

  async listFolder(path) { await delay(150); return mockFs.list(path) },
  contentUrl() { return '#' },
  async previewFile(id) { await delay(250); const f = getOr404(files, id, '文件'); return mockPreviewOf(f.source_name, f.size_bytes) },
  async previewArchived(path) {
    await delay(250)
    const entry = ledger.find((l) => l.archive_path === path)
    if (!path || (!entry && !SUPPORTED_PREVIEW.test(path))) throw new ApiError(404, 'NOT_FOUND', '文件不存在')
    return mockPreviewOf(basename(path), 52000)
  },
  previewPageUrl(source, page, maxSide) {
    const name = source.kind === 'file' ? files.get(source.id)?.source_name ?? '文件' : basename(source.path)
    return mockPageUrl(name, page, maxSide)
  },
  async createFolder(path) { await delay(150); return mockFs.mkdir(path) },
  async moveEntry(from, to, opts) { await delay(200); return mockFs.move(from, to, opts) },
  async bulkMove(paths, targetDir, opts) { await delay(250); return mockFs.bulkMove(paths, targetDir, opts) },
  async bulkTrash(paths) { await delay(250); return mockFs.bulkTrash(paths) },
  async verifyFiles(paths) { await delay(400); return mockFs.verify(paths) },
  downloadUrl() { return '#' },
  async downloadSelection() { throw new ApiError(501, 'NOT_IMPLEMENTED', '演示模式无法下载文件') },
  async trashEntry(path) { await delay(200); return mockFs.trashEntry(path) },
  async listTrash() { await delay(150); return mockFs.listTrash() },
  async restoreTrash(id, onConflict) { await delay(200); return mockFs.restore(id, onConflict) },
  async revealInExplorer() { await delay(100); throw new ApiError(501, 'NOT_IMPLEMENTED', '演示模式不支持在资源管理器中显示') },

  listStorageKinds: () => storage.listKinds(),
  listStorageConnections: () => storage.list(),
  getStorageConnection: (id) => storage.getConn(id),
  createStorageConnection: (input) => storage.create(input),
  updateStorageConnection: (id, patch) => storage.update(id, patch),
  deleteStorageConnection: (id) => storage.remove(id),
  testStorageDraft: (input) => storage.testDraft(input),
  testStorageConnection: (id, opts) => storage.testConn(id, opts),
  browseStorage: (id, path) => storage.browse(id, path),
  replicateToStorage: (id, opts) => storage.replicate(id, opts),
  listReplications: (q) => storage.listReps(q),
  retryReplication: (id) => storage.retry(id),
  createRemoteImport: (connectionId, paths, opts) => storage.remoteImport(connectionId, paths, opts),
  ...mockAi.api,
  ...mockDiag.api,
  ...createMockAuth((actor, action, target, summary) => { audit.unshift({ id: uid(), timestamp: now(), actor, action, target, summary }) }, authCtl),
}
const SUPPORTED_PREVIEW = /\.[A-Za-z0-9]{1,10}$/ // 演示：任何带扩展名的归档路径都能得到预览应答（不可读格式返回“暂不支持预览”）
const mockPreviewOf = mockPreview
const pendingCorrections = new Map<string, CorrectionProposal>()
