<script setup lang="ts">
import { uuid } from '../utils/uuid'
import { useAuth } from '../stores/auth'
import { computed, nextTick, onMounted, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { api, ApiError, type ArchivePlan, type ArchivePlanItem, type ClassificationOptions } from '../api'
import { errMsg, useAsync } from '../utils/useAsync'
import { fmtTime } from '../utils/format'
import { useUi } from '../stores/ui'
import PageHeader from '../components/PageHeader.vue'
import StateView from '../components/StateView.vue'
import Icon from '../components/Icon.vue'
import Dialog from '../components/Dialog.vue'
import FilePreview from '../components/FilePreview.vue'
import ContentBadge from '../components/ContentBadge.vue'
import RetryNotice from '../components/RetryNotice.vue'
import ComboField from '../components/ComboField.vue'
import StepButtons from '../components/StepButtons.vue'
import { confirmPatches, fallbackItems, fallbackLabel, OTHER, validateCategory } from '../utils/classification'
import { coachSeen, markCoachSeen, planStepView } from '../utils/planSteps'
import { summarizeLlmFailures, type LlmTaskFailure } from '../utils/llmError'

const auth = useAuth()
const props = defineProps<{ id: string }>()
const ui = useUi()
const router = useRouter()
const plan = useAsync(() => api.getPlan(props.id))
const busy = ref<'' | 'approve' | 'execute' | 'save'>('')
const editing = ref<ArchivePlanItem | null>(null)
const previewing = ref<ArchivePlanItem | null>(null)
const form = reactive({ department: '', document_type: '', document_date: '', suggested_name: '', destination_path: '' })
const formErr = ref<string | null>(null)
const filter = ref<'all' | 'attention'>('all')
const confirmExec = ref(false)
const confirmFallback = ref(false)
/** 部门/类型留空保存时，先问“要不要归入「其他」” */
const askOther = ref(false)
// 候选值：进入页面时取一次（含本任务计划里的值），保存后刷新
const opts = ref<ClassificationOptions | null>(null)
const optsLoading = ref(false)
async function loadOpts() {
  optsLoading.value = true
  try { opts.value = await api.getClassificationOptions(plan.data.value?.task_id) } catch { /* 候选只是辅助，失败时仍可手工输入 */ } finally { optsLoading.value = false }
}
// 首次打开待批准计划时的一次性步骤提示
const coach = ref(false)
const pulse = ref(false)
const confirmRollback = ref(false)
// 同一页面内多次点击“执行”使用同一把幂等键；刷新页面后重新生成，后端据计划状态保证不重复执行
const idemKey = uuid()
// 未进入计划的文件里，因大模型 HTTP 错误失败的：在计划页给出原因与一键重试（只重新识别这些文件）
const llmFail = ref<LlmTaskFailure | null>(null)
const taskUpdated = ref<string | undefined>()
const retrying = ref(false)
async function loadLlmFail() {
  if (!plan.data.value) return
  try {
    const [files, task] = await Promise.all([api.listTaskFiles(plan.data.value.task_id), api.getTask(plan.data.value.task_id)])
    llmFail.value = summarizeLlmFailures(files); taskUpdated.value = task.updated_at
  } catch { llmFail.value = null }
}
async function retryLlm() {
  if (!p.value || retrying.value) return
  retrying.value = true
  try { await api.retryTask(p.value.task_id); ui.toast('已重新排队：只重新识别失败的文件，计划会重新生成', 'success'); llmFail.value = null } catch (e) { ui.toast(errMsg(e), 'error') } finally { retrying.value = false }
}
onMounted(async () => {
  await plan.run(); void loadLlmFail(); void loadOpts()
  if (plan.data.value && ['draft', 'awaiting_review'].includes(plan.data.value.status) && auth.canWrite && !coachSeen()) coach.value = true
})
function dismissCoach() { coach.value = false; markCoachSeen() }

const p = computed<ArchivePlan | undefined>(() => plan.data.value)
const attention = computed(() => p.value?.items.filter((i) => i.review_required || i.warnings.length) ?? [])
const rows = computed(() => (filter.value === 'attention' ? attention.value : p.value?.items ?? []))
const blocking = computed(() => p.value?.items.filter((i) => i.review_required).length ?? 0)
const locked = computed(() => !!p.value && !['draft', 'awaiting_review'].includes(p.value.status))
const steps = computed(() => planStepView(p.value, busy.value))
const fallbackCount = computed(() => fallbackItems(p.value?.items ?? []).length)
const deptErr = computed(() => (form.department ? validateCategory(form.department) : null))
const typeErr = computed(() => (form.document_type ? validateCategory(form.document_type) : null))
/** 跳到第一项待确认条目：滚动到该行并把焦点放在它的“修订”按钮上 */
async function jumpFirst() {
  const it = (p.value?.items ?? []).find((i) => i.review_required)
  if (!it) return
  filter.value = 'all'
  await nextTick()
  const row = document.getElementById('plan-row-' + it.file_id)
  row?.scrollIntoView?.({ block: 'center' })
  ;(row?.querySelector('button.edit') as HTMLElement | null)?.focus()
}
const isFb = (it: ArchivePlanItem, f: 'department' | 'document_type') => (it.fallback_fields ?? []).includes(f)

function dirOf(path: string) { const i = path.lastIndexOf('/'); return i < 0 ? '' : path.slice(0, i + 1) }
function baseOf(path: string) { return path.slice(path.lastIndexOf('/') + 1) }

function edit(it: ArchivePlanItem) {
  editing.value = it; formErr.value = null; askOther.value = false
  Object.assign(form, { department: it.department ?? '', document_type: it.document_type ?? '', document_date: it.document_date ?? '', suggested_name: it.suggested_name, destination_path: it.destination_path })
}
async function handle(e: unknown) {
  ui.toast(errMsg(e), 'error')
  if (e instanceof ApiError && e.status === 409) await plan.run() // 版本冲突/状态变化：拉取最新
}
/** 暂填的「其他」作为表单初值时，保存即视为确认；其余字段照旧 */
function save(): Promise<void> | void {
  if (!editing.value) return
  if (deptErr.value || typeErr.value) { formErr.value = '请先修正部门/类型中的问题'; return }
  if (!form.department.trim() || !form.document_type.trim()) { askOther.value = true; return }
  return doSave()
}
function saveWithOther() {
  if (!form.department.trim()) form.department = OTHER
  if (!form.document_type.trim()) form.document_type = OTHER
  askOther.value = false
  return doSave()
}
async function doSave() {
  askOther.value = false
  if (!p.value || !editing.value) return
  if (form.document_date && !/^\d{4}-\d{2}-\d{2}$/.test(form.document_date)) { formErr.value = '日期格式应为 YYYY-MM-DD'; return }
  if (/(^|[\\/])\.\.([\\/]|$)/.test(form.destination_path) || /^([a-zA-Z]:|[\\/])/.test(form.destination_path)) { formErr.value = '目标路径必须是相对路径，且不能包含 ..'; return }
  busy.value = 'save'
  try {
    // 仅提交被修改的字段；未改动的名称/路径由后端按新字段重算
    const f = editing.value
    const patch: Record<string, string | null> = { file_id: f.file_id }
    // 仍是系统暂填的字段：保存即视为确认，即使值没变也提交（后端据此移出 fallback_fields）
    if (form.department !== (f.department ?? '') || isFb(f, 'department')) patch.department = form.department
    if (form.document_type !== (f.document_type ?? '') || isFb(f, 'document_type')) patch.document_type = form.document_type
    if (form.document_date !== (f.document_date ?? '')) patch.document_date = form.document_date
    if (form.suggested_name !== f.suggested_name) patch.suggested_name = form.suggested_name
    if (form.destination_path !== f.destination_path) patch.destination_path = form.destination_path
    plan.data.value = await api.patchPlan(p.value.id, p.value.version, [patch as never])
    editing.value = null
    ui.toast('已保存人工修订', 'success')
    void loadOpts()
  } catch (e) {
    if (e instanceof ApiError && e.status === 422) formErr.value = errMsg(e) // 与后端同一规则的校验错误：显示在对话框内，便于直接改
    else await handle(e)
  } finally { busy.value = '' }
}
/** 确认暂填的「其他」：把仍是占位的字段原样提交（显式提交即确认），不改动其它字段 */
async function confirmOne(it: ArchivePlanItem) { await confirmFallbackItems([it]) }
async function confirmAllFallback() { confirmFallback.value = false; await confirmFallbackItems(p.value?.items ?? []) }
async function confirmFallbackItems(items: ArchivePlanItem[]) {
  if (!p.value) return
  const patches = confirmPatches(items)
  if (!patches.length) return
  busy.value = 'save'
  try {
    let cur = p.value
    for (let i = 0; i < patches.length; i += 500) cur = await api.patchPlan(cur.id, cur.version, patches.slice(i, i + 500) as never)
    plan.data.value = cur
    ui.toast(`已确认 ${patches.length} 项归入「${OTHER}」`, 'success')
  } catch (e) { await handle(e) } finally { busy.value = '' }
}
async function approve() {
  if (!p.value) return
  busy.value = 'approve'
  try {
    plan.data.value = await api.approvePlan(p.value.id, p.value.version)
    dismissCoach()
    // 不自动执行：执行是单独、有意的一步（带幂等键）；提示下一步并让执行按钮短暂提示
    ui.toast('计划已批准。下一步：点击「执行归档」', 'success', { label: '执行归档', run: () => { confirmExec.value = true } })
    pulse.value = true; setTimeout(() => { pulse.value = false }, 4000)
  } catch (e) { await handle(e) } finally { busy.value = '' }
}
async function rollback() {
  if (!p.value?.operation_id) return
  confirmRollback.value = false; busy.value = 'execute'
  try { await api.rollbackOperation(p.value.operation_id); ui.toast('已恢复：本次归档发布的文件已移除，原件未动', 'success'); await plan.run() } catch (e) { await handle(e) } finally { busy.value = '' }
}
async function execute() {
  if (!p.value) return
  confirmExec.value = false; busy.value = 'execute'
  try { plan.data.value = await api.executePlan(p.value.id, idemKey); ui.toast('归档完成，已写入台账', 'success', { label: '查看台账', run: () => { void router.push('/ledger') } }) } catch (e) { await handle(e) } finally { busy.value = '' }
}
</script>

<template>
  <PageHeader title="归档计划与审批" subtitle="逐项核对原名/新名与原路径/目标路径。批准与执行是两个独立步骤；批准前不会改动任何文件。">
    <template #actions><RouterLink v-if="p" :to="`/tasks/${p.task_id}`" class="btn has-icon"><Icon name="arrow_back" sm />返回任务</RouterLink></template>
  </PageHeader>

  <StateView :loading="plan.loading.value && !p" :error="plan.error.value" :empty="false" @retry="plan.run">
    <template v-if="p">
      <RetryNotice v-if="llmFail && !locked" class="llmn" :view="llmFail.view" :busy="retrying" :readonly="!auth.canWrite" :since="taskUpdated" :title="`${llmFail.count} 个文件因大模型错误未识别，不在本计划中`" @retry="retryLlm" />
      <section class="card bar">
        <div class="row wrap grow">
          <span class="chip" :class="{ attention: ['awaiting_review', 'approved'].includes(p.status), success: p.status === 'executed', progress: p.status === 'executing', error: p.status === 'failed' }">{{ { draft: '草稿', awaiting_review: '待审批', approved: '已批准，待执行', executing: '执行中', executed: '已执行', failed: '执行失败' }[p.status] }}</span>
          <span>共 <b>{{ p.items.length }}</b> 项</span>
          <span v-if="blocking" class="chip error"><Icon name="rule" sm />{{ blocking }} 项待人工确认</span>
          <span v-if="p.approved_at" class="muted">由 {{ p.approved_by ?? '—' }} 于 {{ fmtTime(p.approved_at) }} 批准</span>
          <span class="muted mono">v{{ p.version }}</span>
        </div>
        <div class="seg" role="group" aria-label="筛选"><button :aria-pressed="filter === 'all'" @click="filter = 'all'">全部</button><button :aria-pressed="filter === 'attention'" @click="filter = 'attention'">需关注 {{ attention.length }}</button></div>
        <button v-if="auth.canWrite && p.operation_id && api.capabilities.rollback" class="btn outlined danger has-icon" :disabled="!!busy" @click="confirmRollback = true"><Icon name="undo" sm />撤销本次归档</button>
        <StepButtons v-if="auth.canWrite" :view="steps" :pulse-execute="pulse" :coach="coach" @approve="approve" @execute="confirmExec = true" @jump-first="jumpFirst" @confirm-fallback="confirmFallback = true" @coach-dismiss="dismissCoach" />
      </section>

      <div v-if="blocking" class="banner" style="margin-bottom: 16px"><Icon name="rule" /><div>有 {{ blocking }} 项待人工确认（置信度低或信息缺失）。请点击“修订”补全，系统不会替你虚构字段。<template v-if="fallbackCount">其中 {{ fallbackCount }} 项的部门/类型无法识别，已暂填「{{ OTHER }}」（系统占位，非 AI 判断），请逐项确认或修改。</template></div><button v-if="fallbackCount && auth.canWrite && !locked" class="btn sm tonal" :disabled="!!busy" data-testid="plan-confirm-all" @click="confirmFallback = true">确认全部暂填的「{{ OTHER }}」</button></div>
      <div v-if="p.status === 'executed'" class="banner info" style="margin-bottom: 16px"><Icon name="check_circle" /><div>归档已完成。<RouterLink to="/ledger">在台账中查看</RouterLink><template v-if="p.items.length"> · <RouterLink :to="{ path: '/manage', query: { path: dirOf(p.items[0].destination_path).replace(/\/$/, '') } }">在文件管理中查看</RouterLink></template></div></div>

      <div class="card flat">
        <StateView :empty="!rows.length" empty-title="没有符合筛选条件的条目" empty-icon="fact_check">
          <div class="table-wrap">
            <table class="tbl">
              <thead><tr><th>文件名（原 → 新）</th><th>路径（原 → 目标）</th><th>部门 / 类型 / 日期</th><th>提示</th><th /></tr></thead>
              <tbody>
                <tr v-for="it in rows" :id="'plan-row-' + it.file_id" :key="it.file_id" :class="{ warnrow: it.review_required }">
                  <td class="diff"><div class="old"><Icon name="remove" sm />{{ it.original_name }}</div><div class="new"><Icon name="add" sm />{{ it.suggested_name }}</div></td>
                  <td class="diff mono"><div class="old"><Icon name="remove" sm /><span class="ellipsis">{{ dirOf(it.source_path) }}{{ baseOf(it.source_path) }}</span></div><div class="new"><Icon name="add" sm /><span class="ellipsis"><span class="muted">{{ dirOf(it.destination_path) }}</span>{{ baseOf(it.destination_path) }}</span></div></td>
                  <td class="nowrap"><div><span v-if="isFb(it, 'department')" class="chip attention" title="系统暂填，待确认">{{ OTHER }} · 待确认</span><template v-else>{{ it.department ?? '—' }}</template> · <span v-if="isFb(it, 'document_type')" class="chip attention" title="系统暂填，待确认">{{ OTHER }} · 待确认</span><template v-else>{{ it.document_type ?? '—' }}</template></div><div class="muted">{{ it.document_date ?? '日期待审核' }}</div></td>
                  <td><div class="col" style="gap: 4px"><span v-if="it.review_required" class="chip error">待人工确认</span><ContentBadge :basis="it.basis" :note="it.basis_note" /><span v-for="(w, i) in it.warnings" :key="i" class="chip attention" style="height: auto; padding: 2px 10px; white-space: normal">{{ w }}</span></div></td>
                  <td class="nowrap"><button class="btn icon-only sm" :aria-label="'预览 ' + it.original_name" title="预览原件" data-testid="plan-preview" @click="previewing = it"><Icon name="visibility" sm /></button><button v-if="auth.canWrite && (it.fallback_fields ?? []).length && !locked" class="btn sm tonal" :disabled="!!busy" :title="`确认${fallbackLabel(it)}归入「${OTHER}」`" data-testid="plan-confirm-one" @click="confirmOne(it)">确认「{{ OTHER }}」</button><button v-if="auth.canWrite" class="btn sm outlined edit" :disabled="locked" @click="edit(it)">修订</button></td>
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
      <div class="field grow"><label for="e-d">所属部门</label><ComboField id="e-d" v-model="form.department" :options="opts?.departments ?? []" :loading="optsLoading" placeholder="选择或直接输入" :error="deptErr" /></div>
      <div class="field grow"><label for="e-t">文件类型</label><ComboField id="e-t" v-model="form.document_type" :options="opts?.types ?? []" :loading="optsLoading" placeholder="选择或直接输入" :error="typeErr" /></div>
      <div class="field grow"><label for="e-dt">日期</label><input id="e-dt" v-model="form.document_date" type="date" class="input" min="1990-01-01" max="2100-12-31" /></div>
    </div>
    <div class="field"><label for="e-n">标准文件名</label><input id="e-n" v-model="form.suggested_name" class="input" /></div>
    <div class="field"><label for="e-p">目标路径（相对归档根目录）</label><input id="e-p" v-model="form.destination_path" class="input mono" :aria-invalid="!!formErr" /></div>
    <p class="muted">只修改本条；不会改变全局命名/归档规则。修改部门/类型/日期后若未手动改名与路径，将由后端重新计算。</p>
    <div v-if="formErr" class="banner error" role="alert"><Icon name="error" />{{ formErr }}</div>
    <div v-if="askOther" class="banner" role="alert" data-testid="plan-ask-other"><Icon name="rule" /><div>部门或类型为空。要把空着的项归入「{{ OTHER }}」吗？</div></div>
    <template #actions>
      <template v-if="askOther"><button class="btn" @click="askOther = false">返回填写</button><button class="btn" :disabled="busy === 'save'" @click="doSave">仍留空，待确认</button><button class="btn filled" :disabled="busy === 'save'" data-testid="plan-save-other" @click="saveWithOther">归入「{{ OTHER }}」并保存</button></template>
      <template v-else><button class="btn" @click="editing = null">取消</button><button class="btn filled" :disabled="busy === 'save'" data-testid="plan-save" @click="save">保存修订</button></template>
    </template>
  </Dialog>

  <Dialog v-if="confirmFallback" title="确认暂填的「其他」？" @close="confirmFallback = false">
    <p>有 <b>{{ fallbackCount }}</b> 个文件的部门/类型无法识别，系统已暂填「{{ OTHER }}」。确认后它们将按「{{ OTHER }}」归档（只确认这些暂填字段，不改动其它内容）；仍需你逐项核对时请先取消并点“修订”。</p>
    <template #actions><button class="btn" @click="confirmFallback = false">取消</button><button class="btn filled" autofocus data-testid="plan-confirm-all-ok" @click="confirmAllFallback">确认 {{ fallbackCount }} 项归入「{{ OTHER }}」</button></template>
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
.llmn { margin-bottom: 16px; }
.bar { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; margin-bottom: 16px; padding: 14px 20px; }
.diff { min-width: 260px; max-width: 420px; } .diff > div { display: flex; gap: 6px; align-items: center; padding: 2px 6px; border-radius: 4px; min-width: 0; }
.old { background: color-mix(in srgb, var(--md-sys-color-error) 10%, transparent); text-decoration: line-through; text-decoration-color: color-mix(in srgb, var(--md-sys-color-error) 40%, transparent); color: var(--md-sys-color-on-surface-variant); }
.new { background: color-mix(in srgb, var(--c-success) 14%, transparent); margin-top: 3px; }
.warnrow { background: color-mix(in srgb, var(--c-warn-container) 35%, transparent); }
</style>
