import type { Preview } from '../api/types'

export type PreviewKind = Preview['kind']
export type Zoom = 'fit' | '100' | '150'

export const ZOOMS: { id: Zoom; label: string }[] = [
  { id: 'fit', label: '适应宽度' },
  { id: '100', label: '100%' },
  { id: '150', label: '150%' },
]

/** 页码限制在 [1, total]；total 未知或非法时固定为 1。 */
export function clampPage(page: number, total: number | null | undefined): number {
  const max = total && total > 0 ? Math.floor(total) : 1
  if (!Number.isFinite(page)) return 1
  return Math.min(Math.max(Math.floor(page), 1), max)
}

/** 把 Preview.page_url 模板中的 {page} 替换为页码。 */
export function fillPageTemplate(template: string, page: number): string {
  return template.replace(/\{page\}/g, String(page))
}

/** 表格截断提示：仅显示前 N 行，共 M 行。 */
export function rowLimitMessage(shown: number, total: number): string {
  return `仅显示前 ${shown} 行，共 ${total} 行`
}

/** 缩放对应的图片宽度（CSS）：适应宽度铺满容器，其余按图片原始宽度的倍数。 */
export function zoomWidth(zoom: Zoom, naturalWidth: number): string {
  if (zoom === 'fit' || !naturalWidth || naturalWidth <= 0) return '100%'
  return `${Math.round(naturalWidth * (zoom === '150' ? 1.5 : 1))}px`
}

/** 请求页图时的 max_side：放大时取更高分辨率。 */
export function maxSideFor(zoom: Zoom): number {
  return zoom === '150' ? 2000 : 1400
}

export const THUMB_SIDE = 200

export function kindLabel(kind: PreviewKind): string {
  return { pdf: 'PDF', image: '图片', document: '文档', spreadsheet: '表格', unsupported: '无法预览' }[kind]
}

/** 标题级别限制在 1–6。 */
export function headingLevel(level: number | undefined): 1 | 2 | 3 | 4 | 5 | 6 {
  const n = Math.floor(level ?? 1)
  return Math.min(Math.max(Number.isFinite(n) ? n : 1, 1), 6) as 1 | 2 | 3 | 4 | 5 | 6
}

/** 工作表第一行当作表头，其余为数据；行号从 1 开始计数的是数据行之前的表头行 + 1。 */
export function splitHeader(rows: string[][]): { head: string[]; body: string[][] } {
  return { head: rows[0] ?? [], body: rows.slice(1) }
}

/** 工作表列数（取各行最大值），保证渲染时每行补齐。 */
export function columnCount(rows: string[][]): number {
  return rows.reduce((m, r) => Math.max(m, r.length), 0)
}
