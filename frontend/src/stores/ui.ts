import { defineStore } from 'pinia'
import { ref, watch } from 'vue'
import { applyM3, SEEDS, type ThemeMode } from '../theme/theme'

function load<T>(k: string, d: T): T {
  try { const v = localStorage.getItem(k); return v ? (JSON.parse(v) as T) : d } catch { return d }
}
function save(k: string, v: unknown) { try { localStorage.setItem(k, JSON.stringify(v)) } catch { /* ignore */ } }

export const useUi = defineStore('ui', () => {
  const mode = ref<ThemeMode>(load('oa.theme.mode', 'system'))
  const seed = ref<string>(load('oa.theme.seed', SEEDS[0].hex))
  const navCollapsed = ref<boolean>(load('oa.nav.collapsed', false))
  const toasts = ref<{ id: number; text: string; kind: 'info' | 'error' | 'success' }[]>([])
  let n = 0

  function apply() { applyM3(seed.value, mode.value) }
  watch([mode, seed], () => { save('oa.theme.mode', mode.value); save('oa.theme.seed', seed.value); apply() })
  watch(navCollapsed, (v) => save('oa.nav.collapsed', v))
  window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => { if (mode.value === 'system') apply() })

  function toast(text: string, kind: 'info' | 'error' | 'success' = 'info') {
    const id = ++n
    toasts.value.push({ id, text, kind })
    setTimeout(() => { toasts.value = toasts.value.filter((t) => t.id !== id) }, kind === 'error' ? 7000 : 4000)
  }
  return { mode, seed, navCollapsed, toasts, apply, toast }
})
