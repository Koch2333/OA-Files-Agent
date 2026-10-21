<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { api, type FileRecord } from '../api'
import { useAsync } from '../utils/useAsync'
import { fmtBytes, fmtTime } from '../utils/format'
import PageHeader from '../components/PageHeader.vue'
import StateView from '../components/StateView.vue'
import Pager from '../components/Pager.vue'
import Icon from '../components/Icon.vue'
import Drawer from '../components/Drawer.vue'
import FileDetail from '../components/FileDetail.vue'
import CorrectionDrawer from '../components/CorrectionDrawer.vue'

const q = ref(''), dept = ref(''), type = ref(''), from = ref(''), to = ref(''), status = ref('')
const view = ref<'list' | 'tree'>('list')
const page = ref(1), size = ref(50)
const sel = ref<FileRecord | null>(null)
const correcting = ref(false)
const r = useAsync(() => api.listFiles({ q: q.value, department: dept.value, type: type.value, date_from: from.value, date_to: to.value, archive_status: (status.value || undefined) as never, page: page.value, page_size: size.value }))
onMounted(r.run)
let t = 0
watch(q, () => { clearTimeout(t); t = window.setTimeout(() => { page.value = 1; void r.run() }, 300) })
watch([dept, type, from, to, status, size], () => { page.value = 1; void r.run() })
watch(page, r.run)

interface Node { name: string; children: Map<string, Node>; files: FileRecord[] }
const tree = computed(() => {
  const root: Node = { name: '', children: new Map(), files: [] }
  for (const f of r.data.value?.items ?? []) {
    let n = root
    const parts = f.source_path.split('/'); parts.pop()
    for (const p of parts) { if (!n.children.has(p)) n.children.set(p, { name: p, children: new Map(), files: [] }); n = n.children.get(p)! }
    n.files.push(f)
  }
  return root
})
const DEPTS = ['财务部', '人事部', '采购部', '行政部'], TYPES = ['合同', '通知', '报表', '申请']
</script>

<template>
  <PageHeader title="文件库" subtitle="所有已导入文件及其识别结果、来源与归档状态。">
    <template #actions><div class="seg" role="group" aria-label="视图"><button :aria-pressed="view === 'list'" @click="view = 'list'"><Icon name="list" sm />列表</button><button :aria-pressed="view === 'tree'" @click="view = 'tree'"><Icon name="account_tree" sm />目录树</button></div></template>
  </PageHeader>
  <div class="row wrap filters">
    <div class="search grow" style="min-width: 220px"><Icon name="search" /><input v-model="q" class="input" placeholder="搜索文件名或路径" aria-label="搜索" /></div>
    <select v-model="dept" class="input" aria-label="部门"><option value="">全部部门</option><option v-for="d in DEPTS" :key="d">{{ d }}</option></select>
    <select v-model="type" class="input" aria-label="类型"><option value="">全部类型</option><option v-for="d in TYPES" :key="d">{{ d }}</option></select>
    <select v-model="status" class="input" aria-label="归档状态"><option value="">全部状态</option><option value="pending">未归档</option><option value="archived">已归档</option><option value="failed">失败</option></select>
    <input v-model="from" type="date" class="input" aria-label="起始日期" /><span class="muted">至</span><input v-model="to" type="date" class="input" aria-label="截止日期" />
  </div>
  <div class="card flat">
    <StateView :loading="r.loading.value && !r.data.value" :error="r.error.value" :empty="!r.data.value?.items.length" empty-title="没有符合条件的文件" empty-icon="folder_off" empty-hint="调整筛选条件，或先导入文件。" @retry="r.run">
      <div v-if="view === 'list'" class="table-wrap">
        <table class="tbl">
          <thead><tr><th>文件</th><th>部门</th><th>类型</th><th>日期</th><th>大小</th><th>归档状态</th><th>导入时间</th></tr></thead>
          <tbody>
            <tr v-for="f in r.data.value?.items" :key="f.id" class="clickable" :class="{ selected: sel?.id === f.id }" tabindex="0" @click="sel = f" @keydown.enter="sel = f">
              <td><div class="ellipsis" style="max-width: 320px">{{ f.source_name }}</div><div class="muted mono ellipsis" style="max-width: 320px">{{ f.source_path }}</div></td>
              <td>{{ f.detected_department ?? '待审核' }}</td><td>{{ f.detected_type ?? '待审核' }}</td><td class="nowrap">{{ f.detected_date ?? '待审核' }}</td>
              <td class="muted nowrap">{{ fmtBytes(f.size_bytes) }}</td>
              <td><span class="chip" :class="{ success: f.archive_status === 'archived', error: f.archive_status === 'failed' }">{{ { pending: '未归档', archived: '已归档', failed: '失败' }[f.archive_status] }}</span></td>
              <td class="muted nowrap">{{ fmtTime(f.created_at) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <ul v-else class="tree" role="tree">
        <template v-for="n in [tree]" :key="'root'"><li v-for="f in n.files" :key="f.id" class="leaf" role="treeitem"><button @click="sel = f"><Icon name="description" sm />{{ f.source_name }}</button></li></template>
        <details v-for="[k, n] in tree.children" :key="k" open class="dir"><summary><Icon name="folder" sm />{{ k }}</summary>
          <ul><li v-for="f in n.files" :key="f.id" class="leaf"><button @click="sel = f"><Icon name="description" sm />{{ f.source_name }}</button></li>
            <details v-for="[k2, n2] in n.children" :key="k2" open class="dir"><summary><Icon name="folder" sm />{{ k2 }}</summary><ul><li v-for="f in n2.files" :key="f.id" class="leaf"><button @click="sel = f"><Icon name="description" sm />{{ f.source_name }}</button></li></ul></details>
          </ul>
        </details>
      </ul>
      <Pager v-model:page="page" v-model:page-size="size" :total="r.data.value?.total ?? 0" />
    </StateView>
  </div>
  <Drawer v-if="sel" title="文件详情" @close="sel = null"><FileDetail :file="sel" /><button class="btn tonal has-icon" @click="correcting = true"><Icon name="auto_fix" sm />纠错</button></Drawer>
  <CorrectionDrawer v-if="sel && correcting" :file="sel" @close="correcting = false" @applied="r.run(); sel = null" />
</template>

<style scoped>
.filters { margin-bottom: 16px; }
.tree, .tree ul { list-style: none; margin: 0; padding-left: 20px; } .tree { padding: 12px 20px; }
.tree summary { cursor: pointer; padding: 6px 0; display: flex; align-items: center; gap: 6px; font-weight: 500; } .dir > summary { list-style: none; }
.leaf button { all: unset; cursor: pointer; display: flex; gap: 6px; align-items: center; padding: 5px 8px; border-radius: var(--shape-sm); } .leaf button:hover { background: var(--c-surface-3); } .leaf button:focus-visible { outline: 3px solid var(--md-sys-color-primary); }
</style>
