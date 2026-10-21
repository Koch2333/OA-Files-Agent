<script setup lang="ts">
defineProps<{ name: string; fill?: boolean; sm?: boolean }>()
</script>
<template><span class="icon" :class="{ fill, sm }" aria-hidden="true">{{ name }}</span></template>
