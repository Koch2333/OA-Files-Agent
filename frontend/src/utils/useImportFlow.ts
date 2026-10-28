import { computed, ref, toRef } from 'vue'
import { useRouter } from 'vue-router'
import { api, ApiError, type PathImportStatus, type PickedFile } from '../api'
import { useAuth } from '../stores/auth'
import { useAi } from '../stores/ai'
import { useUi } from '../stores/ui'
import { useAutomation } from './automation'
import { errMsg } from './errorInfo'
import { fromInput } from './paths'
import { ingestDrop } from './ingestDrop'
import { mergePicked, summarize } from './importLimits'
import { pickBrief } from './importFlow'
import { importLocalPaths, useImportUploader } from './importUpload'
import { desktop } from './desktop'

/**
 * 导入流程的共享状态与提交逻辑：批量导入页（ImportView）与工作台拖入区（ImportDropZone）都用它，
 * 因此“选择 → 校验 → 运行方式/说明 → 上传（可续传分片 / 桌面本机路径）→ 提交批次 → 跳转任务页”只有一份实现。
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
  const found = ref(0)
  const err = ref<string | null>(null)
  const uploader = useImportUploader()
  const auto = useAutomation(toRef(ai, 'status'), toRef(auth, 'role'))
  const summary = computed(() => summarize(picked.value))
  const brief = computed(() => pickBrief(picked.value, summary.value))
  const llmOk = computed(() => !!ai.status?.configured)

  function add(list: PickedFile[]) { picked.value = mergePicked(picked.value, list) }
  function remove(p: PickedFile) { picked.value = picked.value.filter((x) => x !== p) }
  function clear() { picked.value = []; err.value = null }
  function addFromInput(files: FileList | null) { if (files) add(fromInput(files)) }
  /** 投放入口：读取（含文件夹）→ 合并到待导入列表 → toast 提示结果。与上传机制无关，上传器之后从 picked 取 File。 */
  function addFromDrop(dt: DataTransfer) { return ingestDrop(dt, { add, notify: (n) => ui.toast(n.text, n.kind), scanning, found, err }) }

  /** 仅在已配置大模型时才携带“说明”；全自动携带风险确认。 */
  function currentOptions() { return auto.options(llmOk.value ? instruction.value : '') }
  async function afterError(e: unknown) {
    if (e instanceof ApiError && ['AUTO_ARCHIVE_DISABLED', 'LLM_REQUIRED', 'AUTO_ACK_REQUIRED'].includes(e.code)) { await ai.refresh(); auto.revalidate() }
  }
  async function submit() {
    if (!picked.value.length || submitting.value) return
    submitting.value = true; err.value = null
    // 上传期间还可以继续添加：已交给上传器的文件记在 sent 里，drain 取出之后新加入的，并入同一个批次
    const sent = new Set<PickedFile>(picked.value)
    const drain = () => { const fresh = picked.value.filter((p) => !sent.has(p)); fresh.forEach((p) => sent.add(p)); return fresh }
    try {
      const job = await uploader.start(picked.value, { templateId: templateId.value || null, options: currentOptions(), drain })
      uploader.reset()
      const late = picked.value.filter((p) => !sent.has(p))
      if (late.length) {
        // 一次请求就传完的小批次来不及并入：这些文件留在待提交列表里，不丢
        picked.value = late
        ui.toast(`已创建归档任务；上传期间追加的 ${late.length} 个文件还没有提交，仍在列表中`, 'info', { label: '查看任务', run: () => { void router.push(`/tasks/${job.id}`) } })
        return
      }
      ui.toast('已创建归档任务', 'success')
      await router.push(`/tasks/${job.id}`)
    } catch (e) { err.value = errMsg(e); await afterError(e) } finally { submitting.value = false }
  }
  /** 取消正在进行的上传（已上传的临时数据会被删除）。 */
  async function cancelUpload() { await uploader.cancel(); uploader.reset(); submitting.value = false }
  /** 桌面版（local 模式）：原生“选择文件/文件夹”，后端直接按路径复制，不经过 HTTP 上传。 */
  const nativePick = ref(false)
  const localProgress = ref<PathImportStatus | null>(null)
  try {
    const d = desktop()
    if (d?.pickPaths) void api.getImportConfig().then((c) => { nativePick.value = !!c.local_path_import }, () => { nativePick.value = false })
  } catch { nativePick.value = false }
  async function pickNative(folders: boolean) {
    const d = desktop()
    if (!d?.pickPaths || submitting.value) return
    const paths = await d.pickPaths({ folders })
    if (!paths.length) return
    submitting.value = true; err.value = null
    try {
      const id = await importLocalPaths(paths, { templateId: templateId.value || null, options: currentOptions() }, (p) => { localProgress.value = p })
      ui.toast('已创建归档任务', 'success')
      await router.push(`/tasks/${id}`)
    } catch (e) { err.value = errMsg(e); await afterError(e) } finally { submitting.value = false; localProgress.value = null }
  }
  return { picked, templateId, instruction, submitting, scanning, found, err, auto, summary, brief, llmOk, add, remove, clear, addFromInput, addFromDrop, currentOptions, afterError, submit, progress: uploader.progress, cancelUpload, nativePick, pickNative, localProgress }
}
