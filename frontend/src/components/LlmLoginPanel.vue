<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api } from '../api'
import type { LlmConfig, LlmOauthStatus, LlmPreset } from '../api'
import { desktop } from '../utils/desktop'
import { callbackFlagText, canOauth, clearPending, keySourceLabel, loadPending, oauthErrorText, pollStatus, safeKeyUrl, savePending, statusText, type OauthScope } from '../utils/llmOauth'
import ConfirmDialog from './ConfirmDialog.vue'
import Icon from './Icon.vue'

/** “用平台账号登录授权”：支持的平台显示登录按钮；其余平台显示“打开密钥页面”链接与说明（手动粘贴始终可用）。 */
const props = defineProps<{ scope: OauthScope; preset?: LlmPreset; saved?: LlmConfig }>()
const emit = defineEmits<{ connected: [cfg: LlmConfig | undefined]; disconnected: [cfg: LlmConfig] }>()

const route = useRoute()
const router = useRouter()
const busy = ref<'' | 'starting' | 'waiting' | 'disconnecting'>('')
const result = ref<{ kind: 'success' | 'warn' | 'error'; text: string } | null>(null)
const confirmDisconnect = ref(false)
let cancelled = false
const tid = (n: string) => `${props.scope}-llm-login-${n}`

const name = computed(() => props.preset?.name ?? '')
const keyUrl = computed(() => safeKeyUrl(props.preset))
const isDesktop = computed(() => desktop() !== null)
const connectedHere = computed(() => !!props.saved?.api_key_set && props.saved.key_source === 'oauth' && props.saved.preset_id === props.preset?.id)
const sourceLabel = computed(() => keySourceLabel(props.saved, name.value))

async function wait(state: string) {
  busy.value = 'waiting'
  try {
    const s: LlmOauthStatus | null = await pollStatus({ get: (st) => api.getLlmOauthStatus(st), state, isCancelled: () => cancelled })
    if (!s) return
    clearPending()
    result.value = statusText(s, name.value)
    if (s.status === 'connected') emit('connected', s.config)
  } catch (e) {
    clearPending()
    result.value = { kind: 'error', text: oauthErrorText(e) }
  } finally { busy.value = '' }
}

async function start() {
  if (!props.preset) return
  result.value = null; busy.value = 'starting'
  try {
    const bridge = desktop()
    const r = await api.startLlmOauth({ provider: props.preset.id, scope: props.scope, client: bridge ? 'desktop' : 'web' })
    if (bridge) {
      // 桌面版：系统浏览器完成授权，回调落在本机 API；本窗口轮询
      let opened = true
      if (bridge.openExternal) opened = await bridge.openExternal(r.authorize_url)
      else window.open(r.authorize_url, '_blank', 'noopener,noreferrer')
      if (!opened) { result.value = { kind: 'error', text: '无法打开系统浏览器，请改用“打开密钥页面”手动创建 Key。' }; busy.value = ''; return }
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

function cancel() { cancelled = true; busy.value = ''; clearPending(); result.value = { kind: 'error', text: '已取消。若你已在服务商页面完成授权，可到其密钥页面吊销多余的 Key。' } }

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
    const rest = { ...route.query }; delete rest.llm_oauth
    void router.replace({ query: rest })
    const f = callbackFlagText(flag)
    if (f.kind === 'error') { clearPending(); result.value = { kind: 'error', text: f.text }; return }
    if (f.kind === 'ok') void wait(pending.state)
  }
})
onBeforeUnmount(() => { cancelled = true })
</script>

<template>
  <div v-if="preset" class="login col" :data-testid="tid('panel')">
    <div v-if="connectedHere" class="row wrap">
      <span class="chip success" :data-testid="tid('connected')"><Icon name="verified_user" sm />{{ sourceLabel }}</span>
      <button class="btn sm danger has-icon" :disabled="busy !== ''" :data-testid="tid('disconnect')" @click="confirmDisconnect = true"><Icon name="logout" sm />断开</button>
    </div>
    <span v-else-if="saved?.api_key_set && saved.preset_id === preset.id" class="muted hint" :data-testid="tid('source')">{{ sourceLabel }}</span>

    <div v-if="canOauth(preset) && !connectedHere" class="row wrap">
      <button v-if="busy !== 'waiting'" class="btn tonal has-icon" :disabled="busy !== ''" :data-testid="tid('start')" @click="start"><Icon name="login" sm :class="{ spin: busy === 'starting' }" />使用 {{ name }} 账号登录授权</button>
      <template v-else>
        <span class="muted" role="status" :data-testid="tid('waiting')"><Icon name="hourglass_top" sm class="spin" /> {{ isDesktop ? '请在浏览器中完成授权…' : '正在完成连接…' }}</span>
        <button class="btn sm" :data-testid="tid('cancel')" @click="cancel">取消</button>
      </template>
      <span class="muted hint grow">跳转到 {{ name }} 官方页面登录并授权；本应用不会接触你的密码，授权后自动生成专用 API Key 并测试连接。</span>
    </div>
    <div v-else-if="!canOauth(preset) && keyUrl" class="row wrap" :data-testid="tid('keylink')">
      <a class="btn outlined has-icon" :href="keyUrl" target="_blank" rel="noopener noreferrer"><Icon name="open_in_new" sm />打开 {{ name }} 的 API Key 页面</a>
      <span class="muted hint grow">{{ name }} 没有面向第三方应用的登录授权接口，无法自动配置：请在该页面登录并创建 Key，复制后粘贴到上方。</span>
    </div>

    <div v-if="result" class="banner" :class="result.kind === 'error' ? 'error' : result.kind === 'warn' ? 'warn' : ''" role="status" :data-testid="tid('result')"><Icon :name="result.kind === 'success' ? 'check_circle' : result.kind === 'warn' ? 'warning' : 'error'" /><span>{{ result.text }}</span></div>

    <ConfirmDialog v-if="confirmDisconnect" title="断开登录授权？" confirm-label="断开并删除密钥" danger :busy="busy === 'disconnecting'" @confirm="disconnect" @close="confirmDisconnect = false">
      <p>将删除本应用保存的 API Key，之后需要重新登录授权或手动填写才能使用 {{ name }}。</p>
      <p class="muted">本应用无法代你吊销这把 Key（{{ name }} 未提供此接口）；它在 {{ name }} 一侧仍然有效，如需作废请到其 Keys 页面删除。</p>
    </ConfirmDialog>
  </div>
</template>

<style scoped>
.login { gap: 8px; grid-column: 1 / -1; }
.hint { font-size: 12px; }
</style>
