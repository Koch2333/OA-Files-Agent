import table from './formats.json'

/**
 * 文件格式表：唯一来源是 contracts/formats.json（scripts/sync_formats.py 复制到本目录的 formats.json，CI 校验一致）。
 * “可识别内容”的格式在表里；其他任何扩展名也会被接收，只是内容无法读取，按文件名归档（见 importLimits.ts）。
 */
export interface FormatRow {
  ext: string
  mime: string
  group: 'word' | 'excel' | 'powerpoint' | 'pdf' | 'image' | 'text' | 'web' | 'mail'
  label: string
  extractor: string
  preview: 'document' | 'spreadsheet' | 'pdf' | 'image'
  icon: string
  legacy?: boolean
  best_effort?: boolean
}
export const FORMATS = table.formats as FormatRow[]
export const FORMAT_EXTS: string[] = FORMATS.map((f) => f.ext)
const escapeRe = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
/** 可读取内容的扩展名（含点，不区分大小写）。 */
export const READABLE_RE = new RegExp(`(${FORMAT_EXTS.map(escapeRe).join('|')})$`, 'i')
const BY_EXT = new Map(FORMATS.map((f) => [f.ext, f]))

export function extOf(name: string): string {
  const base = name.split(/[\\/]/).pop() ?? name
  const i = base.lastIndexOf('.')
  return i > 0 ? base.slice(i).toLowerCase() : ''
}
export function formatOf(name: string): FormatRow | undefined { return BY_EXT.get(extOf(name)) }
export const isReadable = (name: string) => BY_EXT.has(extOf(name))
/** 文件图标（Material Symbols 名，须在 components/iconMap.ts 中登记）。 */
export function iconForName(name: string): string { return formatOf(name)?.icon ?? 'draft' }

/** 导入页“支持的格式”文案分组（按固定顺序）。 */
export const GROUP_LABELS: { group: FormatRow['group'][]; label: string; short: string; icon: string }[] = [
  { group: ['word'], label: 'Word / 文字文档', short: 'Word', icon: 'description' }, { group: ['excel'], label: 'Excel / 表格', short: 'Excel', icon: 'table_chart' },
  { group: ['powerpoint'], label: 'PowerPoint / 演示', short: 'PowerPoint', icon: 'slideshow' }, { group: ['pdf'], label: 'PDF（含扫描件）', short: 'PDF', icon: 'picture_as_pdf' },
  { group: ['image'], label: '图片（OCR）', short: '图片', icon: 'image' }, { group: ['text', 'web'], label: '文本 / 网页', short: '文本', icon: 'article' }, { group: ['mail'], label: '邮件', short: '邮件', icon: 'mail' },
]
export function extsOfGroups(groups: FormatRow['group'][]): string[] { return FORMATS.filter((f) => groups.includes(f.group)).map((f) => f.ext) }

/** 文件夹导入时不应带入的系统/临时文件（与后端 formats.IsJunk 一致）。 */
export function isJunk(name: string): boolean {
  const base = (name.split(/[\\/]/).pop() ?? name).toLowerCase()
  return ['thumbs.db', 'ehthumbs.db', 'desktop.ini', '.ds_store', '.localized'].includes(base) || base.startsWith('~$') || base.startsWith('._') || base.startsWith('.~lock.')
}
