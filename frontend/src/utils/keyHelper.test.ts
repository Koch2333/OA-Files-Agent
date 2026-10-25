import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import type { LlmPreset } from '../api/types'
import { GUIDES } from './llmGuide'
import { helperButtonText, helperMode, helperModeText, initialSteps, keyGuideOf, keyPatternOf, sanitizeKeyLabel, stepReducer } from './keyHelper'

const presets = JSON.parse(readFileSync(resolve(__dirname, '../../../backend/internal/llm/presets.json'), 'utf8')) as LlmPreset[]
const preset = (id: string) => presets.find((p) => p.id === id) as LlmPreset

describe('shared key-helper data (backend presets.json is the single source)', () => {
  it('deepseek, qwen and openai have a 5-step guide, a pattern and a host list', () => {
    for (const id of ['deepseek', 'qwen', 'openai']) {
      const p = preset(id)
      const g = keyGuideOf(p)
      expect(g, id).not.toBeNull()
      expect(g!.steps.length).toBe(5)
      expect(g!.suggested_name).toBe('OA Agent')
      expect(g!.steps.every((s) => s.title && s.hint)).toBe(true)
      expect(p.key_hosts?.length).toBeGreaterThan(0)
      expect(keyPatternOf(p)).toBeInstanceOf(RegExp)
    }
  })
  it('the key formats of llmGuide.ts equal the ones in presets.json (both detectors stay in step)', () => {
    for (const g of GUIDES) {
      const p = preset(g.id)
      if (!g.keyPattern) continue
      expect(keyPatternOf(p)?.source, g.id).toBe(g.keyPattern.source)
    }
  })
  it('OpenRouter has a 3-step sign-in list and a 5-step key guide', () => {
    const p = preset('openrouter')
    expect(p.key_guide?.login_steps?.length).toBe(3)
    expect(keyGuideOf(p)?.steps.length).toBe(5)
    expect(p.key_hosts).toEqual(['.openrouter.ai'])
  })
  it('the guide never invents prices or exact button positions', () => {
    for (const id of ['deepseek', 'qwen', 'openai']) {
      const text = JSON.stringify(preset(id).key_guide)
      expect(text, id).not.toMatch(/[¥￥$]\s*\d|\d+\s*(元|美元|USD)/)
      expect(text, id).not.toMatch(/左上角|右上角的第|第[一二三四五六七八九]个按钮/)
    }
  })
  it('keyGuideOf / keyPatternOf are defensive', () => {
    expect(keyGuideOf(undefined)).toBeNull()
    expect(keyGuideOf({ key_guide: { suggested_name: 'x', steps: [] } } as never)).toBeNull()
    expect(keyPatternOf({ key_pattern: '(' })).toBeNull()
    expect(keyPatternOf({ key_pattern: 'a'.repeat(400) })).toBeNull()
    expect(keyPatternOf({})).toBeNull()
  })
})

describe('mode and labels', () => {
  it('desktop only when the bridge can open the helper window', () => {
    expect(helperMode(null)).toBe('web')
    expect(helperMode({} as never)).toBe('web')
    expect(helperMode({ openKeyHelper: async () => ({ ok: true }) })).toBe('desktop')
    expect(helperButtonText('desktop')).toBe('打开向导窗口，一步步带你拿到 Key')
    expect(helperButtonText('web')).toBe('打开密钥页面并显示指引')
    expect(helperModeText('desktop')).toContain('桌面版')
    expect(helperModeText('web')).toContain('网页版')
  })
  it('key label is printable and at most 80 characters', () => {
    expect(sanitizeKeyLabel('  OA Agent\n')).toBe('OA Agent')
    expect(sanitizeKeyLabel('a\u0000b')).toBe('a b')
    expect(sanitizeKeyLabel('x'.repeat(200))).toHaveLength(80)
    expect(sanitizeKeyLabel(undefined)).toBe('')
  })
})

describe('step machine (same rules as desktop/src/keyhelper-core.js)', () => {
  it('moves manually, jumps on detection, and recovers when the key goes away', () => {
    let s = initialSteps(5)
    s = stepReducer(s, { type: 'next' })
    expect(s.step).toBe(1)
    s = stepReducer(s, { type: 'prev' })
    s = stepReducer(s, { type: 'prev' })
    expect(s.step).toBe(0)
    for (let i = 0; i < 9; i++) s = stepReducer(s, { type: 'next' })
    expect(s.step).toBe(4)
    s = stepReducer(initialSteps(5), { type: 'detected' })
    expect(s).toEqual({ step: 4, total: 5, detected: true })
    expect(stepReducer(s, { type: 'next' })).toBe(s)
    expect(stepReducer(s, { type: 'cleared' })).toEqual({ step: 3, total: 5, detected: false })
    expect(stepReducer(initialSteps(5), { type: 'goto', step: 7 }).step).toBe(0)
    expect(stepReducer(initialSteps(5), { type: 'goto', step: 2 }).step).toBe(2)
  })
})
