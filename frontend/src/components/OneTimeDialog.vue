<script setup lang="ts">
import { canClose, wipe, type OneTimeState } from '../utils/oneTime'
import Dialog from './Dialog.vue'
import OneTimeSecret from './OneTimeSecret.vue'
const props = defineProps<{ state: OneTimeState; who?: string }>()
const emit = defineEmits<{ 'update:state': [s: OneTimeState]; close: [] }>()
function close() { if (canClose(props.state)) { emit('update:state', wipe(props.state)); emit('close') } }
</script>
<template>
  <Dialog :title="state.title" width="520px" @close="close">
    <OneTimeSecret :state="state" :who="who" @update:state="emit('update:state', $event)" />
    <template #actions><button class="btn filled" :disabled="!canClose(state)" :title="canClose(state) ? '' : '请先复制并确认已保存'" @click="close">完成</button></template>
  </Dialog>
</template>
