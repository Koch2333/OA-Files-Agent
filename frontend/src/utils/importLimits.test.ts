import { describe, expect, it } from 'vitest'
import type { PickedFile } from '../api/types'
import { MAX_FILE_BYTES, MAX_FILES, mergePicked, summarize, verdict } from './importLimits'

const pf = (name: string, size = 10, rel = name): PickedFile => ({ file: Object.defineProperty(new File([], name), 'size', { value: size }), relativePath: rel })

describe('import limits', () => {
  it('classifies files', () => {
    expect(verdict({ name: 'a.PDF', size: 5 })).toBe('ok')
    expect(verdict({ name: 'a.doc', size: 5 })).toBe('legacy')
    expect(verdict({ name: 'a.zip', size: 5 })).toBe('unsupported')
    expect(verdict({ name: 'a.pdf', size: 0 })).toBe('empty')
    expect(verdict({ name: 'a.pdf', size: MAX_FILE_BYTES + 1 })).toBe('too_large')
  })
  it('rejected files do not block, more than 100 files does', () => {
    const s = summarize([pf('a.pdf'), pf('b.zip'), pf('c.pdf', MAX_FILE_BYTES + 1)])
    expect(s).toMatchObject({ count: 3, ok: 1, rejected: 2, blocking: false })
    expect(s.rejectedList.map((r) => r.verdict)).toEqual(['unsupported', 'too_large'])
    const many = Array.from({ length: MAX_FILES + 1 }, (_, i) => pf(`f${i}.pdf`))
    expect(summarize(many)).toMatchObject({ overCount: true, blocking: true })
  })
  it('merge dedupes by relative path', () => {
    expect(mergePicked([pf('a.pdf')], [pf('a.pdf'), pf('b.pdf'), pf('b.pdf')]).map((p) => p.relativePath)).toEqual(['a.pdf', 'b.pdf'])
  })
})
