<script setup lang="ts">
import type { FileRecord } from '../api/types'
import { fmtBytes, fmtTime } from '../utils/format'
import { confidencePct, profileLabel, qualityClass, qualityLabel } from '../utils/ocr'
import Icon from './Icon.vue'
import DecisionBadge from './DecisionBadge.vue'
/** decision：该文件归档时的决策方式（来自台账或所属任务）；未归档时不显示。 */
defineProps<{ file: FileRecord; decision?: 'manual' | 'ai_auto'; decisionModel?: string }>()
defineEmits<{ preview: [] }>()
const dash = (v: string | null) => v ?? '待审核'
</script>
<template>
  <div class="col">
    <div class="row"><Icon name="description" class="muted" /><div class="grow"><div class="t-title-m" style="word-break: break-all">{{ file.source_name }}</div><div class="muted mono" style="word-break: break-all">{{ file.source_path }}</div></div></div>
    <div v-if="file.error" class="banner error" role="alert"><Icon name="error" /><div><b>解析失败</b><br />{{ file.error }}</div></div>
    <div v-else-if="file.review_required" class="banner"><Icon name="rule" /><div>存在无法确认的字段，已标记为待审核；系统不会虚构缺失信息。</div></div>
    <dl class="kv">
      <dt>所属部门</dt><dd><span :class="{ chip: !file.detected_department, attention: !file.detected_department }">{{ dash(file.detected_department) }}</span></dd>
      <dt>文件类型</dt><dd><span :class="{ chip: !file.detected_type, attention: !file.detected_type }">{{ dash(file.detected_type) }}</span></dd>
      <dt>生成日期</dt><dd><span :class="{ chip: !file.detected_date, attention: !file.detected_date }">{{ dash(file.detected_date) }}</span></dd>
      <dt>归档状态</dt><dd>{{ { pending: '未归档', archived: '已归档', failed: '失败' }[file.archive_status] }}</dd>
      <template v-if="file.archive_status === 'archived' && decision"><dt>决策方式</dt><dd><DecisionBadge :decision="decision" :model="decisionModel" /></dd></template>
      <dt>格式 / 大小</dt><dd>{{ file.mime_type }} · {{ fmtBytes(file.size_bytes) }}</dd>
      <template v-if="file.ocr?.used">
        <dt>OCR</dt>
        <dd class="col" style="gap: 8px" data-testid="ocr-row">
          <div class="row wrap" style="gap: 6px">
            <span class="chip" :class="qualityClass(file.ocr.quality)" data-testid="ocr-quality">识别质量：{{ qualityLabel(file.ocr.quality) }}</span>
            <span class="chip">平均置信度 {{ confidencePct(file.ocr.mean_confidence) }}</span>
            <span v-if="file.ocr.vision_used" class="chip progress"><Icon name="visibility" sm />视觉识别已使用</span>
          </div>
          <div class="muted">识别模式：{{ profileLabel(file.ocr.profile) }} · 尝试次数：{{ file.ocr.attempts }}</div>
          <div v-if="file.ocr.quality === 'poor'" class="banner error" role="note"><Icon name="warning" /><div>识别质量较差，请核对证据或使用纠错</div></div>
        </dd>
      </template>
      <dt>导入时间</dt><dd>{{ fmtTime(file.created_at) }}</dd>
      <dt>SHA-256</dt><dd class="mono" style="word-break: break-all">{{ file.sha256 }}</dd>
      <dt>所属任务</dt><dd><RouterLink :to="`/tasks/${file.task_id}`">{{ file.task_id.slice(0, 8) }}</RouterLink></dd>
    </dl>
    <div><button class="btn tonal has-icon" data-testid="preview-original" @click="$emit('preview')"><Icon name="visibility" sm />预览原件</button></div>
    <div>
      <div class="t-label muted" style="margin-bottom: 8px">识别证据（文件名 + OCR/正文）</div>
      <ul v-if="file.evidence.length" class="ev"><li v-for="(e, i) in file.evidence" :key="i"><Icon name="manage_search" sm />{{ e }}</li></ul>
      <p v-else class="muted">暂无证据。</p>
    </div>
  </div>
</template>
<style scoped>
.kv { display: grid; grid-template-columns: 96px 1fr; gap: 10px 12px; margin: 0; } .kv dt { color: var(--md-sys-color-on-surface-variant); } .kv dd { margin: 0; min-width: 0; }
.ev { list-style: none; padding: 0; margin: 0; display: flex; flex-direction: column; gap: 6px; } .ev li { display: flex; gap: 8px; align-items: flex-start; padding: 8px 12px; background: var(--c-surface-3); border-radius: var(--shape-sm); } .ev .icon { margin-top: 1px; }
</style>
