<script setup lang="ts">
import { computed } from 'vue'
import type { LedgerEntry } from '../api/types'
import { archiveRow } from '../utils/recentArchive'
import Icon from './Icon.vue'
import DecisionBadge from './DecisionBadge.vue'

/** 最近归档列表：易读名称 + 部门 / 类型 / 日期标签 + 相对时间。原文件名与完整路径不在这里显示，点击或按 Enter 打开详情。 */
const props = defineProps<{ items: LedgerEntry[]; now?: number }>()
const emit = defineEmits<{ open: [entry: LedgerEntry] }>()
const rows = computed(() => props.items.map((l) => ({ l, r: archiveRow(l, props.now ?? Date.now()) })))
</script>
<template>
  <ul class="ra" aria-label="最近归档">
    <li v-for="{ l, r } in rows" :key="r.id" class="item" tabindex="0" role="button" :aria-label="`查看详情：${r.fullTitle}`" data-testid="recent-row" @click="emit('open', l)" @keydown.enter.prevent="emit('open', l)" @keydown.space.prevent="emit('open', l)">
      <Icon name="description" class="muted ico" />
      <div class="grow body">
        <div class="ellipsis name" :title="r.fullTitle">{{ r.title }}</div>
        <div class="row chips">
          <span class="chip" :class="{ attention: r.department.missing }" title="部门">{{ r.department.text }}</span>
          <span class="chip" :class="{ attention: r.type.missing }" title="类型">{{ r.type.text }}</span>
          <span class="chip date" :class="{ attention: r.date.missing }" title="文件日期"><Icon name="event" sm />{{ r.date.text }}</span>
          <DecisionBadge v-if="r.decision === 'ai_auto'" decision="ai_auto" :model="r.decisionModel" />
        </div>
      </div>
      <span class="muted nowrap when" :title="r.whenFull">{{ r.when }}</span>
    </li>
  </ul>
</template>
<style scoped>
.ra { list-style: none; margin: 0; padding: 4px 8px 8px; }
.item { display: flex; gap: 10px; align-items: flex-start; padding: 8px 10px; border-radius: var(--shape-md); cursor: pointer; min-width: 0; }
.item:hover { background: color-mix(in srgb, var(--md-sys-color-primary) 6%, transparent); }
.ico { margin-top: 2px; } .body { display: flex; flex-direction: column; gap: 4px; } .name { font-weight: 500; }
.chips { gap: 6px; flex-wrap: wrap; } .chip { height: 22px; padding: 0 8px; } .chip .icon { font-size: 14px; }
.when { font-size: 12px; margin-top: 2px; }
@media (max-width: 900px) { .item { min-height: 56px; } }
</style>
