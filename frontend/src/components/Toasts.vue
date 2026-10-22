<script setup lang="ts">
import { useUi } from '../stores/ui'
import Icon from './Icon.vue'
const ui = useUi()
</script>
<template>
  <div class="toasts" aria-live="polite">
    <div v-for="t in ui.toasts" :key="t.id" class="toast" :class="t.kind" :role="t.kind === 'error' ? 'alert' : 'status'">
      <Icon :name="t.kind === 'error' ? 'error' : t.kind === 'success' ? 'check_circle' : 'info'" sm /><span class="grow">{{ t.text }}</span>
      <button v-if="t.action" class="tbtn" type="button" @click="t.action.run()">{{ t.action.label }}</button>
    </div>
  </div>
</template>
<style scoped>
.toasts { position: fixed; left: 50%; bottom: 24px; transform: translateX(-50%); display: flex; flex-direction: column; gap: 8px; z-index: 200; width: min(560px, calc(100vw - 32px)); }
.toast { display: flex; gap: 8px; align-items: center; padding: 14px 16px; border-radius: var(--shape-sm); background: var(--md-sys-color-inverse-surface); color: var(--md-sys-color-inverse-on-surface); box-shadow: var(--elev-3); animation: up var(--dur-m) var(--ease); }
.toast.error { background: var(--md-sys-color-error); color: var(--md-sys-color-on-error); }
.tbtn { flex: none; border: 1px solid currentColor; background: transparent; color: inherit; border-radius: var(--shape-full); height: 28px; padding: 0 12px; font: 500 12px var(--font); cursor: pointer; }
.tbtn:hover { background: color-mix(in srgb, currentColor 14%, transparent); }
@keyframes up { from { transform: translateY(12px); opacity: 0 } }
</style>
