<script setup lang="ts">
import Icon from './Icon.vue'
defineProps<{ show: boolean; label?: string }>()
</script>
<template>
  <Teleport to="body">
    <div v-if="show" class="drop-ov" role="status" aria-live="polite" data-testid="drop-overlay">
      <div class="card">
        <Icon name="cloud_upload" class="ico" />
        <div class="t-title">{{ label ?? '松开即可上传' }}</div>
        <p class="muted">文件或整个文件夹都可以</p>
      </div>
    </div>
  </Teleport>
</template>
<style scoped>
.drop-ov { position: fixed; inset: 0; z-index: 900; display: grid; place-items: center; pointer-events: none; background: color-mix(in srgb, var(--md-sys-color-scrim, #000) 32%, transparent); }
.drop-ov::before { content: ''; position: absolute; inset: 12px; border: 3px dashed var(--md-sys-color-primary); border-radius: var(--shape-xl); }
.card { position: relative; display: flex; flex-direction: column; align-items: center; gap: 6px; padding: 28px 48px; border-radius: var(--shape-xl); background: var(--md-sys-color-primary-container); color: var(--md-sys-color-on-primary-container); box-shadow: var(--elev-3, 0 6px 24px rgba(0, 0, 0, .25)); }
.ico { font-size: 64px; color: var(--md-sys-color-primary); }
.muted { margin: 0; opacity: .8; }
</style>
