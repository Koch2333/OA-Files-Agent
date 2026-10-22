<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { api, ApiError, type SecuritySettings } from '../api'
import { useAuth } from '../stores/auth'
import { useUi } from '../stores/ui'
import { authErrorMessage } from '../utils/authErrors'
import { buildPatch, rpChanged, toForm, validateSettings, type SettingsForm } from '../utils/securitySettings'
import { useAsync } from '../utils/useAsync'
import PageHeader from '../components/PageHeader.vue'
import StateView from '../components/StateView.vue'
import Icon from '../components/Icon.vue'

const auth = useAuth()
const ui = useUi()
const r = useAsync(() => api.adminGetSecuritySettings())
const orig = ref<SecuritySettings | null>(null)
const f = reactive<SettingsForm>({ rpId: '', rpName: '', originsText: '', mfaPolicy: 'admins', allowPassword: true, idle: 60, max: 12, minLen: 12, lockThreshold: 5, lockMinutes: 15 })
const confirmReset = ref(false)
const busy = ref(false)
const err = ref<string | null>(null)
const touched = ref(false)

async function load() { await r.run(); if (r.data.value) apply(r.data.value) }
function apply(s: SecuritySettings) { orig.value = s; Object.assign(f, toForm(s)); confirmReset.value = false; err.value = null; touched.value = false }
onMounted(load)

const errors = computed(() => validateSettings(f))
const changedRp = computed(() => !!orig.value && rpChanged(orig.value, f))
const patch = computed(() => (orig.value ? buildPatch(orig.value, f, confirmReset.value) : {}))
const dirty = computed(() => Object.keys(patch.value).filter((k) => k !== 'confirm_passkey_reset').length > 0)
const canSave = computed(() => dirty.value && !Object.keys(errors.value).length && (!changedRp.value || confirmReset.value) && !busy.value)

async function save() {
  touched.value = true
  if (!canSave.value || !orig.value) return
  busy.value = true; err.value = null
  try {
    const s = await auth.withReauth(() => api.adminUpdateSecuritySettings(patch.value), false)
    apply(s)
    await auth.refreshMe()
    ui.toast('安全策略已保存', 'success')
  } catch (e) { err.value = e instanceof ApiError ? authErrorMessage(e) : e instanceof Error ? e.message : String(e) } finally { busy.value = false }
}
const bad = (k: string) => (touched.value || dirty.value) && errors.value[k]
const MFA: [SecuritySettings['mfa_policy'], string, string][] = [['off', '不强制', '用户自行决定是否绑定。'], ['admins', '管理员必须', '管理员登录后必须绑定 Passkey 或验证器 App（推荐）。'], ['all', '所有用户必须', '所有账号都必须绑定 MFA，未绑定者登录后先进入绑定页。']]
</script>

<template>
  <PageHeader title="安全策略" subtitle="多因素认证、登录方式、会话时长、密码规则与 Passkey 域名。" />
  <StateView :loading="r.loading.value && !orig" :error="r.error.value" :empty="false" @retry="load">
    <form v-if="orig" class="col" novalidate @submit.prevent="save">
      <section class="card col">
        <h2 class="t-title-m">登录与多因素认证</h2>
        <div class="muted">运行模式：{{ orig.mode === 'local' ? '本机单用户' : '企业' }}</div>
        <fieldset class="field" style="border: 0; padding: 0; margin: 0"><legend class="lbl">MFA 要求</legend>
          <div class="seg" role="group" aria-label="MFA 要求"><button v-for="m in MFA" :key="m[0]" type="button" :aria-pressed="f.mfaPolicy === m[0]" @click="f.mfaPolicy = m[0]">{{ m[1] }}</button></div>
          <span class="muted t-label">{{ MFA.find((m) => m[0] === f.mfaPolicy)?.[2] }}</span></fieldset>
        <label class="switch"><input v-model="f.allowPassword" type="checkbox" role="switch" :disabled="!orig.passkey_available && f.allowPassword" />允许密码登录</label>
        <span class="muted t-label">关闭后只能使用 Passkey 登录。仍有账号没有 Passkey 时服务端会拒绝关闭，请先确认所有人已绑定。{{ orig.passkey_available ? '' : ' 当前环境不能使用 Passkey（需要 HTTPS 或 localhost），不能关闭。' }}</span>
      </section>

      <section class="card col">
        <h2 class="t-title-m">会话与锁定</h2>
        <div class="grid cols3">
          <div class="field"><label for="ss-idle">空闲超时（分钟）</label><input id="ss-idle" v-model.number="f.idle" class="input" type="number" min="5" max="1440" :aria-invalid="!!bad('idle') || undefined" aria-describedby="ss-idle-h" /><span id="ss-idle-h" class="t-label" :class="bad('idle') ? '' : 'muted'" :style="bad('idle') ? 'color: var(--md-sys-color-error)' : ''">{{ bad('idle') || '无操作多久后需重新登录（5–1440）' }}</span></div>
          <div class="field"><label for="ss-max">会话最长（小时）</label><input id="ss-max" v-model.number="f.max" class="input" type="number" min="1" max="720" :aria-invalid="!!bad('max') || undefined" aria-describedby="ss-max-h" /><span id="ss-max-h" class="t-label" :class="bad('max') ? '' : 'muted'" :style="bad('max') ? 'color: var(--md-sys-color-error)' : ''">{{ bad('max') || '登录后的绝对有效期（1–720）' }}</span></div>
          <div class="field"><label for="ss-min">密码最短长度</label><input id="ss-min" v-model.number="f.minLen" class="input" type="number" min="8" max="128" :aria-invalid="!!bad('minLen') || undefined" aria-describedby="ss-min-h" /><span id="ss-min-h" class="t-label" :class="bad('minLen') ? '' : 'muted'" :style="bad('minLen') ? 'color: var(--md-sys-color-error)' : ''">{{ bad('minLen') || '只对之后设置的密码生效（8–128）' }}</span></div>
          <div class="field"><label for="ss-lt">锁定阈值（次）</label><input id="ss-lt" v-model.number="f.lockThreshold" class="input" type="number" min="3" max="20" :aria-invalid="!!bad('lockThreshold') || undefined" aria-describedby="ss-lt-h" /><span id="ss-lt-h" class="t-label" :class="bad('lockThreshold') ? '' : 'muted'" :style="bad('lockThreshold') ? 'color: var(--md-sys-color-error)' : ''">{{ bad('lockThreshold') || '连续登录失败多少次后锁定（3–20）' }}</span></div>
          <div class="field"><label for="ss-lm">锁定时长（分钟）</label><input id="ss-lm" v-model.number="f.lockMinutes" class="input" type="number" min="1" max="1440" :aria-invalid="!!bad('lockMinutes') || undefined" aria-describedby="ss-lm-h" /><span id="ss-lm-h" class="t-label" :class="bad('lockMinutes') ? '' : 'muted'" :style="bad('lockMinutes') ? 'color: var(--md-sys-color-error)' : ''">{{ bad('lockMinutes') || '锁定持续时间（1–1440）' }}</span></div>
        </div>
      </section>

      <section class="card col">
        <h2 class="t-title-m">Passkey 域名（RP）</h2>
        <div class="banner" role="note"><Icon name="warning" />修改 RP ID 或允许的来源会使<strong>所有已注册的 Passkey 失效</strong>，用户需用密码/TOTP 登录后重新注册。</div>
        <div class="grid cols3">
          <div class="field"><label for="ss-rp">RP ID</label><input id="ss-rp" v-model="f.rpId" class="input" autocapitalize="off" spellcheck="false" :aria-invalid="!!bad('rpId') || undefined" /><span v-if="bad('rpId')" class="t-label" style="color: var(--md-sys-color-error)">{{ bad('rpId') }}</span></div>
          <div class="field"><label for="ss-rpn">显示名称</label><input id="ss-rpn" v-model="f.rpName" class="input" maxlength="64" /></div>
        </div>
        <div class="field"><label for="ss-or">允许的来源（每行一个）</label><textarea id="ss-or" v-model="f.originsText" class="input mono" rows="3" spellcheck="false" :aria-invalid="!!bad('origins') || undefined" /><span v-if="bad('origins')" class="t-label" style="color: var(--md-sys-color-error)">{{ bad('origins') }}</span></div>
        <label v-if="changedRp" class="row" data-testid="confirm-reset"><input v-model="confirmReset" type="checkbox" /><span>我了解这会使已注册的 Passkey 全部失效</span></label>
      </section>

      <div v-if="err" class="banner error" role="alert"><Icon name="error" />{{ err }}</div>
      <div class="row"><button type="submit" class="btn filled" :disabled="!canSave">{{ busy ? '保存中…' : '保存' }}</button><button type="button" class="btn" :disabled="busy || !dirty" @click="apply(orig)">还原</button></div>
    </form>
  </StateView>
</template>
<style scoped>.cols3 { grid-template-columns: repeat(3, minmax(0, 1fr)); } @media (max-width: 1000px) { .cols3 { grid-template-columns: 1fr; } }</style>
