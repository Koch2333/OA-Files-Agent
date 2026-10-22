import type { PickedFile } from '../api/types'
import { fmtBytes } from './format'
import { MAX_FILES, SUPPORTED_RE, summarize, type PickSummary } from './importLimits'
import { fromInput } from './paths'

/** 工作台拖入区与批量导入页共用的纯逻辑（限制与规则在 importLimits.ts）。 */

const FORMAT_GROUPS: { label: string; re: RegExp }[] = [
  { label: 'Word', re: /\.docx$/i }, { label: 'Excel', re: /\.xlsx$/i }, { label: 'PDF', re: /\.pdf$/i }, { label: '图片', re: /\.(png|jpe?g)$/i },
]
/** 选中的文件里会被识别的格式（按固定顺序）。 */
export function recognisedFormats(picked: PickedFile[]): string[] {
  return FORMAT_GROUPS.filter((g) => picked.some((p) => g.re.test(p.file.name))).map((g) => g.label)
}

export interface PickBrief { text: string; formats: string; rejected: number; blocking: boolean; blockReason: string }
/** “N 个文件 · 总大小 · 将识别的格式”确认条文案。 */
export function pickBrief(picked: PickedFile[], sum: PickSummary = summarize(picked)): PickBrief {
  const formats = recognisedFormats(picked)
  const fmt = formats.length ? `将识别 ${formats.join(' / ')}` : '没有可识别的格式'
  const blockReason = sum.overCount ? `一次最多 ${MAX_FILES} 个文件，当前 ${sum.count} 个，请移除多余文件或分批提交` : sum.overBatch ? '整批大小超过 128 MiB，请分批提交' : ''
  return { text: `${sum.count} 个文件 · ${fmtBytes(sum.totalBytes)} · ${fmt}`, formats: fmt, rejected: sum.rejected, blocking: sum.blocking, blockReason }
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
/** 粘贴的剪贴板文件是否值得处理（至少有一个受支持格式）。 */
export const hasSupported = (picked: PickedFile[]) => picked.some((p) => SUPPORTED_RE.test(p.file.name))
