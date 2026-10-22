<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { api, ApiError, type AdminUser, type AdminUserCreate, type Role } from '../api'
import { useAuth } from '../stores/auth'
import { useUi } from '../stores/ui'
import { authErrorMessage } from '../utils/authErrors'
import { ROLE_LABEL } from '../utils/authz'
import { fmtTime } from '../utils/format'
import { openOneTime, type OneTimeState } from '../utils/oneTime'
import { usernameError } from '../utils/setup'
import { useAsync } from '../utils/useAsync'
import PageHeader from '../components/PageHeader.vue'
import StateView from '../components/StateView.vue'
import Pager from '../components/Pager.vue'
import Icon from '../components/Icon.vue'
import Dialog from '../components/Dialog.vue'
import Drawer from '../components/Drawer.vue'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import OneTimeDialog from '../components/OneTimeDialog.vue'

const auth = useAuth()
const ui = useUi()
const q = ref('')
const role = ref<Role | ''>('')
const status = ref<'' | 'active' | 'disabled' | 'locked'>('')
const page = ref(1), size = ref(20)
const list = useAsync(() => api.adminListUsers({ page: page.value, page_size: size.value, q: q.value.trim() || undefined, role: role.value || undefined, status: status.value || undefined }))
let t = 0
watch(q, () => { window.clearTimeout(t); t = window.setTimeout(() => { page.value = 1; void list.run() }, 300) })
watch([role, status, size], () => { page.value = 1; void list.run() })
watch(page, list.run)
onMounted(list.run)

const STATUS: Record<string, [string, string]> = { active: ['正常', 'success'], disabled: ['已停用', ''], locked: ['已锁定', 'attention'] }
const ROLES: Role[] = ['admin', 'operator', 'viewer']

/* ===== 创建 ===== */
const createOpen = ref(false)
const nu = ref<{ username: string; display_name: string; role: Role; credential: AdminUserCreate['credential'] }>({ username: '', display_name: '', role: 'viewer', credential: 'password' })
const busy = ref(false)
const err = ref<string | null>(null)
const secret = ref<{ state: OneTimeState; who: string } | null>(null)
const uErr = computed(() => (nu.value.username ? usernameError(nu.value.username) : null))
const msg = (e: unknown) => (e instanceof ApiError ? authErrorMessage(e) : String(e))
function openCreate() { nu.value = { username: '', display_name: '', role: 'viewer', credential: 'password' }; err.value = null; createOpen.value = true }
async function create() {
  if (usernameError(nu.value.username) || !nu.value.display_name.trim()) { err.value = usernameError(nu.value.username) ?? '请输入显示名'; return }
  busy.value = true; err.value = null
  try {
    const r = await api.adminCreateUser({ ...nu.value, username: nu.value.username.trim(), display_name: nu.value.display_name.trim() })
    createOpen.value = false
    if (r.temporary_password) secret.value = { state: openOneTime('password', '临时密码（仅显示一次）', [r.temporary_password]), who: r.user.username }
    else if (r.invite) secret.value = { state: openOneTime('invite', 'Passkey 邀请链接（仅显示一次）', [location.origin + r.invite.path]), who: r.user.username }
    ui.toast(`已创建用户 ${r.user.username}`, 'success')
    await list.run()
  } catch (e) { err.value = msg(e) } finally { busy.value = false }
}

/* ===== 详情 ===== */
const sel = ref<AdminUser | null>(null)
const edit = ref({ display_name: '', role: 'viewer' as Role })
const isSelf = computed(() => sel.value?.id === auth.user?.id)
function open(u: AdminUser) { sel.value = u; edit.value = { display_name: u.display_name, role: u.role }; err.value = null }
function applyUser(u: AdminUser) { sel.value = u; edit.value = { display_name: u.display_name, role: u.role }; void list.run() }
const dirty = computed(() => !!sel.value && (edit.value.display_name !== sel.value.display_name || edit.value.role !== sel.value.role))
async function save() {
  if (!sel.value || !dirty.value) return
  busy.value = true; err.value = null
  const patch: { display_name?: string; role?: Role } = {}
  if (edit.value.display_name !== sel.value.display_name) patch.display_name = edit.value.display_name.trim()
  if (edit.value.role !== sel.value.role) patch.role = edit.value.role
  try { applyUser(await api.adminUpdateUser(sel.value.id, patch)); ui.toast('已保存', 'success') } catch (e) { err.value = msg(e) } finally { busy.value = false }
}

/* ===== 需确认的操作 ===== */
interface Pending { title: string; body: string; label: string; danger?: boolean; run: () => Promise<void> }
const pending = ref<Pending | null>(null)
const perr = ref<string | null>(null)
function ask(p: Pending) { perr.value = null; pending.value = p }
async function runPending() {
  const p = pending.value; if (!p) return
  busy.value = true; perr.value = null
  try { await p.run(); pending.value = null } catch (e) { perr.value = msg(e) } finally { busy.value = false }
}
function toggleStatus(u: AdminUser) {
  const disable = u.status !== 'disabled'
  const go = async () => { applyUser(await api.adminUpdateUser(u.id, { status: disable ? 'disabled' : 'active' })); ui.toast(disable ? '已停用' : '已启用', 'success') }
  if (disable) ask({ title: '停用该用户？', body: `「${u.username}」将无法登录，现有会话立即失效。`, label: '停用', danger: true, run: go })
  else void go().catch((e) => { err.value = msg(e) })
}
function resetPassword(u: AdminUser) {
  ask({ title: '重置密码？', body: `将为「${u.username}」生成新的临时密码，并使其所有会话失效。该用户下次登录必须修改密码。`, label: '重置', danger: true, run: async () => {
    const pw = await api.adminResetPassword(u.id)
    secret.value = { state: openOneTime('password', '新的临时密码（仅显示一次）', [pw]), who: u.username }
    await refreshSel(u.id)
  } })
}
function resetMfa(u: AdminUser) {
  ask({ title: '重置多因素认证？', body: `将清除「${u.username}」的验证器 App、全部 Passkey 与恢复码，并使其会话失效。下次登录将按策略重新绑定。`, label: '重置 MFA', danger: true, run: async () => { applyUser(await api.adminResetMfa(u.id)); ui.toast('已重置 MFA', 'success') } })
}
function revoke(u: AdminUser) {
  ask({ title: '吊销全部会话？', body: `「${u.username}」在所有设备上的登录将立即失效。`, label: '吊销', danger: true, run: async () => { const n = await api.adminRevokeSessions(u.id); ui.toast(`已吊销 ${n} 个会话`, 'success') } })
}
function invite(u: AdminUser) {
  ask({ title: '生成 Passkey 邀请链接？', body: '链接 24 小时内有效，一次性使用；同一用户之前的邀请链接会作废。', label: '生成', run: async () => {
    const r = await api.adminCreateInvite(u.id)
    secret.value = { state: openOneTime('invite', 'Passkey 邀请链接（仅显示一次）', [location.origin + r.path]), who: u.username }
  } })
}
function remove(u: AdminUser) {
  ask({ title: '删除用户？', body: `将永久删除「${u.username}」及其 Passkey、恢复码与会话，无法恢复。历史审计记录保留。`, label: '删除', danger: true, run: async () => { await api.adminDeleteUser(u.id); sel.value = null; ui.toast('已删除', 'success'); await list.run() } })
}
async function unlock(u: AdminUser) { try { applyUser(await api.adminUnlockUser(u.id)); ui.toast('已解锁', 'success') } catch (e) { err.value = msg(e) } }
async function refreshSel(id: string) { try { applyUser(await api.adminGetUser(id)) } catch { /* 忽略 */ } }
const mfaText = (u: AdminUser) => [u.totp_enabled ? 'TOTP' : '', u.passkey_count ? `Passkey×${u.passkey_count}` : ''].filter(Boolean).join(' + ') || '未绑定'
</script>

<template>
  <PageHeader title="用户管理" subtitle="创建账号、分配角色、重置密码与多因素认证。没有自助注册。">
    <template #actions>
      <button class="btn icon-only" aria-label="刷新" @click="list.run"><Icon name="refresh" /></button>
      <button class="btn filled has-icon" data-testid="user-create" @click="openCreate"><Icon name="person_add" sm />新建用户</button>
    </template>
  </PageHeader>
  <div class="row wrap filters">
    <div class="search grow" style="min-width: 220px"><Icon name="search" /><input v-model="q" class="input" type="search" placeholder="搜索用户名或显示名" aria-label="搜索用户" /></div>
    <select v-model="role" class="input" aria-label="按角色筛选"><option value="">全部角色</option><option v-for="r in ROLES" :key="r" :value="r">{{ ROLE_LABEL[r] }}</option></select>
    <select v-model="status" class="input" aria-label="按状态筛选"><option value="">全部状态</option><option value="active">正常</option><option value="disabled">已停用</option><option value="locked">已锁定</option></select>
  </div>
  <div class="card flat">
    <StateView :loading="list.loading.value && !list.data.value" :error="list.error.value" :empty="!list.data.value?.items.length" empty-title="没有符合条件的用户" empty-icon="group" @retry="list.run">
      <div class="table-wrap"><table class="tbl" aria-label="用户列表">
        <thead><tr><th>用户</th><th>角色</th><th>状态</th><th>MFA</th><th>最近登录</th><th /></tr></thead>
        <tbody><tr v-for="u in list.data.value?.items" :key="u.id" class="clickable" tabindex="0" :aria-label="`查看 ${u.username}`" @click="open(u)" @keydown.enter.self="open(u)">
          <td><div><strong>{{ u.display_name }}</strong></div><div class="muted mono">{{ u.username }}</div></td>
          <td><span class="chip" :class="u.role === 'admin' ? 'progress' : ''">{{ ROLE_LABEL[u.role] }}</span></td>
          <td><span class="chip" :class="STATUS[u.status][1]">{{ STATUS[u.status][0] }}</span> <span v-if="u.must_change_password" class="chip attention">待改密</span></td>
          <td class="muted">{{ mfaText(u) }}</td><td class="muted nowrap">{{ u.last_login_at ? fmtTime(u.last_login_at) : '从未' }}</td>
          <td><button class="btn icon-only sm" :aria-label="`管理 ${u.username}`" @click.stop="open(u)"><Icon name="chevron_right" sm /></button></td></tr></tbody></table></div>
      <Pager v-model:page="page" v-model:page-size="size" :total="list.data.value?.total ?? 0" />
    </StateView>
  </div>

  <Dialog v-if="createOpen" title="新建用户" width="520px" @close="!busy && (createOpen = false)">
    <form class="col" @submit.prevent="create">
      <div class="field"><label for="nu-user">用户名</label><input id="nu-user" v-model="nu.username" class="input" autocomplete="off" autocapitalize="off" :aria-invalid="!!uErr || undefined" /><span class="t-label" :class="uErr ? '' : 'muted'" :style="uErr ? 'color: var(--md-sys-color-error)' : ''">{{ uErr ?? '3–64 位：字母、数字与 . _ @ -（不区分大小写）' }}</span></div>
      <div class="field"><label for="nu-name">显示名</label><input id="nu-name" v-model="nu.display_name" class="input" maxlength="64" autocomplete="off" /></div>
      <div class="field"><label for="nu-role">角色</label><select id="nu-role" v-model="nu.role" class="input"><option value="viewer">只读：查看任务、文件、台账</option><option value="operator">操作员：导入、审批、执行、文件管理</option><option value="admin">管理员：全部权限（含用户与设置）</option></select></div>
      <fieldset class="field" style="border: 0; padding: 0; margin: 0"><legend class="lbl">登录凭据</legend>
        <div class="seg" role="group" aria-label="登录凭据"><button type="button" :aria-pressed="nu.credential === 'password'" @click="nu.credential = 'password'">临时密码</button><button type="button" :aria-pressed="nu.credential === 'passkey_invite'" @click="nu.credential = 'passkey_invite'">Passkey 邀请链接</button></div>
        <span class="muted t-label">{{ nu.credential === 'password' ? '系统生成一次性临时密码，用户首次登录必须修改。' : '无密码账号：把邀请链接发给用户，24 小时内注册 Passkey 后生效。' }}</span></fieldset>
      <div v-if="err" class="banner error" role="alert">{{ err }}</div>
      <button type="submit" hidden />
    </form>
    <template #actions><button class="btn" :disabled="busy" @click="createOpen = false">取消</button><button class="btn filled" :disabled="busy" @click="create">创建</button></template>
  </Dialog>

  <Drawer v-if="sel" :title="sel.username" @close="sel = null">
    <div class="row wrap"><span class="chip" :class="STATUS[sel.status][1]">{{ STATUS[sel.status][0] }}</span><span class="chip">{{ ROLE_LABEL[sel.role] }}</span><span v-if="isSelf" class="chip progress">你自己</span></div>
    <div v-if="err" class="banner error" role="alert">{{ err }}</div>
    <div class="field"><label for="ud-name">显示名</label><input id="ud-name" v-model="edit.display_name" class="input" maxlength="64" /></div>
    <div class="field"><label for="ud-role">角色</label><select id="ud-role" v-model="edit.role" class="input"><option v-for="r in ROLES" :key="r" :value="r">{{ ROLE_LABEL[r] }}</option></select></div>
    <div><button class="btn filled" :disabled="!dirty || busy" @click="save">保存修改</button></div>
    <hr class="divider" />
    <dl class="sum">
      <dt>MFA</dt><dd>{{ mfaText(sel) }}</dd><dt>恢复码</dt><dd>剩余 {{ sel.recovery_codes_remaining }} 个</dd><dt>登录方式</dt><dd>{{ sel.has_password ? '密码' : '仅 Passkey' }}</dd>
      <dt>登录失败</dt><dd>{{ sel.failed_logins }} 次<template v-if="sel.locked_until">，锁定至 {{ fmtTime(sel.locked_until) }}</template></dd>
      <dt>最近登录</dt><dd>{{ sel.last_login_at ? fmtTime(sel.last_login_at) : '从未' }}</dd><dt>创建时间</dt><dd>{{ fmtTime(sel.created_at) }}</dd>
      <dt>待改密</dt><dd>{{ sel.must_change_password ? '是' : '否' }}</dd>
    </dl>
    <hr class="divider" />
    <h3 class="t-title-m">操作</h3>
    <div class="col acts">
      <button v-if="sel.status === 'locked'" class="btn tonal has-icon" @click="unlock(sel)"><Icon name="lock_open" sm />解锁</button>
      <button class="btn outlined has-icon" :disabled="isSelf && sel.status !== 'disabled'" :title="isSelf ? '不能停用自己' : ''" @click="toggleStatus(sel)"><Icon :name="sel.status === 'disabled' ? 'check_circle' : 'block'" sm />{{ sel.status === 'disabled' ? '启用' : '停用' }}</button>
      <button class="btn outlined has-icon" @click="resetPassword(sel)"><Icon name="password" sm />重置密码</button>
      <button class="btn outlined has-icon" @click="invite(sel)"><Icon name="link" sm />生成 Passkey 邀请链接</button>
      <button class="btn outlined has-icon" @click="resetMfa(sel)"><Icon name="phonelink_erase" sm />重置 MFA</button>
      <button class="btn outlined has-icon" @click="revoke(sel)"><Icon name="logout" sm />吊销全部会话</button>
      <button class="btn danger outlined has-icon" :disabled="isSelf" :title="isSelf ? '不能删除自己' : ''" @click="remove(sel)"><Icon name="delete" sm />删除用户</button>
    </div>
  </Drawer>

  <ConfirmDialog v-if="pending" :title="pending.title" :confirm-label="pending.label" :danger="pending.danger" :busy="busy" :error="perr" @confirm="runPending" @close="pending = null"><p>{{ pending.body }}</p></ConfirmDialog>
  <OneTimeDialog v-if="secret" :state="secret.state" :who="secret.who" @update:state="secret = { ...secret, state: $event }" @close="secret = null" />
</template>
<style scoped>
.filters { margin-bottom: 16px; }
.sum { display: grid; grid-template-columns: max-content 1fr; gap: 6px 16px; margin: 0; } .sum dt { color: var(--md-sys-color-on-surface-variant); } .sum dd { margin: 0; }
.acts .btn { justify-content: flex-start; }
</style>
