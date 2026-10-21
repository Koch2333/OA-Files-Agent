<script setup lang="ts">
import { onMounted } from 'vue'
import { api, isMock, setMock } from '../api'
import { useAsync } from '../utils/useAsync'
import { fmtTime } from '../utils/format'
import { useUi } from '../stores/ui'
import { SEEDS } from '../theme/theme'
import PageHeader from '../components/PageHeader.vue'
import StateView from '../components/StateView.vue'
import Icon from '../components/Icon.vue'

const ui = useUi()
const ready = useAsync(() => api.readiness())
const providers = useAsync(() => api.providerStatus())
const audit = useAsync(() => api.listAuditLogs({ page_size: 10 }))
onMounted(() => { void ready.run(); if (api.capabilities.providerStatus) void providers.run(); void audit.run() })
const dep = (s?: string) => (s === 'ok' ? ['正常', 'success'] : s ? [s, 'error'] : ['未知', ''])
</script>

<template>
  <PageHeader title="设置" />
  <div class="grid cols">
    <section class="card col">
      <h2 class="t-title-m">外观</h2>
      <div class="field"><span class="lbl">配色模式</span>
        <div class="seg" role="group" aria-label="配色模式"><button v-for="m in [['system', '跟随系统', 'brightness_auto'], ['light', '浅色', 'light_mode'], ['dark', '深色', 'dark_mode']] as const" :key="m[0]" :aria-pressed="ui.mode === m[0]" @click="ui.mode = m[0]"><Icon :name="m[2]" sm />{{ m[1] }}</button></div></div>
      <div class="field"><span class="lbl">主题色</span>
        <div class="row wrap"><button v-for="s in SEEDS" :key="s.hex" class="sw" :class="{ on: ui.seed === s.hex }" :style="{ background: s.hex }" :aria-label="s.name" :aria-pressed="ui.seed === s.hex" :title="s.name" @click="ui.seed = s.hex"><Icon v-if="ui.seed === s.hex" name="check" sm /></button></div></div>
    </section>

    <section class="card col">
      <div class="row"><h2 class="t-title-m grow">服务状态</h2><button class="btn icon-only sm" aria-label="刷新" @click="ready.run"><Icon name="refresh" sm /></button></div>
      <StateView :loading="ready.loading.value && !ready.data.value" :error="ready.error.value" :empty="false" :rows="2" @retry="ready.run">
        <template v-if="ready.data.value">
          <div class="row between"><span>API 服务</span><span class="chip" :class="ready.data.value.alive ? 'success' : 'error'">{{ ready.data.value.alive ? '在线' : '无法连接' }}</span></div>
          <div class="row between"><span>数据库</span><span class="chip" :class="dep(ready.data.value.database)[1]">{{ dep(ready.data.value.database)[0] }}</span></div>
          <div class="row between"><span>文档解析 Worker</span><span class="chip" :class="dep(ready.data.value.worker)[1]">{{ dep(ready.data.value.worker)[0] }}</span></div>
        </template>
      </StateView>
    </section>

    <section class="card col">
      <h2 class="t-title-m">模型 Provider</h2>
      <p v-if="!api.capabilities.providerStatus" class="muted">后端契约暂未提供 Provider 状态接口，此处无法展示。接入后仅显示名称、模型与可用状态，不显示密钥。</p>
      <StateView v-else :loading="providers.loading.value && !providers.data.value" :error="providers.error.value" :empty="!providers.data.value?.length" empty-title="未配置模型" :rows="1" @retry="providers.run">
        <div v-for="p in providers.data.value" :key="p.name" class="row between"><div><div>{{ p.name }}</div><div class="muted mono">{{ p.model }}</div></div><span class="chip" :class="p.state === 'ok' ? 'success' : 'error'">{{ { ok: '可用', unconfigured: '未配置', unavailable: '不可用' }[p.state] }}</span></div>
      </StateView>
    </section>

    <section class="card col">
      <h2 class="t-title-m">数据来源</h2>
      <label class="switch"><input type="checkbox" role="switch" :checked="isMock()" @change="setMock(($event.target as HTMLInputElement).checked)" />使用演示数据（不连接后端）</label>
      <p class="muted">开启后所有页面使用本地模拟数据，界面顶部会显示常驻提示。切换会刷新页面，真实数据与演示数据不会混用。</p>
      <hr class="divider" />
      <h2 class="t-title-m">隐私与存储</h2>
      <p class="muted">界面仅在浏览器本地保存主题偏好。文件内容只发送到你配置的后端；前端不直接调用模型，也不显示 Agent 的完整提示词。</p>
    </section>

    <section class="card flat span">
      <div class="row" style="padding: 16px 20px"><h2 class="t-title-m grow">最近审计日志</h2></div>
      <StateView :loading="audit.loading.value && !audit.data.value" :error="audit.error.value" :empty="!audit.data.value?.items.length" empty-title="暂无审计记录" empty-icon="history" @retry="audit.run">
        <table class="tbl"><thead><tr><th>时间</th><th>操作人</th><th>动作</th><th>说明</th></tr></thead><tbody><tr v-for="a in audit.data.value?.items" :key="a.id"><td class="nowrap muted">{{ fmtTime(a.timestamp) }}</td><td>{{ a.actor }}</td><td class="mono">{{ a.action }}</td><td>{{ a.summary }}</td></tr></tbody></table>
      </StateView>
    </section>
  </div>
</template>
<style scoped>
.cols { grid-template-columns: repeat(2, minmax(0, 1fr)); } .span { grid-column: 1 / -1; }
.sw { width: 40px; height: 40px; border-radius: 50%; border: 2px solid transparent; cursor: pointer; color: #fff; display: grid; place-items: center; } .sw.on { border-color: var(--md-sys-color-on-surface); }
@media (max-width: 1000px) { .cols { grid-template-columns: 1fr; } }
</style>
