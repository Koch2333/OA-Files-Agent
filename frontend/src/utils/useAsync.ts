import { onScopeDispose, ref, shallowRef } from 'vue'
import { errMsg } from './errorInfo'

export { errMsg }

/** 统一 loading / error / data 状态；并丢弃过期请求的结果。 */
export function useAsync<T>(fn: () => Promise<T>) {
  const data = shallowRef<T>()
  const error = ref<string | null>(null)
  const loading = ref(false)
  let seq = 0
  let alive = true
  onScopeDispose(() => { alive = false })
  async function run() {
    const my = ++seq
    loading.value = true
    error.value = null
    try {
      const r = await fn()
      if (alive && my === seq) data.value = r
    } catch (e) {
      if (alive && my === seq) error.value = errMsg(e)
    } finally {
      if (alive && my === seq) loading.value = false
    }
  }
  return { data, error, loading, run }
}
