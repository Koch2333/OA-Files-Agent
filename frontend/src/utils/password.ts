/** 密码强度提示（仅前端引导；真正的策略由后端校验）。纯函数，便于测试。 */
export interface Strength { score: 0 | 1 | 2 | 3 | 4; label: string; issues: string[]; ok: boolean }

const LABELS = ['很弱', '较弱', '一般', '较强', '很强'] as const
const COMMON = ['password', 'passw0rd', '123456', '12345678', 'qwerty', 'admin', 'letmein', 'welcome', 'iloveyou', '111111', 'abc123']

export function passwordStrength(pw: string, minLength: number, username = ''): Strength {
  const issues: string[] = []
  if (!pw) return { score: 0, label: LABELS[0], issues: [`至少 ${minLength} 个字符`], ok: false }
  const classes = [/[a-z]/, /[A-Z]/, /\d/, /[^A-Za-z0-9]/].filter((r) => r.test(pw)).length
  const lower = pw.toLowerCase()
  const uniq = new Set(pw).size
  let score = 0
  if (pw.length >= minLength) score++
  if (pw.length >= minLength + 4) score++
  if (classes >= 3) score++
  if (classes >= 2 && uniq >= Math.min(8, Math.ceil(pw.length * 0.6))) score++
  if (pw.length < minLength) issues.push(`至少 ${minLength} 个字符（当前 ${pw.length}）`)
  if (classes < 2) issues.push('建议混合字母、数字或符号')
  if (uniq < Math.ceil(pw.length / 3)) { issues.push('重复字符过多'); score = Math.min(score, 1) }
  if (COMMON.some((c) => lower.includes(c))) { issues.push('包含常见弱口令'); score = Math.min(score, 1) }
  if (username.length >= 3 && lower.includes(username.toLowerCase())) { issues.push('不要包含用户名'); score = Math.min(score, 1) }
  if (pw.length < minLength) score = Math.min(score, 1)
  const s = Math.max(0, Math.min(4, score)) as Strength['score']
  return { score: s, label: LABELS[s], issues, ok: pw.length >= minLength && s >= 2 && !issues.some((i) => i.startsWith('包含') || i.startsWith('不要') || i.startsWith('重复')) }
}
