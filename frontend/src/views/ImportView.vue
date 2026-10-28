<script setup lang="ts">
import { uuid } from '../utils/uuid'
import { useAuth } from '../stores/auth'
import { useAi } from '../stores/ai'
import { computed, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { api, type StorageConnection } from '../api'
import { errMsg, useAsync } from '../utils/useAsync'
import { useImportFlow } from '../utils/useImportFlow'
import { FORMAT_EXTS, GROUP_LABELS, extsOfGroups } from '../utils/formats'
import { useUi } from '../stores/ui'
import PageHeader from '../components/PageHeader.vue'
import Icon from '../components/Icon.vue'
import FilePicker from '../components/FilePicker.vue'
import ErrorBanner from '../components/ErrorBanner.vue'
import UploadProgress from '../components/UploadProgress.vue'
import AutomationSelector from '../components/AutomationSelector.vue'
import AutoRiskDialog from '../components/AutoRiskDialog.vue'
import RemoteBrowserDialog from '../components/RemoteBrowserDialog.vue'

const auth = useAuth()
const ai = useAi()
const router = useRouter()
const ui = useUi()
// 选择/校验/运行方式/提交与工作台拖入区共用同一份逻辑（utils/useImportFlow.ts）
const flow = useImportFlow()
const { picked, templateId, instruction, submitting, err, auto, summary, llmOk } = flow
const instrOpen = ref(false)
const tpls = useAsync(() => api.listTemplates())
watch(() => ai.status, () => auto.revalidate())
// 存储连接是可选功能：接口不可用或没有可导入的连接时，不渲染来源面板
const remoteConns = ref<StorageConnection[]>([])
const remoteOpen = ref(false)
const remoteBusy = ref(false)
const remoteErr = ref<string | null>(null)
const importable = computed(() => remoteConns.value.filter((c) => c.enabled && c.use_for_import))
onMounted(() => { void tpls.run(); void ai.refresh(); if (auth.isAdmin) api.listStorageConnections().then((l) => { remoteConns.value = l }, () => { remoteConns.value = [] }) })

async function importRemote(p: { connectionId: string; paths: string[] }) {
  remoteBusy.value = true; remoteErr.value = null
  try {
    const job = await api.createRemoteImport(p.connectionId, p.paths, { templateId: templateId.value || null, idempotencyKey: uuid(), options: flow.currentOptions() })
    remoteOpen.value = false
    ui.toast('已创建归档任务', 'success')
    await router.push(`/tasks/${job.id}`)
  } catch (e) { remoteErr.value = errMsg(e); await flow.afterError(e) } finally { remoteBusy.value = false }
}
const submit = () => flow.submit()
const groups = GROUP_LABELS.map((g) => ({ label: g.label, icon: g.icon, exts: extsOfGroups(g.group) }))
const allExts = FORMAT_EXTS.join(' ')
</script>

<template>
  <PageHeader title="批量导入 / 智能归档" subtitle="导入后 Agent 自动解析、识别并生成归档计划；此步骤不会移动或修改任何原文件。" />

  <div class="layout">
    <section class="col">
      <FilePicker v-model="picked" />

      <div v-if="importable.length" class="card outlined row" data-testid="remote-source">
        <Icon name="cloud_sync" class="big-s" />
        <div class="grow"><div class="t-title-m">从存储连接导入</div><p class="muted" style="margin: 2px 0 0">直接从云存储或企业内部存储选择文件/文件夹，拉取到本机后走同样的识别与审批流程。</p></div>
        <button class="btn tonal has-icon" data-testid="remote-open" @click="remoteErr = null; remoteOpen = true"><Icon name="folder_open" sm />选择远端文件</button>
      </div>

      <ErrorBanner :message="err" />
    </section>

    <aside class="col">
      <section class="card col">
        <h2 class="t-title-m">归档设置</h2>
        <div class="field">
          <label for="tpl">自定义 OA 模板</label>
          <select id="tpl" v-model="templateId" class="input">
            <option value="">自动匹配（按优先级）</option>
            <option v-for="t in tpls.data.value?.filter((x) => x.enabled)" :key="t.id" :value="t.id">{{ t.name }}</option>
          </select>
          <span v-if="tpls.error.value" class="muted" style="color: var(--md-sys-color-error)">模板列表加载失败：{{ tpls.error.value }}</span>
        </div>

        <AutomationSelector :mode="auto.mode.value" :gate="auto.gate.value" @select="auto.select" />

        <div class="field">
          <button class="adv-toggle" type="button" :aria-expanded="instrOpen" aria-controls="imp-instr" data-testid="instr-toggle" @click="instrOpen = !instrOpen"><Icon :name="instrOpen ? 'expand_more' : 'chevron_right'" sm />给 AI 的说明（可选）</button>
          <div v-show="instrOpen" id="imp-instr" class="col">
            <textarea id="imp-instr-text" v-model="instruction" class="input" rows="3" maxlength="2000" :disabled="!llmOk" placeholder="例如：只处理采购相关文件，按项目名归档" aria-label="给 AI 的说明" data-testid="import-instruction" />
            <span class="muted hint">{{ llmOk ? `${instruction.length} / 2000。说明只用于指导分类与命名，不会授予额外权限。` : '需要先配置大模型才能使用任务说明（设置 → 大模型），目前将使用离线规则。' }}</span>
          </div>
        </div>

        <UploadProgress v-if="submitting" :progress="flow.progress.value" @cancel="flow.cancelUpload()" />
        <button class="btn filled has-icon" :disabled="!picked.length || submitting" data-testid="import-submit" @click="submit">
          <Icon :name="submitting ? 'progress_activity' : 'auto_awesome'" :class="{ spin: submitting }" sm />{{ submitting ? '正在提交…' : `开始智能归档（${picked.length}）` }}
        </button>
        <p v-if="summary.nameOnly" class="muted">内容无法读取的文件仍会提交：只依据文件名判断，并标记为“仅依据文件名”进入人工审核，不会导致整批失败。</p>
      </section>
      <section class="card outlined col">
        <h2 class="t-title-m">支持的格式</h2>
        <div class="row wrap" data-testid="format-chips"><span v-for="g in groups" :key="g.label" class="chip" :title="g.exts.join(' ')"><Icon :name="g.icon" sm />{{ g.label }}</span></div>
        <p class="muted">识别内容：{{ allExts }}。旧版 Office（.doc / .xls / .ppt）可直接导入，无需转换；ZIP 压缩包会自动展开成里面的文件（加密或损坏的按普通文件处理）；加密、损坏、超大或其他格式的文件也会接收，只依据文件名判断并进入人工审核（原件不会被修改）。</p>
        <p class="muted">图片与扫描 PDF 通过 OCR 识别，并与文件名联合判断部门、类型和日期。无法确认的字段会标为“待审核”，不会被虚构。</p>
        <div class="banner info"><Icon name="info" sm /><div>文件大小和数量不受限制：大文件自动分块上传，网络中断后可从断点继续。Windows 桌面版还可以直接选择本机文件/文件夹，免上传。超大文件只做元数据判断（不做全文识别），并标记为待人工审核。</div></div>
      </section>
    </aside>
  </div>
  <RemoteBrowserDialog v-if="remoteOpen" :connections="importable" :busy="remoteBusy" :error="remoteErr" @close="remoteOpen = false" @import="importRemote" />
  <AutoRiskDialog v-if="auto.dialogOpen.value && ai.status" v-model:checked="auto.checked.value" :status="ai.status" :remaining-sec="auto.remainingSec.value" :can-confirm="auto.canConfirm.value" @confirm="auto.confirm" @cancel="auto.cancel" />
</template>

<style scoped>
.big-s { font-size: 36px; color: var(--md-sys-color-primary); }
.layout { display: grid; grid-template-columns: minmax(0, 1fr) 360px; gap: 20px; align-items: start; }
.adv-toggle { all: unset; box-sizing: border-box; display: inline-flex; align-items: center; gap: 6px; cursor: pointer; padding: 6px 8px; margin-left: -8px; border-radius: var(--shape-sm); font-weight: 500; }
.adv-toggle:hover { background: var(--c-surface-3); } .adv-toggle:focus-visible { outline: 3px solid var(--md-sys-color-primary); }
.hint { font-size: 12px; }
@media (max-width: 1100px) { .layout { grid-template-columns: 1fr; } }
</style>
