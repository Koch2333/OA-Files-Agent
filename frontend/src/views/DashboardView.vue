<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { api, type LedgerEntry } from '../api'
import { useAuth } from '../stores/auth'
import { useUi } from '../stores/ui'
import { useAsync } from '../utils/useAsync'
import { errMsg } from '../utils/errorInfo'
import { downloadBlob } from '../utils/download'
import { deriveAttention } from '../utils/attention'
import { ACTIVE, fmtTime, shortId } from '../utils/format'
import PageHeader from '../components/PageHeader.vue'
import StateView from '../components/StateView.vue'
import Icon from '../components/Icon.vue'
import ImportDropZone from '../components/ImportDropZone.vue'
import AgentProgress from '../components/AgentProgress.vue'
import RecentArchives from '../components/RecentArchives.vue'
import LedgerDetail from '../components/LedgerDetail.vue'
import Drawer from '../components/Drawer.vue'

const auth = useAuth()
const ui = useUi()
const router = useRouter()
const tasks = useAsync(() => api.listTasks({ page_size: 50 }))
const ledger = useAsync(() => api.listLedger({ page_size: 8, sort: 'executed_at', order: 'desc' }))
const files = useAsync(() => api.listFiles({ page_size: 1, review_required: true }))
const ready = useAsync(() => api.readiness())
function reload() { void tasks.run(); void ledger.run(); void files.run(); void ready.run() }
let timer = 0
onMounted(() => {
  reload()
  // 有任务在跑时每 3 秒刷新一次，进度条与“待你处理”都是实时的
  timer = window.setInterval(() => { if (list.value.some((t) => ACTIVE.includes(t.status))) { void tasks.run(); void files.run() } }, 3000)
})
onUnmounted(() => clearInterval(timer))

const list = computed(() => tasks.data.value?.items ?? [])
const running = computed(() => list.value.filter((t) => ACTIVE.includes(t.status)))
const todo = computed(() => deriveAttention(list.value, files.data.value?.total ?? 0, Date.now(), auth.canWrite))
const loadingTasks = computed(() => tasks.loading.value && !tasks.data.value)
const firstRun = computed(() => !!tasks.data.value && !!ledger.data.value && !list.value.length && !ledger.data.value.total)
// 任务从“进行中”变为结束时，最近归档同步刷新
watch(() => running.value.length, (n, o) => { if (n < o) { void ledger.run(); void files.run() } })

const stats = computed(() => [
  { key: 'run', label: '进行中', value: running.value.length, icon: 'autorenew', to: '/tasks?view=active', warn: false },
  { key: 'appr', label: '待审批', value: list.value.filter((t) => t.status === 'awaiting_review').length, icon: 'pending_actions', to: '/tasks?view=awaiting_review', warn: true },
  { key: 'rev', label: '待人工确认', value: files.data.value?.total ?? 0, icon: 'rule', to: '/files?review=1', warn: true },
  { key: 'fail', label: '失败', value: list.value.reduce((n, t) => n + t.failure_count, 0), icon: 'error', to: '/tasks?view=problem', warn: true },
  { key: 'led', label: '台账记录', value: ledger.data.value?.total ?? 0, icon: 'table_view', to: '/ledger', warn: false },
])

const retrying = ref<string | null>(null)
async function retry(id: string) {
  retrying.value = id
  try { await api.retryTask(id); ui.toast('已重新排队', 'success'); reload() } catch (e) { ui.toast(errMsg(e), 'error') } finally { retrying.value = null }
}
const canRetry = computed(() => auth.canWrite && api.capabilities.retryTask)

const sel = ref<LedgerEntry | null>(null)
const exporting = ref(false)
async function exportIt() {
  exporting.value = true
  try { const { blob, filename } = await api.exportLedger({}); downloadBlob(blob, filename); ui.toast(`已导出：${filename}`, 'success') } catch (e) { ui.toast('导出失败：' + errMsg(e), 'error') } finally { exporting.value = false }
}
const q = ref('')
function search() { void router.push({ path: '/ledger', query: q.value.trim() ? { q: q.value.trim() } : {} }) }
</script>

<template>
  <PageHeader title="工作台" subtitle="拖入文件，AI 自动识别并生成归档计划，你只需审批。">
    <template #actions>
      <form class="search qs" role="search" @submit.prevent="search"><Icon name="search" /><input v-model="q" class="input" placeholder="搜索台账：文件名、部门、类型…" aria-label="快速搜索台账" data-testid="quick-search" /></form>
      <button class="btn icon-only" aria-label="刷新" @click="reload"><Icon name="refresh" /></button>
    </template>
  </PageHeader>

  <div v-if="ready.data.value && (!ready.data.value.alive || ready.data.value.ready === false)" class="banner error mb" role="alert">
    <Icon name="warning" /><div><b>{{ ready.data.value.alive ? '后端依赖未就绪' : '无法连接后端服务' }}</b><br /><span v-if="ready.data.value.alive">数据库：{{ ready.data.value.database ?? '未知' }}，文档 Worker：{{ ready.data.value.worker ?? '未知' }}</span><span v-else>请先启动 Go API（默认 127.0.0.1:8080），或在设置中启用演示数据。</span></div>
  </div>

  <ImportDropZone :first-run="firstRun" />

  <nav class="stats" aria-label="任务统计">
    <RouterLink v-for="s in stats" :key="s.key" :to="s.to" class="stat" :class="{ hot: s.warn && s.value > 0 && !loadingTasks }" :data-testid="'stat-' + s.key">
      <span class="si"><Icon :name="s.icon" /></span>
      <span class="sv"><span class="num">{{ loadingTasks ? '–' : s.value }}</span><span class="sl">{{ s.label }}</span></span>
    </RouterLink>
  </nav>

  <div class="cols">
    <div class="left col">
      <section class="card flat" aria-labelledby="todo-h" data-testid="todo-card">
        <div class="row hd"><h2 id="todo-h" class="t-title-m grow">待你处理<span v-if="todo.length" class="cnt">{{ todo.length }}</span></h2><RouterLink to="/tasks">全部任务</RouterLink></div>
        <StateView :loading="loadingTasks" :error="tasks.error.value" :empty="false" @retry="tasks.run">
          <ul v-if="todo.length" class="todo">
            <li v-for="i in todo.slice(0, 8)" :key="i.key" :class="i.tone" :data-kind="i.kind">
              <Icon :name="i.icon" class="ti" />
              <div class="grow"><div class="tt">{{ i.title }}</div><div class="muted tm">{{ i.meta }}</div></div>
              <button v-if="i.retryable && canRetry && i.taskId" class="btn sm" :disabled="retrying === i.taskId" @click="retry(i.taskId)"><Icon name="replay" sm />重试</button>
              <RouterLink :to="i.to" class="btn sm" :class="i.kind === 'failed' ? 'tonal' : 'filled'">{{ i.actionLabel }}</RouterLink>
            </li>
          </ul>
          <p v-else class="ok" data-testid="todo-empty"><Icon name="check_circle" />暂时没有需要你处理的事项 ✓</p>
          <p v-if="todo.length > 8" class="muted more"><RouterLink to="/tasks">还有 {{ todo.length - 8 }} 项，去任务中心查看</RouterLink></p>
        </StateView>
      </section>

      <section v-if="running.length" class="card flat" aria-labelledby="run-h" data-testid="running-card">
        <div class="row hd"><h2 id="run-h" class="t-title-m grow">进行中的任务<span class="cnt">{{ running.length }}</span></h2></div>
        <ul class="runs">
          <li v-for="t in running.slice(0, 5)" :key="t.id">
            <RouterLink :to="`/tasks/${t.id}`" class="rh"><span class="mono">任务 {{ shortId(t.id) }}</span><span class="muted"> · {{ t.total_files }} 个文件 · {{ fmtTime(t.created_at) }}</span><span v-if="t.automation === 'auto'" class="chip attention" style="margin-left: 8px"><Icon name="bolt" sm />全自动</span></RouterLink>
            <AgentProgress :task="t" compact />
          </li>
        </ul>
      </section>
    </div>

    <section class="right card flat" aria-labelledby="ra-h">
      <div class="row hd"><h2 id="ra-h" class="t-title-m grow">最近归档</h2>
        <button class="btn sm has-icon" :disabled="exporting" data-testid="home-export" @click="exportIt"><Icon :name="exporting ? 'progress_activity' : 'download'" :class="{ spin: exporting }" sm />导出 Excel</button>
        <RouterLink to="/ledger" class="btn sm">打开台账</RouterLink>
      </div>
      <StateView :loading="ledger.loading.value && !ledger.data.value" :error="ledger.error.value" :empty="!ledger.data.value?.items.length" empty-title="还没有归档记录" empty-icon="table_view" empty-hint="审批并执行归档计划后，归档的文件会出现在这里，并自动记入台账。" @retry="ledger.run">
        <RecentArchives :items="ledger.data.value?.items ?? []" @open="sel = $event" />
      </StateView>
    </section>
  </div>

  <Drawer v-if="sel" title="归档详情" @close="sel = null"><LedgerDetail :entry="sel" /></Drawer>
</template>

<style scoped>
.mb { margin-bottom: 12px; }
.qs { width: 280px; }
.stats { display: grid; grid-template-columns: repeat(5, minmax(0, 1fr)); gap: 10px; margin: 12px 0; }
.stat { display: flex; align-items: center; gap: 10px; height: 64px; padding: 0 14px; border-radius: var(--shape-lg); background: var(--c-surface-1); color: inherit; text-decoration: none !important; min-width: 0; transition: background var(--dur-s); }
.stat:hover { background: var(--c-surface-2); }
.si { width: 36px; height: 36px; border-radius: 50%; display: grid; place-items: center; background: var(--c-surface-3); color: var(--md-sys-color-on-surface-variant); flex: none; }
.sv { display: flex; flex-direction: column; min-width: 0; line-height: 18px; }
.num { font-size: 22px; line-height: 26px; font-weight: 600; } .sl { font-size: 12px; color: var(--md-sys-color-on-surface-variant); white-space: nowrap; }
.stat.hot { background: var(--c-warn-container); color: var(--c-on-warn-container); } .stat.hot .si { background: var(--c-warn); color: var(--c-warn-container); } .stat.hot .sl { color: inherit; }
.cols { display: grid; grid-template-columns: repeat(12, minmax(0, 1fr)); gap: 16px; align-items: start; }
.left { grid-column: span 7; min-width: 0; gap: 16px; } .right { grid-column: span 5; min-width: 0; }
.hd { padding: 12px 16px 4px; gap: 8px; }
.cnt { margin-left: 8px; display: inline-grid; place-items: center; min-width: 22px; height: 22px; padding: 0 6px; border-radius: 11px; background: var(--md-sys-color-primary); color: var(--md-sys-color-on-primary); font-size: 12px; font-weight: 600; vertical-align: 1px; }
.todo { list-style: none; margin: 0; padding: 4px 8px 8px; display: flex; flex-direction: column; gap: 4px; }
.todo li { display: flex; align-items: center; gap: 10px; padding: 10px 12px; border-radius: var(--shape-md); background: color-mix(in srgb, var(--c-warn-container) 55%, transparent); min-width: 0; flex-wrap: wrap; }
.todo li.error { background: color-mix(in srgb, var(--md-sys-color-error-container) 55%, transparent); }
.ti { color: var(--c-warn); } .todo li.error .ti { color: var(--md-sys-color-error); }
.tt { font-weight: 600; word-break: break-word; } .tm { font-size: 12px; }
.ok { margin: 0; padding: 20px 16px 24px; display: flex; align-items: center; gap: 8px; justify-content: center; color: var(--c-success); font-weight: 500; }
.more { padding: 0 16px 12px; font-size: 13px; }
.runs { list-style: none; margin: 0; padding: 4px 16px 12px; display: flex; flex-direction: column; gap: 12px; }
.runs li { display: flex; flex-direction: column; gap: 6px; padding-bottom: 12px; border-bottom: 1px solid var(--md-sys-color-outline-variant); } .runs li:last-child { border-bottom: 0; padding-bottom: 0; }
.rh { color: inherit; font-size: 13px; } .rh .mono { font-weight: 600; }
@media (max-width: 1100px) { .qs { width: 220px; } }
@media (max-width: 900px) {
  .cols { grid-template-columns: 1fr; } .left, .right { grid-column: auto; }
  .stats { display: flex; overflow-x: auto; gap: 8px; margin-inline: -4px; padding: 0 4px 4px; scroll-snap-type: x proximity; }
  .stat { flex: 0 0 150px; scroll-snap-align: start; }
  .qs { width: 100%; }
  .todo li .btn { min-height: 44px; }
}
</style>
