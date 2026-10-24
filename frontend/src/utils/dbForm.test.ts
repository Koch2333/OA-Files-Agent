import { describe, expect, it } from 'vitest'
import { buildDbRequest, emptyForm, validateDbForm } from './dbForm'
import { defaultPort } from '../api/dbAdmin'

describe('dbForm', () => {
  it('uses per-provider default ports', () => {
    expect(defaultPort('postgres')).toBe(5432)
    expect(defaultPort('mysql')).toBe(3306)
    expect(defaultPort('mssql')).toBe(1433)
    expect(emptyForm('mysql').port).toBe(3306)
  })
  it('validates required fields', () => {
    const f = emptyForm('mysql')
    expect(validateDbForm(f, false)).toContain('主机')
    f.host = 'db.local'; f.name = 'oa'; f.user = 'oa'
    expect(validateDbForm(f, false)).toContain('密码')
    expect(validateDbForm(f, true)).toBe('')
    f.port = 70000
    expect(validateDbForm(f, true)).toContain('端口')
  })
  it('never sends an empty password and falls back to the saved one', () => {
    const f = { ...emptyForm('mssql'), host: ' h ', name: 'oa', user: 'u' }
    const r = buildDbRequest(f, true)
    expect(r.use_saved_password).toBe(true)
    expect('password' in r).toBe(false)
    expect(r.host).toBe('h')
    f.password = 'pw'
    const r2 = buildDbRequest(f, true)
    expect(r2.password).toBe('pw')
    expect(r2.use_saved_password).toBeUndefined()
  })
})
