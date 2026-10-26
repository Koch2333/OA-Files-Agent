<script setup lang="ts">
import { computed, ref } from 'vue'
import type { PickedFile } from '../api'
import { fromInput } from '../utils/paths'
import { ingestDrop } from '../utils/ingestDrop'
import { useWindowDrop } from '../utils/dropTarget'
import { useUi } from '../stores/ui'
import DropOverlay from './DropOverlay.vue'
import ElevatedNotice from './ElevatedNotice.vue'
import { fmtBytes } from '../utils/format'
import { mergePicked, summarize, verdict, VERDICT_LABEL } from '../utils/importLimits'
import Icon from './Icon.vue'
import ErrorBanner from './ErrorBanner.vue'

/** 本机文件/文件夹选择（拖拽 + 选择）。保留相对路径、拒绝穿越路径。 */
const props = defineProps<{ modelValue: PickedFile[]; compact?: boolean; disabled?: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [v: PickedFile[]] }>()
const ui = useUi()
const scanning = ref(false)
const found = ref(0)
const err = ref<string | null>(null)
const sum = computed(() => summarize(props.modelValue))

function add(list: PickedFile[]) { emit('update:modelValue', mergePicked(props.modelValue, list)) }
// 整页都是投放区（遮罩提示“松开即可上传”）；页面外的拖放由全局守卫拦截，不会导航离开
const { dragging } = useWindowDrop({ enabled: () => !props.disabled, onDrop: (dt) => { void ingestDrop(dt, { add, notify: (n) => ui.toast(n.text, n.kind), scanning, found, err }) } })
function onPick(e: Event) { const el = e.target as HTMLInputElement; if (el.files) add(fromInput(el.files)); el.value = '' }
function remove(p: PickedFile) { emit('update:modelValue', props.modelValue.filter((x) => x !== p)) }
const chipFor = (p: PickedFile) => verdict(p.file)
</script>
<template>
  <div class="col">
    <div class="drop" :class="{ on: dragging, small: compact, off: disabled }" data-testid="drop-zone">
      <Icon name="cloud_upload" :class="compact ? 'big-s' : 'big'" />
      <div class="t-title-m">{{ scanning ? `正在读取文件${found ? `（已发现 ${found} 个）` : ''}…` : dragging ? '松开即可上传' : '把文件或整个文件夹拖到这里' }}</div>
      <p v-if="!compact" class="muted">子文件夹会被递归读取，并保留相对路径</p>
      <div class="row wrap" style="justify-content: center">
        <label class="btn filled has-icon" :aria-disabled="disabled"><Icon name="note_add" sm />选择文件<input type="file" multiple hidden :disabled="disabled" @change="onPick" /></label>
        <label class="btn tonal has-icon" :aria-disabled="disabled"><Icon name="folder_open" sm />选择文件夹<input type="file" hidden webkitdirectory :disabled="disabled" @change="onPick" /></label>
        <slot name="actions" />
      </div>
      <p class="muted lim">不限制文件大小与格式，大文件分块上传；Word / Excel / PPT（含 .doc .xls .ppt）、PDF、图片、文本、网页、邮件可识别内容，其他格式按文件名处理</p>
    </div>
    <DropOverlay :show="dragging" />
    <ElevatedNotice />
    <ErrorBanner :message="err" />

    <div v-if="modelValue.length" class="card flat">
      <div class="row hd">
        <div class="grow"><b>{{ sum.count }}</b> 个文件 · {{ fmtBytes(sum.totalBytes) }}<span v-if="sum.nameOnly" class="chip attention" style="margin-left: 8px" data-testid="rejected-count">{{ sum.nameOnly }} 个仅按文件名处理</span></div>
        <button class="btn sm" :disabled="disabled" @click="emit('update:modelValue', [])">清空</button>
      </div>
      <div class="table-wrap" :style="{ maxHeight: compact ? '240px' : '360px' }">
        <table class="tbl">
          <thead><tr><th>相对路径</th><th>大小</th><th>状态</th><th /></tr></thead>
          <tbody>
            <tr v-for="p in modelValue" :key="p.relativePath">
              <td class="ellipsis" style="max-width: 420px" :title="p.relativePath">{{ p.relativePath }}</td>
              <td class="nowrap muted">{{ fmtBytes(p.file.size) }}</td>
              <td><span class="chip" :class="chipFor(p) === 'ok' ? 'success' : 'attention'">{{ VERDICT_LABEL[chipFor(p)] }}</span></td>
              <td><button class="btn icon-only sm" :aria-label="`移除 ${p.file.name}`" :disabled="disabled" @click="remove(p)"><Icon name="close" sm /></button></td>
            </tr>
          </tbody>
        </table>
      </div>
      <p v-if="sum.nameOnly" class="muted rej" data-testid="rejected-list">格式无法读取内容（或是空文件）的文件仍会提交：只依据文件名判断，一定进入人工审核，原件不会被修改：{{ sum.nameOnlyList.slice(0, 5).map((r) => r.path).join('、') }}<template v-if="sum.nameOnlyList.length > 5"> 等 {{ sum.nameOnlyList.length }} 个</template></p>
    </div>
  </div>
</template>
<style scoped>
.drop { border: 2px dashed var(--md-sys-color-outline); border-radius: var(--shape-xl); padding: 48px 24px; display: flex; flex-direction: column; align-items: center; gap: 10px; text-align: center; transition: all var(--dur-s); background: var(--c-surface-1); }
.drop.small { padding: 20px 16px; gap: 8px; }
.drop.on { border-width: 3px; border-color: var(--md-sys-color-primary); background: var(--md-sys-color-primary-container); transform: scale(1.005); }
.drop.off { opacity: .5; }
.big { font-size: 56px; color: var(--md-sys-color-primary); } .big-s { font-size: 36px; color: var(--md-sys-color-primary); }
.lim { font-size: 12px; }
.hd { padding: 12px 16px; border-bottom: 1px solid var(--md-sys-color-outline-variant); }
.rej { padding: 8px 16px 12px; font-size: 12px; }
</style>
