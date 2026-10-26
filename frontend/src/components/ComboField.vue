<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { buildComboGroups, flattenGroups, validateCategory, type CategoryOption, type ComboItem } from '../utils/classification'
import Icon from './Icon.vue'

/**
 * 可输入的下拉框（ARIA combobox，列表框自动补全）：M3 outlined 文本框 + 末尾下拉箭头；输入即过滤；
 * 候选里没有的值照样可以输入（列表末尾给出“新建值”提示行）；“已使用 / 预设 / 兜底分类”分组；
 * 键盘 ↑ ↓ Enter Esc Home End、Alt+↓ 展开；列表 Teleport 到 body（在 Dialog 里不会被裁剪）。
 * 值以纯文本渲染，不使用 v-html。校验规则与后端部门/类型一致，错误信息显示在输入框下方。
 */
const props = withDefaults(defineProps<{
  modelValue: string
  id: string
  options: CategoryOption[]
  placeholder?: string
  /** 外部给出的错误（如服务端校验失败）；为空时使用本地校验 */
  error?: string | null
  loading?: boolean
  disabled?: boolean
}>(), { placeholder: '', error: null, loading: false, disabled: false })
const emit = defineEmits<{ 'update:modelValue': [v: string] }>()

const open = ref(false)
const typing = ref(false)
const active = ref(-1)
const input = ref<HTMLInputElement>()
const pop = ref<HTMLElement>()
const place = ref({ left: 0, top: 0, width: 240, maxH: 280, up: false })
const listId = computed(() => `${props.id}-list`)
const optId = (i: number) => `${props.id}-opt-${i}`
const errId = computed(() => `${props.id}-err`)

const groups = computed(() => buildComboGroups(props.options, props.modelValue, { typing: typing.value }))
const flat = computed(() => flattenGroups(groups.value))
const indexOf = (gi: number, ii: number) => groups.value.slice(0, gi).reduce((n, g) => n + g.items.length, 0) + ii
const err = computed(() => props.error ?? (validateCategory(props.modelValue) ? `“${props.modelValue.trim()}”：${validateCategory(props.modelValue)}` : null))
const norm = (s: string) => s.normalize('NFC').trim().toLowerCase()
watch(flat, () => { active.value = -1 })

function reposition() {
  const el = input.value
  if (!el) return
  const r = el.getBoundingClientRect(), vh = window.innerHeight, vw = window.innerWidth
  const below = vh - r.bottom - 12, above = r.top - 12
  const up = below < 180 && above > below
  const width = Math.min(Math.max(r.width, 240), vw - 16)
  place.value = { left: Math.max(8, Math.min(r.left, vw - width - 8)), top: up ? r.top - 4 : r.bottom + 4, width, maxH: Math.max(120, Math.min(320, up ? above : below)), up }
}
function show(t = false) { if (props.disabled) return; typing.value = t; open.value = true; reposition(); void nextTick(reposition) }
function hide() { open.value = false; active.value = -1; typing.value = false }
function toggle() { if (open.value) hide(); else { input.value?.focus(); show(false) } }
function onInput(e: Event) { emit('update:modelValue', (e.target as HTMLInputElement).value); show(true) }
function choose(o: ComboItem) { emit('update:modelValue', o.value); hide(); input.value?.focus() }
function clear() { emit('update:modelValue', ''); hide(); input.value?.focus() }
function move(d: number) {
  if (!open.value) { show(false); return }
  const n = flat.value.length
  if (!n) return
  active.value = active.value < 0 ? (d > 0 ? 0 : n - 1) : (active.value + d + n) % n
  void nextTick(() => document.getElementById(optId(active.value))?.scrollIntoView?.({ block: 'nearest' }))
}
function onKey(e: KeyboardEvent) {
  if (e.key === 'ArrowDown') { e.preventDefault(); if (e.altKey && !open.value) show(false); else move(1) }
  else if (e.key === 'ArrowUp') { e.preventDefault(); move(-1) }
  else if ((e.key === 'Home' || e.key === 'End') && open.value && flat.value.length) {
    e.preventDefault(); active.value = e.key === 'Home' ? 0 : flat.value.length - 1
    void nextTick(() => document.getElementById(optId(active.value))?.scrollIntoView?.({ block: 'nearest' }))
  }
  else if (e.key === 'Enter') { if (open.value && active.value >= 0) { e.preventDefault(); choose(flat.value[active.value]) } }
  else if (e.key === 'Escape') { if (open.value) { e.stopPropagation(); e.preventDefault(); hide() } }
  else if (e.key === 'Tab') hide()
}
function onScroll(e: Event) { if (open.value && !pop.value?.contains(e.target as Node)) reposition() }
watch(open, (v) => {
  if (v) { window.addEventListener('resize', reposition); window.addEventListener('scroll', onScroll, true) }
  else { window.removeEventListener('resize', reposition); window.removeEventListener('scroll', onScroll, true) }
})
onBeforeUnmount(() => { window.removeEventListener('resize', reposition); window.removeEventListener('scroll', onScroll, true) })
const popStyle = computed(() => ({ left: place.value.left + 'px', top: place.value.top + 'px', width: place.value.width + 'px', maxHeight: place.value.maxH + 'px', transform: place.value.up ? 'translateY(-100%)' : undefined }))
const label = (o: ComboItem) => (o.kind === 'create' ? `新建值“${o.value}”` : o.value)
defineExpose({ focus: () => input.value?.focus() })
</script>

<template>
  <div class="cf">
    <div class="box">
      <input :id="id" ref="input" :value="modelValue" class="input" type="text" role="combobox" autocomplete="off" spellcheck="false" :disabled="disabled" :placeholder="placeholder"
        aria-autocomplete="list" aria-haspopup="listbox" :aria-expanded="open" :aria-controls="listId" :aria-activedescendant="open && active >= 0 ? optId(active) : undefined"
        :aria-invalid="!!err" :aria-describedby="err ? errId : undefined" :data-testid="`combo-${id}`" @input="onInput" @click="show(false)" @blur="hide" @keydown="onKey" />
      <span class="trail">
        <button v-if="modelValue && !disabled" type="button" class="btn icon-only sm" tabindex="-1" aria-label="清除" title="清除" @mousedown.prevent @click="clear"><Icon name="close" sm /></button>
        <button type="button" class="btn icon-only sm" tabindex="-1" :aria-label="open ? '收起候选' : '展开候选'" :title="open ? '收起候选' : '展开候选'" :disabled="disabled" data-testid="combo-toggle" @mousedown.prevent @click="toggle"><Icon name="arrow_drop_down" :class="{ flip: open }" /></button>
      </span>
    </div>
    <p v-if="err" :id="errId" class="supporting error" role="alert">{{ err }}</p>
    <Teleport to="body">
      <ul v-show="open" :id="listId" ref="pop" class="cf-pop" role="listbox" :style="popStyle" :aria-label="loading ? '候选加载中' : '候选值'" @mousedown.prevent>
        <li v-if="loading && !flat.length" class="cf-note" role="presentation">正在加载候选…</li>
        <template v-for="(g, gi) in groups" :key="g.id">
          <li class="cf-grp" role="presentation">{{ g.label }}</li>
          <li v-for="(o, ii) in g.items" :id="optId(indexOf(gi, ii))" :key="g.id + o.value" class="cf-opt" :class="{ on: indexOf(gi, ii) === active, cur: norm(o.value) === norm(modelValue) && o.kind !== 'create', create: o.kind === 'create' }" role="option" :aria-selected="norm(o.value) === norm(modelValue) && o.kind !== 'create'" @mouseenter="active = indexOf(gi, ii)" @click="choose(o)">
            <Icon v-if="o.kind === 'create'" name="add" sm /><span class="cf-txt">{{ label(o) }}</span><span v-if="o.kind === 'used'" class="cf-n">{{ o.count }} 次</span>
          </li>
        </template>
      </ul>
    </Teleport>
  </div>
</template>

<style scoped>
.cf { display: flex; flex-direction: column; gap: 4px; min-width: 0; }
.box { position: relative; }
.box .input { width: 100%; height: 56px; box-sizing: border-box; padding-right: 84px; }
.trail { position: absolute; right: 4px; top: 50%; transform: translateY(-50%); display: flex; align-items: center; }
.trail .flip { transform: rotate(180deg); }
@media (prefers-reduced-motion: no-preference) { .trail .icon { transition: transform var(--dur-s) var(--ease); } }
.cf-pop { position: fixed; z-index: 300; overflow: auto; margin: 0; padding: 8px 0; list-style: none; background: var(--md-sys-color-surface-container); color: var(--md-sys-color-on-surface); border-radius: var(--shape-xs); box-shadow: var(--elev-2); }
.cf-grp { padding: 8px 16px 4px; font: var(--md-sys-typescale-label-small); color: var(--md-sys-color-on-surface-variant); }
.cf-opt { display: flex; align-items: center; gap: 8px; min-height: 48px; padding: 0 16px; cursor: pointer; font: var(--md-sys-typescale-body-large); }
.cf-txt { flex: 1; min-width: 0; overflow-wrap: anywhere; }
.cf-n { font: var(--md-sys-typescale-label-small); color: var(--md-sys-color-on-surface-variant); white-space: nowrap; }
.cf-opt.on { background: color-mix(in srgb, var(--md-sys-color-on-surface) 8%, transparent); }
.cf-opt.cur { color: var(--md-sys-color-primary); font-weight: 600; }
.cf-opt.create { color: var(--md-sys-color-primary); }
.cf-note { padding: 12px 16px; color: var(--md-sys-color-on-surface-variant); font: var(--md-sys-typescale-body-medium); }
@media (pointer: fine) { .cf-opt { min-height: 40px; } }
@media (forced-colors: active) { .cf-pop { border: 1px solid CanvasText; } .cf-opt.on { outline: 2px solid Highlight; } }
</style>
