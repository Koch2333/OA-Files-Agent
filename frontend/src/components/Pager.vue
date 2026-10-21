<script setup lang="ts">
import { computed } from 'vue'
import Icon from './Icon.vue'
const props = defineProps<{ page: number; pageSize: number; total: number }>()
const emit = defineEmits<{ 'update:page': [n: number]; 'update:pageSize': [n: number] }>()
const pages = computed(() => Math.max(1, Math.ceil(props.total / props.pageSize)))
const from = computed(() => (props.total ? (props.page - 1) * props.pageSize + 1 : 0))
const to = computed(() => Math.min(props.total, props.page * props.pageSize))
</script>
<template>
  <div class="row pager muted">
    <span>每页</span>
    <select class="input sm" :value="pageSize" aria-label="每页条数" @change="emit('update:pageSize', +($event.target as HTMLSelectElement).value)">
      <option v-for="n in [10, 20, 50, 100]" :key="n" :value="n">{{ n }}</option>
    </select>
    <span class="grow" />
    <span>{{ from }}–{{ to }} / 共 {{ total }} 条</span>
    <button class="btn icon-only sm" :disabled="page <= 1" aria-label="上一页" @click="emit('update:page', page - 1)"><Icon name="chevron_left" /></button>
    <span>{{ page }} / {{ pages }}</span>
    <button class="btn icon-only sm" :disabled="page >= pages" aria-label="下一页" @click="emit('update:page', page + 1)"><Icon name="chevron_right" /></button>
  </div>
</template>
<style scoped>.pager { padding: 8px 16px; border-top: 1px solid var(--md-sys-color-outline-variant); }</style>
