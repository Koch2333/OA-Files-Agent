<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { isMock } from './api'
import { useUi } from './stores/ui'
import { useAuth } from './stores/auth'
import { useDiag } from './stores/diag'
import { useAi } from './stores/ai'
import { useAttention } from './stores/attention'
import { badgeText } from './utils/diagnostics'
import { ROLE_LABEL } from './utils/authz'
import { buildNav, groupBadge, inSettingsGroup, SETTINGS_GROUP_LABEL, settingsExpanded, type NavItem } from './utils/navModel'
import Icon from './components/Icon.vue'
import Toasts from './components/Toasts.vue'
import ReauthDialog from './components/ReauthDialog.vue'
import BackendBanner from './components/BackendBanner.vue'
import ApiLevelBanner from './components/ApiLevelBanner.vue'
import RouteErrorBoundary from './components/RouteErrorBoundary.vue'

const route = useRoute()
const router = useRouter()
const ui = useUi()
const auth = useAuth()
const diag = useDiag()
const ai = useAi()
const attention = useAttention()
const active = computed(() => String(route.meta.parent ?? route.name))
const mock = isMock()
/** 公开页（登录/向导/邀请）不显示应用外壳 */
const bare = computed(() => !!route.meta.public || !auth.me || route.name === undefined)
/** 侧栏模型：日常功能 + 折叠的“设置与管理”，均按角色过滤（后端仍是权威） */
const nav = computed(() => buildNav(auth.role))
/** 向导未完成时服务端闸门会拒绝应用接口：此时不启动任何周期请求 */
const gated = computed(() => auth.setup?.wizard_finished === false)
const restricted = computed(() => auth.me?.scope === 'enroll_mfa' || !!auth.user?.must_change_password || gated.value)

/** “设置与管理”分组：默认收起；进入其子页面时自动展开；本会话内记住手动展开/收起 */
const settingsOpen = computed(() => settingsExpanded(active.value, ui.settingsOpen))
watch(active, (a) => { if (inSettingsGroup(a)) ui.settingsOpen = true }, { immediate: true })
function toggleSettings() { ui.settingsOpen = !settingsOpen.value }

const menuOpen = ref(false)
const menuEl = ref<HTMLElement>()
function onDoc(e: MouseEvent) { if (menuOpen.value && menuEl.value && !menuEl.value.contains(e.target as Node)) menuOpen.value = false }
onMounted(() => document.addEventListener('mousedown', onDoc))
onBeforeUnmount(() => document.removeEventListener('mousedown', onDoc))
const initial = computed(() => (auth.user?.display_name || auth.user?.username || '?').slice(0, 1).toUpperCase())
function cycleTheme() { ui.mode = ui.mode === 'system' ? 'light' : ui.mode === 'light' ? 'dark' : 'system' }
const themeLabel = computed(() => ({ system: '跟随系统', light: '浅色', dark: '深色' })[ui.mode])
function toggleDensity() { ui.density = ui.density === 'compact' ? 'comfortable' : 'compact' }
async function logout() { menuOpen.value = false; await auth.logout(); await router.replace('/login') }
/** 管理员：每 60 秒刷新运行状态（导航角标 = 未处理错误报告数）；退出登录或换账号时清理 */
const openReports = computed(() => badgeText(diag.system?.open_error_reports))
watch(() => [auth.isAdmin, restricted.value] as const, ([admin, r]) => { if (admin && !r) diag.start(); else diag.reset() }, { immediate: true })
/** 操作员及以上：任务中心角标 = 等待审批的任务数（每 30 秒及每次换页刷新） */
watch(() => [auth.canWrite, restricted.value, auth.user?.id] as const, ([w, r]) => { if (w && !r) attention.start(); else attention.reset() }, { immediate: true })
watch(() => route.fullPath, () => { if (auth.canWrite && !restricted.value) void attention.refresh() })
watch(() => auth.user?.id, () => ai.reset())
onBeforeUnmount(() => { diag.stop(); attention.stop() })
function go(p: string) { menuOpen.value = false; void router.push(p) }

const reviewCount = computed(() => (auth.canWrite && attention.reviewTasks > 0 ? badgeText(attention.reviewTasks) : ''))
const badges = computed(() => ({ review: reviewCount.value, diag: openReports.value ?? '' }))
const settingsBadge = computed(() => groupBadge(nav.value.settings, { diag: openReports.value ?? '' }))
function badgeOf(n: NavItem): string { return n.badge ? (badges.value[n.badge] ?? '') : '' }
function badgeLabel(n: NavItem): string { return n.badge === 'review' ? `${badgeOf(n)} 个任务等待你审批` : `${badgeOf(n)} 个未处理错误报告` }
function badgeTest(n: NavItem): string | undefined { return n.badge === 'diag' ? 'diag-badge' : n.badge === 'review' ? 'review-badge' : undefined }
</script>

<template>
  <template v-if="bare"><ApiLevelBanner /><BackendBanner /><RouterView /><Toasts /><ReauthDialog v-if="auth.reauthPrompt" /></template>
  <template v-else>
    <a href="#main" class="skip btn filled">跳到主要内容</a>
    <div class="shell" :class="{ collapsed: ui.navCollapsed }">
      <nav class="nav" aria-label="主导航">
        <div class="brand row">
          <button class="btn icon-only" :aria-label="ui.navCollapsed ? '展开导航' : '收起导航'" @click="ui.navCollapsed = !ui.navCollapsed"><Icon name="menu" /></button>
          <span class="t-title-m name">OA 文件智能归档</span>
        </div>
        <template v-if="!restricted">
          <RouterLink v-for="n in nav.primary" :key="n.to" :to="n.to" class="item" :class="{ on: active === n.name }" :aria-current="active === n.name ? 'page' : undefined" :title="n.label">
            <Icon :name="n.icon" :fill="active === n.name" /><span class="lab">{{ n.label }}</span>
            <span v-if="badgeOf(n)" class="badge" :class="{ info: n.badge === 'review' }" :aria-label="badgeLabel(n)" :data-testid="badgeTest(n)">{{ badgeOf(n) }}</span>
          </RouterLink>
          <div v-if="nav.settings.length" class="gwrap">
            <button type="button" class="item ghead" :class="{ open: settingsOpen }" :aria-expanded="settingsOpen" aria-controls="nav-settings" :title="SETTINGS_GROUP_LABEL" data-testid="nav-settings-toggle" @click="toggleSettings">
              <Icon name="tune" /><span class="lab">{{ SETTINGS_GROUP_LABEL }}</span>
              <span v-if="!settingsOpen && settingsBadge" class="badge" :aria-label="`${settingsBadge} 个未处理错误报告`">{{ settingsBadge }}</span>
              <Icon name="expand_more" class="lab chev" />
            </button>
            <!-- 收起时用 v-show 保留在 DOM 中：路由、测试脚本与屏幕阅读器查找都不受影响 -->
            <div v-show="settingsOpen" id="nav-settings" class="sub" role="group" :aria-label="SETTINGS_GROUP_LABEL">
              <RouterLink v-for="n in nav.settings" :key="n.to" :to="n.to" class="item subitem" :class="{ on: active === n.name }" :aria-current="active === n.name ? 'page' : undefined" :title="n.hint ? `${n.label}（${n.hint}）` : n.label">
                <Icon :name="n.icon" :fill="active === n.name" /><span class="lab">{{ n.label }}</span>
                <span v-if="badgeOf(n)" class="badge" :aria-label="badgeLabel(n)" :data-testid="badgeTest(n)">{{ badgeOf(n) }}</span>
              </RouterLink>
            </div>
          </div>
        </template>
        <div class="grow" />
        <div v-if="auth.user" ref="menuEl" class="usermenu">
          <button class="item userbtn" aria-haspopup="menu" :aria-expanded="menuOpen" :title="auth.user.display_name" data-testid="user-menu" @click="menuOpen = !menuOpen" @keydown.esc="menuOpen = false">
            <span class="avatar" aria-hidden="true">{{ initial }}</span><span class="lab ellipsis">{{ auth.user.display_name }}</span>
          </button>
          <div v-if="menuOpen" class="menu card" role="menu" aria-label="用户菜单" @keydown.esc="menuOpen = false">
            <div class="mhead"><div class="t-title-m ellipsis">{{ auth.user.display_name }}</div><div class="muted t-label ellipsis">{{ auth.user.username }}</div><span class="chip" :class="auth.user.role === 'admin' ? 'progress' : ''" data-testid="role-chip">{{ ROLE_LABEL[auth.user.role] }}</span></div>
            <button class="mi" role="menuitem" @click="go('/account/security')"><Icon name="manage_accounts" sm />账号安全</button>
            <button class="mi" role="menuitem" @click="cycleTheme"><Icon name="contrast" sm />配色：{{ themeLabel }}</button>
            <button class="mi" role="menuitem" data-testid="density-toggle" @click="toggleDensity"><Icon name="density_small" sm />表格行高：{{ ui.density === 'compact' ? '紧凑' : '舒适' }}</button>
            <button v-if="!auth.isLocalMode" class="mi" role="menuitem" data-testid="logout" @click="logout"><Icon name="logout" sm />退出登录</button>
          </div>
        </div>
      </nav>
      <div class="main-col">
        <ApiLevelBanner />
        <BackendBanner />
        <div v-if="mock" class="mockbar" role="status"><Icon name="science" sm />演示数据模式：界面中的文件、任务、账号与会话均为模拟，不来自后端。<RouterLink v-if="auth.isAdmin" to="/settings">前往设置切换</RouterLink></div>
        <main id="main" tabindex="-1">
          <div class="page">
            <RouterView v-slot="{ Component }"><RouteErrorBoundary><component :is="Component" :key="route.fullPath" /></RouteErrorBoundary></RouterView>
          </div>
        </main>
      </div>
    </div>
    <Toasts />
    <ReauthDialog v-if="auth.reauthPrompt" />
  </template>
</template>

<style scoped>
.skip { position: fixed; left: 16px; top: -60px; z-index: 300; } .skip:focus { top: 16px; }
.shell { display: grid; grid-template-columns: 248px 1fr; height: 100%; }
.shell.collapsed { grid-template-columns: 80px 1fr; }
.nav { display: flex; flex-direction: column; gap: 2px; padding: 12px; background: var(--c-surface-1); overflow-y: auto; }
.brand { height: 52px; margin-bottom: 4px; } .brand .name { white-space: nowrap; }
.item { display: flex; align-items: center; gap: 12px; height: 46px; padding: 0 16px; border-radius: var(--shape-full); color: var(--md-sys-color-on-surface-variant); text-decoration: none !important; font-weight: 500; white-space: nowrap; transition: background var(--dur-s); position: relative; }
.item:hover { background: color-mix(in srgb, var(--md-sys-color-on-surface) 8%, transparent); }
.item.on { background: var(--md-sys-color-secondary-container); color: var(--md-sys-color-on-secondary-container); }
.gwrap { margin-top: 8px; padding-top: 6px; border-top: 1px solid var(--md-sys-color-outline-variant); display: flex; flex-direction: column; gap: 2px; }
.ghead { width: 100%; border: 0; background: transparent; font: 500 14px var(--font); cursor: pointer; text-align: left; }
.ghead .chev { margin-left: auto; font-size: 20px; transition: transform var(--dur-s) var(--ease); } .ghead.open .chev { transform: rotate(180deg); }
.sub { display: flex; flex-direction: column; gap: 2px; }
.subitem { height: 40px; padding-left: 28px; font-size: 13px; }
.usermenu { position: relative; } .userbtn { width: 100%; border: 0; background: transparent; font: inherit; cursor: pointer; height: 48px; }
.avatar { width: 32px; height: 32px; border-radius: 50%; background: var(--md-sys-color-primary); color: var(--md-sys-color-on-primary); display: grid; place-items: center; font-weight: 600; flex: none; }
.menu { position: absolute; left: 8px; bottom: 56px; min-width: 240px; z-index: 50; background: var(--c-surface-3); box-shadow: var(--elev-3); padding: 8px; display: flex; flex-direction: column; gap: 2px; }
.mhead { padding: 8px 12px 12px; display: flex; flex-direction: column; gap: 2px; align-items: flex-start; }
.mi { display: flex; align-items: center; gap: 12px; height: 40px; padding: 0 12px; border: 0; border-radius: var(--shape-sm); background: transparent; color: inherit; font: 500 14px var(--font); cursor: pointer; text-align: left; }
.mi:hover { background: color-mix(in srgb, var(--md-sys-color-on-surface) 8%, transparent); }
.badge { margin-left: auto; min-width: 20px; height: 20px; padding: 0 6px; border-radius: 10px; background: var(--md-sys-color-error); color: var(--md-sys-color-on-error); font-size: 11px; font-weight: 600; display: grid; place-items: center; }
.badge.info { background: var(--md-sys-color-primary); color: var(--md-sys-color-on-primary); }
.ghead .badge { margin-left: auto; } .ghead .badge + .chev { margin-left: 4px; }
.collapsed .badge { position: absolute; margin: 0; transform: translate(14px, -12px); }
.collapsed .lab, .collapsed .name { display: none; } .collapsed .item { justify-content: center; padding: 0; } .collapsed .subitem { padding: 0; }
.collapsed .gwrap { align-items: stretch; } .collapsed .menu { left: 8px; }
.main-col { display: flex; flex-direction: column; min-width: 0; min-height: 0; }
main { flex: 1; overflow: auto; padding: var(--page-pad-t) var(--page-pad-x) 40px; outline: 0; }
.mockbar { display: flex; gap: 8px; align-items: center; padding: 6px var(--page-pad-x); background: var(--c-warn-container); color: var(--c-on-warn-container); font-size: 13px; }
/* 窄屏：侧栏收成图标栏（沿用原行为），“设置与管理”保持可用：点图标展开，子项同样是图标 */
@media (max-width: 1000px) { .badge { position: absolute; margin: 0; transform: translate(14px, -12px); } .shell, .shell.collapsed { grid-template-columns: 80px 1fr; } .lab, .name { display: none; } .item { justify-content: center; padding: 0; } .subitem { padding: 0; } }
</style>
