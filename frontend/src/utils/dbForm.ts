import type { DbConnRequest } from '../api/dbAdmin'
import { defaultPort } from '../api/dbAdmin'

export interface DbForm {
  provider: string; host: string; port: number | null; name: string; user: string; password: string
  schema: string; tls_mode: string; ca_pem: string; server_name: string; confirm_switch: boolean
}

export function emptyForm(provider = 'postgres'): DbForm {
  return { provider, host: '', port: defaultPort(provider), name: '', user: '', password: '', schema: '', tls_mode: 'require', ca_pem: '', server_name: '', confirm_switch: false }
}

/** 返回第一条校验错误（中文），无错误返回空串。 */
export function validateDbForm(f: DbForm, hasSavedPassword: boolean): string {
  if (!f.host.trim()) return '请填写主机地址'
  if (!f.port || f.port < 1 || f.port > 65535) return '端口应为 1-65535'
  if (!f.name.trim()) return '请填写数据库名'
  if (!f.user.trim()) return '请填写用户名'
  if (!f.password && !hasSavedPassword) return '请填写密码'
  return ''
}

/** 构造请求体：空密码且已有保存的密码时使用 use_saved_password，端口缺省取引擎默认值。 */
export function buildDbRequest(f: DbForm, hasSavedPassword: boolean): DbConnRequest {
  const r: DbConnRequest = { provider: f.provider as DbConnRequest['provider'], host: f.host.trim(), port: f.port ?? defaultPort(f.provider), name: f.name.trim(), user: f.user.trim(), tls_mode: f.tls_mode as DbConnRequest['tls_mode'] }
  if (f.password) r.password = f.password
  else if (hasSavedPassword) r.use_saved_password = true
  if (f.schema.trim()) r.schema = f.schema.trim()
  if (f.ca_pem.trim()) r.ca_pem = f.ca_pem.trim()
  if (f.server_name.trim()) r.server_name = f.server_name.trim()
  return r
}
