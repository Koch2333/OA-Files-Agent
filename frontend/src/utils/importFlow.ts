import type { PickedFile } from '../api/types'
import { fmtBytes } from './format'
import { FORMAT_EXTS, GROUP_LABELS, extsOfGroups, isReadable } from './formats'
import { summarize, type PickSummary } from './importLimits'
import { fromInput } from './paths'

/** 工作台拖入区与批量导入页共用的纯逻辑（限制与规则在 importLimits.ts）。 */

/** 选中的文件里会被识别内容的格式分组（按固定顺序）。 */
export function recognisedFormats(picked: PickedFile[]): string[] {
  return GROUP_LABELS.filter((g) => { const exts = extsOfGroups(g.group); return picked.some((p) => exts.some((e) => p.file.name.toLowerCase().endsWith(e))) }).map((g) => g.short)
}
export { FORMAT_EXTS }

export interface PickBrief { text: string; formats: string; nameOnly: number; blocking: boolean; blockReason: string }
/** “N 个文件 · 总大小 · 将识别的格式”确认条文案。 */
export function pickBrief(picked: PickedFile[], sum: PickSummary = summarize(picked)): PickBrief {
  const formats = recognisedFormats(picked)
  const fmt = formats.length ? `将识别 ${formats.join(' / ')}` : '没有可识别内容的格式，将仅按文件名处理'
  const blockReason = ''
  return { text: `${sum.count} 个文件 · ${fmtBytes(sum.totalBytes)} · ${fmt}`, formats: fmt, nameOnly: sum.nameOnly, blocking: sum.blocking, blockReason }
}

/** 粘贴板里的文件（截图、复制的文件）→ 待导入列表；重名自动加序号避免被去重吞掉。 */
export function fromClipboard(files: FileList | File[] | null | undefined): PickedFile[] {
  if (!files) return []
  const seen = new Map<string, number>()
  return Array.from(files).flatMap((f) => {
    const n = seen.get(f.name) ?? 0; seen.set(f.name, n + 1)
    const name = n ? f.name.replace(/(\.[^.]+)?$/, `(${n})$1`) : f.name
    const [p] = fromInput([n ? new File([f], name, { type: f.type }) : f] as unknown as FileList)
    return p ? [p] : []
  })
}
/** 粘贴的剪贴板文件是否值得处理（至少有一个可识别内容的格式）。 */
export const hasSupported = (picked: PickedFile[]) => picked.some((p) => isReadable(p.file.name))
