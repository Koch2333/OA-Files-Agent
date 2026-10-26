<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useAuth } from '../stores/auth'
import { useAi } from '../stores/ai'
import { fmtBytes } from '../utils/format'
import { verdict, VERDICT_LABEL } from '../utils/importLimits'
import { useWindowDrop } from '../utils/dropTarget'
import DropOverlay from './DropOverlay.vue'
import ElevatedNotice from './ElevatedNotice.vue'
import { fromClipboard } from '../utils/importFlow'
import { useImportFlow } from '../utils/useImportFlow'
import Icon from './Icon.vue'
import ErrorBanner from './ErrorBanner.vue'
import UploadProgress from './UploadProgress.vue'
import AutomationSelector from './AutomationSelector.vue'
import AutoRiskDialog from './AutoRiskDialog.vue'
import LlmStatusBanner from './LlmStatusBanner.vue'

/**
 * 工作台拖入区：整页都能接收拖拽（文件/文件夹）与粘贴；选好文件后变成确认条，点“开始智能归档”才提交（绝不自动提交）。
 * 状态与提交与批量导入页共用 useImportFlow。
 */
const props = defineProps<{ firstRun?: boolean }>()
const auth = useAuth()
const ai = useAi()
const flow = useImportFlow()
const { picked, brief, auto } = flow
const listOpen = ref(false)
const instrOpen = ref(false)
const canWrite = computed(() => auth.canWrite)
const shown = computed(() => picked.value.slice(0, listOpen.value ? 100 : 0))
const nameOnlyPreview = computed(() => flow.summary.value.nameOnlyList.slice(0, 4).map((r) => r.path).join('、'))

// 页面级投放（见 utils/dropTarget.ts）：整页可放，遮罩提示“松开即可上传”；只读账号/提交中仍阻止导航但不接收
const { dragging } = useWindowDrop({ enabled: () => canWrite.value && !flow.submitting.value, onDrop: (dt) => { void flow.addFromDrop(dt) } })
function onPaste(e: ClipboardEvent) {
  const t = e.target as HTMLElement | null
  if (!canWrite.value || flow.submitting.value || t?.closest('input, textarea, select, [contenteditable]')) return
  const list = fromClipboard(e.clipboardData?.files)
  if (list.length) { e.preventDefault(); flow.add(list) }
}
function onPick(e: Event) { const el = e.target as HTMLInputElement; flow.addFromInput(el.files); el.value = '' }
onMounted(() => {
  void ai.refresh()
  document.addEventListener('paste', onPaste)
})
onBeforeUnmount(() => { document.removeEventListener('paste', onPaste) })
const chip = (f: File) => { const v = verdict(f); return v === 'ok' ? 'success' : 'attention' }
</script>
<template>
  <section class="dz" :class="{ on: dragging, has: picked.length > 0, off: !canWrite }" role="group" aria-label="导入文件" data-testid="home-dropzone">
    <!-- 只读账号：禁用并解释原因 -->
    <div v-if="!canWrite" class="empty">
      <Icon name="lock" class="big" />
      <div class="t-title">当前账号是只读权限，不能导入文件</div>
      <p class="muted">你可以查看任务、文件库和台账。需要导入文件时，请联系管理员把账号权限调整为“操作员”。</p>
    </div>

    <!-- 还没选文件：大号拖入区 -->
    <div v-else-if="!picked.length" class="empty">
      <Icon :name="flow.scanning.value ? 'progress_activity' : 'cloud_upload'" class="big" :class="{ spin: flow.scanning.value }" />
      <div class="t-title" data-testid="dz-title" aria-live="polite">{{ flow.scanning.value ? `正在读取文件${flow.found.value ? `（已发现 ${flow.found.value} 个）` : ''}…` : dragging ? '松开即可上传' : '把文件或文件夹拖到这里，AI 自动识别并归档' }}</div>
      <p class="muted">支持 Word / Excel / PPT（含旧版 .doc .xls .ppt）、PDF、图片（含扫描件 OCR）、文本、网页、邮件；其他格式也会接收，只按文件名处理。也可以直接粘贴文件。归档前一定会先让你预览并审批。</p>
      <div class="row wrap" style="justify-content: center">
        <label class="btn filled has-icon"><Icon name="note_add" sm />选择文件<input type="file" multiple hidden data-testid="dz-files" @change="onPick" /></label>
        <label class="btn tonal has-icon"><Icon name="folder_open" sm />选择文件夹<input type="file" hidden webkitdirectory data-testid="dz-folder" @change="onPick" /></label>
      </div>
      <div v-if="flow.nativePick.value" class="row wrap" style="justify-content: center" data-testid="dz-native">
        <button class="btn tonal has-icon" type="button" :disabled="flow.submitting.value" data-testid="dz-native-files" @click="flow.pickNative(false)"><Icon name="note_add" sm />从本机选择文件（免上传）</button>
        <button class="btn tonal has-icon" type="button" :disabled="flow.submitting.value" data-testid="dz-native-folder" @click="flow.pickNative(true)"><Icon name="folder_open" sm />从本机选择文件夹（免上传）</button>
      </div>
      <UploadProgress v-if="flow.localProgress.value" :progress="null" :local="flow.localProgress.value" @cancel="flow.cancelUpload()" />
      <p class="muted lim">不限制文件大小和数量；大文件会分块上传，中断后可以继续。超大文件和无法读取内容的文件只依据文件名判断，并标记为待人工审核。</p>
      <ol v-if="props.firstRun" class="steps" aria-label="使用步骤">
        <li><b>1</b>拖入文件</li><li class="sep" aria-hidden="true"><Icon name="arrow_forward" sm /></li><li><b>2</b>AI 识别并生成计划</li><li class="sep" aria-hidden="true"><Icon name="arrow_forward" sm /></li><li><b>3</b>你审批后归档</li>
      </ol>
      <LlmStatusBanner compact />
    </div>

    <!-- 已选文件：确认条 -->
    <div v-else class="confirm" data-testid="dz-confirm">
      <div class="row wrap top">
        <Icon name="inventory_2" class="ico" />
        <div class="grow"><div class="t-title-m" data-testid="dz-brief">{{ brief.text }}</div>
          <div class="muted t-label"><button type="button" class="link" :aria-expanded="listOpen" @click="listOpen = !listOpen">{{ listOpen ? '收起文件清单' : '查看文件清单' }}</button><span v-if="brief.nameOnly" class="chip attention" style="margin-left: 8px" data-testid="rejected-count">{{ brief.nameOnly }} 个仅按文件名处理</span></div></div>
        <label class="btn sm has-icon"><Icon name="add" sm />继续添加<input type="file" multiple hidden @change="onPick" /></label>
        <button class="btn sm has-icon" :disabled="flow.submitting.value" data-testid="dz-clear" @click="flow.clear()"><Icon name="delete_sweep" sm />全部移除</button>
      </div>
      <div v-if="listOpen" class="table-wrap files">
        <table class="tbl"><tbody>
          <tr v-for="p in shown" :key="p.relativePath"><td class="ellipsis" style="max-width: 380px" :title="p.relativePath">{{ p.relativePath }}</td><td class="muted nowrap">{{ fmtBytes(p.file.size) }}</td><td><span class="chip" :class="chip(p.file)">{{ VERDICT_LABEL[verdict(p.file)] }}</span></td>
            <td><button class="btn icon-only sm" :aria-label="`移除 ${p.file.name}`" @click="flow.remove(p)"><Icon name="close" sm /></button></td></tr>
        </tbody></table>
      </div>
      <p v-if="brief.nameOnly" class="muted t-label" data-testid="rejected-list">格式无法读取内容（或是空文件）的文件仍会提交：只依据文件名判断，一定进入人工审核：{{ nameOnlyPreview }}<template v-if="flow.summary.value.nameOnlyList.length > 4"> 等 {{ flow.summary.value.nameOnlyList.length }} 个</template></p>
      <div class="opts">
        <AutomationSelector :mode="auto.mode.value" :gate="auto.gate.value" :disabled="flow.submitting.value" @select="auto.select" />
        <div class="field instr">
          <button class="link" type="button" :aria-expanded="instrOpen" aria-controls="dz-instr" data-testid="dz-instr-toggle" :disabled="!flow.llmOk.value" :title="flow.llmOk.value ? '' : '需要先配置大模型才能使用任务说明'" @click="instrOpen = !instrOpen"><Icon :name="instrOpen ? 'expand_more' : 'chevron_right'" sm />给 AI 的说明（可选）</button>
          <textarea v-if="instrOpen && flow.llmOk.value" id="dz-instr" v-model="flow.instruction.value" class="input" rows="2" maxlength="2000" placeholder="例如：只处理采购相关文件，按项目名归档" aria-label="给 AI 的说明" />
          <span v-if="!flow.llmOk.value" class="muted t-label">未配置大模型，将使用离线规则识别。</span>
        </div>
      </div>
      <UploadProgress v-if="flow.submitting.value" :progress="flow.progress.value" @cancel="flow.cancelUpload()" />
      <ErrorBanner :message="flow.err.value" />
      <div class="row go">
        <span class="muted t-label grow">此步骤只读取你选的文件，不会移动或修改任何原文件。</span>
        <button class="btn filled has-icon big-btn" :disabled="flow.submitting.value" data-testid="home-start" @click="flow.submit()">
          <Icon :name="flow.submitting.value ? 'progress_activity' : 'auto_awesome'" :class="{ spin: flow.submitting.value }" sm />{{ flow.submitting.value ? '正在提交…' : '开始智能归档' }}
        </button>
      </div>
    </div>
    <ErrorBanner v-if="!picked.length" :message="flow.err.value" />
    <DropOverlay :show="dragging" />
    <ElevatedNotice />
    <AutoRiskDialog v-if="auto.dialogOpen.value && ai.status" v-model:checked="auto.checked.value" :status="ai.status" :remaining-sec="auto.remainingSec.value" :can-confirm="auto.canConfirm.value" @confirm="auto.confirm" @cancel="auto.cancel" />
  </section>
</template>
<style scoped>
.dz { border: 2px dashed var(--md-sys-color-outline); border-radius: var(--shape-xl); padding: 24px; background: color-mix(in srgb, var(--md-sys-color-primary-container) 40%, var(--c-surface-1)); transition: border-color var(--dur-s), background var(--dur-s), box-shadow var(--dur-s); }
.dz.on { border-width: 3px; border-color: var(--md-sys-color-primary); background: var(--md-sys-color-primary-container); box-shadow: 0 0 0 4px color-mix(in srgb, var(--md-sys-color-primary) 22%, transparent); }
.dz.has { border-style: solid; border-width: 1px; border-color: var(--md-sys-color-outline-variant); padding: 16px 20px; background: var(--c-surface-1); }
.dz.off { border-color: var(--md-sys-color-outline-variant); background: var(--c-surface-1); }
.empty { display: flex; flex-direction: column; align-items: center; gap: 8px; text-align: center; }
.empty p { max-width: 640px; } .empty .t-title { line-height: 30px; }
.big { font-size: 56px; color: var(--md-sys-color-primary); } .off .big { color: var(--md-sys-color-outline); }
.spin { animation: spin 1.1s linear infinite; }
.lim { font-size: 12px; }
.steps { display: flex; align-items: center; gap: 8px; list-style: none; margin: 8px 0 0; padding: 0; flex-wrap: wrap; justify-content: center; }
.steps li { display: flex; align-items: center; gap: 8px; } .steps b { width: 22px; height: 22px; border-radius: 50%; display: grid; place-items: center; background: var(--md-sys-color-primary); color: var(--md-sys-color-on-primary); font-size: 12px; }
.steps .sep { color: var(--md-sys-color-outline); }
.confirm { display: flex; flex-direction: column; gap: 12px; }
.top { gap: 12px; } .ico { font-size: 32px; color: var(--md-sys-color-primary); }
.link { all: unset; box-sizing: border-box; cursor: pointer; display: inline-flex; align-items: center; gap: 4px; color: var(--md-sys-color-primary); font-weight: 500; border-radius: var(--shape-sm); padding: 2px 4px; margin-left: -4px; }
.link:hover { text-decoration: underline; } .link:focus-visible { outline: 3px solid var(--md-sys-color-primary); } .link:disabled { color: var(--md-sys-color-outline); cursor: not-allowed; text-decoration: none; }
.files { max-height: 220px; border: 1px solid var(--md-sys-color-outline-variant); border-radius: var(--shape-md); }
.opts { display: grid; grid-template-columns: auto minmax(0, 1fr); gap: 16px; align-items: start; } .instr { gap: 6px; }
.go { gap: 16px; } .big-btn { height: 44px; padding: 0 28px 0 20px; }
@media (max-width: 900px) { .opts { grid-template-columns: 1fr; } .dz { padding: 20px 16px; } .go { flex-wrap: wrap; } .big-btn { width: 100%; justify-content: center; } .steps .sep { display: none; } .steps { flex-direction: column; align-items: flex-start; } }
@media (max-width: 900px) { .btn, .link { min-height: 44px; } .btn.icon-only.sm { width: 44px; height: 44px; } }
@media (prefers-reduced-motion: reduce) { .spin { animation: none; } }
</style>
