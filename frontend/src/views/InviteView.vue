<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { api, ApiError, type Invitation } from '../api'
import { useAuth } from '../stores/auth'
import { authErrorMessage } from '../utils/authErrors'
import { createPasskey, passkeyAvailability } from '../utils/passkey'
import { fmtTime } from '../utils/format'
import { isUserCancel, webauthnErrorMessage } from '../utils/webauthn'
import AuthLayout from '../components/AuthLayout.vue'
import Icon from '../components/Icon.vue'
const props = defineProps<{ token: string }>()
const router = useRouter()
const auth = useAuth()
const inv = ref<Invitation | null>(null)
const loading = ref(true)
const invalid = ref(false)
const name = ref('')
const busy = ref(false)
const err = ref<string | null>(null)
const avail = passkeyAvailability()
onMounted(async () => {
  try { inv.value = await api.getInvitation(props.token) } catch (e) {
    // 无效/过期统一 404，不区分原因；其他错误（限速、网络）如实显示
    if (e instanceof ApiError && e.status === 404) invalid.value = true
    else err.value = e instanceof ApiError ? authErrorMessage(e) : String(e)
  } finally { loading.value = false }
})
async function accept() {
  busy.value = true; err.value = null
  try {
    const b = await api.invitationPasskeyBegin(props.token)
    const cred = await createPasskey(b.options)
    const r = await api.invitationPasskeyFinish(props.token, b.challenge_id, cred, name.value.trim() || undefined)
    await auth.applyLogin(r)
    await router.replace('/')
  } catch (e) {
    err.value = e instanceof ApiError ? authErrorMessage(e) : isUserCancel(e) ? '已取消，可以重试。' : webauthnErrorMessage(e, 'register')
    if (e instanceof ApiError && e.status === 404) invalid.value = true
  } finally { busy.value = false }
}
</script>
<template>
  <AuthLayout title="接受邀请" subtitle="管理员邀请你创建账号。注册一个 Passkey 后即可登录，无需设置密码。">
    <div v-if="loading" class="skeleton" style="height: 120px" role="status" aria-label="加载中" />
    <div v-else-if="invalid" class="banner error" role="alert" data-testid="invite-invalid"><Icon name="link_off" />邀请链接无效或已过期。请联系管理员重新生成邀请。</div>
    <template v-else-if="inv">
      <dl class="sum">
        <dt>用户名</dt><dd data-testid="invite-username">{{ inv.username }}</dd>
        <dt>显示名</dt><dd>{{ inv.display_name }}</dd>
        <dt>有效期至</dt><dd>{{ fmtTime(inv.expires_at) }}</dd>
      </dl>
      <div v-if="!avail.ok || !inv.passkey_available" class="banner" role="alert"><Icon name="warning" />{{ avail.reason ?? '服务器当前访问域名/协议不满足 Passkey 要求。' }}请改用 HTTPS 地址打开此链接，或联系管理员。</div>
      <form v-else class="col" @submit.prevent="accept">
        <div class="field"><label for="inv-name">Passkey 名称（便于以后识别）</label><input id="inv-name" v-model="name" class="input" maxlength="64" placeholder="例如：办公电脑" autocomplete="off" /></div>
        <div v-if="err" class="banner error" role="alert"><Icon name="error" />{{ err }}</div>
        <button type="submit" class="btn filled has-icon" :disabled="busy" data-testid="invite-accept"><Icon name="passkey" sm />{{ busy ? '请在系统弹窗中验证…' : '注册 Passkey 并登录' }}</button>
      </form>
    </template>
    <div v-else-if="err" class="banner error" role="alert">{{ err }}</div>
    <RouterLink to="/login" class="t-label">已有账号？去登录</RouterLink>
  </AuthLayout>
</template>
<style scoped>.sum { display: grid; grid-template-columns: max-content 1fr; gap: 6px 16px; margin: 0; } .sum dt { color: var(--md-sys-color-on-surface-variant); } .sum dd { margin: 0; }</style>
