<script setup lang="ts">
import { isMock } from '../api'
import Icon from './Icon.vue'
defineProps<{ title: string; subtitle?: string; wide?: boolean }>()
const mock = isMock()
</script>
<template>
  <div class="auth-page">
    <div v-if="mock" class="mockbar" role="status"><Icon name="science" sm />演示数据模式：账号、会话与 Passkey 均为模拟，不来自后端。</div>
    <main id="main" tabindex="-1" class="auth-main">
      <section class="card auth-card" :class="{ wide }" :aria-labelledby="'auth-title'">
        <div class="row brand"><Icon name="lock" class="logo" /><span class="t-title-m">OA 文件智能归档</span></div>
        <h1 id="auth-title" class="t-title">{{ title }}</h1>
        <p v-if="subtitle" class="muted">{{ subtitle }}</p>
        <slot />
      </section>
    </main>
  </div>
</template>
<style scoped>
.auth-page { min-height: 100%; display: flex; flex-direction: column; background: var(--md-sys-color-surface); }
.mockbar { display: flex; gap: 8px; align-items: center; padding: 8px 24px; background: var(--c-warn-container); color: var(--c-on-warn-container); font-size: 13px; }
.auth-main { flex: 1; display: grid; place-items: center; padding: 24px 16px; outline: 0; }
.auth-card { width: min(440px, 100%); display: flex; flex-direction: column; gap: 16px; padding: 28px; background: var(--c-surface-2); }
.auth-card.wide { width: min(720px, 100%); }
.brand { gap: 8px; color: var(--md-sys-color-primary); } .logo { font-size: 28px; }
</style>
