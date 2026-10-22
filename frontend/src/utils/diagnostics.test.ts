import { describe, expect, it } from 'vitest'
import { badgeText, DEFAULT_FILTERS, diskView, filtersFromQuery, fmtUptime, reportClipboard, reportQuery, uncleanBanner } from './diagnostics'

const GIB = 1024 ** 3

describe('diagnostics filters', () => {
  it('maps UI filters to the query (open → resolved=false, all → no resolved)', () => {
    expect(reportQuery(DEFAULT_FILTERS, 1, 20)).toEqual({ page: 1, page_size: 20, resolved: false })
    expect(reportQuery({ source: 'worker', severity: 'panic', state: 'resolved', q: '  req-1 ' }, 2, 50)).toEqual({ page: 2, page_size: 50, source: 'worker', severity: 'panic', resolved: true, q: 'req-1' })
    expect(reportQuery({ ...DEFAULT_FILTERS, state: 'all' }, 1, 20).resolved).toBeUndefined()
  })
  it('restores filters from the route query and falls back for invalid values', () => {
    expect(filtersFromQuery({ source: 'supervisor', state: 'all' })).toEqual({ source: 'supervisor', severity: '', state: 'all', q: '' })
    expect(filtersFromQuery({ source: 'nope', severity: 'x', state: 'weird' })).toEqual(DEFAULT_FILTERS)
  })
})

describe('badge, disk, uptime, unclean banner', () => {
  it('badge text', () => {
    expect(badgeText(0)).toBe(''); expect(badgeText(undefined)).toBe(''); expect(badgeText(7)).toBe('7'); expect(badgeText(100)).toBe('99+')
  })
  it('disk warning below 10% or below 10 GiB', () => {
    expect(diskView({ archive_path: 'D:', free_bytes: 142 * GIB, total_bytes: 256 * GIB }).warn).toBe(false)
    expect(diskView({ archive_path: 'D:', free_bytes: 6 * GIB, total_bytes: 256 * GIB }).warn).toBe(true)
    expect(diskView({ archive_path: 'D:', free_bytes: 20 * GIB, total_bytes: 400 * GIB }).warn).toBe(true) // 5%
    expect(diskView({ archive_path: 'D:', free_bytes: 50 * GIB, total_bytes: 100 * GIB })).toMatchObject({ warn: false, freePct: 50, usedPct: 50 })
  })
  it('uptime formatting', () => {
    expect(fmtUptime(45)).toBe('45 秒'); expect(fmtUptime(300)).toBe('5 分钟'); expect(fmtUptime(3 * 3600 + 25 * 60)).toBe('3 小时 25 分'); expect(fmtUptime(2 * 86400 + 5 * 3600)).toBe('2 天 5 小时')
  })
  it('unclean-exit banner links to supervisor reports', () => {
    expect(uncleanBanner({ last_exit_unclean: false })).toBeNull()
    expect(uncleanBanner({ last_exit_unclean: true })?.to).toEqual({ path: '/admin/diagnostics', query: { source: 'supervisor', state: 'all' } })
  })
  it('clipboard text includes stack and details', () => {
    const t = reportClipboard({ id: 'i', source: 'api', severity: 'panic', code: 'PANIC', message: 'm', count: 2, first_seen: 'a', last_seen: 'b', request_id: 'r', stack: 'S', details: { k: 1 } })
    expect(t).toContain('PANIC'); expect(t).toContain('S'); expect(t).toContain('{"k":1}')
  })
})
