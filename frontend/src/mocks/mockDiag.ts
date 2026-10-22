/** 演示用运行诊断：错误报告（含聚合、已解决、上次异常结束样例）、系统信息、客户端错误上报、诊断包下载。 */
import { ApiError, type Api, type ClientErrorInput, type ErrorReport, type ErrorReportDetail, type ErrorSeverity, type ErrorSource, type Page, type SystemInfo } from '../api/types'

const delay = (ms = 200) => new Promise((r) => setTimeout(r, ms))
const uid = () => crypto.randomUUID()
const ago = (min: number) => new Date(Date.now() - min * 60_000).toISOString()
const GIB = 1024 ** 3

export type MockDiagApi = Pick<Api, 'getSystemInfo' | 'listErrorReports' | 'getErrorReport' | 'updateErrorReport' | 'deleteErrorReport' | 'downloadDiagnosticBundle' | 'reportClientError'>

const GO_STACK = `goroutine 41 [running]:
runtime/debug.Stack()
\tC:/Program Files/Go/src/runtime/debug/stack.go:26 +0x5e
oa-agent/internal/api.recoverMiddleware.func1.1()
\tD:/build/oa-agent/backend/internal/api/recovery.go:31 +0x6d
panic({0x7ff61a2b3c40?, 0xc0001a2f30?})
oa-agent/internal/pipeline.(*Planner).buildItems(0xc0003a8000, {0xc000412000, 0x3, 0x4})
\tD:/build/oa-agent/backend/internal/pipeline/plan.go:188 +0x2a5
oa-agent/internal/pipeline.(*Runner).planTask(0xc0003a8000, {0x7ff61a2c1d28, 0xc0001b4000}, {0xc0004aa0f0, 0x24})
\tD:/build/oa-agent/backend/internal/pipeline/runner.go:244 +0x1d9`

const CLIENT_STACK = `TypeError: Cannot read properties of undefined (reading 'items')
    at PlanView.setup (/assets/PlanView-3fa9c1.js:1:4821)
    at callWithErrorHandling (/assets/index-9d2b7e.js:12:3311)
    at setupStatefulComponent (/assets/index-9d2b7e.js:12:11204)`

function occ(n: number, users: string[]): ErrorReportDetail['occurrences'] {
  return Array.from({ length: Math.min(n, 20) }, (_, i) => ({ at: ago(5 + i * 37), request_id: `req-${uid().slice(0, 8)}`, user: users[i % users.length] }))
}
function mk(p: { source: ErrorSource; severity: ErrorSeverity; code: string; message: string; count: number; first: number; last: number; resolved?: boolean; stack?: string; details?: Record<string, unknown>; users?: string[] }): ErrorReportDetail {
  const occurrences = occ(p.count, p.users ?? ['admin', 'zhang.wei'])
  return { id: uid(), source: p.source, severity: p.severity, code: p.code, message: p.message, count: p.count, first_seen: ago(p.first), last_seen: ago(p.last), resolved: !!p.resolved, request_id: occurrences[0]?.request_id ?? '', stack: p.stack ?? '', details: p.details ?? {}, occurrences }
}

export function createMockDiag(): { api: MockDiagApi } {
  const q = typeof location === 'undefined' ? new URLSearchParams() : new URLSearchParams(location.search)
  const unclean = q.get('mock_unclean') !== '0'
  const lowDisk = q.get('mock_disk') === 'low'
  const startedAt = Date.now() - (3 * 3600 + 25 * 60) * 1000
  const reports: ErrorReportDetail[] = [
    mk({ source: 'supervisor', severity: 'fatal', code: 'UNCLEAN_EXIT', message: '上次运行异常结束：进程未正常退出（可能被强制结束或崩溃）', count: 1, first: 190, last: 190, stack: '', details: { previous_pid: 18244, last_heartbeat: ago(191), exit_marker: 'missing' }, users: ['system'] }),
    mk({ source: 'api', severity: 'panic', code: 'PANIC', message: 'runtime error: index out of range [3] with length 3', count: 2, first: 1500, last: 640, stack: GO_STACK, details: { route: 'POST /v1/imports', task_id: '8f2c…c1', version: '0.9.0-demo' } }),
    mk({ source: 'worker', severity: 'error', code: 'WORKER_TIMEOUT', message: '文档解析 Worker 处理超时（60 s）：扫描件.pdf', count: 7, first: 2800, last: 55, details: { file: '扫描件.pdf', size_bytes: 18834221, pages: 64, stage: 'ocr' } }),
    mk({ source: 'llm', severity: 'error', code: 'LLM_UPSTREAM_401', message: '大模型服务返回 401：密钥无效或已过期', count: 12, first: 4300, last: 12, details: { provider: 'deepseek', model: 'deepseek-flash', scope: 'personal', user: 'zhang.wei' }, users: ['zhang.wei'] }),
    mk({ source: 'storage', severity: 'error', code: 'STORAGE_WEBDAV_TIMEOUT', message: '存储连接“总部 NAS”读取超时', count: 4, first: 900, last: 300, details: { connection: '总部 NAS', kind: 'webdav', timeout_s: 30 } }),
    mk({ source: 'pipeline', severity: 'error', code: 'ARCHIVE_SOURCE_CHANGED', message: '归档前原件已被改动，已跳过 1 个文件', count: 1, first: 400, last: 400, details: { task_id: 'c2d1…90', file: '三月薪酬核算表.xlsx' } }),
    mk({ source: 'client', severity: 'error', code: 'CLIENT_ERROR', message: "TypeError: Cannot read properties of undefined (reading 'items')", count: 3, first: 1200, last: 700, resolved: true, stack: CLIENT_STACK, details: { route: '/plans/:id', component: 'PlanView', app_version: '0.9.0-demo' } }),
    mk({ source: 'startup', severity: 'error', code: 'MIGRATION_NOTICE', message: '数据库迁移 0023 已应用；检测到未使用的索引', count: 1, first: 9000, last: 9000, resolved: true }),
  ]
  const byId = (id: string) => { const r = reports.find((x) => x.id === id); if (!r) throw new ApiError(404, 'NOT_FOUND', '错误报告不存在', 'mock-req'); return r }
  const brief = (r: ErrorReportDetail): ErrorReport => { const { stack: _s, details: _d, occurrences: _o, ...rest } = r; return rest }
  const open = () => reports.filter((r) => !r.resolved).length

  const api: MockDiagApi = {
    async getSystemInfo() {
      await delay(120)
      const total = 256 * GIB
      return {
        version: '0.9.0-demo', commit: 'a1b2c3d4', build_mode: 'debug', go_version: 'go1.24.2', os: 'windows/amd64', standalone: true,
        started_at: new Date(startedAt).toISOString(), uptime_seconds: Math.floor((Date.now() - startedAt) / 1000),
        database: { ok: true, message: '嵌入式 PostgreSQL 正常（迁移 0023）' }, worker: { ok: true, message: '文档解析 Worker 正常' },
        disk: { archive_path: 'D:\\OA归档', free_bytes: lowDisk ? 6 * GIB : 142 * GIB, total_bytes: total },
        last_exit_unclean: unclean, open_error_reports: open(),
      } satisfies SystemInfo
    },
    async listErrorReports(f) {
      await delay(180)
      const needle = f?.q?.toLowerCase()
      const rows = reports.filter((r) => (!f?.source || r.source === f.source) && (!f?.severity || r.severity === f.severity) && (f?.resolved === undefined || r.resolved === f.resolved)
        && (!needle || [r.message, r.code, r.request_id, ...r.occurrences.map((o) => o.request_id)].some((s) => s.toLowerCase().includes(needle))))
        .sort((a, b) => b.last_seen.localeCompare(a.last_seen))
      const page = f?.page ?? 1, size = f?.page_size ?? 20
      return { items: rows.slice((page - 1) * size, page * size).map(brief), total: rows.length, page, page_size: size } satisfies Page<ErrorReport>
    },
    async getErrorReport(id) { await delay(120); return structuredClone(byId(id)) },
    async updateErrorReport(id, resolved) { await delay(150); const r = byId(id); r.resolved = resolved; return structuredClone(r) },
    async deleteErrorReport(id) { await delay(150); const r = byId(id); reports.splice(reports.indexOf(r), 1) },
    async downloadDiagnosticBundle() {
      await delay(900)
      const text = ['OA 文件智能归档 · 诊断包（演示数据）', `生成时间：${new Date().toISOString()}`, '', '真实后端返回 zip，内含：版本/环境摘要、脱敏配置、最近日志、错误报告、迁移状态；绝不含密钥、令牌、密码、文档内容。', '', ...reports.map((r) => `[${r.severity}] ${r.source}/${r.code} ×${r.count} ${r.message}`)].join('\n')
      return { blob: new Blob([text], { type: 'text/plain;charset=utf-8' }), filename: 'oa-diagnostics-demo.txt' }
    },
    async reportClientError(input: ClientErrorInput) {
      await delay(80)
      if (!input.message || input.message.length > 500 || (input.stack ?? '').length > 4000) throw new ApiError(400, 'INVALID_ARGUMENT', '上报内容不合法', 'mock-req')
      const hit = reports.find((r) => r.source === 'client' && r.message === input.message && (r.details.route ?? '') === (input.route ?? '') && (r.details.component ?? '') === (input.component ?? ''))
      const rid = input.request_id || `req-${uid().slice(0, 8)}`
      if (hit) {
        hit.count++; hit.last_seen = new Date().toISOString(); hit.resolved = false; hit.request_id = rid
        hit.occurrences.unshift({ at: hit.last_seen, request_id: rid, user: '演示用户' }); hit.occurrences.length = Math.min(hit.occurrences.length, 20)
        return { report_id: hit.id }
      }
      const r = mk({ source: 'client', severity: 'error', code: 'CLIENT_ERROR', message: input.message, count: 1, first: 0, last: 0, stack: input.stack ?? '', details: { route: input.route ?? '', component: input.component ?? '', app_version: input.app_version ?? '' }, users: ['演示用户'] })
      r.request_id = rid; r.occurrences[0].request_id = rid
      reports.unshift(r)
      return { report_id: r.id }
    },
  }
  return { api }
}
