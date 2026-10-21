/**
 * 领域类型：核心数据结构由 contracts/openapi.yaml 经 openapi-typescript 生成（`npm run gen:api` → schema.d.ts），
 * 此处只做别名与少量前端专用类型。契约变更后先重新生成，再由 vue-tsc 指出需要适配的位置。
 */
import type { components } from './schema'

type S = components['schemas']

export type TaskStatus = S['TaskStatus']
export type ImportJob = S['ImportJob']
export type FileRecord = S['FileRecord']
export type ArchiveStatus = FileRecord['archive_status']
export type ArchivePlan = S['ArchivePlan']
export type PlanStatus = ArchivePlan['status']
export type ArchivePlanItem = S['PlanItem']
export type PlanItemPatch = S['PlanPatch']
export type LedgerEntry = S['LedgerEntry']
export type TemplateTestResult = S['TemplateTestResult']
export type ArchiveRule = S['ArchiveRule']
export type ArchiveRuleInput = Omit<ArchiveRule, 'id'>
export type AgentEvent = Omit<S['AgentEvent'], 'file_id' | 'details'> & { file_id?: string | null; details?: Record<string, unknown> | null }
export type AuditLog = S['AuditLog']
export type CorrectionProposal = S['CorrectionProposal']
export type ProviderStatus = S['ProviderStatus']

export interface TemplateMatchRules { filename_keywords: string[]; text_keywords: string[] }
export interface TemplateFieldMappings { department: string; document_type: string; date_source: 'ocr_text' | 'filename' | 'file_metadata' }
/** match_rules / field_mappings 在契约中是自由对象；前端收窄为已文档化的键。 */
export interface OATemplate extends Omit<S['OATemplate'], 'match_rules' | 'field_mappings'> {
  match_rules: TemplateMatchRules
  field_mappings: TemplateFieldMappings
}
export type OATemplateInput = Omit<OATemplate, 'id' | 'created_at'>

export interface Page<T> { items: T[]; total: number; page: number; page_size: number }

export interface ListQuery { page?: number; page_size?: number }
export interface FileQuery extends ListQuery { q?: string; department?: string; type?: string; date_from?: string; date_to?: string; archive_status?: ArchiveStatus; review_required?: boolean }
export interface LedgerQuery extends ListQuery { q?: string; department?: string; type?: string; date_from?: string; date_to?: string; sort?: string; order?: 'asc' | 'desc' }
export interface TaskQuery extends ListQuery { status?: TaskStatus }

export interface ReadinessInfo {
  alive: boolean
  ready: boolean | null
  database?: string
  worker?: string
}

/** 浏览器选择/拖拽得到的待导入文件：保留相对路径，绝不使用本机绝对路径。 */
export interface PickedFile { file: File; relativePath: string }

export interface ApiErrorBody { error: { code: string; message: string; request_id?: string; details?: Record<string, unknown> } }

export class ApiError extends Error {
  constructor(public status: number, public code: string, message: string, public requestId?: string, public details?: Record<string, unknown>) {
    super(message)
  }
}

/** 后端能力开关：契约没有对应接口时前端诚实地禁用，而不是做假交互。 */
export interface Capabilities { retryTask: boolean; providerStatus: boolean; rollback: boolean }

export interface Api {
  readonly mode: 'mock' | 'real'
  readonly capabilities: Capabilities
  readiness(): Promise<ReadinessInfo>
  createImport(files: PickedFile[], opts: { templateId?: string | null; idempotencyKey?: string }): Promise<ImportJob>
  listTasks(q?: TaskQuery): Promise<Page<ImportJob>>
  getTask(id: string): Promise<ImportJob>
  cancelTask(id: string): Promise<ImportJob>
  retryTask(id: string): Promise<ImportJob>
  /** 返回 EventSource 兼容的取消函数；Last-Event-ID 由实现负责续传。 */
  subscribeEvents(taskId: string, onEvent: (e: AgentEvent) => void, onState: (s: 'open' | 'reconnecting' | 'closed') => void): () => void
  listFiles(q?: FileQuery): Promise<Page<FileRecord>>
  listTaskFiles(taskId: string): Promise<FileRecord[]>
  getFile(id: string): Promise<FileRecord>
  getPlan(id: string): Promise<ArchivePlan>
  patchPlan(id: string, version: number, items: PlanItemPatch[]): Promise<ArchivePlan>
  approvePlan(id: string, version: number): Promise<ArchivePlan>
  executePlan(id: string, idempotencyKey: string): Promise<ArchivePlan>
  rollbackOperation(operationId: string): Promise<void>
  listLedger(q?: LedgerQuery): Promise<Page<LedgerEntry>>
  exportLedger(q?: LedgerQuery): Promise<{ blob: Blob; filename: string }>
  listTemplates(): Promise<OATemplate[]>
  createTemplate(t: OATemplateInput): Promise<OATemplate>
  updateTemplate(id: string, t: Partial<OATemplateInput>): Promise<OATemplate>
  deleteTemplate(id: string): Promise<void>
  testTemplate(id: string, sample: File): Promise<TemplateTestResult>
  listRules(): Promise<ArchiveRule[]>
  createRule(r: ArchiveRuleInput): Promise<ArchiveRule>
  listAuditLogs(q?: ListQuery): Promise<Page<AuditLog>>
  proposeCorrection(fileId: string, instruction: string): Promise<CorrectionProposal>
  approveCorrection(id: string): Promise<CorrectionProposal>
  providerStatus(): Promise<ProviderStatus[]>
}
