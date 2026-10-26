import type { ArchivePlanItem } from '../api/types'

/** 保留分类「其他」：后端同名常量 archive.CategoryOther。命名模板里按字面渲染（如 其他/2026/…）。 */
export const OTHER = '其他'
/** 部门/类型的最大长度（与后端 archive.ValidateCategory 一致）。 */
export const CATEGORY_MAX = 60

export interface CategoryOption { value: string; source: 'builtin' | 'ledger' | 'plan' | 'reserved'; count: number }
export interface ComboItem { value: string; count: number; kind: 'used' | 'defined' | 'reserved' | 'create' }
export interface ComboGroup { id: ComboItem['kind']; label: string; items: ComboItem[] }

/** 去重键：NFC、去首尾空白、不区分大小写（与后端 archive.CategoryKey 一致）。 */
export function categoryKey(s: string): string { return s.normalize('NFC').trim().toLowerCase() }

/** 读出时的展示：空值与占位“待审核”都显示为「其他」（不改写存量数据）。 */
export function displayCategory(v: string | null | undefined): string {
  const s = (v ?? '').normalize('NFC').trim()
  return !s || s === '待审核' ? OTHER : s
}

// eslint-disable-next-line no-control-regex
const ILLEGAL = /[\\/:*?"<>|\u0000-\u001f\u007f-\u009f​-‏‪-‮﻿�]/

/** 与后端同一规则：空值合法（表示清空）；非空须 ≤60 字符、不含路径非法字符、不只由点号组成。返回错误文案或 null。 */
export function validateCategory(v: string): string | null {
  const s = v.trim()
  if (!s) return null
  if (Array.from(s).length > CATEGORY_MAX) return `最多 ${CATEGORY_MAX} 个字符`
  if (ILLEGAL.test(s)) return '不能包含 \\ / : * ? " < > | 或控制字符'
  if (!s.replace(/[. ]/g, '')) return '不能只由点号组成'
  return null
}

const MAX_SHOWN = 60

/**
 * 下拉候选的分组：已使用（台账/本任务，次数多者在前）→ 预设 → 「其他」；输入内容不在列表里时追加“新建”行。
 * 输入即过滤；纯函数，便于测试。
 */
export function buildComboGroups(options: CategoryOption[], query: string, ctx: { typing?: boolean } = {}): ComboGroup[] {
  const q = ctx.typing === false ? '' : categoryKey(query)
  const hit = (v: string) => !q || categoryKey(v).includes(q)
  const used: ComboItem[] = [], defined: ComboItem[] = []
  let other: ComboItem | null = null
  for (const o of options) {
    if (o.source === 'reserved' || categoryKey(o.value) === categoryKey(OTHER)) { other = { value: OTHER, count: o.count, kind: 'reserved' }; continue }
    if (!hit(o.value)) continue
    if (o.count > 0) used.push({ value: o.value, count: o.count, kind: 'used' })
    else defined.push({ value: o.value, count: 0, kind: 'defined' })
  }
  used.sort((a, b) => b.count - a.count)
  const groups: ComboGroup[] = []
  if (used.length) groups.push({ id: 'used', label: '已使用', items: used.slice(0, MAX_SHOWN) })
  if (defined.length) groups.push({ id: 'defined', label: '预设', items: defined.slice(0, MAX_SHOWN) })
  if (hit(OTHER)) groups.push({ id: 'reserved', label: '兜底分类', items: [other ?? { value: OTHER, count: 0, kind: 'reserved' }] })
  const t = query.trim()
  if (t && ctx.typing !== false && !options.some((o) => categoryKey(o.value) === categoryKey(t)) && categoryKey(t) !== categoryKey(OTHER) && !validateCategory(t)) {
    groups.push({ id: 'create', label: '新建', items: [{ value: t.normalize('NFC'), count: 0, kind: 'create' }] })
  }
  return groups
}

export const flattenGroups = (g: ComboGroup[]): ComboItem[] => g.flatMap((x) => x.items)

/** 仍是系统暂填「其他」、等待人工确认的条目。 */
export function fallbackItems(items: ArchivePlanItem[]): ArchivePlanItem[] { return items.filter((i) => (i.fallback_fields ?? []).length > 0) }
export const fallbackLabel = (i: ArchivePlanItem): string => {
  const f = i.fallback_fields ?? []
  return f.length === 2 ? '部门与类型' : f[0] === 'department' ? '部门' : f[0] === 'document_type' ? '类型' : ''
}

/** 对一批暂填条目生成“确认”补丁：把仍是占位值的字段原样提交（显式提交 = 人工确认，后端据此移出 fallback_fields）。 */
export function confirmPatches(items: ArchivePlanItem[]): { file_id: string; department?: string; document_type?: string }[] {
  return fallbackItems(items).map((i) => {
    const f = i.fallback_fields ?? []
    return { file_id: i.file_id, ...(f.includes('department') ? { department: i.department ?? OTHER } : {}), ...(f.includes('document_type') ? { document_type: i.document_type ?? OTHER } : {}) }
  })
}
