<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import type { FsConflict } from '../api'
import { applyAll, choiceLabel, defaultPicks, groupByItem, kindText, pickable, shortHash, summarize, toResolutions, type Choice } from '../utils/fsConflicts'
import { fmtBytes, fmtTime } from '../utils/format'
import Dialog from './Dialog.vue'
import Icon from './Icon.vue'

const props = defineProps<{ conflicts: FsConflict[]; busy?: boolean; /** 还原时没有“所选项路径”，用于标题 */ verb?: string }>()
const emit = defineEmits<{ confirm: [resolutions: Record<string, Choice>]; cancel: [] }>()

const groups = computed(() => groupByItem(props.conflicts))
const picks = ref<Record<string, Choice>>({})
watch(groups, (g) => { picks.value = defaultPicks(g) }, { immediate: true })
const ALL: Choice[] = ['skip', 'rename', 'replace']
</script>

<template>
  <Dialog :title="`${verb ?? '移动'}时发现冲突`" width="720px" testid="fs-conflict-dialog" @close="emit('cancel')">
    <p style="margin: 0">共 {{ conflicts.length }} 处冲突（{{ summarize(conflicts) }}）。<strong>尚未做任何改动</strong>，请选择每一项的处理方式；“替换”会先把旧文件移入回收站，可随时还原，不会直接删除。</p>
    <div class="row wrap all" role="group" aria-label="全部应用">
      <span class="muted">全部应用：</span>
      <button v-for="c in ALL" :key="c" class="chip" :disabled="busy || !pickable(groups, c)" :data-testid="'fs-conflict-all-' + c" @click="picks = applyAll(groups, c)">{{ choiceLabel(c) }}</button>
    </div>
    <ul class="list" aria-label="冲突列表">
      <li v-for="g in groups" :key="g.item" class="card outlined it" :data-testid="'fs-conflict-item'">
        <div class="row nm"><Icon name="warning" class="warn" /><strong class="ellipsis" :title="g.item">{{ g.conflicts[0].name }}</strong><span v-for="k in new Set(g.conflicts.map((c) => c.kind))" :key="k" class="chip sm">{{ kindText(k) }}</span></div>
        <div v-for="(c, i) in g.conflicts.slice(0, 3)" :key="i" class="cmp">
          <div><div class="lbl">移入的</div><div class="mono small" :title="c.incoming.path">{{ c.incoming.path }}</div><div class="muted small">{{ c.incoming.is_dir ? '文件夹' : fmtBytes(c.incoming.size_bytes) }} · {{ fmtTime(c.incoming.modified_at) }}<template v-if="c.incoming.sha256"> · SHA-256 {{ shortHash(c.incoming.sha256) }}</template></div></div>
          <div v-if="c.existing"><div class="lbl">已有的</div><div class="mono small" :title="c.existing.path">{{ c.existing.path }}</div><div class="muted small">{{ c.existing.is_dir ? '文件夹' : fmtBytes(c.existing.size_bytes) }} · {{ fmtTime(c.existing.modified_at) }}<template v-if="c.existing.sha256"> · SHA-256 {{ shortHash(c.existing.sha256) }}</template></div></div>
          <div v-else class="muted small">{{ c.message }}</div>
          <div v-if="c.identical && c.kind === 'duplicate_content'" class="muted small cm">{{ c.message }}</div>
        </div>
        <div v-if="g.conflicts.length > 3" class="muted small">……以及另外 {{ g.conflicts.length - 3 }} 处同一文件夹内的冲突，将采用同一处理方式。</div>
        <div class="seg" role="radiogroup" :aria-label="'处理方式：' + g.conflicts[0].name">
          <button v-for="c in g.choices" :key="c" role="radio" :aria-checked="picks[g.item] === c" :aria-pressed="picks[g.item] === c" :disabled="busy" :data-testid="'fs-choice-' + c" @click="picks[g.item] = c">{{ choiceLabel(c, g) }}</button>
        </div>
      </li>
    </ul>
    <template #actions>
      <button class="btn" :disabled="busy" @click="emit('cancel')">取消（不做改动）</button>
      <button class="btn filled" :disabled="busy" data-testid="fs-conflict-confirm" @click="emit('confirm', toResolutions(picks))">按所选方式继续</button>
    </template>
  </Dialog>
</template>

<style scoped>
.all { gap: 8px; align-items: center; }
.list { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 12px; max-height: 46vh; overflow: auto; }
.it { padding: 12px 16px; display: flex; flex-direction: column; gap: 8px; }
.nm { gap: 8px; flex-wrap: nowrap; } .warn { color: var(--c-warn); }
.cmp { display: grid; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); gap: 12px; }
.lbl { font: var(--md-sys-typescale-label-medium); color: var(--md-sys-color-on-surface-variant); }
.small { font: var(--md-sys-typescale-body-small); word-break: break-all; }
.cm { grid-column: 1 / -1; }
</style>
