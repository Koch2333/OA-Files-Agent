import { describe, expect, it } from 'vitest'
import { basename, breadcrumbs, dirname, isInside, join, normalize, validateName, validatePath } from './fsPaths'

describe('fsPaths', () => {
  it('join/dirname/basename', () => {
    expect(join('', 'a')).toBe('a')
    expect(join('a/b', 'c.pdf')).toBe('a/b/c.pdf')
    expect(dirname('a/b/c.pdf')).toBe('a/b')
    expect(dirname('c.pdf')).toBe('')
    expect(basename('a/b/c.pdf')).toBe('c.pdf')
  })
  it('normalize', () => expect(normalize('\\a//b\\ c /')).toBe('a/b/c'))
  it('breadcrumbs', () => {
    expect(breadcrumbs('')).toEqual([{ label: '归档根目录', path: '' }])
    expect(breadcrumbs('a/b').map((c) => c.path)).toEqual(['', 'a', 'a/b'])
  })
  it('validateName', () => {
    expect(validateName('合同.pdf')).toBeNull()
    expect(validateName('  ')).not.toBeNull()
    expect(validateName('.hidden')).not.toBeNull()
    for (const c of ['a/b', 'a\\b', 'a:b', 'a*b', 'a?b', 'a"b', 'a<b', 'a>b', 'a|b']) expect(validateName(c)).not.toBeNull()
    expect(validateName('x'.repeat(120))).toBeNull()
    expect(validateName('x'.repeat(121))).not.toBeNull()
  })
  it('validatePath', () => {
    expect(validatePath('a/b')).toBeNull()
    expect(validatePath('')).toBeNull()
    expect(validatePath('a/../b')).not.toBeNull()
    expect(validatePath('a/.x')).not.toBeNull()
  })
  it('isInside', () => {
    expect(isInside('a/b', 'a')).toBe(true)
    expect(isInside('a', 'a')).toBe(true)
    expect(isInside('ab', 'a')).toBe(false)
  })
})
