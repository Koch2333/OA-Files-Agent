import { describe, expect, it } from 'vitest'
import { buildImportForm } from './importForm'
import type { PickedFile } from './types'

const f = (name: string, rel = name): PickedFile => ({ file: new File(['x'], name), relativePath: rel })

describe('import multipart ordering', () => {
  it('paths → options → files', () => {
    const fd = buildImportForm([f('a.pdf', '财务/a.pdf'), f('b.docx')], { instruction: '只处理采购', automation: 'auto', auto_ack: { version: 'v1', accepted: true } })
    expect([...fd.keys()]).toEqual(['paths', 'options', 'files', 'files'])
    expect(JSON.parse(fd.get('paths') as string)).toEqual(['财务/a.pdf', 'b.docx'])
    expect(JSON.parse(fd.get('options') as string)).toEqual({ instruction: '只处理采购', automation: 'auto', auto_ack: { version: 'v1', accepted: true } })
    expect(fd.getAll('files').map((x) => (x as File).name)).toEqual(['a.pdf', 'b.docx'])
  })
  it('without options the field is absent (paths → files)', () => {
    expect([...buildImportForm([f('a.pdf')]).keys()]).toEqual(['paths', 'files'])
    expect([...buildImportForm([f('a.pdf')], {}).keys()]).toEqual(['paths', 'files'])
  })
})
