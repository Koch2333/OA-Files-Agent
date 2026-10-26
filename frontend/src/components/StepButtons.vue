<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import type { PlanStepView } from '../utils/planSteps'
import Icon from './Icon.vue'

/**
 * 计划页的两步流程按钮：① 批准计划 → ② 执行归档，中间用箭头连接。
 * 被挡住的按钮使用 aria-disabled（仍可聚焦、可点击）：悬停/聚焦显示一句提示（tooltip），点击弹出带“下一步”动作的小气泡。
 * 首次打开待批准计划时可显示一次性提示（coach，非模态，不抢焦点，Esc 关闭）。
 */
const props = defineProps<{ view: PlanStepView; readonly?: boolean; pulseExecute?: boolean; coach?: boolean }>()
const emit = defineEmits<{ approve: []; execute: []; 'jump-first': []; 'confirm-fallback': []; 'coach-dismiss': [] }>()

const wrap = ref<HTMLElement>()
const approveBtn = ref<HTMLButtonElement>()
const popFor = ref<'' | 'approve' | 'execute'>('')
const tipFor = ref<'' | 'approve' | 'execute'>('')

const approveDisabled = computed(() => props.view.approve !== 'active')
const executeDisabled = computed(() => props.view.execute !== 'ready')
const approveIcon = computed(() => (props.view.approve === 'busy' ? 'progress_activity' : props.view.approve === 'done' ? 'check' : 'verified'))
const executeIcon = computed(() => (props.view.execute === 'running' ? 'progress_activity' : props.view.execute === 'done' ? 'check' : props.view.execute === 'locked' ? 'lock' : 'inventory_2'))
const approveLabel = computed(() => (props.view.approve === 'done' ? '计划已批准' : '批准计划'))
const executeLabel = computed(() => (props.view.execute === 'done' ? '已执行' : '执行归档'))
const hintOf = (w: 'approve' | 'execute') => (w === 'approve' ? props.view.approveHint : props.view.executeHint)

function press(w: 'approve' | 'execute') {
  const disabled = w === 'approve' ? approveDisabled.value : executeDisabled.value
  if (!disabled) { popFor.value = ''; if (w === 'approve') emit('approve'); else emit('execute'); return }
  if (props.view.approve === 'busy' || props.view.execute === 'running') return
  if (hintOf(w)) { popFor.value = popFor.value === w ? '' : w; tipFor.value = '' }
}
function act(a: 'approve' | 'jump' | 'confirm') {
  popFor.value = ''
  if (a === 'approve') { if (props.view.approve === 'active') emit('approve'); else void nextTick(() => approveBtn.value?.focus()) }
  else if (a === 'jump') emit('jump-first')
  else emit('confirm-fallback')
}
function onDoc(e: MouseEvent) { if (popFor.value && wrap.value && !wrap.value.contains(e.target as Node)) popFor.value = '' }
function onKey(e: KeyboardEvent) {
  if (e.key !== 'Escape') return
  if (popFor.value) { e.stopPropagation(); popFor.value = ''; tipFor.value = '' }
  else if (props.coach) { e.stopPropagation(); emit('coach-dismiss') }
}
onMounted(() => document.addEventListener('mousedown', onDoc))
onBeforeUnmount(() => document.removeEventListener('mousedown', onDoc))
watch(() => props.view.phase, () => { popFor.value = '' })
</script>

<template>
  <div ref="wrap" class="sb" role="group" aria-label="归档步骤：先批准，再执行" data-testid="plan-steps" @keydown="onKey">
    <div class="stepwrap">
      <button ref="approveBtn" type="button" class="btn has-icon step" :class="[view.approve === 'done' ? 'outlined' : 'tonal', { disabled: approveDisabled && view.approve !== 'done' }]" :aria-disabled="approveDisabled || undefined" :aria-describedby="tipFor === 'approve' && view.approveHint ? 'sb-tip-a' : undefined" data-testid="plan-approve" data-step="1" :data-state="view.approve"
        @click="press('approve')" @mouseenter="tipFor = 'approve'" @mouseleave="tipFor = ''" @focus="tipFor = 'approve'" @blur="tipFor = ''">
        <span class="badge" :class="view.approve" aria-hidden="true"><Icon v-if="view.approve === 'done'" name="check" sm /><template v-else>1</template></span>
        <Icon :name="approveIcon" :class="{ spin: view.approve === 'busy' }" sm />{{ approveLabel }}
      </button>
      <span v-if="tipFor === 'approve' && view.approveHint && !popFor" id="sb-tip-a" class="tip" role="tooltip">{{ view.approveHint }}</span>
      <div v-if="popFor === 'approve' && view.approveHint" class="pop" role="dialog" aria-label="为什么还不能批准" data-testid="plan-approve-pop">
        <p>{{ view.approveHint }}</p>
        <div class="row"><button type="button" class="btn sm" @click="act('jump')">跳到第一项</button><button v-if="view.fallback" type="button" class="btn sm tonal" data-testid="plan-confirm-fallback" @click="act('confirm')">确认全部暂填的「其他」（{{ view.fallback }}）</button></div>
      </div>
    </div>
    <Icon name="chevron_right" class="link" :class="{ done: view.approve === 'done' }" aria-hidden="true" />
    <div class="stepwrap">
      <button type="button" class="btn has-icon step" :class="[view.execute === 'ready' || view.execute === 'running' ? 'filled' : 'outlined', { disabled: executeDisabled && view.execute !== 'done', pulse: pulseExecute && view.execute === 'ready' }]" :aria-disabled="executeDisabled || undefined" :aria-describedby="tipFor === 'execute' && view.executeHint ? 'sb-tip-e' : undefined" data-testid="plan-execute" data-step="2" :data-state="view.execute"
        @click="press('execute')" @mouseenter="tipFor = 'execute'" @mouseleave="tipFor = ''" @focus="tipFor = 'execute'" @blur="tipFor = ''">
        <span class="badge" :class="view.execute" aria-hidden="true"><Icon v-if="view.execute === 'done'" name="check" sm /><template v-else>2</template></span>
        <Icon :name="executeIcon" :class="{ spin: view.execute === 'running' }" sm />{{ executeLabel }}
      </button>
      <span v-if="tipFor === 'execute' && view.executeHint && !popFor" id="sb-tip-e" class="tip" role="tooltip">{{ view.executeHint }}</span>
      <div v-if="popFor === 'execute' && view.executeHint" class="pop" role="dialog" aria-label="为什么还不能执行" data-testid="plan-execute-pop">
        <p>{{ view.executeHint }}</p>
        <div class="row"><button v-if="view.phase === 'unapproved'" type="button" class="btn sm tonal" data-testid="plan-pop-go-approve" @click="act('approve')">去批准</button><button v-else-if="view.phase === 'blocked'" type="button" class="btn sm" @click="act('jump')">跳到第一项</button></div>
      </div>
    </div>
    <div v-if="coach" class="pop coach" role="dialog" aria-label="归档步骤提示" data-testid="plan-coach">
      <p><b>① 先批准计划 → ② 再执行归档</b><br />批准前不会改动任何文件。</p>
      <div class="row"><button type="button" class="btn sm tonal" @click="emit('coach-dismiss')">知道了</button></div>
    </div>
  </div>
</template>

<style scoped>
.sb { display: inline-flex; align-items: center; gap: 4px; position: relative; flex-wrap: wrap; }
.stepwrap { position: relative; display: inline-flex; }
.link { color: var(--md-sys-color-outline); } .link.done { color: var(--md-sys-color-primary); }
.badge { display: inline-grid; place-items: center; width: 20px; height: 20px; border-radius: 50%; font: 600 12px/1 var(--font); background: var(--md-sys-color-primary); color: var(--md-sys-color-on-primary); flex: none; margin-left: -4px; }
.badge.locked, .badge.blocked { background: var(--md-sys-color-surface-container-highest); color: var(--md-sys-color-on-surface-variant); }
.badge.done { background: var(--md-sys-color-primary); color: var(--md-sys-color-on-primary); }
.btn.filled .badge { background: var(--md-sys-color-on-primary); color: var(--md-sys-color-primary); }
.btn.disabled { opacity: .62; cursor: help; }
.btn.disabled:hover { box-shadow: none; }
.tip { position: absolute; z-index: 40; top: calc(100% + 6px); left: 0; max-width: 280px; width: max-content; padding: 4px 8px; border-radius: var(--shape-xs); background: var(--md-sys-color-inverse-surface); color: var(--md-sys-color-inverse-on-surface); font: var(--md-sys-typescale-body-small); pointer-events: none; }
.pop { position: absolute; z-index: 41; top: calc(100% + 8px); left: 0; width: max-content; max-width: min(320px, calc(100vw - 32px)); padding: 12px 16px; border-radius: var(--shape-md); background: var(--md-sys-color-surface-container-high); color: var(--md-sys-color-on-surface); box-shadow: var(--elev-2); font: var(--md-sys-typescale-body-medium); display: flex; flex-direction: column; gap: 8px; }
.pop p { margin: 0; } .pop .row { display: flex; gap: 8px; flex-wrap: wrap; }
.coach { top: calc(100% + 8px); left: 0; border: 1px solid var(--md-sys-color-primary); }
@media (prefers-reduced-motion: no-preference) {
  .pulse { animation: sb-pulse 1.2s ease-out 3; }
  @keyframes sb-pulse { 0% { box-shadow: 0 0 0 0 color-mix(in srgb, var(--md-sys-color-primary) 55%, transparent); } 100% { box-shadow: 0 0 0 12px transparent; } }
}
@media (forced-colors: active) { .pop, .tip { border: 1px solid CanvasText; } }
</style>
