<script setup lang="ts">
import { onMounted } from 'vue'
import { api } from '../api'
import { useAsync } from '../utils/useAsync'
import { fmtTime, shortId } from '../utils/format'
import PageHeader from '../components/PageHeader.vue'
import StateView from '../components/StateView.vue'
import Icon from '../components/Icon.vue'

/** 待审核队列：所有仍在等人决定的归档计划，包括“稍后审核”拆出来的那一部分。点“审核”进入该计划的审批页。 */
const r = useAsync(() => api.getReviewQueue())
onMounted(() => { void r.run() })
</script>
<template>
  <PageHeader title="待审核队列" subtitle="还没有决定的归档计划都在这里，包括你选择“稍后审核”的条目。随时可以回来处理。">
    <template #actions><button class="btn tonal has-icon" :disabled="r.loading.value" @click="r.run"><Icon name="refresh" sm />刷新</button></template>
  </PageHeader>
  <StateView :loading="r.loading.value && !r.data.value" :error="r.error.value" :empty="!(r.data.value?.length)" empty-title="没有待审核的计划" empty-icon="task_alt" @retry="r.run">
    <div class="card flat">
      <div class="table-wrap">
        <table class="tbl" data-testid="review-queue">
          <thead><tr><th>计划</th><th>条目</th><th>待人工确认</th><th>来源</th><th>更新时间</th><th /></tr></thead>
          <tbody>
            <tr v-for="e in r.data.value" :key="e.plan_id">
              <td><div class="ellipsis" style="max-width: 320px" :title="e.first_name">{{ e.first_name }}<template v-if="e.items > 1"> 等 {{ e.items }} 个文件</template></div><div class="muted mono t-label">任务 {{ shortId(e.task_id) }}</div></td>
              <td>{{ e.items }}</td>
              <td><span v-if="e.needs_review" class="chip error">{{ e.needs_review }} 项</span><span v-else class="chip success">可直接批准</span></td>
              <td><span v-if="e.deferred" class="chip"><Icon name="schedule" sm />稍后审核</span><span v-else class="muted">任务当前计划</span></td>
              <td class="muted nowrap">{{ fmtTime(e.updated_at) }}</td>
              <td class="nowrap"><RouterLink class="btn tonal sm" :to="`/plans/${e.plan_id}`" data-testid="queue-open">审核</RouterLink></td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </StateView>
</template>
