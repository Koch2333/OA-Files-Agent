<script setup lang="ts">
import { ref, watch } from 'vue'
import QRCode from 'qrcode'
/** 本地渲染二维码（不经任何外部服务）。内容仅在内存与该 data: URL 中。 */
const props = defineProps<{ value: string; size?: number; label?: string }>()
const src = ref('')
const failed = ref(false)
watch(() => props.value, async (v) => {
  failed.value = false
  try { src.value = v ? await QRCode.toDataURL(v, { margin: 1, width: props.size ?? 192, errorCorrectionLevel: 'M' }) : '' } catch { failed.value = true; src.value = '' }
}, { immediate: true })
</script>
<template>
  <img v-if="src" :src="src" :width="size ?? 192" :height="size ?? 192" :alt="label ?? '二维码'" class="qr" />
  <p v-else-if="failed" class="muted">二维码生成失败，请使用下方手动密钥。</p>
</template>
<style scoped>.qr { background: #fff; padding: 8px; border-radius: var(--shape-md); image-rendering: pixelated; }</style>
