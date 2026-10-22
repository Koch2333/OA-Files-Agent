<script setup lang="ts">
import { computed } from 'vue'
import { copyText, downloadText } from '../utils/clipboard'
import { markCopied, recoveryCodesText, setAcknowledged, type OneTimeState } from '../utils/oneTime'
import { useUi } from '../stores/ui'
import Icon from './Icon.vue'
const props = defineProps<{ state: OneTimeState; who?: string }>()
const emit = defineEmits<{ 'update:state': [s: OneTimeState] }>()
const ui = useUi()
const multi = computed(() => props.state.kind === 'recovery')
const text = computed(() => props.state.values.join('\n'))
async function copy() {
  const ok = await copyText(text.value)
  if (ok) { emit('update:state', markCopied(props.state)); ui.toast('已复制', 'success') } else ui.toast('复制失败，请手动选择文本复制', 'error')
}
function download() {
  downloadText('oa-recovery-codes.txt', recoveryCodesText(props.state.values, props.who))
  emit('update:state', markCopied(props.state))
}
</script>
<template>
  <div class="col">
    <div class="banner" role="note"><Icon name="warning" /><div>这些内容<strong>只显示这一次</strong>，关闭后无法再次查看。请立即复制或下载并安全保存；不要通过聊天工具明文转发。</div></div>
    <ul v-if="multi" class="codes mono" aria-label="恢复码"><li v-for="c in state.values" :key="c">{{ c }}</li></ul>
    <textarea v-else class="input mono secret" readonly rows="2" :value="text" aria-label="一次性内容" @focus="($event.target as HTMLTextAreaElement).select()" />
    <div class="row wrap">
      <button class="btn tonal has-icon" type="button" @click="copy"><Icon name="content_copy" sm />复制</button>
      <button v-if="multi" class="btn outlined has-icon" type="button" @click="download"><Icon name="download" sm />下载 .txt</button>
    </div>
    <label class="row"><input type="checkbox" :checked="state.acknowledged" @change="emit('update:state', setAcknowledged(state, ($event.target as HTMLInputElement).checked))" /><span>{{ multi ? '我已安全保存恢复码' : '我已复制并保存' }}</span></label>
  </div>
</template>
<style scoped>
.codes { list-style: none; margin: 0; padding: 16px; display: grid; grid-template-columns: repeat(2, 1fr); gap: 8px 24px; background: var(--c-surface-3); border-radius: var(--shape-md); font-size: 15px; user-select: all; }
.secret { height: auto; padding: 10px 12px; word-break: break-all; resize: none; }
</style>
