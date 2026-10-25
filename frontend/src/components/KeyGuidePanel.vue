<script setup lang="ts">
import { computed, reactive, watch } from 'vue'
import type { KeyGuide } from '../utils/keyHelper'
import { helperModeText, initialSteps, stepReducer, type HelperMode, type StepEvent } from '../utils/keyHelper'
import Icon from './Icon.vue'

/**
 * 网页版的浮动指引：服务商页面在新标签页里打开（网页不能被嵌入），这里在设置表单旁边显示同一份分步说明。
 * 步骤数据与桌面向导窗口相同（后端 presets.json 的 key_guide）。本组件不读取任何网页内容；
 * “识别到密钥”由父组件根据剪贴板提示（用户手势后才读取）传入。
 */
const props = defineProps<{ provider: string; guide: KeyGuide; mode: HelperMode; detected: boolean; testid: string }>()
const emit = defineEmits<{ reopen: []; close: [] }>()

const st = reactive({ ...initialSteps(props.guide.steps.length) })
const apply = (e: StepEvent) => Object.assign(st, stepReducer({ ...st }, e))
watch(() => props.detected, (d) => apply({ type: d ? 'detected' : 'cleared' }))
const last = computed(() => props.guide.steps.length - 1)
</script>

<template>
  <aside class="kg card outlined col" :data-testid="testid" :aria-label="`${provider} 密钥指引`">
    <div class="row head">
      <h4 class="grow">{{ provider }} · 一步步拿到密钥</h4>
      <button class="btn icon-only sm" type="button" aria-label="关闭指引" :data-testid="testid + '-close'" @click="emit('close')"><Icon name="close" sm /></button>
    </div>
    <p class="muted mode" :data-testid="testid + '-mode'"><Icon name="info" sm />{{ helperModeText(mode) }}</p>
    <ol class="steps">
      <li v-for="(s, i) in guide.steps" :key="i" :class="{ cur: i === st.step, done: i < st.step }" :aria-current="i === st.step ? 'step' : undefined">
        <span class="n" aria-hidden="true"><Icon v-if="i < st.step" name="check" sm /><template v-else>{{ i + 1 }}</template></span>
        <div class="body">
          <div class="st">{{ s.title }}</div>
          <template v-if="i === st.step">
            <div class="hint">{{ s.hint }}</div>
            <div v-if="s.warn" class="warnbox"><Icon name="warning" sm />{{ s.warn }}</div>
            <div v-if="i < last && !st.detected" class="row acts">
              <button class="btn tonal sm" type="button" :data-testid="testid + '-next'" @click="apply({ type: 'next' })">{{ i === last - 1 ? '我已复制了' : '这一步完成了' }}</button>
              <button v-if="i > 0" class="btn sm" type="button" @click="apply({ type: 'prev' })">上一步</button>
            </div>
          </template>
        </div>
      </li>
    </ol>
    <p v-if="st.step === last && !detected" class="muted" role="status">复制好密钥后，回到本页：下面会出现“检测到密钥”的提示。</p>
    <details v-if="guide.login_note || guide.cost_note" class="notes">
      <summary>登录和费用提示</summary>
      <p v-if="guide.login_note">{{ guide.login_note }}</p>
      <p v-if="guide.cost_note">{{ guide.cost_note }}</p>
    </details>
    <div class="row">
      <button class="btn outlined sm has-icon" type="button" :data-testid="testid + '-reopen'" @click="emit('reopen')"><Icon name="open_in_new" sm />重新打开密钥页面</button>
    </div>
    <p class="muted priv">本页不会读取服务商网页的内容、账号或密码；只在你点击按钮或回到本页时，才检查剪贴板里是不是一把密钥，并由你确认后再使用。</p>
  </aside>
</template>

<style scoped>
.kg { gap: 10px; padding: 14px 16px; position: sticky; top: 72px; align-self: start; }
.head h4 { margin: 0; font: var(--md-sys-typescale-title-small); }
.mode { display: flex; gap: 6px; align-items: flex-start; font-size: 12px; margin: 0; }
.steps { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 6px; }
.steps li { display: flex; gap: 10px; padding: 8px 10px; border-radius: var(--shape-md); color: var(--md-sys-color-on-surface-variant); }
.steps .n { flex: none; width: 22px; height: 22px; border-radius: 50%; display: grid; place-items: center; font: var(--md-sys-typescale-label-small); background: var(--md-sys-color-surface-container-highest); margin-top: 1px; }
.steps .st { font-weight: 600; }
.steps .body { min-width: 0; flex: 1; }
.steps li.cur { background: var(--md-sys-color-secondary-container); color: var(--md-sys-color-on-secondary-container); }
.steps li.cur .n { background: var(--md-sys-color-primary); color: var(--md-sys-color-on-primary); }
.steps li.done .n { background: var(--c-success); color: #fff; }
.hint { margin-top: 4px; line-height: 1.55; }
.warnbox { display: flex; gap: 6px; margin-top: 8px; padding: 6px 10px; border-radius: var(--shape-sm); background: var(--c-warn-container); color: var(--c-on-warn-container); font-size: 13px; }
.acts { margin-top: 8px; gap: 8px; }
.notes { font-size: 13px; }
.notes summary { cursor: pointer; }
.priv { font-size: 12px; margin: 0; }
</style>
