import type { PickedFile } from '../api/types'

/** 规范化浏览器给出的相对路径：统一分隔符，拒绝 .. 与绝对路径（防路径穿越，后端仍须再校验）。 */
export function safeRelativePath(p: string): string | null {
  const parts = p.replace(/\\/g, '/').split('/').filter((s) => s && s !== '.')
  if (/^[a-zA-Z]:$/.test(parts[0] ?? '') || parts.includes('..') || p.startsWith('/')) return null
  return parts.length ? parts.join('/') : null
}

interface FsEntry { isFile: boolean; isDirectory: boolean; name: string; fullPath: string; file?: (cb: (f: File) => void, err: (e: unknown) => void) => void; createReader?: () => { readEntries: (cb: (e: FsEntry[]) => void, err: (e: unknown) => void) => void } }

async function walk(entry: FsEntry, out: PickedFile[]) {
  if (entry.isFile && entry.file) {
    const file = await new Promise<File>((res, rej) => entry.file!(res, rej))
    const rel = safeRelativePath(entry.fullPath)
    if (rel) out.push({ file, relativePath: rel })
  } else if (entry.isDirectory && entry.createReader) {
    const reader = entry.createReader()
    for (;;) { // readEntries 每次最多返回 100 项，需循环到空
      const batch = await new Promise<FsEntry[]>((res, rej) => reader.readEntries(res, rej))
      if (!batch.length) break
      for (const e of batch) await walk(e, out)
    }
  }
}

/** 拖拽（含文件夹）→ 带相对路径的文件列表。 */
export async function collectDropped(dt: DataTransfer): Promise<PickedFile[]> {
  const out: PickedFile[] = []
  const entries: FsEntry[] = []
  for (const item of Array.from(dt.items)) {
    const e = (item as DataTransferItem & { webkitGetAsEntry?: () => FsEntry | null }).webkitGetAsEntry?.()
    if (e) entries.push(e)
  }
  if (entries.length) { for (const e of entries) await walk(e, out); return out }
  for (const f of Array.from(dt.files)) out.push({ file: f, relativePath: f.name })
  return out
}

/** <input webkitdirectory> / 多选文件 → 文件列表。 */
export function fromInput(list: FileList): PickedFile[] {
  return Array.from(list).flatMap((f) => {
    const rel = safeRelativePath((f as File & { webkitRelativePath?: string }).webkitRelativePath || f.name)
    return rel ? [{ file: f, relativePath: rel }] : []
  })
}
