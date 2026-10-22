<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { api, ApiError, type Page, type Replication, type ReplicationStatus, type StorageConnection, type StorageKind, type StorageTestResult } from '../api'
import { errMsg } from '../utils/useAsync'
import { fmtBytes, fmtTime } from '../utils/format'
import {
  groupLabel, isReplicationActive, kindIcon, REPLICATE_LABEL, REPLICATION_LABEL, REPLICATION_TONE, replicationSummary,
} from '../utils/storage'
import { useUi } from '../stores/ui'
import PageHeader from '../components/PageHeader.vue'
import StateView from '../components/StateView.vue'
import Dialog from '../components/Dialog.vue'
import Pager from '../components/Pager.vue'
import Icon from '../components/Icon.vue'
import StorageConnectionDialog from '../components/StorageConnectionDialog.vue'
import RemoteBrowserDialog from '../components/RemoteBrowserDialog.vue'

const ui = useUi()
const tab = ref<'conns' | 'reps'>('conns')

// ---- 连接 ----
const kinds = ref<StorageKind[]>([])
const conns = ref<StorageConnection[]>([])
const loading = ref(true)
const error = ref<string | null>(null)
const featureOff = ref(false)

async function load() {
  loading.value = true; error.value = null
  try {
    kinds.value = await api.listStorageKinds()
    featureOff.value = false
    conns.value = await api.listStorageConnections()
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) featureOff.value = true
    else error.value = errMsg(e)
  } finally { loading.value = false }
}
async function refreshConns() { try { conns.value = await api.listStorageConnections() } catch { /* 保留旧数据 */ } }

const kindOf = (id: string) => kinds.value.find((k) => k.id === id)
const kindName = (id: string) => kindOf(id)?.name ?? id

const dlg = ref<{ edit: StorageConnection | null } | null>(null)
async function onSaved(c: StorageConnection) {
  const edited = !!dlg.value?.edit
  dlg.value = null
  ui.toast(edited ? `已保存“${c.name}”` : `已添加“${c.name}”`, 'success')
  await refreshConns()
}

const busy = ref<Record<string, string>>({})
const results = ref<Record<string, StorageTestResult>>({})
async function withBusy(id: string, what: string, fn: () => Promise<void>) {
  busy.value = { ...busy.value, [id]: what }
  try { await fn() } catch (e) { ui.toast(errMsg(e), 'error') } finally { const b = { ...busy.value }; delete b[id]; busy.value = b }
}
const test = (c: StorageConnection) => withBusy(c.id, 'test', async () => {
  const res = await api.testStorageConnection(c.id, { check_write: c.replicate !== 'off' })
  results.value = { ...results.value, [c.id]: res }
  ui.toast(res.ok ? `“${c.name}”连接正常` : `“${c.name}”测试未通过`, res.ok ? 'success' : 'error')
  await refreshConns()
})
const toggle = (c: StorageConnection) => withBusy(c.id, 'toggle', async () => {
  await api.updateStorageConnection(c.id, { enabled: !c.enabled })
  await refreshConns()
})
const replicate = (c: StorageConnection) => withBusy(c.id, 'rep', async () => {
  const r = await api.replicateToStorage(c.id)
  ui.toast(r.queued ? `已加入同步队列：${r.queued} 个文件` : '没有需要同步的缺失项', r.queued ? 'success' : 'info')
  await refreshConns()
  if (r.queued) { tab.value = 'reps'; repFilter.value.connection_id = c.id }
})

const del = ref<StorageConnection | null>(null)
async function doDelete() {
  const c = del.value
  if (!c) return
  await withBusy(c.id, 'del', async () => { await api.deleteStorageConnection(c.id); del.value = null; ui.toast(`已删除“${c.name}”`, 'success'); await refreshConns() })
}
const browsing = ref<StorageConnection | null>(null)

// ---- 同步记录 ----
const repFilter = ref<{ connection_id: string; status: '' | ReplicationStatus }>({ connection_id: '', status: '' })
const repPage = ref(1)
const repSize = ref(20)
const reps = ref<Page<Replication> | null>(null)
const repsLoading = ref(false)
const repsError = ref<string | null>(null)
let timer: number | undefined

async function loadReps(silent = false) {
  if (!silent) repsLoading.value = true
  repsError.value = null
  try {
    reps.value = await api.listReplications({
      page: repPage.value, page_size: repSize.value, connection_id: repFilter.value.connection_id || undefined, status: repFilter.value.status || undefined,
    })
  } catch (e) { repsError.value = errMsg(e) } finally { repsLoading.value = false }
}
const hasActive = computed(() => !!reps.value?.items.some((r) => isReplicationActive(r.status)))
function schedule() {
  window.clearInterval(timer)
  timer = undefined
  if (tab.value === 'reps' && hasActive.value) timer = window.setInterval(() => { void loadReps(true).then(refreshConns) }, 4000)
}
watch([hasActive, tab], schedule)
watch([repFilter, repSize], () => { repPage.value = 1; void loadReps() }, { deep: true })
watch(repPage, () => loadReps())
watch(tab, (t) => { if (t === 'reps') void loadReps() })
async function retry(r: Replication) {
  try { await api.retryReplication(r.id); ui.toast('已重新加入队列', 'success'); await loadReps(true) } catch (e) { ui.toast(errMsg(e), 'error') }
}

onMounted(load)
onBeforeUnmount(() => window.clearInterval(timer))

const policyChip = (c: StorageConnection) => `同步：${REPLICATE_LABEL[c.replicate]}`
</script>

<template>
  <PageHeader title="存储连接" subtitle="可选功能：从云存储或企业内部存储导入文件，并把归档成品只增不改不删地同步到远端。">
    <template #actions>
      <button v-if="!featureOff && kinds.length" class="btn filled has-icon" data-testid="storage-add" @click="dlg = { edit: null }"><Icon name="add" sm />添加连接</button>
    </template>
  </PageHeader>

  <div v-if="featureOff" class="card col center-note" data-testid="storage-off">
    <Icon name="cloud_off" class="big" />
    <div class="t-title-m">存储连接功能未启用</div>
    <p class="muted">当前后端没有开启存储连接功能。这是可选功能，不影响本机导入与归档；需要时请联系管理员启用。</p>
  </div>

  <template v-else>
    <div class="seg" role="group" aria-label="存储连接视图" style="margin-bottom: 16px">
      <button :aria-pressed="tab === 'conns'" data-tab="conns" @click="tab = 'conns'">连接</button>
      <button :aria-pressed="tab === 'reps'" data-tab="reps" @click="tab = 'reps'">同步记录</button>
    </div>

    <!-- 连接 -->
    <section v-if="tab === 'conns'" data-testid="storage-conns">
      <StateView :loading="loading" :error="error" :empty="!conns.length" empty-icon="cloud_sync" empty-title="还没有存储连接" @retry="load">
        <template #empty-action>
          <div class="col empty-extra">
            <p class="muted msg">存储连接是可选功能：不配置时，系统只使用本机上传与本机归档目录。配置后可以从远端选文件导入，也可以把归档成品复制到远端备份。</p>
            <div class="row wrap" style="justify-content: center"><span v-for="k in kinds" :key="k.id" class="chip"><Icon :name="kindIcon(k.id)" sm />{{ k.name }}</span></div>
            <button class="btn filled has-icon" @click="dlg = { edit: null }"><Icon name="add" sm />添加第一个连接</button>
          </div>
        </template>
        <div class="cards">
          <article v-for="c in conns" :key="c.id" class="card col conn" :class="{ off: !c.enabled }" :data-conn="c.name">
            <div class="row">
              <Icon :name="kindIcon(c.kind)" class="kicon" />
              <div class="grow">
                <div class="t-title-m ellipsis" :title="c.name">{{ c.name }}</div>
                <div class="muted small">{{ kindName(c.kind) }} · <span class="chip">{{ groupLabel(kindOf(c.kind)?.group ?? '') }}</span></div>
              </div>
              <label class="switch" :title="c.enabled ? '点击停用' : '点击启用'"><input type="checkbox" role="switch" :checked="c.enabled" :disabled="!!busy[c.id]" :aria-label="`启用 ${c.name}`" @change="toggle(c)" /></label>
            </div>
            <div class="muted small">起始目录：<span class="mono">{{ c.base_path || '/（根）' }}</span></div>
            <div class="row wrap" style="gap: 6px">
              <span v-if="c.use_for_import" class="chip progress">用于导入</span>
              <span class="chip" :class="{ success: c.replicate === 'auto' }">{{ policyChip(c) }}</span>
              <span v-if="!c.enabled" class="chip attention">已停用</span>
            </div>
            <div class="small">
              <template v-if="c.last_test">
                <span class="chip" :class="c.last_test.ok ? 'success' : 'error'"><Icon :name="c.last_test.ok ? 'check_circle' : 'error'" sm />{{ c.last_test.ok ? '测试通过' : '测试失败' }}</span>
                <span class="muted"> {{ fmtTime(c.last_test.at) }} · {{ c.last_test.message }}</span>
              </template>
              <span v-else class="muted">尚未测试</span>
            </div>
            <div class="muted small">同步：{{ replicationSummary(c) }}</div>
            <div v-if="results[c.id]" class="small res" :data-testid="`test-${c.name}`">
              <div v-for="s in results[c.id].steps" :key="s.name" class="row" style="gap: 6px"><Icon :name="s.ok ? 'check_circle' : 'cancel'" sm :class="s.ok ? 'okc' : 'errc'" /><b>{{ s.name }}</b><span class="muted">{{ s.message }}</span></div>
              <div v-if="results[c.id].host_key" class="errc">
                {{ results[c.id].host_key!.changed ? '主机密钥已变化，已拒绝连接；请在“编辑”中核实后清除已信任指纹。' : '首次连接需要核对主机指纹，请点击“编辑”并测试连接。' }}
              </div>
            </div>
            <div class="row wrap actions">
              <button class="btn sm has-icon" :disabled="!!busy[c.id]" @click="test(c)"><Icon :name="busy[c.id] === 'test' ? 'progress_activity' : 'network_check'" :class="{ spin: busy[c.id] === 'test' }" sm />测试</button>
              <button class="btn sm has-icon" @click="browsing = c"><Icon name="folder_open" sm />浏览</button>
              <button v-if="c.replicate !== 'off'" class="btn sm has-icon" :disabled="!!busy[c.id] || !c.enabled" @click="replicate(c)"><Icon name="sync" sm />同步缺失项</button>
              <span class="grow" />
              <button class="btn sm icon-only" :aria-label="`编辑 ${c.name}`" title="编辑" @click="dlg = { edit: c }"><Icon name="edit" sm /></button>
              <button class="btn sm icon-only danger" :aria-label="`删除 ${c.name}`" title="删除" @click="del = c"><Icon name="delete" sm /></button>
            </div>
          </article>
        </div>
      </StateView>
    </section>

    <!-- 同步记录 -->
    <section v-else data-testid="storage-reps" class="card flat">
      <div class="row wrap" style="padding: 12px 16px; border-bottom: 1px solid var(--md-sys-color-outline-variant)">
        <select v-model="repFilter.connection_id" class="input sm" aria-label="按连接筛选" style="max-width: 220px">
          <option value="">全部连接</option>
          <option v-for="c in conns" :key="c.id" :value="c.id">{{ c.name }}</option>
        </select>
        <select v-model="repFilter.status" class="input sm" aria-label="按状态筛选" style="max-width: 160px">
          <option value="">全部状态</option>
          <option v-for="(label, s) in REPLICATION_LABEL" :key="s" :value="s">{{ label }}</option>
        </select>
        <span class="grow" />
        <span v-if="hasActive" class="muted small"><Icon name="autorenew" sm class="spin" /> 每 4 秒自动刷新</span>
        <button class="btn sm has-icon" @click="loadReps()"><Icon name="refresh" sm />刷新</button>
      </div>
      <StateView :loading="repsLoading && !reps" :error="repsError" :empty="!!reps && !reps.items.length" empty-icon="sync_alt" empty-title="暂无同步记录" empty-hint="开启连接的同步策略后，归档成功的文件会出现在这里。" @retry="loadReps()">
        <div class="table-wrap">
          <table class="tbl">
            <thead><tr><th>连接</th><th>本机文件</th><th>远端路径</th><th>大小</th><th>状态</th><th>更新时间</th><th /></tr></thead>
            <tbody>
              <tr v-for="r in reps?.items" :key="r.id">
                <td class="nowrap">{{ r.connection_name }}</td>
                <td class="ellipsis" style="max-width: 260px" :title="r.local_path">{{ r.local_path }}</td>
                <td class="ellipsis mono" style="max-width: 260px" :title="r.remote_path">{{ r.remote_path }}</td>
                <td class="nowrap muted">{{ fmtBytes(r.size_bytes) }}</td>
                <td>
                  <span class="chip" :class="REPLICATION_TONE[r.status] === 'neutral' ? '' : REPLICATION_TONE[r.status]"><Icon v-if="isReplicationActive(r.status)" name="progress_activity" sm class="spin" />{{ REPLICATION_LABEL[r.status] }}</span>
                  <div v-if="r.error" class="small errc" style="max-width: 280px">{{ r.error }}</div>
                </td>
                <td class="nowrap muted">{{ fmtTime(r.updated_at) }}</td>
                <td><button v-if="r.status === 'failed'" class="btn sm has-icon" :aria-label="`重试 ${r.local_path}`" @click="retry(r)"><Icon name="replay" sm />重试</button></td>
              </tr>
            </tbody>
          </table>
        </div>
        <Pager v-if="reps" v-model:page="repPage" v-model:page-size="repSize" :total="reps.total" />
      </StateView>
    </section>
  </template>

  <StorageConnectionDialog v-if="dlg" :kinds="kinds" :connection="dlg.edit" @close="dlg = null" @saved="onSaved" />
  <RemoteBrowserDialog v-if="browsing" :connections="conns" :initial-id="browsing.id" :selectable="false" @close="browsing = null" />
  <Dialog v-if="del" title="删除存储连接" width="460px" @close="del = null">
    <p>确定删除连接“{{ del.name }}”吗？这只会移除本系统中的连接配置与同步记录，<b>不会删除远端文件</b>。</p>
    <template #actions>
      <button class="btn" @click="del = null">取消</button>
      <button class="btn filled danger-fill" :disabled="!!busy[del.id]" @click="doDelete">删除</button>
    </template>
  </Dialog>
</template>

<style scoped>
.cards { display: grid; grid-template-columns: repeat(auto-fill, minmax(340px, 1fr)); gap: 16px; }
.conn.off { opacity: .75; }
.kicon { font-size: 32px; color: var(--md-sys-color-primary); }
.small { font-size: 12px; }
.actions { margin-top: 4px; }
.res { background: var(--c-surface-3); border-radius: var(--shape-sm); padding: 8px 10px; display: flex; flex-direction: column; gap: 4px; }
.okc { color: var(--c-success, #2e7d32); } .errc { color: var(--md-sys-color-error); }
.center-note { align-items: center; text-align: center; padding: 56px 24px; }
.big { font-size: 48px; color: var(--md-sys-color-outline); }
.empty-extra { align-items: center; max-width: 560px; }
.msg { max-width: 520px; }
.danger-fill { background: var(--md-sys-color-error); color: var(--md-sys-color-on-error); }
</style>
