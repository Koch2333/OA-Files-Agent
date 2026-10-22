<script setup lang="ts">
import type { LlmStatus } from '../api'
import Dialog from './Dialog.vue'
import Icon from './Icon.vue'

defineProps<{ status: LlmStatus; checked: boolean; remainingSec: number; canConfirm: boolean }>()
const emit = defineEmits<{ 'update:checked': [v: boolean]; confirm: []; cancel: [] }>()
</script>
<template>
  <Dialog title="启用全自动归档" alert width="600px" describedby="auto-risk-text" testid="auto-dialog" @close="emit('cancel')">
    <div class="warnhead" role="heading" aria-level="3"><Icon name="warning" class="wi" fill /><span>由大模型自动决策，可能出错</span></div>
    <p id="auto-risk-text" class="risk" data-testid="auto-ack-text">{{ status.auto_ack_text }}</p>
    <label class="row ack"><input type="checkbox" :checked="checked" data-testid="auto-ack-check" @change="emit('update:checked', ($event.target as HTMLInputElement).checked)" /><span>我已阅读并理解上述风险，由此产生的后果由我/本组织承担</span></label>
    <template #actions>
      <button class="btn" data-testid="auto-cancel" @click="emit('cancel')">取消，使用人工审核</button>
      <button class="btn filled warnbtn" :disabled="!canConfirm" data-testid="auto-confirm" @click="emit('confirm')">{{ remainingSec > 0 ? `确认启用（${remainingSec} 秒后可点）` : '确认启用全自动' }}</button>
    </template>
  </Dialog>
</template>
<style scoped>
.warnhead { display: flex; align-items: center; gap: 10px; padding: 12px 16px; border-radius: var(--shape-md); background: var(--md-sys-color-error-container); color: var(--md-sys-color-on-error-container); font-size: 18px; font-weight: 600; }
.wi { font-size: 28px; }
.risk { white-space: pre-wrap; line-height: 1.7; padding: 12px 16px; border: 1px solid var(--md-sys-color-outline-variant); border-radius: var(--shape-md); background: var(--c-surface-1); max-height: 40vh; overflow: auto; }
.ack { align-items: flex-start; cursor: pointer; } .ack input { margin-top: 3px; width: 18px; height: 18px; flex: none; }
.warnbtn:not(:disabled) { background: var(--md-sys-color-error); color: var(--md-sys-color-on-error); }
</style>
