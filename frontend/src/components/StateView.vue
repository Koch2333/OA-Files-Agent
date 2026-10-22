<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import { useUi } from '../stores/ui'
import { copyText } from '../utils/clipboard'
import { splitRequestId } from '../utils/errorInfo'
import Icon from './Icon.vue'
const props = defineProps<{ loading?: boolean; error?: string | null; empty?: boolean; emptyTitle?: string; emptyHint?: string; emptyIcon?: string; rows?: number }>()
defineEmits<{ retry: [] }>()
const route = useRoute()
const ui = useUi()
const parts = computed(() => splitRequestId(props.error ?? ''))
async function copy() {
  const lines = [`错误：${parts.value.text}`, parts.value.id ? `请求编号：${parts.value.id}` : '', `页面：${route.fullPath}`, `时间：${new Date().toISOString()}`].filter(Boolean)
  ui.toast((await copyText(lines.join('\n'))) ? '已复制错误信息' : '复制失败，请手动选择文字复制', 'info')
}
</script>
<template>
  <div v-if="loading && empty !== false && !$slots.default" class="st" role="status" aria-label="加载中">
    <div v-for="i in rows ?? 4" :key="i" class="skeleton" style="height: 44px" />
  </div>
  <div v-else-if="error" class="st center" role="alert">
    <Icon name="cloud_off" class="big err" />
    <div class="t-title-m">加载失败</div>
    <p class="muted msg">{{ parts.text }}</p>
    <p v-if="parts.id" class="t-label muted">请求编号 <code class="mono" data-testid="request-id">{{ parts.id }}</code></p>
    <div class="row wrap" style="justify-content: center">
      <button class="btn tonal has-icon" @click="$emit('retry')"><Icon name="refresh" sm />重试</button>
      <button class="btn has-icon" type="button" @click="copy"><Icon name="content_copy" sm />复制错误信息</button>
    </div>
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
.msg { max-width: 520px; word-break: break-word; }
</style>
