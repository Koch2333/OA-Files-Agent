import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { apiLevelMismatch, KNOWN_STATIC_PATHS, REQUIRED_API_LEVEL, staleBackendHint, STALE_BACKEND_HINT } from './apiLevel'

describe('apiLevelMismatch', () => {
  it('is silent when the levels match or the status is unknown', () => {
    expect(apiLevelMismatch({ api_level: REQUIRED_API_LEVEL, server_version: 'x' })).toBeNull()
    expect(apiLevelMismatch(null)).toBeNull()
  })
  it('reports an old backend, including one that does not report a level', () => {
    const m = apiLevelMismatch({ api_level: REQUIRED_API_LEVEL - 1, server_version: 'v1' })!
    expect(m.kind).toBe('old')
    expect(m.message).toContain('后端版本过旧')
    expect(m.message).toContain(`需要 ${REQUIRED_API_LEVEL}`)
    expect(m.message).toContain('v1')
    expect(apiLevelMismatch({})!.kind).toBe('old')
  })
  it('reports a newer backend', () => {
    expect(apiLevelMismatch({ api_level: REQUIRED_API_LEVEL + 1 })!.message).toContain('后端版本过新')
  })
})

describe('staleBackendHint', () => {
  it('only hints for 404 on a known contract path', () => {
    expect(staleBackendHint(404, '/v1/llm/status')).toBe(STALE_BACKEND_HINT)
    expect(staleBackendHint(404, '/v1/admin/system?x=1')).toBe(STALE_BACKEND_HINT)
    expect(staleBackendHint(404, '/v1/tasks/abc')).toBeNull()
    expect(staleBackendHint(500, '/v1/llm/status')).toBeNull()
  })
  it('matches the static paths of the contract exactly', () => {
    const yaml = readFileSync(new URL('../../../contracts/openapi.yaml', import.meta.url), 'utf8')
    const paths = [...yaml.matchAll(/^ {2}(\/v1\/[^:\s]+):\s*$/gm)].map((m) => m[1]).filter((p) => !p.includes('{')).sort()
    expect([...KNOWN_STATIC_PATHS].sort()).toEqual(paths)
  })
  it('keeps REQUIRED_API_LEVEL equal to the backend constant', () => {
    const go = readFileSync(new URL('../../../backend/internal/api/setup.go', import.meta.url), 'utf8')
    expect(Number(/const APILevel = (\d+)/.exec(go)![1])).toBe(REQUIRED_API_LEVEL)
  })
})
