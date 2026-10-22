<script setup lang="ts">
import { useAuth } from '../stores/auth'
import { onMounted, reactive, ref } from 'vue'
import { api, type OATemplate, type TemplateTestResult } from '../api'
import { errMsg, useAsync } from '../utils/useAsync'
import { useUi } from '../stores/ui'
import PageHeader from '../components/PageHeader.vue'
import StateView from '../components/StateView.vue'
import Dialog from '../components/Dialog.vue'
import Icon from '../components/Icon.vue'

const auth = useAuth()
const ui = useUi()
const tab = ref<'templates' | 'rules'>('templates')
const tpls = useAsync(() => api.listTemplates())
const rules = useAsync(() => api.listRules())
onMounted(() => { void tpls.run(); void rules.run() })

const blank = () => ({ name: '', enabled: true, priority: 100, fn: '', tx: '', department: '', document_type: '', date_source: 'ocr_text' as const })
const form = reactive(blank())
const editingId = ref<string | null>(null)
const open = ref(false)
const formErr = ref<string | null>(null)
const saving = ref(false)
const testing = ref<OATemplate | null>(null)
const testRes = ref<TemplateTestResult | null>(null)
const testErr = ref<string | null>(null)
const testBusy = ref(false)
const del = ref<OATemplate | null>(null)

const split = (s: string) => s.split(/[,，\n]/).map((x) => x.trim()).filter(Boolean)
function openNew() { Object.assign(form, blank()); editingId.value = null; formErr.value = null; open.value = true }
function openEdit(t: OATemplate) {
  Object.assign(form, { name: t.name, enabled: t.enabled, priority: t.priority, fn: t.match_rules.filename_keywords.join('，'), tx: t.match_rules.text_keywords.join('，'), department: t.field_mappings.department, document_type: t.field_mappings.document_type, date_source: t.field_mappings.date_source })
  editingId.value = t.id; formErr.value = null; open.value = true
}
async function save() {
  if (!form.name.trim()) { formErr.value = '请填写模板名称'; return }
  if (!split(form.fn).length && !split(form.tx).length) { formErr.value = '至少填写一个文件名关键词或正文关键词，否则模板不会匹配任何文件'; return }
  saving.value = true; formErr.value = null
  const body = { name: form.name.trim(), enabled: form.enabled, priority: Number(form.priority) || 100, match_rules: { filename_keywords: split(form.fn), text_keywords: split(form.tx) }, field_mappings: { department: form.department.trim(), document_type: form.document_type.trim(), date_source: form.date_source } }
  try { if (editingId.value) await api.updateTemplate(editingId.value, body); else await api.createTemplate(body); open.value = false; ui.toast('模板已保存', 'success'); await tpls.run() }
  catch (e) { formErr.value = errMsg(e) } finally { saving.value = false }
}
async function toggle(t: OATemplate) {
  try { await api.updateTemplate(t.id, { enabled: !t.enabled }); await tpls.run() } catch (e) { ui.toast(errMsg(e), 'error') }
}
async function remove() {
  if (!del.value) return
  try { await api.deleteTemplate(del.value.id); ui.toast('模板已删除', 'success'); del.value = null; await tpls.run() } catch (e) { ui.toast(errMsg(e), 'error') }
}
async function runTest(e: Event) {
  const f = (e.target as HTMLInputElement).files?.[0]
  if (!f || !testing.value) return
  testBusy.value = true; testRes.value = null; testErr.value = null
  try { testRes.value = await api.testTemplate(testing.value.id, f) } catch (x) { testErr.value = errMsg(x) } finally { testBusy.value = false }
}

const rf = reactive({ name: '', naming_pattern: '{date}_{department}_{type}_{original}', directory_pattern: '{department}/{year}/{type}', enabled: true })
const ruleOpen = ref(false)
const ruleErr = ref<string | null>(null)
async function saveRule() {
  if (!rf.name.trim() || !rf.naming_pattern.trim() || !rf.directory_pattern.trim()) { ruleErr.value = '名称、命名规则与目录规则均不能为空'; return }
  if (/\.\.|^[\\/]|^[a-zA-Z]:/.test(rf.directory_pattern)) { ruleErr.value = '目录规则必须是相对路径且不能含 ..'; return }
  try { await api.createRule({ ...rf, name: rf.name.trim() }); ruleOpen.value = false; ui.toast('规则已新增', 'success'); await rules.run() } catch (e) { ruleErr.value = errMsg(e) }
}
const TOKENS = ['{date}', '{year}', '{month}', '{department}', '{type}', '{original}']
</script>

<template>
  <PageHeader title="OA 模板与规则" subtitle="自定义企业 OA 模板决定如何识别文件；命名与归档规则决定新文件名和目标目录。">
    <template #actions>
      <div class="seg" role="tablist"><button role="tab" :aria-selected="tab === 'templates'" :aria-pressed="tab === 'templates'" @click="tab = 'templates'">OA 模板</button><button role="tab" :aria-selected="tab === 'rules'" :aria-pressed="tab === 'rules'" @click="tab = 'rules'">命名与归档规则</button></div>
      <button v-if="auth.canWrite && tab === 'templates'" class="btn filled has-icon" @click="openNew"><Icon name="add" sm />新建模板</button>
      <button v-else-if="auth.canWrite" class="btn filled has-icon" @click="ruleOpen = true; ruleErr = null"><Icon name="add" sm />新增规则</button>
    </template>
  </PageHeader>

  <div v-if="tab === 'templates'" class="card flat">
    <StateView :loading="tpls.loading.value && !tpls.data.value" :error="tpls.error.value" :empty="!tpls.data.value?.length" empty-title="还没有 OA 模板" empty-icon="rule_settings" empty-hint="新建模板后，导入时可指定使用或自动按优先级匹配。" @retry="tpls.run">
      <ul class="list">
        <li v-for="t in tpls.data.value" :key="t.id">
          <div class="grow"><div class="t-title-m">{{ t.name }} <span class="chip" style="margin-left: 8px">优先级 {{ t.priority }}</span></div>
            <div class="muted">文件名关键词：{{ t.match_rules.filename_keywords.join('、') || '—' }}　正文关键词：{{ t.match_rules.text_keywords.join('、') || '—' }}</div>
            <div class="muted">映射：部门←{{ t.field_mappings.department || '—' }}，类型←{{ t.field_mappings.document_type || '—' }}，日期来源：{{ { ocr_text: 'OCR/正文', filename: '文件名', file_metadata: '文件属性' }[t.field_mappings.date_source] }}</div></div>
          <label class="switch" :title="t.enabled ? '点击停用' : '点击启用'"><input type="checkbox" role="switch" :disabled="!auth.canWrite" :checked="t.enabled" :aria-label="`启用 ${t.name}`" @change="toggle(t)" /><span>{{ t.enabled ? '启用' : '停用' }}</span></label>
          <button v-if="auth.canWrite" class="btn sm tonal has-icon" @click="testing = t; testRes = null; testErr = null"><Icon name="science" sm />测试</button>
          <button v-if="auth.canWrite" class="btn icon-only sm" :aria-label="`编辑 ${t.name}`" @click="openEdit(t)"><Icon name="edit" sm /></button>
          <button v-if="auth.canWrite" class="btn icon-only sm danger" :aria-label="`删除 ${t.name}`" @click="del = t"><Icon name="delete" sm /></button>
        </li>
      </ul>
    </StateView>
  </div>

  <div v-else class="card flat">
    <StateView :loading="rules.loading.value && !rules.data.value" :error="rules.error.value" :empty="!rules.data.value?.length" empty-title="没有归档规则" empty-icon="drive_file_rename_outline" @retry="rules.run">
      <ul class="list"><li v-for="r in rules.data.value" :key="r.id"><div class="grow"><div class="t-title-m">{{ r.name }}</div><div class="mono muted">命名：{{ r.naming_pattern }}</div><div class="mono muted">目录：{{ r.directory_pattern }}</div></div><span class="chip" :class="r.enabled ? 'success' : ''">{{ r.enabled ? '启用' : '停用' }}</span></li></ul>
    </StateView>
  </div>

  <Dialog v-if="open" :title="editingId ? '编辑 OA 模板' : '新建 OA 模板'" width="640px" @close="open = false">
    <div class="row wrap"><div class="field grow"><label for="t-n">模板名称</label><input id="t-n" v-model="form.name" class="input" /></div><div class="field" style="width: 120px"><label for="t-p">优先级（小优先）</label><input id="t-p" v-model.number="form.priority" type="number" class="input" /></div></div>
    <div class="field"><label for="t-f">文件名关键词（逗号分隔）</label><input id="t-f" v-model="form.fn" class="input" placeholder="通知，合同，报表" /></div>
    <div class="field"><label for="t-x">正文 / OCR 关键词（逗号分隔）</label><input id="t-x" v-model="form.tx" class="input" placeholder="文号，签发" /></div>
    <div class="row wrap"><div class="field grow"><label for="t-d">部门字段对应</label><input id="t-d" v-model="form.department" class="input" placeholder="发文部门" /></div><div class="field grow"><label for="t-t">类型字段对应</label><input id="t-t" v-model="form.document_type" class="input" placeholder="文种" /></div>
      <div class="field grow"><label for="t-ds">日期来源</label><select id="t-ds" v-model="form.date_source" class="input"><option value="ocr_text">OCR / 正文</option><option value="filename">文件名</option><option value="file_metadata">文件属性</option></select></div></div>
    <label class="switch"><input v-model="form.enabled" type="checkbox" role="switch" />启用此模板</label>
    <div v-if="formErr" class="banner error" role="alert"><Icon name="error" />{{ formErr }}</div>
    <template #actions><button class="btn" @click="open = false">取消</button><button class="btn filled" :disabled="saving" @click="save">保存</button></template>
  </Dialog>

  <Dialog v-if="testing" :title="`测试模板：${testing.name}`" @close="testing = null">
    <p class="muted">选择一个样本文件，查看该模板是否命中以及提取到的字段。样本仅用于测试，不会被归档。</p>
    <label class="btn tonal has-icon" style="align-self: flex-start"><Icon :name="testBusy ? 'progress_activity' : 'upload_file'" :class="{ spin: testBusy }" sm />选择样本文件<input type="file" hidden :disabled="testBusy" @change="runTest" /></label>
    <div v-if="testErr" class="banner error" role="alert"><Icon name="error" />{{ testErr }}</div>
    <div v-if="testRes" class="card outlined col">
      <span class="chip" :class="testRes.matched ? 'success' : 'attention'" style="align-self: flex-start">{{ testRes.matched ? '命中模板' : '未命中' }}</span>
      <div>部门：{{ testRes.department ?? '待审核' }}　类型：{{ testRes.document_type ?? '待审核' }}　日期：{{ testRes.document_date ?? '待审核' }}</div>
      <ul style="margin: 0; padding-left: 18px"><li v-for="(e, i) in testRes.evidence" :key="i">{{ e }}</li></ul>
    </div>
    <template #actions><button class="btn" @click="testing = null">关闭</button></template>
  </Dialog>

  <Dialog v-if="del" title="删除模板？" @close="del = null"><p>将删除「{{ del.name }}」。已归档的文件不受影响。</p><template #actions><button class="btn" @click="del = null">取消</button><button class="btn filled" style="background: var(--md-sys-color-error); color: var(--md-sys-color-on-error)" @click="remove">删除</button></template></Dialog>

  <Dialog v-if="ruleOpen" title="新增命名与归档规则" width="620px" @close="ruleOpen = false">
    <div class="field"><label for="r-n">规则名称</label><input id="r-n" v-model="rf.name" class="input" /></div>
    <div class="field"><label for="r-p">文件命名规则</label><input id="r-p" v-model="rf.naming_pattern" class="input mono" /></div>
    <div class="field"><label for="r-d">归档目录规则（相对路径）</label><input id="r-d" v-model="rf.directory_pattern" class="input mono" /></div>
    <p class="muted">可用占位符：<code v-for="t in TOKENS" :key="t" style="margin-right: 6px">{{ t }}</code></p>
    <label class="switch"><input v-model="rf.enabled" type="checkbox" role="switch" />启用</label>
    <div v-if="ruleErr" class="banner error" role="alert"><Icon name="error" />{{ ruleErr }}</div>
    <template #actions><button class="btn" @click="ruleOpen = false">取消</button><button class="btn filled" @click="saveRule">保存</button></template>
  </Dialog>
</template>

<style scoped>.list { list-style: none; margin: 0; padding: 8px; } .list li { display: flex; align-items: center; gap: 12px; padding: 14px 16px; border-bottom: 1px solid var(--md-sys-color-outline-variant); flex-wrap: wrap; } .list li:last-child { border: 0; }</style>
