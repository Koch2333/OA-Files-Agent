<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { api, type ImportJob } from '../api'
import { useAuth } from '../stores/auth'
import { useUi } from '../stores/ui'
import { autoSummary, llmLabel, modeBadge, type AutoSummary } from '../utils/autoSummary'
import { errMsg } from '../utils/errorInfo'
import { ACTIVE, fmtTime, shortId } from '../utils/format'
import StatusChip from './StatusChip.vue'
import AgentProgress from './AgentProgress.vue'
import AgentTrace from './AgentTrace.vue'
import ConfirmDialog from './ConfirmDialog.vue'
import Icon from './Icon.vue'

const props = defineProps<{ task: ImportJob; expanded?: boolean }>()
const emit = defineEmits<{ changed: [] }>()
const auth = useAuth()
const ui = useUi()
const open = ref(!!props.expanded)
const summary = ref<AutoSummary | null>(null)
const confirmUndo = ref(false)
const undoing = ref(false)
const undoErr = ref<string | null>(null)

const badge = computed(() => modeBadge(props.task))
const isAuto = computed(() => props.task.automation === 'auto')
const active = computed(() => ACTIVE.includes(props.task.status))
const pct = computed(() => (props.task.total_files ? Math.round((props.task.processed_files / props.task.total_files) * 100) : 0))
const canUndo = computed(() => auth.canWrite && !!summary.value?.operationId && summary.value.archived > 0 && api.capabilities.rollback)

/** 全自动任务的结果由文件与计划推导（契约没有专门的汇总字段）。 */
async function loadSummary() {
  if (!isAuto.value || active.value) { summary.value = null; return }
  try {
    const [files, plan] = await Promise.all([api.listTaskFiles(props.task.id), props.task.plan_id ? api.getPlan(props.task.plan_id) : Promise.resolve(null)])
    summary.value = autoSummary(files, plan)
  } catch { /* 摘要加载失败不影响卡片；用户可进入任务页查看 */ }
}
onMounted(loadSummary)
watch(() => [props.task.status, props.task.updated_at], loadSummary)

async function undo() {
  if (!summary.value?.operationId) return
  undoing.value = true; undoErr.value = null
  try {
    await api.rollbackOperation(summary.value.operationId)
    confirmUndo.value = false
    ui.toast('已撤销本次全自动归档：文件已移除，条目回到待审核计划', 'success')
    emit('changed'); await loadSummary()
  } catch (e) { undoErr.value = errMsg(e) } finally { undoing.value = false }
}
</script>
<template>
  <article class="card col task" :data-testid="'ai-task-' + task.id" :aria-label="'AI 任务 ' + shortId(task.id)">
    <div class="row wrap hd">
      <StatusChip :status="task.status" />
      <span class="chip" :class="{ attention: badge.tone === 'attention' }" data-testid="mode-badge"><Icon :name="isAuto ? 'bolt' : 'fact_check'" sm />{{ badge.label }}</span>
      <span class="chip" data-testid="llm-badge"><Icon name="smart_toy" sm />{{ llmLabel(task) }}</span>
      <span class="grow" />
      <span class="muted t-label mono">{{ shortId(task.id) }} · {{ fmtTime(task.created_at) }}</span>
    </div>
    <p v-if="task.instruction" class="instr" data-testid="task-instruction">{{ task.instruction }}</p>
    <p v-else class="muted">（未填写任务说明，按默认规则处理）</p>
    <AgentProgress :task="task" compact />
    <div class="progressbar" :class="{ indet: active && !task.processed_files }"><i :style="{ width: pct + '%' }" /></div>
    <div class="row wrap muted t-label"><span>已处理 {{ task.processed_files }} / {{ task.total_files }}</span><span>成功 {{ task.success_count }}</span><span :style="{ color: task.failure_count ? 'var(--md-sys-color-error)' : undefined }">失败 {{ task.failure_count }}</span></div>

    <div v-if="isAuto && summary" class="result row wrap" data-testid="auto-result">
      <span class="chip success"><Icon name="inventory_2" sm />已归档 {{ summary.archived }}</span>
      <span class="chip" :class="summary.review ? 'attention' : ''"><Icon name="rule" sm />留待人工审核 {{ summary.review }}</span>
      <span v-if="summary.failed" class="chip error">失败 {{ summary.failed }}</span>
      <span class="grow" />
      <button v-if="canUndo" class="btn danger outlined sm has-icon" data-testid="auto-undo" @click="undoErr = null; confirmUndo = true"><Icon name="undo" sm />撤销本次全自动归档</button>
    </div>

    <div class="row wrap">
      <RouterLink :to="`/tasks/${task.id}`" class="btn sm has-icon"><Icon name="open_in_new" sm />任务详情</RouterLink>
      <RouterLink v-if="task.plan_id" :to="`/plans/${task.plan_id}`" class="btn sm has-icon" data-testid="task-plan-link"><Icon name="fact_check" sm />{{ task.status === 'awaiting_review' ? '去审批计划' : '归档计划' }}</RouterLink>
      <RouterLink v-if="['completed', 'partial_failure', 'awaiting_review'].includes(task.status) && (!isAuto || summary?.archived)" to="/ledger" class="btn sm has-icon"><Icon name="table_view" sm />台账</RouterLink>
      <span class="grow" />
      <button class="btn sm has-icon" :aria-expanded="open" @click="open = !open"><Icon :name="open ? 'expand_less' : 'expand_more'" sm />Agent 轨迹</button>
    </div>
    <div v-if="open" class="trace"><AgentTrace :task-id="task.id" max-height="260px" @event="emit('changed')" /></div>

    <ConfirmDialog v-if="confirmUndo" title="撤销本次全自动归档？" confirm-label="确认撤销" danger :busy="undoing" :error="undoErr" @confirm="undo" @close="confirmUndo = false">
      <p>将移除本次由模型自动归档发布的文件副本（仅当文件内容未被改动时），台账记录同步移除。导入时保存的原件不受影响；这些文件会回到“待审核”的归档计划中，等待人工处理。</p>
    </ConfirmDialog>
  </article>
</template>
<style scoped>
.task { gap: 10px; } .instr { white-space: pre-wrap; word-break: break-word; font-weight: 500; }
.result { padding: 8px 12px; border-radius: var(--shape-md); background: var(--c-surface-2); }
.trace { border: 1px solid var(--md-sys-color-outline-variant); border-radius: var(--shape-md); overflow: hidden; }
</style>
