import { describe, expect, it } from 'vitest'
import { ApiError, type ImportJob, type PickedFile } from '../api/types'
import { MOCK_KINDS, MockStorage, mockFingerprint } from './mockStorage'

function mk() {
  const imported: PickedFile[][] = []
  const s = new MockStorage({
    delay: 0, step: 5,
    importFn: async (f) => { imported.push(f); return { id: 'job', status: 'queued', total_files: f.length } as unknown as ImportJob },
  })
  return { s, imported }
}
const status = async (p: Promise<unknown>) => { try { await p } catch (e) { return (e as ApiError).status } return 0 }

describe('MockStorage', () => {
  it('catalogue has all seven kinds', () => {
    expect(MOCK_KINDS.map((k) => k.id).sort()).toEqual(['azure_blob', 'dingtalk', 'ftp', 's3', 'sftp', 'smb', 'webdav'])
  })

  it('validates required fields, base path and duplicate names', async () => {
    const { s } = mk()
    expect(await status(s.create({ name: '', kind: 'ftp', settings: { host: 'h' } }))).toBe(422)
    expect(await status(s.create({ name: 'a', kind: 'ftp', settings: {} }))).toBe(422)
    expect(await status(s.create({ name: 'a', kind: 'ftp', settings: { host: 'h' }, base_path: '/abs' }))).toBe(422)
    expect(await status(s.create({ name: 'a', kind: 'nope' }))).toBe(422)
    expect(await status(s.create({ name: 'a', kind: 'sftp', settings: { host: 'h', username: 'u' } }))).toBe(422) // 缺密码
    await s.create({ name: 'a', kind: 'ftp', settings: { host: 'h' } })
    expect(await status(s.create({ name: 'a', kind: 'ftp', settings: { host: 'h2' } }))).toBe(409)
  })

  it('never echoes secrets and keeps unchanged ones on patch', async () => {
    const { s } = mk()
    const c = await s.create({ name: 'k', kind: 'sftp', settings: { host: 'h', username: 'u' }, secrets: { password: 'TOPSECRET' } })
    expect(c.secrets_set).toEqual({ password: true })
    const all = JSON.stringify(await s.list()) + JSON.stringify(await s.getConn(c.id))
    expect(all).not.toContain('TOPSECRET')
    const p = await s.update(c.id, { name: 'k2' })
    expect(p.secrets_set).toEqual({ password: true })
    const cleared = await status(s.update(c.id, { clear_secrets: ['password'] }))
    expect(cleared).toBe(422) // 必填凭据不能清空
  })

  it('sftp host key trust-on-first-use', async () => {
    const { s } = mk()
    const draft = { kind: 'sftp', settings: { host: 'new.corp', username: 'u' }, secrets: { password: 'pw' } }
    const first = await s.testDraft(draft)
    expect(first.ok).toBe(false)
    expect(first.host_key).toEqual({ fingerprint: mockFingerprint('new.corp'), changed: false })
    const accepted = await s.testDraft({ ...draft, accept_host_key: first.host_key!.fingerprint })
    expect(accepted.ok).toBe(true)
    expect(accepted.host_key).toBeNull()
    // 与已固定指纹一起测试直接通过
    expect((await s.testDraft({ ...draft, settings: { ...draft.settings, host_key: first.host_key!.fingerprint } })).ok).toBe(true)
  })

  it('sftp changed key is reported as changed', async () => {
    const { s } = mk()
    const r = await s.testDraft({ kind: 'sftp', settings: { host: 'changed.corp', username: 'u', host_key: 'SHA256:old' }, secrets: { password: 'pw' } })
    expect(r.host_key?.changed).toBe(true)
    expect(r.ok).toBe(false)
  })

  it('reports auth failure, unreachable and missing write permission', async () => {
    const { s } = mk()
    const base = { kind: 'ftp', settings: { host: 'h' } }
    expect((await s.testDraft({ ...base, secrets: { password: 'wrong' } })).steps[0].message).toContain('认证失败')
    expect((await s.testDraft({ kind: 'ftp', settings: { host: 'unreachable.corp' } })).steps[0].message).toContain('无法连接')
    const w = await s.testDraft({ ...base, check_write: true })
    expect(w.steps.at(-1)?.name).toBe('写入权限')
    expect(w.ok).toBe(false)
  })

  it('browses with 502 and 404 handling', async () => {
    const { s } = mk()
    const [conn] = await s.list()
    const root = await s.browse(conn.id)
    expect(root.parent).toBeNull()
    expect(root.entries[0].is_dir).toBe(true)
    expect(await status(s.browse(conn.id, 'nope'))).toBe(404)
    expect(await status(s.browse(conn.id, '../x'))).toBe(400)
    const down = await s.create({ name: 'down', kind: 'ftp', settings: { host: 'down.corp' } })
    expect(await status(s.browse(down.id))).toBe(502)
  })

  it('replication moves queued → running → done/failed and failed can be retried', async () => {
    const { s } = mk()
    const c = (await s.list()).find((x) => x.replicate === 'auto')!
    const off = await s.create({ name: 'off', kind: 'ftp', settings: { host: 'h' } })
    expect(await status(s.replicate(off.id))).toBe(409)
    const { queued } = await s.replicate(c.id)
    expect(queued).toBeGreaterThan(0)
    expect((await s.listReps({ connection_id: c.id, status: 'queued' })).total).toBe(queued)
    await new Promise((r) => setTimeout(r, 60))
    const page = await s.listReps({ connection_id: c.id })
    expect(page.items.some((r) => r.status === 'queued' || r.status === 'running')).toBe(false)
    const failed = page.items.find((r) => r.status === 'failed')!
    expect(await status(s.retry(page.items.find((r) => r.status === 'done')!.id))).toBe(409)
    expect((await s.retry(failed.id)).status).toBe('queued')
  })

  it('remote import expands folders, preserves hierarchy and enforces limits', async () => {
    const { s, imported } = mk()
    const [conn] = await s.list()
    const job = await s.remoteImport(conn.id, ['合同扫描件'], { idempotencyKey: 'k1' })
    expect(job.id).toBe('job')
    expect(imported[0].map((f) => f.relativePath)).toContain('合同扫描件/2025/历史合同.pdf')
    await s.remoteImport(conn.id, ['合同扫描件'], { idempotencyKey: 'k1' })
    expect(imported).toHaveLength(1) // 幂等
    expect(await status(s.remoteImport(conn.id, [], {}))).toBe(400)
    expect(await status(s.remoteImport(conn.id, ['不存在'], {}))).toBe(404)
    expect(await status(s.remoteImport(conn.id, ['历史归档'], {}))).toBe(413)
  })
})
