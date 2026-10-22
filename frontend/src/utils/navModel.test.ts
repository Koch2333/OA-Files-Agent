import { describe, expect, it } from 'vitest'
import { buildNav, groupBadge, inSettingsGroup, PRIMARY_NAV, SETTINGS_NAV, settingsExpanded } from './navModel'

const names = (l: { name: string }[]) => l.map((n) => n.name)

describe('nav model', () => {
  it('keeps at most 7 primary items in the agreed order', () => {
    expect(PRIMARY_NAV.length).toBeLessThanOrEqual(7)
    expect(names(buildNav('admin').primary)).toEqual(['dashboard', 'import', 'ai', 'tasks', 'files', 'ledger', 'manage'])
  })
  it('moves low-frequency items into the settings group', () => {
    expect(names(buildNav('admin').settings)).toEqual(['templates', 'storage', 'settings', 'admin-users', 'admin-security', 'admin-sessions', 'admin-audit', 'admin-diagnostics'])
  })
  it('filters by role exactly like before', () => {
    const op = buildNav('operator')
    expect(names(op.primary)).toEqual(['dashboard', 'import', 'ai', 'tasks', 'files', 'ledger', 'manage'])
    expect(names(op.settings)).toEqual(['templates', 'settings'])
    const v = buildNav('viewer')
    expect(names(v.primary)).toEqual(['dashboard', 'tasks', 'files', 'ledger', 'manage'])
    expect(names(v.settings)).toEqual(['templates'])
  })
  it('has no duplicate names or paths and keeps the old URLs', () => {
    const all = [...PRIMARY_NAV, ...SETTINGS_NAV]
    expect(new Set(all.map((n) => n.name)).size).toBe(all.length)
    expect(new Set(all.map((n) => n.to)).size).toBe(all.length)
    expect(all.map((n) => n.to).sort()).toEqual(['/', '/admin/audit', '/admin/diagnostics', '/admin/security', '/admin/sessions', '/admin/users', '/ai', '/files', '/import', '/ledger', '/manage', '/settings', '/storage', '/tasks', '/templates'])
  })
  it('expands the group when a child route is active, otherwise lets an explicit user choice win', () => {
    expect(inSettingsGroup('storage')).toBe(true)
    expect(inSettingsGroup('tasks')).toBe(false)
    expect(settingsExpanded('storage', null)).toBe(true)
    expect(settingsExpanded('storage', false)).toBe(false)
    expect(settingsExpanded('dashboard', null)).toBe(false)
    expect(settingsExpanded('dashboard', true)).toBe(true)
  })
  it('surfaces child badges on the group header', () => {
    expect(groupBadge(SETTINGS_NAV, { diag: '3' })).toBe('3')
    expect(groupBadge(SETTINGS_NAV, {})).toBe('')
    expect(groupBadge(buildNav('operator').settings, { diag: '3' })).toBe('')
  })
  it('flags the review badge on the task centre', () => {
    expect(PRIMARY_NAV.find((n) => n.name === 'tasks')?.badge).toBe('review')
    expect(SETTINGS_NAV.find((n) => n.name === 'admin-diagnostics')?.badge).toBe('diag')
  })
})
