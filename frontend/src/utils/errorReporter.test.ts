import { describe, expect, it, vi } from 'vitest'
import { ApiError } from '../api/types'
import { buildPayload, createReporter, safeRoute, scrub } from './errorReporter'

function make(over: Partial<Parameters<typeof createReporter>[0]> = {}) {
  let t = 1_000_000
  const sent: unknown[] = []
  const notes: { message: string; reportId?: string; copy: string }[] = []
  let n = 0
  const r = createReporter({
    send: async (p) => { sent.push(p); return { report_id: `rep-${++n}` } },
    now: () => t, enabled: () => true, route: () => '/plans/abc?token=zzz', appVersion: '1.2.3',
    notify: (i) => notes.push(i), ...over,
  })
  return { r, sent, notes, tick: (ms: number) => { t += ms } }
}
const err = (m: string) => { const e = new Error(m); e.stack = `Error: ${m}\n    at f (/assets/x.js:1:1)`; return e }

describe('client error reporter', () => {
  it('sends only message/stack/route/component/app_version, truncated, and shows a toast with the report id', async () => {
    const { r, sent, notes } = make()
    const id = await r.report(err('x'.repeat(900)), { component: 'PlanView' })
    expect(id).toBe('rep-1')
    const p = sent[0] as Record<string, string>
    expect(Object.keys(p).sort()).toEqual(['app_version', 'component', 'message', 'route', 'stack'])
    expect(p.message.length).toBeLessThanOrEqual(500)
    expect(p.route).toBe('/plans/abc') // 查询串被去掉
    expect(p.app_version).toBe('1.2.3')
    expect(notes[0].message).toBe('页面出现了问题，已记录（编号 rep-1）')
    expect(notes[0].copy).toContain('rep-1')
  })
  it('dedupes identical messages for 60 s, then reports again', async () => {
    const { r, sent, notes, tick } = make()
    await r.report(err('same'))
    expect(await r.report(err('same'))).toBe('rep-1') // 命中去重：返回已有编号，不再发送也不再提示
    expect(sent).toHaveLength(1)
    expect(notes).toHaveLength(1)
    tick(61_000)
    await r.report(err('same'))
    expect(sent).toHaveLength(2)
  })
  it('allows at most 5 reports per minute', async () => {
    const { r, sent, tick } = make()
    for (let i = 0; i < 8; i++) await r.report(err('e' + i))
    expect(sent).toHaveLength(5)
    tick(61_000)
    await r.report(err('later'))
    expect(sent).toHaveLength(6)
  })
  it('is inert when disabled (not logged in)', async () => {
    const { r, sent } = make({ enabled: () => false })
    expect(await r.report(err('x'))).toBeUndefined()
    expect(sent).toHaveLength(0)
  })
  it('ignores ApiError (server already recorded it), network TypeErrors and ResizeObserver noise', async () => {
    const { r, sent } = make()
    await r.report(new ApiError(500, 'X', 'boom', 'r1'))
    await r.report(new TypeError('Failed to fetch'))
    await r.report(new Error('ResizeObserver loop completed with undelivered notifications.'))
    expect(sent).toHaveLength(0)
  })
  it('notifies without an id when sending fails, and never throws', async () => {
    const { r, notes } = make({ send: async () => { throw new Error('down') } })
    expect(await r.report(err('x'))).toBeUndefined()
    expect(notes[0].reportId).toBeUndefined()
    expect(notes[0].message).toContain('未能自动上报')
  })
  it('silent mode records without a toast (used by the route error boundary)', async () => {
    const notify = vi.fn()
    const { r } = make({ notify })
    expect(await r.report(err('boundary'), {}, { silent: true })).toBe('rep-1')
    expect(notify).not.toHaveBeenCalled()
  })
})

describe('redaction of what is sent', () => {
  it('scrubs query strings, bearer tokens, key=value secrets and long random strings', () => {
    const s = scrub('GET https://h/v1/x?token=abc&y=1 failed; Authorization: Bearer abc.def-ghi; password=hunter2 csrf: 0123456789abcdef0123456789abcdef0123')
    expect(s).not.toContain('abc&y')
    expect(s).not.toContain('hunter2')
    expect(s).not.toContain('abc.def-ghi')
    expect(s).not.toContain('0123456789abcdef0123456789abcdef')
    expect(s).toContain('https://h/v1/x')
  })
  it('route keeps only the path and masks invitation tokens', () => {
    expect(safeRoute('/invite/AbCdEf123456?x=1#h')).toBe('/invite/:token')
    expect(safeRoute('/ledger?q=secret')).toBe('/ledger')
  })
  it('payload for a non-Error value and for stacks is truncated and scrubbed', () => {
    const e = new Error('m'); e.stack = 'Error: m\n' + 'at x (https://h/a.js?sig=SECRET:1:1)\n'.repeat(400)
    const p = buildPayload(e, '/x', '9', { component: 'C' })
    expect(p.stack!.length).toBeLessThanOrEqual(4000)
    expect(p.stack).not.toContain('SECRET')
    expect(buildPayload('plain string', '/x', '9').message).toBe('plain string')
  })
})
