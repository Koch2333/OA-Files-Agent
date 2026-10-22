import { describe, expect, it } from 'vitest'
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { FALLBACK_ICON, ICON_MAP, resolveIcon } from './iconMap'

const SRC = join(__dirname, '..')

function walk(dir: string, out: string[] = []): string[] {
  for (const n of readdirSync(dir)) {
    const p = join(dir, n)
    if (statSync(p).isDirectory()) walk(p, out)
    else if (/\.(vue|ts)$/.test(n) && !/\.test\.ts$|schema\.d\.ts$|iconMap\.ts$/.test(n)) out.push(p)
  }
  return out
}

const QUOTED = /['"]([a-z][a-z0-9_]*)['"]/g
const names = new Map<string, Set<string>>()
function add(n: string, file: string) { if (!names.has(n)) names.set(n, new Set()); names.get(n)!.add(file.slice(SRC.length + 1)) }

for (const f of walk(SRC)) {
  const text = readFileSync(f, 'utf8')
  // <Icon ... name="x"> / :name="cond ? 'a' : 'b'"
  for (const tag of text.matchAll(/<Icon\b[^>]*?>/g)) {
    const m = /(?<!:)\bname="([a-z][a-z0-9_]*)"/.exec(tag[0])
    if (m) add(m[1], f)
    for (const e of tag[0].matchAll(/:name="([^"]*)"/g)) for (const q of e[1].replace(/[!=]==? *'[^']*'/g, '').matchAll(QUOTED)) add(q[1], f)
  }
  // icon: 'x' (data tables), empty-icon="x", emptyIcon
  for (const m of text.matchAll(/\bicon: *'([a-z][a-z0-9_]*)'/g)) add(m[1], f)
  for (const m of text.matchAll(/\bempty-icon="([a-z][a-z0-9_]*)"/g)) add(m[1], f)
  // name -> icon maps: ICON / evIcon / StatusChip / KIND_ICON / iconOf / ternaries returning names
  for (const line of text.split('\n')) {
    if (/\b(ICON|KIND_ICON)\b *[:=]|const (evIcon|icon) *=|function (iconOf|kindIcon)/.test(line) && !/<Icon/.test(line)) {
      for (const q of line.matchAll(QUOTED)) {
        // only values (after ':' ) look like icon names; keep keys out by requiring a preceding ': ' or '?? ' or 'return '
        const idx = q.index ?? 0
        const before = line.slice(0, idx)
        if (/(:\s*|\?\?\s*|\?\s*)$/.test(before)) add(q[1], f)
      }
    }
  }
  for (const b of text.matchAll(/function iconOf[\s\S]*?\n}/g)) for (const q of b[0].matchAll(/return '([a-z][a-z0-9_]*)'/g)) add(q[1], f)
  // seg theme buttons: ['system', '跟随系统', 'brightness_auto']
  for (const m of text.matchAll(/\['(?:system|light|dark)', '[^']*', '([a-z_0-9]+)'\]/g)) add(m[1], f)
}

describe('icon map', () => {
  it('finds a believable set of used icon names', () => {
    expect(names.size).toBeGreaterThan(80)
    for (const n of ['space_dashboard', 'progress_activity', 'folder_open', 'cloud_sync', 'check_circle', 'brightness_auto', 'add_to_drive', 'picture_as_pdf']) expect(names.has(n), n).toBe(true)
  })

  it('maps every icon name used in src/', () => {
    const unmapped = [...names].filter(([n]) => !ICON_MAP[n]).map(([n, f]) => `${n} (${[...f].join(', ')})`)
    expect(unmapped).toEqual([])
  })

  it('has a real component for every entry', () => {
    for (const [n, c] of Object.entries(ICON_MAP)) expect(c, n).toBeTruthy()
    expect(Object.values(ICON_MAP).every((c) => typeof c === 'function' || typeof c === 'object')).toBe(true)
  })

  it('falls back to a neutral icon for unknown names', () => {
    expect(resolveIcon('definitely_not_an_icon')).toBe(FALLBACK_ICON)
    expect(resolveIcon('passkey')).toBe(ICON_MAP.passkey)
  })

  it('keeps the semantic aliases', () => {
    for (const a of ['passkey', 'upload', 'folder', 'spinner', 'trash']) expect(ICON_MAP[a], a).toBeTruthy()
  })
})
