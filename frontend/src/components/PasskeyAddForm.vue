<script setup lang="ts">
import { ref } from 'vue'
import { api, ApiError, type MfaEnrolled } from '../api'
import { useAuth } from '../stores/auth'
import { authErrorMessage } from '../utils/authErrors'
import { createPasskey, passkeyAvailability } from '../utils/passkey'
import { isUserCancel, webauthnErrorMessage } from '../utils/webauthn'
import Icon from './Icon.vue'
const props = defineProps<{ serverAvailable?: boolean; primary?: boolean; defaultName?: string }>()
const emit = defineEmits<{ done: [r: MfaEnrolled] }>()
const auth = useAuth()
const name = ref(props.defaultName ?? '')
const busy = ref(false)
const err = ref<string | null>(null)
const avail = passkeyAvailability(props.serverAvailable)
async function add() {
  busy.value = true; err.value = null
  try {
    const b = await api.passkeyRegisterBegin()
    const cred = await createPasskey(b.options)
    const r = await api.passkeyRegisterFinish(b.challenge_id, cred, name.value.trim() || undefined)
    auth.applyEnrolled(r)
    name.value = ''
    emit('done', r)
  } catch (e) {
    err.value = e instanceof ApiError ? authErrorMessage(e) : isUserCancel(e) ? '已取消。' : webauthnErrorMessage(e, 'register')
  } finally { busy.value = false }
}
</script>
<template>
  <div class="col">
    <div v-if="!avail.ok" class="banner info" role="note"><Icon name="info" />{{ avail.reason }}<br />可改用验证器 App（TOTP）。</div>
    <template v-else>
      <div class="field"><label for="pk-name">Passkey 名称（便于以后识别）</label><input id="pk-name" v-model="name" class="input" maxlength="64" placeholder="例如：办公电脑 / 手机" autocomplete="off" @keydown.enter.prevent="add" /></div>
      <div><button class="btn has-icon" :class="primary ? 'filled' : 'tonal'" :disabled="busy" @click="add"><Icon :name="busy ? 'progress_activity' : 'passkey'" :class="{ spin: busy }" sm />{{ busy ? '请在系统弹窗中验证…' : '添加 Passkey' }}</button></div>
    </template>
    <div v-if="err" class="banner error" role="alert"><Icon name="error" />{{ err }}</div>
  </div>
</template>
