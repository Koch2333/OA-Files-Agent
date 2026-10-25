<script setup lang="ts">
import { computed } from 'vue'
import { CHECK_KEY_LABEL, CHECK_KEY_TO, retryUi, type LlmErrorView } from '../utils/llmError'
import { useCountdown } from '../utils/useCountdown'
import Icon from './Icon.vue'

/**
 * 大模型（HTTP）出错的统一提示：原因（含状态码/自动重试次数）+ 主按钮。
 * 可重试的原因 → 主按钮“重试”（忙时禁用，429 有 Retry-After 时倒计时）；
 * 不可重试（401/403/404）→ 主按钮“检查 API Key”（跳设置），“重试”降为次要按钮，仍可点。
 * 组件不发请求：点击只触发 retry，由页面决定重试什么（任务 / 重新读取状态）。
 */
const props = defineProps<{
  view: LlmErrorView
  busy?: boolean
  /** 无权限重试（只读账号）时不显示按钮 */
  readonly?: boolean
  /** 失败发生的时间（ISO）；Retry-After 倒计时从这里扣除已过去的时间 */
  since?: string
  /** 标题，默认“大模型调用失败” */
  title?: string
}>()
const emit = defineEmits<{ retry: [] }>()
const remaining = computed(() => {
  if (!props.view.retryAfter) return 0
  const t = props.since ? Date.parse(props.since) : NaN
  const elapsed = Number.isFinite(t) ? Math.max(0, (Date.now() - t) / 1000) : 0
  return Math.max(0, Math.ceil(props.view.retryAfter - elapsed))
})
const cd = useCountdown(remaining)
const ui = computed(() => retryUi(props.view, { busy: !!props.busy, remaining: cd.left.value }))
const primaryRetry = computed(() => ui.value.primary === 'retry')
</script>
<template>
  <div class="banner error retry-notice" role="alert" data-testid="retry-notice" :data-primary="view.primary">
    <Icon :name="primaryRetry ? 'warning' : 'key'" />
    <div class="grow">
      <div class="rn-title"><b>{{ title ?? '大模型调用失败' }}</b><span v-if="view.status" class="chip error" data-testid="retry-status">HTTP {{ view.status }}</span></div>
      <div class="rn-msg" data-testid="retry-message">{{ view.message }}</div>
      <div v-if="ui.showCountdown" class="t-label rn-wait" data-testid="retry-countdown">服务商要求等待，{{ cd.left.value }} 秒后可重试</div>
      <div v-else-if="ui.showCheckKey" class="t-label rn-wait">这类原因重试通常不会成功：请先在设置里检查 API Key、服务地址与模型名称。</div>
    </div>
    <div v-if="!readonly" class="rn-actions">
      <RouterLink v-if="ui.showCheckKey" :to="CHECK_KEY_TO" class="btn filled sm has-icon" data-testid="retry-check-key"><Icon name="key" sm />{{ CHECK_KEY_LABEL }}</RouterLink>
      <button type="button" class="btn sm has-icon" :class="primaryRetry ? 'filled' : 'outlined'" :disabled="ui.retryDisabled" data-testid="retry-btn" @click="emit('retry')"><Icon name="replay" sm />{{ ui.retryLabel }}</button>
    </div>
  </div>
</template>
<style scoped>
.rn-title { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; } .rn-msg { word-break: break-word; margin-top: 2px; }
.rn-wait { margin-top: 2px; opacity: .85; } .rn-title .chip { border: 1px solid currentColor; background: transparent; color: inherit; }
.retry-notice { flex-wrap: wrap; } .retry-notice > .grow { flex: 1 1 220px; min-width: 0; }
.rn-actions { display: flex; gap: 8px; flex: none; flex-wrap: wrap; margin-left: auto; }
.retry-notice .btn.outlined { color: inherit; border-color: currentColor; }

</style>
