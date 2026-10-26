<script setup lang="ts">
import { uuid } from '../utils/uuid'
import { computed, onMounted, onUnmounted, ref, toRef, watch } from 'vue'
import { api, ApiError, type PickedFile, type StorageConnection } from '../api'
import { useAuth } from '../stores/auth'
import { useAi } from '../stores/ai'
import { useUi } from '../stores/ui'
import { errMsg, useAsync } from '../utils/useAsync'
import { ACTIVE } from '../utils/format'
import { summarize } from '../utils/importLimits'
import { useAutomation } from '../utils/automation'
import { useImportUploader } from '../utils/importUpload'
import UploadProgress from '../components/UploadProgress.vue'
import PageHeader from '../components/PageHeader.vue'
import StateView from '../components/StateView.vue'
import Icon from '../components/Icon.vue'
import FilePicker from '../components/FilePicker.vue'
import ErrorBanner from '../components/ErrorBanner.vue'
import LlmStatusBanner from '../components/LlmStatusBanner.vue'
import AutomationSelector from '../components/AutomationSelector.vue'
import AutoRiskDialog from '../components/AutoRiskDialog.vue'
import AiTaskCard from '../components/AiTaskCard.vue'
import RemoteBrowserDialog from '../components/RemoteBrowserDialog.vue'

const MAX_INSTR = 2000
const EXAMPLES = ['只处理采购相关文件，按项目名归档', '把扫描件单独标记待审核', '合同类文件按“年份/合同类型”归档', '无法确定部门的文件不要自动归档，留给我审核']

const auth = useAuth()
const ai = useAi()
const ui = useUi()
const instruction = ref('')
const picked = ref<PickedFile[]>([])
const templateId = ref('')
const submitting = ref(false)
const uploader = useImportUploader()
const err = ref<string | null>(null)
const lastId = ref<string | null>(null)
const tpls = useAsync(() => api.listTemplates())
const tasks = useAsync(() => api.listTasks({ page: 1, page_size: 10 }))
const auto = useAutomation(toRef(ai, 'status'), toRef(auth, 'role'))
watch(() => ai.status, () => auto.revalidate())

const ready = computed(() => !!ai.status?.configured)
const summary = computed(() => summarize(picked.value))
const canSubmit = computed(() => ready.value && picked.value.length > 0 && !submitting.value && instruction.value.length <= MAX_INSTR)

const remoteConns = ref<StorageConnection[]>([])
const remoteOpen = ref(false)
const remoteBusy = ref(false)
const remoteErr = ref<string | null>(null)
const importable = computed(() => remoteConns.value.filter((c) => c.enabled && c.use_for_import))

let timer = 0
onMounted(() => {
  void ai.refresh(); void tpls.run(); void tasks.run()
  if (auth.isAdmin) api.listStorageConnections().then((l) => { remoteConns.value = l }, () => { remoteConns.value = [] })
  timer = window.setInterval(() => { if (tasks.data.value?.items.some((t) => ACTIVE.includes(t.status))) void tasks.run() }, 3000)
})
onUnmounted(() => clearInterval(timer))

function addExample(t: string) {
  const cur = instruction.value.trim()
  const next = cur ? `${cur}\n${t}` : t
  instruction.value = next.slice(0, MAX_INSTR)
}
async function afterError(e: unknown) {
  if (e instanceof ApiError && ['AUTO_ARCHIVE_DISABLED', 'LLM_REQUIRED', 'AUTO_ACK_REQUIRED'].includes(e.code)) { await ai.refresh(); auto.revalidate() }
}
function done(id: string) {
  ui.toast(auto.mode.value === 'auto' ? '已提交，全自动任务开始处理' : '已提交，处理完成后请审批归档计划', 'success')
  picked.value = []; instruction.value = ''; lastId.value = id
  void tasks.run()
}
// 失败后点“重试”复用同一个幂等键：即使上一次请求其实已到达服务器，也不会创建重复任务；改动文件或说明后换新键。
let submitKey: string | null = null
watch([picked, instruction, templateId], () => { submitKey = null })
async function submit() {
  if (!canSubmit.value) return
  submitting.value = true; err.value = null
  try {
    submitKey ??= uuid()
    const job = await uploader.start(picked.value, { templateId: templateId.value || null, idempotencyKey: submitKey, options: auto.options(instruction.value) })
    submitKey = null
    done(job.id)
  } catch (e) { err.value = errMsg(e); await afterError(e) } finally { submitting.value = false }
}
async function importRemote(p: { connectionId: string; paths: string[] }) {
  remoteBusy.value = true; remoteErr.value = null
  try {
    const job = await api.createRemoteImport(p.connectionId, p.paths, { templateId: templateId.value || null, idempotencyKey: uuid(), options: auto.options(instruction.value) })
    remoteOpen.value = false; done(job.id)
  } catch (e) { remoteErr.value = errMsg(e); await afterError(e) } finally { remoteBusy.value = false }
}
</script>

<template>
  <PageHeader title="AI 工作台" subtitle="给 AI 布置归档任务：写下要求、选好文件，由大模型识别、命名并整理。这里不是聊天，每次提交就是一个可审计的任务。" />
  <LlmStatusBanner />

  <div class="layout" :class="{ off: !ready }">
    <section class="card col composer" aria-label="新建任务" data-testid="ai-composer">
      <h2 class="t-title-m">新任务</h2>
      <fieldset class="col fs" :disabled="!ready || submitting">
        <legend class="sr-only">任务内容</legend>
        <div class="field">
          <label for="ai-instr">给 AI 的任务说明</label>
          <textarea id="ai-instr" v-model="instruction" class="input" rows="5" :maxlength="MAX_INSTR" placeholder="例如：只处理采购相关文件，按项目名归档；扫描件一律标记为待审核" data-testid="ai-instruction" />
          <div class="row between hint"><span class="muted">说明只用于指导分类与命名，不能授予额外权限，也不会改变路径安全规则。</span><span class="muted mono" :class="{ over: instruction.length >= MAX_INSTR }" data-testid="instr-count">{{ instruction.length }} / {{ MAX_INSTR }}</span></div>
          <div class="row wrap" role="group" aria-label="示例说明"><button v-for="e in EXAMPLES" :key="e" type="button" class="ex" data-testid="ai-example" @click="addExample(e)"><Icon name="add" sm />{{ e }}</button></div>
        </div>

        <FilePicker v-model="picked" compact :disabled="!ready">
          <template #actions>
            <button v-if="importable.length" type="button" class="btn outlined has-icon" data-testid="remote-open" :disabled="!ready" @click="remoteErr = null; remoteOpen = true"><Icon name="cloud_sync" sm />从存储连接选取</button>
          </template>
        </FilePicker>

        <div class="grid2">
          <div class="field">
            <label for="ai-tpl">OA 模板</label>
            <select id="ai-tpl" v-model="templateId" class="input">
              <option value="">自动匹配（按优先级）</option>
              <option v-for="t in tpls.data.value?.filter((x) => x.enabled)" :key="t.id" :value="t.id">{{ t.name }}</option>
            </select>
          </div>
          <AutomationSelector :mode="auto.mode.value" :gate="auto.gate.value" :disabled="!ready" @select="auto.select" />
        </div>
      </fieldset>

      <UploadProgress v-if="submitting" :progress="uploader.progress.value" @cancel="uploader.cancel()" />
      <ErrorBanner :message="err" retry :busy="submitting" @retry="submit" />
      <div class="row wrap">
        <button class="btn filled has-icon" :disabled="!canSubmit" data-testid="ai-submit" @click="submit"><Icon :name="submitting ? 'progress_activity' : 'send'" :class="{ spin: submitting }" sm />{{ submitting ? '正在提交…' : auto.mode.value === 'auto' ? `提交并全自动归档（${picked.length}）` : `提交任务（${picked.length}）` }}</button>
        <span v-if="!ready" class="muted">请先配置大模型。</span>
        <span v-else-if="!picked.length" class="muted">请选择要处理的文件或文件夹。</span>
      </div>
    </section>

    <section class="col thread" aria-label="最近的 AI 任务" data-testid="ai-task-list">
      <div class="row"><h2 class="t-title-m grow">最近任务</h2><button class="btn icon-only sm" aria-label="刷新" @click="tasks.run"><Icon name="refresh" sm /></button></div>
      <StateView :loading="tasks.loading.value && !tasks.data.value" :error="tasks.error.value" :empty="!tasks.data.value?.items.length" empty-title="还没有任务" empty-icon="smart_toy" empty-hint="在左侧写下要求并选择文件，提交后任务会出现在这里。" @retry="tasks.run">
        <AiTaskCard v-for="t in tasks.data.value?.items" :key="t.id" :task="t" :expanded="t.id === lastId" @changed="tasks.run" />
      </StateView>
    </section>
  </div>

  <RemoteBrowserDialog v-if="remoteOpen" :connections="importable" :busy="remoteBusy" :error="remoteErr" @close="remoteOpen = false" @import="importRemote" />
  <AutoRiskDialog v-if="auto.dialogOpen.value && ai.status" v-model:checked="auto.checked.value" :status="ai.status" :remaining-sec="auto.remainingSec.value" :can-confirm="auto.canConfirm.value" @confirm="auto.confirm" @cancel="auto.cancel" />
</template>

<style scoped>
.layout { display: grid; grid-template-columns: minmax(0, 5fr) minmax(0, 4fr); gap: 20px; align-items: start; }
.layout.off .composer { opacity: .55; }
.fs { border: 0; padding: 0; margin: 0; min-width: 0; }
.grid2 { display: grid; grid-template-columns: 1fr 1fr; gap: 16px; align-items: start; }
.hint { font-size: 12px; align-items: flex-start; } .over { color: var(--md-sys-color-error); }
.ex { display: inline-flex; align-items: center; gap: 4px; height: 30px; padding: 0 12px; border: 1px solid var(--md-sys-color-outline-variant); border-radius: var(--shape-full); background: transparent; color: var(--md-sys-color-on-surface-variant); font: 500 12px var(--font); cursor: pointer; text-align: left; }
.ex:hover:not(:disabled) { background: var(--md-sys-color-secondary-container); color: var(--md-sys-color-on-secondary-container); } .ex:disabled { opacity: .5; cursor: not-allowed; }
.thread { min-width: 0; }
@media (max-width: 1100px) { .layout { grid-template-columns: 1fr; } }
@media (max-width: 800px) { .grid2 { grid-template-columns: 1fr; } }
</style>
