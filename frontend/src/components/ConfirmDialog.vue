<script setup lang="ts">
import Dialog from './Dialog.vue'
defineProps<{ title: string; confirmLabel?: string; danger?: boolean; busy?: boolean; error?: string | null }>()
const emit = defineEmits<{ confirm: []; close: [] }>()
</script>
<template>
  <Dialog :title="title" width="480px" @close="!busy && emit('close')">
    <slot />
    <div v-if="error" class="banner error" role="alert">{{ error }}</div>
    <template #actions>
      <button class="btn" :disabled="busy" @click="emit('close')">取消</button>
      <button class="btn filled" :class="{ 'danger-fill': danger }" :style="danger ? { background: 'var(--md-sys-color-error)', color: 'var(--md-sys-color-on-error)' } : undefined" :disabled="busy" @click="emit('confirm')">{{ confirmLabel ?? '确认' }}</button>
    </template>
  </Dialog>
</template>
