<script setup lang="ts">
import type { Automation } from '../utils/automation'
import type { AutoGate } from '../utils/aiStatus'
import Icon from './Icon.vue'

const props = defineProps<{ mode: Automation; gate: AutoGate; disabled?: boolean }>()
const emit = defineEmits<{ select: [m: Automation] }>()
void props
</script>
<template>
  <div class="field">
    <span id="auto-lbl" class="lbl">处理模式</span>
    <div class="seg" role="group" aria-labelledby="auto-lbl">
      <button type="button" :aria-pressed="mode === 'review'" :disabled="disabled" data-testid="mode-review" @click="emit('select', 'review')"><Icon name="fact_check" sm />人工审核（推荐）</button>
      <button type="button" :aria-pressed="mode === 'auto'" :disabled="disabled || !gate.allowed" :aria-disabled="disabled || !gate.allowed" :title="gate.allowed ? '' : gate.text" data-testid="mode-auto" @click="emit('select', 'auto')"><Icon name="bolt" sm />全自动</button>
    </div>
    <p v-if="!gate.allowed && gate.reason !== 'loading'" class="muted hint" data-testid="auto-why"><Icon name="info" sm />全自动不可用：{{ gate.text }}</p>
    <p v-else-if="mode === 'review'" class="muted hint">模型给出归档计划后，由你逐项核对、批准并执行；不会自动改动文件。</p>
    <p v-else class="hint warn"><Icon name="warning" sm />全自动：模型直接决定并归档，无人工审批；拿不准的字段先用「其他」「未知日期」占位并照常归档，之后在台账里筛选修改；读不出内容或与已归档重复的文件仍留待人工处理。可事后一键撤销。</p>
  </div>
</template>
<style scoped>
.seg button:disabled { opacity: .45; cursor: not-allowed; }
.hint { font-size: 12px; display: flex; gap: 6px; align-items: flex-start; } .warn { color: var(--c-warn); }
</style>
