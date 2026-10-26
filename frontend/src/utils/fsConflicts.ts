/** 文件管理冲突对话框的纯逻辑：分组、可选处理方式、全部应用、构造 resolutions、文案。 */
import { ApiError, type ConflictPolicy, type FsBatchResult, type FsConflict, type FsItemResult } from '../api/types'

export type Choice = 'skip' | 'rename' | 'replace'

export function conflictsOf(err: unknown): FsConflict[] | null {
  if (!(err instanceof ApiError) || err.status !== 409) return null
  const c = err.details?.conflicts
  return Array.isArray(c) && c.length ? (c as FsConflict[]) : null
}

export interface Group { item: string; conflicts: FsConflict[]; choices: Choice[] }

/** 同一所选项（例如合并的文件夹）的多个冲突共用一个处理方式；可选项取交集。 */
export function groupByItem(conflicts: readonly FsConflict[]): Group[] {
  const map = new Map<string, FsConflict[]>()
  for (const c of conflicts) map.set(c.item, [...(map.get(c.item) ?? []), c])
  return [...map].map(([item, cs]) => {
    const choices = (['skip', 'rename', 'replace'] as Choice[]).filter((ch) => cs.every((c) => c.choices.includes(ch)))
    return { item, conflicts: cs, choices: choices.length ? choices : ['skip'] }
  })
}

/** 默认全部“跳过”：最保守，永不丢数据。 */
export function defaultPicks(groups: readonly Group[]): Record<string, Choice> {
  return Object.fromEntries(groups.map((g) => [g.item, 'skip' as Choice]))
}

/** 把同一个选择应用到全部项；某项不允许该选择时退回“跳过”。 */
export function applyAll(groups: readonly Group[], choice: Choice): Record<string, Choice> {
  return Object.fromEntries(groups.map((g) => [g.item, g.choices.includes(choice) ? choice : ('skip' as Choice)]))
}

export function pickable(groups: readonly Group[], choice: Choice): number {
  return groups.filter((g) => g.choices.includes(choice)).length
}

export function toResolutions(picks: Record<string, Choice>): Record<string, Choice> {
  return { ...picks }
}

export function choiceLabel(choice: Choice, g?: Pick<Group, 'conflicts'>): string {
  if (choice === 'skip') return '跳过'
  if (choice === 'rename') return g?.conflicts.every((c) => c.kind === 'duplicate_content') ? '仍然放入' : '保留两者'
  return '替换（旧文件进回收站）'
}

const KIND_TEXT: Record<FsConflict['kind'], string> = {
  name_exists: '同名文件（内容不同）', type_conflict: '文件与文件夹同名', folder_exists: '同名文件夹', duplicate_content: '相同内容已存在',
}
export const kindText = (k: FsConflict['kind']) => KIND_TEXT[k]

export function summarize(conflicts: readonly FsConflict[]): string {
  const n: Partial<Record<FsConflict['kind'], number>> = {}
  for (const c of conflicts) n[c.kind] = (n[c.kind] ?? 0) + 1
  return (Object.keys(n) as FsConflict['kind'][]).map((k) => `${n[k]} 项${KIND_TEXT[k]}`).join('，')
}

export const shortHash = (h?: string) => (h ? h.slice(0, 8) + '…' + h.slice(-4) : '未知')

const STATUS_TEXT: Record<string, string> = {
  moved: '已移动', renamed: '已保留两者（新名称）', replaced: '已替换（旧文件在回收站）', skipped: '已跳过', duplicate: '内容相同，未重复放入', partial: '部分完成', failed: '失败',
}
export const statusText = (s: string) => STATUS_TEXT[s] ?? s

/** 批量操作结果的一句话摘要（供 toast）。 */
export function batchSummary(r: FsBatchResult, verb = '移动'): string {
  const s = r.summary
  const parts: string[] = []
  if (s.moved) parts.push(`${verb} ${s.moved} 项`)
  if (s.renamed) parts.push(`保留两者 ${s.renamed} 项`)
  if (s.replaced) parts.push(`替换 ${s.replaced} 项（旧文件在回收站）`)
  if (s.duplicate) parts.push(`内容相同未重复放入 ${s.duplicate} 项`)
  if (s.skipped) parts.push(`跳过 ${s.skipped} 项`)
  if (s.failed) parts.push(`失败 ${s.failed} 项`)
  return parts.join('，') || '没有需要处理的项目'
}

export const failedItems = (r: FsBatchResult): FsItemResult[] => r.items.filter((i) => i.status === 'failed' || i.status === 'partial')
export const outsideModified = (r: FsBatchResult): string[] => r.items.flatMap((i) => i.modified_outside ?? [])
