<script setup lang="ts">
defineProps<{ title: string; subtitle?: string }>()
</script>
<template>
  <header class="ph">
    <div class="grow"><h1 class="ttl">{{ title }}</h1><p v-if="subtitle" class="muted sub">{{ subtitle }}</p></div>
    <div class="row wrap acts"><slot name="actions" /></div>
  </header>
</template>
<style scoped>
/* 粘性页头：滚动时标题与主操作留在顶部；背景与页面一致，向上外扩以盖住 main 的上内边距。
   top 必须取 -padding：粘性偏移以 main 的内容区为基准，取 0 会让页头在滚动前就被推下 16px，盖住下一块内容的上沿（被“切掉”）。 */
.ph { position: sticky; top: calc(var(--page-pad-t) * -1); z-index: 20; display: flex; align-items: flex-end; gap: 12px 16px; flex-wrap: wrap; margin: calc(var(--page-pad-t) * -1) calc(var(--page-pad-x) * -1) 0; padding: var(--page-pad-t) var(--page-pad-x) calc(var(--space-2) + var(--space-3)); background: var(--md-sys-color-surface); }
.ttl { font-size: 24px; line-height: 32px; font-weight: 500; }
.sub { margin-top: 2px; font-size: 13px; line-height: 18px; }
.acts { gap: 8px; }
@media (max-width: 900px) { .ttl { font-size: 22px; line-height: 28px; } .sub { display: none; } }
</style>
