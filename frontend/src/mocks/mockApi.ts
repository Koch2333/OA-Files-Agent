/**
 * 演示数据实现。仅在 VITE_USE_MOCK=true 或设置页开启“演示数据”时启用，
 * 界面会常驻“演示数据”标识。状态机语义与契约一致：审批与执行分离，未批准不可执行。
 */
import {
  ApiError, type AgentEvent, type Api, type ArchivePlan, type ArchivePlanItem, type ArchiveRule, type AuditLog,
  type CorrectionProposal, type FileQuery, type FileRecord, type ImportJob, type LedgerEntry, type LedgerQuery,
  type OATemplate, type Page, type PickedFile, type TaskStatus,
} from '../api/types'

const delay = (ms = 250) => new Promise((r) => setTimeout(r, ms))
const uid = () => crypto.randomUUID()
const now = () => new Date().toISOString()
const iso = (daysAgo: number) => new Date(Date.now() - daysAgo * 864e5).toISOString()

const DEPTS = ['财务部', '人事部', '采购部', '行政部']
const TYPES = ['合同', '通知', '报表', '申请']

function sha(): string { return Array.from({ length: 64 }, () => '0123456789abcdef'[Math.floor(Math.random() * 16)]).join('') }
function mimeOf(name: string) {
  const ext = name.split('.').pop()?.toLowerCase() ?? ''
  return ({ docx: 'application/vnd.openxmlformats-officedocument.wordprocessingml.document', xlsx: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet', pdf: 'application/pdf', png: 'image/png', jpg: 'image/jpeg', jpeg: 'image/jpeg' } as Record<string, string>)[ext] ?? 'application/octet-stream'
}
const SUPPORTED = /\.(docx?|xlsx?|pdf|png|jpe?g)$/i

interface TaskRuntime { events: AgentEvent[]; timers: number[]; listeners: Set<(e: AgentEvent) => void>; seq: number }

const tasks = new Map<string, ImportJob>()
const files = new Map<string, FileRecord>()
const plans = new Map<string, ArchivePlan>()
const ledger: LedgerEntry[] = []
const runtime = new Map<string, TaskRuntime>()
const audit: AuditLog[] = []
const templates: OATemplate[] = [
  { id: uid(), name: '集团通用 OA 模板', enabled: true, priority: 10, created_at: iso(30), match_rules: { filename_keywords: ['通知', '合同', '报表'], text_keywords: ['文号', '签发'] }, field_mappings: { department: '发文部门', document_type: '文种', date_source: 'ocr_text' } },
  { id: uid(), name: '财务报销单模板', enabled: false, priority: 20, created_at: iso(12), match_rules: { filename_keywords: ['报销', '发票'], text_keywords: ['报销人'] }, field_mappings: { department: '报销部门', document_type: '申请', date_source: 'filename' } },
]
const rules: ArchiveRule[] = [
  { id: uid(), name: '默认命名与目录', naming_pattern: '{date}_{department}_{type}_{original}', directory_pattern: '{department}/{year}/{type}', enabled: true },
]

function seed() {
  const t: ImportJob = { id: uid(), status: 'completed', total_files: 6, processed_files: 6, success_count: 6, failure_count: 0, plan_id: null, created_at: iso(3), updated_at: iso(3) }
  tasks.set(t.id, t)
  const samples = ['2026年度采购合同.docx', '三月薪酬核算表.xlsx', '关于调整作息的通知.pdf', '差旅报销申请.docx', '一季度财务报表.xlsx', '办公用品申请.pdf']
  samples.forEach((n, i) => {
    const d = DEPTS[[2, 1, 3, 0, 0, 3][i]], ty = TYPES[[0, 2, 1, 3, 2, 3][i]]
    const date = `2026-0${(i % 3) + 1}-${10 + i}`
    const id = uid()
    files.set(id, { id, task_id: t.id, source_name: n, source_path: `导入/${n}`, mime_type: mimeOf(n), sha256: sha(), size_bytes: 40000 + i * 13000, detected_department: d, detected_type: ty, detected_date: date, evidence: [`文件名包含“${n.slice(0, 4)}”`, 'OCR/正文抽取：落款部门及日期匹配'], review_required: false, archive_status: 'archived', error: null, created_at: t.created_at })
    ledger.push({ id: uid(), status: 'archived' as const, original_name: n, canonical_name: `${date}_${d}_${ty}_${n}`, department: d, document_type: ty, document_date: date, archive_path: `归档/${d}/2026/${ty}/${date}_${d}_${ty}_${n}`, sha256: files.get(id)!.sha256, task_id: t.id, executed_at: iso(3) })
  })
  audit.push({ id: uid(), timestamp: iso(3), actor: '演示用户', action: 'plan.execute', target: t.id, summary: '执行归档计划，6 个文件' })
}
seed()

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

function simulate(taskId: string) {
  const rt = runtime.get(taskId)!
  const at = (ms: number, fn: () => void) => rt.timers.push(window.setTimeout(fn, ms))
  const fs = () => [...files.values()].filter((f) => f.task_id === taskId)
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
          emit(taskId, { phase: p.phase, event_type: 'file_classified', file_id: f.id, summary: `${f.source_name} → ${cur.detected_department ?? '待审核'} / ${cur.detected_type ?? '待审核'} / ${cur.detected_date ?? '待审核'}` })
        }
        const t = tasks.get(taskId)!
        if (p.status === 'extracting') setStatus(taskId, t.status, { processed_files: Math.min(t.total_files, t.processed_files + 1) })
      }))
      ms += 350 + fs().length * 450 + 300
    } else ms += 900
  })
  at(ms, () => {
    const items: ArchivePlanItem[] = fs().filter((f) => !f.error).map((f) => {
      const date = f.detected_date ?? '未知日期'
      const dept = f.detected_department ?? '待审核'
      const ty = f.detected_type ?? '待审核'
      const name = `${date}_${dept}_${ty}_${f.source_name.split('/').pop()}`
      const warnings: string[] = []
      if (f.review_required) warnings.push('字段置信度低，需人工确认')
      if (ledger.some((l) => l.canonical_name === name)) warnings.push('目标文件名已存在，将自动追加序号')
      return { file_id: f.id, original_name: f.source_name, suggested_name: name, source_path: f.source_path, destination_path: `归档/${dept}/${date.slice(0, 4)}/${ty}/${name}`, department: f.detected_department, document_type: f.detected_type, document_date: f.detected_date, warnings, review_required: f.review_required }
    })
    const plan: ArchivePlan = { id: uid(), task_id: taskId, items, status: 'awaiting_review', version: 1, requires_approval: true, approved_by: null, approved_at: null, operation_id: null }
    plans.set(plan.id, plan)
    const failed = fs().filter((f) => f.error).length
    setStatus(taskId, 'awaiting_review', { plan_id: plan.id, success_count: items.length, failure_count: failed, processed_files: tasks.get(taskId)!.total_files })
    emit(taskId, { phase: 'planning', event_type: 'plan_ready', summary: `归档计划已生成，共 ${items.length} 项，等待审批` })
  })
}

function classify(name: string, path: string): Partial<FileRecord> {
  if (!SUPPORTED.test(name)) return { error: `不支持的文件格式：.${name.split('.').pop()}`, detected_department: null, detected_type: null, detected_date: null, review_required: true, archive_status: 'failed' }
  const full = name + path
  const dept = (/财|报销|发票|预算/.test(full) && DEPTS[0]) || (/人|薪|招聘|考勤/.test(full) && DEPTS[1]) || (/采购|供应|合同/.test(full) && DEPTS[2]) || (/通知|行政|办公/.test(full) && DEPTS[3]) || null
  const type = (/合同|协议/.test(full) && TYPES[0]) || (/通知|公告/.test(full) && TYPES[1]) || (/报表|统计|核算/.test(full) && TYPES[2]) || (/申请|报销/.test(full) && TYPES[3]) || null
  const m = /(20\d{2})[-年._]?(\d{1,2})[-月._]?(\d{1,2})?/.exec(full)
  const date = m ? `${m[1]}-${m[2].padStart(2, '0')}-${(m[3] ?? '01').padStart(2, '0')}` : null
  const evidence = [`文件名：${name}`]
  if (dept) evidence.push(`关键词映射部门：${dept}`)
  if (type) evidence.push(`关键词映射类型：${type}`)
  if (date) evidence.push(`日期来自文件名：${date}`)
  if (/\.(png|jpe?g)$/i.test(name)) evidence.push('OCR：识别到 3 行文字，置信度 0.86')
  return { detected_department: dept, detected_type: type, detected_date: date, evidence, review_required: !(dept && type && date) }
}

function paginate<T>(arr: T[], q?: { page?: number; page_size?: number }): Page<T> {
  const page = q?.page ?? 1, size = q?.page_size ?? 20
  return { items: arr.slice((page - 1) * size, page * size), total: arr.length, page, page_size: size }
}
const textHit = (q: string | undefined, ...vals: (string | null)[]) => !q || vals.some((v) => v?.toLowerCase().includes(q.toLowerCase()))

function filterLedger(q?: LedgerQuery) {
  let r = ledger.filter((l) => textHit(q?.q, l.original_name, l.canonical_name, l.department, l.document_type, l.archive_path)
    && (!q?.department || l.department === q.department) && (!q?.type || l.document_type === q.type)
    && (!q?.date_from || l.document_date >= q.date_from) && (!q?.date_to || l.document_date <= q.date_to))
  const key = (q?.sort ?? 'executed_at') as keyof LedgerEntry
  r = [...r].sort((a, b) => String(a[key]).localeCompare(String(b[key])) * (q?.order === 'asc' ? 1 : -1))
  return r
}

const getOr404 = <T,>(m: Map<string, T>, id: string, what: string): T => {
  const v = m.get(id)
  if (!v) throw new ApiError(404, 'NOT_FOUND', `${what}不存在`)
  return v
}

export const mockApi: Api = {
  mode: 'mock',
  capabilities: { retryTask: true, providerStatus: true, rollback: true },

  async readiness() { await delay(150); return { alive: true, ready: true, database: 'ok', worker: 'ok' } },

  async createImport(picked: PickedFile[]) {
    await delay(500)
    if (!picked.length) throw new ApiError(400, 'INVALID_ARGUMENT', '未选择文件')
    const id = uid()
    const job: ImportJob = { id, status: 'queued', total_files: picked.length, processed_files: 0, success_count: 0, failure_count: 0, plan_id: null, created_at: now(), updated_at: now() }
    tasks.set(id, job)
    runtime.set(id, { events: [], timers: [], listeners: new Set(), seq: 0 })
    for (const p of picked) {
      const fid = uid()
      files.set(fid, { id: fid, task_id: id, source_name: p.file.name, source_path: p.relativePath, mime_type: p.file.type || mimeOf(p.file.name), sha256: sha(), size_bytes: p.file.size, detected_department: null, detected_type: null, detected_date: null, evidence: [], review_required: false, archive_status: 'pending', error: null, created_at: now(), ...classify(p.file.name, p.relativePath) })
    }
    audit.unshift({ id: uid(), timestamp: now(), actor: '演示用户', action: 'import.create', target: id, summary: `导入 ${picked.length} 个文件` })
    emit(id, { phase: 'queued', event_type: 'task_created', summary: `任务已创建，共 ${picked.length} 个文件` })
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
    if (!['failed', 'cancelled', 'partial_failure'].includes(t.status)) throw new ApiError(409, 'TASK_NOT_RETRYABLE', '仅失败或已取消的任务可重试')
    setStatus(id, 'queued', { processed_files: 0, success_count: 0, failure_count: 0 })
    emit(id, { phase: 'queued', event_type: 'task_retry', summary: '任务已重新排队' })
    simulate(id)
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
      Object.assign(it, patch)
      it.review_required = false
      it.warnings = it.warnings.filter((w) => !w.includes('置信度'))
      if (!patch.suggested_name || !patch.destination_path) {
        const date = it.document_date ?? '未知日期', dept = it.department ?? '待审核', ty = it.document_type ?? '待审核'
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
      ledger.push({ id: uid(), status: 'archived' as const, original_name: it.original_name, canonical_name: it.suggested_name, department: it.department ?? '', document_type: it.document_type ?? '', document_date: it.document_date ?? '', archive_path: it.destination_path, sha256: f.sha256, task_id: p.task_id, executed_at: now() })
    }
    p.status = 'executed'
    p.operation_id = uid()
    opPlans.set(p.operation_id, p.id)
    const failed = tasks.get(p.task_id)!.failure_count
    setStatus(p.task_id, failed ? 'partial_failure' : 'completed')
    emit(p.task_id, { phase: 'archiving', event_type: 'archive_completed', summary: `已归档 ${p.items.length} 个文件并写入台账` })
    audit.unshift({ id: uid(), timestamp: now(), actor: '演示用户', action: 'plan.execute', target: id, summary: `执行归档，${p.items.length} 个文件` })
    return structuredClone(p)
  },

  async rollbackOperation(opId) {
    await delay(500)
    const planId = opPlans.get(opId)
    const p = planId ? plans.get(planId) : undefined
    if (!p || p.operation_id !== opId) throw new ApiError(404, "NOT_FOUND", "归档操作不存在或已恢复")
    for (let i = ledger.length - 1; i >= 0; i--) if (p.items.some((it) => it.suggested_name === ledger[i].canonical_name && ledger[i].task_id === p.task_id)) ledger.splice(i, 1)
    for (const it of p.items) { const f = files.get(it.file_id); if (f) files.set(f.id, { ...f, archive_status: "pending" }) }
    p.status = "approved"; p.operation_id = null; p.version += 1
    setStatus(p.task_id, "approved")
    audit.unshift({ id: uid(), timestamp: now(), actor: "演示用户", action: "operation.rollback", target: opId, summary: "恢复归档操作" })
  },

  async listLedger(q) { await delay(); return paginate(filterLedger(q), q) },
  async exportLedger(q) {
    await delay(400)
    const rows = filterLedger({ ...q, page: undefined })
    const head = ['原文件名', '规范文件名', '部门', '类型', '日期', '归档路径', 'SHA-256', '任务ID', '执行时间']
    const esc = (s: string) => `"${s.replace(/"/g, '""')}"`
    const csv = '﻿' + [head, ...rows.map((r) => [r.original_name, r.canonical_name, r.department, r.document_type, r.document_date, r.archive_path, r.sha256, r.task_id, r.executed_at])].map((r) => r.map(esc).join(',')).join('\r\n')
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
}
const pendingCorrections = new Map<string, CorrectionProposal>()
const opPlans = new Map<string, string>()
