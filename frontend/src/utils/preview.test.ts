import { describe, expect, it } from 'vitest'
import { clampPage, columnCount, fillPageTemplate, headingLevel, kindLabel, maxSideFor, rowLimitMessage, splitHeader, zoomWidth } from './preview'
import { confidencePct, isPoorOcr, profileLabel, qualityClass, qualityLabel } from './ocr'
import { mockApi } from '../mocks/mockApi'

describe('preview helpers', () => {
  it('clamps pages', () => {
    expect(clampPage(0, 3)).toBe(1)
    expect(clampPage(9, 3)).toBe(3)
    expect(clampPage(2.7, 3)).toBe(2)
    expect(clampPage(NaN, 3)).toBe(1)
    expect(clampPage(5, null)).toBe(1)
  })
  it('fills the page template', () => {
    expect(fillPageTemplate('/v1/files/x/preview/page?page={page}&max_side=1400', 4)).toBe('/v1/files/x/preview/page?page=4&max_side=1400')
  })
  it('formats the row-limit notice', () => expect(rowLimitMessage(200, 1234)).toBe('仅显示前 200 行，共 1234 行'))
  it('computes zoom widths', () => {
    expect(zoomWidth('fit', 800)).toBe('100%')
    expect(zoomWidth('100', 800)).toBe('800px')
    expect(zoomWidth('150', 801)).toBe('1202px')
    expect(zoomWidth('150', 0)).toBe('100%')
    expect(maxSideFor('150')).toBeGreaterThan(maxSideFor('fit'))
  })
  it('limits heading levels and labels kinds', () => {
    expect(headingLevel(0)).toBe(1); expect(headingLevel(9)).toBe(6); expect(headingLevel(undefined)).toBe(1); expect(headingLevel(3)).toBe(3)
    expect(kindLabel('spreadsheet')).toBe('表格')
  })
  it('splits sheet rows', () => {
    expect(splitHeader([['a'], ['b'], ['c']])).toEqual({ head: ['a'], body: [['b'], ['c']] })
    expect(splitHeader([])).toEqual({ head: [], body: [] })
    expect(columnCount([['a'], ['a', 'b', 'c'], []])).toBe(3)
  })
})

describe('ocr helpers', () => {
  it('maps labels', () => {
    expect(qualityLabel('poor')).toBe('差'); expect(qualityClass('good')).toBe('success'); expect(qualityClass('fair')).toBe('attention'); expect(qualityClass('poor')).toBe('error'); expect(qualityClass(null)).toBe('')
    expect(profileLabel('high_dpi')).toBe('高清渲染'); expect(profileLabel('vision')).toBe('视觉模型'); expect(profileLabel('x')).toBe('x')
    expect(confidencePct(0.876)).toBe('88%'); expect(confidencePct(null)).toBe('—')
  })
  it('detects poor OCR', () => {
    expect(isPoorOcr({ ocr: { used: true, quality: 'poor', mean_confidence: 0.3, profile: 'default', attempts: 1, vision_used: false } })).toBe(true)
    expect(isPoorOcr({ ocr: null })).toBe(false)
  })
})

describe('mock preview', () => {
  it('previews archived files by kind', async () => {
    const doc = await mockApi.previewArchived('归档/x/a.docx')
    expect(doc.kind).toBe('document')
    expect(doc.blocks?.some((b) => b.type === 'heading')).toBe(true)
    expect(doc.blocks?.some((b) => b.type === 'table')).toBe(true)
    const xls = await mockApi.previewArchived('归档/x/a.xlsx')
    expect(xls.kind).toBe('spreadsheet')
    expect(xls.sheets).toHaveLength(3)
    expect(xls.sheets?.some((s) => s.truncated && s.rows_total > s.rows.length)).toBe(true)
    const pdf = await mockApi.previewArchived('归档/x/a.pdf')
    expect(pdf.kind).toBe('pdf'); expect(pdf.pages).toBe(3); expect(pdf.page_url).toContain('{page}')
    expect((await mockApi.previewArchived('归档/x/a.png')).kind).toBe('image')
    const old = await mockApi.previewArchived('归档/x/a.doc')
    expect(old.kind).toBe('unsupported'); expect(old.message).toMatch(/转换/)
  })
  it('page urls are distinct inline svg images', () => {
    const a = mockApi.previewPageUrl({ kind: 'archived', path: '归档/a.pdf' }, 1, 1400)
    const b = mockApi.previewPageUrl({ kind: 'archived', path: '归档/a.pdf' }, 2, 1400)
    expect(a.startsWith('data:image/svg+xml')).toBe(true)
    expect(a).not.toBe(b)
  })
  it('rejects unknown files', async () => {
    await expect(mockApi.previewFile('nope')).rejects.toMatchObject({ status: 404 })
  })
})
