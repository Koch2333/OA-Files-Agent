<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { api } from '../api'
import { useAsync } from '../utils/useAsync'
import { ACTIVE, fmtTime, shortId } from '../utils/format'
import PageHeader from '../components/PageHeader.vue'
import StatusChip from '../components/StatusChip.vue'
import StateView from '../components/StateView.vue'
import Icon from '../components/Icon.vue'

const tasks = useAsync(() => api.listTasks({ page_size: 50 }))
const ledger = useAsync(() => api.listLedger({ page_size: 5 }))
const files = useAsync(() => api.listFiles({ page_size: 1, review_required: true }))
const ready = useAsync(() => api.readiness())
function reload() { void tasks.run(); void ledger.run(); void files.run(); void ready.run() }
onMounted(reload)

const list = computed(() => tasks.data.value?.items ?? [])
const stats = computed(() => [
  { label: '进行中任务', value: list.value.filter((t) => ACTIVE.includes(t.status)).length, icon: 'autorenew', to: '/tasks' },
  { label: '待审批计划', value: list.value.filter((t) => t.status === 'awaiting_review').length, icon: 'pending_actions', to: '/tasks' },
  { label: '待人工确认文件', value: files.data.value?.total ?? 0, icon: 'rule', to: '/files' },
  { label: '失败文件数', value: list.value.reduce((n, t) => n + t.failure_count, 0), icon: 'error', to: '/tasks' },
  { label: '台账记录', value: ledger.data.value?.total ?? 0, icon: 'table_view', to: '/ledger' },
])
const pending = computed(() => list.value.filter((t) => t.status === 'awaiting_review'))
</script>

<template>
  <PageHeader title="工作台" subtitle="选择文件或文件夹，Agent 自动识别、命名并生成归档计划，你只需审批。">
    <template #actions><button class="btn icon-only" aria-label="刷新" @click="reload"><Icon name="refresh" /></button></template>
  </PageHeader>

  <section class="hero card">
    <div class="grow"><div class="t-title">一键智能归档</div><p class="muted" style="margin-top: 4px">支持 Word / Excel / PDF / 图片（含扫描件 OCR），可整夹拖入。归档前一定会先让你预览并审批。</p></div>
    <RouterLink to="/import" class="btn filled has-icon"><Icon name="upload_file" sm />开始导入</RouterLink>
  </section>

  <div v-if="ready.data.value && (!ready.data.value.alive || ready.data.value.ready === false)" class="banner error" style="margin-top: 16px" role="alert">
    <Icon name="warning" /><div><b>{{ ready.data.value.alive ? '后端依赖未就绪' : '无法连接后端服务' }}</b><br /><span v-if="ready.data.value.alive">数据库：{{ ready.data.value.database ?? '未知' }}，文档 Worker：{{ ready.data.value.worker ?? '未知' }}</span><span v-else>请先启动 Go API（默认 127.0.0.1:8080），或在设置中启用演示数据。</span></div>
  </div>

  <div class="stats">
    <RouterLink v-for="s in stats" :key="s.label" :to="s.to" class="stat card" style="text-decoration: none">
      <Icon :name="s.icon" class="muted" /><div class="num">{{ tasks.loading.value && !tasks.data.value ? '–' : s.value }}</div><div class="muted">{{ s.label }}</div>
    </RouterLink>
  </div>

  <div class="two">
    <section class="card flat">
      <div class="row hd"><h2 class="t-title-m grow">待你审批</h2><RouterLink to="/tasks">全部任务</RouterLink></div>
      <StateView :loading="tasks.loading.value" :error="tasks.error.value" :empty="!pending.length" empty-title="没有待审批的归档计划" empty-icon="task_alt" empty-hint="新导入的文件处理完成后会出现在这里。" @retry="tasks.run">
        <ul class="list"><li v-for="t in pending" :key="t.id">
          <div class="grow"><div>任务 {{ shortId(t.id) }} · {{ t.total_files }} 个文件</div><div class="muted">{{ fmtTime(t.created_at) }}</div></div>
          <RouterLink v-if="t.plan_id" :to="`/plans/${t.plan_id}`" class="btn tonal sm">去审批</RouterLink><RouterLink v-else :to="`/tasks/${t.id}`" class="btn sm">查看</RouterLink>
        </li></ul>
      </StateView>
    </section>
    <section class="card flat">
      <div class="row hd"><h2 class="t-title-m grow">最近任务</h2></div>
      <StateView :loading="tasks.loading.value" :error="tasks.error.value" :empty="!list.length" empty-title="还没有任务" empty-icon="inbox" @retry="tasks.run">
        <ul class="list"><li v-for="t in list.slice(0, 5)" :key="t.id">
          <RouterLink :to="`/tasks/${t.id}`" class="grow" style="color: inherit">任务 {{ shortId(t.id) }}<div class="muted">{{ fmtTime(t.created_at) }} · {{ t.success_count }}/{{ t.total_files }} 成功</div></RouterLink>
          <StatusChip :status="t.status" />
        </li></ul>
      </StateView>
    </section>
    <section class="card flat span">
      <div class="row hd"><h2 class="t-title-m grow">最近归档</h2><RouterLink to="/ledger">打开台账</RouterLink></div>
      <StateView :loading="ledger.loading.value" :error="ledger.error.value" :empty="!ledger.data.value?.items.length" empty-title="尚无归档记录" empty-icon="table_view" empty-hint="批准并执行归档计划后，台账会自动新增记录。" @retry="ledger.run">
        <ul class="list"><li v-for="l in ledger.data.value?.items" :key="l.id">
          <Icon name="description" class="muted" /><div class="grow"><div class="ellipsis">{{ l.canonical_name }}</div><div class="muted ellipsis">{{ l.archive_path }}</div></div><span class="muted nowrap">{{ fmtTime(l.executed_at) }}</span>
        </li></ul>
      </StateView>
    </section>
  </div>
</template>

<style scoped>
.hero { display: flex; gap: 24px; align-items: center; padding: 28px; background: var(--md-sys-color-primary-container); color: var(--md-sys-color-on-primary-container); } .hero .muted { color: inherit; opacity: .8; }
.stats { display: grid; grid-template-columns: repeat(auto-fit, minmax(170px, 1fr)); gap: 12px; margin: 16px 0; }
.stat { display: flex; flex-direction: column; gap: 4px; color: inherit; } .stat:hover { background: var(--c-surface-2); } .num { font-size: 32px; line-height: 40px; font-weight: 500; }
.two { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 16px; } .span { grid-column: 1 / -1; }
.hd { padding: 16px 20px 4px; } .list { list-style: none; margin: 0; padding: 4px 8px 12px; } .list li { display: flex; align-items: center; gap: 12px; padding: 10px 12px; border-radius: var(--shape-sm); min-width: 0; }
@media (max-width: 1100px) { .two { grid-template-columns: 1fr; } .hero { flex-direction: column; align-items: flex-start; } }
</style>
