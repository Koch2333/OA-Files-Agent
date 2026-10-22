<script setup lang="ts">
import { computed } from 'vue'
import { decisionLabel } from '../utils/autoSummary'
import Icon from './Icon.vue'
const props = defineProps<{ decision?: 'manual' | 'ai_auto'; model?: string }>()
const d = computed(() => decisionLabel(props.decision, props.model))
</script>
<template>
  <span class="chip" :class="{ attention: d.tone === 'attention' }" data-testid="decision-badge" :title="decision === 'ai_auto' ? '由大模型自动决策，未经人工审批' : '经人工审批后归档'"><Icon :name="decision === 'ai_auto' ? 'bolt' : 'person'" sm />{{ d.label }}</span>
</template>
