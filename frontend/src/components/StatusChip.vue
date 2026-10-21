<script setup lang="ts">
import { computed } from 'vue'
import type { TaskStatus } from '../api/types'
import { ACTIVE, STATUS_LABEL, STATUS_TONE } from '../utils/format'
import Icon from './Icon.vue'
const props = defineProps<{ status: TaskStatus }>()
const icon = computed(() => ({ completed: 'check_circle', awaiting_review: 'pending_actions', approved: 'verified', failed: 'error', partial_failure: 'warning', cancelled: 'block' } as Record<string, string>)[props.status] ?? (ACTIVE.includes(props.status) ? 'progress_activity' : 'schedule'))
</script>
<template>
  <span class="chip" :class="STATUS_TONE[status]"><Icon :name="icon" sm :class="{ rot: ACTIVE.includes(status) && status !== 'queued' }" />{{ STATUS_LABEL[status] }}</span>
</template>
<style scoped>.rot { animation: spin 1.2s linear infinite; }</style>
