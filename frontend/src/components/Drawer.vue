<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import Icon from './Icon.vue'
defineProps<{ title: string }>()
const emit = defineEmits<{ close: [] }>()
const el = ref<HTMLElement>()
let prev: HTMLElement | null = null
onMounted(async () => { prev = document.activeElement as HTMLElement; await nextTick(); el.value?.focus() })
onBeforeUnmount(() => prev?.focus?.())
</script>
<template>
  <Teleport to="body">
    <div class="scrim" @mousedown.self="emit('close')">
      <aside ref="el" class="drawer" role="dialog" aria-modal="true" :aria-label="title" tabindex="-1" @keydown.esc="emit('close')">
        <div class="row hd"><h2 class="t-title grow">{{ title }}</h2><button class="btn icon-only" aria-label="关闭" @click="emit('close')"><Icon name="close" /></button></div>
        <div class="bd"><slot /></div>
      </aside>
    </div>
  </Teleport>
</template>
<style scoped>
.scrim { position: fixed; inset: 0; background: rgb(0 0 0 / .32); z-index: 90; display: flex; justify-content: flex-end; animation: fade var(--dur-m); }
.drawer { width: min(480px, 100vw); height: 100%; background: var(--c-surface-2); border-radius: var(--shape-xl) 0 0 var(--shape-xl); box-shadow: var(--elev-3); display: flex; flex-direction: column; outline: 0; animation: slide var(--dur-m) var(--ease); }
.hd { padding: 16px 16px 8px 24px; } .bd { padding: 8px 24px 24px; overflow: auto; flex: 1; display: flex; flex-direction: column; gap: 16px; }
@keyframes fade { from { opacity: 0 } } @keyframes slide { from { transform: translateX(40px); opacity: 0 } }
</style>
