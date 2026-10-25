/**
 * 领域类型：核心数据结构由 contracts/openapi.yaml 经 openapi-typescript 生成（`npm run gen:api` → schema.d.ts），
 * 此处只做别名与少量前端专用类型。契约变更后先重新生成，再由 vue-tsc 指出需要适配的位置。
 */
import type { components } from './schema'
import type {
  AdminUser, AdminUserCreate, AdminUserCreated, AdminUserPatch, AdminUserQuery, Invitation, InviteCreated, LoginResult, Me, MfaEnrolled, Passkey,
  SecuritySettings, SecuritySettingsPatch, SecuritySummary, SessionInfo, SetupRequest, SetupStatus, TotpEnrollment, WebAuthnBegin, WebAuthnJSON,
} from './authTypes'

export * from './authTypes'

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
export type LlmPreset = S['LlmPreset']
export type LlmConfig = S['LlmConfig']
export type LlmOauthStart = { authorize_url: string; state: string; expires_at: string }
export type LlmOauthStatus = { status: 'pending' | 'connecting' | 'connected' | 'failed' | 'denied' | 'expired'; provider: string; scope: string; message?: string; test?: S['LlmTestResult']; config?: S['LlmConfig'] }
export type LlmConfigInput = S['LlmConfigInput']
export type LlmTestInput = S['LlmTestInput']
export type LlmTestResult = S['LlmTestResult']
export type LlmStatus = S['LlmStatus']
export type AiPolicy = S['AiPolicy']
export type AiPolicyPatch = S['AiPolicyPatch']
/** 契约里 automation / llm_source 带默认值，生成类型会标为必填；实际都是可选。 */
export type ImportOptions = Partial<S['ImportOptions']>
export type ErrorReport = S['ErrorReport']
export type ErrorReportDetail = S['ErrorReportDetail']
export type ClientErrorInput = S['ClientErrorInput']
export type SystemInfo = S['SystemInfo']
export type ErrorSource = ErrorReport['source']
export type ErrorSeverity = ErrorReport['severity']
export type FsEntry = S['FsEntry']
export type FolderListing = S['FolderListing']
export type TrashItem = S['TrashItem']
export type Preview = S['Preview']
export type StorageKind = S['StorageKind']
export type StorageField = S['StorageField']
export type StorageConnection = S['StorageConnection']
export type StorageConnectionInput = S['StorageConnectionInput']
export type StorageConnectionPatch = S['StorageConnectionPatch']
export type StorageTestInput = S['StorageTestInput']
export type StorageTestResult = S['StorageTestResult']
export type StorageListing = S['StorageListing']
export type StorageEntry = S['StorageEntry']
export type Replication = S['Replication']
export type ReplicationStatus = Replication['status']
export type ReplicatePolicy = StorageConnection['replicate']
/** 预览来源：导入时保存的原件副本（文件 ID），或归档目录中的文件（相对路径）。 */
export type PreviewSource = { kind: 'file'; id: string } | { kind: 'archived'; path: string }

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
export interface LedgerQuery extends ListQuery { decision?: 'manual' | 'ai_auto'; q?: string; department?: string; type?: string; date_from?: string; date_to?: string; sort?: string; order?: 'asc' | 'desc' }
export interface ReplicationQuery extends ListQuery { connection_id?: string; status?: ReplicationStatus }
export interface TaskQuery extends ListQuery { status?: TaskStatus }
export interface ErrorReportQuery extends ListQuery { source?: ErrorSource; severity?: ErrorSeverity; resolved?: boolean; q?: string }

export interface ReadinessInfo {
  alive: boolean
  ready: boolean | null
  database?: string
  worker?: string
}

/** 浏览器选择/拖拽得到的待导入文件：保留相对路径，绝不使用本机绝对路径。 */
export interface PickedFile { file: File; relativePath: string }

/** 可续传分片上传 / 本机路径导入（契约 imports 标签）。没有大小或数量上限；config 里的上限为运维可选安全阀，null = 不限制。 */
export type ImportConfig = S['ImportConfig']
export type UploadSession = S['UploadSession']
export type ImportBatchStatus = S['ImportBatchStatus']
export type PathImportStatus = S['PathImportStatus']
export interface CreateUploadInput { batch_id?: string; name: string; path?: string; size: number; sha256?: string }
export interface FromPathsInput { batch_id?: string; paths: string[]; include_hidden?: boolean; commit?: boolean; template_id?: string; options?: ImportOptions; idempotencyKey?: string }

export interface ApiErrorBody { error: { code: string; message: string; request_id?: string; details?: Record<string, unknown> } }

export class ApiError extends Error {
  /** Retry-After（秒），仅 429 等限速响应 */
  retryAfter?: number
  constructor(public status: number, public code: string, message: string, public requestId?: string, public details?: Record<string, unknown>) {
    super(message)
  }
}

/** 后端能力开关：契约没有对应接口时前端诚实地禁用，而不是做假交互。 */
export interface Capabilities { retryTask: boolean; providerStatus: boolean; rollback: boolean; /** 浏览器 WebAuthn 是否真实可用（演示模式为 false，改用模拟） */ webauthn: boolean }

export interface Api {
  readonly mode: 'mock' | 'real'
  readonly capabilities: Capabilities
  readiness(): Promise<ReadinessInfo>
  createImport(files: PickedFile[], opts: { templateId?: string | null; idempotencyKey?: string; options?: ImportOptions }): Promise<ImportJob>
  getImportConfig(): Promise<ImportConfig>
  createUpload(input: CreateUploadInput): Promise<UploadSession>
  getUpload(id: string): Promise<UploadSession>
  /** 追加一个分片（幂等）；offset 不一致时抛 ApiError(409, OFFSET_MISMATCH, details.offset)。 */
  putUploadChunk(id: string, offset: number, chunk: Blob, signal?: AbortSignal): Promise<UploadSession>
  completeUpload(id: string): Promise<UploadSession>
  abortUpload(id: string): Promise<void>
  getImportBatch(batchId: string): Promise<ImportBatchStatus>
  discardImportBatch(batchId: string): Promise<void>
  commitImportBatch(batchId: string, opts: { templateId?: string | null; idempotencyKey?: string; options?: ImportOptions }): Promise<ImportJob>
  /** 仅桌面本机模式：后端按绝对路径流式复制（不移动源文件）。 */
  importFromPaths(input: FromPathsInput): Promise<PathImportStatus>
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
  /** 识别引擎（模型）配置：预设、读取、保存即生效、测试连接、获取模型列表；密钥永不返回。 */
  listLlmPresets(): Promise<LlmPreset[]>
  getLlmConfig(): Promise<LlmConfig>
  saveLlmConfig(input: LlmConfigInput): Promise<LlmConfig>
  testLlm(input: LlmTestInput): Promise<LlmTestResult>
  listLlmModels(input: { base_url: string; api_key?: string }): Promise<string[]>
  /** 文件管理：路径均相对归档根目录（空串 = 根）。 */
  listFolder(path: string): Promise<FolderListing>
  /** 纯 URL 构造（预览/下载）；演示模式返回 '#'，调用方需提示不可预览。 */
  contentUrl(path: string, download?: boolean): string
  /** 结构化预览（pdf/image 只返回页数，页图见 previewPageUrl）。 */
  previewFile(id: string): Promise<Preview>
  previewArchived(path: string): Promise<Preview>
  /** 纯 URL 构造：第 page 页的 PNG；演示模式返回内联 SVG。 */
  previewPageUrl(source: PreviewSource, page: number, maxSide: number): string
  createFolder(path: string): Promise<FsEntry>
  moveEntry(from: string, to: string): Promise<{ entry: FsEntry; ledger_updated: number }>
  trashEntry(path: string): Promise<TrashItem>
  listTrash(): Promise<TrashItem[]>
  restoreTrash(id: string): Promise<{ path: string; ledger_updated: number }>
  revealInExplorer(path: string): Promise<void>
  /** 存储连接（可选功能）：kinds 返回 404 表示后端未启用该功能。 */
  listStorageKinds(): Promise<StorageKind[]>
  listStorageConnections(): Promise<StorageConnection[]>
  getStorageConnection(id: string): Promise<StorageConnection>
  createStorageConnection(input: StorageConnectionInput): Promise<StorageConnection>
  updateStorageConnection(id: string, patch: StorageConnectionPatch): Promise<StorageConnection>
  deleteStorageConnection(id: string): Promise<void>
  testStorageDraft(input: StorageTestInput): Promise<StorageTestResult>
  testStorageConnection(id: string, opts?: { check_write?: boolean; accept_host_key?: string }): Promise<StorageTestResult>
  browseStorage(id: string, path?: string): Promise<StorageListing>
  replicateToStorage(id: string, opts?: { path_prefix?: string }): Promise<{ queued: number }>
  listReplications(q?: ReplicationQuery): Promise<Page<Replication>>
  retryReplication(id: string): Promise<Replication>
  createRemoteImport(connectionId: string, paths: string[], opts: { templateId?: string | null; idempotencyKey?: string; options?: ImportOptions }): Promise<ImportJob>

  /* ===== AI 工作台 / 个人大模型 / 管理员策略（契约 tag ai、admin） ===== */
  getLlmStatus(): Promise<LlmStatus>
  getPersonalLlm(): Promise<LlmConfig>
  savePersonalLlm(input: LlmConfigInput): Promise<LlmConfig>
  deletePersonalLlm(): Promise<void>
  testPersonalLlm(input: LlmTestInput): Promise<LlmTestResult>
  listPersonalLlmModels(input: { base_url: string; api_key?: string }): Promise<string[]>
  /** 用平台账号登录授权（OAuth + PKCE）：start 返回授权地址；status 由发起者轮询；disconnect 删除登录获得的密钥 */
  startLlmOauth(input: { provider: string; scope: 'personal' | 'global'; client?: 'web' | 'desktop' }): Promise<LlmOauthStart>
  getLlmOauthStatus(state: string): Promise<LlmOauthStatus>
  disconnectLlmOauth(scope: 'personal' | 'global'): Promise<LlmConfig>
  getAiPolicy(): Promise<AiPolicy>
  updateAiPolicy(patch: AiPolicyPatch): Promise<AiPolicy>

  /* ===== 运行诊断（契约 tag diagnostics） ===== */
  getSystemInfo(): Promise<SystemInfo>
  listErrorReports(q?: ErrorReportQuery): Promise<Page<ErrorReport>>
  getErrorReport(id: string): Promise<ErrorReportDetail>
  updateErrorReport(id: string, resolved: boolean): Promise<ErrorReportDetail>
  deleteErrorReport(id: string): Promise<void>
  downloadDiagnosticBundle(): Promise<{ blob: Blob; filename: string }>
  reportClientError(input: ClientErrorInput): Promise<{ report_id: string }>

  /* ===== 初始化向导（公开） ===== */
  getSetupStatus(): Promise<SetupStatus>
  verifySetupToken(token: string): Promise<boolean>
  completeSetup(req: SetupRequest): Promise<LoginResult>
  /** 完成向导（解除服务端闸门）；需要管理员完整会话。返回最新的公开状态 */
  finishSetup(): Promise<SetupStatus>
  /* ===== 登录 / 会话 ===== */
  login(username: string, password: string): Promise<LoginResult>
  logout(): Promise<void>
  /** 当前会话；返回的 csrf_token 由实现保存在内存中。silent=true 时 401 不触发全局“登录过期”处理。 */
  getMe(opts?: { silent?: boolean }): Promise<Me>
  mfaTotp(mfaToken: string, code: string): Promise<LoginResult>
  mfaRecovery(mfaToken: string, code: string): Promise<LoginResult>
  mfaPasskeyBegin(mfaToken: string): Promise<WebAuthnBegin>
  mfaPasskeyFinish(mfaToken: string, challengeId: string, credential: WebAuthnJSON): Promise<LoginResult>
  passkeyLoginBegin(): Promise<WebAuthnBegin>
  passkeyLoginFinish(challengeId: string, credential: WebAuthnJSON): Promise<LoginResult>
  /* ===== 近期认证 ===== */
  reauth(password: string): Promise<Me>
  reauthPasskeyBegin(): Promise<WebAuthnBegin>
  reauthPasskeyFinish(challengeId: string, credential: WebAuthnJSON): Promise<Me>
  /* ===== 账号安全（自助） ===== */
  changePassword(current: string, next: string): Promise<void>
  getMySecurity(): Promise<SecuritySummary>
  totpBegin(): Promise<TotpEnrollment>
  totpConfirm(challengeId: string, code: string): Promise<MfaEnrolled>
  totpDisable(): Promise<void>
  passkeyRegisterBegin(): Promise<WebAuthnBegin>
  passkeyRegisterFinish(challengeId: string, credential: WebAuthnJSON, name?: string): Promise<MfaEnrolled>
  renamePasskey(id: string, name: string): Promise<Passkey>
  deletePasskey(id: string): Promise<void>
  regenerateRecoveryCodes(): Promise<string[]>
  listMySessions(): Promise<SessionInfo[]>
  revokeMySession(id: string): Promise<void>
  /* ===== 邀请（公开） ===== */
  getInvitation(token: string): Promise<Invitation>
  invitationPasskeyBegin(token: string): Promise<WebAuthnBegin>
  invitationPasskeyFinish(token: string, challengeId: string, credential: WebAuthnJSON, name?: string): Promise<LoginResult>
  /* ===== 管理员 ===== */
  adminListUsers(q?: AdminUserQuery): Promise<Page<AdminUser>>
  adminCreateUser(input: AdminUserCreate): Promise<AdminUserCreated>
  adminGetUser(id: string): Promise<AdminUser>
  adminUpdateUser(id: string, patch: AdminUserPatch): Promise<AdminUser>
  adminDeleteUser(id: string): Promise<void>
  adminResetPassword(id: string): Promise<string>
  adminResetMfa(id: string): Promise<AdminUser>
  adminUnlockUser(id: string): Promise<AdminUser>
  adminRevokeSessions(id: string): Promise<number>
  adminCreateInvite(id: string): Promise<InviteCreated>
  adminListSessions(userId?: string): Promise<SessionInfo[]>
  adminRevokeSession(id: string): Promise<void>
  adminGetSecuritySettings(): Promise<SecuritySettings>
  adminUpdateSecuritySettings(patch: SecuritySettingsPatch): Promise<SecuritySettings>
}
