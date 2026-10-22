<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api, ApiError, type MfaEnrolled, type Passkey } from '../api'
import { ReauthCancelled, useAuth } from '../stores/auth'
import { useUi } from '../stores/ui'
import { authErrorMessage } from '../utils/authErrors'
import { ROLE_LABEL } from '../utils/authz'
import { fmtTime } from '../utils/format'
import { openOneTime, type OneTimeState } from '../utils/oneTime'
import { useAsync } from '../utils/useAsync'
import PageHeader from '../components/PageHeader.vue'
import StateView from '../components/StateView.vue'
import Icon from '../components/Icon.vue'
import Dialog from '../components/Dialog.vue'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import ChangePasswordDialog from '../components/ChangePasswordDialog.vue'
import TotpEnrollPanel from '../components/TotpEnrollPanel.vue'
import PasskeyAddForm from '../components/PasskeyAddForm.vue'
import OneTimeDialog from '../components/OneTimeDialog.vue'

const auth = useAuth()
const ui = useUi()
const route = useRoute()
const router = useRouter()
const sec = useAsync(() => api.getMySecurity())
const sessions = useAsync(() => api.listMySessions())
const enrollMode = computed(() => auth.me?.scope === 'enroll_mfa')
const forcePw = computed(() => !!auth.user?.must_change_password)
const pwOpen = ref(false)
const totpOpen = ref(false)
const pkOpen = ref(false)
const confirm = ref<null | { kind: 'totp' | 'passkey' | 'recovery' | 'session'; passkey?: Passkey; sessionId?: string }>(null)
const rename = ref<{ p: Passkey; name: string } | null>(null)
const codes = ref<OneTimeState | null>(null)
const busy = ref(false)
const cerr = ref<string | null>(null)
const hadEnrollScope = ref(route.query.enroll === '1')

onMounted(() => { void sec.run(); void sessions.run() })

function report(e: unknown): string | null {
  if (e instanceof ReauthCancelled) return null
  return e instanceof ApiError ? authErrorMessage(e) : String(e)
}
async function act(fn: () => Promise<void>) {
  busy.value = true; cerr.value = null
  try { await fn() } catch (e) { cerr.value = report(e) } finally { busy.value = false }
}

async function afterEnroll(r: MfaEnrolled) {
  totpOpen.value = false; pkOpen.value = false
  await sec.run()
  if (r.recovery_codes.length) codes.value = openOneTime('recovery', '请保存你的恢复码', r.recovery_codes)
  else ui.toast('已绑定', 'success')
  await maybeLeaveEnroll()
}
async function maybeLeaveEnroll() {
  if (hadEnrollScope.value && auth.me?.scope === 'full' && !codes.value && !forcePw.value) { hadEnrollScope.value = false; ui.toast('多因素认证已绑定，欢迎使用', 'success'); await router.replace('/') }
}
async function closeCodes() { codes.value = null; await maybeLeaveEnroll() }

function doConfirm() {
  const c = confirm.value; if (!c) return
  void act(async () => {
    if (c.kind === 'totp') { await auth.withReauth(() => api.totpDisable()); ui.toast('已关闭验证器 App', 'success') }
    else if (c.kind === 'passkey' && c.passkey) { await auth.withReauth(() => api.deletePasskey(c.passkey!.id)); ui.toast('已删除 Passkey', 'success') }
    else if (c.kind === 'recovery') { const list = await auth.withReauth(() => api.regenerateRecoveryCodes()); codes.value = openOneTime('recovery', '新的恢复码', list) }
    else if (c.kind === 'session' && c.sessionId) { await api.revokeMySession(c.sessionId); ui.toast('已退出该设备', 'success') }
    confirm.value = null
    await Promise.all([sec.run(), sessions.run()])
  }).then(() => { if (cerr.value) { /* 错误显示在确认框内 */ } })
}
function closeConfirm() { confirm.value = null; cerr.value = null }
async function saveRename() {
  const r = rename.value; if (!r || !r.name.trim()) return
  await act(async () => { await api.renamePasskey(r.p.id, r.name.trim()); rename.value = null; await sec.run() })
}
async function reauthNow() { try { await auth.requestReauth() } catch { /* 取消 */ } }
const confirmTitle = computed(() => ({ totp: '关闭验证器 App？', passkey: '删除 Passkey？', recovery: '重新生成恢复码？', session: '退出该设备？' })[confirm.value?.kind ?? 'totp'])
const mfaMethods = computed(() => (sec.data.value ? (sec.data.value.totp_enabled ? 1 : 0) + sec.data.value.passkeys.length : 0))
</script>

<template>
  <PageHeader title="账号安全" :subtitle="auth.user ? `${auth.user.display_name}（${auth.user.username}） · ${ROLE_LABEL[auth.user.role]}` : ''" />
  <div v-if="enrollMode" class="banner" role="alert"><Icon name="shield_lock" /><div><strong>请先绑定多因素认证</strong><br />安全策略要求你的账号绑定 Passkey 或验证器 App 后才能使用系统其他功能。</div></div>
  <div v-if="cerr && !confirm" class="banner error" role="alert" style="margin-top: 12px"><Icon name="error" />{{ cerr }}</div>

  <StateView :loading="sec.loading.value && !sec.data.value" :error="sec.error.value" :empty="false" @retry="sec.run">
    <div v-if="sec.data.value" class="grid cols">
      <section class="card col">
        <div class="row"><h2 class="t-title-m grow">密码</h2></div>
        <p v-if="sec.data.value.has_password" class="muted">修改密码后，其他设备上的登录会被退出。</p>
        <p v-else class="muted">你的账号使用 Passkey 登录，没有密码。</p>
        <div><button v-if="sec.data.value.has_password" class="btn tonal has-icon" :disabled="enrollMode" @click="pwOpen = true"><Icon name="key" sm />修改密码</button></div>
        <hr class="divider" />
        <div class="row"><Icon :name="auth.me?.fresh ? 'verified_user' : 'lock_clock'" /><span class="grow muted">{{ auth.me?.fresh ? '近期认证有效：可进行敏感操作。' : '近期认证已过期：删除登录方式、生成恢复码前需重新验证身份。' }}</span>
          <button v-if="!auth.me?.fresh && !auth.isLocalMode" class="btn sm" @click="reauthNow">重新验证</button></div>
      </section>

      <section class="card col">
        <div class="row"><h2 class="t-title-m grow">验证器 App（TOTP）</h2><span class="chip" :class="sec.data.value.totp_enabled ? 'success' : ''">{{ sec.data.value.totp_enabled ? '已启用' : '未启用' }}</span></div>
        <p class="muted">登录时输入 App 里每 30 秒更新的 6 位验证码。</p>
        <div v-if="sec.data.value.totp_enabled"><button class="btn danger has-icon" @click="confirm = { kind: 'totp' }"><Icon name="mobile_off" sm />关闭</button></div>
        <div v-else><button class="btn tonal has-icon" data-testid="totp-enable" @click="totpOpen = true"><Icon name="qr_code_2" sm />绑定验证器 App</button></div>
      </section>

      <section class="card col span">
        <div class="row"><h2 class="t-title-m grow">Passkey</h2><button class="btn tonal has-icon" data-testid="passkey-add" @click="pkOpen = true"><Icon name="add" sm />添加 Passkey</button></div>
        <p class="muted">使用设备的指纹、面容、PIN 或安全密钥登录，无需记密码，且能防钓鱼。建议至少注册两个以防设备丢失。</p>
        <p v-if="!sec.data.value.passkeys.length" class="muted">还没有 Passkey。</p>
        <ul v-else class="list" aria-label="Passkey 列表">
          <li v-for="p in sec.data.value.passkeys" :key="p.id" class="row">
            <Icon name="passkey" /><div class="grow"><div><strong>{{ p.name }}</strong> <span v-if="p.backup_state" class="chip">已同步</span></div>
              <div class="muted t-label">添加于 {{ fmtTime(p.created_at) }} · {{ p.last_used_at ? '最近使用 ' + fmtTime(p.last_used_at) : '尚未使用' }}</div></div>
            <button class="btn icon-only" :aria-label="`重命名 ${p.name}`" @click="rename = { p, name: p.name }"><Icon name="edit" /></button>
            <button class="btn icon-only danger" :aria-label="`删除 ${p.name}`" @click="confirm = { kind: 'passkey', passkey: p }"><Icon name="delete" /></button>
          </li>
        </ul>
      </section>

      <section class="card col">
        <div class="row"><h2 class="t-title-m grow">恢复码</h2><span class="chip" :class="sec.data.value.recovery_codes_remaining ? '' : 'attention'">剩余 {{ sec.data.value.recovery_codes_remaining }} 个</span></div>
        <p class="muted">设备丢失时用于登录，每个只能使用一次。重新生成会让旧的恢复码全部作废。</p>
        <div><button class="btn tonal has-icon" :disabled="!mfaMethods" :title="mfaMethods ? '' : '请先绑定 TOTP 或 Passkey'" @click="confirm = { kind: 'recovery' }"><Icon name="autorenew" sm />重新生成</button></div>
      </section>

      <section class="card flat span">
        <div class="row" style="padding: 16px 20px"><h2 class="t-title-m grow">我的登录会话</h2><button class="btn icon-only sm" aria-label="刷新" @click="sessions.run"><Icon name="refresh" sm /></button></div>
        <StateView :loading="sessions.loading.value && !sessions.data.value" :error="sessions.error.value" :empty="!sessions.data.value?.length" empty-title="没有会话" @retry="sessions.run">
          <div class="table-wrap"><table class="tbl"><thead><tr><th>设备</th><th>IP</th><th>验证方式</th><th>最近活动</th><th>到期</th><th /></tr></thead>
            <tbody><tr v-for="s in sessions.data.value" :key="s.id">
              <td class="ellipsis" style="max-width: 280px" :title="s.user_agent">{{ s.user_agent || '未知' }} <span v-if="s.current" class="chip success">当前会话</span></td>
              <td class="mono">{{ s.ip }}</td><td>{{ s.mfa }}</td><td class="muted nowrap">{{ fmtTime(s.last_seen_at) }}</td><td class="muted nowrap">{{ fmtTime(s.expires_at) }}</td>
              <td><button v-if="!s.current" class="btn sm danger" @click="confirm = { kind: 'session', sessionId: s.id }">退出</button></td></tr></tbody></table></div>
        </StateView>
      </section>
    </div>
  </StateView>

  <ChangePasswordDialog v-if="forcePw" forced />
  <ChangePasswordDialog v-else-if="pwOpen" @close="pwOpen = false" />
  <Dialog v-if="totpOpen" title="绑定验证器 App" width="560px" @close="totpOpen = false"><TotpEnrollPanel @done="afterEnroll" @cancel="totpOpen = false" /></Dialog>
  <Dialog v-if="pkOpen" title="添加 Passkey" width="480px" @close="pkOpen = false"><PasskeyAddForm primary @done="afterEnroll" /><template #actions><button class="btn" @click="pkOpen = false">关闭</button></template></Dialog>
  <Dialog v-if="rename" title="重命名 Passkey" width="420px" @close="rename = null">
    <div class="field"><label for="pk-rename">名称</label><input id="pk-rename" v-model="rename.name" class="input" maxlength="64" @keydown.enter.prevent="saveRename" /></div>
    <div v-if="cerr" class="banner error" role="alert">{{ cerr }}</div>
    <template #actions><button class="btn" @click="rename = null">取消</button><button class="btn filled" :disabled="busy || !rename.name.trim()" @click="saveRename">保存</button></template>
  </Dialog>
  <ConfirmDialog v-if="confirm" :title="confirmTitle" :danger="confirm.kind !== 'recovery'" :busy="busy" :error="cerr" :confirm-label="confirm.kind === 'recovery' ? '重新生成' : confirm.kind === 'session' ? '退出' : '确认'" @confirm="doConfirm" @close="closeConfirm">
    <p v-if="confirm.kind === 'totp'">关闭后登录将不再需要验证器 App 验证码。若策略要求多因素认证，且这是你最后一种 MFA，将无法关闭。</p>
    <p v-else-if="confirm.kind === 'passkey'">将删除 Passkey「{{ confirm.passkey?.name }}」。至少需保留一种可登录方式。</p>
    <p v-else-if="confirm.kind === 'recovery'">将生成 10 个新的恢复码，<strong>旧恢复码立即作废</strong>。新恢复码只显示一次。</p>
    <p v-else>该设备上的登录将立即失效。</p>
  </ConfirmDialog>
  <OneTimeDialog v-if="codes" :state="codes" :who="auth.user?.username" @update:state="codes = $event" @close="closeCodes" />
</template>
<style scoped>
.cols { grid-template-columns: repeat(2, minmax(0, 1fr)); margin-top: 16px; } .span { grid-column: 1 / -1; }
.list { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 4px; } .list li { padding: 8px 0; border-bottom: 1px solid var(--md-sys-color-outline-variant); }
@media (max-width: 1000px) { .cols { grid-template-columns: 1fr; } }
</style>
