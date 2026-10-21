<script setup lang="ts">
import { onMounted, onUnmounted, ref, watch } from 'vue'
import { api, type TaskStatus } from '../api'
import { useAsync } from '../utils/useAsync'
import { ACTIVE, fmtTime, shortId, STATUS_LABEL } from '../utils/format'
import PageHeader from '../components/PageHeader.vue'
import StatusChip from '../components/StatusChip.vue'
import StateView from '../components/StateView.vue'
import Pager from '../components/Pager.vue'
import Icon from '../components/Icon.vue'

const status = ref<TaskStatus | ''>('')
const page = ref(1), size = ref(20)
const r = useAsync(() => api.listTasks({ page: page.value, page_size: size.value, status: status.value || undefined }))
let timer = 0
onMounted(() => { void r.run(); timer = window.setInterval(() => { if (r.data.value?.items.some((t) => ACTIVE.includes(t.status))) void r.run() }, 3000) })
onUnmounted(() => clearInterval(timer))
watch([status, size], () => { page.value = 1; void r.run() })
watch(page, r.run)
</script>

<template>
  <PageHeader title="任务中心" subtitle="每次导入对应一个任务：查看阶段、文件进度与 Agent 工具轨迹。">
    <template #actions>
      <select v-model="status" class="input" aria-label="按状态筛选"><option value="">全部状态</option><option v-for="(l, k) in STATUS_LABEL" :key="k" :value="k">{{ l }}</option></select>
      <button class="btn icon-only" aria-label="刷新" @click="r.run"><Icon name="refresh" /></button>
      <RouterLink to="/import" class="btn filled has-icon"><Icon name="add" sm />新建归档</RouterLink>
    </template>
  </PageHeader>
  <div class="card flat">
    <StateView :loading="r.loading.value && !r.data.value" :error="r.error.value" :empty="!r.data.value?.items.length" empty-title="没有符合条件的任务" empty-icon="task_alt" @retry="r.run">
      <div class="table-wrap">
        <table class="tbl">
          <thead><tr><th>任务</th><th>状态</th><th style="width: 220px">进度</th><th>成功 / 失败</th><th>创建时间</th><th /></tr></thead>
          <tbody>
            <tr v-for="t in r.data.value?.items" :key="t.id" class="clickable" @click="$router.push(`/tasks/${t.id}`)">
              <td><RouterLink :to="`/tasks/${t.id}`" class="mono" @click.stop>{{ shortId(t.id) }}</RouterLink></td>
              <td><StatusChip :status="t.status" /></td>
              <td><div class="progressbar"><i :style="{ width: (t.total_files ? (t.processed_files / t.total_files) * 100 : 0) + '%' }" /></div><span class="muted">{{ t.processed_files }} / {{ t.total_files }}</span></td>
              <td>{{ t.success_count }} / <span :style="{ color: t.failure_count ? 'var(--md-sys-color-error)' : undefined }">{{ t.failure_count }}</span></td>
              <td class="muted nowrap">{{ fmtTime(t.created_at) }}</td>
              <td><RouterLink v-if="t.status === 'awaiting_review' && t.plan_id" :to="`/plans/${t.plan_id}`" class="btn tonal sm" @click.stop>去审批</RouterLink></td>
            </tr>
          </tbody>
        </table>
      </div>
      <Pager v-model:page="page" v-model:page-size="size" :total="r.data.value?.total ?? 0" />
    </StateView>
  </div>
</template>
