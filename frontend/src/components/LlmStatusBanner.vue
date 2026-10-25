<script setup lang="ts">
import { computed, ref } from 'vue'
import { api } from '../api'
import { useAi } from '../stores/ai'
import { useAuth } from '../stores/auth'
import { useUi } from '../stores/ui'
import { errMsg } from '../utils/errorInfo'
import RetryNotice from './RetryNotice.vue'
import Icon from './Icon.vue'
import ErrorBanner from './ErrorBanner.vue'

/** 顶部状态：可用 → 小标签“将使用：全局/个人 · 模型”；不可用 → 醒目提示卡与主按钮。compact 时不可用只显示一行。 */
const props = defineProps<{ compact?: boolean }>()
const ai = useAi()
const v = computed(() => ai.view)
const auth = useAuth()
const ui = useUi()
const retrying = ref(false)
/** 全局提示：最近有任务因大模型错误停下 → 一键重试该任务（只重新识别失败的文件）。 */
async function retryFailedTask() {
  const f = ai.failure
  if (!f || retrying.value) return
  retrying.value = true
  try { await api.retryTask(f.taskId); ui.toast('已重新排队：只重新识别失败的文件', 'success'); ai.clearFailure(f.taskId) } catch (e) { ui.toast(errMsg(e), 'error') } finally { retrying.value = false }
}
</script>
<template>
  <div class="lsb">
  <RetryNotice v-if="ai.failure" :view="ai.failure.view" :busy="retrying" :readonly="!auth.canWrite" :since="ai.failure.since" :title="`最近有 ${ai.failure.count} 个文件的大模型调用失败`" data-testid="llm-failure-banner" @retry="retryFailedTask" />
  <ErrorBanner v-if="ai.error && !ai.status" :message="ai.error" retry :busy="ai.loading" @retry="ai.refresh()" />
  <template v-else-if="ai.status">
    <span v-if="v.ok" class="chip progress" data-testid="ai-llm-banner"><Icon name="smart_toy" sm />{{ v.chip }}</span>
    <section v-else-if="!props.compact" class="card nollm" role="alert" data-testid="ai-nollm-card">
      <Icon name="smart_toy" class="big" />
      <div class="grow col">
        <h2 class="t-title">{{ v.title }}</h2>
        <p>{{ v.detail }}</p>
        <p v-if="v.extra" class="muted">{{ v.extra }}</p>
        <div v-if="v.cta"><RouterLink :to="v.cta.to" class="btn filled has-icon" data-testid="ai-nollm-cta"><Icon name="settings" sm />{{ v.cta.label }}</RouterLink></div>
      </div>
    </section>
    <span v-else class="chip attention" data-testid="ai-llm-banner"><Icon name="warning" sm />未配置大模型：<RouterLink v-if="v.cta" :to="v.cta.to">{{ v.cta.label }}</RouterLink><template v-else>请联系管理员</template></span>
  </template>
  </div>
</template>
<style scoped>
.lsb { margin-bottom: 16px; } .lsb :deep(.retry-notice) { margin-bottom: 12px; }
.nollm { display: flex; gap: 20px; align-items: flex-start; border: 2px solid var(--c-warn); background: var(--c-warn-container); color: var(--c-on-warn-container); }
.nollm .big { font-size: 48px; flex: none; } .nollm .muted { color: inherit; opacity: .85; }
@media (max-width: 800px) { .nollm { flex-direction: column; } }
</style>
