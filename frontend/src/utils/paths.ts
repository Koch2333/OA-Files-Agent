import { isJunk } from './formats'
import type { PickedFile } from '../api/types'

/** 规范化浏览器给出的相对路径：统一分隔符，拒绝 .. 与绝对路径（防路径穿越，后端仍须再校验）。 */
export function safeRelativePath(p: string): string | null {
  const parts = p.replace(/\\/g, '/').split('/').filter((s) => s && s !== '.')
  if (/^[a-zA-Z]:$/.test(parts[0] ?? '') || parts.includes('..') || p.startsWith('/')) return null
  return parts.length ? parts.join('/') : null
}

/**
 * FileSystemEntry.fullPath of a dropped item is anchored at a virtual root ("/a.docx", "/dir/a.docx"). It is NOT a local
 * absolute path, so strip the anchor before the usual safety check. (Feeding it to safeRelativePath unchanged rejected
 * every dropped file: the drop "did nothing".)
 */
export function entryRelativePath(fullPath: string): string | null {
  return safeRelativePath(fullPath.replace(/^[\\/]+/, ''))
}

interface FsEntry { isFile: boolean; isDirectory: boolean; name: string; fullPath: string; file?: (cb: (f: File) => void, err: (e: unknown) => void) => void; createReader?: () => { readEntries: (cb: (e: FsEntry[]) => void, err: (e: unknown) => void) => void } }

export interface CollectOptions {
  /** 被跳过的条目（路径不安全或读取失败）会记录在这里，调用方据此提示用户，而不是静默忽略。 */
  skipped?: string[]
  /** 已发现的文件数（用于“正在读取…”进度）。 */
  onProgress?: (found: number) => void
}

async function walk(entry: FsEntry, out: PickedFile[], opt: CollectOptions) {
  if (entry.isFile && isJunk(entry.name)) return // Thumbs.db、~$锁文件等：不是文档，文件夹导入时静默忽略
  if (entry.isFile && entry.file) {
    try {
      const file = await new Promise<File>((res, rej) => entry.file!(res, rej))
      const rel = entryRelativePath(entry.fullPath || file.name)
      if (rel) { out.push({ file, relativePath: rel }); opt.onProgress?.(out.length) } else opt.skipped?.push(entry.fullPath)
    } catch { opt.skipped?.push(entry.fullPath || entry.name) }
  } else if (entry.isDirectory && entry.createReader) {
    const reader = entry.createReader()
    for (;;) { // readEntries 每次最多返回 100 项，需循环到空
      let batch: FsEntry[]
      try { batch = await new Promise<FsEntry[]>((res, rej) => reader.readEntries(res, rej)) } catch { opt.skipped?.push(entry.fullPath || entry.name); break }
      if (!batch.length) break
      for (const e of batch) await walk(e, out, opt)
    }
  }
}

/**
 * 拖拽（含文件夹）→ 带相对路径的文件列表。DataTransfer 在事件处理函数返回后即被清空，
 * 所以 items 与 files 必须在第一个 await 之前同步取出。
 */
export async function collectDropped(dt: DataTransfer, opt: CollectOptions = {}): Promise<PickedFile[]> {
  const entries: FsEntry[] = []
  for (const item of Array.from(dt.items ?? [])) {
    if (item.kind && item.kind !== 'file') continue
    const e = (item as DataTransferItem & { webkitGetAsEntry?: () => FsEntry | null }).webkitGetAsEntry?.()
    if (e) entries.push(e)
  }
  const plain = Array.from(dt.files ?? [])
  const out: PickedFile[] = []
  for (const e of entries) await walk(e, out, opt)
  if (!out.length && plain.length) { // 条目不可用或全部读取失败时回退到 files（不含文件夹结构）
    for (const f of plain) {
      const rel = safeRelativePath(f.name)
      if (rel) out.push({ file: f, relativePath: rel })
    }
    if (out.length) opt.skipped?.splice(0)
  }
  return out
}

/** <input webkitdirectory> / 多选文件 → 文件列表。 */
export function fromInput(list: FileList): PickedFile[] {
  return Array.from(list).flatMap((f) => {
    if (isJunk(f.name) && (f as File & { webkitRelativePath?: string }).webkitRelativePath) return [] // 仅文件夹选择时过滤
    const rel = safeRelativePath((f as File & { webkitRelativePath?: string }).webkitRelativePath || f.name)
    return rel ? [{ file: f, relativePath: rel }] : []
  })
}
