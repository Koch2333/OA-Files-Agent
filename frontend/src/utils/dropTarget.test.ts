import { describe, expect, it, vi } from 'vitest'
import { createDropHandlers, hasFiles, installDropGuard, intakeNote, isHandled } from './dropTarget'

const ev = (types: string[] = ['Files']) => ({ dataTransfer: { types, dropEffect: 'none' }, preventDefault: vi.fn() })
const setup = (enabled = true) => {
  const state = { dragging: false }
  const onDrop = vi.fn()
  const h = createDropHandlers({ enabled: () => enabled, onDrop, setDragging: (v) => { state.dragging = v } })
  return { h, state, onDrop }
}
const pf = (name: string) => ({ file: new File(['x'], name), relativePath: name })

describe('drop handlers', () => {
  it('ignores non-file drags (text selections, links)', () => {
    const { h, state } = setup(); const e = ev(['text/plain'])
    h.enter(e); h.over(e); h.drop(e)
    expect(e.preventDefault).not.toHaveBeenCalled(); expect(state.dragging).toBe(false); expect(hasFiles(e)).toBe(false)
  })
  it('dragenter/dragover preventDefault (required or drop never fires), copy effect, drag state on', () => {
    const { h, state } = setup(); const e = ev()
    h.enter(e); h.over(e)
    expect(e.preventDefault).toHaveBeenCalledTimes(2); expect(e.dataTransfer.dropEffect).toBe('copy'); expect(state.dragging).toBe(true)
  })
  it('drop preventDefault, clears state and hands the DataTransfer over once', () => {
    const { h, state, onDrop } = setup(); const e = ev()
    h.enter(e); h.drop(e)
    expect(e.preventDefault).toHaveBeenCalled(); expect(state.dragging).toBe(false); expect(onDrop).toHaveBeenCalledWith(e.dataTransfer); expect(isHandled(e)).toBe(true)
  })
  it('child-element enter/leave pairs do not flicker the overlay off', () => {
    const { h, state } = setup()
    h.enter(ev()); h.enter(ev()); h.leave(ev())
    expect(state.dragging).toBe(true)
    h.leave(ev())
    expect(state.dragging).toBe(false)
  })
  it('disabled (read-only/submitting): still prevents navigation, shows no drop state, does not ingest', () => {
    const { h, state, onDrop } = setup(false); const e = ev()
    h.enter(e); h.over(e); h.drop(e)
    expect(e.preventDefault).toHaveBeenCalled(); expect(e.dataTransfer.dropEffect).toBe('none'); expect(state.dragging).toBe(false); expect(onDrop).not.toHaveBeenCalled()
  })
})

describe('global guard', () => {
  const install = () => {
    const l: Record<string, (e: unknown) => void> = {}
    const hint = vi.fn()
    installDropGuard({ addEventListener: ((t: string, f: (e: unknown) => void) => { l[t] = f }) as never }, hint)
    return { l, hint }
  }
  it('prevents navigation for a file dropped on an unhandled area and tells the user', async () => {
    vi.useFakeTimers(); const { l, hint } = install(); const e = ev()
    l.dragover(e); expect(e.preventDefault).toHaveBeenCalled(); expect(e.dataTransfer.dropEffect).toBe('none')
    l.drop(e); expect(e.preventDefault).toHaveBeenCalledTimes(2)
    vi.runAllTimers(); expect(hint).toHaveBeenCalledTimes(1); vi.useRealTimers()
  })
  it('stays quiet when a drop zone handled the event', () => {
    vi.useFakeTimers(); const { l, hint } = install(); const { h } = setup(); const e = ev()
    l.drop(e); h.drop(e); vi.runAllTimers(); expect(hint).not.toHaveBeenCalled(); vi.useRealTimers()
  })
  it('does not touch non-file drags', () => {
    const { l } = install(); const e = ev(['text/plain']); l.drop(e); expect(e.preventDefault).not.toHaveBeenCalled()
  })
})

describe('intakeNote', () => {
  it('explains an empty result instead of staying silent', () => {
    const n = intakeNote([], []); expect(n.kind).toBe('error'); expect(n.text).toContain('没有读取到')
  })
  it('success without limit wording', () => {
    const n = intakeNote([pf('a.docx'), pf('b.pdf')], []); expect(n).toEqual({ kind: 'success', text: '已添加 2 个文件。' })
    expect(n.text).not.toMatch(/MB|MiB|最多/)
  })
  it('flags unsupported, legacy and skipped entries', () => {
    const n = intakeNote([pf('a.docx'), pf('x.exe'), pf('o.doc')], ['/../bad'])
    expect(n.kind).toBe('info'); expect(n.text).toMatch(/1 个格式不支持/); expect(n.text).toMatch(/1 个旧版/); expect(n.text).toMatch(/1 个条目已跳过/)
  })
  it('all unsupported is an error', () => { expect(intakeNote([pf('x.exe')], []).kind).toBe('error') })
})
