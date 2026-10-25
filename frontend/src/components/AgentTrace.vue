<script setup lang="ts">
import { nextTick, onMounted, onUnmounted, ref } from 'vue'
import { api, type AgentEvent } from '../api'
import { fmtTime } from '../utils/format'
import Icon from './Icon.vue'

/** Agent 事件流（SSE）：按单调递增的事件 id 去重；每个事件向上冒泡，由页面决定刷新哪些数据。 */
const props = defineProps<{ taskId: string; maxHeight?: string }>()
const emit = defineEmits<{ event: [e: AgentEvent] }>()
const events = ref<AgentEvent[]>([])
const stream = ref<'open' | 'reconnecting' | 'closed'>('closed')
const logEl = ref<HTMLElement>()
let stop: (() => void) | null = null

function onEvent(e: AgentEvent) {
  if (events.value.some((x) => x.id === e.id)) return
  events.value.push(e)
  emit('event', e)
  void nextTick(() => logEl.value?.scrollTo({ top: logEl.value.scrollHeight }))
}
function connect() { stop?.(); stop = api.subscribeEvents(props.taskId, onEvent, (s) => (stream.value = s)) }
defineExpose({ connect })
onMounted(connect)
onUnmounted(() => stop?.())
const evIcon = (t: string) => ({ tool_call: 'build', file_classified: 'label', file_failed: 'error', provider_error: 'error', plan_ready: 'fact_check', phase_started: 'play_arrow', archive_completed: 'inventory_2', task_cancelled: 'block' } as Record<string, string>)[t] ?? 'circle'
</script>
<template>
  <div>
    <div class="row hd"><h3 class="t-title-m grow">Agent 轨迹</h3><span class="chip" :class="stream === 'open' ? 'success' : stream === 'reconnecting' ? 'attention' : ''">{{ { open: '实时', reconnecting: '重连中', closed: '已断开' }[stream] }}</span></div>
    <div ref="logEl" class="log" role="log" aria-live="off" :style="{ maxHeight: maxHeight ?? '520px' }">
      <p v-if="!events.length" class="muted" style="padding: 16px">暂无事件。</p>
      <div v-for="e in events" :key="e.id" class="ev" :class="e.event_type"><Icon :name="evIcon(e.event_type)" sm /><div class="grow"><div>{{ e.summary }} <span v-if="e.event_type === 'provider_error' && e.details?.status" class="chip error" data-testid="trace-llm-status">HTTP {{ e.details?.status }}</span><span v-if="e.event_type === 'provider_error' && Number(e.details?.attempts) > 1" class="muted t-label"> · 已尝试 {{ e.details?.attempts }} 次</span></div><div class="muted t-label">{{ fmtTime(e.timestamp) }} · {{ e.phase }}</div></div></div>
    </div>
  </div>
</template>
<style scoped>
.hd { padding: 14px 20px; border-bottom: 1px solid var(--md-sys-color-outline-variant); }
.log { overflow: auto; padding: 8px; } .ev { display: flex; gap: 10px; padding: 8px 10px; border-radius: var(--shape-sm); } .ev .icon { margin-top: 2px; color: var(--md-sys-color-primary); }
.ev.file_failed .icon, .ev.provider_error .icon, .ev.task_cancelled .icon { color: var(--md-sys-color-error); }
</style>
