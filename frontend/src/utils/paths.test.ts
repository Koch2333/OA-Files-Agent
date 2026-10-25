import { describe, expect, it } from 'vitest'
import { collectDropped, entryRelativePath, safeRelativePath } from './paths'

// Minimal FileSystemEntry fakes. Real browsers ALWAYS give fullPath rooted at '/', e.g. '/a.docx' or '/dir/a.docx'.
const file = (name: string, size = 3) => new File([new Uint8Array(size)], name)
const fileEntry = (fullPath: string, f: File) => ({ isFile: true, isDirectory: false, name: f.name, fullPath, file: (ok: (f: File) => void) => ok(f) })
const dirEntry = (fullPath: string, kids: unknown[]) => {
  let given = false
  return { isFile: false, isDirectory: true, name: fullPath.split('/').pop()!, fullPath, createReader: () => ({ readEntries: (ok: (e: unknown[]) => void) => { ok(given ? [] : kids); given = true } }) }
}
const dt = (entries: unknown[], files: File[] = []) => ({
  items: entries.map((e) => ({ kind: 'file', webkitGetAsEntry: () => e })),
  files,
}) as unknown as DataTransfer

describe('safeRelativePath', () => {
  it('rejects traversal and absolute paths', () => {
    expect(safeRelativePath('/a.docx')).toBeNull()
    expect(safeRelativePath('../a.docx')).toBeNull()
    expect(safeRelativePath('C:/a.docx')).toBeNull()
    expect(safeRelativePath('d\\a.docx')).toBe('d/a.docx')
  })
})
describe('entryRelativePath', () => {
  it('treats the root-anchored fullPath of a dropped entry as relative to the drop', () => {
    expect(entryRelativePath('/a.docx')).toBe('a.docx')
    expect(entryRelativePath('/dir/sub/a.docx')).toBe('dir/sub/a.docx')
    expect(entryRelativePath('/dir/../a.docx')).toBeNull()
  })
})
describe('collectDropped', () => {
  it('keeps a single dropped file (regression: every file was silently dropped because fullPath starts with "/")', async () => {
    const out = await collectDropped(dt([fileEntry('/a.docx', file('a.docx'))]))
    expect(out.map((p) => p.relativePath)).toEqual(['a.docx'])
  })
  it('keeps several files', async () => {
    const out = await collectDropped(dt([fileEntry('/a.docx', file('a.docx')), fileEntry('/b.pdf', file('b.pdf'))]))
    expect(out.map((p) => p.relativePath)).toEqual(['a.docx', 'b.pdf'])
  })
  it('walks folders and keeps relative paths', async () => {
    const tree = dirEntry('/合同', [fileEntry('/合同/a.docx', file('a.docx')), dirEntry('/合同/2024', [fileEntry('/合同/2024/b.pdf', file('b.pdf'))])])
    const out = await collectDropped(dt([tree]))
    expect(out.map((p) => p.relativePath)).toEqual(['合同/a.docx', '合同/2024/b.pdf'])
  })
  it('reports unsafe entries instead of silently dropping them', async () => {
    const skipped: string[] = []
    const out = await collectDropped(dt([fileEntry('/x/../../a.docx', file('a.docx')), fileEntry('/ok.pdf', file('ok.pdf'))]), { skipped })
    expect(out.map((p) => p.relativePath)).toEqual(['ok.pdf'])
    expect(skipped).toHaveLength(1)
  })
  it('falls back to dataTransfer.files when entries are unavailable', async () => {
    const d = { items: [{ kind: 'file' }], files: [file('z.xlsx')] } as unknown as DataTransfer
    expect((await collectDropped(d)).map((p) => p.relativePath)).toEqual(['z.xlsx'])
  })
  it('falls back to files when entries yield nothing readable', async () => {
    const bad = { isFile: true, isDirectory: false, name: 'q.pdf', fullPath: '/q.pdf', file: (_ok: unknown, err: (e: unknown) => void) => err(new Error('nope')) }
    const skipped: string[] = []
    const out = await collectDropped(dt([bad], [file('q.pdf')]), { skipped })
    expect(out.map((p) => p.relativePath)).toEqual(['q.pdf'])
  })
  it('reads dataTransfer synchronously (DataTransfer is emptied after the event handler returns)', async () => {
    let live = true
    const d = { get items() { if (!live) throw new Error('stale'); return [{ kind: 'file', webkitGetAsEntry: () => fileEntry('/a.docx', file('a.docx')) }] }, get files() { if (!live) throw new Error('stale'); return [] } } as unknown as DataTransfer
    const p = collectDropped(d)
    live = false
    expect((await p).map((x) => x.relativePath)).toEqual(['a.docx'])
  })
})
