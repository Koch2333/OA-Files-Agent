/**
 * 演示用内存文件树：由台账的归档路径派生，语义对齐契约 /v1/fs（永不静默覆盖、隐藏名拒绝、
 * 文件夹不能移入自身、回收站还原遇占用自动加序号、台账只对台账来源的文件生效），
 * 以及基于 SHA-256 的冲突判定（同名同哈希 = 重复、同名不同内容 = 冲突、其他名称同哈希 = 重复内容警告）、
 * 批量移动/删除的逐项结果、哈希校验与“系统外被修改”标记。
 */
import { uuid } from '../utils/uuid'
import {
  ApiError, type ConflictOptions, type ConflictPolicy, type FolderListing, type FsBatchResult, type FsConflict, type FsEntry,
  type FsFileInfo, type FsItemResult, type FsItemStatus, type FsVerifyReport, type LedgerEntry, type MoveResult, type RestoreResult, type TrashItem,
} from '../api/types'
import { basename, dirname, isInside, join, normalize, validateName } from '../utils/fsPaths'

interface Node { is_dir: boolean; size: number; modified: string; mime?: string; ledgerId?: string; tampered?: boolean; verdict?: 'verified' | 'mismatch' }
interface TrashRec { item: TrashItem; nodes: [string, Node][]; ledger: LedgerEntry[] }

const MIME: Record<string, string> = {
  pdf: 'application/pdf', png: 'image/png', jpg: 'image/jpeg', jpeg: 'image/jpeg',
  docx: 'application/vnd.openxmlformats-officedocument.wordprocessingml.document',
  xlsx: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
}
const previewable = (name: string) => /\.(pdf|png|jpe?g)$/i.test(name)
const mimeOf = (name: string) => MIME[name.split('.').pop()?.toLowerCase() ?? ''] ?? 'application/octet-stream'
const sizeOf = (s: string) => 30000 + (s.split('').reduce((a, c) => a + c.charCodeAt(0), 0) % 90000)
const fold = (s: string) => s.normalize('NFC').toLowerCase()
const counts = () => ({ moved: 0, renamed: 0, replaced: 0, skipped: 0, duplicate: 0, failed: 0 })

/** “name (2).ext”：与后端相同的保留两者命名。 */
export function uniqueName(name: string, isDir: boolean, taken: (n: string) => boolean): string {
  const dot = isDir ? -1 : name.lastIndexOf('.')
  const stem = dot > 0 ? name.slice(0, dot) : name, ext = dot > 0 ? name.slice(dot) : ''
  for (let n = 2; n < 1000; n++) { const c = `${stem} (${n})${ext}`; if (!taken(c)) return c }
  return name
}

interface Leaf { from: string; to: string; action: 'move' | 'skip' | 'dup' | 'fail' | 'cleanup'; rename?: boolean; replace?: string; dupOf?: string; code?: string; msg?: string; status?: FsItemStatus; root: number }

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
  private ledgerOf(n: Node) { return n.ledgerId ? this.ledger.find((x) => x.id === n.ledgerId) : undefined }
  private entry(path: string, n: Node): FsEntry {
    const l = this.ledgerOf(n)
    const task = !!l && !n.is_dir
    const hash_status: FsEntry['hash_status'] = !task ? 'not_task' : n.verdict ?? 'unverified'
    return {
      name: basename(path), path, is_dir: n.is_dir, size_bytes: n.size, modified_at: n.modified,
      ...(n.is_dir ? {} : { mime_type: n.mime ?? mimeOf(path), previewable: previewable(path) }),
      ledger: l ? { id: l.id, original_name: l.original_name, department: l.department, document_type: l.document_type, document_date: l.document_date } : null,
      task_related: task, ...(task ? { sha256: l!.sha256 } : {}), hash_status,
    }
  }
  private info(path: string, n: Node): FsFileInfo {
    const l = this.ledgerOf(n)
    return { path, name: basename(path), is_dir: n.is_dir, size_bytes: n.size, modified_at: n.modified, task_related: !!l, ...(l && !n.is_dir ? { sha256: l.sha256 } : {}) }
  }
  private requireDir(dir: string) {
    if (dir && !this.nodes.get(dir)?.is_dir) throw new ApiError(404, 'NOT_FOUND', '文件夹不存在')
  }
  /** 大小写与 NFC 不敏感的同名查找（与后端 Lookup 一致）。 */
  private lookup(dir: string, name: string): string | null {
    const want = fold(name)
    for (const k of this.nodes.keys()) if (dirname(k) === dir && fold(basename(k)) === want) return k
    return null
  }
  private shaOf(n: Node | undefined): string | undefined { return n && !n.is_dir ? this.ledgerOf(n)?.sha256 : undefined }

  /** 演示/测试钩子：模拟文件在系统外被改动（校验时变为“不一致”）。 */
  tamper(path: string) { this.sync(); const n = this.nodes.get(path); if (n) { n.tampered = true; n.verdict = undefined } }

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
    if (this.lookup(dirname(p), basename(p))) throw new ApiError(409, 'CONFLICT', '同名文件或文件夹已存在')
    const n: Node = { is_dir: true, size: 0, modified: new Date().toISOString() }
    this.nodes.set(p, n)
    return this.entry(p, n)
  }

  // ---- 移动（含冲突策略） ----

  /** 单项移动：失败抛错；冲突（ask）抛 409 + details.conflicts。 */
  move(from: string, to: string, opts: ConflictOptions = {}): MoveResult {
    const out = this.moveBatch([{ from, to }], opts)
    if (out.conflicts.length) throw new ApiError(409, 'ALREADY_EXISTS', `发现 ${out.conflicts.length} 处冲突，未做任何改动`, undefined, { conflicts: out.conflicts })
    const it = out.items[0]
    if (it.status === 'failed') throw new ApiError(it.code === 'NOT_FOUND' ? 404 : it.code === 'INVALID_PATH' ? 400 : 409, it.code ?? 'FAILED', it.message ?? '失败')
    const final = it.final_path && this.nodes.get(it.final_path)
    return { ...(final ? { entry: this.entry(it.final_path!, final) } : {}), ledger_updated: it.ledger_updated, status: it.status, final_path: it.final_path, hash_status: it.hash_status, modified_outside: it.modified_outside, duplicate_of: it.duplicate_of, message: it.message }
  }

  bulkMove(paths: string[], targetDir: string, opts: ConflictOptions = {}): FsBatchResult {
    const dir = this.clean(targetDir)
    const out = this.moveBatch(paths.map((p) => ({ from: this.clean(p), to: join(dir, basename(p)) })), opts)
    if (out.conflicts.length) throw new ApiError(409, 'NAME_CONFLICT', `发现 ${out.conflicts.length} 处冲突，未做任何改动`, undefined, { conflicts: out.conflicts })
    return out
  }

  private moveBatch(specs: { from: string; to: string }[], opts: ConflictOptions): FsBatchResult {
    this.sync()
    const def: ConflictPolicy = opts.on_conflict ?? 'ask'
    const conflicts: FsConflict[] = []
    const leaves: Leaf[] = []
    const results: FsItemResult[] = specs.map((s) => ({ path: s.from, status: 'moved', ledger_updated: 0, hash_status: 'not_task', counts: counts() }))
    const early = new Set<number>()
    const taken = new Set<string>()
    const movingAll = new Set(specs.map((s) => s.from))
    const finishEarly = (i: number, status: FsItemStatus, code: string | undefined, message: string) => {
      early.add(i); results[i].status = status; results[i].code = code; results[i].message = message
      results[i].counts = { ...counts(), [status === 'failed' ? 'failed' : 'skipped']: 1 }
    }
    const plan = (ri: number, from: string, to: string, pol: ConflictPolicy) => {
      const src = this.nodes.get(from)!
      const dir = dirname(to), name = basename(to)
      const exKey = this.lookup(dir, name)
      const push = (l: Omit<Leaf, 'root'>) => leaves.push({ ...l, root: ri })
      const conflict = (c: Omit<FsConflict, 'item'>) => conflicts.push({ ...c, item: specs[ri].from })
      if (!exKey) {
        if (taken.has(fold(to))) {
          if (pol === 'ask') conflict({ kind: 'name_exists', name, incoming: this.info(from, src), identical: false, message: '所选项目中有另一项移动后会得到同样的名称', choices: ['skip', 'rename'] })
          else if (pol === 'skip') push({ from, to, action: 'skip', msg: '已跳过：同批次中另一项目标相同' })
          else if (pol === 'rename') { const nn = uniqueName(name, src.is_dir, (c) => taken.has(fold(join(dir, c))) || !!this.lookup(dir, c)); taken.add(fold(join(dir, nn))); push({ from, to: join(dir, nn), action: 'move', rename: true }) }
          else push({ from, to, action: 'fail', code: 'BATCH_DUPLICATE', msg: '同一批次中另一项目标相同，不能替换' })
          return
        }
        const sha = this.shaOf(src)
        if (sha && !src.is_dir) {
          const dup = [...this.nodes].find(([k, n]) => k !== from && !movingAll.has(k) && (dir === '' || isInside(k, dir)) && this.shaOf(n) === sha)
          if (dup) {
            if (pol === 'ask') { conflict({ kind: 'duplicate_content', name, incoming: { ...this.info(from, src), sha256: sha }, existing: this.info(dup[0], dup[1]), identical: true, message: `目标文件夹中已有内容完全相同的文件“${dup[0]}”（SHA-256 相同，名称不同）`, choices: ['skip', 'rename'] }); return }
            if (pol === 'skip') { push({ from, to, action: 'dup', dupOf: dup[0], msg: '目标中已有相同内容的文件：' + dup[0] }); return }
          }
        }
        taken.add(fold(to)); push({ from, to, action: 'move' }); return
      }
      if (exKey === from) { taken.add(fold(to)); push({ from, to, action: 'move' }); return }
      const ex = this.nodes.get(exKey)!
      if (src.is_dir && ex.is_dir) {
        for (const [k] of [...this.nodes]) if (dirname(k) === from) plan(ri, k, join(exKey, basename(k)), pol)
        push({ from, to: exKey, action: 'cleanup' })
        return
      }
      const type = src.is_dir !== ex.is_dir
      const sa = this.shaOf(src), sb = this.shaOf(ex)
      const identical = !type && !!sa && sa === sb
      if (identical && pol !== 'rename') { push({ from, to, action: 'dup', dupOf: exKey, msg: '目标位置已有相同内容的同名文件，未重复放入' }); return }
      const c: Omit<FsConflict, 'item'> = {
        kind: type ? 'type_conflict' : 'name_exists', name, incoming: { ...this.info(from, src), ...(sa ? { sha256: sa } : {}) }, existing: { ...this.info(exKey, ex), ...(sb ? { sha256: sb } : {}) }, identical,
        message: type ? '类型冲突：文件与文件夹同名，不会互相替换' : '目标位置已有同名文件，内容不同', choices: type ? ['skip', 'rename'] : ['skip', 'rename', 'replace'],
      }
      if (pol === 'ask') conflict(c)
      else if (pol === 'skip') push({ from, to, action: 'skip', msg: '已跳过：' + c.message })
      else if (pol === 'rename') { const nn = uniqueName(name, src.is_dir, (x) => taken.has(fold(join(dir, x))) || !!this.lookup(dir, x)); taken.add(fold(join(dir, nn))); push({ from, to: join(dir, nn), action: 'move', rename: true }) }
      else if (type) push({ from, to, action: 'fail', code: 'TYPE_CONFLICT', msg: '文件与文件夹同名，不能互相替换（请选择跳过或保留两者）' })
      else { taken.add(fold(to)); push({ from, to, action: 'move', replace: exKey }) }
    }
    specs.forEach((s, i) => {
      const pol = opts.resolutions?.[s.from] ?? def
      let from: string, to: string
      try { from = this.clean(s.from); to = this.cleanName(s.to) } catch { finishEarly(i, 'failed', 'INVALID_PATH', '路径无效'); return }
      const src = this.nodes.get(from)
      if (!from || !src) return finishEarly(i, 'failed', 'NOT_FOUND', '文件或文件夹不存在')
      if (specs.some((o) => o.from !== s.from && isInside(from, o.from) && this.nodes.get(o.from)?.is_dir)) return finishEarly(i, 'skipped', undefined, '已包含在所选文件夹中')
      if (from === to) return finishEarly(i, 'skipped', undefined, '已在目标位置')
      if (src.is_dir && isInside(to, from)) return finishEarly(i, 'failed', 'INVALID_PATH', '不能把文件夹移动到它自身内部')
      if (dirname(to) && !this.nodes.get(dirname(to))?.is_dir) return finishEarly(i, 'failed', 'NOT_FOUND', '目标文件夹不存在')
      plan(i, from, to, pol)
    })
    if (conflicts.length) return { items: [], summary: counts(), conflicts }

    // 执行
    const now = new Date().toISOString()
    for (const l of leaves.filter((x) => x.action !== 'cleanup')) {
      const r = results[l.root]
      if (l.action === 'skip') { l.status = 'skipped'; continue }
      if (l.action === 'dup') { l.status = 'duplicate'; continue }
      if (l.action === 'fail') { l.status = 'failed'; r.code ??= l.code; r.message ??= l.msg; continue }
      const keys = [...this.nodes.keys()].filter((k) => isInside(k, l.from))
      const touched = keys.map((k) => this.nodes.get(k)!).filter((n) => this.ledgerOf(n) && !n.is_dir)
      if (touched.some((n) => n.tampered)) { r.hash_status = 'modified_outside'; (r.modified_outside ??= []).push(...keys.filter((k) => this.nodes.get(k)!.tampered).map((k) => l.to + k.slice(l.from.length))) }
      else if (touched.length && r.hash_status !== 'modified_outside') r.hash_status = 'verified'
      if (l.replace) this.trashEntry(l.replace)
      for (const k of keys) {
        const n = this.nodes.get(k)!
        const nk = l.to + k.slice(l.from.length)
        this.nodes.delete(k); this.nodes.set(nk, n)
        if (!n.tampered && this.ledgerOf(n) && !n.is_dir) n.verdict = 'verified'
        const led = this.ledgerOf(n)
        if (led) { led.archive_path = nk; led.canonical_name = basename(nk); r.ledger_updated++ }
      }
      this.nodes.get(l.to)!.modified = now
      l.status = l.replace ? 'replaced' : l.rename ? 'renamed' : 'moved'
    }
    for (const l of [...leaves].reverse()) if (l.action === 'cleanup' && ![...this.nodes.keys()].some((k) => dirname(k) === l.from)) this.nodes.delete(l.from)
    const summary = counts()
    results.forEach((r, i) => {
      if (early.has(i)) { summary[r.status === 'failed' ? 'failed' : 'skipped']++; return }
      const mine = leaves.filter((l) => l.root === i && l.action !== 'cleanup')
      const c = r.counts
      for (const l of mine) {
        if (l.status === 'moved') c.moved++; else if (l.status === 'renamed') c.renamed++; else if (l.status === 'replaced') c.replaced++
        else if (l.status === 'skipped') c.skipped++; else if (l.status === 'duplicate') { c.duplicate++; r.duplicate_of ??= l.dupOf } else c.failed++
      }
      const total = mine.length, ok = c.moved + c.renamed + c.replaced
      r.status = !total ? 'moved' : c.failed === total ? 'failed' : ok && total > ok ? 'partial' : c.duplicate === total ? 'duplicate' : c.skipped + c.duplicate === total ? 'skipped'
        : c.failed ? 'partial' : c.replaced && total === 1 ? 'replaced' : c.renamed && total === 1 ? 'renamed' : 'moved'
      r.final_path = mine.length === 1 ? mine[0].to : specs[i].to
      if (mine.length === 1 && mine[0].action !== 'move') delete r.final_path
      ;(Object.keys(summary) as (keyof typeof summary)[]).forEach((k) => { summary[k] += c[k] })
    })
    return { items: results, summary, conflicts: [] }
  }

  // ---- 回收站 ----

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
    const item: TrashItem = { id: uuid(), original_path: p, name: basename(p), is_dir: n.is_dir, size_bytes: size, deleted_at: new Date().toISOString() }
    this.trash.unshift({ item, nodes, ledger })
    return item
  }

  bulkTrash(paths: string[]): FsBatchResult {
    this.sync()
    const items: FsItemResult[] = []
    const summary = counts()
    const sel = paths.map((p) => normalize(p))
    const covered = new Set(sel.filter((p) => sel.some((o) => o !== p && isInside(p, o) && this.nodes.get(o)?.is_dir))) // decided before anything is deleted
    for (const p of sel) {
      const r: FsItemResult = { path: p, status: 'moved', ledger_updated: 0, hash_status: 'not_task', counts: counts() }
      if (covered.has(p)) { r.status = 'skipped'; r.message = '已包含在所选文件夹中'; r.counts.skipped = 1 }
      else {
        try {
          const before = this.ledger.length
          this.trashEntry(p)
          r.ledger_updated = before - this.ledger.length; r.counts.moved = 1
        } catch (e) { r.status = 'failed'; r.code = e instanceof ApiError ? e.code : 'FAILED'; r.message = e instanceof Error ? e.message : '失败'; r.counts.failed = 1 }
      }
      ;(Object.keys(summary) as (keyof typeof summary)[]).forEach((k) => { summary[k] += r.counts[k] })
      items.push(r)
    }
    return { items, summary, conflicts: [] }
  }

  listTrash(): TrashItem[] { return this.trash.map((t) => t.item) }

  restore(id: string, onConflict?: ConflictPolicy): RestoreResult {
    this.sync()
    const i = this.trash.findIndex((t) => t.item.id === id)
    if (i < 0) throw new ApiError(404, 'NOT_FOUND', '回收站中没有该项目')
    const rec = this.trash[i]
    const orig = rec.item.original_path
    let target = orig
    let status: RestoreResult['status'] = 'restored'
    let duplicateOf: string | undefined
    const existing = this.lookup(dirname(orig), basename(orig))
    if (!onConflict) { // 旧行为：自动加序号
      if (existing) {
        const base = basename(orig), dot = rec.item.is_dir ? -1 : base.lastIndexOf('.')
        const stem = dot > 0 ? base.slice(0, dot) : base, ext = dot > 0 ? base.slice(dot) : ''
        target = orig
        for (let n = 1; this.nodes.has(target); n++) target = join(dirname(orig), `${stem} (${n})${ext}`)
      }
    } else {
      const root = rec.nodes.find(([k]) => k === orig)?.[1]
      const sha = this.shaOf(root)
      const mk = (kind: FsConflict['kind'], message: string, ex: FsFileInfo | undefined, identical: boolean, choices: FsConflict['choices']): FsConflict => ({
        item: id, kind, name: rec.item.name, incoming: { path: '.trash/' + id, name: rec.item.name, is_dir: rec.item.is_dir, size_bytes: rec.item.size_bytes, modified_at: rec.item.deleted_at, task_related: rec.ledger.length > 0, ...(sha ? { sha256: sha } : {}) },
        ...(ex ? { existing: ex } : {}), identical, message, choices,
      })
      let replace: string | undefined
      if (existing) {
        const ex = this.nodes.get(existing)!
        const exInfo = { ...this.info(existing, ex), ...(this.shaOf(ex) ? { sha256: this.shaOf(ex) } : {}) }
        const type = ex.is_dir !== rec.item.is_dir
        if (type || rec.item.is_dir) {
          if (onConflict === 'ask') throw new ApiError(409, 'ALREADY_EXISTS', '发现 1 处冲突，未做任何改动', undefined, { conflicts: [mk(type ? 'type_conflict' : 'folder_exists', type ? '类型冲突：文件与文件夹同名，不会互相替换' : '原位置已有同名文件夹（还原不会合并或替换文件夹）', exInfo, false, ['skip', 'rename'])] })
          if (onConflict === 'skip') return { path: orig, ledger_updated: 0, status: 'skipped', hash_status: 'not_task', message: '已跳过' }
          if (onConflict === 'replace') throw new ApiError(400, 'INVALID_PATH', '不能替换文件夹，也不能让文件与文件夹互相替换')
        } else if (sha && sha === this.shaOf(ex) && onConflict !== 'rename') {
          return { path: existing, ledger_updated: 0, status: 'duplicate', hash_status: 'not_task', duplicate_of: existing, message: '原位置已有相同内容的同名文件，未重复还原（项目仍在回收站）' }
        } else {
          if (onConflict === 'ask') throw new ApiError(409, 'ALREADY_EXISTS', '发现 1 处冲突，未做任何改动', undefined, { conflicts: [mk('name_exists', '原位置已有同名文件，内容不同', exInfo, false, ['skip', 'rename', 'replace'])] })
          if (onConflict === 'skip') return { path: orig, ledger_updated: 0, status: 'skipped', hash_status: 'not_task', message: '已跳过' }
          if (onConflict === 'replace') replace = existing
        }
        if (onConflict === 'rename') {
          target = join(dirname(orig), uniqueName(basename(orig), rec.item.is_dir, (c) => !!this.lookup(dirname(orig), c)))
          status = 'renamed'
        }
      } else if (sha) {
        const dup = [...this.nodes].find(([, n]) => this.shaOf(n) === sha)
        if (dup) {
          if (onConflict === 'ask') throw new ApiError(409, 'ALREADY_EXISTS', '发现 1 处冲突，未做任何改动', undefined, { conflicts: [mk('duplicate_content', `相同内容的文件已归档在“${dup[0]}”（SHA-256 相同，名称或位置不同）`, this.info(dup[0], dup[1]), true, ['skip', 'rename'])] })
          if (onConflict === 'skip') return { path: orig, ledger_updated: 0, status: 'skipped', hash_status: 'not_task', duplicate_of: dup[0], message: '已跳过：相同内容已归档在 ' + dup[0] }
          duplicateOf = dup[0]
        }
      }
      if (replace) { this.trashEntry(replace); status = 'replaced' }
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
    return { path: target, ledger_updated: updated, status, hash_status: updated ? 'verified' : 'not_task', ...(duplicateOf ? { duplicate_of: duplicateOf } : {}) }
  }

  // ---- 校验 ----

  verify(paths: string[]): FsVerifyReport {
    this.sync()
    const rep: FsVerifyReport = { items: [], checked: 0, match: 0, mismatch: 0, missing: 0, not_task: 0, truncated: false }
    const seen = new Set<string>()
    for (const raw of paths) {
      const p = this.clean(raw)
      const n = this.nodes.get(p)
      if (!n) throw new ApiError(404, 'NOT_FOUND', '文件或文件夹不存在')
      const files = n.is_dir ? [...this.nodes].filter(([k, x]) => isInside(k, p) && !x.is_dir && this.ledgerOf(x)) : [[p, n] as [string, Node]]
      for (const [k, x] of files) {
        if (seen.has(k)) continue
        seen.add(k)
        const l = this.ledgerOf(x)
        if (!l) { rep.not_task++; rep.items.push({ path: k, status: 'not_task', size_bytes: x.size, message: '不是任务相关文件（台账中没有记录），无需校验' }); continue }
        rep.checked++
        if (x.tampered) {
          x.verdict = 'mismatch'; rep.mismatch++
          rep.items.push({ path: k, status: 'mismatch', expected_sha256: l.sha256, actual_sha256: 'ffff' + l.sha256.slice(4), size_bytes: x.size, ledger_id: l.id, message: '磁盘内容与台账记录的 SHA-256 不一致（文件可能在系统外被修改）' })
        } else { x.verdict = 'verified'; rep.match++; rep.items.push({ path: k, status: 'match', expected_sha256: l.sha256, actual_sha256: l.sha256, size_bytes: x.size, ledger_id: l.id }) }
      }
    }
    return rep
  }
}
