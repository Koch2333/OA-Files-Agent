/**
 * 文件管理的多选逻辑（纯函数，不依赖 DOM）：复选框切换、Shift 范围、Ctrl/⌘ 点击、全选、空格/Ctrl+A。
 * order 始终是“当前可见行”的有序路径列表（排序、筛选之后）。
 */
export interface Selection { paths: ReadonlySet<string>; anchor: string | null }

export const emptySelection = (): Selection => ({ paths: new Set(), anchor: null })

export function toggle(sel: Selection, path: string): Selection {
  const next = new Set(sel.paths)
  if (next.has(path)) next.delete(path); else next.add(path)
  return { paths: next, anchor: path }
}

export function selectOnly(_sel: Selection, path: string): Selection {
  return { paths: new Set([path]), anchor: path }
}

/** Shift 范围：从锚点到 to（含两端，按 order 的顺序）。additive 为 true（Ctrl+Shift）时保留原有选择。 */
export function selectRange(sel: Selection, order: readonly string[], to: string, additive = false): Selection {
  const j = order.indexOf(to)
  if (j < 0) return sel
  const i = sel.anchor !== null ? order.indexOf(sel.anchor) : -1
  const [a, b] = i < 0 ? [j, j] : [Math.min(i, j), Math.max(i, j)]
  const next = new Set(additive ? sel.paths : [])
  for (const p of order.slice(a, b + 1)) next.add(p)
  return { paths: next, anchor: i < 0 ? to : sel.anchor }
}

export function selectAll(order: readonly string[]): Selection {
  return { paths: new Set(order), anchor: order[0] ?? null }
}

export function clear(): Selection { return emptySelection() }

export function allSelected(sel: Selection, order: readonly string[]): boolean {
  return order.length > 0 && order.every((p) => sel.paths.has(p))
}
export function someSelected(sel: Selection, order: readonly string[]): boolean {
  return order.some((p) => sel.paths.has(p)) && !allSelected(sel, order)
}

export interface Mods { shift?: boolean; ctrl?: boolean; meta?: boolean }

/** 点击复选框（或带修饰键点击行）：Shift 范围，其余切换。 */
export function click(sel: Selection, order: readonly string[], path: string, m: Mods = {}): Selection {
  if (m.shift) return selectRange(sel, order, path, !!(m.ctrl || m.meta))
  return toggle(sel, path)
}

/** 表头“全选”复选框：全部选中则清空，否则选中当前可见的全部行（保留不可见行的选择）。 */
export function toggleAll(sel: Selection, order: readonly string[]): Selection {
  if (allSelected(sel, order)) {
    const next = new Set(sel.paths)
    order.forEach((p) => next.delete(p))
    return { paths: next, anchor: null }
  }
  const next = new Set(sel.paths)
  order.forEach((p) => next.add(p))
  return { paths: next, anchor: order[0] ?? null }
}

/** 键盘：空格切换焦点行；Ctrl/⌘+A 全选可见行；Esc 清除。返回 null 表示不处理该键。 */
export function key(sel: Selection, order: readonly string[], k: string, focused: string | null, m: Mods = {}): Selection | null {
  if (k === ' ' && focused) return m.shift ? selectRange(sel, order, focused) : toggle(sel, focused)
  if ((k === 'a' || k === 'A') && (m.ctrl || m.meta)) return selectAll(order)
  if (k === 'Escape' && sel.paths.size) return emptySelection()
  return null
}

/** 目录刷新后只保留仍然存在的项（排序/筛选不会丢选择，因为这里用的是整个目录而不是可见行）。 */
export function prune(sel: Selection, existing: Iterable<string>): Selection {
  const have = new Set(existing)
  const next = new Set([...sel.paths].filter((p) => have.has(p)))
  if (next.size === sel.paths.size) return sel
  return { paths: next, anchor: sel.anchor !== null && next.has(sel.anchor) ? sel.anchor : null }
}

/** 选择条文案。hidden = 被筛选隐藏但仍选中的项数。 */
export function barText(count: number, hidden = 0): string {
  return `已选 ${count} 项` + (hidden > 0 ? `（其中 ${hidden} 项被筛选隐藏）` : '')
}
