/** 存储连接的纯函数：表单可见性、载荷构造、校验与展示标签。界面由后端 kinds 目录驱动，这里不写死任何字段表。 */
import type {
  ReplicatePolicy, ReplicationStatus, StorageConnection, StorageConnectionInput, StorageConnectionPatch, StorageField, StorageKind, StorageTestInput,
} from '../api/types'
import type { Tone } from './format'

export type KindGroup = StorageKind['group']

export const GROUP_LABEL: Record<KindGroup, string> = { cloud: '云存储', enterprise: '企业内部存储', gateway: '网关' }
const GROUP_ORDER: KindGroup[] = ['cloud', 'enterprise', 'gateway']

/** 图标按 kind id 映射，未知类型回退通用图标。 */
const KIND_ICON: Record<string, string> = { s3: 'cloud', azure_blob: 'cloud_circle', webdav: 'lan', sftp: 'terminal', ftp: 'dns', smb: 'storage', dingtalk: 'forum', onedrive: 'cloud', googledrive: 'add_to_drive', dropbox: 'folder_shared', aliyundrive_open: 'cloud', baidu_netdisk: 'cloud', '123_open': 'cloud', openlist: 'hub', seafile: 'lan', cloudreve: 'lan', localdir: 'hard_drive' }
export function kindIcon(id: string): string { return KIND_ICON[id] ?? 'cloud_sync' }

export function groupLabel(g: string): string { return GROUP_LABEL[g as KindGroup] ?? g }

export function groupKinds(kinds: StorageKind[]): { group: KindGroup; label: string; kinds: StorageKind[] }[] {
  const out = GROUP_ORDER.map((g) => ({ group: g, label: GROUP_LABEL[g], kinds: kinds.filter((k) => k.group === g) })).filter((x) => x.kinds.length)
  const known = new Set<string>(GROUP_ORDER)
  const other = kinds.filter((k) => !known.has(k.group))
  if (other.length) out.push({ group: 'gateway', label: '其他', kinds: other })
  return out
}

export const REPLICATE_LABEL: Record<ReplicatePolicy, string> = { off: '关闭', manual: '手动', auto: '自动' }

export const REPLICATION_LABEL: Record<ReplicationStatus, string> = { queued: '排队', running: '进行中', done: '完成', skipped: '已存在', failed: '失败' }
export const REPLICATION_TONE: Record<ReplicationStatus, Tone> = { queued: 'neutral', running: 'progress', done: 'success', skipped: 'neutral', failed: 'error' }
export function isReplicationActive(s: ReplicationStatus): boolean { return s === 'queued' || s === 'running' }

function defaultOf(kind: StorageKind | undefined, key: string): string {
  return kind?.fields.find((f) => f.key === key)?.default ?? ''
}

export function isVisible(f: StorageField, values: Record<string, string>, kind?: StorageKind): boolean {
  if (!f.show_if) return true
  const cur = values[f.show_if.key] ?? defaultOf(kind, f.show_if.key)
  return cur === f.show_if.equals
}

export function visibleFields(kind: StorageKind, values: Record<string, string>): StorageField[] {
  return kind.fields.filter((f) => isVisible(f, values, kind))
}

/** 非凭据字段的默认值（含托管字段的空串）。 */
export function defaultSettings(kind: StorageKind): Record<string, string> {
  const out: Record<string, string> = {}
  for (const f of kind.fields) if (!f.secret) out[f.key] = f.default ?? ''
  return out
}

/** 与服务端一致：相对路径，不以 / 开头，不含 .. 段、反斜杠或控制字符。空串合法（= 根）。 */
export function validateBasePath(p: string): string | null {
  if (!p) return null
  if (p.startsWith('/')) return '起始目录不能以 / 开头，请填写相对路径'
  if (p.includes('\\')) return '起始目录不能包含反斜杠 \\'
  // eslint-disable-next-line no-control-regex
  if (/[\u0000-\u001f\u007f]/.test(p)) return '起始目录不能包含控制字符'
  if (p.split('/').some((seg) => seg === '..')) return '起始目录不能包含 ..'
  return null
}

export interface FormState {
  name: string
  base_path: string
  enabled: boolean
  use_for_import: boolean
  replicate: ReplicatePolicy
  settings: Record<string, string>
  /** 仅保存用户本次键入的凭据；已保存的永不回填。 */
  secrets: Record<string, string>
  /** 编辑时用户选择清除的已保存凭据。 */
  clearSecrets: string[]
}

export function emptyForm(kind: StorageKind): FormState {
  return { name: '', base_path: '', enabled: true, use_for_import: true, replicate: 'off', settings: defaultSettings(kind), secrets: {}, clearSecrets: [] }
}

export function formFromConnection(kind: StorageKind, c: StorageConnection): FormState {
  return {
    name: c.name, base_path: c.base_path, enabled: c.enabled, use_for_import: c.use_for_import, replicate: c.replicate,
    settings: { ...defaultSettings(kind), ...c.settings }, secrets: {}, clearSecrets: [],
  }
}

/** 某个凭据当前是否会有值：本次键入，或已保存且未被清除。 */
export function secretWillBeSet(key: string, form: FormState, saved?: StorageConnection | null): boolean {
  if ((form.secrets[key] ?? '') !== '') return true
  return !!saved?.secrets_set?.[key] && !form.clearSecrets.includes(key)
}

/** 返回字段键 → 错误文案。只检查必填（服务端仍是权威）。 */
export function validateForm(kind: StorageKind, form: FormState, saved?: StorageConnection | null): Record<string, string> {
  const errs: Record<string, string> = {}
  if (!form.name.trim()) errs.name = '请填写名称'
  const bp = validateBasePath(form.base_path)
  if (bp) errs.base_path = bp
  for (const f of visibleFields(kind, form.settings)) {
    if (!f.required || f.managed) continue
    if (f.secret) { if (!secretWillBeSet(f.key, form, saved)) errs[f.key] = `请填写${f.label}` }
    else if (!(form.settings[f.key] ?? '').trim()) errs[f.key] = `请填写${f.label}`
  }
  return errs
}

/** 仅包含可见、非凭据、非空的设置项。 */
export function cleanSettings(kind: StorageKind, form: FormState): Record<string, string> {
  const out: Record<string, string> = {}
  for (const f of visibleFields(kind, form.settings)) {
    if (f.secret) continue
    const v = (form.settings[f.key] ?? '').trim()
    if (v !== '') out[f.key] = v
  }
  return out
}

/** 仅包含可见凭据字段中键入的非空值。 */
export function typedSecrets(kind: StorageKind, form: FormState): Record<string, string> {
  const out: Record<string, string> = {}
  for (const f of visibleFields(kind, form.settings)) {
    if (!f.secret) continue
    const v = form.secrets[f.key] ?? ''
    if (v !== '') out[f.key] = v
  }
  return out
}

export function buildCreatePayload(kind: StorageKind, form: FormState): StorageConnectionInput {
  return {
    name: form.name.trim(), kind: kind.id, settings: cleanSettings(kind, form), secrets: typedSecrets(kind, form),
    base_path: form.base_path.trim(), enabled: form.enabled, use_for_import: form.use_for_import, replicate: form.replicate,
  }
}

/** 编辑：未改动的凭据不出现在载荷里；清除的凭据放入 clear_secrets（同时不再提交其新值）。 */
export function buildPatchPayload(kind: StorageKind, form: FormState, saved: StorageConnection): StorageConnectionPatch {
  const secrets = typedSecrets(kind, form)
  const clear = form.clearSecrets.filter((k) => saved.secrets_set?.[k] && !(k in secrets))
  const patch: StorageConnectionPatch = {
    name: form.name.trim(), settings: cleanSettings(kind, form), base_path: form.base_path.trim(),
    enabled: form.enabled, use_for_import: form.use_for_import, replicate: form.replicate,
  }
  if (Object.keys(secrets).length) patch.secrets = secrets
  if (clear.length) patch.clear_secrets = clear
  return patch
}

/** 测试请求：编辑时带 connection_id 让服务端补用已保存的凭据，不在前端回填。 */
export function buildTestPayload(kind: StorageKind, form: FormState, opts: { connectionId?: string; acceptHostKey?: string }): StorageTestInput {
  const input: StorageTestInput = {
    kind: kind.id, settings: cleanSettings(kind, form), secrets: typedSecrets(kind, form), base_path: form.base_path.trim(), check_write: form.replicate !== 'off',
  }
  if (opts.connectionId) input.connection_id = opts.connectionId
  if (opts.acceptHostKey) input.accept_host_key = opts.acceptHostKey
  return input
}

/** 钉钉钉盘等有调用额度的类型给出批量导入提示。 */
export function quotaHint(kindId: string): string | null {
  if (kindId === 'dingtalk') return '钉钉标准版下载信息接口每月约 10,000 次调用额度，批量导入大量文件会消耗额度；仅支持企业内部应用。'
  return null
}

export const KIND_HINTS: Record<string, string> = {
  webdav: '可连接 OpenList/AList 的 /dav 访问其支持的各类网盘',
  dingtalk: '仅 企业内部应用 可用；标准版下载接口每月有调用额度（约 10,000 次），批量导入请留意。',
}

export function replicationSummary(c: Pick<StorageConnection, 'replication'>): string {
  const r = c.replication
  const parts: string[] = []
  if (r.queued) parts.push(`排队 ${r.queued}`)
  if (r.running) parts.push(`进行中 ${r.running}`)
  parts.push(`完成 ${r.done}`)
  if (r.skipped) parts.push(`已存在 ${r.skipped}`)
  if (r.failed) parts.push(`失败 ${r.failed}`)
  return parts.join(' · ')
}

export const REMOTE_IMPORT_EXT = /\.(docx|xlsx|pdf|png|jpe?g)$/i

export function crumbs(path: string): { name: string; path: string }[] {
  const segs = path.split('/').filter(Boolean)
  return segs.map((s, i) => ({ name: s, path: segs.slice(0, i + 1).join('/') }))
}
