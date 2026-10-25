import { describe, expect, it } from 'vitest'
import { ApiError, type PickedFile, type UploadSession } from '../api/types'
import { fingerprint, fmtEta, fmtSpeed, ImportUploader, retryable, type ResumeStore, type UploadTransport } from './uploader'

/** A "file" that records how it is sliced and fails the test if anything tries to read all of it. */
function bigFile(name: string, size: number, lastModified = 1) {
  const slices: [number, number][] = []
  const f = {
    name, size, lastModified, type: '',
    slice: (a: number, b: number) => { slices.push([a, b]); return { size: b - a } as Blob },
    arrayBuffer: () => { throw new Error('whole-file read') }, text: () => { throw new Error('whole-file read') }, stream: () => { throw new Error('whole-file read') },
  } as unknown as File
  return { file: f, slices }
}
const pick = (name: string, size: number, rel = name, lm = 1) => { const b = bigFile(name, size, lm); return { p: { file: b.file, relativePath: rel } as PickedFile, slices: b.slices } }

/** In-memory server with the real semantics: offset check, idempotent overlap, complete verifies size. */
class FakeServer {
  sessions = new Map<string, { s: UploadSession; got: number }>()
  puts = 0; aborted: string[] = []; created = 0; active = 0; maxActive = 0
  failPut: (n: number, id: string) => unknown = () => null
  failCreate: () => unknown = () => null
  mk(): UploadTransport {
    return {
      create: async (i) => {
        const e = this.failCreate(); if (e) throw e
        const batch = i.batch_id ?? 'b0'
        const id = `${batch}_${++this.created}`
        const s = { id, batch_id: batch, name: i.name, path: i.path ?? i.name, size: i.size, offset: 0, complete: false, source: 'upload', created_at: '', updated_at: '', expires_at: '' } as UploadSession
        this.sessions.set(id, { s, got: 0 })
        return { ...s }
      },
      query: async (id) => { const x = this.sessions.get(id); if (!x) throw new ApiError(404, 'NOT_FOUND', 'gone'); return { ...x.s, offset: x.s.complete ? x.s.size : x.got } },
      put: async (id, offset, chunk) => {
        this.puts++
        this.active++; this.maxActive = Math.max(this.maxActive, this.active)
        try {
          await Promise.resolve()
          const err = this.failPut(this.puts, id)
          const x = this.sessions.get(id); if (!x) throw new ApiError(404, 'NOT_FOUND', 'gone')
          if (err === 'after') { x.got = Math.max(x.got, offset + chunk.size); throw new ApiError(0, 'NETWORK', 'reset') } // stored, response lost
          if (err) throw err
          if (offset > x.got) throw new ApiError(409, 'OFFSET_MISMATCH', 'mismatch', undefined, { offset: x.got })
          x.got = Math.max(x.got, offset + chunk.size)
          return { ...x.s, offset: x.got }
        } finally { this.active-- }
      },
      complete: async (id) => {
        const x = this.sessions.get(id)!
        if (x.got !== x.s.size) throw new ApiError(409, 'OFFSET_MISMATCH', 'incomplete', undefined, { offset: x.got })
        x.s.complete = true
        return { ...x.s, offset: x.s.size }
      },
      abort: async (id) => { this.aborted.push(id); this.sessions.delete(id) },
    }
  }
}
const memStore = (): ResumeStore & { data: Map<string, { uploadId: string; batchId: string }> } => {
  const data = new Map<string, { uploadId: string; batchId: string }>()
  return { data, get: (k) => data.get(k) ?? null, set: (k, v) => void data.set(k, v), remove: (k) => void data.delete(k) }
}
const noSleep = { sleep: async () => {}, resume: null as ResumeStore | null }

describe('ImportUploader', () => {
  it('uploads in chunks with File.slice only, reports progress, and completes', async () => {
    const srv = new FakeServer()
    const a = pick('a.pdf', 25), z = pick('empty.pdf', 0)
    const seen: number[] = []
    const up = new ImportUploader(srv.mk(), { chunkSize: 64 * 1024, ...noSleep, onProgress: (p) => seen.push(p.sentBytes) })
    // chunkSize is clamped to >= 64 KiB, so use a file bigger than that
    const big = pick('big.pdf', 200 * 1024)
    up.add([a.p, big.p, z.p])
    const r = await up.start()
    expect(r.allComplete).toBe(true)
    expect(big.slices).toEqual([[0, 65536], [65536, 131072], [131072, 196608], [196608, 204800]])
    expect(srv.sessions.get(`${r.batchId}_1`)!.s.complete).toBe(true)
    expect(seen[seen.length - 1]).toBe(25 + 200 * 1024)
    const snap = up.snapshot()
    expect(snap).toMatchObject({ state: 'done', doneCount: 3, failedCount: 0, etaSeconds: 0 })
  })

  it('retries with backoff after a lost response and resumes from the server offset (no duplicate bytes)', async () => {
    const srv = new FakeServer()
    srv.failPut = (n) => (n === 2 ? 'after' : n === 3 ? new ApiError(503, 'X', 'busy') : null)
    const sleeps: number[] = []
    const f = pick('b.pdf', 200 * 1024)
    const up = new ImportUploader(srv.mk(), { chunkSize: 64 * 1024, resume: null, sleep: async (ms) => void sleeps.push(ms), backoffMs: 100 })
    up.add([f.p])
    const r = await up.start()
    expect(r.allComplete).toBe(true)
    expect(sleeps.length).toBe(2)
    expect(sleeps[1]).toBeGreaterThan(sleeps[0]) // exponential
    // chunk 2 reached the server although the response was lost: the client asks for the offset and does NOT resend it
    const ranges = f.slices.map((s) => s.join('-'))
    expect(ranges.filter((x) => x === '65536-131072').length).toBe(1)
  })

  it('adopts the server offset on 409 OFFSET_MISMATCH', async () => {
    const srv = new FakeServer()
    const t = srv.mk()
    const f = pick('c.pdf', 130 * 1024)
    const up = new ImportUploader(t, { chunkSize: 64 * 1024, ...noSleep })
    up.add([f.p])
    // the server already has 64 KiB that the client does not know about (another tab / earlier attempt)
    const created = await t.create({ name: 'c.pdf', size: 130 * 1024 })
    srv.sessions.get(created.id)!.got = 64 * 1024
    const origCreate = t.create
    t.create = async (i) => { t.create = origCreate; return { ...created, offset: 0 } }
    const r = await up.start()
    expect(r.allComplete).toBe(true)
    expect(f.slices[0]).toEqual([0, 65536]) // first attempt used offset 0 → 409
    expect(srv.sessions.get(created.id)!.s.complete).toBe(true)
  })

  it('resumes after a "page reload": a new uploader finds the session through the resume store', async () => {
    const srv = new FakeServer()
    const store = memStore()
    const f1 = pick('d.pdf', 200 * 1024, 'd.pdf', 7)
    srv.failPut = (n) => (n === 3 ? new ApiError(400, 'BAD', 'stop') : null) // dies on the 3rd chunk, not retryable
    const up1 = new ImportUploader(srv.mk(), { chunkSize: 64 * 1024, sleep: async () => {}, resume: store })
    up1.add([f1.p])
    const r1 = await up1.start()
    expect(r1.allComplete).toBe(false)
    expect(store.data.size).toBe(1)
    expect(up1.snapshot().files[0].error).toBeTruthy()
    // reload: fresh objects, same file
    srv.failPut = () => null
    const f2 = pick('d.pdf', 200 * 1024, 'd.pdf', 7)
    expect(fingerprint(f2.p)).toBe(fingerprint(f1.p))
    const up2 = new ImportUploader(srv.mk(), { chunkSize: 64 * 1024, sleep: async () => {}, resume: store })
    up2.add([f2.p])
    const r2 = await up2.start()
    expect(r2.allComplete).toBe(true)
    expect(r2.batchId).toBe(r1.batchId)
    expect(srv.created).toBe(1) // no second session
    expect(f2.slices[0][0]).toBe(131072) // continued from where the server was, not from 0
    expect(up2.snapshot().files[0].resumed).toBe(true)
    expect(store.data.size).toBe(0)
  })

  it('recreates an expired session once (404) and keeps going', async () => {
    const srv = new FakeServer()
    const f = pick('e.pdf', 100 * 1024)
    let killed = false
    srv.failPut = (n, id) => { if (n === 2 && !killed) { killed = true; srv.sessions.delete(id) } return null }
    const up = new ImportUploader(srv.mk(), { chunkSize: 64 * 1024, ...noSleep })
    up.add([f.p])
    expect((await up.start()).allComplete).toBe(true)
    expect(srv.created).toBe(2)
  })

  it('one failing file does not stop the others; 507 is not retried; a second start retries only the failed file', async () => {
    const srv = new FakeServer()
    const bad = pick('bad.pdf', 70 * 1024), ok = pick('ok.pdf', 70 * 1024)
    let failing = true
    srv.failPut = (_n, id) => (failing && srv.sessions.get(id)!.s.name === 'bad.pdf' ? new ApiError(507, 'INSUFFICIENT_STORAGE', '磁盘空间不足：需要 70 KiB，剩余 0 B') : null)
    const up = new ImportUploader(srv.mk(), { chunkSize: 64 * 1024, ...noSleep, concurrency: 1 })
    up.add([bad.p, ok.p])
    const r = await up.start()
    expect(r.allComplete).toBe(false)
    expect(r.failed.map((f) => f.name)).toEqual(['bad.pdf'])
    expect(r.failed[0].error).toContain('磁盘空间不足')
    expect(up.snapshot().files.find((f) => f.name === 'bad.pdf')!.attempts).toBe(0) // not retried
    failing = false
    const putsBefore = srv.puts
    const r2 = await up.start(r.batchId)
    expect(r2.allComplete).toBe(true)
    expect(srv.puts - putsBefore).toBeLessThanOrEqual(2) // ok.pdf is not sent again
  })

  it('gives up after maxRetries on a dead network and reports a resumable message', async () => {
    const srv = new FakeServer()
    srv.failPut = () => new ApiError(0, 'NETWORK', 'down')
    const up = new ImportUploader(srv.mk(), { chunkSize: 64 * 1024, ...noSleep, maxRetries: 3 })
    up.add([pick('f.pdf', 70 * 1024).p])
    const r = await up.start()
    expect(r.allComplete).toBe(false)
    expect(r.failed[0].error).toContain('从中断处继续')
    expect(srv.puts).toBe(4)
  })

  it('runs at most `concurrency` files at once', async () => {
    const srv = new FakeServer()
    const up = new ImportUploader(srv.mk(), { chunkSize: 64 * 1024, ...noSleep, concurrency: 3 })
    up.add(Array.from({ length: 10 }, (_, i) => pick(`f${i}.pdf`, 70 * 1024, `f${i}.pdf`, i).p))
    await up.start()
    expect(srv.maxActive).toBeGreaterThan(1)
    expect(srv.maxActive).toBeLessThanOrEqual(3)
  })

  it('abort deletes server sessions and clears resume records', async () => {
    const srv = new FakeServer()
    const store = memStore()
    let up!: ImportUploader
    srv.failPut = () => { void up.abort(); return null }
    up = new ImportUploader(srv.mk(), { chunkSize: 64 * 1024, sleep: async () => {}, resume: store })
    up.add([pick('g.pdf', 300 * 1024).p])
    await up.start()
    await up.abort()
    expect(srv.aborted.length).toBe(1)
    expect(store.data.size).toBe(0)
    expect(up.snapshot().state).toBe('aborted')
  })

  it('handles a multi-GiB file by slicing only (no whole-file read), 8 MiB chunks', async () => {
    const srv = new FakeServer()
    const huge = pick('huge.pdf', 3 * 1024 ** 3)
    const up = new ImportUploader(srv.mk(), { ...noSleep })
    up.add([huge.p])
    const r = await up.start()
    expect(r.allComplete).toBe(true)
    expect(huge.slices.length).toBe(384)
    expect(huge.slices[0]).toEqual([0, 8 * 1024 * 1024])
  })

  it('classifies errors', () => {
    expect(retryable(new ApiError(0, 'NETWORK', ''))).toBe(true)
    expect(retryable(new ApiError(503, '', ''))).toBe(true)
    expect(retryable(new ApiError(429, '', ''))).toBe(true)
    expect(retryable(new ApiError(507, '', ''))).toBe(false)
    expect(retryable(new ApiError(403, '', ''))).toBe(false)
    expect(retryable(new ApiError(422, 'CHECKSUM_MISMATCH', ''))).toBe(false)
  })

  it('formats speed and ETA', () => {
    expect(fmtSpeed(0)).toBe('—')
    expect(fmtSpeed(5 * 1024 * 1024)).toBe('5.0 MiB/s')
    expect(fmtEta(null)).toBe('计算中…')
    expect(fmtEta(42)).toBe('42 秒')
    expect(fmtEta(125)).toBe('2 分 5 秒')
    expect(fmtEta(7500)).toBe('2 小时 5 分')
  })

  it('computes speed and ETA from a sliding window', async () => {
    const srv = new FakeServer()
    let t = 0
    const up = new ImportUploader(srv.mk(), { chunkSize: 64 * 1024, ...noSleep, now: () => (t += 1000), concurrency: 1 })
    up.add([pick('s.pdf', 64 * 1024 * 10).p])
    let last = up.snapshot()
    const up2 = new ImportUploader(srv.mk(), { chunkSize: 64 * 1024, ...noSleep, now: () => (t += 1000), concurrency: 1, onProgress: (p) => { last = p } })
    up2.add([pick('s2.pdf', 64 * 1024 * 10).p])
    await up2.start()
    expect(last.speed).toBeGreaterThan(0)
    expect(last.etaSeconds).toBe(0)
    void up
  })
})
