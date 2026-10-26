import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { FORMATS, FORMAT_EXTS, GROUP_LABELS, READABLE_RE, extOf, extsOfGroups, formatOf, iconForName, isJunk, isReadable } from './formats'
import { ICON_MAP } from '../components/iconMap'

describe('format table (single source: contracts/formats.json)', () => {
  it('is a verbatim copy of the contract table', () => {
    const contract = readFileSync(join(__dirname, '../../../contracts/formats.json'), 'utf8')
    expect(readFileSync(join(__dirname, 'formats.json'), 'utf8')).toBe(contract)
  })
  it('recognises the Office 97-2003, OpenDocument, text, mail and extra image formats', () => {
    for (const n of ['a.doc', 'a.XLS', 'a.ppt', 'a.pptx', 'a.odt', 'a.ods', 'a.odp', 'a.rtf', 'a.txt', 'a.md', 'a.csv', 'a.tsv', 'a.html', 'a.htm', 'a.eml', 'a.msg', 'a.bmp', 'a.tif', 'a.tiff', 'a.webp', 'a.gif', 'a.jfif', 'a.wps', 'a.et', 'a.dps']) expect(isReadable(n), n).toBe(true)
    for (const n of ['a.zip', 'a.rar', 'a.7z', 'a.exe', 'a.dwg', 'noext', 'a.docx.bak', '.pdf']) expect(isReadable(n), n).toBe(false)
  })
  it('extension helpers', () => {
    expect(extOf('dir/a.B.DOC')).toBe('.doc'); expect(extOf('x')).toBe(''); expect(extOf('.hidden')).toBe('')
    expect(formatOf('a.xls')?.preview).toBe('spreadsheet'); expect(formatOf('a.doc')?.legacy).toBe(true)
    expect(READABLE_RE.test('A.DOCX')).toBe(true)
  })
  it('every row has a mapped icon, every group a label', () => {
    for (const f of FORMATS) { expect(ICON_MAP[f.icon], f.ext + ' ' + f.icon).toBeTruthy(); expect(f.mime).toMatch(/\//) }
    expect(iconForName('x.xls')).toBe('table_chart'); expect(iconForName('x.unknown')).toBe('draft')
    expect(new Set(GROUP_LABELS.flatMap((g) => g.group))).toEqual(new Set(FORMATS.map((f) => f.group)))
    expect(extsOfGroups(['pdf'])).toEqual(['.pdf'])
    expect(FORMAT_EXTS.length).toBe(new Set(FORMAT_EXTS).size)
  })
  it('housekeeping files are junk, real files are not', () => {
    for (const n of ['Thumbs.db', 'a/.DS_Store', '~$report.docx', 'desktop.ini', '._x.pdf']) expect(isJunk(n), n).toBe(true)
    for (const n of ['report.docx', 'a/.gitignore']) expect(isJunk(n), n).toBe(false)
  })
})
