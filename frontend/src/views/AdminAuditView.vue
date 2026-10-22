<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import { api } from '../api'
import { fmtTime } from '../utils/format'
import { useAsync } from '../utils/useAsync'
import PageHeader from '../components/PageHeader.vue'
import StateView from '../components/StateView.vue'
import Pager from '../components/Pager.vue'
import Icon from '../components/Icon.vue'
const page = ref(1), size = ref(50)
const r = useAsync(() => api.listAuditLogs({ page: page.value, page_size: size.value }))
onMounted(r.run)
watch(page, r.run)
watch(size, () => { page.value = 1; void r.run() })
</script>
<template>
  <PageHeader title="审计日志" subtitle="登录、权限、用户与策略变更、归档操作等记录（只读）。">
    <template #actions><button class="btn icon-only" aria-label="刷新" @click="r.run"><Icon name="refresh" /></button></template>
  </PageHeader>
  <div class="card flat">
    <StateView :loading="r.loading.value && !r.data.value" :error="r.error.value" :empty="!r.data.value?.items.length" empty-title="暂无审计记录" empty-icon="history" @retry="r.run">
      <div class="table-wrap"><table class="tbl" aria-label="审计日志"><thead><tr><th>时间</th><th>操作人</th><th>动作</th><th>对象</th><th>说明</th></tr></thead>
        <tbody><tr v-for="a in r.data.value?.items" :key="a.id"><td class="nowrap muted">{{ fmtTime(a.timestamp) }}</td><td>{{ a.actor }}</td><td class="mono">{{ a.action }}</td><td class="mono ellipsis" style="max-width: 200px" :title="a.target">{{ a.target }}</td><td>{{ a.summary }}</td></tr></tbody></table></div>
      <Pager v-model:page="page" v-model:page-size="size" :total="r.data.value?.total ?? 0" />
    </StateView>
  </div>
</template>
