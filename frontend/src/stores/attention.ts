import { defineStore } from 'pinia'
import { ref } from 'vue'
import { api } from '../api'
import { useAuth } from './auth'
import { createManagedPoller } from '../utils/schedule'

/** 导航角标：等待当前用户审批的任务数（GET /v1/tasks?status=awaiting_review 的 total，不新增接口）。 */
export const useAttention = defineStore('attention', () => {
  const reviewTasks = ref(0)
  const auth = useAuth()
  async function fetchCount() { reviewTasks.value = (await api.listTasks({ status: 'awaiting_review', page: 1, page_size: 1 })).total }
  /** 手动刷新（换页时）：失败时保留上次的数，角标只是提示 */
  async function refresh() {
    if (!auth.me || !auth.setup?.initialized) return
    try { await fetchCount() } catch { /* 保留上次的数 */ }
  }
  // 30 s 周期：仅在有会话且系统已初始化时运行；标签页隐藏时暂停；失败（503/网络/…）时指数退避
  const poller = createManagedPoller({
    intervalMs: 30_000, maxIntervalMs: 300_000,
    enabled: () => !!auth.me && !!auth.setup?.initialized,
    run: fetchCount,
  })
  function start() { stop(); void refresh(); poller.start() }
  function stop() { poller.stop() }
  function reset() { stop(); reviewTasks.value = 0 }
  return { reviewTasks, refresh, start, stop, reset }
})
