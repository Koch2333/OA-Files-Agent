import { describe, expect, it } from 'vitest'
import { ApiError, type LedgerEntry } from '../api/types'
import { MockFs } from './mockFs'

function mk() {
  const ledger: LedgerEntry[] = [{
    id: 'l1', status: 'archived', original_name: '原.docx', canonical_name: 'a.docx', department: '财务部', document_type: '合同',
    document_date: '2026-01-01', archive_path: '归档/财务部/a.docx', sha256: 'ab', task_id: 't', executed_at: '2026-01-01T00:00:00Z',
  }]
  return { ledger, fs: new MockFs(ledger) }
}
const code = (fn: () => unknown) => { try { fn() } catch (e) { return (e as ApiError).status } return 0 }

describe('MockFs', () => {
  it('lists folders first with ledger info only on ledger files', () => {
    const { fs } = mk()
    expect(fs.list('').entries.map((e) => e.name)).toEqual(['归档'])
    const l = fs.list('归档/财务部')
    expect(l.parent).toBe('归档')
    expect(l.entries[0].ledger?.original_name).toBe('原.docx')
    fs.mkdir('归档/财务部/新夹')
    const l2 = fs.list('归档/财务部')
    expect(l2.entries[0].is_dir).toBe(true)
    expect(l2.entries[0].ledger).toBeNull()
    expect(l2.capabilities).toEqual({ reveal: false, root: null })
  })
  it('404 for missing folder', () => expect(code(() => mk().fs.list('没有'))).toBe(404))
  it('mkdir rejects existing and hidden names', () => {
    const { fs } = mk()
    expect(code(() => fs.mkdir('归档'))).toBe(409)
    expect(code(() => fs.mkdir('.hidden'))).toBe(400)
    expect(code(() => fs.mkdir('归档/.x'))).toBe(400)
    expect(code(() => fs.mkdir('不存在/子'))).toBe(404)
  })
  it('move never overwrites and updates ledger', () => {
    const { fs, ledger } = mk()
    fs.mkdir('归档/财务部/夹')
    const r = fs.move('归档/财务部/a.docx', '归档/财务部/夹/b.docx')
    expect(r.ledger_updated).toBe(1)
    expect(ledger[0].archive_path).toBe('归档/财务部/夹/b.docx')
    expect(ledger[0].canonical_name).toBe('b.docx')
    fs.mkdir('归档/x')
    expect(code(() => fs.move('归档/x', '归档/财务部'))).toBe(0) // folder onto folder merges per item now; file-vs-folder is a type conflict
    expect(code(() => fs.move('归档/x', '归档/.y'))).toBe(400)
  })
  it('refuses moving a folder into itself; folder move updates descendants', () => {
    const { fs, ledger } = mk()
    expect(code(() => fs.move('归档', '归档/财务部/归档'))).toBe(400)
    const r = fs.move('归档/财务部', '归档/人事部')
    expect(r.ledger_updated).toBe(1)
    expect(ledger[0].archive_path).toBe('归档/人事部/a.docx')
  })
  it('trash removes ledger, restore adds suffix when occupied', () => {
    const { fs, ledger } = mk()
    const t = fs.trashEntry('归档/财务部/a.docx')
    expect(ledger).toHaveLength(0)
    expect(fs.listTrash()).toHaveLength(1)
    fs.mkdir('归档/财务部/占位') // unrelated
    fs.move('归档/财务部/占位', '归档/财务部/a.docx')
    const r = fs.restore(t.id)
    expect(r.path).toBe('归档/财务部/a (1).docx')
    expect(r.ledger_updated).toBe(1)
    expect(ledger[0].archive_path).toBe(r.path)
    expect(fs.listTrash()).toHaveLength(0)
    expect(code(() => fs.restore(t.id))).toBe(404)
  })
})
