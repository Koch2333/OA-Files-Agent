import { describe, expect, it } from 'vitest'
import { ApiError, type FsBatchResult, type FsConflict } from '../api/types'
import { applyAll, batchSummary, choiceLabel, conflictsOf, defaultPicks, groupByItem, pickable, summarize, toResolutions } from './fsConflicts'

const info = (path: string) => ({ path, name: path.split('/').pop()!, is_dir: false, size_bytes: 1, modified_at: '2026-01-01T00:00:00Z', task_related: true })
const c = (item: string, kind: FsConflict['kind'], choices: FsConflict['choices'], path = item): FsConflict => ({ item, kind, name: path.split('/').pop()!, incoming: info(path), identical: false, message: '', choices })

describe('fsConflicts', () => {
  it('reads structured conflicts only from a 409 with details.conflicts', () => {
    const list = [c('a', 'name_exists', ['skip'])]
    expect(conflictsOf(new ApiError(409, 'NAME_CONFLICT', 'x', undefined, { conflicts: list }))).toEqual(list)
    expect(conflictsOf(new ApiError(409, 'ALREADY_EXISTS', 'x'))).toBeNull()
    expect(conflictsOf(new ApiError(400, 'X', 'x', undefined, { conflicts: list }))).toBeNull()
    expect(conflictsOf(new Error('x'))).toBeNull()
    expect(conflictsOf(new ApiError(409, 'X', 'x', undefined, { conflicts: [] }))).toBeNull()
  })
  it('groups merge conflicts per selected item and intersects the allowed choices', () => {
    const g = groupByItem([c('dir', 'name_exists', ['skip', 'rename', 'replace'], 'dir/a'), c('dir', 'type_conflict', ['skip', 'rename'], 'dir/b'), c('f', 'name_exists', ['skip', 'rename', 'replace'])])
    expect(g.map((x) => [x.item, x.conflicts.length, x.choices.join('/')])).toEqual([['dir', 2, 'skip/rename'], ['f', 1, 'skip/rename/replace']])
  })
  it('defaults to skip everywhere and apply-all falls back to skip where the choice is not allowed', () => {
    const g = groupByItem([c('t', 'type_conflict', ['skip', 'rename']), c('n', 'name_exists', ['skip', 'rename', 'replace'])])
    expect(defaultPicks(g)).toEqual({ t: 'skip', n: 'skip' })
    expect(applyAll(g, 'replace')).toEqual({ t: 'skip', n: 'replace' })
    expect(applyAll(g, 'rename')).toEqual({ t: 'rename', n: 'rename' })
    expect(pickable(g, 'replace')).toBe(1)
    expect(toResolutions({ t: 'rename' })).toEqual({ t: 'rename' })
  })
  it('labels: replace says the old file goes to the trash; duplicate-content keep is "still put it here"', () => {
    const dup = { conflicts: [c('x', 'duplicate_content', ['skip', 'rename'])] }
    expect(choiceLabel('replace')).toBe('替换（旧文件进回收站）')
    expect(choiceLabel('rename', dup)).toBe('仍然放入')
    expect(choiceLabel('rename', { conflicts: [c('x', 'name_exists', ['rename'])] })).toBe('保留两者')
    expect(choiceLabel('skip')).toBe('跳过')
  })
  it('summaries', () => {
    expect(summarize([c('a', 'name_exists', []), c('b', 'name_exists', []), c('c', 'type_conflict', [])])).toBe('2 项同名文件（内容不同），1 项文件与文件夹同名')
    const r = { items: [], conflicts: [], summary: { moved: 2, renamed: 1, replaced: 0, skipped: 1, duplicate: 1, failed: 1 } } as FsBatchResult
    expect(batchSummary(r)).toBe('移动 2 项，保留两者 1 项，内容相同未重复放入 1 项，跳过 1 项，失败 1 项')
    expect(batchSummary({ ...r, summary: { moved: 0, renamed: 0, replaced: 0, skipped: 0, duplicate: 0, failed: 0 } })).toBe('没有需要处理的项目')
  })
})
