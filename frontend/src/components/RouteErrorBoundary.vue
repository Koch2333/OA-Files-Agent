<script setup lang="ts">
import { onErrorCaptured, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { getReporter } from '../errorReporting'
import { copyText } from '../utils/clipboard'
import Icon from './Icon.vue'

/**
 * 路由视图的错误边界：某个页面渲染/生命周期出错时只替换该页面为“出错了”提示，导航与其他页面仍可用。
 * 错误在这里直接上报（并拦截，避免再次触发全局 errorHandler 造成重复上报）。
 */
const route = useRoute()
const failed = ref(false)
const reportId = ref<string | undefined>()
const detail = ref('')
const nonce = ref(0)
const copied = ref(false)

onErrorCaptured((err, inst, info) => {
  failed.value = true
  const o = inst?.$options as { name?: string; __name?: string } | undefined
  const component = o?.name || o?.__name || info
  detail.value = `${err instanceof Error ? err.message : String(err)}\n页面：${route.fullPath}\n组件：${component}`
  console.error(err)
  void getReporter().report(err, { component }, { silent: true }).then((id) => { reportId.value = id })
  return false
})
watch(() => route.fullPath, () => { failed.value = false; reportId.value = undefined; copied.value = false })
function retry() { failed.value = false; reportId.value = undefined; nonce.value++ }
async function copy() { copied.value = await copyText(`${detail.value}${reportId.value ? '\n报告编号：' + reportId.value : ''}`) }
</script>
<template>
  <section v-if="failed" class="card eb" role="alert" data-testid="route-error">
    <Icon name="error" class="big" />
    <h2 class="t-title">这个页面出了点问题</h2>
    <p class="muted">其余功能不受影响，你可以重试或切换到其他页面。{{ reportId ? '错误已自动记录，反馈时请提供下面的报告编号。' : '' }}</p>
    <p v-if="reportId" class="t-label">报告编号 <code class="mono" data-testid="report-id">{{ reportId }}</code></p>
    <div class="row wrap" style="justify-content: center">
      <button class="btn filled has-icon" data-testid="route-retry" @click="retry"><Icon name="refresh" sm />重试</button>
      <button class="btn has-icon" @click="copy"><Icon name="content_copy" sm />{{ copied ? '已复制' : '复制错误详情' }}</button>
    </div>
  </section>
  <slot v-else :key="nonce" />
</template>
<style scoped>
.eb { max-width: 560px; margin: 48px auto; display: flex; flex-direction: column; align-items: center; gap: 10px; text-align: center; }
.big { font-size: 48px; color: var(--md-sys-color-error); }
</style>
