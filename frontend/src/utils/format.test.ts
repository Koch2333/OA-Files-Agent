import { describe, expect, it } from 'vitest'
import { fmtBytes, flowIndex } from './format'
import { safeRelativePath } from './paths'

describe('safeRelativePath', () => {
  it('normalizes separators', () => expect(safeRelativePath('a\\b/c.docx')).toBe('a/b/c.docx'))
  it('rejects traversal', () => expect(safeRelativePath('a/../../etc/passwd')).toBeNull())
  it('rejects absolute paths', () => { expect(safeRelativePath('/etc/x')).toBeNull(); expect(safeRelativePath('C:\\x\\y')).toBeNull() })
})
describe('format', () => {
  it('bytes', () => expect(fmtBytes(2048)).toBe('2.0 KB'))
  it('approved sits between review and archiving', () => { expect(flowIndex('approved')).toBeGreaterThan(flowIndex('awaiting_review')); expect(flowIndex('approved')).toBeLessThan(flowIndex('archiving')) })
})
