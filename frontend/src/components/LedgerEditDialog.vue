<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { api, ApiError, type LedgerEntry } from '../api'
import type { CategoryOption } from '../utils/classification'
import { OTHER, validateCategory } from '../utils/classification'
import { errMsg } from '../utils/errorInfo'
import { useUi } from '../stores/ui'
import ComboField from './ComboField.vue'
import Dialog from './Dialog.vue'

/** 归档后直接修订：部门 / 类型 / 日期（日期可留空）。保存后后端按新值重算标准文件名与目录并移动文件，台账同步。 */
const props = defineProps<{ entry: LedgerEntry }>()
const emit = defineEmits<{ close: []; saved: [entry: LedgerEntry] }>()
const ui = useUi()
const form = ref({ department: props.entry.department, document_type: props.entry.document_type, document_date: props.entry.document_date })
const opts = ref<{ departments: CategoryOption[]; types: CategoryOption[] } | null>(null)
const busy = ref(false)
const err = ref<string | null>(null)
const deptErr = computed(() => validateCategory(form.value.department))
const typeErr = computed(() => validateCategory(form.value.document_type))
const changed = computed(() => form.value.department !== props.entry.department || form.value.document_type !== props.entry.document_type || form.value.document_date !== props.entry.document_date)
onMounted(async () => { try { const o = await api.getClassificationOptions(); opts.value = { departments: o.departments, types: o.types } } catch { /* 仍可手输 */ } })
async function save() {
  if (!changed.value || deptErr.value || typeErr.value) return
  busy.value = true; err.value = null
  try {
    // 部门/类型留空 = 归入「其他」；日期留空 = 清除（目录与文件名使用「未知日期」）
    const e = await api.reclassifyLedger(props.entry.id, {
      department: form.value.department.trim() || OTHER, document_type: form.value.document_type.trim() || OTHER, document_date: form.value.document_date,
    })
    ui.toast('已修订，文件已按新分类移动', 'success')
    emit('saved', e)
  } catch (e) { err.value = e instanceof ApiError && e.status === 422 ? e.message : errMsg(e) } finally { busy.value = false }
}
</script>
<template>
  <Dialog title="修订已归档文件" width="560px" @close="!busy && emit('close')">
    <p class="muted" style="word-break: break-all">{{ entry.canonical_name }}</p>
    <div class="row wrap">
      <div class="field grow"><label for="le-d">所属部门</label><ComboField id="le-d" v-model="form.department" :options="opts?.departments ?? []" placeholder="选择或直接输入" :error="deptErr" /></div>
      <div class="field grow"><label for="le-t">文件类型</label><ComboField id="le-t" v-model="form.document_type" :options="opts?.types ?? []" placeholder="选择或直接输入" :error="typeErr" /></div>
      <div class="field grow"><label for="le-dt">日期（可留空）</label><input id="le-dt" v-model="form.document_date" type="date" class="input" min="1990-01-01" max="2100-12-31" /></div>
    </div>
    <p class="muted">保存后文件会按新的部门/类型/日期移动到新目录并重新命名（不会覆盖已有文件），台账同步更新。没有日期的文件可以留空。</p>
    <div v-if="err" class="banner error" role="alert">{{ err }}</div>
    <template #actions>
      <button class="btn" :disabled="busy" @click="emit('close')">取消</button>
      <button class="btn filled" :disabled="busy || !changed || !!deptErr || !!typeErr" data-testid="ledger-edit-save" @click="save">保存修订</button>
    </template>
  </Dialog>
</template>
