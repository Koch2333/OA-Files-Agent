import type { LlmPreset } from '../api/types'
import type { DesktopBridge } from './desktop'

/**
 * 密钥向导的纯逻辑（桌面向导窗口和网页版浮动指引共用同一份数据：后端 presets.json 的 key_guide / key_pattern）。
 * 桌面版的步骤状态机在 desktop/src/keyhelper-core.js（同样的规则，两边各有测试）。
 */
export interface KeyStep { title: string; hint: string; warn?: string }
export interface KeyGuide { suggested_name: string; steps: KeyStep[]; login_note?: string; cost_note?: string }

/** 这个服务商有没有分步指引（没有就回到旧的三行说明）。 */
export function keyGuideOf(p: Pick<LlmPreset, 'key_guide'> | undefined): KeyGuide | null {
  const g = p?.key_guide
  if (!g || !Array.isArray(g.steps) || g.steps.length === 0) return null
  return g as KeyGuide
}

/** desktop：应用自带的向导窗口（页面和指引并排）；web：新标签页 + 本页的浮动指引。 */
export type HelperMode = 'desktop' | 'web'
export function helperMode(bridge: Pick<DesktopBridge, 'openKeyHelper'> | null | undefined): HelperMode {
  return bridge && typeof bridge.openKeyHelper === 'function' ? 'desktop' : 'web'
}
export function helperModeText(m: HelperMode): string {
  return m === 'desktop'
    ? '桌面版：会打开一个向导窗口，网页在左边、指引在右边。'
    : '网页版：密钥页面会在新标签页打开，指引显示在本页；复制密钥后回到这里即可。'
}
export function helperButtonText(m: HelperMode): string {
  return m === 'desktop' ? '打开向导窗口，一步步带你拿到 Key' : '打开密钥页面并显示指引'
}

/** 密钥名称：可打印字符，最长 80（后端会再清理一次）。 */
export function sanitizeKeyLabel(s: string | null | undefined): string {
  return (s ?? '').replace(/[\u0000-\u001f\u007f]/g, ' ').trim().slice(0, 80)
}

/** presets.json 的 key_pattern（RE2/JS 通用写法）；无效时返回 null。 */
export function keyPatternOf(p: Pick<LlmPreset, 'key_pattern'> | undefined): RegExp | null {
  if (!p?.key_pattern || p.key_pattern.length > 300) return null
  try { return new RegExp(p.key_pattern) } catch { return null }
}

// ---------- 步骤状态机（网页版指引） ----------
export interface StepState { step: number; total: number; detected: boolean }
export type StepEvent =
  | { type: 'next' } | { type: 'prev' } | { type: 'goto'; step: number }
  | { type: 'detected' } | { type: 'cleared' }

export const initialSteps = (total: number): StepState => ({ step: 0, total, detected: false })

/** 最后一步是“回到这里”：手动“下一步”可以到达它，识别到密钥会自动跳到它。 */
export function stepReducer(s: StepState, e: StepEvent): StepState {
  const last = s.total - 1
  switch (e.type) {
    case 'next': return s.detected ? s : { ...s, step: Math.min(s.step + 1, last) }
    case 'prev': return s.detected ? { ...s, detected: false, step: Math.max(last - 1, 0) } : { ...s, step: Math.max(s.step - 1, 0) }
    case 'goto': return Number.isInteger(e.step) && e.step >= 0 && e.step <= last ? { ...s, step: e.step, detected: false } : s
    case 'detected': return { ...s, detected: true, step: last }
    case 'cleared': return s.detected ? { ...s, detected: false, step: Math.max(last - 1, 0) } : s
  }
}
