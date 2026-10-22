<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api, ApiError, isMock, type LoginResult } from '../api'
import { useAuth } from '../stores/auth'
import { authErrorMessage } from '../utils/authErrors'
import { safeRedirect } from '../utils/authz'
import { assertPasskey, autofillSupported, cancelPasskeyCeremony, passkeyAvailability } from '../utils/passkey'
import { normalizeRecoveryCode } from '../utils/totp'
import { isUserCancel, webauthnErrorMessage } from '../utils/webauthn'
import AuthLayout from '../components/AuthLayout.vue'
import PasswordField from '../components/PasswordField.vue'
import TotpInput from '../components/TotpInput.vue'
import Icon from '../components/Icon.vue'

type Method = 'totp' | 'passkey' | 'recovery'
const route = useRoute()
const router = useRouter()
const auth = useAuth()
const mock = isMock()

const username = ref('')
const password = ref('')
const busy = ref(false)
const err = ref<string | null>(null)
const cooldown = ref(0)
let timer = 0

const stage = ref<'creds' | 'mfa'>('creds')
const mfaToken = ref('')
const methods = ref<Method[]>([])
const method = ref<Method>('totp')
const recovery = ref('')
const totp = ref<InstanceType<typeof TotpInput>>()

const avail = passkeyAvailability(auth.setup?.passkey_available)
/** 策略只有登录后才能读取（/auth/me）；未知时同时显示两种登录方式，由后端最终裁决。 */
const showPassword = computed(() => auth.policy?.allow_password_login !== false)
const expired = computed(() => route.query.expired === '1' || auth.sessionExpired)

function startCooldown(sec?: number) {
  window.clearInterval(timer)
  cooldown.value = Math.min(Math.ceil(sec ?? 0), 3600)
  if (cooldown.value <= 0) return
  timer = window.setInterval(() => { cooldown.value--; if (cooldown.value <= 0) window.clearInterval(timer) }, 1000)
}
function fail(e: unknown, ceremony: 'login' | 'register' = 'login') {
  if (e instanceof ApiError) { err.value = authErrorMessage(e); if (e.status === 429) startCooldown(e.retryAfter) }
  else if (isUserCancel(e)) err.value = null
  else err.value = webauthnErrorMessage(e, ceremony)
}

async function complete(r: LoginResult) {
  if (r.status === 'mfa_required') {
    mfaToken.value = r.mfa_token ?? ''
    methods.value = (r.methods?.length ? r.methods : ['totp', 'recovery']) as Method[]
    if (!methods.value.includes('recovery')) methods.value.push('recovery')
    method.value = methods.value.includes('totp') ? 'totp' : methods.value[0]
    stage.value = 'mfa'; password.value = ''; err.value = null
    return
  }
  cancelPasskeyCeremony()
  await auth.applyLogin(r)
  password.value = ''
  if (r.status === 'enroll_mfa') await router.replace({ path: '/account/security', query: { enroll: '1' } })
  else await router.replace(safeRedirect(route.query.redirect))
}

async function submitPassword() {
  if (busy.value || cooldown.value > 0 || !username.value.trim() || !password.value) return
  busy.value = true; err.value = null
  try { await complete(await api.login(username.value.trim(), password.value)) } catch (e) { fail(e) } finally { busy.value = false }
}
async function passkeyLogin() {
  busy.value = true; err.value = null
  try {
    const b = await api.passkeyLoginBegin()
    const cred = await assertPasskey(b.options)
    await complete(await api.passkeyLoginFinish(b.challenge_id, cred))
  } catch (e) { fail(e) } finally { busy.value = false }
}
async function conditionalLogin() {
  try {
    if (!(await autofillSupported())) return
    const b = await api.passkeyLoginBegin()
    const cred = await assertPasskey(b.options, { autofill: true })
    busy.value = true
    await complete(await api.passkeyLoginFinish(b.challenge_id, cred))
  } catch (e) { if (!isUserCancel(e)) fail(e) } finally { busy.value = false }
}

async function submitTotp(code: string) {
  if (busy.value) return
  busy.value = true; err.value = null
  try { await complete(await api.mfaTotp(mfaToken.value, code)) } catch (e) { fail(e); totp.value?.clear() } finally { busy.value = false }
}
async function submitRecovery() {
  const code = normalizeRecoveryCode(recovery.value)
  if (busy.value || !code) return
  busy.value = true; err.value = null
  try { await complete(await api.mfaRecovery(mfaToken.value, code)) } catch (e) { fail(e) } finally { busy.value = false }
}
async function mfaPasskey() {
  busy.value = true; err.value = null
  try {
    const b = await api.mfaPasskeyBegin(mfaToken.value)
    const cred = await assertPasskey(b.options)
    await complete(await api.mfaPasskeyFinish(mfaToken.value, b.challenge_id, cred))
  } catch (e) { fail(e) } finally { busy.value = false }
}
function back() { stage.value = 'creds'; mfaToken.value = ''; err.value = null; recovery.value = ''; void conditionalLogin() }

const LABEL: Record<Method, string> = { totp: '验证器 App', passkey: 'Passkey', recovery: '恢复码' }
onMounted(() => { if (showPassword.value || avail.ok) void conditionalLogin() })
onBeforeUnmount(() => { window.clearInterval(timer); cancelPasskeyCeremony() })
</script>

<template>
  <AuthLayout :title="stage === 'creds' ? '登录' : '验证身份'" :subtitle="stage === 'mfa' ? '请完成第二步验证。验证请求 5 分钟内有效。' : undefined">
    <div v-if="expired && stage === 'creds'" class="banner info" role="status"><Icon name="schedule" />登录已过期或在其他位置被退出，请重新登录。</div>
    <div v-if="auth.initError" class="banner error" role="alert"><Icon name="cloud_off" /><div>{{ auth.initError }}<br /><button class="btn sm" @click="auth.init()">重试</button></div></div>

    <template v-if="stage === 'creds'">
      <form v-if="showPassword" class="col" novalidate @submit.prevent="submitPassword">
        <div class="field"><label for="login-user">用户名</label>
          <input id="login-user" v-model="username" class="input" name="username" autocomplete="username webauthn" autocapitalize="off" spellcheck="false" autofocus :aria-invalid="!!err || undefined" /></div>
        <PasswordField id="login-pw" v-model="password" label="密码" autocomplete="current-password" :invalid="!!err" />
        <div v-if="err" class="banner error" role="alert" data-testid="login-error"><Icon name="error" />{{ err }}</div>
        <div v-if="cooldown > 0" class="muted" role="status">请等待 {{ cooldown }} 秒后再试。</div>
        <button type="submit" class="btn filled" :disabled="busy || cooldown > 0 || !username.trim() || !password">{{ busy ? '登录中…' : '登录' }}</button>
      </form>
      <div v-else class="banner info" role="note"><Icon name="passkey" />管理员已关闭密码登录，请使用 Passkey 登录。</div>

      <template v-if="showPassword"><div class="row sep muted"><hr class="divider grow" />或<hr class="divider grow" /></div></template>
      <button class="btn tonal has-icon" type="button" data-testid="passkey-login" :disabled="busy || !avail.ok" @click="passkeyLogin"><Icon name="passkey" sm />使用 Passkey 登录</button>
      <p v-if="avail.reason" class="muted t-label" role="note" data-testid="passkey-unavailable"><Icon name="info" sm /> Passkey 不可用：{{ avail.reason }}</p>
      <p v-else class="muted t-label">无需输入用户名；使用设备的指纹、面容、PIN 或安全密钥验证。</p>
      <div v-if="!showPassword && err" class="banner error" role="alert">{{ err }}</div>
      <p v-if="mock" class="banner info t-label" role="note"><Icon name="science" sm />演示账号：admin（含 TOTP）、operator、viewer；密码 demo-password；验证码 123456；恢复码 DEMO1-DEMO2。</p>
    </template>

    <template v-else>
      <div class="seg" role="group" aria-label="验证方式"><button v-for="m in methods" :key="m" type="button" :aria-pressed="method === m" @click="method = m; err = null">{{ LABEL[m] }}</button></div>
      <div v-if="method === 'totp'" class="col">
        <TotpInput id="mfa-code" ref="totp" label="验证器 App 中的 6 位验证码" :busy="busy" autofocus @submit="submitTotp" />
      </div>
      <div v-else-if="method === 'passkey'" class="col">
        <p class="muted">使用已注册的 Passkey 完成验证。</p>
        <button class="btn filled has-icon" type="button" :disabled="busy || !avail.ok" @click="mfaPasskey"><Icon name="passkey" sm />验证 Passkey</button>
        <p v-if="avail.reason" class="muted t-label">{{ avail.reason }}</p>
      </div>
      <form v-else class="col" @submit.prevent="submitRecovery">
        <div class="field"><label for="mfa-recovery">恢复码</label><input id="mfa-recovery" v-model="recovery" class="input mono" autocomplete="off" autocapitalize="characters" spellcheck="false" placeholder="XXXXX-XXXXX" autofocus /></div>
        <p class="muted t-label">每个恢复码只能使用一次。</p>
        <button class="btn filled" type="submit" :disabled="busy || !recovery.trim()">验证</button>
      </form>
      <div v-if="err" class="banner error" role="alert" data-testid="mfa-error"><Icon name="error" />{{ err }}</div>
      <button class="btn has-icon" type="button" :disabled="busy" @click="back"><Icon name="arrow_back" sm />返回</button>
    </template>
  </AuthLayout>
</template>
<style scoped>.sep { gap: 12px; }</style>
