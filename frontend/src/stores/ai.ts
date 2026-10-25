import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { api, type LlmStatus } from '../api'
import { statusView } from '../utils/aiStatus'
import { errMsg } from '../utils/errorInfo'
import type { LlmErrorView } from '../utils/llmError'

/** 某个任务因大模型调用失败而停下（页面读到失败文件时登记，重试成功或任务不再失败时清除）。 */
export interface LlmTaskFailureState { taskId: string; view: LlmErrorView; count: number; since: string }

/** 当前用户的大模型状态（GET /v1/llm/status）。设置页保存/策略变更后调用 refresh() 同步。 */
export const useAi = defineStore('ai', () => {
  const status = ref<LlmStatus | null>(null)
  const loading = ref(false)
  const error = ref<string | null>(null)
  const failure = ref<LlmTaskFailureState | null>(null)
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
  function reportFailure(f: LlmTaskFailureState) { failure.value = f }
  function clearFailure(taskId?: string) { if (!taskId || failure.value?.taskId === taskId) failure.value = null }
  function reset() { status.value = null; error.value = null; failure.value = null; seq++ }
  return { status, loading, error, failure, view, refresh, reportFailure, clearFailure, reset }
})
