/**
 * 演示用内存文件树：由台账的归档路径派生，语义对齐契约 /v1/fs（不覆盖、隐藏名拒绝、
 * 文件夹不能移入自身、回收站还原遇占用自动加序号、台账只对台账来源的文件生效）。
 */
import { ApiError, type FolderListing, type FsEntry, type LedgerEntry, type TrashItem } from '../api/types'
import { basename, dirname, isInside, join, normalize, validateName } from '../utils/fsPaths'

interface Node { is_dir: boolean; size: number; modified: string; mime?: string; ledgerId?: string }
interface TrashRec { item: TrashItem; nodes: [string, Node][]; ledger: LedgerEntry[] }

const MIME: Record<string, string> = {
  pdf: 'application/pdf', png: 'image/png', jpg: 'image/jpeg', jpeg: 'image/jpeg',
  docx: 'application/vnd.openxmlformats-officedocument.wordprocessingml.document',
  xlsx: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
}
const previewable = (name: string) => /\.(pdf|png|jpe?g)$/i.test(name)
const mimeOf = (name: string) => MIME[name.split('.').pop()?.toLowerCase() ?? ''] ?? 'application/octet-stream'
const sizeOf = (s: string) => 30000 + (s.split('').reduce((a, c) => a + c.charCodeAt(0), 0) % 90000)

export class MockFs {
  private nodes = new Map<string, Node>()
  private trash: TrashRec[] = []
  private seen = new Set<string>()
  constructor(private ledger: LedgerEntry[]) {}

  /** 把台账里尚未出现过的记录补进文件树（计划执行后新增的文件）。 */
  private sync() {
    for (const l of this.ledger) {
      if (this.seen.has(l.id)) continue
      this.seen.add(l.id)
      this.mkdirs(dirname(l.archive_path), l.executed_at)
      this.nodes.set(l.archive_path, { is_dir: false, size: sizeOf(l.sha256), modified: l.executed_at, mime: mimeOf(l.archive_path), ledgerId: l.id })
    }
  }
  private mkdirs(dir: string, at: string) {
    let acc = ''
    for (const p of dir.split('/').filter(Boolean)) {
      acc = join(acc, p)
      if (!this.nodes.has(acc)) this.nodes.set(acc, { is_dir: true, size: 0, modified: at })
    }
  }
  private clean(p: string): string {
    const n = normalize(p)
    for (const seg of n.split('/').filter(Boolean)) {
      if (seg === '..' || seg === '.') throw new ApiError(400, 'INVALID_PATH', '路径不能包含 . 或 ..')
      if (seg.startsWith('.')) throw new ApiError(400, 'INVALID_PATH', '名称不能以 . 开头')
    }
    return n
  }
  private cleanName(p: string): string {
    const n = this.clean(p)
    if (!n) throw new ApiError(400, 'INVALID_PATH', '路径不能为空')
    const e = validateName(basename(n))
    if (e) throw new ApiError(400, 'INVALID_NAME', e)
    return n
  }
  private entry(path: string, n: Node): FsEntry {
    const l = n.ledgerId ? this.ledger.find((x) => x.id === n.ledgerId) : undefined
    return {
      name: basename(path), path, is_dir: n.is_dir, size_bytes: n.size, modified_at: n.modified,
      ...(n.is_dir ? {} : { mime_type: n.mime ?? mimeOf(path), previewable: previewable(path) }),
      ledger: l ? { id: l.id, original_name: l.original_name, department: l.department, document_type: l.document_type, document_date: l.document_date } : null,
    }
  }
  private requireDir(dir: string) {
    if (dir && !this.nodes.get(dir)?.is_dir) throw new ApiError(404, 'NOT_FOUND', '文件夹不存在')
  }

  list(path: string): FolderListing {
    this.sync()
    const p = this.clean(path)
    this.requireDir(p)
    const entries: FsEntry[] = []
    for (const [k, n] of this.nodes) if (dirname(k) === p) entries.push(this.entry(k, n))
    entries.sort((a, b) => Number(b.is_dir) - Number(a.is_dir) || a.name.localeCompare(b.name, 'zh-CN'))
    return { path: p, parent: p ? dirname(p) : null, entries, capabilities: { reveal: false, root: null } }
  }

  mkdir(path: string): FsEntry {
    this.sync()
    const p = this.cleanName(path)
    this.requireDir(dirname(p))
    if (this.nodes.has(p)) throw new ApiError(409, 'CONFLICT', '同名文件或文件夹已存在')
    const n: Node = { is_dir: true, size: 0, modified: new Date().toISOString() }
    this.nodes.set(p, n)
    return this.entry(p, n)
  }

  move(from: string, to: string): { entry: FsEntry; ledger_updated: number } {
    this.sync()
    const f = this.clean(from), t = this.cleanName(to)
    const src = this.nodes.get(f)
    if (!f || !src) throw new ApiError(404, 'NOT_FOUND', '源文件或文件夹不存在')
    if (src.is_dir && isInside(t, f)) throw new ApiError(400, 'INVALID_MOVE', '不能把文件夹移动到它自身内部')
    this.requireDir(dirname(t))
    if (this.nodes.has(t)) throw new ApiError(409, 'CONFLICT', '目标位置已存在同名文件或文件夹，不会覆盖')
    let updated = 0
    const moved = [...this.nodes.keys()].filter((k) => isInside(k, f))
    for (const k of moved) {
      const n = this.nodes.get(k)!
      const nk = t + k.slice(f.length)
      this.nodes.delete(k)
      this.nodes.set(nk, n)
      const l = n.ledgerId ? this.ledger.find((x) => x.id === n.ledgerId) : undefined
      if (l) { l.archive_path = nk; l.canonical_name = basename(nk); updated++ }
    }
    src.modified = new Date().toISOString()
    return { entry: this.entry(t, this.nodes.get(t)!), ledger_updated: updated }
  }

  trashEntry(path: string): TrashItem {
    this.sync()
    const p = this.clean(path)
    const n = this.nodes.get(p)
    if (!p || !n) throw new ApiError(404, 'NOT_FOUND', '文件或文件夹不存在')
    const keys = [...this.nodes.keys()].filter((k) => isInside(k, p))
    const nodes: [string, Node][] = keys.map((k) => [k, this.nodes.get(k)!])
    const ledger: LedgerEntry[] = []
    for (const [, nd] of nodes) {
      const i = nd.ledgerId ? this.ledger.findIndex((x) => x.id === nd.ledgerId) : -1
      if (i >= 0) ledger.push(...this.ledger.splice(i, 1))
    }
    keys.forEach((k) => this.nodes.delete(k))
    const size = nodes.reduce((a, [, nd]) => a + nd.size, 0)
    const item: TrashItem = { id: crypto.randomUUID(), original_path: p, name: basename(p), is_dir: n.is_dir, size_bytes: size, deleted_at: new Date().toISOString() }
    this.trash.unshift({ item, nodes, ledger })
    return item
  }

  listTrash(): TrashItem[] { return this.trash.map((t) => t.item) }

  restore(id: string): { path: string; ledger_updated: number } {
    this.sync()
    const i = this.trash.findIndex((t) => t.item.id === id)
    if (i < 0) throw new ApiError(404, 'NOT_FOUND', '回收站中没有该项目')
    const rec = this.trash[i]
    const orig = rec.item.original_path
    let target = orig
    if (this.nodes.has(target)) {
      const base = basename(orig), dot = rec.item.is_dir ? -1 : base.lastIndexOf('.')
      const stem = dot > 0 ? base.slice(0, dot) : base, ext = dot > 0 ? base.slice(dot) : ''
      for (let n = 1; this.nodes.has(target); n++) target = join(dirname(orig), `${stem} (${n})${ext}`)
    }
    this.mkdirs(dirname(target), new Date().toISOString())
    let updated = 0
    for (const [k, nd] of rec.nodes) {
      const nk = target + k.slice(orig.length)
      this.nodes.set(nk, nd)
      const l = rec.ledger.find((x) => x.id === nd.ledgerId)
      if (l) { l.archive_path = nk; l.canonical_name = basename(nk); this.ledger.push(l); updated++ }
    }
    this.trash.splice(i, 1)
    return { path: target, ledger_updated: updated }
  }
}
