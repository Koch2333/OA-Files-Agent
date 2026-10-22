<script setup lang="ts">
import { ref, watch } from 'vue'
import { normalizeTotpInput, shouldAutoSubmit } from '../utils/totp'
const props = defineProps<{ id: string; label: string; busy?: boolean; autofocus?: boolean }>()
const emit = defineEmits<{ submit: [code: string] }>()
const v = ref('')
watch(v, (n, o) => { if (shouldAutoSubmit(o, n, !!props.busy)) emit('submit', n) })
function onInput(e: Event) { v.value = normalizeTotpInput((e.target as HTMLInputElement).value) }
function clear() { v.value = '' }
defineExpose({ clear })
</script>
<template>
  <div class="field">
    <label :for="id">{{ label }}</label>
    <input :id="id" class="input otp" :value="v" inputmode="numeric" autocomplete="one-time-code" pattern="[0-9]*" maxlength="6" placeholder="000000" :disabled="busy" :autofocus="autofocus" spellcheck="false" @input="onInput" @keydown.enter.prevent="v.length === 6 && emit('submit', v)" />
  </div>
</template>
<style scoped>.otp { font: 600 22px var(--mono); letter-spacing: .35em; text-align: center; height: 52px; }</style>
