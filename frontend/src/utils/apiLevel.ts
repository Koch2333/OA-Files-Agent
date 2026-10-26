/**
 * 前后端契约代数。必须与后端 api.APILevel（backend/internal/api/setup.go）保持一致；
 * 前端开始依赖新的接口或字段时，两处一起递增。GET /v1/setup/status 返回后端的 api_level。
 */
export const REQUIRED_API_LEVEL = 8

export interface ApiLevelMismatch { kind: 'old' | 'new'; got: number | null; need: number; serverVersion: string; message: string }

/** 后端报告的 api_level 与前端要求不一致时返回提示信息（缺失视为 0：旧后端不返回该字段）。 */
export function apiLevelMismatch(s: { api_level?: number | null; server_version?: string | null } | null | undefined, need = REQUIRED_API_LEVEL): ApiLevelMismatch | null {
  if (!s) return null
  const got = typeof s.api_level === 'number' ? s.api_level : null
  const level = got ?? 0
  if (level === need) return null
  const kind = level < need ? 'old' : 'new'
  const serverVersion = s.server_version || '未知'
  return {
    kind, got, need, serverVersion,
    message: `后端版本${kind === 'old' ? '过旧' : '过新'}（api_level ${got ?? '未报告'}，需要 ${need}；后端版本 ${serverVersion}）：请重启或升级后端`,
  }
}

/** 契约里不含路径参数的固定接口。对这些路径的 404 说明后端没有实现该接口（版本过旧），而不是“资源不存在”。 */
export const KNOWN_STATIC_PATHS: readonly string[] = [
  '/v1/admin/ai-policy', '/v1/admin/database', '/v1/admin/database/initialize', '/v1/admin/database/pending', '/v1/admin/database/test', '/v1/admin/database/validate-permissions', '/v1/admin/diagnostics/bundle', '/v1/admin/error-reports', '/v1/admin/security-settings', '/v1/admin/sessions', '/v1/admin/system',
  '/v1/admin/users', '/v1/archive-rules', '/v1/audit-logs', '/v1/auth/login', '/v1/auth/logout', '/v1/auth/me', '/v1/auth/mfa/passkey/begin', '/v1/auth/mfa/passkey/finish',
  '/v1/auth/mfa/recovery', '/v1/auth/mfa/totp', '/v1/auth/passkey/login/begin', '/v1/auth/passkey/login/finish', '/v1/auth/passkeys/begin', '/v1/auth/passkeys/finish',
  '/v1/auth/password', '/v1/auth/reauth', '/v1/auth/reauth/passkey/begin', '/v1/auth/reauth/passkey/finish', '/v1/auth/recovery-codes', '/v1/auth/security',
  '/v1/auth/sessions', '/v1/auth/totp', '/v1/auth/totp/begin', '/v1/auth/totp/confirm', '/v1/classification/options', '/v1/client-errors', '/v1/files', '/v1/fs', '/v1/fs/bulk/move', '/v1/fs/bulk/trash', '/v1/fs/content', '/v1/fs/download', '/v1/fs/folders',
  '/v1/fs/move', '/v1/fs/preview', '/v1/fs/preview/page', '/v1/fs/reveal', '/v1/fs/trash', '/v1/fs/verify', '/v1/imports', '/v1/imports/config', '/v1/imports/from-paths', '/v1/imports/remote', '/v1/imports/uploads', '/v1/ledger', '/v1/ledger/export',
  '/v1/llm/config', '/v1/llm/models', '/v1/llm/oauth/callback', '/v1/llm/oauth/disconnect', '/v1/llm/oauth/start', '/v1/llm/oauth/status', '/v1/llm/personal', '/v1/llm/personal/models', '/v1/llm/personal/test', '/v1/llm/presets', '/v1/llm/status', '/v1/llm/test',
  '/v1/oa-templates', '/v1/providers', '/v1/setup/complete', '/v1/setup/finish', '/v1/setup/status', '/v1/setup/verify-token', '/v1/storage/connections',
  '/v1/storage/connections/test', '/v1/storage/kinds', '/v1/storage/replications', '/v1/tasks',
]
const KNOWN = new Set(KNOWN_STATIC_PATHS)

export const STALE_BACKEND_HINT = '接口不存在：后端可能版本过旧，请重启或升级后端'

/** 对已知契约路径的 404：给出“后端版本过旧”的提示；其它 404 返回 null（资源不存在等）。 */
export function staleBackendHint(status: number, path: string): string | null {
  if (status !== 404) return null
  const p = path.split('?')[0].replace(/\/+$/, '')
  return KNOWN.has(p) ? STALE_BACKEND_HINT : null
}
