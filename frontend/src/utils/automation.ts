import { computed, getCurrentScope, onScopeDispose, ref, type Ref } from 'vue'
import type { ImportOptions, LlmStatus } from '../api/types'
import type { Role } from './authz'
import { automationGate } from './aiStatus'

export type Automation = 'review' | 'auto'
/** “确认”按钮的强制等待时间（毫秒）。 */
export const AUTO_CONFIRM_DELAY_MS = 3000

/**
 * 本页会话内对风险提示的确认记忆：只在内存里（模块变量），绝不写入 localStorage/sessionStorage/Cookie，
 * 每次页面加载都会重新询问；提示版本变化后同样重新询问。
 */
let ackedVersion: string | null = null
export function hasAcknowledged(version: string): boolean { return ackedVersion === version }
export function rememberAcknowledged(version: string) { ackedVersion = version }
export function forgetAcknowledged() { ackedVersion = null }

export function canConfirmAuto(elapsedMs: number, checked: boolean, delayMs = AUTO_CONFIRM_DELAY_MS): boolean {
  return checked && elapsedMs >= delayMs
}

export interface OptionsInput {
  instruction?: string
  automation?: Automation
  /** 全自动必须携带：LlmStatus.auto_ack_version */
  ackVersion?: string | null
  llmSource?: 'auto' | 'global' | 'personal'
}

/**
 * 组装 ImportOptions：没有任何非默认项时返回 undefined（请求里不带 options 字段）。
 * 全自动但缺少确认版本时抛错——绝不静默降级或伪造确认。
 */
export function buildImportOptions(i: OptionsInput): ImportOptions | undefined {
  const o: ImportOptions = {}
  const text = (i.instruction ?? '').trim()
  if (text) o.instruction = text.slice(0, 2000)
  if (i.llmSource && i.llmSource !== 'auto') o.llm_source = i.llmSource
  if (i.automation === 'auto') {
    if (!i.ackVersion) throw new Error('全自动模式缺少风险确认')
    o.automation = 'auto'
    o.auto_ack = { version: i.ackVersion, accepted: true }
  }
  return Object.keys(o).length ? o : undefined
}

/**
 * 模式选择状态机（人工审核 / 全自动）：
 * 选择“全自动” → 允许时打开警告对话框（本页已确认过同一版本则直接生效）；
 * 取消/Esc → 回到人工审核；确认需勾选且等待 3 秒；确认后记住（仅本页会话）。
 */
export function useAutomation(status: Ref<LlmStatus | null | undefined>, role: Ref<Role | null | undefined>, delayMs = AUTO_CONFIRM_DELAY_MS) {
  const mode = ref<Automation>('review')
  const dialogOpen = ref(false)
  const checked = ref(false)
  const elapsed = ref(0)
  let openedAt = 0
  let timer: ReturnType<typeof setInterval> | undefined

  const gate = computed(() => automationGate(status.value, role.value))
  const remainingSec = computed(() => Math.max(0, Math.ceil((delayMs - elapsed.value) / 1000)))
  const canConfirm = computed(() => dialogOpen.value && canConfirmAuto(elapsed.value, checked.value, delayMs))

  function stop() { if (timer) { clearInterval(timer); timer = undefined } }
  function tick() { elapsed.value = Date.now() - openedAt }
  function open() {
    checked.value = false; elapsed.value = 0; openedAt = Date.now(); dialogOpen.value = true
    stop(); timer = setInterval(tick, 200)
  }
  function close() { dialogOpen.value = false; checked.value = false; stop() }

  function select(m: Automation) {
    if (m === 'review') { mode.value = 'review'; close(); return }
    const s = status.value
    if (!gate.value.allowed || !s) return
    if (hasAcknowledged(s.auto_ack_version)) { mode.value = 'auto'; return }
    open()
  }
  function confirm(): boolean {
    tick()
    const s = status.value
    if (!canConfirm.value || !s) return false
    rememberAcknowledged(s.auto_ack_version)
    mode.value = 'auto'
    close()
    return true
  }
  /** 取消或按 Esc：保持/恢复人工审核。 */
  function cancel() { mode.value = 'review'; close() }
  /** 状态变化（策略被关闭、版本更新、换账号）后若不再满足条件，回退到人工审核。 */
  function revalidate() {
    if (mode.value === 'auto' && (!gate.value.allowed || !status.value || !hasAcknowledged(status.value.auto_ack_version))) mode.value = 'review'
  }
  function ackVersion(): string | null { return mode.value === 'auto' && status.value ? status.value.auto_ack_version : null }
  function options(instruction?: string): ImportOptions | undefined {
    return buildImportOptions({ instruction, automation: mode.value, ackVersion: ackVersion() })
  }
  if (getCurrentScope()) onScopeDispose(stop)
  return { mode, dialogOpen, checked, remainingSec, canConfirm, gate, select, confirm, cancel, revalidate, options, ackVersion }
}
