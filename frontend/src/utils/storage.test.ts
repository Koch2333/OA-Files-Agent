import { describe, expect, it } from 'vitest'
import type { StorageConnection } from '../api/types'
import { MOCK_KINDS } from '../mocks/mockStorage'
import {
  buildCreatePayload, buildPatchPayload, buildTestPayload, crumbs, emptyForm, formFromConnection, groupKinds, kindIcon, validateBasePath, validateForm, visibleFields,
} from './storage'

const kind = (id: string) => MOCK_KINDS.find((k) => k.id === id)!
const saved = (over: Partial<StorageConnection> = {}): StorageConnection => ({
  id: 'c1', name: 'x', kind: 'sftp', enabled: true, base_path: '', use_for_import: true, replicate: 'off', settings: { host: 'h', username: 'u', auth: 'password' },
  secrets_set: { password: true }, last_test: null, replication: { queued: 0, running: 0, done: 0, skipped: 0, failed: 0 }, created_at: '', updated_at: '', ...over,
})

describe('field visibility', () => {
  it('follows show_if with defaults', () => {
    const k = kind('sftp')
    const keys = (v: Record<string, string>) => visibleFields(k, v).map((f) => f.key)
    expect(keys({ auth: 'password' })).toContain('password')
    expect(keys({ auth: 'password' })).not.toContain('private_key')
    expect(keys({ auth: 'key' })).toContain('private_key')
    expect(keys({})).toContain('password') // 缺省取 default=password
  })
})

describe('base path validation', () => {
  it('mirrors server rules', () => {
    expect(validateBasePath('')).toBeNull()
    expect(validateBasePath('a/b')).toBeNull()
    expect(validateBasePath('/a')).toMatch('/')
    expect(validateBasePath('a/../b')).toMatch('..')
    expect(validateBasePath('a\\b')).toMatch('反斜杠')
    expect(validateBasePath('a\nb')).toMatch('控制字符')
    expect(validateBasePath('a..b')).toBeNull()
  })
})

describe('payloads', () => {
  it('create only carries visible, non-empty values', () => {
    const k = kind('sftp')
    const f = emptyForm(k)
    f.name = ' 服务器 '; f.settings.host = 'files'; f.settings.username = 'oa'; f.secrets.password = 'pw'; f.secrets.private_key = 'KEY'
    const p = buildCreatePayload(k, f)
    expect(p.name).toBe('服务器')
    expect(p.settings).toMatchObject({ host: 'files', username: 'oa', auth: 'password', port: '22' })
    expect(p.secrets).toEqual({ password: 'pw' }) // private_key 当前不可见
    expect(p.settings).not.toHaveProperty('host_key')
  })

  it('patch never touches unchanged secrets', () => {
    const k = kind('sftp')
    const f = formFromConnection(k, saved())
    expect(f.secrets).toEqual({})
    const p = buildPatchPayload(k, f, saved())
    expect(p.secrets).toBeUndefined()
    expect(p.clear_secrets).toBeUndefined()
  })

  it('patch sends typed secret or clear request, not both', () => {
    const k = kind('sftp')
    const f = formFromConnection(k, saved())
    f.secrets.password = 'new'
    f.clearSecrets = ['password']
    expect(buildPatchPayload(k, f, saved())).toMatchObject({ secrets: { password: 'new' } })
    expect(buildPatchPayload(k, f, saved()).clear_secrets).toBeUndefined()
    const g = formFromConnection(k, saved())
    g.clearSecrets = ['password']
    expect(buildPatchPayload(k, g, saved()).clear_secrets).toEqual(['password'])
  })

  it('test payload references the saved connection and write check follows policy', () => {
    const k = kind('sftp')
    const f = formFromConnection(k, saved({ replicate: 'auto' }))
    const t = buildTestPayload(k, f, { connectionId: 'c1', acceptHostKey: 'SHA256:x' })
    expect(t).toMatchObject({ connection_id: 'c1', accept_host_key: 'SHA256:x', check_write: true })
    expect(t.secrets).toEqual({})
    expect(buildTestPayload(k, emptyForm(k), {}).check_write).toBe(false)
  })
})

describe('validateForm', () => {
  it('requires name and visible required fields; saved secrets satisfy required', () => {
    const k = kind('sftp')
    const f = emptyForm(k)
    const e = validateForm(k, f)
    expect(Object.keys(e)).toEqual(expect.arrayContaining(['name', 'host', 'username', 'password']))
    expect(e).not.toHaveProperty('private_key')
    expect(e).not.toHaveProperty('host_key') // 托管字段不必填
    const g = formFromConnection(k, saved())
    expect(validateForm(k, g, saved())).toEqual({})
    g.clearSecrets = ['password']
    expect(validateForm(k, g, saved())).toHaveProperty('password')
  })
})

describe('catalogue helpers', () => {
  it('groups kinds and falls back to a generic icon', () => {
    expect(groupKinds(MOCK_KINDS).map((g) => g.group)).toEqual(['cloud', 'enterprise', 'gateway'])
    expect(kindIcon('s3')).not.toBe(kindIcon('nope'))
    expect(kindIcon('nope')).toBe('cloud_sync')
  })
  it('builds breadcrumbs', () => {
    expect(crumbs('a/b/c')).toEqual([{ name: 'a', path: 'a' }, { name: 'b', path: 'a/b' }, { name: 'c', path: 'a/b/c' }])
    expect(crumbs('')).toEqual([])
  })
})
