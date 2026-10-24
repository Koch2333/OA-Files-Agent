<script setup lang="ts">
import { computed } from 'vue'
import { resolveIcon } from './iconMap'

// Material Symbols (Outlined, 400). `fill` selects the filled glyph (M3: selected/active states) when the
// glyph ships one (see FILLED in iconMap.ts), otherwise the outlined glyph is used.
// Glyphs are decorative (aria-hidden) unless a `label` is given.
const props = defineProps<{ name: string; fill?: boolean; sm?: boolean; label?: string }>()
const path = computed(() => {
  const g = resolveIcon(props.name)
  return props.fill && g[1] ? g[1] : g[0]
})
</script>
<template>
  <span class="icon" :class="{ fill, sm }" :role="label ? 'img' : undefined" :aria-label="label || undefined" :aria-hidden="label ? undefined : 'true'">
    <svg viewBox="0 -960 960 960" focusable="false" aria-hidden="true"><path :d="path" /></svg>
  </span>
</template>
