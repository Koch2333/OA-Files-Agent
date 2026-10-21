<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { api, type PickedFile } from '../api'
import { collectDropped, fromInput } from '../utils/paths'
import { errMsg, useAsync } from '../utils/useAsync'
import { fmtBytes } from '../utils/format'
import { useUi } from '../stores/ui'
import PageHeader from '../components/PageHeader.vue'
import Icon from '../components/Icon.vue'

const router = useRouter()
const ui = useUi()
const picked = ref<PickedFile[]>([])
const dragging = ref(false)
const scanning = ref(false)
const submitting = ref(false)
const err = ref<string | null>(null)
const templateId = ref<string>('')
const tpls = useAsync(() => api.listTemplates())
onMounted(tpls.run)

const SUPPORTED = /\.(docx|xlsx|pdf|png|jpe?g)$/i
const LEGACY = /\.(doc|xls)$/i
const supported = computed(() => picked.value.filter((p) => SUPPORTED.test(p.file.name)))
const unsupported = computed(() => picked.value.filter((p) => !SUPPORTED.test(p.file.name)))
const total = computed(() => picked.value.reduce((n, p) => n + p.file.size, 0))

function add(list: PickedFile[]) {
  const seen = new Set(picked.value.map((p) => p.relativePath))
  picked.value.push(...list.filter((p) => !seen.has(p.relativePath)))
}
async function onDrop(e: DragEvent) {
  dragging.value = false
  if (!e.dataTransfer) return
  scanning.value = true
  try { add(await collectDropped(e.dataTransfer)) } catch (x) { err.value = '读取拖入内容失败：' + errMsg(x) } finally { scanning.value = false }
}
function onPick(e: Event) { const el = e.target as HTMLInputElement; if (el.files) add(fromInput(el.files)); el.value = '' }
function remove(p: PickedFile) { picked.value = picked.value.filter((x) => x !== p) }

async function submit() {
  submitting.value = true; err.value = null
  try {
    const job = await api.createImport(picked.value, { templateId: templateId.value || null })
    ui.toast('已创建归档任务', 'success')
    await router.push(`/tasks/${job.id}`)
  } catch (e) { err.value = errMsg(e) } finally { submitting.value = false }
}
</script>

<template>
  <PageHeader title="批量导入 / 智能归档" subtitle="导入后 Agent 自动解析、识别并生成归档计划；此步骤不会移动或修改任何原文件。" />

  <div class="layout">
    <section class="col">
      <div class="drop" :class="{ on: dragging }" @dragover.prevent="dragging = true" @dragleave.prevent="dragging = false" @drop.prevent="onDrop">
        <Icon name="cloud_upload" class="big" />
        <div class="t-title-m">{{ scanning ? '正在读取文件夹…' : '把文件或整个文件夹拖到这里' }}</div>
        <p class="muted">子文件夹会被递归读取，并保留相对路径</p>
        <div class="row wrap" style="justify-content: center">
          <label class="btn filled has-icon"><Icon name="note_add" sm />选择文件<input type="file" multiple hidden accept=".docx,.xlsx,.pdf,.png,.jpg,.jpeg,.doc,.xls" @change="onPick" /></label>
          <label class="btn tonal has-icon"><Icon name="folder_open" sm />选择文件夹<input type="file" hidden webkitdirectory @change="onPick" /></label>
        </div>
      </div>

      <div v-if="err" class="banner error" role="alert"><Icon name="error" />{{ err }}</div>

      <div v-if="picked.length" class="card flat">
        <div class="row hd">
          <div class="grow"><b>{{ picked.length }}</b> 个文件 · {{ fmtBytes(total) }}<span v-if="unsupported.length" class="chip attention" style="margin-left: 8px">{{ unsupported.length }} 个格式不支持</span></div>
          <button class="btn sm" @click="picked = []">清空</button>
        </div>
        <div class="table-wrap" style="max-height: 360px">
          <table class="tbl">
            <thead><tr><th>相对路径</th><th>大小</th><th>状态</th><th /></tr></thead>
            <tbody>
              <tr v-for="p in picked" :key="p.relativePath">
                <td class="ellipsis" style="max-width: 420px" :title="p.relativePath">{{ p.relativePath }}</td>
                <td class="nowrap muted">{{ fmtBytes(p.file.size) }}</td>
                <td><span v-if="SUPPORTED.test(p.file.name)" class="chip success">可处理</span><span v-else-if="LEGACY.test(p.file.name)" class="chip attention">旧版 Office，需转换</span><span v-else class="chip error">不支持</span></td>
                <td><button class="btn icon-only sm" :aria-label="`移除 ${p.file.name}`" @click="remove(p)"><Icon name="close" sm /></button></td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
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
        <button class="btn filled has-icon" :disabled="!picked.length || submitting || scanning" @click="submit">
          <Icon :name="submitting ? 'progress_activity' : 'auto_awesome'" :class="{ spin: submitting }" sm />{{ submitting ? '正在提交…' : `开始智能归档（${picked.length}）` }}
        </button>
        <p v-if="unsupported.length" class="muted">格式不支持的文件仍会提交，并在任务中显示“格式错误”，不会导致整批失败。</p>
      </section>
      <section class="card outlined col">
        <h2 class="t-title-m">支持的格式</h2>
        <div class="row wrap"><span class="chip">Word .docx</span><span class="chip">Excel .xlsx</span><span class="chip">PDF（含扫描件）</span><span class="chip">PNG / JPG</span></div>
        <p class="muted">图片与扫描 PDF 通过 OCR 识别，并与文件名联合判断部门、类型和日期。无法确认的字段会标为“待审核”，不会被虚构。</p>
        <div class="banner info"><Icon name="info" sm /><div>浏览器只能上传你主动选择或拖入的文件，无法读取任意本机路径。若需直接授权本机目录，请使用后端的本机启动器能力（契约待定）。</div></div>
      </section>
    </aside>
  </div>
</template>

<style scoped>
.layout { display: grid; grid-template-columns: minmax(0, 1fr) 360px; gap: 20px; align-items: start; }
.drop { border: 2px dashed var(--md-sys-color-outline); border-radius: var(--shape-xl); padding: 48px 24px; display: flex; flex-direction: column; align-items: center; gap: 10px; text-align: center; transition: all var(--dur-s); background: var(--c-surface-1); }
.drop.on { border-color: var(--md-sys-color-primary); background: var(--md-sys-color-primary-container); transform: scale(1.005); }
.big { font-size: 56px; color: var(--md-sys-color-primary); }
.hd { padding: 12px 16px; border-bottom: 1px solid var(--md-sys-color-outline-variant); }
@media (max-width: 1100px) { .layout { grid-template-columns: 1fr; } }
</style>
