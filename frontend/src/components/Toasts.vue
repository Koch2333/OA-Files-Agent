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
/* snackbar: inverse surface, 4dp corners, min 48dp, action = text button in inverse-primary */
.toast { display: flex; gap: 12px; align-items: center; min-height: 48px; padding: 8px 8px 8px 16px; border-radius: var(--shape-xs); background: var(--md-sys-color-inverse-surface); color: var(--md-sys-color-inverse-on-surface); box-shadow: var(--elev-3); font: var(--md-sys-typescale-body-medium); animation: up var(--dur-m) var(--ease-decel); }
.toast.error { background: var(--md-sys-color-error-container); color: var(--md-sys-color-on-error-container); }
.tbtn { flex: none; border: 0; background: transparent; color: var(--md-sys-color-inverse-primary); border-radius: var(--shape-full); height: 40px; padding: 0 12px; font: var(--md-sys-typescale-label-large); cursor: pointer; }
.toast.error .tbtn { color: var(--md-sys-color-error); }
.tbtn:hover { background: color-mix(in srgb, currentColor 8%, transparent); }
.tbtn:focus-visible { background: color-mix(in srgb, currentColor 10%, transparent); }
@keyframes up { from { transform: translateY(12px); opacity: 0 } }
</style>
