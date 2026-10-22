import { filenameFromDisposition, json, qs, raw, API_BASE, setCsrfToken } from './http'
import { openEventStream } from './sse'
import { buildImportForm } from './importForm'
import type {
  AiPolicy, ErrorReport, ErrorReportDetail, LlmStatus, SystemInfo,
  Api, ArchivePlan, ArchiveRule, AuditLog, CorrectionProposal, FileRecord, FolderListing, FsEntry, ImportJob,
  LedgerEntry, LlmConfig, LlmPreset, LlmTestResult, OATemplate, Page, Preview, ProviderStatus, ReadinessInfo, Replication, StorageConnection, StorageKind,
  StorageListing, StorageTestResult, TemplateTestResult, TrashItem,
  AdminUser, InviteCreated, Invitation, LoginResult, Me, MfaEnrolled, Passkey, SecuritySettings, SecuritySummary, SessionInfo, SetupStatus, TotpEnrollment, WebAuthnBegin,
} from './types'

/** 真实后端实现，路径与 contracts/openapi.yaml 一致。 */
export const realApi: Api = {
  mode: 'real',
  capabilities: { retryTask: true, providerStatus: true, rollback: true, webauthn: true },

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
    // 契约要求 paths 最先出现，其后（可选）options，再是与 paths 同序的 files
    const fd = buildImportForm(files, opts.options)
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
  listLlmPresets: () => json<LlmPreset[]>('/v1/llm/presets'),
  getLlmConfig: () => json<LlmConfig>('/v1/llm/config'),
  saveLlmConfig: (input) => json<LlmConfig>('/v1/llm/config', { method: 'PUT' }, input),
  testLlm: (input) => json<LlmTestResult>('/v1/llm/test', { method: 'POST' }, input),
  async listLlmModels(input) { return (await json<{ models: string[] }>('/v1/llm/models', { method: 'POST' }, input)).models },

  listFolder: (path) => json<FolderListing>('/v1/fs' + qs({ path })),
  contentUrl: (path, download) => API_BASE + '/v1/fs/content' + qs({ path, download: download ? '1' : undefined }),
  previewFile: (id) => json<Preview>(`/v1/files/${id}/preview`),
  previewArchived: (path) => json<Preview>('/v1/fs/preview' + qs({ path })),
  previewPageUrl: (source, page, maxSide) => source.kind === 'file'
    ? API_BASE + `/v1/files/${source.id}/preview/page` + qs({ page, max_side: maxSide })
    : API_BASE + '/v1/fs/preview/page' + qs({ path: source.path, page, max_side: maxSide }),
  createFolder: (path) => json<FsEntry>('/v1/fs/folders', { method: 'POST' }, { path }),
  moveEntry: (from, to) => json<{ entry: FsEntry; ledger_updated: number }>('/v1/fs/move', { method: 'POST' }, { from, to }),
  trashEntry: (path) => json<TrashItem>('/v1/fs/trash', { method: 'POST' }, { path }),
  listTrash: () => json<TrashItem[]>('/v1/fs/trash'),
  restoreTrash: (id) => json<{ path: string; ledger_updated: number }>(`/v1/fs/trash/${id}/restore`, { method: 'POST' }),
  async revealInExplorer(path) { await json<{ revealed: string }>('/v1/fs/reveal', { method: 'POST' }, { path }) },

  listStorageKinds: () => json<StorageKind[]>('/v1/storage/kinds'),
  listStorageConnections: () => json<StorageConnection[]>('/v1/storage/connections'),
  getStorageConnection: (id) => json<StorageConnection>(`/v1/storage/connections/${id}`),
  createStorageConnection: (input) => json<StorageConnection>('/v1/storage/connections', { method: 'POST' }, input),
  updateStorageConnection: (id, patch) => json<StorageConnection>(`/v1/storage/connections/${id}`, { method: 'PATCH' }, patch),
  deleteStorageConnection: (id) => json<void>(`/v1/storage/connections/${id}`, { method: 'DELETE' }),
  testStorageDraft: (input) => json<StorageTestResult>('/v1/storage/connections/test', { method: 'POST' }, input),
  testStorageConnection: (id, opts) => json<StorageTestResult>(`/v1/storage/connections/${id}/test`, { method: 'POST' }, opts ?? {}),
  browseStorage: (id, path) => json<StorageListing>(`/v1/storage/connections/${id}/browse` + qs({ path })),
  replicateToStorage: (id, opts) => json<{ queued: number }>(`/v1/storage/connections/${id}/replicate`, { method: 'POST' }, opts ?? {}),
  listReplications: (q) => json<Page<Replication>>('/v1/storage/replications' + qs(q)),
  retryReplication: (id) => json<Replication>(`/v1/storage/replications/${id}/retry`, { method: 'POST' }),
  createRemoteImport(connectionId, paths, opts) {
    const headers: Record<string, string> = {}
    if (opts.idempotencyKey) headers['Idempotency-Key'] = opts.idempotencyKey
    return json<ImportJob>('/v1/imports/remote', { method: 'POST', headers }, {
      connection_id: connectionId, items: paths.map((path) => ({ path })), ...(opts.templateId ? { template_id: opts.templateId } : {}), ...(opts.options && Object.keys(opts.options).length ? { options: opts.options } : {}),
    })
  },

  getLlmStatus: () => json<LlmStatus>('/v1/llm/status'),
  getPersonalLlm: () => json<LlmConfig>('/v1/llm/personal'),
  savePersonalLlm: (input) => json<LlmConfig>('/v1/llm/personal', { method: 'PUT' }, input),
  deletePersonalLlm: () => json<void>('/v1/llm/personal', { method: 'DELETE' }),
  testPersonalLlm: (input) => json<LlmTestResult>('/v1/llm/personal/test', { method: 'POST' }, input),
  async listPersonalLlmModels(input) { return (await json<{ models: string[] }>('/v1/llm/personal/models', { method: 'POST' }, input)).models },
  getAiPolicy: () => json<AiPolicy>('/v1/admin/ai-policy'),
  updateAiPolicy: (patch) => json<AiPolicy>('/v1/admin/ai-policy', { method: 'PATCH' }, patch),

  getSystemInfo: () => json<SystemInfo>('/v1/admin/system'),
  listErrorReports: (q) => json<Page<ErrorReport>>('/v1/admin/error-reports' + qs(q)),
  getErrorReport: (id) => json<ErrorReportDetail>(`/v1/admin/error-reports/${encodeURIComponent(id)}`),
  updateErrorReport: (id, resolved) => json<ErrorReportDetail>(`/v1/admin/error-reports/${encodeURIComponent(id)}`, { method: 'PATCH' }, { resolved }),
  deleteErrorReport: (id) => json<void>(`/v1/admin/error-reports/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  async downloadDiagnosticBundle() {
    const res = await raw('/v1/admin/diagnostics/bundle')
    return { blob: await res.blob(), filename: filenameFromDisposition(res.headers.get('Content-Disposition'), 'oa-diagnostics.zip') }
  },
  reportClientError: (input) => json<{ report_id: string }>('/v1/client-errors', { method: 'POST' }, input, { silent: true }),

  /* ===== 初始化向导 / 登录 / 账号安全 / 管理（契约 setup、auth、admin-users） ===== */
  getSetupStatus: () => json<SetupStatus>('/v1/setup/status', {}, undefined, { silent: true }),
  async verifySetupToken(token) { return (await json<{ valid: boolean }>('/v1/setup/verify-token', { method: 'POST' }, { token }, { silent: true })).valid },
  async completeSetup(req) { return remember(await json<LoginResult>('/v1/setup/complete', { method: 'POST' }, req, { silent: true })) },
  finishSetup: () => json<SetupStatus>('/v1/setup/finish', { method: 'POST' }),

  async login(username, password) { return remember(await json<LoginResult>('/v1/auth/login', { method: 'POST' }, { username, password }, { silent: true })) },
  async logout() { try { await json<void>('/v1/auth/logout', { method: 'POST' }, undefined, { silent: true }) } finally { setCsrfToken('') } },
  async getMe(opts) {
    const me = await json<Me>('/v1/auth/me', {}, undefined, { silent: opts?.silent })
    setCsrfToken(me.csrf_token)
    return me
  },
  async mfaTotp(mfaToken, code) { return remember(await json<LoginResult>('/v1/auth/mfa/totp', { method: 'POST' }, { mfa_token: mfaToken, code }, { silent: true })) },
  async mfaRecovery(mfaToken, code) { return remember(await json<LoginResult>('/v1/auth/mfa/recovery', { method: 'POST' }, { mfa_token: mfaToken, code }, { silent: true })) },
  mfaPasskeyBegin: (mfaToken) => json<WebAuthnBegin>('/v1/auth/mfa/passkey/begin', { method: 'POST' }, { mfa_token: mfaToken }, { silent: true }),
  async mfaPasskeyFinish(mfaToken, challengeId, credential) { return remember(await json<LoginResult>('/v1/auth/mfa/passkey/finish', { method: 'POST' }, { mfa_token: mfaToken, challenge_id: challengeId, credential }, { silent: true })) },
  passkeyLoginBegin: () => json<WebAuthnBegin>('/v1/auth/passkey/login/begin', { method: 'POST' }, undefined, { silent: true }),
  async passkeyLoginFinish(challengeId, credential) { return remember(await json<LoginResult>('/v1/auth/passkey/login/finish', { method: 'POST' }, { challenge_id: challengeId, credential }, { silent: true })) },

  async reauth(password) { return rememberMe(await json<Me>('/v1/auth/reauth', { method: 'POST' }, { password })) },
  reauthPasskeyBegin: () => json<WebAuthnBegin>('/v1/auth/reauth/passkey/begin', { method: 'POST' }),
  async reauthPasskeyFinish(challengeId, credential) { return rememberMe(await json<Me>('/v1/auth/reauth/passkey/finish', { method: 'POST' }, { challenge_id: challengeId, credential })) },

  changePassword: (current, next) => json<void>('/v1/auth/password', { method: 'POST' }, { current_password: current, new_password: next }),
  getMySecurity: () => json<SecuritySummary>('/v1/auth/security'),
  totpBegin: () => json<TotpEnrollment>('/v1/auth/totp/begin', { method: 'POST' }),
  async totpConfirm(challengeId, code) { return rememberEnrolled(await json<MfaEnrolled>('/v1/auth/totp/confirm', { method: 'POST' }, { challenge_id: challengeId, code })) },
  totpDisable: () => json<void>('/v1/auth/totp', { method: 'DELETE' }),
  passkeyRegisterBegin: () => json<WebAuthnBegin>('/v1/auth/passkeys/begin', { method: 'POST' }),
  async passkeyRegisterFinish(challengeId, credential, name) {
    return rememberEnrolled(await json<MfaEnrolled>('/v1/auth/passkeys/finish', { method: 'POST' }, { challenge_id: challengeId, credential, ...(name ? { name } : {}) }))
  },
  renamePasskey: (id, name) => json<Passkey>(`/v1/auth/passkeys/${encodeURIComponent(id)}`, { method: 'PATCH' }, { name }),
  deletePasskey: (id) => json<void>(`/v1/auth/passkeys/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  async regenerateRecoveryCodes() { return (await json<{ recovery_codes: string[] }>('/v1/auth/recovery-codes', { method: 'POST' })).recovery_codes },
  listMySessions: () => json<SessionInfo[]>('/v1/auth/sessions'),
  revokeMySession: (id) => json<void>(`/v1/auth/sessions/${encodeURIComponent(id)}`, { method: 'DELETE' }),

  getInvitation: (token) => json<Invitation>(`/v1/auth/invitations/${encodeURIComponent(token)}`, {}, undefined, { silent: true }),
  invitationPasskeyBegin: (token) => json<WebAuthnBegin>(`/v1/auth/invitations/${encodeURIComponent(token)}/passkey/begin`, { method: 'POST' }, undefined, { silent: true }),
  async invitationPasskeyFinish(token, challengeId, credential, name) {
    return remember(await json<LoginResult>(`/v1/auth/invitations/${encodeURIComponent(token)}/passkey/finish`, { method: 'POST' }, { challenge_id: challengeId, credential, ...(name ? { name } : {}) }, { silent: true }))
  },

  adminListUsers: (q) => json<Page<AdminUser>>('/v1/admin/users' + qs(q)),
  adminCreateUser: (input) => json('/v1/admin/users', { method: 'POST' }, input),
  adminGetUser: (id) => json<AdminUser>(`/v1/admin/users/${id}`),
  adminUpdateUser: (id, patch) => json<AdminUser>(`/v1/admin/users/${id}`, { method: 'PATCH' }, patch),
  adminDeleteUser: (id) => json<void>(`/v1/admin/users/${id}`, { method: 'DELETE' }),
  async adminResetPassword(id) { return (await json<{ temporary_password: string }>(`/v1/admin/users/${id}/reset-password`, { method: 'POST' })).temporary_password },
  adminResetMfa: (id) => json<AdminUser>(`/v1/admin/users/${id}/reset-mfa`, { method: 'POST' }),
  adminUnlockUser: (id) => json<AdminUser>(`/v1/admin/users/${id}/unlock`, { method: 'POST' }),
  async adminRevokeSessions(id) { return (await json<{ revoked: number }>(`/v1/admin/users/${id}/revoke-sessions`, { method: 'POST' })).revoked },
  adminCreateInvite: (id) => json<InviteCreated>(`/v1/admin/users/${id}/invite`, { method: 'POST' }),
  adminListSessions: (userId) => json<SessionInfo[]>('/v1/admin/sessions' + qs({ user_id: userId })),
  adminRevokeSession: (id) => json<void>(`/v1/admin/sessions/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  adminGetSecuritySettings: () => json<SecuritySettings>('/v1/admin/security-settings'),
  adminUpdateSecuritySettings: (patch) => json<SecuritySettings>('/v1/admin/security-settings', { method: 'PATCH' }, patch),
}

/** 登录结果带 csrf_token（ok / enroll_mfa）：只放进内存。 */
function remember(r: LoginResult): LoginResult { if (r.csrf_token) setCsrfToken(r.csrf_token); return r }
function rememberMe(m: Me): Me { setCsrfToken(m.csrf_token); return m }
function rememberEnrolled(r: MfaEnrolled): MfaEnrolled { if (r.me) setCsrfToken(r.me.csrf_token); return r }
