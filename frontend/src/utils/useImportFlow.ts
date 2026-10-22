import { computed, ref, toRef } from 'vue'
import { useRouter } from 'vue-router'
import { api, ApiError, type PickedFile } from '../api'
import { useAuth } from '../stores/auth'
import { useAi } from '../stores/ai'
import { useUi } from '../stores/ui'
import { useAutomation } from './automation'
import { errMsg } from './errorInfo'
import { collectDropped, fromInput } from './paths'
import { mergePicked, summarize } from './importLimits'
import { pickBrief } from './importFlow'

/**
 * 导入流程的共享状态与提交逻辑：批量导入页（ImportView）与工作台拖入区（ImportDropZone）都用它，
 * 因此“选择 → 校验 → 运行方式/说明 → createImport → 跳转任务页”只有一份实现。
 * 绝不自动提交：只有调用方在用户点击后调用 submit()。
 */
export function useImportFlow() {
  const auth = useAuth()
  const ai = useAi()
  const ui = useUi()
  const router = useRouter()
  const picked = ref<PickedFile[]>([])
  const templateId = ref('')
  const instruction = ref('')
  const submitting = ref(false)
  const scanning = ref(false)
  const err = ref<string | null>(null)
  const auto = useAutomation(toRef(ai, 'status'), toRef(auth, 'role'))
  const summary = computed(() => summarize(picked.value))
  const brief = computed(() => pickBrief(picked.value, summary.value))
  const llmOk = computed(() => !!ai.status?.configured)

  function add(list: PickedFile[]) { picked.value = mergePicked(picked.value, list) }
  function remove(p: PickedFile) { picked.value = picked.value.filter((x) => x !== p) }
  function clear() { picked.value = []; err.value = null }
  function addFromInput(files: FileList | null) { if (files) add(fromInput(files)) }
  async function addFromDrop(dt: DataTransfer) {
    scanning.value = true; err.value = null
    try { add(await collectDropped(dt)) } catch (x) { err.value = '读取拖入内容失败：' + errMsg(x) } finally { scanning.value = false }
  }

  /** 仅在已配置大模型时才携带“说明”；全自动携带风险确认。 */
  function currentOptions() { return auto.options(llmOk.value ? instruction.value : '') }
  async function afterError(e: unknown) {
    if (e instanceof ApiError && ['AUTO_ARCHIVE_DISABLED', 'LLM_REQUIRED', 'AUTO_ACK_REQUIRED'].includes(e.code)) { await ai.refresh(); auto.revalidate() }
  }
  async function submit() {
    if (!picked.value.length || summary.value.blocking || submitting.value) return
    submitting.value = true; err.value = null
    try {
      const job = await api.createImport(picked.value, { templateId: templateId.value || null, options: currentOptions() })
      ui.toast('已创建归档任务', 'success')
      await router.push(`/tasks/${job.id}`)
    } catch (e) { err.value = errMsg(e); await afterError(e) } finally { submitting.value = false }
  }
  return { picked, templateId, instruction, submitting, scanning, err, auto, summary, brief, llmOk, add, remove, clear, addFromInput, addFromDrop, currentOptions, afterError, submit }
}
