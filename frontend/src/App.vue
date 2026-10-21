<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import { NAV } from './router'
import { isMock } from './api'
import { useUi } from './stores/ui'
import Icon from './components/Icon.vue'
import Toasts from './components/Toasts.vue'

const route = useRoute()
const ui = useUi()
const active = computed(() => String(route.meta.parent ?? route.name))
const mock = isMock()
</script>

<template>
  <a href="#main" class="skip btn filled">跳到主要内容</a>
  <div class="shell" :class="{ collapsed: ui.navCollapsed }">
    <nav class="nav" aria-label="主导航">
      <div class="brand row">
        <button class="btn icon-only" :aria-label="ui.navCollapsed ? '展开导航' : '收起导航'" @click="ui.navCollapsed = !ui.navCollapsed"><Icon name="menu" /></button>
        <span class="t-title-m name">OA 文件智能归档</span>
      </div>
      <RouterLink v-for="n in NAV" :key="n.to" :to="n.to" class="item" :class="{ on: active === n.name }" :aria-current="active === n.name ? 'page' : undefined" :title="n.label">
        <Icon :name="n.icon" :fill="active === n.name" /><span class="lab">{{ n.label }}</span>
      </RouterLink>
      <div class="grow" />
      <RouterLink to="/import" class="btn filled has-icon cta" title="智能归档"><Icon name="auto_awesome" sm /><span class="lab">智能归档</span></RouterLink>
    </nav>
    <div class="main-col">
      <div v-if="mock" class="mockbar" role="status"><Icon name="science" sm />演示数据模式：界面中的文件、任务与台账均为模拟，不来自后端。<RouterLink to="/settings">前往设置切换</RouterLink></div>
      <main id="main" tabindex="-1">
        <RouterView v-slot="{ Component }"><component :is="Component" :key="route.fullPath" /></RouterView>
      </main>
    </div>
  </div>
  <Toasts />
</template>

<style scoped>
.skip { position: fixed; left: 16px; top: -60px; z-index: 300; } .skip:focus { top: 16px; }
.shell { display: grid; grid-template-columns: 264px 1fr; height: 100%; }
.shell.collapsed { grid-template-columns: 88px 1fr; }
.nav { display: flex; flex-direction: column; gap: 4px; padding: 12px; background: var(--c-surface-1); overflow-y: auto; }
.brand { height: 56px; margin-bottom: 8px; } .brand .name { white-space: nowrap; }
.item { display: flex; align-items: center; gap: 12px; height: 52px; padding: 0 16px; border-radius: var(--shape-full); color: var(--md-sys-color-on-surface-variant); text-decoration: none !important; font-weight: 500; white-space: nowrap; transition: background var(--dur-s); }
.item:hover { background: color-mix(in srgb, var(--md-sys-color-on-surface) 8%, transparent); }
.item.on { background: var(--md-sys-color-secondary-container); color: var(--md-sys-color-on-secondary-container); }
.cta { justify-content: center; margin: 8px 0 4px; }
.collapsed .lab, .collapsed .name { display: none; } .collapsed .item { justify-content: center; padding: 0; } .collapsed .cta { padding: 0; width: 48px; align-self: center; }
.main-col { display: flex; flex-direction: column; min-width: 0; min-height: 0; }
main { flex: 1; overflow: auto; padding: 28px 32px 48px; outline: 0; }
.mockbar { display: flex; gap: 8px; align-items: center; padding: 8px 32px; background: var(--c-warn-container); color: var(--c-on-warn-container); font-size: 13px; }
@media (max-width: 1000px) { .shell, .shell.collapsed { grid-template-columns: 88px 1fr; } .lab, .name { display: none; } .item { justify-content: center; padding: 0; } .cta { padding: 0; width: 48px; align-self: center; } main { padding: 20px 16px 40px; } .mockbar { padding: 8px 16px; } }
</style>
