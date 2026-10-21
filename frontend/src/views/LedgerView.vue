<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { api, type LedgerEntry } from '../api'
import { errMsg, useAsync } from '../utils/useAsync'
import { fmtTime } from '../utils/format'
import { useUi } from '../stores/ui'
import PageHeader from '../components/PageHeader.vue'
import StateView from '../components/StateView.vue'
import Pager from '../components/Pager.vue'
import Icon from '../components/Icon.vue'

const ui = useUi()
const q = ref(''), dept = ref(''), type = ref(''), from = ref(''), to = ref('')
const sort = ref<keyof LedgerEntry>('executed_at'), order = ref<'asc' | 'desc'>('desc')
const page = ref(1), size = ref(20)
const exporting = ref(false)
const query = computed(() => ({ q: q.value, department: dept.value, type: type.value, date_from: from.value, date_to: to.value, sort: sort.value, order: order.value }))
const r = useAsync(() => api.listLedger({ ...query.value, page: page.value, page_size: size.value }))
onMounted(r.run)
let t = 0
watch(q, () => { clearTimeout(t); t = window.setTimeout(() => { page.value = 1; void r.run() }, 300) })
watch([dept, type, from, to, sort, order, size], () => { page.value = 1; void r.run() })
watch(page, r.run)

function sortBy(k: keyof LedgerEntry) { if (sort.value === k) order.value = order.value === 'asc' ? 'desc' : 'asc'; else { sort.value = k; order.value = 'asc' } }
const cols: { k: keyof LedgerEntry; label: string }[] = [{ k: 'original_name', label: '原文件名' }, { k: 'canonical_name', label: '规范文件名' }, { k: 'department', label: '部门' }, { k: 'document_type', label: '类型' }, { k: 'document_date', label: '日期' }, { k: 'archive_path', label: '归档路径' }, { k: 'executed_at', label: '归档时间' }]
function hl(s: string) { // 关键词高亮（转义后再包裹）
  const esc = s.replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]!))
  const k = q.value.trim()
  if (!k) return esc
  const ke = k.replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]!)).replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  return esc.replace(new RegExp(ke, 'gi'), (m) => `<mark>${m}</mark>`)
}
async function exportIt() {
  exporting.value = true
  try {
    const { blob, filename } = await api.exportLedger(query.value)
    const a = document.createElement('a'); a.href = URL.createObjectURL(blob); a.download = filename; document.body.appendChild(a); a.click(); a.remove()
    setTimeout(() => URL.revokeObjectURL(a.href), 10000)
    ui.toast(`已导出：${filename}`, 'success')
  } catch (e) { ui.toast('导出失败：' + errMsg(e), 'error') } finally { exporting.value = false }
}
const DEPTS = ['财务部', '人事部', '采购部', '行政部'], TYPES = ['合同', '通知', '报表', '申请']
</script>

<template>
  <PageHeader title="结构化台账" subtitle="归档完成后自动生成的目录台账，可搜索、筛选并导出 Excel。">
    <template #actions><button class="btn filled has-icon" :disabled="exporting" @click="exportIt"><Icon :name="exporting ? 'progress_activity' : 'download'" :class="{ spin: exporting }" sm />导出 Excel（当前筛选）</button></template>
  </PageHeader>
  <div class="row wrap" style="margin-bottom: 16px">
    <div class="search grow" style="min-width: 240px"><Icon name="search" /><input v-model="q" class="input" placeholder="关键词搜索：文件名、部门、类型、路径" aria-label="关键词搜索" /></div>
    <select v-model="dept" class="input" aria-label="部门"><option value="">全部部门</option><option v-for="d in DEPTS" :key="d">{{ d }}</option></select>
    <select v-model="type" class="input" aria-label="类型"><option value="">全部类型</option><option v-for="d in TYPES" :key="d">{{ d }}</option></select>
    <input v-model="from" type="date" class="input" aria-label="起始日期" /><span class="muted">至</span><input v-model="to" type="date" class="input" aria-label="截止日期" />
  </div>
  <div class="card flat">
    <StateView :loading="r.loading.value && !r.data.value" :error="r.error.value" :empty="!r.data.value?.items.length" :empty-title="q ? `没有命中“${q}”的记录` : '台账还是空的'" empty-icon="table_view" empty-hint="批准并执行归档计划后会自动新增记录。" @retry="r.run">
      <div class="table-wrap">
        <table class="tbl">
          <thead><tr><th v-for="c in cols" :key="c.k" class="sortable" :aria-sort="sort === c.k ? (order === 'asc' ? 'ascending' : 'descending') : 'none'" tabindex="0" @click="sortBy(c.k)" @keydown.enter="sortBy(c.k)">{{ c.label }}<Icon v-if="sort === c.k" :name="order === 'asc' ? 'arrow_upward' : 'arrow_downward'" sm /></th></tr></thead>
          <tbody>
            <tr v-for="l in r.data.value?.items" :key="l.id">
              <td class="ellipsis" style="max-width: 220px" :title="l.original_name" v-html="hl(l.original_name)" />
              <td class="ellipsis" style="max-width: 260px" :title="l.canonical_name" v-html="hl(l.canonical_name)" />
              <td v-html="hl(l.department)" /><td v-html="hl(l.document_type)" /><td class="nowrap">{{ l.document_date }}</td>
              <td class="mono ellipsis" style="max-width: 320px" :title="l.archive_path" v-html="hl(l.archive_path)" />
              <td class="muted nowrap">{{ fmtTime(l.executed_at) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <Pager v-model:page="page" v-model:page-size="size" :total="r.data.value?.total ?? 0" />
    </StateView>
  </div>
</template>
<style scoped>:deep(mark) { background: var(--c-warn-container); color: var(--c-on-warn-container); border-radius: 2px; padding: 0 1px; }</style>
