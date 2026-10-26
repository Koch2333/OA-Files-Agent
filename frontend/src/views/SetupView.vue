<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch, nextTick } from 'vue'
import { useRouter } from 'vue-router'
import { api, ApiError, isMock, type LlmConfig, type MfaEnrolled, type SetupRequest } from '../api'
import { useAuth } from '../stores/auth'
import { authErrorMessage } from '../utils/authErrors'
import { openOneTime, setAcknowledged, canClose, type OneTimeState } from '../utils/oneTime'
import { passkeyAvailability } from '../utils/passkey'
import { llmStepActions, llmStepState, llmSummary, LLM_SKIP_HINT, policyPatchForWizard } from '../utils/setupLlm'
import { errMsg } from '../utils/errorInfo'
import { canAdvance, finishFailureStep, nextStep, originError, originsText, parseOrigins, prevStep, restoreStep, resumeStep, rpIdError, rpIdMatchesHost, SETUP_STEPS, stepsFor, type SetupFormLike, type SetupStepId } from '../utils/setup'
import AuthLayout from '../components/AuthLayout.vue'
import PasswordField from '../components/PasswordField.vue'
import PasskeyAddForm from '../components/PasskeyAddForm.vue'
import TotpEnrollPanel from '../components/TotpEnrollPanel.vue'
import OneTimeSecret from '../components/OneTimeSecret.vue'
import Icon from '../components/Icon.vue'
import LlmSettings from '../components/LlmSettings.vue'

const KEY = 'oa.setup.v1' // sessionStorage：只保存非敏感的表单项与步骤（绝不保存密码/令牌）
const router = useRouter()
const auth = useAuth()
const st = computed(() => auth.setup!)
const mock = isMock()

interface Form extends SetupFormLike { rpName: string; originsText: string; mfaPolicy: 'off' | 'admins' | 'all'; allowPassword: boolean }
function load(): { step?: SetupStepId; f?: Partial<Form> } {
  try { return JSON.parse(sessionStorage.getItem(KEY) ?? '{}') } catch { return {} }
}
const saved = load()
const f = reactive<Form>({
  mode: '', token: '', username: 'admin', displayName: '', password: '', password2: '', rpId: '', origins: [], minLen: 12, sessionIdle: 60, sessionMax: 12,
  rpName: 'OA 文件智能归档', originsText: '', mfaPolicy: 'admins', allowPassword: true, ...(saved.f ?? {}),
})
const step = ref<SetupStepId>('welcome')
const err = ref<string | null>(null)
const busy = ref(false)
const heading = ref<HTMLElement>()
const PHASE_KEY = 'oa.setup.phase' // sessionStorage：complete 之后最近到达的步骤编号（非敏感）。真正的恢复依据是服务端状态
const doneSetup = ref(false) // complete 已成功（或从服务端状态恢复）：之后只剩 MFA、大模型与完成
const resuming = ref(false)
const resumeErr = ref<string | null>(null)
const finishing = ref(false)
const llmError = ref<string | null>(null)
const regenBusy = ref(false)
const mfaRequired = ref(false)
const mfaTab = ref<'passkey' | 'totp'>('passkey')
const mfaDone = ref(false)
const codes = ref<OneTimeState | null>(null)
/** 大模型一步：已保存的全局配置、是否允许员工配置个人 API（默认关闭，与服务端默认一致） */
const llmSaved = ref<LlmConfig | null>(null)
const allowPersonal = ref(false)
const llmBusy = ref(false)
const llmState = computed(() => llmStepState(llmSaved.value))
const summaryLlm = computed(() => llmSummary(llmSaved.value, allowPersonal.value))

const steps = computed(() => stepsFor(f.mode))
const idx = computed(() => steps.value.indexOf(step.value))
const label = (id: SetupStepId) => SETUP_STEPS.find((s) => s.id === id)!.label
f.origins = parseOrigins(f.originsText)
watch(() => f.originsText, (t) => { f.origins = parseOrigins(t) })
const avail = computed(() => passkeyAvailability(st.value?.passkey_available))
const passkeyOk = computed(() => avail.value.ok)

function persist() {
  if (doneSetup.value) return
  const { password: _p, password2: _p2, token: _t, ...rest } = f
  try { sessionStorage.setItem(KEY, JSON.stringify({ step: doneSetup.value ? undefined : step.value, f: rest })) } catch { /* 隐私模式 */ }
}
watch([step, f], persist, { deep: true })
watch(step, (s) => { if (doneSetup.value) { try { sessionStorage.setItem(PHASE_KEY, s) } catch { /* 隐私模式 */ } } })

/** 已 complete 但向导未完成（服务端闸门）：从服务端状态恢复到正确的步骤，而不是依赖内存标志 */
async function resume() {
  resuming.value = true; resumeErr.value = null
  try {
    const me = auth.me ?? (await auth.refreshMe())
    if (!me) return // 守卫会把未登录的访问者送去 /login
    const sec = await api.getMySecurity()
    f.mode = me.policy.mode; f.username = me.user.username; f.mfaPolicy = me.policy.mfa_policy; f.allowPassword = me.policy.allow_password_login
    f.rpId = st.value.rp_id_suggestion ?? location.hostname
    doneSetup.value = true
    let saved: string | null = null
    try { saved = sessionStorage.getItem(PHASE_KEY) } catch { /* 隐私模式 */ }
    const r = resumeStep({ scope: me.scope, mfaRequiredByPolicy: sec.mfa_required_by_policy, hasMfa: sec.totp_enabled || sec.passkeys.length > 0, saved })
    mfaRequired.value = r.mfaRequired; mfaDone.value = r.mfaDone
    if (!passkeyOk.value || !f.allowPassword) mfaTab.value = passkeyOk.value ? 'passkey' : 'totp'
    step.value = r.step
  } catch (e) { resumeErr.value = errMsg(e) } finally { resuming.value = false }
}

onMounted(async () => {
  try { await auth.loadSetup() } catch (e) { err.value = e instanceof ApiError ? e.message : '无法读取初始化状态'; return }
  const s = st.value
  if (s.initialized && s.wizard_finished === false) { await resume(); await focusHeading(); return }
  if (!f.rpId) f.rpId = s.rp_id_suggestion ?? location.hostname
  if (!f.originsText) f.originsText = originsText([s.origin_suggestion ?? location.origin])
  if (!saved.f?.minLen) f.minLen = s.min_password_length
  step.value = restoreStep(saved.step, f, s.needs_token)
  if (f.mode === 'local' && !s.loopback) f.mode = ''
  await focusHeading()
})
async function focusHeading() { await nextTick(); heading.value?.focus() }

const stepError = computed(() => {
  if (!st.value) return null
  const e = canAdvance(step.value, f, st.value, passkeyOk.value)
  if (e) return e
  if (step.value === 'policy') {
    if (passkeyOk.value && !rpIdMatchesHost(f.rpId, location.hostname)) return `RP ID 必须是当前访问域名（${location.hostname}）或其父域，否则 Passkey 无法使用`
    if (passkeyOk.value && !f.origins.includes(location.origin)) return `允许的来源需包含你当前访问的地址（${location.origin}）`
    if (!f.allowPassword && !passkeyOk.value) return '当前环境不能使用 Passkey，不能关闭密码登录'
  }
  return null
})
const showErr = ref(false)
watch(step, () => { showErr.value = false; err.value = null; void focusHeading() })

async function next() {
  err.value = null
  if (stepError.value) { showErr.value = true; return }
  busy.value = true
  try {
    if (step.value === 'token' && f.token.trim()) await api.verifySetupToken(f.token.trim())
    if (step.value === 'policy') { await complete(); return }
    if (step.value === 'mode' && f.mode === 'local') { step.value = 'finish'; return }
    step.value = nextStep(step.value, f.mode)
  } catch (e) { err.value = e instanceof ApiError ? authErrorMessage(e) : String(e) } finally { busy.value = false }
}
function back() { err.value = null; step.value = prevStep(step.value, f.mode) }

function request(): SetupRequest {
  const base: SetupRequest = { mode: f.mode as 'enterprise' | 'local', ...(f.token.trim() ? { setup_token: f.token.trim() } : {}) }
  if (f.mode === 'local') return base
  return {
    ...base, admin: { username: f.username.trim(), display_name: f.displayName.trim(), password: f.password },
    rp_id: passkeyOk.value ? f.rpId.trim() : '', rp_name: f.rpName.trim() || undefined, origins: passkeyOk.value ? f.origins : [], mfa_policy: f.mfaPolicy, allow_password_login: f.allowPassword,
    session_idle_minutes: f.sessionIdle, session_max_hours: f.sessionMax, password_min_length: f.minLen,
  }
}
async function complete() {
  busy.value = true; err.value = null
  try {
    const r = await api.completeSetup(request())
    f.password = f.password2 = f.token = ''
    sessionStorage.removeItem(KEY)
    await auth.applyLogin(r)
    // 公开状态：local 模式立即完成；enterprise 要走完后续步骤（闸门由服务端强制）
    if (auth.setup) auth.setup = { ...auth.setup, initialized: true, wizard_finished: f.mode === 'local' }
    if (f.mode === 'local') { await router.replace('/'); return }
    doneSetup.value = true
    mfaRequired.value = r.status === 'enroll_mfa'
    if (!passkeyOk.value) mfaTab.value = 'totp'
    if (!f.allowPassword) mfaTab.value = 'passkey'
    step.value = 'mfa'
  } catch (e) {
    if (e instanceof ApiError && e.code === 'ALREADY_SETUP') { await auth.loadSetup(); await router.replace('/login'); return }
    if (e instanceof ApiError && e.status === 401) { step.value = 'token' }
    err.value = e instanceof ApiError ? authErrorMessage(e) : String(e)
  } finally { busy.value = false }
}
function onEnrolled(r: MfaEnrolled) {
  mfaDone.value = true
  if (r.recovery_codes.length) codes.value = openOneTime('recovery', '恢复码', r.recovery_codes)
  else step.value = 'llm'
}
function afterCodes() { if (codes.value && canClose(codes.value)) { codes.value = null; step.value = 'llm' } }
function skipMfa() { step.value = 'llm' }
function continueFromMfa() { step.value = 'llm' }
/** 首次展示的恢复码刷新后无法找回：已绑定 MFA 的恢复视图提供“重新生成”（旧码立即失效） */
async function regenerateCodes() {
  regenBusy.value = true; err.value = null
  try { codes.value = openOneTime('recovery', '新的恢复码', await auth.withReauth(() => api.regenerateRecoveryCodes())) }
  catch (e) { err.value = errMsg(e) } finally { regenBusy.value = false }
}
/** 大模型一步“继续”：只有开启“允许员工个人 API”时才写策略（默认即关闭）；失败时本步保持打开，可重试或显式跳过，绝不自动前进 */
async function finishLlm() {
  err.value = null; llmError.value = null
  const patch = policyPatchForWizard(allowPersonal.value)
  if (patch) {
    llmBusy.value = true
    try { await api.updateAiPolicy(patch) } catch (e) { llmError.value = errMsg(e); return } finally { llmBusy.value = false }
  }
  step.value = 'finish'
}
/** 显式跳过：这是用户点击“跳过”按钮的结果，稍后可在 设置 中配置 */
function skipLlm() { err.value = null; llmError.value = null; step.value = 'finish' }
const llmActions = computed(() => llmStepActions(llmState.value, llmError.value))

/** 最后一步“完成”：POST /v1/setup/finish 解除服务端闸门，然后刷新状态并进入系统 */
async function finishWizard() {
  finishing.value = true; err.value = null
  try {
    await auth.finishWizard()
    try { sessionStorage.removeItem(PHASE_KEY) } catch { /* 隐私模式 */ }
    await router.replace('/')
  } catch (e) {
    const back = finishFailureStep(e instanceof ApiError ? e : null)
    if (back === 'mfa') { mfaRequired.value = true; mfaDone.value = false; step.value = 'mfa' }
    err.value = errMsg(e)
  } finally { finishing.value = false }
}
const modeLabel = computed(() => (f.mode === 'local' ? '本机单用户（无需登录）' : '企业（必须登录）'))
const mfaLabel = { off: '不强制', admins: '管理员必须', all: '所有用户必须' }
</script>

<template>
  <AuthLayout title="首次安装向导" subtitle="只需几步：检查环境、创建管理员、设置安全策略。初始化完成后此页面不再出现。" wide>
    <ol class="stepper" aria-label="向导步骤">
      <li v-for="(id, i) in steps" :key="id" :class="{ on: id === step, done: i < idx }" :aria-current="id === step ? 'step' : undefined">
        <span class="dot">{{ i < idx ? '✓' : i + 1 }}</span><span class="lab">{{ label(id) }}</span>
      </li>
    </ol>
    <div v-if="mock" class="banner info t-label" role="note"><Icon name="science" sm />演示模式：向导不会改动任何真实系统。</div>
    <div v-if="!st && !err" class="skeleton" style="height: 160px" role="status" aria-label="加载中" />
    <div v-if="st && resuming" class="skeleton" style="height: 160px" role="status" aria-label="正在恢复向导进度" />
    <div v-else-if="st && resumeErr" class="banner error" role="alert" data-testid="setup-resume-error"><Icon name="error" /><div>无法读取初始化进度：{{ resumeErr }}<br /><button class="btn sm" @click="resume">重试</button></div></div>

    <template v-if="st && !resuming && !resumeErr">
      <h2 ref="heading" class="t-title-m" tabindex="-1">步骤 {{ idx + 1 }} / {{ steps.length }}：{{ label(step) }}</h2>

      <section v-if="step === 'welcome'" class="col">
        <p>欢迎使用。系统将检查运行环境，然后引导你创建第一个管理员账号。</p>
        <ul class="checks" aria-label="环境检查">
          <li v-for="c in st.checks" :key="c.name" :class="c.ok ? 'ok' : 'bad'"><Icon :name="c.ok ? 'check_circle' : 'warning'" fill /><div><strong>{{ c.name }}</strong><div class="muted">{{ c.message }}</div></div></li>
          <li :class="st.https ? 'ok' : 'bad'"><Icon :name="st.https ? 'check_circle' : 'warning'" fill /><div><strong>HTTPS</strong><div class="muted">{{ st.https ? '当前使用安全连接。' : '当前不是 HTTPS。生产环境请在反向代理上启用 HTTPS，否则 Cookie 与 Passkey 的安全性受限。' }}</div></div></li>
          <li :class="passkeyOk ? 'ok' : 'bad'" data-testid="setup-passkey-check"><Icon :name="passkeyOk ? 'check_circle' : 'warning'" fill /><div><strong>Passkey（WebAuthn）</strong><div class="muted">{{ passkeyOk ? '可用。' : avail.reason }}</div></div></li>
          <li :class="st.loopback ? 'ok' : 'neutral'"><Icon :name="st.loopback ? 'check_circle' : 'info'" fill /><div><strong>访问来源</strong><div class="muted">{{ st.loopback ? '来自本机回环地址：可以选择“本机单用户”模式。' : '来自网络：只能选择“企业”模式（本机模式仅限在服务器本机访问时选择）。' }}</div></div></li>
        </ul>
        <div v-if="st.checks.some((c) => !c.ok)" class="banner" role="note"><Icon name="warning" />部分检查未通过。可以继续，但相关功能可能不可用。</div>
        <div><button class="btn tonal has-icon" :disabled="busy" @click="auth.loadSetup()"><Icon name="refresh" sm />重新检查</button></div>
      </section>

      <section v-else-if="step === 'token'" class="col">
        <p v-if="st.needs_token">为防止服务暴露时被他人抢先初始化，需要输入<strong>初始化令牌</strong>。令牌只在服务日志和命令行里出现一次：Docker 使用 <code class="mono">docker compose logs api | grep SETUP</code>，或在服务器上运行 <code class="mono">oa-admin setup-token</code>。</p>
        <p v-else>当前来自本机访问，<strong>无需</strong>初始化令牌，可直接跳过。若你要创建“企业”模式且服务端要求令牌，也可以在此填写。</p>
        <div class="field"><label for="setup-token">初始化令牌{{ st.needs_token ? '' : '（可选）' }}</label>
          <input id="setup-token" v-model="f.token" class="input mono" autocomplete="off" autocapitalize="characters" spellcheck="false" :aria-invalid="showErr && !!stepError || undefined" @keydown.enter.prevent="next" /></div>
      </section>

      <section v-else-if="step === 'mode'" class="col">
        <fieldset class="modes">
          <legend class="sr-only">运行模式</legend>
          <label class="mode" :class="{ on: f.mode === 'enterprise' }"><input v-model="f.mode" type="radio" name="mode" value="enterprise" />
            <div><strong>企业模式（推荐用于服务器）</strong><p class="muted">所有人必须登录；账号分为管理员、操作员、只读三种角色；支持 Passkey 与验证器 App。</p></div></label>
          <label class="mode" :class="{ on: f.mode === 'local', off: !st.loopback }"><input v-model="f.mode" type="radio" name="mode" value="local" :disabled="!st.loopback" />
            <div><strong>本机模式（单机使用，免登录）</strong>
              <p class="muted">打开即用，所有访问者都视为管理员。<strong>代价：</strong>任何能打开此页面的人都拥有全部权限，因此仅在个人电脑、且不对外提供服务时使用。之后只能升级为企业模式（需创建管理员），不能从企业模式改回本机模式。</p>
              <p v-if="!st.loopback" class="t-label" style="color: var(--md-sys-color-error)">不可选：当前不是从服务器本机访问。</p></div></label>
        </fieldset>
      </section>

      <section v-else-if="step === 'admin'" class="col">
        <div class="grid two">
          <div class="field"><label for="su-user">用户名</label><input id="su-user" v-model="f.username" class="input" autocomplete="username" autocapitalize="off" spellcheck="false" /><span class="muted t-label">3–64 位：字母、数字与 . _ @ -</span></div>
          <div class="field"><label for="su-name">显示名</label><input id="su-name" v-model="f.displayName" class="input" autocomplete="name" maxlength="64" /></div>
        </div>
        <PasswordField id="su-pw" v-model="f.password" label="密码" autocomplete="new-password" strength :min-length="f.minLen" :username="f.username" />
        <PasswordField id="su-pw2" v-model="f.password2" label="确认密码" autocomplete="new-password" :invalid="!!f.password2 && f.password !== f.password2" />
        <p class="muted t-label">至少 {{ st.min_password_length }} 位。建议使用口令短语或密码管理器生成的随机密码。</p>
      </section>

      <section v-else-if="step === 'policy'" class="col">
        <p v-if="!passkeyOk" class="banner" role="note" data-testid="setup-no-passkey"><Icon name="info" />当前是 HTTP 或 IP 地址访问，Passkey 不可用，登录将使用密码 + TOTP，这里无需填写域名。以后绑定域名并启用 HTTPS（oa-agentctl domain ）后，可在设置里再启用 Passkey。</p>
        <template v-if="passkeyOk">
        <div class="banner" role="note"><Icon name="warning" />RP ID 与允许的来源决定 Passkey 能在哪个域名使用。<strong>之后修改域名会使已注册的 Passkey 全部失效</strong>（需重新绑定）。请确认这是用户将长期使用的访问地址。</div>
        <div class="grid two">
          <div class="field"><label for="su-rp">RP ID（域名）</label><input id="su-rp" v-model="f.rpId" class="input" autocapitalize="off" spellcheck="false" /><span class="muted t-label">不含协议与端口，例如 oa.example.com；必须是当前访问域名或其父域。</span></div>
          <div class="field"><label for="su-rpname">显示名称</label><input id="su-rpname" v-model="f.rpName" class="input" maxlength="64" /></div>
        </div>
        <div class="field"><label for="su-origins">允许的来源（每行一个，精确到协议+主机+端口）</label><textarea id="su-origins" v-model="f.originsText" class="input mono" rows="3" spellcheck="false" />
          <span v-for="o in f.origins.filter((x) => originError(x))" :key="o" class="t-label" style="color: var(--md-sys-color-error)">{{ o }}：{{ originError(o) }}</span></div>
        </template>
        <fieldset class="field"><legend class="lbl">多因素认证（MFA）要求</legend>
          <div class="seg" role="group" aria-label="MFA 要求"><button v-for="k in (['off', 'admins', 'all'] as const)" :key="k" type="button" :aria-pressed="f.mfaPolicy === k" @click="f.mfaPolicy = k">{{ mfaLabel[k] }}</button></div>
          <span class="muted t-label">“管理员必须”：管理员登录后若未绑定 MFA，会被要求先绑定。生产环境建议至少选择此项。</span></fieldset>
        <label class="switch"><input v-model="f.allowPassword" type="checkbox" role="switch" :disabled="!passkeyOk" />允许使用密码登录</label>
        <span class="muted t-label">关闭后所有人只能用 Passkey 登录；仅当每个账号都已绑定 Passkey 时再关闭。</span>
        <div class="grid three">
          <div class="field"><label for="su-idle">空闲超时（分钟）</label><input id="su-idle" v-model.number="f.sessionIdle" class="input" type="number" min="5" max="1440" /></div>
          <div class="field"><label for="su-max">会话最长（小时）</label><input id="su-max" v-model.number="f.sessionMax" class="input" type="number" min="1" max="720" /></div>
          <div class="field"><label for="su-min">密码最短长度</label><input id="su-min" v-model.number="f.minLen" class="input" type="number" :min="Math.max(8, st.min_password_length)" max="128" /></div>
        </div>
      </section>

      <section v-else-if="step === 'mfa'" class="col">
        <template v-if="!codes && !mfaDone">
          <p v-if="mfaRequired" class="banner" role="note"><Icon name="shield_lock" />安全策略要求管理员绑定多因素认证，请先完成绑定。</p>
          <p v-else class="muted">建议立即绑定 Passkey 或验证器 App。绑定第一个 MFA 时会生成一组<strong>一次性恢复码</strong>，用于设备丢失时登录。</p>
          <div v-if="f.allowPassword" class="seg" role="group" aria-label="MFA 方式"><button type="button" :aria-pressed="mfaTab === 'passkey'" @click="mfaTab = 'passkey'"><Icon name="passkey" sm />Passkey</button><button type="button" :aria-pressed="mfaTab === 'totp'" @click="mfaTab = 'totp'"><Icon name="qr_code_2" sm />验证器 App（TOTP）</button></div>
          <PasskeyAddForm v-if="mfaTab === 'passkey'" :server-available="st.passkey_available" primary default-name="管理员 Passkey" @done="onEnrolled" />
          <TotpEnrollPanel v-else @done="onEnrolled" @cancel="mfaTab = 'passkey'" />
        </template>
        <template v-else-if="codes">
          <p>绑定成功。请保存下面的恢复码：</p>
          <OneTimeSecret :state="codes" :who="f.username" @update:state="codes = $event" />
        </template>
        <div v-if="!codes && mfaDone" class="banner info" role="status" data-testid="setup-mfa-done"><Icon name="check_circle" /><div>已绑定多因素认证。首次展示的恢复码只显示一次，刷新页面后无法再查看；如果还没有保存，请重新生成（旧恢复码会立即失效）。<br /><button class="btn tonal sm" :disabled="regenBusy" data-testid="setup-regen-codes" @click="regenerateCodes">{{ regenBusy ? '生成中…' : '重新生成恢复码' }}</button></div></div>
      </section>

      <section v-else-if="step === 'llm'" class="col" data-testid="setup-llm">
        <p>AI 工作台需要一个<strong>大模型</strong>来理解任务说明并决定归档方式。填写服务商信息后点“测试连接”，再保存即可生效。这里配置的是<strong>全局大模型</strong>，全体员工共用。</p>
        <LlmSettings scope="global" embedded @saved="llmSaved = $event" />
        <label class="switch"><input v-model="allowPersonal" type="checkbox" role="switch" data-testid="setup-allow-personal" />允许员工配置个人大模型 API</label>
        <span class="muted t-label">开启后，操作员可以在“设置”里填写自己的模型服务商与密钥，只对其本人的任务生效；默认关闭，之后可在 设置 → AI 使用策略 中调整。</span>
        <div v-if="llmState === 'configured'" class="banner info" role="status" data-testid="setup-llm-ok"><Icon name="check_circle" />全局大模型已配置：{{ llmSaved?.model }}。</div>
        <div v-else class="banner" role="note" data-testid="setup-llm-skip-hint"><Icon name="info" />{{ LLM_SKIP_HINT }}。</div>
        <div v-if="llmError" class="banner error" role="alert" data-testid="setup-llm-error"><Icon name="error" /><div>{{ llmError }}<br /><span class="t-label">本步保持打开：可以重试，或明确选择“跳过”稍后在 设置 中配置。</span></div></div>
      </section>

      <section v-else class="col">
        <div class="banner info" role="status"><Icon name="check_circle" />{{ f.mode === 'local' ? '即将完成：确认后将以本机模式启用，无需登录。' : '最后一步：点击“完成”启用系统。在此之前系统的其他功能不会开放。' }}</div>
        <dl class="sum">
          <dt>运行模式</dt><dd>{{ modeLabel }}</dd>
          <template v-if="f.mode !== 'local'"><dt>管理员</dt><dd>{{ f.username }}</dd><template v-if="passkeyOk"><dt>RP ID</dt><dd>{{ f.rpId }}</dd></template><dt>MFA 要求</dt><dd>{{ mfaLabel[f.mfaPolicy] }}</dd><dt>全局大模型</dt><dd data-testid="setup-sum-llm">{{ summaryLlm.llm }}</dd><dt>个人大模型</dt><dd>{{ summaryLlm.personal }}</dd></template>
        </dl>
        <p v-if="f.mode !== 'local'" class="muted">之后可在“管理 → 安全策略”中调整策略，在“账号安全”中管理自己的 Passkey 与恢复码。</p>
      </section>

      <div v-if="showErr && stepError" class="banner error" role="alert"><Icon name="error" />{{ stepError }}</div>
      <div v-if="err" class="banner error" role="alert" data-testid="setup-error"><Icon name="error" />{{ err }}</div>

      <div class="row nav">
        <button v-if="idx > 0 && !doneSetup && step !== 'mfa' && step !== 'llm'" class="btn" :disabled="busy" @click="back">上一步</button>
        <span class="grow" />
        <template v-if="step === 'welcome' || step === 'mode' || step === 'admin' || step === 'policy'"><button class="btn filled" :disabled="busy" data-testid="setup-next" @click="next">{{ step === 'policy' ? (busy ? '正在初始化…' : '创建管理员并初始化') : '下一步' }}</button></template>
        <template v-else-if="step === 'token'">
          <button v-if="!st.needs_token && !f.token" class="btn tonal" :disabled="busy" @click="step = nextStep('token', f.mode)">跳过</button>
          <button class="btn filled" :disabled="busy || (st.needs_token && !f.token.trim())" data-testid="setup-next" @click="next">{{ busy ? '校验中…' : '验证并继续' }}</button>
        </template>
        <template v-else-if="step === 'llm'">
          <button v-if="llmActions.canSkip" class="btn tonal" :disabled="llmBusy" data-testid="setup-llm-skip" @click="skipLlm">跳过，稍后配置</button>
          <button v-if="llmActions.showRetry" class="btn filled" :disabled="llmBusy" data-testid="setup-llm-retry" @click="finishLlm">{{ llmBusy ? '保存中…' : '重试' }}</button>
          <button v-else class="btn filled" :disabled="llmBusy || !llmActions.canContinue" data-testid="setup-llm-next" @click="finishLlm">{{ llmBusy ? '保存中…' : '继续' }}</button>
        </template>
        <template v-else-if="step === 'mfa'">
          <button v-if="codes" class="btn filled" :disabled="!canClose(codes)" @click="afterCodes">继续</button>
          <button v-else-if="mfaDone" class="btn filled" data-testid="setup-mfa-next" @click="continueFromMfa">继续</button>
          <button v-else-if="!mfaRequired" class="btn" data-testid="setup-mfa-skip" @click="skipMfa">稍后设置</button>
        </template>
        <template v-else>
          <button v-if="f.mode === 'local' && !doneSetup" class="btn filled" :disabled="busy" data-testid="setup-next" @click="complete">{{ busy ? '正在启用…' : '确认并启用' }}</button>
          <button v-else class="btn filled" :disabled="finishing" data-testid="setup-enter" @click="finishWizard">{{ finishing ? '正在完成…' : err ? '重试完成' : '完成' }}</button>
        </template>
      </div>
    </template>
    <div v-else-if="err" class="banner error" role="alert"><Icon name="cloud_off" />{{ err }}</div>
  </AuthLayout>
</template>

<style scoped>
.stepper { list-style: none; margin: 0; padding: 0; display: flex; gap: 8px; flex-wrap: wrap; }
.stepper li { display: flex; align-items: center; gap: 6px; color: var(--md-sys-color-on-surface-variant); font-size: 13px; }
.stepper .dot { width: 24px; height: 24px; border-radius: 50%; display: grid; place-items: center; background: var(--c-surface-3); font-size: 12px; font-weight: 600; }
.stepper .on { color: var(--md-sys-color-on-surface); font-weight: 600; } .stepper .on .dot { background: var(--md-sys-color-primary); color: var(--md-sys-color-on-primary); }
.stepper .done .dot { background: var(--c-success-container); color: var(--c-on-success-container); }
@media (max-width: 800px) { .stepper .lab { display: none; } .stepper .on .lab { display: inline; } }
h2:focus { outline: 0; }
.checks { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 10px; }
.checks li { display: flex; gap: 12px; align-items: flex-start; } .checks .ok .icon { color: var(--c-success); } .checks .bad .icon { color: var(--c-warn); } .checks .neutral .icon { color: var(--md-sys-color-primary); }
.modes { border: 0; padding: 0; margin: 0; display: flex; flex-direction: column; gap: 12px; }
.mode { display: flex; gap: 12px; padding: 16px; border: 1px solid var(--md-sys-color-outline-variant); border-radius: var(--shape-md); cursor: pointer; }
.mode.on { border-color: var(--md-sys-color-primary); background: color-mix(in srgb, var(--md-sys-color-primary) 8%, transparent); } .mode.off { opacity: .6; cursor: not-allowed; }
.mode p { margin-top: 4px; }
.grid.two { grid-template-columns: repeat(2, minmax(0, 1fr)); } .grid.three { grid-template-columns: repeat(3, minmax(0, 1fr)); }
@media (max-width: 800px) { .grid.two, .grid.three { grid-template-columns: 1fr; } }
fieldset.field { border: 0; padding: 0; margin: 0; } legend.lbl { padding: 0; margin-bottom: 4px; }
.sum { display: grid; grid-template-columns: max-content 1fr; gap: 6px 16px; margin: 0; } .sum dt { color: var(--md-sys-color-on-surface-variant); } .sum dd { margin: 0; }
.nav { margin-top: 8px; }
</style>
