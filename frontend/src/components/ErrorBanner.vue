<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import { useUi } from '../stores/ui'
import { copyText } from '../utils/clipboard'
import { splitRequestId } from '../utils/errorInfo'
import Icon from './Icon.vue'

/** retry：显示“重试”按钮（点击触发 retry 事件，由页面重做失败的操作）；busy 时禁用。 */
const props = defineProps<{ message?: string | null; icon?: string; retry?: boolean; busy?: boolean }>()
const emit = defineEmits<{ retry: [] }>()
const route = useRoute()
const ui = useUi()
const parts = computed(() => splitRequestId(props.message ?? ''))
async function copy() {
  const lines = [`错误：${parts.value.text}`, parts.value.id ? `请求编号：${parts.value.id}` : '', `页面：${route.fullPath}`, `时间：${new Date().toISOString()}`].filter(Boolean)
  ui.toast((await copyText(lines.join('\n'))) ? '已复制错误信息' : '复制失败，请手动选择文字复制', 'info')
}
</script>
<template>
  <div v-if="message" class="banner error ebanner" role="alert">
    <Icon :name="icon ?? 'error'" />
    <div class="grow"><div class="txt">{{ parts.text }}</div><div v-if="parts.id" class="t-label rid">请求编号 <code class="mono" data-testid="request-id">{{ parts.id }}</code></div></div>
    <button v-if="retry" class="btn sm has-icon filled" type="button" :disabled="busy" data-testid="error-retry" @click="emit('retry')"><Icon name="replay" sm />{{ busy ? '重试中…' : '重试' }}</button>
    <button class="btn sm has-icon" type="button" @click="copy"><Icon name="content_copy" sm />复制错误信息</button>
  </div>
</template>
<style scoped>
.ebanner .txt { word-break: break-word; } .rid { margin-top: 2px; opacity: .85; }
.ebanner .btn:not(.filled) { color: inherit; border: 1px solid currentColor; flex: none; }
.ebanner .btn { flex: none; }
</style>
