<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
import type { TourStep } from '../utils/onboarding'
import Dialog from './Dialog.vue'
import Icon from './Icon.vue'

/** 新手引导（分步）：上一步 / 下一步 / 跳过；每一步可直达对应页面。关闭（含 Esc、跳过、完成）都算看过。 */
const props = defineProps<{ steps: TourStep[] }>()
const emit = defineEmits<{ close: [] }>()
const router = useRouter()
const i = ref(0)
const step = computed(() => props.steps[i.value])
const last = computed(() => i.value >= props.steps.length - 1)
function go(to: string) { emit('close'); void router.push(to) }
</script>
<template>
  <Dialog v-if="step" title="新手引导" width="560px" @close="emit('close')">
    <div class="col" data-testid="tour">
      <div class="dots" role="group" :aria-label="`第 ${i + 1} 步，共 ${steps.length} 步`"><span v-for="(s, k) in steps" :key="s.id" class="dot" :class="{ on: k === i, done: k < i }" /></div>
      <div class="row" style="align-items: center; gap: 12px"><Icon :name="step.icon" class="big" /><div class="t-title-m" data-testid="tour-title">{{ step.title }}</div></div>
      <p v-for="(t, k) in step.body" :key="k" style="margin: 0">{{ t }}</p>
      <div v-if="step.to"><button class="btn tonal has-icon" type="button" data-testid="tour-go" @click="go(step.to)"><Icon name="arrow_forward" sm />{{ step.toLabel }}</button></div>
    </div>
    <template #actions>
      <button class="btn" type="button" data-testid="tour-skip" @click="emit('close')">跳过</button>
      <span class="grow" />
      <button v-if="i > 0" class="btn" type="button" data-testid="tour-prev" @click="i--">上一步</button>
      <button v-if="!last" class="btn filled" type="button" autofocus data-testid="tour-next" @click="i++">下一步</button>
      <button v-else class="btn filled" type="button" autofocus data-testid="tour-done" @click="emit('close')">开始使用</button>
    </template>
  </Dialog>
</template>
<style scoped>
.big { font-size: 32px; color: var(--md-sys-color-primary); }
.dots { display: flex; gap: 6px; }
.dot { width: 22px; height: 4px; border-radius: 2px; background: var(--md-sys-color-outline-variant); }
.dot.done { background: color-mix(in srgb, var(--md-sys-color-primary) 45%, transparent); } .dot.on { background: var(--md-sys-color-primary); }
</style>
