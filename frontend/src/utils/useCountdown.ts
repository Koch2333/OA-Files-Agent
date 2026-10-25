import { computed, onUnmounted, ref, watch, type Ref } from 'vue'

/** 倒计时（秒）：source 变化时从新值重新开始；到 0 停止。用于 429 Retry-After 时禁用“重试”。 */
export function useCountdown(source: Ref<number> | (() => number)) {
  const left = ref(0)
  let timer: ReturnType<typeof setInterval> | null = null
  const stop = () => { if (timer) { clearInterval(timer); timer = null } }
  function start(n: number) {
    stop()
    left.value = Math.max(0, Math.ceil(n))
    if (left.value <= 0) return
    timer = setInterval(() => { left.value = Math.max(0, left.value - 1); if (left.value <= 0) stop() }, 1000)
  }
  const get = typeof source === 'function' ? source : () => source.value
  watch(get, (n) => start(n), { immediate: true })
  onUnmounted(stop)
  return { left, waiting: computed(() => left.value > 0), restart: start }
}
