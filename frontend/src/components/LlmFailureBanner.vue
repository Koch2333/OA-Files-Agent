<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import type { LlmFailure } from '../utils/llmError'
import { retryLabel } from '../utils/llmError'
import Icon from './Icon.vue'

/** 调用服务商失败时的统一提示：大白话原因（含 HTTP 状态）+ “重试”按钮（重试中禁用；429 带 Retry-After 倒计时）。 */
const props = defineProps<{ failure: LlmFailure; busy?: boolean; testid?: string; what?: string }>()
const emit = defineEmits<{ retry: [] }>()

const wait = ref(0)
let timer: ReturnType<typeof setInterval> | undefined
function stop() { if (timer) { clearInterval(timer); timer = undefined } }
watch(() => props.failure, (f) => {
  stop()
  wait.value = f.kind === 'rate_limit' && f.retryAfter && f.retryAfter > 0 ? Math.min(Math.ceil(f.retryAfter), 120) : 0
  if (wait.value > 0) timer = setInterval(() => { wait.value -= 1; if (wait.value <= 0) stop() }, 1000)
}, { immediate: true })
onBeforeUnmount(stop)

const disabled = computed(() => !!props.busy || wait.value > 0)
const tid = computed(() => props.testid ?? 'llm-failure')
</script>

<template>
  <div class="banner error fail" role="alert" :data-testid="tid" :data-kind="failure.kind">
    <Icon name="error" />
    <div class="grow body">
      <span>{{ failure.message }}</span>
      <span v-if="what" class="muted what">{{ what }}</span>
    </div>
    <button v-if="failure.retryable" class="btn outlined sm has-icon" type="button" :disabled="disabled" :aria-busy="!!busy" :data-testid="tid + '-retry'" @click="emit('retry')">
      <Icon name="refresh" sm :class="{ spin: busy }" />{{ retryLabel(wait, !!busy) }}
    </button>
  </div>
</template>

<style scoped>
.fail { align-items: center; flex-wrap: wrap; }
.body { display: flex; flex-direction: column; gap: 2px; min-width: 200px; }
.what { font-size: 12px; }
</style>
