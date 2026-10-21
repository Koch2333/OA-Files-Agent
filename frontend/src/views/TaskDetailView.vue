<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { api, type AgentEvent, type FileRecord } from '../api'
import { errMsg, useAsync } from '../utils/useAsync'
import { ACTIVE, FLOW, flowIndex, fmtTime, shortId } from '../utils/format'
import { useUi } from '../stores/ui'
import PageHeader from '../components/PageHeader.vue'
import StatusChip from '../components/StatusChip.vue'
import StateView from '../components/StateView.vue'
import Icon from '../components/Icon.vue'
import Drawer from '../components/Drawer.vue'
import FileDetail from '../components/FileDetail.vue'
import CorrectionDrawer from '../components/CorrectionDrawer.vue'

const props = defineProps<{ id: string }>()
const ui = useUi()
const task = useAsync(() => api.getTask(props.id))
const files = useAsync(() => api.listTaskFiles(props.id))
const events = ref<AgentEvent[]>([])
const stream = ref<'open' | 'reconnecting' | 'closed'>('closed')
const sel = ref<FileRecord | null>(null)
const correcting = ref(false)
const busy = ref(false)
const logEl = ref<HTMLElement>()
let stop: (() => void) | null = null

const idx = computed(() => (task.data.value ? flowIndex(task.data.value.status) : -1))
const terminalFail = computed(() => ['failed', 'partial_failure', 'cancelled'].includes(task.data.value?.status ?? ''))

function onEvent(e: AgentEvent) {
  if (events.value.some((x) => x.id === e.id)) return
  events.value.push(e)
  void task.run()
  if (e.file_id || ['plan_ready', 'archive_completed'].includes(e.event_type)) void files.run()
  void nextTick(() => logEl.value?.scrollTo({ top: logEl.value.scrollHeight }))
}
function connect() { stop?.(); stop = api.subscribeEvents(props.id, onEvent, (s) => (stream.value = s)) }
onMounted(async () => { await task.run(); void files.run(); connect() }) // 刷新页面后由 Last-Event-ID 之外的完整回放恢复状态
onUnmounted(() => stop?.())
watch(() => task.data.value?.status, (s) => { if (s && !ACTIVE.includes(s) && s !== 'awaiting_review') void files.run() })

async function act(kind: 'cancel' | 'retry') {
  busy.value = true
  try { await (kind === 'cancel' ? api.cancelTask(props.id) : api.retryTask(props.id)); ui.toast(kind === 'cancel' ? '已请求取消' : '已重新排队', 'success'); await task.run(); if (kind === 'retry') connect() }
  catch (e) { ui.toast(errMsg(e), 'error') } finally { busy.value = false }
}
const evIcon = (t: string) => ({ tool_call: 'build', file_classified: 'label', file_failed: 'error', plan_ready: 'fact_check', phase_started: 'play_arrow', archive_completed: 'inventory_2', task_cancelled: 'block' } as Record<string, string>)[t] ?? 'circle'
</script>

<template>
  <PageHeader :title="`任务 ${shortId(id)}`" subtitle="Agent 处理过程与识别结果（事件流实时更新）">
    <template #actions>
      <StatusChip v-if="task.data.value" :status="task.data.value.status" />
      <button v-if="task.data.value && ACTIVE.includes(task.data.value.status)" class="btn outlined has-icon" :disabled="busy" @click="act('cancel')"><Icon name="stop_circle" sm />取消任务</button>
      <button v-if="terminalFail" class="btn outlined has-icon" :disabled="busy || !api.capabilities.retryTask" :title="api.capabilities.retryTask ? '' : '后端契约暂未提供重试接口'" @click="act('retry')"><Icon name="replay" sm />重试</button>
      <RouterLink v-if="task.data.value?.plan_id && ['awaiting_review', 'approved', 'completed', 'partial_failure'].includes(task.data.value.status)" :to="`/plans/${task.data.value.plan_id}`" class="btn filled has-icon"><Icon name="fact_check" sm />{{ task.data.value.status === 'awaiting_review' ? '预览并审批归档计划' : '查看归档计划' }}</RouterLink>
    </template>
  </PageHeader>

  <StateView :loading="task.loading.value && !task.data.value" :error="task.error.value" :empty="false" @retry="task.run">
    <template v-if="task.data.value">
      <section class="card steps" aria-label="处理阶段">
        <ol class="stepper">
          <li v-for="(f, i) in FLOW" :key="f.key" :class="{ done: idx > i, cur: Math.floor(idx) === i && !terminalFail && idx < FLOW.length, fail: terminalFail && Math.floor(idx) === i }">
            <span class="dot"><Icon :name="idx > i ? 'check' : 'circle'" sm :fill="idx <= i" /></span><span>{{ f.label }}</span>
          </li>
        </ol>
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
                    <td class="ellipsis" style="max-width: 260px" :title="f.source_path">{{ f.source_name }}</td>
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

        <section class="card flat">
          <div class="row hd"><h2 class="t-title-m grow">Agent 轨迹</h2><span class="chip" :class="stream === 'open' ? 'success' : stream === 'reconnecting' ? 'attention' : ''">{{ { open: '实时', reconnecting: '重连中', closed: '已断开' }[stream] }}</span></div>
          <div ref="logEl" class="log" role="log" aria-live="off">
            <p v-if="!events.length" class="muted" style="padding: 16px">暂无事件。</p>
            <div v-for="e in events" :key="e.id" class="ev" :class="e.event_type"><Icon :name="evIcon(e.event_type)" sm /><div class="grow"><div>{{ e.summary }}</div><div class="muted t-label">{{ fmtTime(e.timestamp) }} · {{ e.phase }}</div></div></div>
          </div>
        </section>
      </div>
    </template>
  </StateView>

  <Drawer v-if="sel" title="文件详情" @close="sel = null">
    <FileDetail :file="sel" />
    <button class="btn tonal has-icon" @click="correcting = true"><Icon name="auto_fix" sm />纠错</button>
  </Drawer>
  <CorrectionDrawer v-if="sel && correcting" :file="sel" @close="correcting = false" @applied="files.run(); sel = null" />
</template>

<style scoped>
.steps { display: flex; flex-direction: column; gap: 14px; margin-bottom: 16px; }
.stepper { display: flex; list-style: none; margin: 0; padding: 0; gap: 4px; overflow-x: auto; }
.stepper li { flex: 1; display: flex; align-items: center; gap: 8px; color: var(--md-sys-color-on-surface-variant); min-width: 88px; white-space: nowrap; }
.stepper li::after { content: ''; flex: 1; height: 2px; background: var(--md-sys-color-outline-variant); } .stepper li:last-child::after { display: none; }
.dot { width: 24px; height: 24px; border-radius: 50%; display: grid; place-items: center; background: var(--c-surface-3); flex: none; }
.stepper .done { color: var(--md-sys-color-on-surface); } .stepper .done .dot { background: var(--md-sys-color-primary); color: var(--md-sys-color-on-primary); } .stepper .done::after { background: var(--md-sys-color-primary); }
.stepper .cur { color: var(--md-sys-color-primary); font-weight: 600; } .stepper .cur .dot { background: var(--md-sys-color-primary-container); color: var(--md-sys-color-primary); box-shadow: 0 0 0 4px color-mix(in srgb, var(--md-sys-color-primary) 20%, transparent); }
.stepper .fail { color: var(--md-sys-color-error); } .stepper .fail .dot { background: var(--md-sys-color-error-container); }
.split { display: grid; grid-template-columns: minmax(0, 3fr) minmax(300px, 2fr); gap: 16px; align-items: start; }
.hd { padding: 14px 20px; border-bottom: 1px solid var(--md-sys-color-outline-variant); }
.log { max-height: 520px; overflow: auto; padding: 8px; } .ev { display: flex; gap: 10px; padding: 8px 10px; border-radius: var(--shape-sm); } .ev .icon { margin-top: 2px; color: var(--md-sys-color-primary); }
.ev.file_failed .icon, .ev.task_cancelled .icon { color: var(--md-sys-color-error); }
@media (max-width: 1100px) { .split { grid-template-columns: 1fr; } }
</style>
