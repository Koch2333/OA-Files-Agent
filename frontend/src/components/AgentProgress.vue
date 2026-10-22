<script setup lang="ts">
import { computed } from 'vue'
import type { ImportJob } from '../api/types'
import { progressFor, type StepState } from '../utils/agentProgress'
import Icon from './Icon.vue'

/** Agent 执行进度：解析文件 → AI 识别 → 生成归档计划 → 人工审批 → 执行归档。compact = 列表里的小圆点 + 标签；默认完整版用于任务页头部。 */
const props = defineProps<{ task: Pick<ImportJob, 'status' | 'total_files' | 'processed_files' | 'failure_count'> & Partial<Pick<ImportJob, 'automation' | 'plan_id'>>; compact?: boolean }>()
const view = computed(() => progressFor(props.task))
const ICON: Record<StepState, string> = { done: 'check', active: 'progress_activity', waiting: 'hourglass_top', pending: '', error: 'priority_high', cancelled: 'block', skipped: 'bolt' }
const STATE_TEXT: Record<StepState, string> = { done: '已完成', active: '进行中', waiting: '等你操作', pending: '未开始', error: '出错', cancelled: '已取消', skipped: '已跳过（全自动）' }
</script>
<template>
  <ol class="ap" :class="{ compact, you: view.needsYou }" role="list" :aria-label="view.summary" :title="view.summary" data-testid="agent-progress">
    <li v-for="(s, i) in view.steps" :key="s.key" :class="[s.state, { focus: i === view.focus }]" :aria-current="i === view.focus ? 'step' : undefined" :data-step="s.key" :data-state="s.state">
      <span class="dot" aria-hidden="true"><Icon v-if="ICON[s.state]" :name="ICON[s.state]" sm :class="{ spin: s.state === 'active' }" /></span>
      <span class="txt">
        <span class="lab">{{ s.label }}</span>
        <span v-if="s.note" class="note">{{ s.note }}</span>
        <span class="sr-only">：{{ STATE_TEXT[s.state] }}</span>
      </span>
    </li>
  </ol>
</template>
<style scoped>
.ap { --ap-line: var(--md-sys-color-outline-variant); display: flex; list-style: none; margin: 0; padding: 0; gap: 0; min-width: 0; }
/* 横向：圆点在上、文字在下；连接线只画在圆点那一行 */
.ap li { flex: 1 1 0; min-width: 0; display: flex; flex-direction: column; align-items: flex-start; gap: 6px; position: relative; color: var(--md-sys-color-on-surface-variant); }
.ap li:not(:last-child)::after { content: ''; position: absolute; left: 32px; right: 6px; top: 11px; height: 2px; background: var(--ap-line); border-radius: 1px; }
.ap li.done:not(:last-child)::after { background: var(--md-sys-color-primary); }
.dot { width: 24px; height: 24px; border-radius: 50%; display: grid; place-items: center; flex: none; background: var(--c-surface-3); border: 2px solid transparent; position: relative; z-index: 1; }
.dot .icon { font-size: 16px; }
.dot .spin { animation: spin 1.1s linear infinite; }
.txt { display: flex; flex-direction: column; min-width: 0; max-width: 100%; line-height: 16px; padding-right: 8px; }
.lab { font-size: 13px; font-weight: 500; line-height: 16px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.note { font-size: 12px; opacity: .85; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }

li.done { color: var(--md-sys-color-on-surface); } li.done .dot { background: var(--md-sys-color-primary); color: var(--md-sys-color-on-primary); }
li.active { color: var(--md-sys-color-primary); } li.active .dot { background: var(--md-sys-color-primary-container); color: var(--md-sys-color-primary); box-shadow: 0 0 0 3px color-mix(in srgb, var(--md-sys-color-primary) 22%, transparent); } li.active .lab { font-weight: 600; }
li.waiting .dot { background: var(--c-warn-container); color: var(--c-on-warn-container); border-color: var(--c-warn); box-shadow: 0 0 0 3px color-mix(in srgb, var(--c-warn) 25%, transparent); }
li.waiting .txt { background: var(--c-warn-container); color: var(--c-on-warn-container); border-radius: var(--shape-sm); padding: 2px 8px; } li.waiting .lab { font-weight: 600; }
li.error { color: var(--md-sys-color-error); } li.error .dot { background: var(--md-sys-color-error-container); color: var(--md-sys-color-on-error-container); border-color: var(--md-sys-color-error); } li.error .lab { font-weight: 600; }
li.cancelled .dot { background: var(--c-surface-3); color: var(--md-sys-color-outline); border-color: var(--md-sys-color-outline); }
li.skipped { color: var(--md-sys-color-outline); } li.skipped .dot { background: transparent; border: 2px dashed var(--md-sys-color-outline); color: var(--md-sys-color-outline); } li.skipped .lab { text-decoration: line-through; text-decoration-thickness: 1px; }
li.pending .dot { background: transparent; border-color: var(--md-sys-color-outline-variant); }

/* 紧凑版：小圆点 + 标签，用于列表行 */
.compact { padding: 2px 0; }
.compact li { gap: 4px; } .compact li:not(:last-child)::after { left: 22px; top: 7px; }
.compact .dot { width: 16px; height: 16px; border-width: 1.5px; } .compact .dot .icon { font-size: 12px; }
.compact .lab { font-size: 12px; } .compact .note { font-size: 11px; }
.compact li.active .dot, .compact li.waiting .dot { box-shadow: none; }
.compact li.waiting .txt { padding: 0 6px; }

/* 窄屏：完整版改为纵向；紧凑版只显示当前步骤的文字，其余仅保留圆点（屏幕阅读器仍可读到全部） */
@media (max-width: 700px) {
  .ap:not(.compact) { flex-direction: column; gap: 12px; }
  .ap:not(.compact) li { flex: none; flex-direction: row; align-items: flex-start; gap: 10px; }
  .ap:not(.compact) li:not(:last-child)::after { left: 11px; right: auto; top: 26px; width: 2px; height: calc(100% - 12px + 2px); }
  .ap:not(.compact) .txt { padding-top: 4px; }
  .compact { flex-wrap: nowrap; align-items: center; gap: 6px; }
  .compact li { flex: 0 0 auto; flex-direction: row; align-items: center; } .compact li:not(:last-child)::after { display: none; }
  .compact li:not(.focus) .txt { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0 0 0 0); padding: 0; }
  .compact li.focus { flex: 1 1 auto; }
}
@media (prefers-reduced-motion: reduce) { .dot .spin { animation: none; } }
</style>
