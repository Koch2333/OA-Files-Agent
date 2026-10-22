<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { modelGroups, type ModelOption } from '../utils/llm'
import Icon from './Icon.vue'

/** 可自由输入的模型选择框：输入即过滤，“服务商返回（实时）”在前，“预设建议”在后。 */
const props = withDefaults(defineProps<{
  modelValue: string
  id: string
  live?: string[]
  suggestions?: ModelOption[]
  placeholder?: string
  invalid?: boolean
  refreshable?: boolean
  fetching?: boolean
  fetchError?: string | null
  /** 已获取的模型数；null 表示尚未获取 */
  fetchedCount?: number | null
}>(), { live: () => [], suggestions: () => [], placeholder: '', refreshable: false, fetching: false, fetchError: null, fetchedCount: null })
const emit = defineEmits<{ 'update:modelValue': [v: string]; refresh: [] }>()

const open = ref(false)
const typing = ref(false)
const active = ref(-1)
const listId = computed(() => `${props.id}-list`)
const optId = (i: number) => `${props.id}-opt-${i}`

const groups = computed(() => modelGroups(props.live, props.suggestions, typing.value ? props.modelValue : ''))
const flat = computed(() => groups.value.flatMap((g) => g.items))
const indexOf = (gi: number, ii: number) => groups.value.slice(0, gi).reduce((n, g) => n + g.items.length, 0) + ii
watch(flat, () => { active.value = -1 })

function show() { open.value = true }
function hide() { open.value = false; active.value = -1; typing.value = false }
function onInput(e: Event) {
  emit('update:modelValue', (e.target as HTMLInputElement).value)
  typing.value = true; open.value = true; active.value = -1
}
function choose(o: ModelOption) { emit('update:modelValue', o.id); hide() }
function move(d: number) {
  if (!open.value) { show(); return }
  const n = flat.value.length
  if (!n) return
  active.value = active.value < 0 ? (d > 0 ? 0 : n - 1) : (active.value + d + n) % n
}
function onKey(e: KeyboardEvent) {
  if (e.key === 'ArrowDown') { e.preventDefault(); move(1) }
  else if (e.key === 'ArrowUp') { e.preventDefault(); move(-1) }
  else if (e.key === 'Enter') { if (open.value && active.value >= 0) { e.preventDefault(); choose(flat.value[active.value]) } }
  else if (e.key === 'Escape') { if (open.value) { e.stopPropagation(); e.preventDefault(); hide() } }
}
const hasAny = computed(() => flat.value.length > 0)
</script>

<template>
  <div class="mc">
    <div class="row">
      <div class="box grow">
        <input :id="id" :value="modelValue" class="input mono" type="text" role="combobox" autocomplete="off" spellcheck="false"
          aria-autocomplete="list" aria-haspopup="listbox" :aria-expanded="open" :aria-controls="listId" :aria-activedescendant="active >= 0 ? optId(active) : undefined"
          :placeholder="placeholder" :aria-invalid="invalid" @input="onInput" @focus="show" @click="show" @blur="hide" @keydown="onKey" />
        <Icon name="arrow_drop_down" class="caret muted" />
        <ul v-show="open && hasAny" :id="listId" class="pop" role="listbox" @mousedown.prevent>
          <template v-for="(g, gi) in groups" :key="g.id">
            <li class="grp" role="presentation">{{ g.label }}</li>
            <li v-for="(o, ii) in g.items" :id="optId(indexOf(gi, ii))" :key="o.id" class="opt" :class="{ on: indexOf(gi, ii) === active, cur: o.id === modelValue }" role="option" :aria-selected="o.id === modelValue" @mouseenter="active = indexOf(gi, ii)" @click="choose(o)">{{ o.label }}</li>
          </template>
        </ul>
      </div>
      <button v-if="refreshable" type="button" class="btn icon-only tonal" aria-label="从服务商刷新模型列表" title="从服务商获取可用模型" :disabled="fetching" @click="emit('refresh')"><Icon name="sync" sm :class="{ spin: fetching }" /></button>
    </div>
    <template v-if="refreshable">
      <span v-if="fetching" class="hint muted" role="status">正在从服务商获取模型列表…</span>
      <span v-else-if="fetchError" class="hint err" role="status">获取失败：{{ fetchError }}（仍可直接输入模型名）</span>
      <span v-else-if="fetchedCount != null && fetchedCount > 0" class="hint muted" role="status">已获取 {{ fetchedCount }} 个模型</span>
      <span v-else-if="fetchedCount === 0" class="hint muted" role="status">服务商未返回模型列表，请手动输入模型名</span>
      <span v-else class="hint muted">点击刷新从服务商获取可用模型，或直接输入</span>
    </template>
  </div>
</template>

<style scoped>
.mc { display: flex; flex-direction: column; gap: 4px; min-width: 0; }
.box { position: relative; }
.box .input { width: 100%; padding-right: 32px; box-sizing: border-box; }
.caret { position: absolute; right: 6px; top: 8px; pointer-events: none; }
.pop { position: absolute; z-index: 20; left: 0; right: 0; top: calc(100% + 4px); max-height: 260px; overflow: auto; margin: 0; padding: 6px 0; list-style: none; background: var(--c-surface-3); border-radius: var(--shape-md); box-shadow: var(--elev-2); }
.grp { padding: 6px 14px 2px; font-size: 11px; font-weight: 600; letter-spacing: .04em; color: var(--md-sys-color-on-surface-variant); }
.opt { padding: 8px 14px; cursor: pointer; font-family: var(--font-mono, monospace); font-size: 13px; word-break: break-all; }
.opt.on { background: color-mix(in srgb, var(--md-sys-color-primary) 12%, transparent); }
.opt.cur { font-weight: 600; color: var(--md-sys-color-primary); }
.hint { font-size: 12px; } .err { color: var(--md-sys-color-error); }
</style>
