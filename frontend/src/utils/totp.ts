/** TOTP / 恢复码输入处理。 */
export function normalizeTotpInput(raw: string, digits = 6): string {
  return raw.replace(/\D/g, '').slice(0, digits)
}
export const isCompleteTotp = (v: string, digits = 6) => v.length === digits && /^\d+$/.test(v)

/** 仅在“刚好补满”的那一次触发自动提交，避免重复提交。 */
export function shouldAutoSubmit(prev: string, next: string, busy: boolean, digits = 6): boolean {
  return !busy && next.length === digits && prev.length < digits && /^\d+$/.test(next)
}

/** 恢复码：去空白、转大写，保留连字符。 */
export function normalizeRecoveryCode(raw: string): string {
  return raw.replace(/\s+/g, '').toUpperCase()
}

/** 手动密钥 4 位一组展示。 */
export function formatManualKey(secret: string): string {
  return secret.replace(/\s+/g, '').replace(/(.{4})/g, '$1 ').trim()
}
