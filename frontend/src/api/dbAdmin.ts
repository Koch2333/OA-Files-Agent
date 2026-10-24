// 数据库管理接口（/v1/admin/database*，仅管理员）。演示模式下返回只读示例，不访问后端。
import type { components } from './schema'
import { json } from './http'
import { isMock } from './index'

type S = components['schemas']
export type DbConnRequest = S['DatabaseConnRequest']
export type DbStatus = S['DatabaseStatus']
export type DbTestResult = S['DatabaseTestResult']
export type DbReport = S['DatabaseReport']
export type DbInitPreview = S['DatabaseInitPreview']
export type DbProvider = 'postgres' | 'mysql' | 'mssql'

export const DB_PROVIDERS: { id: DbProvider; name: string; port: number; minVersion: string }[] = [
  { id: 'postgres', name: 'PostgreSQL', port: 5432, minVersion: '14+（已测 16）' },
  { id: 'mysql', name: 'MySQL', port: 3306, minVersion: '8.0+（已测 8.0）' },
  { id: 'mssql', name: 'SQL Server', port: 1433, minVersion: '2019+（已测 2019）' },
]

export function defaultPort(p: string): number { return DB_PROVIDERS.find((x) => x.id === p)?.port ?? 5432 }

const demoStatus: DbStatus = {
  configured: true, source: 'embedded', read_only: true, read_only_reason: '演示数据：不连接后端', restart_needed: false, warnings: [],
  active: { provider: 'postgres', label: 'PostgreSQL（内嵌）', host: '127.0.0.1', port: 54320, name: 'oa', user: 'oa', tls_mode: 'disable', has_password: true,
    connected: true, server_version: '16.4', latency_ms: 2, migration: { state: 'current', version: 10, latest: 10, dirty: false }, isolation: 'dedicated_database', checks: [], embedded: true },
}

export const dbAdminApi = {
  status: (): Promise<DbStatus> => isMock() ? Promise.resolve(demoStatus) : json<DbStatus>('/v1/admin/database'),
  test: (r: DbConnRequest) => json<DbTestResult>('/v1/admin/database/test', { method: 'POST' }, r),
  validate: (r: DbConnRequest) => json<DbReport>('/v1/admin/database/validate-permissions', { method: 'POST' }, r),
  save: (r: DbConnRequest) => json<DbStatus>('/v1/admin/database', { method: 'PUT' }, r),
  discard: () => json<void>('/v1/admin/database/pending', { method: 'DELETE' }),
  initialize: (body: { target?: 'pending' | 'active'; dry_run?: boolean; confirmation_token?: string }) => json<DbInitPreview>('/v1/admin/database/initialize', { method: 'POST' }, body),
}
