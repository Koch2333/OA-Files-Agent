<script setup lang="ts">
import { computed } from 'vue'
import type { ImportProgress } from '../utils/importUpload'
import { fmtBytes } from '../utils/format'
import { fmtEta, fmtSpeed } from '../utils/uploader'
import Icon from './Icon.vue'

/** 上传/复制进度：总进度、速度、剩余时间、重试状态与逐文件清单（只显示最近活动的几个文件）。 */
const props = defineProps<{ progress: ImportProgress | null; local?: import('../api').PathImportStatus | null }>()
defineEmits<{ cancel: [] }>()
const up = computed(() => props.progress?.upload ?? null)
const copy = computed(() => props.local ?? props.progress?.copy ?? null)
const pct = computed(() => {
  if (copy.value) return copy.value.bytes_total > 0 ? Math.min(100, Math.floor((copy.value.bytes_copied / copy.value.bytes_total) * 100)) : 0
  const u = up.value
  return u && u.totalBytes > 0 ? Math.min(100, Math.floor((u.sentBytes / u.totalBytes) * 100)) : 0
})
const active = computed(() => (up.value?.files ?? []).filter((f) => f.status === 'uploading' || f.status === 'retrying' || f.status === 'failed').slice(0, 5))
const retrying = computed(() => (up.value?.files ?? []).some((f) => f.status === 'retrying'))
const resumed = computed(() => (up.value?.files ?? []).filter((f) => f.resumed).length)
const label = computed(() => {
  const p = props.progress
  if (copy.value) return `正在从本机复制文件（${copy.value.copied}/${copy.value.discovered}）`
  if (p?.phase === 'committing') return '正在创建任务…'
  if (p?.phase === 'preparing') return '正在准备…'
  if (up.value) return `正在上传（${up.value.doneCount}/${up.value.files.length} 个文件）`
  return '正在提交…'
})
</script>
<template>
  <div v-if="progress || local" class="up" role="status" aria-live="polite" data-testid="upload-progress">
    <div class="row between">
      <b>{{ label }}</b>
      <button class="btn text" type="button" data-testid="upload-cancel" @click="$emit('cancel')">取消</button>
    </div>
    <div class="progressbar" :class="{ indet: !up && !copy }"><i :style="{ width: pct + '%' }" /></div>
    <div v-if="up" class="muted t-label stats">
      <span>{{ fmtBytes(up.sentBytes) }} / {{ fmtBytes(up.totalBytes) }}（{{ pct }}%）</span>
      <span data-testid="upload-speed">{{ fmtSpeed(up.speed) }}</span>
      <span data-testid="upload-eta">剩余 {{ fmtEta(up.etaSeconds) }}</span>
      <span v-if="resumed">已从中断处续传 {{ resumed }} 个文件</span>
    </div>
    <div v-else-if="copy" class="muted t-label stats">
      <span>{{ fmtBytes(copy.bytes_copied) }} / {{ fmtBytes(copy.bytes_total) }}（{{ pct }}%）</span>
      <span v-if="copy.failed">失败 {{ copy.failed }}</span><span v-if="copy.skipped">跳过 {{ copy.skipped }}</span>
    </div>
    <div v-if="retrying" class="banner" role="status"><Icon name="warning" sm />网络不稳定，正在自动重试；将从服务器已收到的位置继续，不会重复上传。</div>
    <ul v-if="active.length" class="files">
      <li v-for="f in active" :key="f.key">
        <span class="name">{{ f.path }}</span>
        <span class="muted">{{ fmtBytes(f.sent) }} / {{ fmtBytes(f.size) }}</span>
        <span v-if="f.status === 'retrying'" class="chip attention">重试 {{ f.attempts }}</span>
        <span v-else-if="f.status === 'failed'" class="chip error">{{ f.error }}</span>
      </li>
    </ul>
  </div>
</template>
<style scoped>
.up { display: flex; flex-direction: column; gap: 8px; padding: 12px 16px; border-radius: var(--shape-md); background: var(--md-sys-color-surface-container); }
.row { display: flex; align-items: center; gap: 8px; } .between { justify-content: space-between; }
.stats { display: flex; flex-wrap: wrap; gap: 4px 16px; }
.files { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 4px; }
.files li { display: flex; gap: 12px; align-items: center; min-width: 0; }
.files .name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
</style>
