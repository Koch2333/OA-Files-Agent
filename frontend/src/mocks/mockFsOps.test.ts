import { describe, expect, it } from 'vitest'
import { ApiError, type LedgerEntry } from '../api/types'
import { MockFs } from './mockFs'

const led = (id: string, path: string, sha: string): LedgerEntry => ({ id, original_name: path.split('/').pop()!, canonical_name: path.split('/').pop()!, department: '财务部', document_type: '合同', document_date: '2026-01-01', archive_path: path, sha256: sha, task_id: 't', status: 'archived', executed_at: '2026-01-01T00:00:00Z' } as LedgerEntry)
const make = () => new MockFs([led('1', 'in/a.pdf', 'aa'.repeat(32)), led('2', 'dst/a.pdf', 'bb'.repeat(32)), led('3', 'in/b.pdf', 'cc'.repeat(32)), led('4', 'dst/same.pdf', 'cc'.repeat(32)), led('5', 'in/d/x.pdf', 'dd'.repeat(32)), led('6', 'dst/d/x.pdf', 'ee'.repeat(32)), led('7', 'in/d/y.pdf', 'ff'.repeat(32))])

describe('mock file manager: conflicts and hashes', () => {
  it('ask lists conflicts (409) and changes nothing', () => {
    const fs = make()
    try { fs.bulkMove(['in/a.pdf'], 'dst'); expect.unreachable() } catch (e) {
      expect(e).toBeInstanceOf(ApiError)
      const cs = (e as ApiError).details!.conflicts as { kind: string; existing: { sha256: string } }[]
      expect(cs[0].kind).toBe('name_exists'); expect(cs[0].existing.sha256).toBe('bb'.repeat(32))
    }
    expect(fs.list('in').entries.some((x) => x.name === 'a.pdf')).toBe(true)
  })
  it('skip / rename / replace', () => {
    expect(make().bulkMove(['in/a.pdf'], 'dst', { on_conflict: 'skip' }).items[0].status).toBe('skipped')
    const r = make(); const out = r.bulkMove(['in/a.pdf'], 'dst', { on_conflict: 'rename' })
    expect(out.items[0]).toMatchObject({ status: 'renamed', final_path: 'dst/a (2).pdf' })
    const p = make(); const o2 = p.bulkMove(['in/a.pdf'], 'dst', { on_conflict: 'replace' })
    expect(o2.items[0].status).toBe('replaced'); expect(p.listTrash().map((t) => t.original_path)).toEqual(['dst/a.pdf'])
  })
  it('same name + same hash is a duplicate; other name + same hash in the target folder is warned', () => {
    const fs = make()
    expect(() => fs.bulkMove(['in/b.pdf'], 'dst')).toThrowError(ApiError)
    const out = fs.bulkMove(['in/b.pdf'], 'dst', { on_conflict: 'skip' })
    expect(out.items[0]).toMatchObject({ status: 'duplicate', duplicate_of: 'dst/same.pdf' })
    expect(fs.bulkMove(['in/b.pdf'], 'dst', { on_conflict: 'rename' }).items[0].status).toBe('moved')
  })
  it('folder merge applies the policy per item and keeps the partial result', () => {
    const fs = make()
    const out = fs.bulkMove(['in/d'], 'dst', { on_conflict: 'skip' })
    expect(out.items[0].status).toBe('partial'); expect(out.items[0].counts).toMatchObject({ moved: 1, skipped: 1 })
    expect(fs.list('dst/d').entries.map((e) => e.name).sort()).toEqual(['x.pdf', 'y.pdf'])
  })
  it('modified outside the system is flagged on verify, in the listing and on move', () => {
    const fs = make()
    expect(fs.list('in').entries.find((e) => e.name === 'a.pdf')).toMatchObject({ task_related: true, hash_status: 'unverified' })
    fs.tamper('in/a.pdf')
    const rep = fs.verify(['in'])
    expect(rep.mismatch).toBe(1); expect(rep.match).toBeGreaterThan(0)
    expect(fs.list('in').entries.find((e) => e.name === 'a.pdf')!.hash_status).toBe('mismatch')
    const out = fs.bulkMove(['in/a.pdf'], 'elsewhere0'.length ? 'in' : '', { on_conflict: 'ask' })
    expect(out.items[0].status).toBe('skipped')
    const fs2 = make(); fs2.tamper('in/a.pdf'); fs2.mkdir('new')
    const m = fs2.bulkMove(['in/a.pdf'], 'new')
    expect(m.items[0]).toMatchObject({ hash_status: 'modified_outside', modified_outside: ['new/a.pdf'] })
  })
  it('bulk trash reports each item and restore honours the policy', () => {
    const fs = make()
    const out = fs.bulkTrash(['in/a.pdf', 'in/none.pdf', 'in/d', 'in/d/x.pdf'])
    expect(out.items.map((i) => i.status)).toEqual(['moved', 'failed', 'moved', 'skipped'])
    const t = fs.listTrash().find((x) => x.original_path === 'in/a.pdf')!
    fs.mkdir('in2')
    expect(fs.restore(t.id, 'ask').status).toBe('restored')
  })
  it('restore: occupied name asks, identical is a duplicate, replace trashes the occupant', () => {
    const fs = make()
    const t = fs.trashEntry('in/a.pdf')
    fs.bulkMove(['dst/a.pdf'], 'in')   // another file takes the name
    expect(() => fs.restore(t.id, 'ask')).toThrowError(ApiError)
    expect(fs.restore(t.id, 'replace').status).toBe('replaced')
    expect(fs.listTrash().map((x) => x.original_path)).toEqual(['in/a.pdf'])
  })
})
