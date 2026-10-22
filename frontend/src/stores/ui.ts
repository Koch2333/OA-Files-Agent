import { defineStore } from 'pinia'
import { ref, watch } from 'vue'
import { applyM3, SEEDS, type ThemeMode } from '../theme/theme'
import { extractRequestId } from '../utils/errorInfo'
import { copyText } from '../utils/clipboard'

export interface ToastAction { label: string; run: () => void }

function load<T>(k: string, d: T): T {
  try { const v = localStorage.getItem(k); return v ? (JSON.parse(v) as T) : d } catch { return d }
}
function save(k: string, v: unknown) { try { localStorage.setItem(k, JSON.stringify(v)) } catch { /* ignore */ } }
function loadSession(k: string): boolean | null { try { const v = sessionStorage.getItem(k); return v === null ? null : JSON.parse(v) === true } catch { return null } }

export const useUi = defineStore('ui', () => {
  const mode = ref<ThemeMode>(load('oa.theme.mode', 'system'))
  const seed = ref<string>(load('oa.theme.seed', SEEDS[0].hex))
  const navCollapsed = ref<boolean>(load('oa.nav.collapsed', false))
  const density = ref<'comfortable' | 'compact'>(load('oa.density', 'comfortable'))
  /** 导航「设置与管理」分组：本会话内记住展开/收起（null = 未手动操作，默认收起；子页面激活时自动展开） */
  const settingsOpen = ref<boolean | null>(loadSession('oa.nav.settingsOpen'))
  const toasts = ref<{ id: number; text: string; kind: 'info' | 'error' | 'success'; action?: ToastAction }[]>([])
  let n = 0

  function apply() { applyM3(seed.value, mode.value); document.documentElement.dataset.density = density.value }
  watch(density, (v) => { save('oa.density', v); apply() })
  watch(settingsOpen, (v) => { try { if (v === null) sessionStorage.removeItem('oa.nav.settingsOpen'); else sessionStorage.setItem('oa.nav.settingsOpen', JSON.stringify(v)) } catch { /* ignore */ } })
  watch([mode, seed], () => { save('oa.theme.mode', mode.value); save('oa.theme.seed', seed.value); apply() })
  watch(navCollapsed, (v) => save('oa.nav.collapsed', v))
  window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => { if (mode.value === 'system') apply() })

  function toast(text: string, kind: 'info' | 'error' | 'success' = 'info', action?: ToastAction) {
    const id = ++n
    // 错误提示里带“请求编号”时自动附带“复制”按钮，便于用户把编号发给管理员
    const act = action ?? (kind === 'error' && extractRequestId(text) ? { label: '复制错误信息', run: () => { void copyText(text).then((ok) => { if (ok) toast('已复制', 'success') }) } } : undefined)
    toasts.value.push({ id, text, kind, action: act })
    setTimeout(() => { toasts.value = toasts.value.filter((t) => t.id !== id) }, act ? 15000 : kind === 'error' ? 7000 : 4000)
  }
  return { mode, seed, navCollapsed, density, settingsOpen, toasts, apply, toast }
})
