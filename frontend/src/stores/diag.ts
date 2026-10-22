import { defineStore } from 'pinia'
import { ref } from 'vue'
import { api, type SystemInfo } from '../api'
import { useAuth } from './auth'
import { createManagedPoller } from '../utils/schedule'

/** 管理员的运行状态：导航角标（未处理错误数）与诊断页共用，每 60 秒刷新（隐藏时暂停，失败时退避）。 */
export const useDiag = defineStore('diag', () => {
  const system = ref<SystemInfo | null>(null)
  const error = ref<string | null>(null)
  const auth = useAuth()

  async function fetchSystem() {
    try { system.value = await api.getSystemInfo(); error.value = null } catch (e) { error.value = e instanceof Error ? e.message : String(e); throw e }
  }
  /** 手动刷新（诊断页）：不抛出，错误在 error 中 */
  async function refresh() { try { await fetchSystem() } catch { /* 见 error */ } }
  const poller = createManagedPoller({
    intervalMs: 60_000, maxIntervalMs: 600_000,
    enabled: () => !!auth.me && !!auth.setup?.initialized,
    run: fetchSystem,
  })
  function start() { stop(); if (auth.me && auth.setup?.initialized) void refresh(); poller.start() }
  function stop() { poller.stop() }
  function reset() { stop(); system.value = null }
  return { system, error, refresh, start, stop, reset }
})
