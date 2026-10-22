/** “只显示一次”的敏感内容（临时密码、邀请链接、恢复码）的状态：关闭前必须确认已保存，关闭后立即清除。 */
export type OneTimeKind = 'password' | 'invite' | 'recovery'
export interface OneTimeState { kind: OneTimeKind; title: string; values: string[]; acknowledged: boolean; copied: boolean }

export function openOneTime(kind: OneTimeKind, title: string, values: string[]): OneTimeState {
  return { kind, title, values: [...values], acknowledged: false, copied: false }
}
export function markCopied(s: OneTimeState): OneTimeState { return { ...s, copied: true } }
export function setAcknowledged(s: OneTimeState, v: boolean): OneTimeState { return { ...s, acknowledged: v } }
/** 恢复码强制勾选；其余在复制过或勾选后可关闭。 */
export function canClose(s: OneTimeState): boolean { return s.acknowledged || (s.kind !== 'recovery' && s.copied) }
/** 返回清空后的状态（不保留明文）。 */
export function wipe(s: OneTimeState): OneTimeState { return { ...s, values: s.values.map(() => ''), copied: false, acknowledged: false } }
export function recoveryCodesText(codes: string[], who = ''): string {
  return `OA 文件智能归档 恢复码${who ? `（${who}）` : ''}\n每个恢复码只能使用一次，请妥善保管。\n\n${codes.join('\n')}\n`
}
