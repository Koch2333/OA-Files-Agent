<script setup lang="ts">
import { useAuth } from '../stores/auth'
import { computed, onMounted, reactive, ref } from 'vue'
import { api, ApiError, type ArchivePlan, type ArchivePlanItem } from '../api'
import { errMsg, useAsync } from '../utils/useAsync'
import { fmtTime } from '../utils/format'
import { useUi } from '../stores/ui'
import PageHeader from '../components/PageHeader.vue'
import StateView from '../components/StateView.vue'
import Icon from '../components/Icon.vue'
import Dialog from '../components/Dialog.vue'
import FilePreview from '../components/FilePreview.vue'

const auth = useAuth()
const props = defineProps<{ id: string }>()
const ui = useUi()
const plan = useAsync(() => api.getPlan(props.id))
const busy = ref<'' | 'approve' | 'execute' | 'save'>('')
const editing = ref<ArchivePlanItem | null>(null)
const previewing = ref<ArchivePlanItem | null>(null)
const form = reactive({ department: '', document_type: '', document_date: '', suggested_name: '', destination_path: '' })
const formErr = ref<string | null>(null)
const filter = ref<'all' | 'attention'>('all')
const confirmExec = ref(false)
const confirmRollback = ref(false)
// 同一页面内多次点击“执行”使用同一把幂等键；刷新页面后重新生成，后端据计划状态保证不重复执行
const idemKey = crypto.randomUUID()
onMounted(plan.run)

const p = computed<ArchivePlan | undefined>(() => plan.data.value)
const attention = computed(() => p.value?.items.filter((i) => i.review_required || i.warnings.length) ?? [])
const rows = computed(() => (filter.value === 'attention' ? attention.value : p.value?.items ?? []))
const blocking = computed(() => p.value?.items.filter((i) => i.review_required).length ?? 0)
const locked = computed(() => !!p.value && !['draft', 'awaiting_review'].includes(p.value.status))

function dirOf(path: string) { const i = path.lastIndexOf('/'); return i < 0 ? '' : path.slice(0, i + 1) }
function baseOf(path: string) { return path.slice(path.lastIndexOf('/') + 1) }

function edit(it: ArchivePlanItem) {
  editing.value = it; formErr.value = null
  Object.assign(form, { department: it.department ?? '', document_type: it.document_type ?? '', document_date: it.document_date ?? '', suggested_name: it.suggested_name, destination_path: it.destination_path })
}
async function handle(e: unknown) {
  ui.toast(errMsg(e), 'error')
  if (e instanceof ApiError && e.status === 409) await plan.run() // 版本冲突/状态变化：拉取最新
}
async function save() {
  if (!p.value || !editing.value) return
  if (form.document_date && !/^\d{4}-\d{2}-\d{2}$/.test(form.document_date)) { formErr.value = '日期格式应为 YYYY-MM-DD'; return }
  if (/(^|[\\/])\.\.([\\/]|$)/.test(form.destination_path) || /^([a-zA-Z]:|[\\/])/.test(form.destination_path)) { formErr.value = '目标路径必须是相对路径，且不能包含 ..'; return }
  busy.value = 'save'
  try {
    // 仅提交被修改的字段；未改动的名称/路径由后端按新字段重算
    const f = editing.value
    const patch: Record<string, string | null> = { file_id: f.file_id }
    if (form.department !== (f.department ?? '')) patch.department = form.department
    if (form.document_type !== (f.document_type ?? '')) patch.document_type = form.document_type
    if (form.document_date !== (f.document_date ?? '')) patch.document_date = form.document_date
    if (form.suggested_name !== f.suggested_name) patch.suggested_name = form.suggested_name
    if (form.destination_path !== f.destination_path) patch.destination_path = form.destination_path
    plan.data.value = await api.patchPlan(p.value.id, p.value.version, [patch as never])
    editing.value = null
    ui.toast('已保存人工修订', 'success')
  } catch (e) { await handle(e) } finally { busy.value = '' }
}
async function approve() {
  if (!p.value) return
  busy.value = 'approve'
  try { plan.data.value = await api.approvePlan(p.value.id, p.value.version); ui.toast('计划已批准；尚未执行，请确认后点击“执行归档”', 'success') } catch (e) { await handle(e) } finally { busy.value = '' }
}
async function rollback() {
  if (!p.value?.operation_id) return
  confirmRollback.value = false; busy.value = 'execute'
  try { await api.rollbackOperation(p.value.operation_id); ui.toast('已恢复：本次归档发布的文件已移除，原件未动', 'success'); await plan.run() } catch (e) { await handle(e) } finally { busy.value = '' }
}
async function execute() {
  if (!p.value) return
  confirmExec.value = false; busy.value = 'execute'
  try { plan.data.value = await api.executePlan(p.value.id, idemKey); ui.toast('归档完成，已写入台账', 'success') } catch (e) { await handle(e) } finally { busy.value = '' }
}
</script>

<template>
  <PageHeader title="归档计划与审批" subtitle="逐项核对原名/新名与原路径/目标路径。批准与执行是两个独立步骤；批准前不会改动任何文件。">
    <template #actions><RouterLink v-if="p" :to="`/tasks/${p.task_id}`" class="btn has-icon"><Icon name="arrow_back" sm />返回任务</RouterLink></template>
  </PageHeader>

  <StateView :loading="plan.loading.value && !p" :error="plan.error.value" :empty="false" @retry="plan.run">
    <template v-if="p">
      <section class="card bar">
        <div class="row wrap grow">
          <span class="chip" :class="{ attention: ['awaiting_review', 'approved'].includes(p.status), success: p.status === 'executed', progress: p.status === 'executing', error: p.status === 'failed' }">{{ { draft: '草稿', awaiting_review: '待审批', approved: '已批准，待执行', executing: '执行中', executed: '已执行', failed: '执行失败' }[p.status] }}</span>
          <span>共 <b>{{ p.items.length }}</b> 项</span>
          <span v-if="blocking" class="chip error"><Icon name="rule" sm />{{ blocking }} 项待人工确认</span>
          <span v-if="p.approved_at" class="muted">由 {{ p.approved_by ?? '—' }} 于 {{ fmtTime(p.approved_at) }} 批准</span>
          <span class="muted mono">v{{ p.version }}</span>
        </div>
        <div class="seg" role="group" aria-label="筛选"><button :aria-pressed="filter === 'all'" @click="filter = 'all'">全部</button><button :aria-pressed="filter === 'attention'" @click="filter = 'attention'">需关注 {{ attention.length }}</button></div>
        <button v-if="auth.canWrite" class="btn outlined has-icon" :disabled="p.status !== 'awaiting_review' && p.status !== 'draft' || !!busy || !!blocking" :title="blocking ? '请先处理待人工确认的条目' : ''" @click="approve"><Icon :name="busy === 'approve' ? 'progress_activity' : 'verified'" :class="{ spin: busy === 'approve' }" sm />批准计划</button>
        <button v-if="auth.canWrite && p.operation_id && api.capabilities.rollback" class="btn outlined danger has-icon" :disabled="!!busy" @click="confirmRollback = true"><Icon name="undo" sm />撤销本次归档</button>
        <button v-if="auth.canWrite" class="btn filled has-icon" :disabled="p.status !== 'approved' || !!busy" :title="p.status !== 'approved' ? '计划批准后才能执行' : ''" @click="confirmExec = true"><Icon :name="busy === 'execute' ? 'progress_activity' : 'inventory_2'" :class="{ spin: busy === 'execute' }" sm />执行归档</button>
      </section>

      <div v-if="blocking" class="banner" style="margin-bottom: 16px"><Icon name="rule" /><div>有 {{ blocking }} 项因置信度低或信息缺失被标为待审核。请点击“修订”补全，系统不会替你虚构字段。</div></div>
      <div v-if="p.status === 'executed'" class="banner info" style="margin-bottom: 16px"><Icon name="check_circle" /><div>归档已完成。<RouterLink to="/ledger">在台账中查看</RouterLink><template v-if="p.items.length"> · <RouterLink :to="{ path: '/manage', query: { path: dirOf(p.items[0].destination_path).replace(/\/$/, '') } }">在文件管理中查看</RouterLink></template></div></div>

      <div class="card flat">
        <StateView :empty="!rows.length" empty-title="没有符合筛选条件的条目" empty-icon="fact_check">
          <div class="table-wrap">
            <table class="tbl">
              <thead><tr><th>文件名（原 → 新）</th><th>路径（原 → 目标）</th><th>部门 / 类型 / 日期</th><th>提示</th><th /></tr></thead>
              <tbody>
                <tr v-for="it in rows" :key="it.file_id" :class="{ warnrow: it.review_required }">
                  <td class="diff"><div class="old"><Icon name="remove" sm />{{ it.original_name }}</div><div class="new"><Icon name="add" sm />{{ it.suggested_name }}</div></td>
                  <td class="diff mono"><div class="old"><Icon name="remove" sm /><span class="ellipsis">{{ dirOf(it.source_path) }}{{ baseOf(it.source_path) }}</span></div><div class="new"><Icon name="add" sm /><span class="ellipsis"><span class="muted">{{ dirOf(it.destination_path) }}</span>{{ baseOf(it.destination_path) }}</span></div></td>
                  <td class="nowrap"><div>{{ it.department ?? '—' }} · {{ it.document_type ?? '—' }}</div><div class="muted">{{ it.document_date ?? '日期待审核' }}</div></td>
                  <td><div class="col" style="gap: 4px"><span v-if="it.review_required" class="chip error">待人工确认</span><span v-for="(w, i) in it.warnings" :key="i" class="chip attention" style="height: auto; padding: 2px 10px; white-space: normal">{{ w }}</span></div></td>
                  <td class="nowrap"><button class="btn icon-only sm" :aria-label="'预览 ' + it.original_name" title="预览原件" data-testid="plan-preview" @click="previewing = it"><Icon name="visibility" sm /></button><button v-if="auth.canWrite" class="btn sm outlined" :disabled="locked" @click="edit(it)">修订</button></td>
                </tr>
              </tbody>
            </table>
          </div>
        </StateView>
      </div>
    </template>
  </StateView>

  <FilePreview v-if="previewing" :source="{ kind: 'file', id: previewing.file_id }" :name="previewing.original_name" @close="previewing = null" />
  <Dialog v-if="editing" title="人工修订" width="620px" @close="editing = null">
    <p class="muted">{{ editing.original_name }}</p>
    <div class="row wrap">
      <div class="field grow"><label for="e-d">所属部门</label><input id="e-d" v-model="form.department" class="input" /></div>
      <div class="field grow"><label for="e-t">文件类型</label><input id="e-t" v-model="form.document_type" class="input" /></div>
      <div class="field grow"><label for="e-dt">日期</label><input id="e-dt" v-model="form.document_date" type="date" class="input" min="1990-01-01" max="2100-12-31" /></div>
    </div>
    <div class="field"><label for="e-n">标准文件名</label><input id="e-n" v-model="form.suggested_name" class="input" /></div>
    <div class="field"><label for="e-p">目标路径（相对归档根目录）</label><input id="e-p" v-model="form.destination_path" class="input mono" :aria-invalid="!!formErr" /></div>
    <p class="muted">只修改本条；不会改变全局命名/归档规则。修改部门/类型/日期后若未手动改名与路径，将由后端重新计算。</p>
    <div v-if="formErr" class="banner error" role="alert"><Icon name="error" />{{ formErr }}</div>
    <template #actions><button class="btn" @click="editing = null">取消</button><button class="btn filled" :disabled="busy === 'save'" @click="save">保存修订</button></template>
  </Dialog>

  <Dialog v-if="confirmRollback" title="撤销本次归档？" @close="confirmRollback = false">
    <p>将移除本次归档发布的文件副本（仅当文件内容未被改动时）。导入时保存的原件不受影响，计划会回到“已批准”状态，台账记录同步移除。</p>
    <template #actions><button class="btn" @click="confirmRollback = false">取消</button><button class="btn filled" style="background: var(--md-sys-color-error); color: var(--md-sys-color-on-error)" @click="rollback">确认撤销</button></template>
  </Dialog>

  <Dialog v-if="confirmExec && p" title="确认执行归档？" @close="confirmExec = false">
    <p>将按已批准的计划对 <b>{{ p.items.length }}</b> 个文件重命名并移动到目标路径，并写入台账。原文件不会被覆盖；遇到重名将安全处理。</p>
    <template #actions><button class="btn" @click="confirmExec = false">再看看</button><button class="btn filled" autofocus @click="execute">确认执行</button></template>
  </Dialog>
</template>

<style scoped>
.bar { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; margin-bottom: 16px; padding: 14px 20px; }
.diff { min-width: 260px; max-width: 420px; } .diff > div { display: flex; gap: 6px; align-items: center; padding: 2px 6px; border-radius: 4px; min-width: 0; }
.old { background: color-mix(in srgb, var(--md-sys-color-error) 10%, transparent); text-decoration: line-through; text-decoration-color: color-mix(in srgb, var(--md-sys-color-error) 40%, transparent); color: var(--md-sys-color-on-surface-variant); }
.new { background: color-mix(in srgb, var(--c-success) 14%, transparent); margin-top: 3px; }
.warnrow { background: color-mix(in srgb, var(--c-warn-container) 35%, transparent); }
</style>
