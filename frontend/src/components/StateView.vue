<script setup lang="ts">
import Icon from './Icon.vue'
defineProps<{ loading?: boolean; error?: string | null; empty?: boolean; emptyTitle?: string; emptyHint?: string; emptyIcon?: string; rows?: number }>()
defineEmits<{ retry: [] }>()
</script>
<template>
  <div v-if="loading && empty !== false && !$slots.default" class="st" role="status" aria-label="加载中">
    <div v-for="i in rows ?? 4" :key="i" class="skeleton" style="height: 44px" />
  </div>
  <div v-else-if="error" class="st center" role="alert">
    <Icon name="cloud_off" class="big err" />
    <div class="t-title-m">加载失败</div>
    <p class="muted msg">{{ error }}</p>
    <button class="btn tonal has-icon" @click="$emit('retry')"><Icon name="refresh" sm />重试</button>
  </div>
  <div v-else-if="loading" class="st" role="status" aria-label="加载中">
    <div v-for="i in rows ?? 4" :key="i" class="skeleton" style="height: 44px" />
  </div>
  <div v-else-if="empty" class="st center">
    <Icon :name="emptyIcon ?? 'inbox'" class="big" />
    <div class="t-title-m">{{ emptyTitle ?? '暂无数据' }}</div>
    <p v-if="emptyHint" class="muted msg">{{ emptyHint }}</p>
    <slot name="empty-action" />
  </div>
  <slot v-else />
</template>
<style scoped>
.st { padding: 24px; display: flex; flex-direction: column; gap: 10px; }
.st.center { align-items: center; text-align: center; padding: 56px 24px; }
.big { font-size: 48px; color: var(--md-sys-color-outline); } .err { color: var(--md-sys-color-error); }
.msg { max-width: 440px; word-break: break-word; }
</style>
