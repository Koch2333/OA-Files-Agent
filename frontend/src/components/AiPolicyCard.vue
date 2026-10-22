<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { api, type AiPolicy, type AiPolicyPatch } from '../api'
import { useAi } from '../stores/ai'
import { useUi } from '../stores/ui'
import { errMsg, useAsync } from '../utils/useAsync'
import StateView from './StateView.vue'
import ErrorBanner from './ErrorBanner.vue'
import Icon from './Icon.vue'

/** 管理员：AI 使用策略。每个开关即时保存（PATCH 只带被改动的字段），失败则回滚并显示错误。 */
const ui = useUi()
const ai = useAi()
const load = useAsync(() => api.getAiPolicy())
const policy = ref<AiPolicy | null>(null)
const busy = ref<keyof AiPolicyPatch | ''>('')
const err = ref<string | null>(null)
onMounted(async () => { await load.run(); policy.value = load.data.value ?? null })

type Key = keyof AiPolicyPatch
const ITEMS: { key: Key; title: string; desc: string; risk?: string }[] = [
  { key: 'allow_personal_llm', title: '允许员工配置个人大模型 API', desc: '开启后，操作员可以在“设置”中填写自己的模型服务商与密钥，只对其本人的任务生效；关闭后所有人只能使用全局大模型（已保存的个人配置不再生效）。' },
  { key: 'allow_personal_private_hosts', title: '允许个人 API 使用内网/本机地址（有安全风险，默认关闭）', desc: '关闭时，个人 API 只能填写公网 https 地址。开启后员工可让服务器访问内网或本机的任意地址，存在服务端请求伪造（SSRF）风险，请仅在确有需要（如自建本地模型）时开启。', risk: '安全风险' },
  { key: 'allow_auto_archive', title: '开放“全自动归档”', desc: '开启后，员工在 AI 工作台和智能归档页可以选择“全自动”：由大模型直接决定并归档，无人工审批（需勾选风险确认，可事后撤销）。关闭后只能使用人工审核。' },
]
async function toggle(k: Key, v: boolean) {
  if (!policy.value) return
  const before = policy.value[k]
  busy.value = k; err.value = null
  policy.value = { ...policy.value, [k]: v }
  try {
    policy.value = await api.updateAiPolicy({ [k]: v })
    ui.toast('AI 使用策略已更新', 'success')
    void ai.refresh()
  } catch (e) {
    policy.value = { ...policy.value!, [k]: before }
    err.value = errMsg(e)
  } finally { busy.value = '' }
}
</script>
<template>
  <section class="card col span" aria-label="AI 使用策略" data-testid="ai-policy-card">
    <h2 class="t-title-m">AI 使用策略</h2>
    <p class="muted">管理员控制员工如何使用大模型。修改立即生效，并记入审计日志。</p>
    <StateView :loading="load.loading.value && !policy" :error="load.error.value" :empty="false" :rows="3" @retry="load.run">
      <template v-if="policy">
        <ul class="pol">
          <li v-for="it in ITEMS" :key="it.key">
            <label class="switch">
              <input type="checkbox" role="switch" :checked="policy[it.key]" :disabled="!!busy" :data-testid="'policy-' + it.key" @change="toggle(it.key, ($event.target as HTMLInputElement).checked)" />
              <span class="grow"><b>{{ it.title }}</b><span v-if="it.risk" class="chip error risk"><Icon name="warning" sm />{{ it.risk }}</span><span class="muted desc">{{ it.desc }}</span></span>
            </label>
          </li>
        </ul>
        <ErrorBanner :message="err" />
      </template>
    </StateView>
  </section>
</template>
<style scoped>
.span { grid-column: 1 / -1; }
.pol { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 16px; }
.pol .switch { align-items: flex-start; width: 100%; }
.pol .switch input { margin-top: 2px; }
.desc { display: block; font-size: 13px; margin-top: 2px; }
.risk { margin-left: 8px; vertical-align: middle; }
</style>
