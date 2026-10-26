import { describe, expect, it } from 'vitest'
import { allSelected, barText, click, emptySelection, key, prune, selectAll, selectRange, someSelected, toggle, toggleAll } from './fsSelection'

const order = ['a', 'b', 'c', 'd', 'e']
const set = (s: { paths: ReadonlySet<string> }) => [...s.paths].sort()

describe('fsSelection', () => {
  it('toggles single items and remembers the anchor', () => {
    let s = toggle(emptySelection(), 'b')
    expect(set(s)).toEqual(['b']); expect(s.anchor).toBe('b')
    s = toggle(s, 'd'); expect(set(s)).toEqual(['b', 'd'])
    s = toggle(s, 'b'); expect(set(s)).toEqual(['d'])
  })
  it('shift selects an inclusive range from the anchor in either direction', () => {
    let s = toggle(emptySelection(), 'b')
    s = selectRange(s, order, 'd'); expect(set(s)).toEqual(['b', 'c', 'd'])
    s = selectRange(s, order, 'a'); expect(set(s)).toEqual(['a', 'b'])
    expect(s.anchor).toBe('b')
  })
  it('ctrl+shift extends instead of replacing; shift without anchor selects just the row', () => {
    let s = toggle(emptySelection(), 'a')
    s = click(s, order, 'e', { shift: true, ctrl: true }); expect(set(s)).toEqual(['a', 'b', 'c', 'd', 'e'])
    expect(set(selectRange(emptySelection(), order, 'c'))).toEqual(['c'])
    expect(set(click(toggle(emptySelection(), 'a'), order, 'c', { shift: true }))).toEqual(['a', 'b', 'c'])
    expect(set(click(toggle(emptySelection(), 'a'), order, 'c', { meta: true }))).toEqual(['a', 'c'])
  })
  it('range ignores rows that are not visible (filtered order) and unknown targets', () => {
    const s = toggle(emptySelection(), 'a')
    expect(set(selectRange(s, ['a', 'c', 'e'], 'e'))).toEqual(['a', 'c', 'e'])
    expect(selectRange(s, order, 'zzz')).toBe(s)
  })
  it('select-all state and toggleAll keep hidden selections', () => {
    const s = toggle(emptySelection(), 'z')
    expect(allSelected(s, order)).toBe(false)
    const all = toggleAll(s, order)
    expect(allSelected(all, order)).toBe(true); expect(all.paths.has('z')).toBe(true)
    const none = toggleAll(all, order)
    expect(set(none)).toEqual(['z'])
    expect(someSelected(toggle(emptySelection(), 'a'), order)).toBe(true)
    expect(someSelected(selectAll(order), order)).toBe(false)
    expect(allSelected(emptySelection(), [])).toBe(false)
  })
  it('keyboard: space toggles the focused row, ctrl/cmd+A selects visible rows, Escape clears', () => {
    expect(set(key(emptySelection(), order, ' ', 'c')!)).toEqual(['c'])
    expect(key(emptySelection(), order, ' ', null)).toBeNull()
    expect(set(key(emptySelection(), order, 'a', null, { ctrl: true })!)).toEqual(order)
    expect(set(key(emptySelection(), order, 'A', null, { meta: true })!)).toEqual(order)
    expect(key(emptySelection(), order, 'a', null)).toBeNull()
    expect(key(selectAll(order), order, 'Escape', null)!.paths.size).toBe(0)
    expect(key(emptySelection(), order, 'Escape', null)).toBeNull()
  })
  it('prune drops vanished paths and keeps the same object when nothing changed', () => {
    const s = selectAll(order)
    expect(prune(s, order)).toBe(s)
    const p = prune(s, ['a', 'b'])
    expect(set(p)).toEqual(['a', 'b']); expect(p.anchor).toBe('a')
    expect(prune(toggle(emptySelection(), 'z'), ['a']).anchor).toBeNull()
  })
  it('bar text mentions hidden selections', () => {
    expect(barText(3)).toBe('已选 3 项')
    expect(barText(3, 2)).toBe('已选 3 项（其中 2 项被筛选隐藏）')
  })
})
