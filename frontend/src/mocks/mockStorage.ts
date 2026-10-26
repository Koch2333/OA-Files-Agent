/**
 * 存储连接的演示实现（仅演示数据）：内存连接、虚拟远端目录树、测试结果、同步进度模拟，
 * 校验规则与服务端一致（必填 → 422、重名 → 409）。凭据永不回显。
 */
import { uuid } from '../utils/uuid'
import {
  ApiError, type ImportJob, type ImportOptions, type Page, type PickedFile, type Replication, type ReplicationQuery, type ReplicationStatus, type StorageConnection,
  type StorageConnectionInput, type StorageConnectionPatch, type StorageField, type StorageKind, type StorageListing, type StorageTestInput,
  type StorageTestResult,
} from '../api/types'
import { isVisible, validateBasePath } from '../utils/storage'

type FieldOpts = Partial<Omit<StorageField, 'key' | 'label' | 'type'>>
const F = (key: string, label: string, type: StorageField['type'], o: FieldOpts = {}): StorageField => ({
  key, label, type, required: false, secret: false, default: '', placeholder: '', help: '', options: [], show_if: null, managed: false, ...o,
})
const opts = (...p: [string, string][]) => p.map(([value, label]) => ({ value, label }))
const when = (key: string, equals: string) => ({ key, equals })

export const MOCK_KINDS: StorageKind[] = [
  {
    id: 's3', name: 'S3 兼容对象存储', group: 'cloud', docs_url: '',
    description: '阿里云 OSS、腾讯云 COS、华为云 OBS、AWS S3、MinIO、Cloudflare R2 等。',
    fields: [
      F('endpoint', '服务地址 Endpoint', 'text', { placeholder: 'oss-cn-hangzhou.aliyuncs.com', help: '留空表示 AWS S3。' }),
      F('region', '区域 Region', 'text', { placeholder: 'cn-hangzhou' }),
      F('bucket', '存储桶 Bucket', 'text', { required: true }),
      F('use_ssl', '使用 HTTPS', 'select', { default: 'true', options: opts(['true', '是'], ['false', '否']) }),
      F('path_style', '路径风格', 'select', { default: 'auto', options: opts(['auto', '自动'], ['true', '路径风格'], ['false', '虚拟主机风格']), help: '自动：OSS/COS/OBS 用虚拟主机风格，IP/MinIO 用路径风格。' }),
      F('insecure_skip_verify', '跳过证书校验', 'select', { default: 'false', options: opts(['false', '否（推荐）'], ['true', '是（仅内网自签名证书）']) }),
      F('access_key', 'Access Key', 'text', { required: true, secret: true }),
      F('secret_key', 'Secret Key', 'password', { required: true, secret: true }),
      F('session_token', 'Session Token', 'password', { secret: true, help: '仅使用临时凭据时填写。' }),
    ],
  },
  {
    id: 'azure_blob', name: 'Azure Blob 存储', group: 'cloud', docs_url: '',
    description: 'Microsoft Azure Blob Storage（含世纪互联）。',
    fields: [
      F('account', '存储账户', 'text', { required: true }),
      F('container', '容器', 'text', { required: true }),
      F('endpoint', '服务地址', 'text', { placeholder: 'https://<account>.blob.core.chinacloudapi.cn', help: '可选，世纪互联需填写。' }),
      F('auth', '认证方式', 'select', { default: 'account_key', options: opts(['account_key', '账户密钥'], ['sas', 'SAS 令牌']) }),
      F('account_key', '账户密钥', 'password', { required: true, secret: true, show_if: when('auth', 'account_key') }),
      F('sas_token', 'SAS 令牌', 'password', { required: true, secret: true, show_if: when('auth', 'sas') }),
    ],
  },
  {
    id: 'webdav', name: 'WebDAV', group: 'gateway', docs_url: '',
    description: 'Nextcloud/ownCloud、群晖、SharePoint 文档库，或 OpenList/AList 网关。',
    fields: [
      F('url', '地址 URL', 'text', { required: true, placeholder: 'http://host:5244/dav', help: '填写完整 URL，OpenList/AList 为 http://主机:5244/dav。' }),
      F('auth', '认证方式', 'select', { default: 'basic', options: opts(['basic', '用户名 + 密码'], ['bearer', 'Bearer 令牌']) }),
      F('username', '用户名', 'text', { show_if: when('auth', 'basic') }),
      F('insecure_skip_verify', '跳过证书校验', 'select', { default: 'false', options: opts(['false', '否（推荐）'], ['true', '是（仅内网自签名证书）']) }),
      F('password', '密码', 'password', { secret: true, show_if: when('auth', 'basic') }),
      F('token', '令牌', 'password', { required: true, secret: true, show_if: when('auth', 'bearer') }),
    ],
  },
  {
    id: 'sftp', name: 'SFTP（SSH）', group: 'enterprise', docs_url: '',
    description: '通过 SSH 访问的文件服务器；首次连接需核对主机指纹。',
    fields: [
      F('host', '主机', 'text', { required: true, placeholder: 'files.example.com' }),
      F('port', '端口', 'number', { default: '22' }),
      F('username', '用户名', 'text', { required: true }),
      F('auth', '认证方式', 'select', { default: 'password', options: opts(['password', '密码'], ['key', '私钥']) }),
      F('host_key', '已信任的主机指纹', 'text', { managed: true, help: '首次连接核对后由系统写入，不可手工编辑。' }),
      F('password', '密码', 'password', { required: true, secret: true, show_if: when('auth', 'password') }),
      F('private_key', '私钥（PEM）', 'textarea', { required: true, secret: true, show_if: when('auth', 'key') }),
      F('passphrase', '私钥口令', 'password', { secret: true, show_if: when('auth', 'key') }),
    ],
  },
  {
    id: 'ftp', name: 'FTP / FTPS', group: 'enterprise', docs_url: '',
    description: '传统 FTP 服务器，建议使用 FTPS。',
    fields: [
      F('host', '主机', 'text', { required: true }),
      F('port', '端口', 'number', { default: '21' }),
      F('tls', '加密', 'select', { default: 'explicit', options: opts(['none', '不加密'], ['explicit', '显式 TLS（FTPES）'], ['implicit', '隐式 TLS（FTPS）']) }),
      F('username', '用户名', 'text'),
      F('insecure_skip_verify', '跳过证书校验', 'select', { default: 'false', options: opts(['false', '否（推荐）'], ['true', '是']) }),
      F('password', '密码', 'password', { secret: true }),
    ],
  },
  {
    id: 'smb', name: 'SMB 共享', group: 'enterprise', docs_url: '',
    description: 'Windows 文件服务器、NAS 共享（仅 SMB2/3）。',
    fields: [
      F('host', '主机', 'text', { required: true }),
      F('port', '端口', 'number', { default: '445' }),
      F('share', '共享名', 'text', { required: true }),
      F('domain', '域', 'text'),
      F('username', '用户名', 'text', { required: true }),
      F('password', '密码', 'password', { secret: true }),
    ],
  },
  {
    id: 'dingtalk', name: '钉钉钉盘', group: 'enterprise', docs_url: '',
    description: '钉钉企业存储（官方 API，企业内部应用）。',
    fields: [
      F('app_key', 'AppKey', 'text', { required: true }),
      F('space_id', '空间 ID', 'text', { required: true }),
      F('union_id', '操作人 unionId', 'text', { required: true, help: '须为组织成员，且对该空间有读写权限。' }),
      F('endpoint', '接口地址', 'text', { default: 'https://api.dingtalk.com' }),
      F('app_secret', 'AppSecret', 'password', { required: true, secret: true }),
    ],
  },
]

interface RNode { name: string; size?: number; children?: RNode[] }
const file = (name: string, size: number): RNode => ({ name, size })
const dir = (name: string, ...children: RNode[]): RNode => ({ name, children })
const TREE: RNode = dir('',
  dir('合同扫描件', file('采购合同-2026-001.pdf', 812_345), file('框架协议.docx', 64_200), dir('2025', file('历史合同.pdf', 1_204_000), file('签章页.png', 345_000))),
  dir('财务报表', file('一季度报表.xlsx', 98_400), file('三月薪酬表.xlsx', 55_800), file('备份说明.txt', 1_200)),
  dir('行政通知', file('关于调整作息的通知.docx', 31_900), file('现场照片.jpg', 2_400_000)),
  dir('历史归档', ...Array.from({ length: 120 }, (_, i) => file(`批次-${i + 1}.pdf`, 50_000))),
  file('目录说明.docx', 18_000), file('旧系统导出.rar', 5_000_000),
)

/** 演示：每个连接的起始目录下都是同一棵虚拟目录树，故 base 不参与查找。 */
function walk(_base: string, rel: string): RNode | null {
  let cur: RNode = TREE
  for (const seg of rel.split('/').filter(Boolean)) {
    const next = cur.children?.find((c) => c.name === seg)
    if (!next) return null
    cur = next
  }
  return cur
}
function countFiles(n: RNode): number { return n.children ? n.children.reduce((s, c) => s + countFiles(c), 0) : 1 }
function flatten(n: RNode, prefix: string, out: { path: string; size: number }[]) {
  if (n.children) n.children.forEach((c) => flatten(c, prefix ? `${prefix}/${c.name}` : c.name, out))
  else out.push({ path: prefix, size: n.size ?? 0 })
}

/** 演示用确定性指纹：同一主机名永远得到同一指纹。 */
export function mockFingerprint(host: string): string {
  let h = 2166136261
  const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/'
  let s = ''
  for (let i = 0; i < 43; i++) {
    h = Math.imul(h ^ (host.charCodeAt(i % Math.max(1, host.length)) + i), 16777619) >>> 0
    s += alphabet[h % 64]
  }
  return 'SHA256:' + s
}

const bad = (status: number, code: string, message: string) => new ApiError(status, code, message)

export interface MockStorageOptions {
  /** 每次调用的模拟延迟（毫秒）。 */
  delay?: number
  /** 同步状态推进的时间步（毫秒）。 */
  step?: number
  importFn: (files: PickedFile[], opts?: { options?: ImportOptions }) => Promise<ImportJob>
  /** 已归档文件的本机路径，供“同步缺失项”使用。 */
  archivedPaths?: () => string[]
  seed?: boolean
}

export class MockStorage {
  readonly kinds = MOCK_KINDS
  private conns = new Map<string, StorageConnection>()
  private reps: Replication[] = []
  private secrets = new Map<string, Record<string, string>>()
  private idem = new Map<string, ImportJob>()
  private delayMs: number
  private step: number

  constructor(private o: MockStorageOptions) {
    this.delayMs = o.delay ?? 200
    this.step = o.step ?? 1200
    if (o.seed !== false) this.seed()
  }

  private wait() { return this.delayMs > 0 ? new Promise<void>((r) => setTimeout(r, this.delayMs)) : Promise.resolve() }
  private kind(id: string): StorageKind {
    const k = this.kinds.find((x) => x.id === id)
    if (!k) throw bad(422, 'INVALID_ARGUMENT', `不支持的存储类型：${id}`)
    return k
  }
  private get(id: string): StorageConnection {
    const c = this.conns.get(id)
    if (!c) throw bad(404, 'NOT_FOUND', '存储连接不存在')
    return c
  }
  private view(c: StorageConnection): StorageConnection {
    const rep = { queued: 0, running: 0, done: 0, skipped: 0, failed: 0 }
    for (const r of this.reps) if (r.connection_id === c.id) rep[r.status]++
    // settings 只含非凭据；secrets_set 只含布尔
    return { ...c, settings: { ...c.settings }, secrets_set: { ...c.secrets_set }, replication: rep }
  }
  private seed() {
    const mk = (name: string, kind: string, settings: Record<string, string>, secrets: Record<string, string>, extra: Partial<StorageConnection>, daysAgo: number) => {
      const id = uuid()
      const at = new Date(Date.now() - daysAgo * 864e5).toISOString()
      this.conns.set(id, {
        id, name, kind, enabled: true, base_path: '', use_for_import: true, replicate: 'off', settings, secrets_set: Object.fromEntries(Object.keys(secrets).map((k) => [k, true])),
        last_test: null, replication: { queued: 0, running: 0, done: 0, skipped: 0, failed: 0 }, created_at: at, updated_at: at, ...extra,
      })
      this.secrets.set(id, secrets)
      return id
    }
    const t = (h: number, ok: boolean, message: string) => ({ ok, at: new Date(Date.now() - h * 36e5).toISOString(), message })
    const s3 = mk('阿里云 OSS 归档桶', 's3', { endpoint: 'oss-cn-hangzhou.aliyuncs.com', region: 'cn-hangzhou', bucket: 'oa-archive', use_ssl: 'true', path_style: 'auto' }, { access_key: 'AK-demo', secret_key: 'SK-demo' },
      { base_path: '归档备份', replicate: 'auto', last_test: t(5, true, '连接正常（读写均通过）') }, 12)
    mk('文件服务器 SFTP', 'sftp', { host: 'files.corp.local', port: '22', username: 'oa', auth: 'password', host_key: mockFingerprint('files.corp.local') }, { password: 'demo' },
      { base_path: '归档', replicate: 'manual', last_test: t(30, false, '读取目录失败：权限不足') }, 6)
    mk('OpenList 网关', 'webdav', { url: 'http://openlist.corp.local:5244/dav', auth: 'basic', username: 'oa' }, { password: 'demo' },
      { base_path: '', use_for_import: true, last_test: null }, 2)
    const mkRep = (cid: string, name: string, status: ReplicationStatus, error: string | null, mins: number): Replication => ({
      id: uuid(), connection_id: cid, connection_name: name, local_path: `归档/财务部/2026/报表/${mins}-一季度报表.xlsx`, remote_path: `归档备份/财务部/2026/报表/${mins}-一季度报表.xlsx`,
      status, attempts: status === 'failed' ? 3 : 1, error, size_bytes: 98_400 + mins * 100, updated_at: new Date(Date.now() - mins * 6e4).toISOString(),
    })
    this.reps.push(mkRep(s3, '阿里云 OSS 归档桶', 'done', null, 40), mkRep(s3, '阿里云 OSS 归档桶', 'skipped', null, 35), mkRep(s3, '阿里云 OSS 归档桶', 'failed', '远端拒绝写入：权限不足（AccessDenied）', 20))
  }

  // ---- 校验 ----
  private validate(kind: StorageKind, name: string, base: string, settings: Record<string, string>, secretsSet: (key: string) => boolean, selfId?: string) {
    if (!name.trim()) throw bad(422, 'INVALID_ARGUMENT', '名称不能为空')
    if ([...this.conns.values()].some((c) => c.id !== selfId && c.name === name.trim())) throw bad(409, 'CONFLICT', `已存在名为“${name.trim()}”的连接`)
    const bp = validateBasePath(base)
    if (bp) throw bad(422, 'INVALID_ARGUMENT', bp)
    for (const f of kind.fields) {
      if (!isVisible(f, { ...Object.fromEntries(kind.fields.map((x) => [x.key, x.default])), ...settings }, kind) || !f.required || f.managed) continue
      const has = f.secret ? secretsSet(f.key) : !!(settings[f.key] ?? '').trim()
      if (!has) throw bad(422, 'INVALID_ARGUMENT', `${f.label}为必填项`)
    }
    for (const k of Object.keys(settings)) if (!kind.fields.some((f) => f.key === k && !f.secret)) throw bad(422, 'INVALID_ARGUMENT', `未知的设置项：${k}`)
  }
  private splitSettings(kind: StorageKind, settings: Record<string, string> | undefined): Record<string, string> {
    const out: Record<string, string> = {}
    for (const [k, v] of Object.entries(settings ?? {})) if (v !== '') out[k] = v
    void kind
    return out
  }

  // ---- 连接 CRUD ----
  async listKinds() { await this.wait(); return this.kinds }
  async list() { await this.wait(); return [...this.conns.values()].sort((a, b) => a.created_at.localeCompare(b.created_at)).map((c) => this.view(c)) }
  async getConn(id: string) { await this.wait(); return this.view(this.get(id)) }

  async create(input: StorageConnectionInput): Promise<StorageConnection> {
    await this.wait()
    const kind = this.kind(input.kind)
    const settings = this.splitSettings(kind, input.settings)
    const secrets = Object.fromEntries(Object.entries(input.secrets ?? {}).filter(([, v]) => v !== ''))
    for (const k of Object.keys(secrets)) if (!kind.fields.some((f) => f.key === k && f.secret)) throw bad(422, 'INVALID_ARGUMENT', `未知的凭据项：${k}`)
    this.validate(kind, input.name, input.base_path ?? '', settings, (k) => k in secrets)
    const id = uuid()
    const at = new Date().toISOString()
    this.conns.set(id, {
      id, name: input.name.trim(), kind: kind.id, enabled: input.enabled ?? true, base_path: input.base_path ?? '', use_for_import: input.use_for_import ?? true, replicate: input.replicate ?? 'off',
      settings, secrets_set: Object.fromEntries(Object.keys(secrets).map((k) => [k, true])), last_test: null, replication: { queued: 0, running: 0, done: 0, skipped: 0, failed: 0 }, created_at: at, updated_at: at,
    })
    this.secrets.set(id, secrets)
    return this.view(this.conns.get(id)!)
  }

  async update(id: string, patch: StorageConnectionPatch): Promise<StorageConnection> {
    await this.wait()
    const cur = this.get(id)
    const kind = this.kind(cur.kind)
    const settings = patch.settings !== undefined ? this.splitSettings(kind, patch.settings) : cur.settings
    const secrets = { ...(this.secrets.get(id) ?? {}) }
    for (const [k, v] of Object.entries(patch.secrets ?? {})) {
      if (!kind.fields.some((f) => f.key === k && f.secret)) throw bad(422, 'INVALID_ARGUMENT', `未知的凭据项：${k}`)
      if (v !== '') secrets[k] = v
    }
    for (const k of patch.clear_secrets ?? []) delete secrets[k]
    const name = patch.name ?? cur.name
    const base = patch.base_path ?? cur.base_path
    this.validate(kind, name, base, settings, (k) => k in secrets, id)
    const next: StorageConnection = {
      ...cur, name: name.trim(), base_path: base, settings, enabled: patch.enabled ?? cur.enabled, use_for_import: patch.use_for_import ?? cur.use_for_import,
      replicate: patch.replicate ?? cur.replicate, secrets_set: Object.fromEntries(Object.keys(secrets).map((k) => [k, true])), updated_at: new Date().toISOString(),
    }
    this.conns.set(id, next)
    this.secrets.set(id, secrets)
    return this.view(next)
  }

  async remove(id: string): Promise<void> {
    await this.wait()
    this.get(id)
    this.conns.delete(id)
    this.secrets.delete(id)
    this.reps = this.reps.filter((r) => r.connection_id !== id)
  }

  // ---- 测试 ----
  private probe(kind: StorageKind, settings: Record<string, string>, secrets: Record<string, string>, base: string, checkWrite: boolean, accept?: string): StorageTestResult {
    const latency = 40 + Math.floor(Math.random() * 120)
    const host = settings.host || settings.url || settings.endpoint || settings.bucket || ''
    const fail = (name: string, message: string, extra: Partial<StorageTestResult> = {}): StorageTestResult => ({ ok: false, latency_ms: latency, host_key: null, steps: [{ name, ok: false, message }], ...extra })
    if (/unreachable|down|offline/i.test(host)) return fail('连接', '无法连接到远端：连接超时，请检查地址、端口与网络')
    if (kind.id === 'sftp') {
      const fp = mockFingerprint(settings.host ?? '')
      const pinned = settings.host_key
      const changed = /changed/i.test(settings.host ?? '')
      if (changed && pinned && !accept) return { ok: false, latency_ms: latency, host_key: { fingerprint: mockFingerprint('changed-' + settings.host), changed: true }, steps: [{ name: '连接', ok: false, message: '主机密钥与已信任的指纹不一致，已拒绝连接' }] }
      if (!pinned && accept !== fp) return { ok: false, latency_ms: latency, host_key: { fingerprint: fp, changed: false }, steps: [{ name: '连接', ok: false, message: '首次连接：需要先确认主机指纹' }] }
    }
    const authBad = Object.values(secrets).some((v) => /^(wrong|bad)/i.test(v))
    if (authBad) return fail('连接', '认证失败：用户名或凭据不正确')
    if (base && /noaccess/i.test(base)) return { ok: false, latency_ms: latency, host_key: null, steps: [{ name: '连接', ok: true, message: '已连接' }, { name: '读取目录', ok: false, message: '起始目录不存在或没有读取权限' }] }
    const steps = [{ name: '连接', ok: true, message: `已连接（${latency} ms）` }, { name: '读取目录', ok: true, message: '可以列出起始目录' }]
    if (checkWrite) {
      const ro = /readonly/i.test(base) || kind.id === 'ftp'
      steps.push({ name: '写入权限', ok: !ro, message: ro ? '没有写入权限：同步需要写入权限，请调整账号权限或将同步策略设为关闭' : '可以写入（已创建并删除测试文件）' })
      return { ok: !ro, latency_ms: latency, host_key: null, steps }
    }
    return { ok: true, latency_ms: latency, host_key: null, steps }
  }

  async testDraft(input: StorageTestInput): Promise<StorageTestResult> {
    await this.wait()
    const kind = this.kind(input.kind)
    const bp = validateBasePath(input.base_path ?? '')
    if (bp) throw bad(422, 'INVALID_ARGUMENT', bp)
    const saved = input.connection_id ? this.get(input.connection_id) : null
    const secrets = { ...(saved ? this.secrets.get(saved.id) : {}), ...(input.secrets ?? {}) }
    const settings = { ...(input.settings ?? {}) }
    for (const f of kind.fields) {
      if (!isVisible(f, { ...Object.fromEntries(kind.fields.map((x) => [x.key, x.default])), ...settings }, kind) || !f.required || f.managed) continue
      if (f.secret ? !(f.key in secrets) : !(settings[f.key] ?? '').trim()) throw bad(422, 'INVALID_ARGUMENT', `${f.label}为必填项`)
    }
    return this.probe(kind, settings, secrets, input.base_path ?? '', !!input.check_write, input.accept_host_key)
  }

  async testConn(id: string, o?: { check_write?: boolean; accept_host_key?: string }): Promise<StorageTestResult> {
    await this.wait()
    const c = this.get(id)
    const res = this.probe(this.kind(c.kind), c.settings, this.secrets.get(id) ?? {}, c.base_path, !!o?.check_write, o?.accept_host_key)
    let settings = c.settings
    // 用户确认指纹后服务器固定信任
    if (c.kind === 'sftp' && o?.accept_host_key && !c.settings.host_key && res.host_key === null) settings = { ...settings, host_key: o.accept_host_key }
    const message = res.ok ? '连接正常' : (res.steps.find((s) => !s.ok)?.message ?? '测试失败')
    this.conns.set(id, { ...c, settings, last_test: { ok: res.ok, at: new Date().toISOString(), message } })
    return res
  }

  // ---- 浏览 ----
  async browse(id: string, path = ''): Promise<StorageListing> {
    await this.wait()
    const c = this.get(id)
    if (path.startsWith('/') || path.includes('\\') || path.split('/').includes('..')) throw bad(400, 'INVALID_ARGUMENT', '路径不合法')
    const host = c.settings.host || c.settings.url || ''
    if (/unreachable|down|offline/i.test(host) || /^(unreachable)/i.test(c.name)) throw bad(502, 'UPSTREAM_ERROR', '无法连接到远端存储，请检查网络与连接设置后重试')
    const node = walk(c.base_path, path)
    if (!node || !node.children) throw bad(404, 'NOT_FOUND', '远端目录不存在')
    const segs = path.split('/').filter(Boolean)
    const entries = node.children.map((n) => ({
      name: n.name, path: [...segs, n.name].join('/'), is_dir: !!n.children, size_bytes: n.children ? 0 : (n.size ?? 0),
      modified_at: n.children ? null : new Date(Date.now() - (n.name.length * 3 + 1) * 864e5).toISOString(),
    })).sort((a, b) => Number(b.is_dir) - Number(a.is_dir) || a.name.localeCompare(b.name, 'zh'))
    return { path: segs.join('/'), parent: segs.length ? segs.slice(0, -1).join('/') : null, entries, truncated: false }
  }

  // ---- 同步 ----
  async replicate(id: string, o?: { path_prefix?: string }): Promise<{ queued: number }> {
    await this.wait()
    const c = this.get(id)
    if (c.replicate === 'off') throw bad(409, 'CONFLICT', '该连接的同步策略为“关闭”，请先在编辑中开启')
    if (!c.enabled) throw bad(409, 'CONFLICT', '该连接已停用')
    const have = new Set(this.reps.filter((r) => r.connection_id === id).map((r) => r.local_path))
    const prefix = o?.path_prefix ?? ''
    const pool = (this.o.archivedPaths?.() ?? []).filter((p) => p.startsWith(prefix))
    const fresh = pool.filter((p) => !have.has(p))
    const paths = fresh.length ? fresh : Array.from({ length: 3 }, (_, i) => `${prefix || '归档/演示'}/补同步-${Date.now() % 10000}-${i + 1}.pdf`)
    paths.forEach((p, i) => {
      const r: Replication = { id: uuid(), connection_id: id, connection_name: c.name, local_path: p, remote_path: [c.base_path, p.replace(/^归档\//, '')].filter(Boolean).join('/'), status: 'queued', attempts: 0, error: null, size_bytes: 40_000 + i * 7_000, updated_at: new Date().toISOString() }
      this.reps.unshift(r)
      this.advance(r, i % 3 === 2)
    })
    return { queued: paths.length }
  }

  /** 排队 → 进行中 → 完成/失败（演示：每三个中有一个失败）。 */
  private advance(r: Replication, willFail: boolean) {
    const upd = (status: ReplicationStatus, error: string | null = null) => {
      const cur = this.reps.find((x) => x.id === r.id)
      if (!cur) return
      cur.status = status; cur.error = error; cur.updated_at = new Date().toISOString()
      if (status === 'running') cur.attempts++
    }
    setTimeout(() => upd('running'), this.step)
    setTimeout(() => (willFail ? upd('failed', '远端写入失败：连接被重置，将保留供手动重试') : upd('done')), this.step * 2.2)
  }

  async listReps(q?: ReplicationQuery): Promise<Page<Replication>> {
    await this.wait()
    const all = this.reps.filter((r) => (!q?.connection_id || r.connection_id === q.connection_id) && (!q?.status || r.status === q.status)).sort((a, b) => b.updated_at.localeCompare(a.updated_at))
    const size = q?.page_size ?? 20
    const page = q?.page ?? 1
    return { items: all.slice((page - 1) * size, page * size).map((r) => ({ ...r })), total: all.length, page, page_size: size }
  }

  async retry(id: string): Promise<Replication> {
    await this.wait()
    const r = this.reps.find((x) => x.id === id)
    if (!r) throw bad(404, 'NOT_FOUND', '同步记录不存在')
    if (r.status !== 'failed') throw bad(409, 'CONFLICT', '仅失败的记录可以重试')
    r.status = 'queued'; r.error = null; r.updated_at = new Date().toISOString()
    this.advance(r, false)
    return { ...r }
  }

  // ---- 远端导入 ----
  async remoteImport(connectionId: string, paths: string[], opts: { idempotencyKey?: string; templateId?: string | null; options?: ImportOptions }): Promise<ImportJob> {
    await this.wait()
    const c = this.get(connectionId)
    if (opts.idempotencyKey && this.idem.has(opts.idempotencyKey)) return this.idem.get(opts.idempotencyKey)!
    if (!paths.length) throw bad(400, 'INVALID_ARGUMENT', '未选择任何文件或文件夹')
    if (!c.enabled || !c.use_for_import) throw bad(409, 'CONFLICT', '该连接未启用或未开启“用于导入”')
    const picked: { path: string; size: number; rel: string }[] = []
    let total = 0
    for (const p of paths) {
      if (p.startsWith('/') || p.includes('\\') || p.split('/').includes('..')) throw bad(400, 'INVALID_ARGUMENT', `路径不合法：${p}`)
      const node = walk(c.base_path, p)
      if (!node) throw bad(404, 'NOT_FOUND', `远端路径不存在：${p}`)
      total += countFiles(node)
      const flat: { path: string; size: number }[] = []
      flatten(node, p, flat)
      const parent = p.includes('/') ? p.slice(0, p.lastIndexOf('/') + 1) : ''
      flat.forEach((f) => picked.push({ ...f, rel: f.path.slice(parent.length) }))
    }
    const files: PickedFile[] = picked.map((f) => {
      const blob = new File([], f.path.split('/').pop() ?? 'file')
      Object.defineProperty(blob, 'size', { value: Math.min(f.size, 20 * 1024 * 1024) })
      return { file: blob, relativePath: f.rel }
    })
    const job = await this.o.importFn(files, { options: opts.options })
    if (opts.idempotencyKey) this.idem.set(opts.idempotencyKey, job)
    return job
  }
}
