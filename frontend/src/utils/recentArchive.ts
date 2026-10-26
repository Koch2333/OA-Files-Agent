import type { LedgerEntry } from '../api/types'
import { displayCategory } from './classification'

/** “最近归档”行视图模型：只显示易读名称与标签；原文件名和完整路径放进详情。 */
export interface ArchiveRow {
  id: string
  /** 列表里显示的名称（过长时中间省略，保留扩展名） */
  title: string
  /** 完整规范名（tooltip） */
  fullTitle: string
  department: { text: string; missing: boolean }
  type: { text: string; missing: boolean }
  date: { text: string; missing: boolean }
  decision: 'manual' | 'ai_auto'
  decisionModel?: string
  when: string
  whenFull: string
}

/** 中间省略并保留扩展名：很长的文件名仍能看到结尾的类型。 */
export function truncateName(name: string, max = 36): string {
  const chars = Array.from(name)
  if (chars.length <= max) return name
  const dot = name.lastIndexOf('.')
  const ext = dot > 0 && name.length - dot <= 6 ? name.slice(dot) : ''
  const stem = Array.from(name.slice(0, ext ? dot : name.length))
  const keepTail = Math.min(6, Math.max(0, stem.length - 1))
  const head = Math.max(1, max - Array.from(ext).length - keepTail - 1)
  return stem.slice(0, head).join('') + '…' + stem.slice(stem.length - keepTail).join('') + ext
}

/** 把常见日期写法规整为 YYYY-MM-DD；无法识别返回空串。 */
export function normDate(s: string | null | undefined): string {
  if (!s) return ''
  const m = /^(\d{4})[-/.年](\d{1,2})[-/.月](\d{1,2})/.exec(s.trim())
  if (!m) return ''
  const mo = +m[2], d = +m[3]
  if (mo < 1 || mo > 12 || d < 1 || d > 31) return ''
  return `${m[1]}-${String(mo).padStart(2, '0')}-${String(d).padStart(2, '0')}`
}

/** 相对时间：刚刚 / N 分钟前 / N 小时前 / 昨天 / N 天前 / 日期。 */
export function relTime(iso: string | null | undefined, now: number = Date.now()): string {
  if (!iso) return '—'
  const t = Date.parse(iso)
  if (isNaN(t)) return iso
  const sec = Math.round((now - t) / 1000)
  if (sec < 0) return '刚刚'
  if (sec < 60) return '刚刚'
  if (sec < 3600) return `${Math.floor(sec / 60)} 分钟前`
  if (sec < 86400) return `${Math.floor(sec / 3600)} 小时前`
  const days = Math.floor(sec / 86400)
  if (days === 1) return '昨天'
  if (days < 30) return `${days} 天前`
  return new Date(t).toLocaleDateString('zh-CN', { year: 'numeric', month: '2-digit', day: '2-digit' }).replace(/\//g, '-')
}

const txt = (v: string | null | undefined, missing: string) => { const s = (v ?? '').trim(); return s ? { text: s, missing: false } : { text: missing, missing: true } }

export function archiveRow(l: LedgerEntry, now: number = Date.now(), max = 36): ArchiveRow {
  const date = normDate(l.document_date)
  return {
    id: l.id,
    title: truncateName(l.canonical_name, max),
    fullTitle: l.canonical_name,
    // 空值/未知值读出时一律显示为「其他」（不改写存量数据）
    department: txt(displayCategory(l.department), '其他'),
    type: txt(displayCategory(l.document_type), '其他'),
    date: date ? { text: date, missing: false } : { text: '日期未知', missing: true },
    decision: l.decision ?? 'manual',
    decisionModel: l.decision_model,
    when: relTime(l.executed_at, now),
    whenFull: l.executed_at,
  }
}
