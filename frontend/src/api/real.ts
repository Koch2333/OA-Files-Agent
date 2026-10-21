import { filenameFromDisposition, json, qs, raw, API_BASE } from './http'
import { openEventStream } from './sse'
import type {
  Api, ArchivePlan, ArchiveRule, AuditLog, CorrectionProposal, FileRecord, ImportJob,
  LedgerEntry, OATemplate, Page, ProviderStatus, ReadinessInfo, TemplateTestResult,
} from './types'

/** 真实后端实现，路径与 contracts/openapi.yaml 一致。 */
export const realApi: Api = {
  mode: 'real',
  capabilities: { retryTask: true, providerStatus: true, rollback: true },

  async readiness(): Promise<ReadinessInfo> {
    const alive = await raw('/healthz').then(() => true, () => false)
    if (!alive) return { alive: false, ready: null }
    try {
      // 503 的响应体同样带 dependencies，故不经过 json()（它会把非 2xx 转成错误）
      const res = await fetch(API_BASE + '/readyz', { headers: { Accept: 'application/json' } })
      const body = (await res.json()) as { data?: { status: string; dependencies: { database: string; worker: string } } }
      const d = body.data
      return d ? { alive, ready: d.status === 'ready', database: d.dependencies.database, worker: d.dependencies.worker } : { alive, ready: false }
    } catch {
      return { alive, ready: false }
    }
  },

  async createImport(files, opts) {
    const fd = new FormData()
    // 契约要求 paths 清单最先出现且仅一次，随后 files 与之同序
    fd.append('paths', JSON.stringify(files.map((f) => f.relativePath)))
    for (const f of files) fd.append('files', f.file, f.file.name)
    const headers: Record<string, string> = {}
    if (opts.idempotencyKey) headers['Idempotency-Key'] = opts.idempotencyKey
    return json<ImportJob>('/v1/imports' + qs({ template_id: opts.templateId }), { method: 'POST', body: fd, headers })
  },
  listTasks: (q) => json<Page<ImportJob>>('/v1/tasks' + qs(q)),
  getTask: (id) => json<ImportJob>(`/v1/tasks/${id}`),
  cancelTask: (id) => json<ImportJob>(`/v1/tasks/${id}/cancel`, { method: 'POST' }),
  retryTask: (id) => json<ImportJob>(`/v1/tasks/${id}/retry`, { method: 'POST' }),
  subscribeEvents: openEventStream,

  listFiles: (q) => json<Page<FileRecord>>('/v1/files' + qs(q)),
  async listTaskFiles(taskId) {
    const p = await json<Page<FileRecord>>('/v1/files' + qs({ task_id: taskId, page_size: 200 }))
    return p.items
  },
  getFile: (id) => json<FileRecord>(`/v1/files/${id}`),

  getPlan: (id) => json<ArchivePlan>(`/v1/archive-plans/${id}`),
  patchPlan: (id, version, items) => json<ArchivePlan>(`/v1/archive-plans/${id}`, { method: 'PATCH' }, { version, items }),
  approvePlan: (id, version) => json<ArchivePlan>(`/v1/archive-plans/${id}/approve`, { method: 'POST' }, { version }),
  executePlan: (id, key) => json<ArchivePlan>(`/v1/archive-plans/${id}/execute`, { method: 'POST', headers: { 'Idempotency-Key': key } }),
  rollbackOperation: (id) => json<void>(`/v1/archive-operations/${id}/rollback`, { method: 'POST' }),

  listLedger: (q) => json<Page<LedgerEntry>>('/v1/ledger' + qs(q)),
  async exportLedger(q) {
    const { page: _p, page_size: _s, ...filters } = q ?? {}
    const res = await raw('/v1/ledger/export' + qs(filters))
    return { blob: await res.blob(), filename: filenameFromDisposition(res.headers.get('Content-Disposition'), '归档台账.xlsx') }
  },

  listTemplates: () => json<OATemplate[]>('/v1/oa-templates'),
  createTemplate: (t) => json<OATemplate>('/v1/oa-templates', { method: 'POST' }, t),
  updateTemplate: (id, t) => json<OATemplate>(`/v1/oa-templates/${id}`, { method: 'PATCH' }, t),
  deleteTemplate: (id) => json<void>(`/v1/oa-templates/${id}`, { method: 'DELETE' }),
  testTemplate(id, sample) {
    const fd = new FormData()
    fd.append('file', sample, sample.name)
    return json<TemplateTestResult>(`/v1/oa-templates/${id}/test`, { method: 'POST', body: fd })
  },
  listRules: () => json<ArchiveRule[]>('/v1/archive-rules'),
  createRule: (r) => json<ArchiveRule>('/v1/archive-rules', { method: 'POST' }, r),
  listAuditLogs: (q) => json<Page<AuditLog>>('/v1/audit-logs' + qs(q)),

  proposeCorrection: (fileId, instruction) => json<CorrectionProposal>(`/v1/files/${fileId}/correction-proposals`, { method: 'POST' }, { instruction }),
  approveCorrection: (id) => json<CorrectionProposal>(`/v1/correction-proposals/${id}/approve`, { method: 'POST' }),
  providerStatus: () => json<ProviderStatus[]>('/v1/providers'),
}
