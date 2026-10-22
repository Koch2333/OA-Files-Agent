<script setup lang="ts">
import { computed, ref } from 'vue'
import { passwordStrength } from '../utils/password'
import Icon from './Icon.vue'
const props = defineProps<{ modelValue: string; label: string; id: string; autocomplete?: 'current-password' | 'new-password'; strength?: boolean; minLength?: number; username?: string; invalid?: boolean; autofocus?: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [v: string] }>()
const shown = ref(false)
const st = computed(() => passwordStrength(props.modelValue, props.minLength ?? 12, props.username))
const COLORS = ['var(--md-sys-color-error)', 'var(--md-sys-color-error)', 'var(--c-warn)', 'var(--c-success)', 'var(--c-success)']
</script>
<template>
  <div class="field">
    <label :for="id">{{ label }}</label>
    <div class="row" style="gap: 4px; position: relative">
      <input :id="id" :type="shown ? 'text' : 'password'" class="input grow" :value="modelValue" :autocomplete="autocomplete ?? 'current-password'" :aria-invalid="invalid || undefined"
        :aria-describedby="strength ? id + '-hint' : undefined" spellcheck="false" autocapitalize="off" :autofocus="autofocus" @input="emit('update:modelValue', ($event.target as HTMLInputElement).value)" />
      <button type="button" class="btn icon-only" :aria-label="shown ? '隐藏密码' : '显示密码'" :aria-pressed="shown" @click="shown = !shown"><Icon :name="shown ? 'visibility_off' : 'visibility'" /></button>
    </div>
    <div v-if="strength && modelValue" :id="id + '-hint'" class="hint" aria-live="polite">
      <div class="bars" aria-hidden="true"><i v-for="n in 4" :key="n" :style="{ background: n <= st.score ? COLORS[st.score] : undefined }" /></div>
      <span class="t-label">强度：{{ st.label }}</span>
      <span v-if="st.issues.length" class="muted t-label"> · {{ st.issues.join('；') }}</span>
    </div>
  </div>
</template>
<style scoped>
.hint { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; margin-top: 4px; }
.bars { display: flex; gap: 4px; width: 120px; } .bars i { flex: 1; height: 4px; border-radius: 2px; background: var(--c-surface-3); }
</style>
