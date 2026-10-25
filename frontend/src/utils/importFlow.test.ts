import { describe, expect, it } from 'vitest'
import type { PickedFile } from '../api/types'
import { fromClipboard, hasSupported, pickBrief, recognisedFormats } from './importFlow'
import { mergePicked, summarize } from './importLimits'

const pf = (name: string, size = 1000, rel = name): PickedFile => ({ file: new File([new Uint8Array(Math.min(size, 10))], name), relativePath: rel }) as PickedFile
const big = (name: string, size: number): PickedFile => ({ file: { name, size } as File, relativePath: name })

describe('import flow helpers (shared by the home drop zone and ImportView)', () => {
  it('lists recognised formats in a fixed order', () => {
    expect(recognisedFormats([pf('a.pdf'), pf('b.docx'), pf('c.JPG'), pf('d.txt')])).toEqual(['Word', 'PDF', '图片'])
    expect(recognisedFormats([pf('d.txt')])).toEqual([])
  })
  it('builds the confirmation bar text', () => {
    const picked = [big('a.pdf', 1048576), big('b.xlsx', 1048576)]
    const b = pickBrief(picked)
    expect(b.text).toBe('2 个文件 · 2.0 MB · 将识别 Excel / PDF')
    expect(b.blocking).toBe(false)
    expect(b.rejected).toBe(0)
  })
  it('counts rejected files without blocking; huge batches and files are never blocked', () => {
    expect(pickBrief([pf('a.pdf'), pf('b.exe')]).rejected).toBe(1)
    expect(pickBrief([pf('a.pdf'), pf('b.exe')]).blocking).toBe(false)
    const many = Array.from({ length: 3000 }, (_, i) => big(`f${i}.pdf`, 10))
    expect(pickBrief(many)).toMatchObject({ blocking: false, blockReason: '' })
    const huge = Array.from({ length: 8 }, (_, i) => big(`h${i}.pdf`, 4 * 1024 ** 3))
    expect(summarize(huge)).toMatchObject({ overBatch: false, blocking: false })
    expect(pickBrief(huge).blockReason).toBe('')
  })
  it('reuses the same merge/dedupe rule as ImportView', () => {
    const a = [pf('a.pdf')]
    expect(mergePicked(a, [pf('a.pdf'), pf('b.pdf')]).map((p) => p.relativePath)).toEqual(['a.pdf', 'b.pdf'])
  })
  it('normalises pasted files and disambiguates duplicates', () => {
    const list = fromClipboard([new File(['1'], 'image.png'), new File(['2'], 'image.png')])
    expect(list.map((p) => p.relativePath)).toEqual(['image.png', 'image(1).png'])
    expect(fromClipboard(null)).toEqual([])
    expect(hasSupported(list)).toBe(true)
    expect(hasSupported([pf('x.txt')])).toBe(false)
  })
})
