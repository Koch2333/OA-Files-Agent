<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { api } from '../api'
import type { LlmConfig, LlmOauthStatus, LlmPreset, LlmTestInput, LlmTestResult } from '../api'
import { desktop, type KeyHelperEvent } from '../utils/desktop'
import { keyGuideOf, sanitizeKeyLabel } from '../utils/keyHelper'
import { callbackFlagText, canOauth, clearPending, keySourceLabel, loadPending, oauthErrorText, pollStatus, safeKeyUrl, savePending, statusText, type OauthScope } from '../utils/llmOauth'
import { classifyError, classifyTestResult, type LlmFailure } from '../utils/llmError'
import { keyDisplay } from '../utils/llmGuide'
import ConfirmDialog from './ConfirmDialog.vue'
import LlmFailureBanner from './LlmFailureBanner.vue'
import Icon from './Icon.vue'

/** “用平台账号登录授权”：支持的平台显示登录按钮；其余平台显示“打开密钥页面”链接与说明（手动粘贴始终可用）。 */
const props = withDefaults(defineProps<{ scope: OauthScope; preset?: LlmPreset; saved?: LlmConfig; /** simple = 新手向导里的大按钮样式 */ variant?: 'simple' | 'full'; /** 重新测试连接（用已保存的密钥）；缺省时不显示测试重试 */ testFn?: (i: LlmTestInput) => Promise<LlmTestResult> }>(), { variant: 'full', preset: undefined, saved: undefined, testFn: undefined })
const emit = defineEmits<{ connected: [cfg: LlmConfig | undefined]; disconnected: [cfg: LlmConfig]; 'key-received': [key: string, label: string]; manage: [] }>()

const route = useRoute()
const busy = ref<'' | 'starting' | 'waiting' | 'disconnecting' | 'retesting'>('')
/** 登录后自动测试没通过时的失败信息（带重试） */
const testFailure = ref<LlmFailure | null>(null)
const testOkText = ref('')
const simple = computed(() => props.variant === 'simple')
const result = ref<{ kind: 'success' | 'warn' | 'error'; text: string } | null>(null)
const confirmDisconnect = ref(false)
let cancelled = false
const tid = (n: string) => `${props.scope}-llm-login-${n}`

const name = computed(() => props.preset?.name ?? '')
const keyUrl = computed(() => safeKeyUrl(props.preset))
const isDesktop = computed(() => desktop() !== null)
/** 桌面版：密钥相关的一切都在应用自带的窗口里完成（不打开系统浏览器）。 */
const inApp = computed(() => typeof desktop()?.openKeyHelper === 'function')
const hasGuide = computed(() => !!keyGuideOf(props.preset))
/** 应用内窗口打不开时，才提供“改用系统浏览器”这个明确的备用入口。 */
const fallback = ref<{ url: string; state: string } | null>(null)
const connectedHere = computed(() => !!props.saved?.api_key_set && props.saved.key_source === 'oauth' && props.saved.preset_id === props.preset?.id)
const sourceLabel = computed(() => keySourceLabel(props.saved, name.value))

async function wait(state: string) {
  busy.value = 'waiting'
  try {
    const s: LlmOauthStatus | null = await pollStatus({ get: (st) => api.getLlmOauthStatus(st), state, isCancelled: () => cancelled })
    if (!s) return
    clearPending()
    result.value = statusText(s, name.value)
    testFailure.value = s.status === 'connected' && s.test ? classifyTestResult(s.test) : null
    if (s.status === 'connected') { if (s.test?.ok) testOkText.value = '连接成功，可以使用了'; emit('connected', s.config) }
  } catch (e) {
    clearPending()
    result.value = { kind: 'error', text: oauthErrorText(e) }
  } finally { busy.value = '' }
}

async function start() {
  cancelled = false
  if (!props.preset) return
  result.value = null; busy.value = 'starting'
  try {
    const bridge = desktop()
    const r = await api.startLlmOauth({ provider: props.preset.id, scope: props.scope, client: bridge ? 'desktop' : 'web' })
    if (bridge) {
      // 桌面版：登录授权页在应用自带的密钥窗口里打开，回调落在本机 API；本窗口轮询
      fallback.value = null
      if (bridge.openKeyHelper) {
        const o = await bridge.openKeyHelper(props.preset.id, { mode: 'login', authorizeUrl: r.authorize_url })
        if (!o?.ok) {
          fallback.value = { url: r.authorize_url, state: r.state }
          result.value = { kind: 'error', text: o?.message || '没能打开登录窗口。' }
          busy.value = ''
          return
        }
      } else {
        // 旧版桌面壳没有应用内窗口：沿用系统浏览器
        let opened = true
        if (bridge.openExternal) opened = await bridge.openExternal(r.authorize_url)
        else window.open(r.authorize_url, '_blank', 'noopener,noreferrer')
        if (!opened) { result.value = { kind: 'error', text: '无法打开系统浏览器，请改用“打开密钥页面”手动创建 Key。' }; busy.value = ''; return }
      }
      await wait(r.state)
    } else {
      // 网页版：整页跳转到服务商，回调后回到设置页（?llm_oauth=…）再续接
      savePending({ state: r.state, scope: props.scope, provider: props.preset.id })
      location.assign(r.authorize_url)
    }
  } catch (e) {
    result.value = { kind: 'error', text: oauthErrorText(e) }
    busy.value = ''
  }
}

/** 明确选择“改用系统浏览器”（授权仍在本机回调，本窗口继续轮询）。 */
async function useSystemBrowser() {
  const f = fallback.value
  const bridge = desktop()
  if (!f || !bridge?.openExternal) return
  const ok = await bridge.openExternal(f.url)
  if (!ok) { result.value = { kind: 'error', text: '无法在系统浏览器中打开登录页面。' }; return }
  fallback.value = null
  result.value = null
  busy.value = 'starting'
  await wait(f.state)
}


/** 高级设置里的“打开向导窗口”：拿到的密钥交给父组件填进表单。 */
async function openGuide() {
  if (!props.preset) return
  const o = await desktop()?.openKeyHelper?.(props.preset.id, { mode: 'create' })
  if (!o?.ok) result.value = { kind: 'error', text: o?.message || '没能打开向导窗口。' }
}

function onHelperEvent(ev: KeyHelperEvent) {
  if (ev.type === 'key' && ev.provider === props.preset?.id && !simple.value) emit('key-received', ev.key, sanitizeKeyLabel(ev.label))
  else if (ev.type === 'closed' && ev.provider === props.preset?.id && busy.value === 'waiting' && !ev.delivered) cancel('已关闭登录窗口，没有完成授权。')
}

/** 原样重试自动测试：用已保存的密钥与模型，不需要重新输入任何东西 */
async function retryTest() {
  if (!props.testFn || !props.saved) return
  busy.value = 'retesting'
  try {
    const r = await props.testFn({ base_url: props.saved.base_url, model: props.saved.model })
    testFailure.value = classifyTestResult(r)
    if (r.ok) { testOkText.value = '连接成功，可以使用了'; result.value = { kind: 'success', text: '重试成功，连接正常。' } }
  } catch (e) { testFailure.value = classifyError(e) }
  finally { busy.value = '' }
}

function cancel(text?: string) { cancelled = true; busy.value = ''; clearPending(); result.value = { kind: 'error', text: typeof text === 'string' ? text : '已取消。若你已在服务商页面完成授权，可到其密钥页面吊销多余的 Key。' } }

async function disconnect() {
  busy.value = 'disconnecting'
  try {
    const cfg = await api.disconnectLlmOauth(props.scope)
    confirmDisconnect.value = false
    result.value = { kind: 'success', text: `已断开并删除本应用保存的密钥。该 Key 在 ${name.value} 一侧仍然有效，如需彻底作废请到其密钥页面删除。` }
    emit('disconnected', cfg)
  } catch (e) { confirmDisconnect.value = false; result.value = { kind: 'error', text: oauthErrorText(e) } } finally { busy.value = '' }
}

onMounted(() => {
  const flag = typeof route.query.llm_oauth === 'string' ? route.query.llm_oauth : null
  const pending = loadPending()
  if (!pending || pending.scope !== props.scope) return
  if (flag) {
    // 只改地址栏，不走 router.replace：App 以 route.fullPath 作为视图 key，路由一变整个设置页会重建，正在进行的轮询会被取消
    try { const u = new URL(location.href); u.searchParams.delete('llm_oauth'); history.replaceState(history.state, '', u.pathname + u.search + u.hash) } catch { /* ignore */ }
    const f = callbackFlagText(flag)
    if (f.kind === 'error') { clearPending(); result.value = { kind: 'error', text: f.text }; return }
    if (f.kind === 'ok') void wait(pending.state)
  }
})
let offHelper: (() => void) | undefined
onMounted(() => { offHelper = desktop()?.onKeyHelper?.(onHelperEvent) })
onBeforeUnmount(() => { cancelled = true; offHelper?.() })
defineExpose({ start })
</script>

<template>
  <div v-if="preset" class="login col" :data-testid="tid('panel')">
    <div v-if="connectedHere && simple" class="card outlined col created" :data-testid="tid('created')">
      <div class="row wrap">
        <Icon name="check_circle" class="ok" />
        <span class="grow"><b>已为你创建专用 Key：</b><span :data-testid="tid('key-name')">{{ keyDisplay(saved) }}</span></span>
        <button v-if="inApp" class="btn sm outlined has-icon" type="button" :data-testid="tid('manage')" @click="emit('manage')"><Icon name="tune" sm />管理密钥</button>
        <button class="btn sm danger has-icon" :disabled="busy !== ''" :data-testid="tid('disconnect')" @click="confirmDisconnect = true"><Icon name="logout" sm />断开</button>
      </div>
      <span class="muted hint">这把 Key 由 {{ name }} 生成并加密保存在本应用里，不会显示完整内容；随时可以在 {{ name }} 的 Keys 页面删除。已自动设置好服务地址和模型，并已启用。</span>
      <span v-if="testOkText && !testFailure" class="okline" role="status" :data-testid="tid('test-ok')"><Icon name="check_circle" sm />{{ testOkText }}</span>
    </div>
    <div v-else-if="connectedHere" class="row wrap">
      <span class="chip success" :data-testid="tid('connected')"><Icon name="verified_user" sm />{{ sourceLabel }}</span>
      <button class="btn sm danger has-icon" :disabled="busy !== ''" :data-testid="tid('disconnect')" @click="confirmDisconnect = true"><Icon name="logout" sm />断开</button>
    </div>
    <span v-else-if="saved?.api_key_set && saved.preset_id === preset.id" class="muted hint" :data-testid="tid('source')">{{ sourceLabel }}</span>

    <div v-if="canOauth(preset) && !connectedHere" class="row wrap">
      <button v-if="busy !== 'waiting'" class="btn has-icon" :class="simple ? 'filled big' : 'tonal'" :disabled="busy !== ''" :data-testid="tid('start')" @click="start"><Icon name="login" sm :class="{ spin: busy === 'starting' }" />{{ simple ? `登录 ${name}，自动完成设置` : `使用 ${name} 账号登录授权` }}</button>
      <template v-else>
        <span class="muted" role="status" :data-testid="tid('waiting')"><Icon name="hourglass_top" sm class="spin" /> {{ inApp ? '请在登录窗口里完成授权…' : isDesktop ? '请在浏览器中完成授权…' : '正在完成连接…' }}</span>
        <button class="btn sm" :data-testid="tid('cancel')" @click="cancel()">取消</button>
      </template>
      <span class="muted hint grow">跳转到 {{ name }} 官方页面登录并授权；本应用不会接触你的密码，授权后自动生成专用 API Key 并测试连接。</span>
    </div>
    <div v-else-if="!canOauth(preset) && keyUrl && inApp && hasGuide" class="row wrap" :data-testid="tid('keylink')">
      <button class="btn outlined has-icon" type="button" :data-testid="tid('open-guide')" @click="openGuide"><Icon name="computer" sm />打开向导窗口，一步步拿到 Key</button>
      <span class="muted hint grow">{{ name }} 没有面向第三方应用的登录授权接口：向导窗口会带你在它的网站上创建 Key，复制后自动填入上方。</span>
    </div>
    <div v-else-if="!canOauth(preset) && keyUrl" class="row wrap" :data-testid="tid('keylink')">
      <a class="btn outlined has-icon" :href="keyUrl" target="_blank" rel="noopener noreferrer"><Icon name="open_in_new" sm />打开 {{ name }} 的 API Key 页面</a>
      <span class="muted hint grow">{{ name }} 没有面向第三方应用的登录授权接口，无法自动配置：请在该页面登录并创建 Key，复制后粘贴到上方。</span>
    </div>

    <div v-if="fallback" class="row wrap" :data-testid="tid('fallback')">
      <button class="btn sm outlined has-icon" type="button" @click="useSystemBrowser"><Icon name="open_in_new" sm />改用系统浏览器登录</button>
      <span class="muted hint grow">应用内窗口没能打开时的备用办法；授权完成后这里会自动继续。</span>
    </div>
    <LlmFailureBanner v-if="testFailure" :failure="testFailure" :busy="busy === 'retesting'" what="密钥已经创建并保存，只是自动测试连接没有通过。" :testid="tid('test-failure')" @retry="retryTest" />
    <div v-else-if="result && !(simple && result.kind === 'success' && connectedHere)" class="banner" :class="result.kind === 'error' ? 'error' : result.kind === 'warn' ? 'warn' : ''" role="status" :data-testid="tid('result')"><Icon :name="result.kind === 'success' ? 'check_circle' : result.kind === 'warn' ? 'warning' : 'error'" /><span>{{ result.text }}</span></div>

    <ConfirmDialog v-if="confirmDisconnect" title="断开登录授权？" confirm-label="断开并删除密钥" danger :busy="busy === 'disconnecting'" @confirm="disconnect" @close="confirmDisconnect = false">
      <p>将删除本应用保存的 API Key，之后需要重新登录授权或手动填写才能使用 {{ name }}。</p>
      <p class="muted">本应用无法代你吊销这把 Key（{{ name }} 未提供此接口）；它在 {{ name }} 一侧仍然有效，如需作废请到其 Keys 页面删除。</p>
    </ConfirmDialog>
  </div>
</template>

<style scoped>
.big { height: 48px; padding: 0 28px !important; font-size: 15px; }
.created { gap: 8px; }
.created .ok { color: var(--c-success, #2e7d32); }
.okline { display: inline-flex; gap: 6px; align-items: center; font-size: 13px; color: var(--c-on-success-container, inherit); }
.login { gap: 8px; grid-column: 1 / -1; }
.hint { font-size: 12px; }
</style>
