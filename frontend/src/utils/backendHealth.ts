import { ref } from 'vue'
import { API_BASE, setNetworkHook } from '../api/http'
import { createManagedPoller, type Poller } from './schedule'

export interface HealthMonitorOpts {
  probe: () => Promise<boolean>
  onChange: (down: boolean, reason: string) => void
  intervalMs?: number
  maxIntervalMs?: number
}

/**
 * 后端可达性监视：一旦标记为不可用，每 intervalMs 探测一次（连续失败时指数退避，最长 maxIntervalMs），
 * 标签页隐藏时暂停，恢复后自动清除。后端可达且没有标记时完全不发请求。
 */
export function createHealthMonitor(o: HealthMonitorOpts) {
  const every = o.intervalMs ?? 5000
  let down = false
  const poller: Poller = createManagedPoller({
    intervalMs: every,
    maxIntervalMs: o.maxIntervalMs ?? 60_000,
    run: async () => {
      if (!(await o.probe().catch(() => false))) throw new Error('backend still down')
      down = false
      poller.stop()
      o.onChange(false, '')
    },
  })
  return {
    markDown(reason = 'network') {
      if (down) return
      down = true
      o.onChange(true, reason)
      poller.start()
    },
    /** 立即探测一次（例如浏览器恢复联网事件） */
    checkNow() { if (down) poller.kick(); return Promise.resolve() },
    isDown: () => down,
    stop() { poller.stop(); down = false },
  }
}
export const backendDown = ref(false)
export const backendReason = ref('')

let monitor: ReturnType<typeof createHealthMonitor> | null = null
/** 安装：网络层失败 → 显示横幅并每 5 秒探测 /healthz。演示模式不会产生网络失败，因此不会触发。 */
export function installBackendHealth() {
  if (monitor) return
  monitor = createHealthMonitor({
    probe: async () => { const r = await fetch(API_BASE + '/healthz', { cache: 'no-store', credentials: 'same-origin' }); return r.ok },
    onChange: (d, reason) => { backendDown.value = d; backendReason.value = reason },
  })
  setNetworkHook(() => monitor?.markDown('network'))
  window.addEventListener('offline', () => monitor?.markDown('offline'))
  window.addEventListener('online', () => { void monitor?.checkNow() })
}
