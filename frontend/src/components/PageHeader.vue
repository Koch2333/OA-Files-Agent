<script setup lang="ts">
defineProps<{ title: string; subtitle?: string }>()
</script>
<template>
  <header class="ph">
    <div class="grow"><h1 class="t-display">{{ title }}</h1><p v-if="subtitle" class="muted sub">{{ subtitle }}</p></div>
    <div class="row wrap"><slot name="actions" /></div>
  </header>
</template>
<style scoped>.ph { display: flex; align-items: flex-end; gap: 16px; margin-bottom: 20px; flex-wrap: wrap; } .sub { margin-top: 4px; }</style>
