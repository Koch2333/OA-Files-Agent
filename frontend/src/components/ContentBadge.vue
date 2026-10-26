<script setup lang="ts">
import { computed } from 'vue'
import type { ContentInfo } from '../api/types'
import { contentChips, planChip, type ContentChip } from '../utils/contentStatus'
import Icon from './Icon.vue'
/** “仅依据文件名”等标记。文件记录传 content；计划条目传 basis + note。 */
const props = defineProps<{ content?: ContentInfo | null; basis?: string | null; note?: string | null }>()
const chips = computed<ContentChip[]>(() => {
  const p = planChip(props.basis, props.note)
  return p ? [p] : contentChips(props.content)
})
</script>
<template>
  <template v-if="chips.length">
    <span v-for="c in chips" :key="c.text" class="chip" :class="c.tone === 'attention' ? 'attention' : ''" :title="c.title" data-testid="content-badge"><Icon :name="c.tone === 'attention' ? 'draft' : 'info'" sm />{{ c.text }}</span>
  </template>
</template>
