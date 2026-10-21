<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
const props = defineProps<{ title: string; width?: string; labelledby?: string }>()
const emit = defineEmits<{ close: [] }>()
const el = ref<HTMLElement>()
let prev: HTMLElement | null = null
function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape') { e.stopPropagation(); emit('close') }
  if (e.key === 'Tab' && el.value) { // 焦点陷阱
    const f = el.value.querySelectorAll<HTMLElement>('button,[href],input,select,textarea,[tabindex]:not([tabindex="-1"])')
    const list = Array.from(f).filter((x) => !x.hasAttribute('disabled'))
    if (!list.length) return
    const first = list[0], last = list[list.length - 1]
    if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus() }
    else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus() }
  }
}
onMounted(async () => { prev = document.activeElement as HTMLElement; await nextTick(); (el.value?.querySelector('[autofocus],input,select,textarea') as HTMLElement | null ?? el.value)?.focus() })
onBeforeUnmount(() => prev?.focus?.())
void props
</script>
<template>
  <Teleport to="body">
    <div class="scrim" @mousedown.self="emit('close')">
      <div ref="el" class="dlg" role="dialog" aria-modal="true" :aria-label="title" tabindex="-1" :style="{ width: width ?? '560px' }" @keydown="onKey">
        <h2 class="t-title">{{ title }}</h2>
        <div class="body"><slot /></div>
        <div class="row actions"><slot name="actions" /></div>
      </div>
    </div>
  </Teleport>
</template>
<style scoped>
.scrim { position: fixed; inset: 0; background: rgb(0 0 0 / .32); display: grid; place-items: center; z-index: 100; padding: 16px; animation: fade var(--dur-m); }
.dlg { max-width: 100%; max-height: 90vh; overflow: auto; background: var(--c-surface-3); border-radius: var(--shape-xl); padding: 24px; box-shadow: var(--elev-3); display: flex; flex-direction: column; gap: 16px; outline: 0; }
.body { display: flex; flex-direction: column; gap: 16px; }
.actions { justify-content: flex-end; }
@keyframes fade { from { opacity: 0 } }
</style>
