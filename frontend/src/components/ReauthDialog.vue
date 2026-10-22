<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { api, ApiError } from '../api'
import { useAuth } from '../stores/auth'
import { authErrorMessage } from '../utils/authErrors'
import { assertPasskey, passkeyAvailability } from '../utils/passkey'
import { isUserCancel, webauthnErrorMessage } from '../utils/webauthn'
import Dialog from './Dialog.vue'
import PasswordField from './PasswordField.vue'
import Icon from './Icon.vue'
/** “近期认证”：敏感操作前用密码或 Passkey 再验证一次。由 auth.withReauth 触发。 */
const auth = useAuth()
const password = ref('')
const busy = ref(false)
const err = ref<string | null>(null)
const hasPassword = ref(true)
const hasPasskey = ref(false)
const avail = passkeyAvailability()
onMounted(async () => {
  try { const s = await api.getMySecurity(); hasPassword.value = s.has_password; hasPasskey.value = s.passkeys.length > 0 } catch { /* 默认只显示密码 */ }
})
const msg = (e: unknown) => (e instanceof ApiError ? authErrorMessage(e) : String(e))
async function byPassword() {
  if (!password.value || busy.value) return
  busy.value = true; err.value = null
  try { const m = await api.reauth(password.value); password.value = ''; auth.reauthDone(m) } catch (e) { err.value = msg(e) } finally { busy.value = false }
}
async function byPasskey() {
  busy.value = true; err.value = null
  try {
    const b = await api.reauthPasskeyBegin()
    const cred = await assertPasskey(b.options)
    auth.reauthDone(await api.reauthPasskeyFinish(b.challenge_id, cred))
  } catch (e) { err.value = e instanceof ApiError ? authErrorMessage(e) : isUserCancel(e) ? '已取消。' : webauthnErrorMessage(e, 'reauth') } finally { busy.value = false }
}
</script>
<template>
  <Dialog title="需要重新验证身份" width="460px" @close="!busy && auth.reauthCancel()">
    <p class="muted">这是敏感操作，请再次验证你的身份（验证后 10 分钟内无需重复）。</p>
    <form v-if="hasPassword" class="col" @submit.prevent="byPassword">
      <PasswordField id="reauth-pw" v-model="password" label="当前密码" autocomplete="current-password" autofocus />
      <button class="btn filled" type="submit" :disabled="busy || !password">验证</button>
    </form>
    <button v-if="hasPasskey && avail.ok" class="btn tonal has-icon" type="button" :disabled="busy" @click="byPasskey"><Icon name="passkey" sm />使用 Passkey 验证</button>
    <div v-if="err" class="banner error" role="alert"><Icon name="error" />{{ err }}</div>
    <template #actions><button class="btn" :disabled="busy" @click="auth.reauthCancel()">取消</button></template>
  </Dialog>
</template>
