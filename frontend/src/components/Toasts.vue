<script setup lang="ts">
import { useUi } from '../stores/ui'
import Icon from './Icon.vue'
const ui = useUi()
</script>
<template>
  <div class="toasts" aria-live="polite">
    <div v-for="t in ui.toasts" :key="t.id" class="toast" :class="t.kind" :role="t.kind === 'error' ? 'alert' : 'status'">
      <Icon :name="t.kind === 'error' ? 'error' : t.kind === 'success' ? 'check_circle' : 'info'" sm />{{ t.text }}
    </div>
  </div>
</template>
<style scoped>
.toasts { position: fixed; left: 50%; bottom: 24px; transform: translateX(-50%); display: flex; flex-direction: column; gap: 8px; z-index: 200; width: min(560px, calc(100vw - 32px)); }
.toast { display: flex; gap: 8px; align-items: center; padding: 14px 16px; border-radius: var(--shape-sm); background: var(--md-sys-color-inverse-surface); color: var(--md-sys-color-inverse-on-surface); box-shadow: var(--elev-3); animation: up var(--dur-m) var(--ease); }
.toast.error { background: var(--md-sys-color-error); color: var(--md-sys-color-on-error); }
@keyframes up { from { transform: translateY(12px); opacity: 0 } }
</style>
