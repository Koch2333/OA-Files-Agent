import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { api, type LlmStatus } from '../api'
import { statusView } from '../utils/aiStatus'
import { errMsg } from '../utils/errorInfo'

/** 当前用户的大模型状态（GET /v1/llm/status）。设置页保存/策略变更后调用 refresh() 同步。 */
export const useAi = defineStore('ai', () => {
  const status = ref<LlmStatus | null>(null)
  const loading = ref(false)
  const error = ref<string | null>(null)
  let seq = 0

  const view = computed(() => statusView(status.value))

  async function refresh(): Promise<LlmStatus | null> {
    const my = ++seq
    loading.value = true; error.value = null
    try {
      const s = await api.getLlmStatus()
      if (my === seq) status.value = s
    } catch (e) {
      if (my === seq) error.value = errMsg(e)
    } finally { if (my === seq) loading.value = false }
    return status.value
  }
  function reset() { status.value = null; error.value = null; seq++ }
  return { status, loading, error, view, refresh, reset }
})
