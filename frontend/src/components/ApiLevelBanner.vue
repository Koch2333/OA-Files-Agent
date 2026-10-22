<script setup lang="ts">
import { computed } from 'vue'
import { useAuth } from '../stores/auth'
import { apiLevelMismatch } from '../utils/apiLevel'
import Icon from './Icon.vue'
const auth = useAuth()
/** 前端与后端的 api_level 不一致：持久横幅，不阻塞任何操作（演示模式的 mock 状态总是一致） */
const mismatch = computed(() => apiLevelMismatch(auth.setup))
</script>
<template>
  <div v-if="mismatch" class="abanner" role="alert" data-testid="api-level-banner">
    <Icon name="warning" sm />
    <span><b>{{ mismatch.message }}</b>。部分页面可能出现“接口不存在”等错误；重启或升级后端后刷新本页即可。</span>
  </div>
</template>
<style scoped>
.abanner { display: flex; gap: 8px; align-items: center; padding: 10px 32px; background: var(--md-sys-color-error); color: var(--md-sys-color-on-error); font-size: 14px; position: sticky; top: 0; z-index: 200; }
@media (max-width: 1000px) { .abanner { padding: 10px 16px; } }
</style>
