<script setup lang="ts">
import { computed, ref } from 'vue'
import { api, ApiError } from '../api'
import { useAuth } from '../stores/auth'
import { useUi } from '../stores/ui'
import { authErrorMessage } from '../utils/authErrors'
import { passwordStrength } from '../utils/password'
import Dialog from './Dialog.vue'
import PasswordField from './PasswordField.vue'
const props = defineProps<{ forced?: boolean }>()
const emit = defineEmits<{ close: []; changed: [] }>()
const auth = useAuth()
const ui = useUi()
const cur = ref(''), next = ref(''), next2 = ref('')
const busy = ref(false)
const err = ref<string | null>(null)
const minLen = computed(() => auth.policy?.password_min_length ?? 12)
const st = computed(() => passwordStrength(next.value, minLen.value, auth.user?.username))
const mismatch = computed(() => !!next2.value && next.value !== next2.value)
const can = computed(() => !!cur.value && st.value.ok && next.value === next2.value && !busy.value)
async function submit() {
  if (!can.value) return
  busy.value = true; err.value = null
  try {
    await api.changePassword(cur.value, next.value)
    cur.value = next.value = next2.value = ''
    await auth.refreshMe()
    ui.toast('密码已修改，其他设备上的登录已退出', 'success')
    emit('changed'); emit('close')
  } catch (e) { err.value = e instanceof ApiError ? authErrorMessage(e) : String(e) } finally { busy.value = false }
}
</script>
<template>
  <Dialog :title="forced ? '请先修改初始密码' : '修改密码'" width="480px" @close="!forced && !busy && emit('close')">
    <p v-if="forced" class="banner info">管理员为你设置了临时密码，必须先改为自己的密码才能继续使用。</p>
    <form class="col" @submit.prevent="submit">
      <input type="text" :value="auth.user?.username" autocomplete="username" hidden readonly tabindex="-1" aria-hidden="true" />
      <PasswordField id="cp-cur" v-model="cur" label="当前密码" autocomplete="current-password" autofocus />
      <PasswordField id="cp-new" v-model="next" label="新密码" autocomplete="new-password" strength :min-length="minLen" :username="auth.user?.username" />
      <PasswordField id="cp-new2" v-model="next2" label="确认新密码" autocomplete="new-password" :invalid="mismatch" />
      <p v-if="mismatch" class="t-label" style="color: var(--md-sys-color-error)" role="alert">两次输入的密码不一致</p>
      <div v-if="err" class="banner error" role="alert">{{ err }}</div>
      <button type="submit" hidden />
    </form>
    <template #actions>
      <button v-if="!forced" class="btn" :disabled="busy" @click="emit('close')">取消</button>
      <button v-else class="btn" :disabled="busy" @click="auth.logout().then(() => $router.replace('/login'))">退出登录</button>
      <button class="btn filled" :disabled="!can" @click="submit">修改密码</button>
    </template>
  </Dialog>
</template>
