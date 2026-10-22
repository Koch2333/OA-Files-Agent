<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { api, ApiError, type SessionInfo } from '../api'
import { useUi } from '../stores/ui'
import { authErrorMessage } from '../utils/authErrors'
import { fmtTime } from '../utils/format'
import { useAsync } from '../utils/useAsync'
import PageHeader from '../components/PageHeader.vue'
import StateView from '../components/StateView.vue'
import Icon from '../components/Icon.vue'
import ConfirmDialog from '../components/ConfirmDialog.vue'

const ui = useUi()
const userId = ref('')
const users = useAsync(() => api.adminListUsers({ page_size: 200 }))
const r = useAsync(() => api.adminListSessions(userId.value || undefined))
onMounted(() => { void users.run(); void r.run() })
watch(userId, r.run)
const target = ref<SessionInfo | null>(null)
const busy = ref(false)
const err = ref<string | null>(null)
const nameOf = computed(() => new Map((users.data.value?.items ?? []).map((u) => [u.id, u.display_name])))
async function revoke() {
  if (!target.value) return
  busy.value = true; err.value = null
  try { await api.adminRevokeSession(target.value.id); target.value = null; ui.toast('会话已吊销', 'success'); await r.run() } catch (e) { err.value = e instanceof ApiError ? authErrorMessage(e) : String(e) } finally { busy.value = false }
}
</script>
<template>
  <PageHeader title="会话管理" subtitle="查看所有登录会话并强制退出。吊销自己的当前会话会立即退出登录。">
    <template #actions>
      <select v-model="userId" class="input" aria-label="按用户筛选"><option value="">全部用户</option><option v-for="u in users.data.value?.items" :key="u.id" :value="u.id">{{ u.display_name }}（{{ u.username }}）</option></select>
      <button class="btn icon-only" aria-label="刷新" @click="r.run"><Icon name="refresh" /></button>
    </template>
  </PageHeader>
  <div class="card flat">
    <StateView :loading="r.loading.value && !r.data.value" :error="r.error.value" :empty="!r.data.value?.length" empty-title="没有会话" empty-icon="devices" @retry="r.run">
      <div class="table-wrap"><table class="tbl" aria-label="会话列表">
        <thead><tr><th>用户</th><th>设备</th><th>IP</th><th>验证</th><th>创建</th><th>最近活动</th><th>到期</th><th /></tr></thead>
        <tbody><tr v-for="s in r.data.value" :key="s.id">
          <td><div>{{ nameOf.get(s.user_id) ?? s.username }}</div><div class="muted mono">{{ s.username }}</div></td>
          <td class="ellipsis" style="max-width: 260px" :title="s.user_agent">{{ s.user_agent || '未知' }} <span v-if="s.current" class="chip success">当前会话</span></td>
          <td class="mono">{{ s.ip }}</td><td>{{ s.mfa }}</td><td class="muted nowrap">{{ fmtTime(s.created_at) }}</td><td class="muted nowrap">{{ fmtTime(s.last_seen_at) }}</td><td class="muted nowrap">{{ fmtTime(s.expires_at) }}</td>
          <td><button class="btn sm danger" @click="target = s; err = null">吊销</button></td></tr></tbody></table></div>
    </StateView>
  </div>
  <ConfirmDialog v-if="target" title="吊销该会话？" confirm-label="吊销" danger :busy="busy" :error="err" @confirm="revoke" @close="target = null">
    <p>用户「{{ target.username }}」在该设备上的登录将立即失效。<template v-if="target.current"><strong>这是你当前的会话，吊销后你将被退出登录。</strong></template></p>
  </ConfirmDialog>
</template>
