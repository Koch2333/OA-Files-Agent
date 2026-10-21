<script setup lang="ts">
import { ref } from 'vue'
import { api, type CorrectionProposal, type FileRecord } from '../api'
import { errMsg } from '../utils/useAsync'
import { useUi } from '../stores/ui'
import Drawer from './Drawer.vue'
import Icon from './Icon.vue'

const props = defineProps<{ file: FileRecord }>()
const emit = defineEmits<{ close: []; applied: [] }>()
const ui = useUi()
const text = ref('')
const busy = ref(false)
const err = ref<string | null>(null)
const proposal = ref<CorrectionProposal | null>(null)

async function propose() {
  if (!text.value.trim()) return
  busy.value = true; err.value = null
  try { proposal.value = await api.proposeCorrection(props.file.id, text.value.trim()) } catch (e) { err.value = errMsg(e) } finally { busy.value = false }
}
async function apply() {
  if (!proposal.value) return
  busy.value = true; err.value = null
  try { await api.approveCorrection(proposal.value.id); ui.toast('修正已应用', 'success'); emit('applied'); emit('close') } catch (e) { err.value = errMsg(e) } finally { busy.value = false }
}
</script>
<template>
  <Drawer title="纠错：自然语言修正" @close="emit('close')">
    <p class="muted">仅针对文件「{{ file.source_name }}」。系统先给出<b>修改前后对比</b>，你确认后才会应用；不会改动全局规则。</p>
    <div class="field">
      <label for="corr">描述需要如何修正</label>
      <textarea id="corr" v-model="text" class="input" placeholder="例如：这份文件应属于财务部，类型是报表，日期 2026-03-15" :disabled="!!proposal || busy" />
    </div>
    <div v-if="err" class="banner error" role="alert"><Icon name="error" />{{ err }}</div>
    <button v-if="!proposal" class="btn filled has-icon" :disabled="busy || !text.trim()" @click="propose"><Icon :name="busy ? 'progress_activity' : 'auto_fix'" :class="{ spin: busy }" sm />生成修改建议</button>
    <template v-else>
      <div class="card outlined">
        <div class="t-label muted" style="margin-bottom: 8px">修改前 → 修改后</div>
        <div v-for="c in proposal.changes" :key="c.field" class="chg row">
          <span class="lbl muted">{{ c.label }}</span>
          <span class="old">{{ c.before ?? '待审核' }}</span><Icon name="arrow_forward" sm /><b>{{ c.after ?? '待审核' }}</b>
        </div>
        <hr class="divider" style="margin: 12px 0" />
        <div class="row muted"><Icon name="info" sm />影响范围：{{ proposal.impact.affected_files }} 个文件{{ proposal.impact.rule_changes ? '，包含规则变更' : '，不修改全局规则' }}</div>
      </div>
      <div class="row">
        <button class="btn filled has-icon" :disabled="busy" @click="apply"><Icon name="check" sm />确认应用</button>
        <button class="btn" :disabled="busy" @click="proposal = null">重新描述</button>
      </div>
    </template>
  </Drawer>
</template>
<style scoped>.chg { padding: 6px 0; } .lbl { width: 64px; } .old { text-decoration: line-through; color: var(--md-sys-color-on-surface-variant); }</style>
