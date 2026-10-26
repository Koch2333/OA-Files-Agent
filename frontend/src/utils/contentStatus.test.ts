import { afterEach, describe, expect, it, vi } from 'vitest'
import type { ContentInfo } from '../api/types'
import { mockApi } from '../mocks/mockApi'
import { contentChips, isNameOnly, nameOnlyCount, nameOnlySummary, planChip, reasonLabel } from './contentStatus'

const c = (over: Partial<ContentInfo>): ContentInfo => ({ status: 'ok', reason: null, basis: 'content', format_mismatch: null, ...over })

describe('content status helpers', () => {
  it('labels reasons', () => {
    expect(reasonLabel('encrypted')).toBe('已加密'); expect(reasonLabel('ocr_empty')).toBe('未能识别出文字'); expect(reasonLabel('capped')).toMatch(/前面部分/); expect(reasonLabel(null)).toBe('原因未知')
  })
  it('flags filename-only files with a tooltip that explains why', () => {
    const chips = contentChips(c({ status: 'unreadable', reason: 'encrypted', basis: 'filename_only' }))
    expect(chips).toHaveLength(1)
    expect(chips[0]).toMatchObject({ text: '仅依据文件名', tone: 'attention' })
    expect(chips[0].title).toBe('内容无法读取（已加密），仅依据文件名判断')
  })
  it('shows partial and mismatch notes, nothing for plain readable files', () => {
    expect(contentChips(c({ status: 'partial', reason: 'damaged' })).map((x) => x.text)).toEqual(['部分内容'])
    expect(contentChips(c({ format_mismatch: 'html' })).map((x) => x.text)).toEqual(['扩展名与内容不符'])
    expect(contentChips(c({}))).toEqual([]); expect(contentChips(null)).toEqual([]); expect(contentChips(undefined)).toEqual([])
  })
  it('plan item chip comes from basis + the backend sentence', () => {
    expect(planChip('filename_only', '内容无法读取（已加密），仅依据文件名判断')?.title).toContain('已加密')
    expect(planChip('content', null)).toBeNull(); expect(planChip(undefined, null)).toBeNull()
  })
  it('counts name-only files for the task summary', () => {
    const files = [{ content: c({ basis: 'filename_only', status: 'unreadable', reason: 'damaged' }) }, { content: c({}) }, { content: null }, {}]
    expect(nameOnlyCount(files)).toBe(1); expect(nameOnlyCount(null)).toBe(0)
    expect(nameOnlySummary(3)).toBe('3 个文件无法读取内容，已按文件名处理')
    expect(isNameOnly(files[0].content)).toBe(true); expect(isNameOnly(null)).toBe(false)
  })
})

afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals() })

describe('mock layer mirrors the backend: unreadable files are classified from their name, never failed', () => {
  it('imports unknown formats and empty files as filename-only items', async () => {
    vi.stubGlobal('window', globalThis) // the mock schedules its simulation through window.setTimeout
    vi.useFakeTimers()
    const mk = (name: string, size: number) => ({ file: Object.defineProperty(new File([], name), 'size', { value: size }), relativePath: name })
    const jobP = mockApi.createImport([mk('采购部_合同_2026-05-01.dwg', 10), mk('空文件.pdf', 0), mk('加密_报销.pdf', 10), mk('正常.docx', 10)], {})
    await vi.advanceTimersByTimeAsync(600)
    const job = await jobP
    const filesP = mockApi.listTaskFiles(job.id)
    await vi.advanceTimersByTimeAsync(400)
    const files = (await filesP).sort((a, b) => a.source_name.localeCompare(b.source_name))
    const by = Object.fromEntries(files.map((f) => [f.source_name, f]))
    for (const n of ['采购部_合同_2026-05-01.dwg', '空文件.pdf', '加密_报销.pdf']) {
      expect(by[n].error).toBeNull(); expect(by[n].review_required).toBe(true)
      expect(by[n].content).toMatchObject({ status: 'unreadable', basis: 'filename_only' })
      expect(by[n].evidence[0]).toMatch(/^内容无法读取（.+），仅依据文件名判断$/)
    }
    expect(by['采购部_合同_2026-05-01.dwg'].content?.reason).toBe('unsupported')
    expect(by['采购部_合同_2026-05-01.dwg'].detected_department).toBe('采购部') // what the name says is still used
    expect(by['采购部_合同_2026-05-01.dwg'].detected_date).toBe('2026-05-01')
    expect(by['空文件.pdf'].content?.reason).toBe('empty'); expect(by['加密_报销.pdf'].content?.reason).toBe('encrypted')
    expect(by['正常.docx'].content).toMatchObject({ status: 'ok', basis: 'content' })
  })
  it('the seeded demo task shows filename-only plan items', async () => {
    const tasks = (await mockApi.listTasks({ page_size: 100 })).items.filter((t) => t.plan_id)
    const plans = await Promise.all(tasks.map((t) => mockApi.getPlan(t.plan_id!)))
    const items = plans.flatMap((p) => p.items)
    expect(items.some((i) => i.basis === 'filename_only' && i.review_required && (i.basis_note ?? '').includes('仅依据文件名判断'))).toBe(true)
    expect(items.some((i) => i.basis === 'content')).toBe(true)
  })
})
