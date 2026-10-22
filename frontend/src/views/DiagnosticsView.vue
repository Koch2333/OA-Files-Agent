<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { api, type ErrorReport, type ErrorReportDetail } from '../api'
import { useDiag } from '../stores/diag'
import { useUi } from '../stores/ui'
import { errMsg, useAsync } from '../utils/useAsync'
import { copyText } from '../utils/clipboard'
import { downloadBlob } from '../utils/download'
import { fmtTime } from '../utils/format'
import { DEFAULT_FILTERS, diskView, filtersFromQuery, fmtUptime, reportClipboard, reportQuery, SEVERITIES, SEVERITY_LABEL, SEVERITY_TONE, SOURCES, SOURCE_LABEL, uncleanBanner, type DiagFilters } from '../utils/diagnostics'
import PageHeader from '../components/PageHeader.vue'
import StateView from '../components/StateView.vue'
import Pager from '../components/Pager.vue'
import Drawer from '../components/Drawer.vue'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import ErrorBanner from '../components/ErrorBanner.vue'
import Icon from '../components/Icon.vue'

const route = useRoute()
const ui = useUi()
const diag = useDiag()
const filters = ref<DiagFilters>({ ...DEFAULT_FILTERS, ...filtersFromQuery(route.query) })
const page = ref(1), size = ref(20)
const list = useAsync(() => api.listErrorReports(reportQuery(filters.value, page.value, size.value)))
const sys = computed(() => diag.system)
const unclean = computed(() => uncleanBanner(sys.value))
const disk = computed(() => (sys.value ? diskView(sys.value.disk) : null))

const detail = ref<ErrorReportDetail | null>(null)
const detailLoading = ref(false)
const detailErr = ref<string | null>(null)
const actionErr = ref<string | null>(null)
const busy = ref(false)
const confirmDel = ref(false)
const downloading = ref(false)
const downloadErr = ref<string | null>(null)

let t = 0
onMounted(() => { void list.run(); void diag.refresh() })
onUnmounted(() => clearTimeout(t))
watch(() => filters.value.q, () => { clearTimeout(t); t = window.setTimeout(() => { page.value = 1; void list.run() }, 300) })
watch(() => [filters.value.source, filters.value.severity, filters.value.state, size.value], () => { page.value = 1; void list.run() })
watch(page, list.run)

async function open(r: ErrorReport) {
  detail.value = null; detailErr.value = null; actionErr.value = null; detailLoading.value = true
  try { detail.value = await api.getErrorReport(r.id) } catch (e) { detailErr.value = errMsg(e) } finally { detailLoading.value = false }
}
function closeDetail() { detail.value = null; detailErr.value = null; detailLoading.value = false; confirmDel.value = false }
async function copy(text: string, what = '已复制') { ui.toast((await copyText(text)) ? what : '复制失败，请手动选择复制', 'info') }
async function toggleResolved() {
  if (!detail.value) return
  busy.value = true; actionErr.value = null
  try {
    detail.value = await api.updateErrorReport(detail.value.id, !detail.value.resolved)
    ui.toast(detail.value.resolved ? '已标记为已解决' : '已重新打开', 'success')
    await Promise.all([list.run(), diag.refresh()])
  } catch (e) { actionErr.value = errMsg(e) } finally { busy.value = false }
}
async function remove() {
  if (!detail.value) return
  busy.value = true; actionErr.value = null
  try {
    await api.deleteErrorReport(detail.value.id)
    ui.toast('已删除报告', 'success'); confirmDel.value = false; detail.value = null
    await Promise.all([list.run(), diag.refresh()])
  } catch (e) { actionErr.value = errMsg(e); confirmDel.value = false } finally { busy.value = false }
}
async function download() {
  downloading.value = true; downloadErr.value = null
  try {
    const { blob, filename } = await api.downloadDiagnosticBundle()
    downloadBlob(blob, filename)
    ui.toast(`已下载：${filename}`, 'success')
  } catch (e) { downloadErr.value = errMsg(e) } finally { downloading.value = false }
}
const detailEntries = computed(() => Object.entries(detail.value?.details ?? {}))
function refreshAll() { void list.run(); void diag.refresh() }
</script>

<template>
  <PageHeader title="运行诊断" subtitle="查看服务运行状态与错误报告；遇到问题时下载诊断包交给技术支持。所有内容均已脱敏。">
    <template #actions>
      <button class="btn icon-only" aria-label="刷新" @click="refreshAll"><Icon name="refresh" /></button>
      <button class="btn filled has-icon" :disabled="downloading" data-testid="bundle-download" @click="download"><Icon :name="downloading ? 'progress_activity' : 'download'" :class="{ spin: downloading }" sm />{{ downloading ? '正在生成诊断包…' : '下载诊断包' }}</button>
    </template>
  </PageHeader>
  <div v-if="downloading" class="progressbar indet" role="progressbar" aria-label="正在生成诊断包" style="margin-bottom: 12px"><i /></div>
  <ErrorBanner :message="downloadErr" />

  <div v-if="unclean" class="banner unclean" role="alert" data-testid="unclean-banner"><Icon name="warning" /><div class="grow"><b>{{ unclean.text }}</b><div class="t-label">如反复出现，请下载诊断包联系技术支持。</div></div><RouterLink :to="unclean.to" class="btn sm outlined">查看相关报告</RouterLink></div>

  <div class="grid top">
    <section class="card col" aria-label="系统状态" data-testid="system-card">
      <h2 class="t-title-m">系统状态</h2>
      <ErrorBanner :message="diag.error && !sys ? diag.error : null" />
      <div v-if="!sys && !diag.error" class="skeleton" style="height: 120px" role="status" aria-label="加载中" />
      <dl v-if="sys" class="kv">
        <dt>版本</dt><dd>{{ sys.version }} <span class="mono muted">({{ sys.commit }})</span></dd>
        <dt>构建模式</dt><dd><span class="chip" :class="sys.build_mode === 'debug' ? 'attention' : ''">{{ sys.build_mode === 'debug' ? '调试版' : '发布版' }}</span></dd>
        <dt>运行环境</dt><dd>{{ sys.os }} · {{ sys.go_version }} · {{ sys.standalone ? '独立运行（内置数据库）' : '服务部署' }}</dd>
        <dt>已运行</dt><dd>{{ fmtUptime(sys.uptime_seconds) }} <span class="muted">（自 {{ fmtTime(sys.started_at) }}）</span></dd>
        <dt>数据库</dt><dd class="row ok" :class="sys.database.ok ? 'good' : 'bad'" data-testid="db-status"><Icon :name="sys.database.ok ? 'check_circle' : 'cancel'" sm fill />{{ sys.database.ok ? '正常' : '异常' }}<span class="muted">{{ sys.database.message }}</span></dd>
        <dt>文档 Worker</dt><dd class="row ok" :class="sys.worker.ok ? 'good' : 'bad'" data-testid="worker-status"><Icon :name="sys.worker.ok ? 'check_circle' : 'cancel'" sm fill />{{ sys.worker.ok ? '正常' : '异常' }}<span class="muted">{{ sys.worker.message }}</span></dd>
      </dl>
      <div v-if="sys && disk" class="col" style="gap: 6px" data-testid="disk">
        <div class="row between"><span>归档目录磁盘 <span class="mono muted">{{ sys.disk.archive_path }}</span></span><span :class="{ warnt: disk.warn }">{{ disk.text }}</span></div>
        <div class="progressbar disk" :class="{ warn: disk.warn }" role="meter" :aria-valuenow="Math.round(disk.usedPct)" aria-valuemin="0" aria-valuemax="100" aria-label="磁盘已用"><i :style="{ width: disk.usedPct + '%' }" /></div>
        <div v-if="disk.warn" class="banner" role="alert" data-testid="disk-warn"><Icon name="warning" sm />磁盘剩余空间不足（低于 10% 或 10 GiB）：归档与导入可能失败，请清理磁盘或更换归档位置。</div>
      </div>
    </section>

    <section class="card col" aria-label="诊断包">
      <h2 class="t-title-m">诊断包</h2>
      <p>点击右上角“下载诊断包”会生成一个 zip 文件，包含：版本与环境摘要、脱敏后的配置、最近日志、错误报告、数据库迁移状态。</p>
      <div class="banner info"><Icon name="verified_user" sm /><div>诊断包<b>不包含</b>密钥、令牌、密码或任何文档内容；路径与用户名中的敏感信息已脱敏。发送给他人前仍建议大致浏览一遍。</div></div>
      <p class="muted">反馈问题时请同时提供页面上显示的“请求编号”或“报告编号”。</p>
    </section>
  </div>

  <h2 class="t-title-m sec">错误报告</h2>
  <div class="row wrap filt">
    <div class="search grow" style="min-width: 220px"><Icon name="search" /><input v-model="filters.q" class="input" placeholder="搜索消息、错误码或请求编号" aria-label="搜索错误报告" data-testid="diag-search" /></div>
    <select v-model="filters.source" class="input" aria-label="来源" data-testid="diag-source"><option value="">全部来源</option><option v-for="s in SOURCES" :key="s" :value="s">{{ SOURCE_LABEL[s] }}</option></select>
    <select v-model="filters.severity" class="input" aria-label="级别" data-testid="diag-severity"><option value="">全部级别</option><option v-for="s in SEVERITIES" :key="s" :value="s">{{ SEVERITY_LABEL[s] }}</option></select>
    <div class="seg" role="group" aria-label="处理状态"><button v-for="s in ([['open', '未解决'], ['resolved', '已解决'], ['all', '全部']] as const)" :key="s[0]" :aria-pressed="filters.state === s[0]" :data-testid="'diag-state-' + s[0]" @click="filters.state = s[0]">{{ s[1] }}</button></div>
  </div>
  <div class="card flat">
    <StateView :loading="list.loading.value && !list.data.value" :error="list.error.value" :empty="!list.data.value?.items.length" empty-title="没有错误报告" empty-icon="task_alt" empty-hint="当前筛选条件下没有记录，这通常是好消息。" @retry="list.run">
      <div class="table-wrap">
        <table class="tbl" data-testid="report-table">
          <thead><tr><th>来源</th><th>级别</th><th>错误码</th><th>消息</th><th>次数</th><th>首次</th><th>最近</th><th>状态</th></tr></thead>
          <tbody>
            <tr v-for="r in list.data.value?.items" :key="r.id" class="clickable" tabindex="0" :data-testid="'report-row-' + r.code" @click="open(r)" @keydown.enter="open(r)">
              <td class="nowrap">{{ SOURCE_LABEL[r.source] }}</td>
              <td><span class="chip" :class="SEVERITY_TONE[r.severity]">{{ SEVERITY_LABEL[r.severity] }}</span></td>
              <td class="mono nowrap">{{ r.code }}</td>
              <td class="msg" :title="r.message">{{ r.message }}</td>
              <td class="nowrap">×{{ r.count }}</td>
              <td class="muted nowrap">{{ fmtTime(r.first_seen) }}</td>
              <td class="muted nowrap">{{ fmtTime(r.last_seen) }}</td>
              <td><span class="chip" :class="r.resolved ? 'success' : 'attention'">{{ r.resolved ? '已解决' : '未解决' }}</span></td>
            </tr>
          </tbody>
        </table>
      </div>
      <Pager v-model:page="page" v-model:page-size="size" :total="list.data.value?.total ?? 0" />
    </StateView>
  </div>

  <Drawer v-if="detail || detailLoading || detailErr" title="错误报告详情" @close="closeDetail">
    <div v-if="detailLoading" class="skeleton" style="height: 120px" role="status" aria-label="加载中" />
    <ErrorBanner :message="detailErr" />
    <template v-if="detail">
      <div class="row wrap"><span class="chip" :class="SEVERITY_TONE[detail.severity]">{{ SEVERITY_LABEL[detail.severity] }}</span><span class="chip">{{ SOURCE_LABEL[detail.source] }}</span><span class="chip mono">{{ detail.code }}</span><span class="chip" :class="detail.resolved ? 'success' : 'attention'">{{ detail.resolved ? '已解决' : '未解决' }}</span></div>
      <p class="dmsg">{{ detail.message }}</p>
      <dl class="kv small">
        <dt>次数</dt><dd>×{{ detail.count }}</dd><dt>首次</dt><dd>{{ fmtTime(detail.first_seen) }}</dd><dt>最近</dt><dd>{{ fmtTime(detail.last_seen) }}</dd>
        <dt>报告编号</dt><dd class="mono" style="word-break: break-all">{{ detail.id }}</dd>
      </dl>
      <div v-if="detail.stack">
        <div class="row" style="margin-bottom: 6px"><span class="t-label muted grow">调用栈（已脱敏）</span><button class="btn sm has-icon" data-testid="copy-stack" @click="copy(detail.stack, '已复制调用栈')"><Icon name="content_copy" sm />复制</button></div>
        <pre class="stack" data-testid="report-stack" tabindex="0">{{ detail.stack }}</pre>
      </div>
      <div v-if="detailEntries.length"><div class="t-label muted" style="margin-bottom: 6px">详情（已脱敏）</div>
        <dl class="kv small"><template v-for="[k, v] in detailEntries" :key="k"><dt class="mono">{{ k }}</dt><dd class="mono" style="word-break: break-all">{{ typeof v === 'object' ? JSON.stringify(v) : String(v) }}</dd></template></dl></div>
      <div><div class="t-label muted" style="margin-bottom: 6px">最近 {{ detail.occurrences.length }} 次发生</div>
        <table class="tbl occ"><thead><tr><th>时间</th><th>请求编号</th><th>用户</th></tr></thead>
          <tbody><tr v-for="(o, i) in detail.occurrences" :key="i"><td class="nowrap">{{ fmtTime(o.at) }}</td><td class="mono"><span style="word-break: break-all">{{ o.request_id }}</span> <button class="btn icon-only sm" :aria-label="`复制请求编号 ${o.request_id}`" @click="copy(o.request_id, '已复制请求编号')"><Icon name="content_copy" sm /></button></td><td>{{ o.user || '—' }}</td></tr></tbody></table></div>
      <ErrorBanner :message="actionErr" />
      <div class="row wrap">
        <button class="btn has-icon" @click="copy(reportClipboard(detail), '已复制报告')"><Icon name="content_copy" sm />复制报告</button>
        <span class="grow" />
        <button class="btn danger has-icon" :disabled="busy" data-testid="report-delete" @click="confirmDel = true"><Icon name="delete" sm />删除</button>
        <button class="btn filled has-icon" :disabled="busy" data-testid="report-resolve" @click="toggleResolved"><Icon :name="detail.resolved ? 'undo' : 'check'" sm />{{ detail.resolved ? '重新打开' : '标记已解决' }}</button>
      </div>
    </template>
  </Drawer>
  <ConfirmDialog v-if="confirmDel && detail" title="删除这条错误报告？" confirm-label="删除" danger :busy="busy" @confirm="remove" @close="confirmDel = false"><p>报告及其发生记录将被永久删除。同样的错误再次发生时会生成新的报告。</p></ConfirmDialog>
</template>

<style scoped>
.top { grid-template-columns: minmax(0, 3fr) minmax(0, 2fr); margin-bottom: 20px; align-items: start; }
.unclean { margin-bottom: 16px; align-items: center; background: var(--md-sys-color-error-container); color: var(--md-sys-color-on-error-container); border: 1px solid var(--md-sys-color-error); }
.unclean .btn { color: inherit; border-color: currentColor; }
.kv { display: grid; grid-template-columns: 96px 1fr; gap: 10px 12px; margin: 0; } .kv dt { color: var(--md-sys-color-on-surface-variant); } .kv dd { margin: 0; min-width: 0; }
.kv.small { grid-template-columns: 88px 1fr; gap: 6px 12px; }
.ok { gap: 6px; flex-wrap: wrap; } .good .icon { color: var(--c-success); } .bad .icon { color: var(--md-sys-color-error); } .bad { color: var(--md-sys-color-error); }
.disk { height: 8px; border-radius: 4px; } .disk.warn > i { background: var(--md-sys-color-error); } .warnt { color: var(--md-sys-color-error); font-weight: 600; }
.sec { margin: 8px 0 12px; } .filt { margin-bottom: 12px; }
.msg { max-width: 360px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.dmsg { word-break: break-word; font-weight: 500; }
.stack { margin: 0; padding: 12px; max-height: 260px; overflow: auto; background: var(--c-surface-3); border-radius: var(--shape-sm); font: 12px/1.5 var(--mono); white-space: pre; }
.occ th, .occ td { padding: 6px 8px; }
@media (max-width: 1000px) { .top { grid-template-columns: 1fr; } }
</style>
