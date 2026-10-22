<script setup lang="ts">
import { computed } from 'vue'
import type { TaskStatus } from '../api/types'
import { ACTIVE, STATUS_LABEL, STATUS_TONE } from '../utils/format'
import Icon from './Icon.vue'
const props = defineProps<{ status: TaskStatus }>()
const icon = computed(() => ({ completed: 'check_circle', awaiting_review: 'pending_actions', approved: 'verified', failed: 'error', partial_failure: 'warning', cancelled: 'block' } as Record<string, string>)[props.status] ?? (ACTIVE.includes(props.status) ? 'progress_activity' : 'schedule'))
const HINT: Record<TaskStatus, string> = {
  queued: '已提交，排队等待处理', scanning: '正在扫描导入的文件', extracting: '正在解析文件内容（含扫描件 OCR）', analyzing: 'AI 正在识别部门、类型和日期', planning: '正在生成归档计划',
  awaiting_review: '计划已生成，等你审批后才会归档', approved: '计划已批准，还需要点击“执行归档”', archiving: '正在把文件归档到目标目录', completed: '全部处理完成',
  partial_failure: '部分文件处理失败，其余已完成', failed: '处理失败，可查看原因后重试', cancelled: '任务已取消',
}
</script>
<template>
  <span class="chip" :class="STATUS_TONE[status]" :title="HINT[status]"><Icon :name="icon" sm :class="{ rot: ACTIVE.includes(status) && status !== 'queued' }" />{{ STATUS_LABEL[status] }}</span>
</template>
<style scoped>.rot { animation: spin 1.2s linear infinite; } @media (prefers-reduced-motion: reduce) { .rot { animation: none; } }</style>
