<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { api, type MfaEnrolled, type TotpEnrollment } from '../api'
import { useAuth } from '../stores/auth'
import { authErrorMessage } from '../utils/authErrors'
import { copyText } from '../utils/clipboard'
import { formatManualKey } from '../utils/totp'
import { ApiError } from '../api'
import QrCode from './QrCode.vue'
import TotpInput from './TotpInput.vue'
import Icon from './Icon.vue'
const emit = defineEmits<{ done: [r: MfaEnrolled]; cancel: [] }>()
const auth = useAuth()
const enr = ref<TotpEnrollment | null>(null)
const err = ref<string | null>(null)
const busy = ref(false)
const copied = ref(false)
const input = ref<InstanceType<typeof TotpInput>>()
const msg = (e: unknown) => (e instanceof ApiError ? authErrorMessage(e) : String(e))
async function begin() {
  err.value = null; busy.value = true
  try { enr.value = await api.totpBegin() } catch (e) { err.value = msg(e) } finally { busy.value = false }
}
async function confirm(code: string) {
  if (!enr.value || busy.value) return
  busy.value = true; err.value = null
  try {
    const r = await api.totpConfirm(enr.value.challenge_id, code)
    auth.applyEnrolled(r)
    enr.value = null // 清除密钥
    emit('done', r)
  } catch (e) { err.value = msg(e); input.value?.clear() } finally { busy.value = false }
}
async function copyKey() { if (enr.value && await copyText(enr.value.secret)) { copied.value = true; setTimeout(() => { copied.value = false }, 2000) } }
onMounted(begin)
</script>
<template>
  <div class="col">
    <div v-if="busy && !enr" class="skeleton" style="height: 200px" role="status" aria-label="正在生成密钥" />
    <template v-if="enr">
      <ol class="steps muted">
        <li>用验证器 App（Microsoft Authenticator、Google Authenticator、1Password 等）扫描二维码。</li>
        <li>无法扫码时，选择“手动输入”并填写下面的密钥。</li>
        <li>输入 App 显示的 {{ enr.digits }} 位验证码完成绑定。</li>
      </ol>
      <div class="row wrap" style="align-items: flex-start; gap: 20px">
        <QrCode :value="enr.otpauth_uri" label="TOTP 绑定二维码" />
        <div class="col grow" style="min-width: 220px">
          <div class="field"><span class="lbl">手动密钥（{{ enr.algorithm }} · {{ enr.digits }} 位 · {{ enr.period }} 秒）</span>
            <div class="row"><code class="mono key grow" data-testid="totp-secret">{{ formatManualKey(enr.secret) }}</code>
              <button class="btn icon-only" type="button" aria-label="复制密钥" @click="copyKey"><Icon :name="copied ? 'check' : 'content_copy'" /></button></div></div>
          <TotpInput id="totp-confirm" ref="input" label="验证码" :busy="busy" autofocus @submit="confirm" />
        </div>
      </div>
    </template>
    <div v-if="err" class="banner error" role="alert"><Icon name="error" />{{ err }}</div>
    <div class="row"><button v-if="!enr && !busy" class="btn tonal" @click="begin">重新生成</button><span class="grow" /><button class="btn" :disabled="busy" @click="emit('cancel')">取消</button></div>
  </div>
</template>
<style scoped>
.steps { margin: 0; padding-left: 20px; display: flex; flex-direction: column; gap: 4px; }
.key { padding: 8px 12px; background: var(--c-surface-3); border-radius: var(--shape-sm); font-size: 14px; word-break: break-all; user-select: all; }
</style>
