/** 文件管理用的相对路径辅助：正斜杠，空串代表归档根目录。 */

export function join(dir: string, name: string): string {
  return dir ? `${dir}/${name}` : name
}
export function dirname(p: string): string {
  const i = p.lastIndexOf('/')
  return i < 0 ? '' : p.slice(0, i)
}
export function basename(p: string): string {
  return p.slice(p.lastIndexOf('/') + 1)
}
/** 规范化用户输入：反斜杠转正斜杠，去掉首尾与重复斜杠。 */
export function normalize(p: string): string {
  return p.replace(/\\/g, '/').split('/').map((s) => s.trim()).filter(Boolean).join('/')
}
export interface Crumb { label: string; path: string }
export function breadcrumbs(p: string, rootLabel = '归档根目录'): Crumb[] {
  const out: Crumb[] = [{ label: rootLabel, path: '' }]
  let acc = ''
  for (const part of p.split('/').filter(Boolean)) {
    acc = join(acc, part)
    out.push({ label: part, path: acc })
  }
  return out
}
/** 文件/文件夹名校验；合法返回 null，否则返回中文错误信息。 */
export function validateName(name: string): string | null {
  const n = name.trim()
  if (!n) return '名称不能为空'
  if (n.length > 120) return '名称不能超过 120 个字符'
  if (n.startsWith('.')) return '名称不能以 . 开头'
  if (/[\\/:*?"<>|]/.test(n)) return '名称不能包含 / \\ : * ? " < > |'
  return null
}
/** 目标路径校验（移动）：每一段都要合法，且不含 .. */
export function validatePath(p: string): string | null {
  for (const seg of normalize(p).split('/').filter(Boolean)) {
    if (seg === '..' || seg === '.') return '路径不能包含 . 或 ..'
    const e = validateName(seg)
    if (e) return e
  }
  return null
}
export function isInside(child: string, parent: string): boolean {
  return child === parent || child.startsWith(parent + '/')
}
const PREVIEW = /\.(pdf|png|jpe?g)$/i
export function isPreviewable(name: string): boolean {
  return PREVIEW.test(name)
}
