<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { api, type LedgerEntry } from '../api'
import { errMsg, useAsync } from '../utils/useAsync'
import { fmtTime } from '../utils/format'
import { archiveRow } from '../utils/recentArchive'
import { displayCategory, OTHER } from '../utils/classification'
import { downloadBlob } from '../utils/download'
import { useUi } from '../stores/ui'
import PageHeader from '../components/PageHeader.vue'
import StateView from '../components/StateView.vue'
import Pager from '../components/Pager.vue'
import Icon from '../components/Icon.vue'
import DecisionBadge from '../components/DecisionBadge.vue'
import Drawer from '../components/Drawer.vue'
import LedgerDetail from '../components/LedgerDetail.vue'

const ui = useUi()
const route = useRoute()
const q = ref(typeof route.query.q === 'string' ? route.query.q : ''), dept = ref(''), type = ref(''), from = ref(''), to = ref('')
const sort = ref<keyof LedgerEntry>('executed_at'), order = ref<'asc' | 'desc'>('desc')
const page = ref(1), size = ref(20)
const exporting = ref(false)
const onlyAuto = ref(false)
const sel = ref<LedgerEntry | null>(null)
/** 列：完整（含原文件名与归档路径，台账的全部必需列）/ 简洁（易读名称 + 标签）。本会话内记住选择，默认完整。 */
function loadCols(): 'full' | 'lite' { try { return sessionStorage.getItem('oa.ledger.cols') === 'lite' ? 'lite' : 'full' } catch { return 'full' } }
const cols = ref<'full' | 'lite'>(loadCols())
watch(cols, (v) => { try { sessionStorage.setItem('oa.ledger.cols', v) } catch { /* ignore */ } })
const query = computed(() => ({ q: q.value, department: dept.value, type: type.value, date_from: from.value, date_to: to.value, sort: sort.value, order: order.value, decision: onlyAuto.value ? ('ai_auto' as const) : undefined }))
const r = useAsync(() => api.listLedger({ ...query.value, page: page.value, page_size: size.value }))
onMounted(() => { void r.run(); void loadOptions() })
let t = 0
watch(q, () => { clearTimeout(t); t = window.setTimeout(() => { page.value = 1; void r.run() }, 300) })
watch([dept, type, from, to, sort, order, size, onlyAuto], () => { page.value = 1; void r.run() })
watch(page, r.run)

function sortBy(k: keyof LedgerEntry) { if (sort.value === k) order.value = order.value === 'asc' ? 'desc' : 'asc'; else { sort.value = k; order.value = 'asc' } }
const FULL: { k: keyof LedgerEntry; label: string }[] = [{ k: 'original_name', label: '原文件名' }, { k: 'canonical_name', label: '规范文件名' }, { k: 'department', label: '部门' }, { k: 'document_type', label: '类型' }, { k: 'document_date', label: '日期' }, { k: 'archive_path', label: '归档路径' }, { k: 'executed_at', label: '归档时间' }]
const LITE: { k: keyof LedgerEntry; label: string }[] = [{ k: 'canonical_name', label: '文件名' }, { k: 'department', label: '部门' }, { k: 'document_type', label: '类型' }, { k: 'document_date', label: '日期' }, { k: 'executed_at', label: '归档时间' }]
const colDefs = computed(() => (cols.value === 'full' ? FULL : LITE))
/** 兜底：若后端未实现 decision 过滤，客户端仍只展示全自动条目（总数以服务端为准） */
const rows = computed(() => (r.data.value?.items ?? []).filter((l) => !onlyAuto.value || l.decision === 'ai_auto'))
const liteRows = computed(() => rows.value.map((l) => ({ l, v: archiveRow(l, Date.now(), 60) })))
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
    downloadBlob(blob, filename)
    ui.toast(`已导出：${filename}`, 'success')
  } catch (e) { ui.toast('导出失败：' + errMsg(e), 'error') } finally { exporting.value = false }
}
// 筛选下拉：台账里实际出现过的值 + 内置词表（自动生成）；「其他」固定在末尾，匹配显式「其他」和空值/未知值
const DEPTS = ref<string[]>(['财务部', '人事部', '采购部', '行政部', OTHER]), TYPES = ref<string[]>(['合同', '通知', '报表', '申请', OTHER])
async function loadOptions() {
  try {
    const o = await api.getClassificationOptions()
    const vals = (xs: { value: string }[]) => [...xs.filter((x) => x.value !== OTHER).map((x) => x.value), OTHER]
    DEPTS.value = vals(o.departments); TYPES.value = vals(o.types)
  } catch { /* 保持默认候选 */ }
}
</script>

<template>
  <PageHeader title="台账（目录日志）" subtitle="每次归档自动记录；可搜索、筛选，并导出 Excel。点一行查看原文件名、完整路径等详情。">
    <template #actions>
      <div class="seg" role="group" aria-label="显示列"><button :aria-pressed="cols === 'lite'" data-testid="ledger-cols-lite" @click="cols = 'lite'"><Icon name="view_agenda" sm />简洁</button><button :aria-pressed="cols === 'full'" data-testid="ledger-cols-full" @click="cols = 'full'"><Icon name="table_rows" sm />完整列</button></div>
      <button class="btn filled has-icon" :disabled="exporting" @click="exportIt"><Icon :name="exporting ? 'progress_activity' : 'download'" :class="{ spin: exporting }" sm />导出 Excel（当前筛选）</button>
    </template>
  </PageHeader>
  <div class="row wrap" style="margin-bottom: 12px">
    <div class="search grow" style="min-width: 240px"><Icon name="search" /><input v-model="q" class="input" placeholder="关键词搜索：文件名、部门、类型、路径" aria-label="关键词搜索" /></div>
    <select v-model="dept" class="input" aria-label="部门"><option value="">全部部门</option><option v-for="d in DEPTS" :key="d">{{ d }}</option></select>
    <select v-model="type" class="input" aria-label="类型"><option value="">全部类型</option><option v-for="d in TYPES" :key="d">{{ d }}</option></select>
    <input v-model="from" type="date" class="input" aria-label="起始日期" /><span class="muted">至</span><input v-model="to" type="date" class="input" aria-label="截止日期" />
    <label class="switch" title="只显示由大模型全自动决策归档的记录"><input v-model="onlyAuto" type="checkbox" role="switch" data-testid="ledger-only-auto" />仅全自动</label>
  </div>
  <div class="card flat">
    <StateView :loading="r.loading.value && !r.data.value" :error="r.error.value" :empty="!rows.length" :empty-title="q ? `没有命中“${q}”的记录` : '台账还是空的'" empty-icon="table_view" empty-hint="审批并执行归档计划后会自动新增记录。" @retry="r.run">
      <div class="table-wrap">
        <table class="tbl">
          <thead><tr><th v-for="c in colDefs" :key="c.k" class="sortable" :aria-sort="sort === c.k ? (order === 'asc' ? 'ascending' : 'descending') : 'none'" tabindex="0" @click="sortBy(c.k)" @keydown.enter="sortBy(c.k)">{{ c.label }}<Icon v-if="sort === c.k" :name="order === 'asc' ? 'arrow_upward' : 'arrow_downward'" sm /></th><th>决策方式</th></tr></thead>
          <tbody v-if="cols === 'full'">
            <tr v-for="l in rows" :key="l.id" class="clickable" tabindex="0" :aria-label="`查看详情：${l.canonical_name}`" @click="sel = l" @keydown.enter="sel = l">
              <td class="ellipsis" style="max-width: 220px" :title="l.original_name" v-html="hl(l.original_name)" />
              <td class="ellipsis" style="max-width: 260px" :title="l.canonical_name" v-html="hl(l.canonical_name)" />
              <td class="nowrap" v-html="hl(displayCategory(l.department))" /><td class="nowrap" v-html="hl(displayCategory(l.document_type))" /><td class="nowrap">{{ l.document_date }}</td>
              <td class="mono ellipsis" style="max-width: 320px" :title="l.archive_path"><RouterLink :to="{ path: '/manage', query: { path: l.archive_path.split('/').slice(0, -1).join('/') } }" title="在文件管理中查看" @click.stop v-html="hl(l.archive_path)" /></td>
              <td class="muted nowrap">{{ fmtTime(l.executed_at) }}</td>
              <td class="nowrap"><DecisionBadge :decision="l.decision ?? 'manual'" :model="l.decision_model" /></td>
            </tr>
          </tbody>
          <tbody v-else>
            <tr v-for="{ l, v } in liteRows" :key="l.id" class="clickable" tabindex="0" :aria-label="`查看详情：${l.canonical_name}`" @click="sel = l" @keydown.enter="sel = l">
              <td class="ellipsis" style="max-width: 380px" :title="v.fullTitle" v-html="hl(v.title)" />
              <td><span class="chip" :class="{ attention: v.department.missing }">{{ v.department.text }}</span></td>
              <td><span class="chip" :class="{ attention: v.type.missing }">{{ v.type.text }}</span></td>
              <td class="nowrap"><span class="chip" :class="{ attention: v.date.missing }">{{ v.date.text }}</span></td>
              <td class="muted nowrap" :title="fmtTime(l.executed_at)">{{ v.when }}</td>
              <td class="nowrap"><DecisionBadge :decision="l.decision ?? 'manual'" :model="l.decision_model" /></td>
            </tr>
          </tbody>
        </table>
      </div>
      <Pager v-model:page="page" v-model:page-size="size" :total="r.data.value?.total ?? 0" />
    </StateView>
  </div>
  <Drawer v-if="sel" title="归档详情" @close="sel = null"><LedgerDetail :entry="sel" @updated="(e) => { sel = e; void r.run() }" /></Drawer>
</template>
<style scoped>:deep(mark) { background: var(--c-warn-container); color: var(--c-on-warn-container); border-radius: 2px; padding: 0 1px; }</style>
