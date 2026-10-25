import { describe, expect, it } from 'vitest'
import type { PickedFile } from '../api/types'
import { mergePicked, summarize, verdict } from './importLimits'

const pf = (name: string, size = 10, rel = name): PickedFile => ({ file: Object.defineProperty(new File([], name), 'size', { value: size }), relativePath: rel })

describe('import limits', () => {
  it('classifies files', () => {
    expect(verdict({ name: 'a.PDF', size: 5 })).toBe('ok')
    expect(verdict({ name: 'a.doc', size: 5 })).toBe('legacy')
    expect(verdict({ name: 'a.zip', size: 5 })).toBe('unsupported')
    expect(verdict({ name: 'a.pdf', size: 0 })).toBe('empty')
    expect(verdict({ name: 'a.pdf', size: 50 * 1024 ** 3 })).toBe('ok') // no size limit
  })
  it('rejected files never block; there is no size or count limit', () => {
    const s = summarize([pf('a.pdf'), pf('b.zip'), pf('c.pdf', 8 * 1024 ** 3)])
    expect(s).toMatchObject({ count: 3, ok: 2, rejected: 1, blocking: false, overCount: false, overBatch: false })
    expect(s.rejectedList.map((r) => r.verdict)).toEqual(['unsupported'])
    const many = Array.from({ length: 5000 }, (_, i) => pf(`f${i}.pdf`))
    expect(summarize(many)).toMatchObject({ count: 5000, ok: 5000, overCount: false, blocking: false })
  })
  it('merge dedupes by relative path', () => {
    expect(mergePicked([pf('a.pdf')], [pf('a.pdf'), pf('b.pdf'), pf('b.pdf')]).map((p) => p.relativePath)).toEqual(['a.pdf', 'b.pdf'])
  })
})
