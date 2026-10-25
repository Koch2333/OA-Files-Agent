import { describe, expect, it } from 'vitest'
import { ApiError, type Api, type PickedFile } from '../api/types'
import { runImport, splitDesktopPaths, SMALL_BATCH_BYTES } from './importUpload'
import type { DesktopBridge } from './desktop'

const file = (name: string, size: number) => ({ name, size, lastModified: 1, type: '', slice: (a: number, b: number) => ({ size: b - a }) as Blob }) as unknown as File
const pk = (name: string, size: number, rel = name): PickedFile => ({ file: file(name, size), relativePath: rel })

function fakeApi(over: Partial<Record<keyof Api, unknown>> = {}) {
  const calls: string[] = []
  const sessions = new Map<string, { size: number; got: number; batch: string }>()
  const api = {
    getImportConfig: async () => ({ chunk_size: 1, max_upload_bytes: null, max_batch_files: null, session_ttl_seconds: 1, disk_reserve_bytes: 0, disk_free_bytes: null, local_path_import: false, supported_extensions: [] }),
    createImport: async () => { calls.push('createImport'); return { id: 'job-small' } },
    createUpload: async (i: { batch_id?: string; size: number }) => { calls.push('create'); const id = `${i.batch_id}_${sessions.size}`; sessions.set(id, { size: i.size, got: 0, batch: i.batch_id! }); return { id, batch_id: i.batch_id, size: i.size, offset: 0, complete: false } },
    getUpload: async (id: string) => { const s = sessions.get(id)!; return { id, batch_id: s.batch, size: s.size, offset: s.got, complete: false } },
    putUploadChunk: async (id: string, off: number, c: Blob) => { calls.push('put'); const s = sessions.get(id)!; s.got = off + c.size; return { id, batch_id: s.batch, size: s.size, offset: s.got, complete: false } },
    completeUpload: async (id: string) => { calls.push('complete'); const s = sessions.get(id)!; return { id, batch_id: s.batch, size: s.size, offset: s.size, complete: true } },
    abortUpload: async () => { calls.push('abort') },
    commitImportBatch: async (b: string, o: { idempotencyKey?: string }) => { calls.push('commit:' + o.idempotencyKey); return { id: 'job-' + b } },
    discardImportBatch: async () => { calls.push('discard') },
    importFromPaths: async (i: { paths: string[] }) => { calls.push('paths:' + i.paths.join(',')); return { batch_id: 'bp', state: 'running', copied: 0, discovered: 1, bytes_copied: 0, bytes_total: 1 } },
    getImportBatch: async () => ({ batch_id: 'bp', files: [], committed: false, path_import: { batch_id: 'bp', state: 'done', copied: 1, discovered: 1, bytes_copied: 1, bytes_total: 1 } }),
    ...over,
  } as unknown as Api
  return { api, calls }
}
const deps = (api: Api, extra = {}) => ({ api, resume: null, desktop: () => null, uploader: { sleep: async () => {} }, sleep: async () => {}, ...extra })

describe('runImport', () => {
  it('uses one multipart request for small batches', async () => {
    const { api, calls } = fakeApi()
    const job = await runImport([pk('a.pdf', 1000)], {}, () => {}, deps(api)).promise
    expect(job.id).toBe('job-small')
    expect(calls).toEqual(['createImport'])
  })

  it('uses chunked upload + commit for big files, with an idempotent commit key', async () => {
    const { api, calls } = fakeApi()
    const phases: string[] = []
    const job = await runImport([pk('big.pdf', 5 * 1024 ** 3)], {}, (p) => { if (phases[phases.length - 1] !== p.phase) phases.push(p.phase) }, deps(api, { uploader: { sleep: async () => {}, chunkSize: 1024 ** 3 } })).promise
    expect(job.id).toMatch(/^job-/)
    expect(calls.filter((c) => c === 'put').length).toBe(5)
    expect(calls[calls.length - 1]).toMatch(/^commit:commit-/)
    expect(phases).toContain('uploading')
    expect(phases[phases.length - 1]).toBe('done')
  })

  it('also switches to chunking for many files (>100) even when tiny', async () => {
    const { api, calls } = fakeApi()
    const many = Array.from({ length: 250 }, (_, i) => pk(`f${i}.pdf`, 10))
    await runImport(many, {}, () => {}, deps(api)).promise
    expect(calls).not.toContain('createImport')
    expect(calls.filter((c) => c === 'create').length).toBe(250)
  })

  it('does not commit when a file failed, and says it can be resumed', async () => {
    const { api, calls } = fakeApi({ putUploadChunk: async () => { throw new ApiError(507, 'INSUFFICIENT_STORAGE', '磁盘空间不足：需要 X，剩余 Y') } })
    await expect(runImport([pk('big.pdf', SMALL_BATCH_BYTES + 1)], {}, () => {}, deps(api)).promise).rejects.toThrow(/磁盘空间不足/)
    expect(calls.some((c) => c.startsWith('commit'))).toBe(false)
  })

  it('desktop: files with real paths go through local-path import, the rest through upload, one commit', async () => {
    const d = { isDesktop: true, pathsForFiles: (fs: ArrayLike<File>) => Array.from(fs).map((f) => (f.name === 'flat.pdf' ? 'C:\\data\\flat.pdf' : '')) } as unknown as DesktopBridge
    const { api, calls } = fakeApi({ getImportConfig: async () => ({ local_path_import: true }) })
    const job = await runImport([pk('flat.pdf', 4 * 1024 ** 3), pk('x.pdf', 10, 'dir/x.pdf')], {}, () => {}, deps(api, { desktop: () => d })).promise
    expect(calls[0]).toBe('paths:C:\\data\\flat.pdf')
    expect(calls.filter((c) => c === 'create').length).toBe(1) // only the nested one is uploaded
    expect(calls[calls.length - 1]).toMatch(/^commit:/)
    expect(job.id).toMatch(/^job-/)
  })

  it('cancel aborts sessions and discards the staging batch', async () => {
    const { api, calls } = fakeApi()
    const run = runImport([pk('big.pdf', 3 * 1024 ** 3)], {}, () => {}, deps(api, { uploader: { sleep: async () => {}, chunkSize: 1024 ** 3 } }))
    await run.cancel()
    await run.promise.catch(() => {})
    expect(calls).toContain('discard')
  })
})

describe('splitDesktopPaths', () => {
  it('only treats flat files with a matching real path as direct', () => {
    const d = { isDesktop: true, pathsForFiles: () => ['/a/flat.pdf', '/a/other.pdf', '/a/dir/n.pdf', ''] } as unknown as DesktopBridge
    const r = splitDesktopPaths([pk('flat.pdf', 1), pk('x.pdf', 1), pk('n.pdf', 1, 'dir/n.pdf'), pk('z.pdf', 1)], d)
    expect(r.paths).toEqual(['/a/flat.pdf'])
    expect(r.rest.map((p) => p.file.name)).toEqual(['x.pdf', 'n.pdf', 'z.pdf'])
    expect(splitDesktopPaths([pk('a.pdf', 1)], null).rest.length).toBe(1)
  })
})
