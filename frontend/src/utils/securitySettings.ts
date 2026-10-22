import type { SecuritySettings, SecuritySettingsPatch } from '../api'
import { originError, parseOrigins, rpIdError } from './setup'

export interface SettingsForm {
  rpId: string; rpName: string; originsText: string; mfaPolicy: SecuritySettings['mfa_policy']; allowPassword: boolean
  idle: number; max: number; minLen: number; lockThreshold: number; lockMinutes: number
}
export const toForm = (s: SecuritySettings): SettingsForm => ({
  rpId: s.rp_id, rpName: s.rp_name, originsText: s.origins.join('\n'), mfaPolicy: s.mfa_policy, allowPassword: s.allow_password_login,
  idle: s.session_idle_minutes, max: s.session_max_hours, minLen: s.password_min_length, lockThreshold: s.lockout_threshold, lockMinutes: s.lockout_minutes,
})

const inRange = (v: number, lo: number, hi: number) => Number.isInteger(v) && v >= lo && v <= hi

/** 返回字段 → 错误文案；空对象表示可提交。范围与契约 SecuritySettingsPatch 一致。 */
export function validateSettings(f: SettingsForm): Record<string, string> {
  const e: Record<string, string> = {}
  const origins = parseOrigins(f.originsText)
  if (!origins.length) e.origins = '至少一个来源'
  else { const bad = origins.map((o) => [o, originError(o)] as const).find((x) => x[1]); if (bad) e.origins = `${bad[0]}：${bad[1]}` }
  const rp = rpIdError(f.rpId.trim(), origins); if (rp) e.rpId = rp
  if (!inRange(f.idle, 5, 1440)) e.idle = '5–1440 分钟'
  if (!inRange(f.max, 1, 720)) e.max = '1–720 小时'
  if (!inRange(f.minLen, 8, 128)) e.minLen = '8–128'
  if (!inRange(f.lockThreshold, 3, 20)) e.lockThreshold = '3–20 次'
  if (!inRange(f.lockMinutes, 1, 1440)) e.lockMinutes = '1–1440 分钟'
  if (!e.idle && !e.max && f.idle > f.max * 60) e.idle = '空闲超时不能大于会话最长时长'
  return e
}

export function rpChanged(orig: SecuritySettings, f: SettingsForm): boolean {
  return f.rpId.trim() !== orig.rp_id || parseOrigins(f.originsText).join('|') !== orig.origins.join('|')
}

/** 只提交改动的字段；RP/来源变化时带 confirm_passkey_reset。 */
export function buildPatch(orig: SecuritySettings, f: SettingsForm, confirmReset: boolean): SecuritySettingsPatch {
  const p: SecuritySettingsPatch = {}
  if (f.rpId.trim() !== orig.rp_id) p.rp_id = f.rpId.trim()
  if (f.rpName.trim() !== orig.rp_name) p.rp_name = f.rpName.trim()
  const origins = parseOrigins(f.originsText)
  if (origins.join('|') !== orig.origins.join('|')) p.origins = origins
  if (f.mfaPolicy !== orig.mfa_policy) p.mfa_policy = f.mfaPolicy
  if (f.allowPassword !== orig.allow_password_login) p.allow_password_login = f.allowPassword
  if (f.idle !== orig.session_idle_minutes) p.session_idle_minutes = f.idle
  if (f.max !== orig.session_max_hours) p.session_max_hours = f.max
  if (f.minLen !== orig.password_min_length) p.password_min_length = f.minLen
  if (f.lockThreshold !== orig.lockout_threshold) p.lockout_threshold = f.lockThreshold
  if (f.lockMinutes !== orig.lockout_minutes) p.lockout_minutes = f.lockMinutes
  if (rpChanged(orig, f) && confirmReset) p.confirm_passkey_reset = true
  return p
}
