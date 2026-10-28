<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { api, type FileRecord, type LedgerEntry } from '../api'
import { useAuth } from '../stores/auth'
import { useUi } from '../stores/ui'
import { copyText } from '../utils/clipboard'
import { errMsg } from '../utils/errorInfo'
import { fmtTime } from '../utils/format'
import { confidencePct, profileLabel, qualityClass, qualityLabel } from '../utils/ocr'
import { archiveRow } from '../utils/recentArchive'
import Icon from './Icon.vue'
import DecisionBadge from './DecisionBadge.vue'
import FilePreview from './FilePreview.vue'
import LedgerEditDialog from './LedgerEditDialog.vue'

/** 台账记录详情（抽屉内容）：完整归档路径、原文件名、SHA-256、来源任务、识别依据、预览与“打开所在文件夹”都在这里，列表里不再显示。 */
const props = defineProps<{ entry: LedgerEntry }>()
const emit = defineEmits<{ updated: [entry: LedgerEntry] }>()
const editing = ref(false)
const auth = useAuth()
const ui = useUi()
const file = ref<FileRecord | null>(null)
const previewing = ref(false)
const canReveal = ref(false)
const row = computed(() => archiveRow(props.entry, Date.now(), 200))
const dir = computed(() => props.entry.archive_path.split('/').slice(0, -1).join('/'))

/** 台账本身不含识别依据：按来源任务找回对应的文件记录（哈希优先）。 */
async function load() {
  file.value = null
  try {
    const list = await api.listTaskFiles(props.entry.task_id)
    file.value = list.find((f) => f.sha256 === props.entry.sha256) ?? list.find((f) => f.source_name === props.entry.original_name) ?? null
  } catch { /* 识别依据只是补充信息，加载失败不影响其余内容 */ }
}
watch(() => props.entry.id, load)
onMounted(() => {
  void load()
  if (auth.isAdmin) api.listFolder('').then((l) => { canReveal.value = !!l.capabilities.reveal }, () => { canReveal.value = false })
})
async function reveal() {
  try { await api.revealInExplorer(dir.value); ui.toast('已在资源管理器中打开所在文件夹', 'success') } catch (e) { ui.toast('无法打开资源管理器：' + errMsg(e), 'error') }
}
async function copy(text: string, what: string) { ui.toast((await copyText(text)) ? `已复制${what}` : '复制失败，请手动选择文字复制', 'info') }
</script>
<template>
  <div class="col" data-testid="ledger-detail">
    <div class="row" style="align-items: flex-start"><Icon name="description" class="muted" /><div class="t-title-m grow" style="word-break: break-all">{{ entry.canonical_name }}</div></div>
    <div class="row wrap" style="gap: 6px">
      <span class="chip" :class="{ attention: row.department.missing }">{{ row.department.text }}</span>
      <span class="chip" :class="{ attention: row.type.missing }">{{ row.type.text }}</span>
      <span class="chip" :class="{ attention: row.date.missing }">{{ row.date.text }}</span>
      <DecisionBadge :decision="entry.decision ?? 'manual'" :model="entry.decision_model" />
    </div>
    <dl class="kv">
      <dt>原文件名</dt><dd style="word-break: break-all" data-testid="ld-original">{{ entry.original_name }}</dd>
      <dt>归档路径</dt>
      <dd class="col" style="gap: 6px"><span class="mono" style="word-break: break-all" data-testid="ld-path">{{ entry.archive_path }}</span>
        <span class="row wrap" style="gap: 6px">
          <button v-if="canReveal" class="btn tonal sm has-icon" data-testid="ld-reveal" @click="reveal"><Icon name="folder_open" sm />在资源管理器中显示</button>
          <RouterLink class="btn sm has-icon" :to="{ path: '/manage', query: { path: dir } }"><Icon name="folder" sm />在文件管理中打开所在文件夹</RouterLink>
          <button class="btn sm has-icon" @click="copy(entry.archive_path, '路径')"><Icon name="content_copy" sm />复制路径</button>
        </span>
      </dd>
      <dt>归档时间</dt><dd>{{ fmtTime(entry.executed_at) }}（{{ row.when }}）</dd>
      <dt>SHA-256</dt><dd class="mono" style="word-break: break-all">{{ entry.sha256 }}</dd>
      <dt>来源任务</dt><dd><RouterLink :to="`/tasks/${entry.task_id}`">{{ entry.task_id.slice(0, 8) }}</RouterLink></dd>
    </dl>
    <div class="row wrap" style="gap: 8px"><button class="btn tonal has-icon" data-testid="ld-preview" @click="previewing = true"><Icon name="visibility" sm />预览归档文件</button><button v-if="auth.canWrite" class="btn tonal has-icon" data-testid="ld-edit" @click="editing = true"><Icon name="edit" sm />修订分类</button></div>
    <LedgerEditDialog v-if="editing" :entry="entry" @close="editing = false" @saved="(e) => { editing = false; emit('updated', e) }" />
    <template v-if="file">
      <div v-if="file.ocr?.used" class="col" style="gap: 8px" data-testid="ld-ocr">
        <div class="t-label muted">OCR 信息</div>
        <div class="row wrap" style="gap: 6px">
          <span class="chip" :class="qualityClass(file.ocr.quality)">识别质量：{{ qualityLabel(file.ocr.quality) }}</span>
          <span class="chip">平均置信度 {{ confidencePct(file.ocr.mean_confidence) }}</span>
          <span v-if="file.ocr.vision_used" class="chip progress"><Icon name="visibility" sm />视觉识别已使用</span>
        </div>
        <div class="muted">识别模式：{{ profileLabel(file.ocr.profile) }} · 尝试次数：{{ file.ocr.attempts }}</div>
      </div>
      <div>
        <div class="t-label muted" style="margin-bottom: 8px">识别证据（文件名 + OCR/正文）</div>
        <ul v-if="file.evidence.length" class="ev"><li v-for="(e, i) in file.evidence" :key="i"><Icon name="manage_search" sm />{{ e }}</li></ul>
        <p v-else class="muted">暂无证据。</p>
      </div>
    </template>
    <FilePreview v-if="previewing" :source="{ kind: 'archived', path: entry.archive_path }" :name="entry.canonical_name" :download-url="api.contentUrl(entry.archive_path, true)" @close="previewing = false" />
  </div>
</template>
<style scoped>
.kv { display: grid; grid-template-columns: 80px 1fr; gap: 10px 12px; margin: 0; } .kv dt { color: var(--md-sys-color-on-surface-variant); } .kv dd { margin: 0; min-width: 0; }
.ev { list-style: none; padding: 0; margin: 0; display: flex; flex-direction: column; gap: 6px; } .ev li { display: flex; gap: 8px; align-items: flex-start; padding: 8px 12px; background: var(--c-surface-3); border-radius: var(--shape-sm); } .ev .icon { margin-top: 1px; }
</style>
