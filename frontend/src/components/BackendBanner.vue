<script setup lang="ts">
import { backendDown, backendReason } from '../utils/backendHealth'
import Icon from './Icon.vue'
</script>
<template>
  <div v-if="backendDown" class="bbanner" role="alert" data-testid="backend-down">
    <Icon name="cloud_off" sm />
    <span><b>{{ backendReason === 'offline' ? '浏览器当前处于离线状态' : '无法连接到后端服务' }}</b>：页面会自动重试（连续失败时间隔逐步拉长，标签页隐藏时暂停），连接恢复后本提示会自动消失。独立版请确认 oa-agent 仍在运行；开发环境请确认后端监听在 OA_API_TARGET。</span>
  </div>
</template>
<style scoped>
.bbanner { display: flex; gap: 8px; align-items: center; padding: 8px 32px; background: var(--md-sys-color-error-container); color: var(--md-sys-color-on-error-container); font-size: 13px; }
@media (max-width: 1000px) { .bbanner { padding: 8px 16px; } }
</style>
