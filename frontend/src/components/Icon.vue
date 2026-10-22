<script setup lang="ts">
import { computed } from 'vue'
import { resolveIcon } from './iconMap'

// Call-site API is unchanged (name / fill / sm). `fill` used to switch the Material font to its filled glyph;
// with outline icons it means "emphasised": a heavier stroke (and a soft tint for folders, see base.css).
// Glyphs are decorative unless a `label` is given.
const props = defineProps<{ name: string; fill?: boolean; sm?: boolean; label?: string }>()
const glyph = computed(() => resolveIcon(props.name))
const tint = computed(() => props.fill && (props.name === 'folder' || props.name === 'folder_open'))
</script>
<template>
  <span class="icon" :class="{ fill, sm, tint }" :role="label ? 'img' : undefined" :aria-label="label || undefined" :aria-hidden="label ? undefined : 'true'">
    <component :is="glyph" :size="'100%'" />
  </span>
</template>
