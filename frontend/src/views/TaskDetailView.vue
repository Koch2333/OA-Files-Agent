<script setup lang="ts">
import { useAuth } from '../stores/auth'
import { computed, onMounted, ref, watch } from 'vue'
import { api, type AgentEvent, type FileRecord } from '../api'
import { autoBannerText, llmLabel, modeBadge } from '../utils/autoSummary'
import { errMsg, useAsync } from '../utils/useAsync'
import { ACTIVE, shortId } from '../utils/format'
import { useUi } from '../stores/ui'
import PageHeader from '../components/PageHeader.vue'
import StatusChip from '../components/StatusChip.vue'
import AgentProgress from '../components/AgentProgress.vue'
import StateView from '../components/StateView.vue'
import Icon from '../components/Icon.vue'
import Drawer from '../components/Drawer.vue'
import FileDetail from '../components/FileDetail.vue'
import AgentTrace from '../components/AgentTrace.vue'
import CorrectionDrawer from '../components/CorrectionDrawer.vue'
import FilePreview from '../components/FilePreview.vue'
import { isPoorOcr } from '../utils/ocr'

const auth = useAuth()
const props = defineProps<{ id: string }>()
const ui = useUi()
const task = useAsync(() => api.getTask(props.id))
const files = useAsync(() => api.listTaskFiles(props.id))
const trace = ref<InstanceType<typeof AgentTrace>>()
const sel = ref<FileRecord | null>(null)
const correcting = ref(false)
const previewing = ref<FileRecord | null>(null)
const busy = ref(false)

const terminalFail = computed(() => ['failed', 'partial_failure', 'cancelled'].includes(task.data.value?.status ?? ''))

function onEvent(e: AgentEvent) {
  void task.run()
  if (e.file_id || ['plan_ready', 'archive_completed'].includes(e.event_type)) void files.run()
}
onMounted(async () => { await task.run(); void files.run() }) // 刷新页面后由 Last-Event-ID 之外的完整回放恢复状态
const autoBanner = computed(() => (task.data.value ? autoBannerText(task.data.value) : null))
watch(() => task.data.value?.status, (s) => { if (s && !ACTIVE.includes(s) && s !== 'awaiting_review') void files.run() })

async function act(kind: 'cancel' | 'retry') {
  busy.value = true
  try { await (kind === 'cancel' ? api.cancelTask(props.id) : api.retryTask(props.id)); ui.toast(kind === 'cancel' ? '已请求取消' : '已重新排队', 'success'); await task.run(); if (kind === 'retry') trace.value?.connect() }
  catch (e) { ui.toast(errMsg(e), 'error') } finally { busy.value = false }
}
</script>

<template>
  <PageHeader :title="`任务 ${shortId(id)}`" subtitle="Agent 处理过程与识别结果（事件流实时更新）">
    <template #actions>
      <StatusChip v-if="task.data.value" :status="task.data.value.status" />
      <button v-if="auth.canWrite && task.data.value && ACTIVE.includes(task.data.value.status)" class="btn outlined has-icon" :disabled="busy" @click="act('cancel')"><Icon name="stop_circle" sm />取消任务</button>
      <button v-if="auth.canWrite && terminalFail" class="btn outlined has-icon" :disabled="busy || !api.capabilities.retryTask" :title="api.capabilities.retryTask ? '' : '后端契约暂未提供重试接口'" @click="act('retry')"><Icon name="replay" sm />重试</button>
      <RouterLink v-if="task.data.value?.plan_id && ['awaiting_review', 'approved', 'completed', 'partial_failure'].includes(task.data.value.status)" :to="`/plans/${task.data.value.plan_id}`" class="btn filled has-icon"><Icon name="fact_check" sm />{{ task.data.value.status === 'awaiting_review' ? '预览并审批归档计划' : '查看归档计划' }}</RouterLink>
    </template>
  </PageHeader>

  <StateView :loading="task.loading.value && !task.data.value" :error="task.error.value" :empty="false" @retry="task.run">
    <template v-if="task.data.value">
      <div v-if="autoBanner" class="banner autob" role="status" data-testid="auto-banner"><Icon name="bolt" /><div><b>{{ autoBanner }}</b><div class="t-label">无法确认的文件已留待人工处理；已归档的内容可在计划页「撤销本次归档」。</div></div></div>
      <section v-if="task.data.value.instruction || task.data.value.llm_source" class="card meta row wrap" data-testid="task-ai-meta">
        <span class="chip" :class="{ attention: modeBadge(task.data.value).tone === 'attention' }">{{ modeBadge(task.data.value).label }}</span>
        <span class="chip"><Icon name="smart_toy" sm />{{ llmLabel(task.data.value) }}</span>
        <span v-if="task.data.value.instruction" class="grow instr"><span class="muted">给 AI 的说明：</span>{{ task.data.value.instruction }}</span>
      </section>
      <section class="card steps" aria-label="处理阶段">
        <AgentProgress :task="task.data.value" />
        <div class="progressbar" :class="{ indet: ACTIVE.includes(task.data.value.status) && !task.data.value.processed_files }"><i :style="{ width: (task.data.value.total_files ? (task.data.value.processed_files / task.data.value.total_files) * 100 : 0) + '%' }" /></div>
        <div class="row muted"><span>已处理 {{ task.data.value.processed_files }} / {{ task.data.value.total_files }}</span><span>成功 {{ task.data.value.success_count }}</span><span :style="{ color: task.data.value.failure_count ? 'var(--md-sys-color-error)' : undefined }">失败 {{ task.data.value.failure_count }}</span></div>
      </section>

      <div class="split">
        <section class="card flat">
          <div class="row hd"><h2 class="t-title-m grow">识别结果</h2></div>
          <StateView :loading="files.loading.value && !files.data.value" :error="files.error.value" :empty="!files.data.value?.length" empty-title="尚无文件记录" empty-icon="description" empty-hint="文件解析完成后会显示在这里。" @retry="files.run">
            <div class="table-wrap">
              <table class="tbl">
                <thead><tr><th>文件</th><th>部门</th><th>类型</th><th>日期</th><th>状态</th></tr></thead>
                <tbody>
                  <tr v-for="f in files.data.value" :key="f.id" class="clickable" :class="{ selected: sel?.id === f.id }" tabindex="0" @click="sel = f" @keydown.enter="sel = f">
                    <td style="max-width: 320px" :title="f.source_path"><div class="row" style="gap: 6px"><span class="ellipsis">{{ f.source_name }}</span><span v-if="isPoorOcr(f)" class="chip error" title="OCR 识别质量较差">OCR 差</span></div></td>
                    <td><span v-if="f.detected_department">{{ f.detected_department }}</span><span v-else class="chip attention">待审核</span></td>
                    <td><span v-if="f.detected_type">{{ f.detected_type }}</span><span v-else class="chip attention">待审核</span></td>
                    <td class="nowrap"><span v-if="f.detected_date">{{ f.detected_date }}</span><span v-else class="chip attention">待审核</span></td>
                    <td><span v-if="f.error" class="chip error" :title="f.error"><Icon name="error" sm />解析失败</span><span v-else-if="f.review_required" class="chip attention">需确认</span><span v-else class="chip success">已识别</span></td>
                  </tr>
                </tbody>
              </table>
            </div>
          </StateView>
        </section>

        <section class="card flat"><AgentTrace ref="trace" :task-id="id" @event="onEvent" /></section>
      </div>
    </template>
  </StateView>

  <Drawer v-if="sel" title="文件详情" @close="sel = null">
    <FileDetail :file="sel" :decision="task.data.value?.automation === 'auto' ? 'ai_auto' : 'manual'" :decision-model="task.data.value?.llm_model" @preview="previewing = sel" />
    <button v-if="auth.canWrite" class="btn tonal has-icon" @click="correcting = true"><Icon name="auto_fix" sm />纠错</button>
  </Drawer>
  <FilePreview v-if="previewing" :source="{ kind: 'file', id: previewing.id }" :name="previewing.source_name" @close="previewing = null" />
  <CorrectionDrawer v-if="sel && correcting" :file="sel" @close="correcting = false" @applied="files.run(); sel = null" />
</template>

<style scoped>
.steps { display: flex; flex-direction: column; gap: 14px; margin-bottom: 16px; }
.split { display: grid; grid-template-columns: minmax(0, 3fr) minmax(300px, 2fr); gap: 16px; align-items: start; }
.hd { padding: 14px 20px; border-bottom: 1px solid var(--md-sys-color-outline-variant); }
.autob { margin-bottom: 16px; background: var(--c-warn-container); color: var(--c-on-warn-container); border: 1px solid var(--c-warn); }
.meta { margin-bottom: 16px; padding: 12px 16px; } .instr { word-break: break-word; }
@media (max-width: 1100px) { .split { grid-template-columns: 1fr; } }
</style>
