<script setup lang="ts">
import { useAuth } from '../stores/auth'
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { api, type ImportJob, type TaskStatus } from '../api'
import { useAsync } from '../utils/useAsync'
import { ACTIVE, fmtTime, shortId, STATUS_LABEL } from '../utils/format'
import PageHeader from '../components/PageHeader.vue'
import StatusChip from '../components/StatusChip.vue'
import AgentProgress from '../components/AgentProgress.vue'
import StateView from '../components/StateView.vue'
import Pager from '../components/Pager.vue'
import Icon from '../components/Icon.vue'

const auth = useAuth()
const route = useRoute()
/** 视图筛选：具体状态，或工作台统计链接带来的分组（进行中 / 有失败）。分组在前端过滤最近 100 个任务。 */
const GROUPS: Record<string, { label: string; match: (t: ImportJob) => boolean }> = {
  active: { label: '进行中（全部阶段）', match: (t) => ACTIVE.includes(t.status) },
  problem: { label: '有失败的任务', match: (t) => t.status === 'failed' || t.status === 'partial_failure' },
}
const fromQuery = (v: unknown) => { const s = String(v ?? ''); return s in GROUPS || s in STATUS_LABEL ? s : '' }
const status = ref<string>(fromQuery(route.query.view ?? route.query.status))
const grouped = computed(() => status.value in GROUPS)
const page = ref(1), size = ref(20)
const r = useAsync(() => api.listTasks(grouped.value ? { page: 1, page_size: 100 } : { page: page.value, page_size: size.value, status: (status.value || undefined) as TaskStatus | undefined }))
const matched = computed(() => (grouped.value ? (r.data.value?.items ?? []).filter(GROUPS[status.value].match) : (r.data.value?.items ?? [])))
const rows = computed(() => (grouped.value ? matched.value.slice((page.value - 1) * size.value, page.value * size.value) : matched.value))
const total = computed(() => (grouped.value ? matched.value.length : (r.data.value?.total ?? 0)))
let timer = 0
onMounted(() => { void r.run(); timer = window.setInterval(() => { if (r.data.value?.items.some((t) => ACTIVE.includes(t.status))) void r.run() }, 3000) })
onUnmounted(() => clearInterval(timer))
watch([status, size], () => { page.value = 1; void r.run() })
watch(page, () => { if (!grouped.value) void r.run() })
</script>

<template>
  <PageHeader title="任务中心" subtitle="每次导入对应一个任务：看它走到哪一步，轮到你时直接去审批。">
    <template #actions>
      <select v-model="status" class="input" aria-label="按状态筛选">
        <option value="">全部状态</option>
        <optgroup label="分组"><option v-for="(g, k) in GROUPS" :key="k" :value="k">{{ g.label }}</option></optgroup>
        <optgroup label="按状态"><option v-for="(l, k) in STATUS_LABEL" :key="k" :value="k">{{ l }}</option></optgroup>
      </select>
      <button class="btn icon-only" aria-label="刷新" @click="r.run"><Icon name="refresh" /></button>
      <RouterLink v-if="auth.canWrite" to="/import" class="btn filled has-icon"><Icon name="add" sm />新建归档</RouterLink>
    </template>
  </PageHeader>
  <div class="card flat">
    <StateView :loading="r.loading.value && !r.data.value" :error="r.error.value" :empty="!rows.length" empty-title="没有符合条件的任务" empty-icon="task_alt" empty-hint="换个筛选条件，或到“智能归档”拖入文件开始。" @retry="r.run">
      <div class="table-wrap">
        <table class="tbl">
          <thead><tr><th>任务</th><th>Agent 执行进度</th><th>成功 / 失败</th><th>创建时间</th><th /></tr></thead>
          <tbody>
            <tr v-for="t in rows" :key="t.id" class="clickable" tabindex="0" @click="$router.push(`/tasks/${t.id}`)" @keydown.enter="$router.push(`/tasks/${t.id}`)">
              <td><div class="col" style="gap: 4px; align-items: flex-start"><RouterLink :to="`/tasks/${t.id}`" class="mono" @click.stop>{{ shortId(t.id) }}</RouterLink><StatusChip :status="t.status" /></div></td>
              <td class="prog"><AgentProgress :task="t" compact /></td>
              <td class="nowrap">{{ t.success_count }} / <span :style="{ color: t.failure_count ? 'var(--md-sys-color-error)' : undefined }">{{ t.failure_count }}</span><div class="muted t-label">共 {{ t.total_files }} 个文件</div></td>
              <td class="muted nowrap">{{ fmtTime(t.created_at) }}</td>
              <td><RouterLink v-if="t.status === 'awaiting_review' && t.plan_id" :to="`/plans/${t.plan_id}`" class="btn tonal sm" @click.stop>去审批</RouterLink></td>
            </tr>
          </tbody>
        </table>
      </div>
      <Pager v-model:page="page" v-model:page-size="size" :total="total" />
    </StateView>
  </div>
</template>
<style scoped>
.prog { min-width: 360px; }
@media (max-width: 900px) { .prog { min-width: 220px; } }
</style>
