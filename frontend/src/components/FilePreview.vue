<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { api, type PreviewSource } from '../api'
import { useAsync } from '../utils/useAsync'
import { fmtBytes } from '../utils/format'
import { THUMB_SIDE, ZOOMS, clampPage, columnCount, headingLevel, kindLabel, maxSideFor, rowLimitMessage, splitHeader, zoomWidth, type Zoom } from '../utils/preview'
import StateView from './StateView.vue'
import Icon from './Icon.vue'

const props = defineProps<{ source: PreviewSource; name?: string; downloadUrl?: string }>()
const emit = defineEmits<{ close: [] }>()

const el = ref<HTMLElement>()
let prev: HTMLElement | null = null

const load = useAsync(() => (props.source.kind === 'file' ? api.previewFile(props.source.id) : api.previewArchived(props.source.path)))
const pv = computed(() => load.data.value)
const title = computed(() => pv.value?.name ?? props.name ?? '文件预览')
const canDownload = computed(() => !!props.downloadUrl && props.downloadUrl !== '#')
const paged = computed(() => pv.value?.kind === 'pdf' || pv.value?.kind === 'image')
const total = computed(() => pv.value?.pages ?? 1)

// ---- 页面查看器 ----
const page = ref(1)
const pageInput = ref('1')
const zoom = ref<Zoom>('fit')
const imgState = ref<'loading' | 'ok' | 'error'>('loading')
const natural = ref(0)
const nonce = ref(0)
const src = computed(() => {
  if (!paged.value) return ''
  const u = api.previewPageUrl(props.source, page.value, maxSideFor(zoom.value))
  return nonce.value && !u.startsWith('data:') ? u + (u.includes('?') ? '&' : '?') + '_r=' + nonce.value : u
})
const thumbs = computed(() => pv.value?.kind === 'pdf' && total.value > 1)
const thumbUrl = (p: number) => api.previewPageUrl(props.source, p, THUMB_SIDE)

function goto(p: number) {
  const n = clampPage(p, total.value)
  page.value = n; pageInput.value = String(n)
}
function commitInput() { goto(Number.parseInt(pageInput.value, 10)) }
watch([page, zoom], () => { imgState.value = 'loading' })
function retryImg() { imgState.value = 'loading'; nonce.value++ }
function onImgLoad(e: Event) { natural.value = (e.target as HTMLImageElement).naturalWidth; imgState.value = 'ok' }
watch(page, async () => { await nextTick(); el.value?.querySelector('.thumb[aria-current="true"]')?.scrollIntoView?.({ block: 'nearest' }) })

// ---- 表格 ----
const sheetIdx = ref(0)
const sheet = computed(() => pv.value?.sheets?.[sheetIdx.value])
const sheetParts = computed(() => splitHeader(sheet.value?.rows ?? []))
const sheetCols = computed(() => columnCount(sheet.value?.rows ?? []))
const colIdx = (n: number) => Array.from({ length: n }, (_, i) => i)

// ---- 对话框行为：焦点陷阱 / Esc / 方向键 ----
function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape') { e.stopPropagation(); emit('close'); return }
  if (e.key === 'Tab' && el.value) {
    const list = Array.from(el.value.querySelectorAll<HTMLElement>('button,[href],input,select,textarea,[tabindex]:not([tabindex="-1"])')).filter((x) => !x.hasAttribute('disabled'))
    if (!list.length) return
    const first = list[0], last = list[list.length - 1]
    if (e.shiftKey && (document.activeElement === first || document.activeElement === el.value)) { e.preventDefault(); last.focus() }
    else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus() }
    return
  }
  if (paged.value && (e.key === 'ArrowLeft' || e.key === 'ArrowRight')) {
    const t = e.target as HTMLElement
    if (t.tagName === 'INPUT') return
    e.preventDefault()
    goto(page.value + (e.key === 'ArrowRight' ? 1 : -1))
  }
}
onMounted(async () => {
  prev = document.activeElement as HTMLElement
  void load.run()
  await nextTick()
  el.value?.focus()
})
onBeforeUnmount(() => prev?.focus?.())
</script>

<template>
  <Teleport to="body">
    <div class="scrim" @mousedown.self="emit('close')">
      <div ref="el" class="pv" role="dialog" aria-modal="true" :aria-label="'文件预览：' + title" tabindex="-1" data-testid="file-preview" @keydown="onKey">
        <header class="row hd">
          <Icon name="visibility" class="muted" />
          <div class="grow">
            <h2 class="t-title-m ellipsis" :title="title">{{ title }}</h2>
            <div v-if="pv" class="row meta"><span class="chip">{{ kindLabel(pv.kind) }}</span><span class="muted">{{ fmtBytes(pv.size_bytes) }}</span></div>
          </div>
          <a v-if="canDownload" class="btn tonal has-icon" :href="downloadUrl" :download="title"><Icon name="download" sm />下载</a>
          <button class="btn icon-only" aria-label="关闭" title="关闭（Esc）" @click="emit('close')"><Icon name="close" /></button>
        </header>

        <div v-if="pv?.warnings.length" class="banner info wn" role="status"><Icon name="info" /><div><div v-for="(w, i) in pv.warnings" :key="i">{{ w }}</div></div></div>
        <div v-if="pv?.truncated" class="banner wn" role="status"><Icon name="content_cut" /><div>内容过长，仅显示了前一部分。</div></div>

        <div class="bd">
          <StateView :loading="load.loading.value && !pv" :error="load.error.value" :empty="false" :rows="5" @retry="load.run">
            <template v-if="pv">
              <!-- PDF / 图片 -->
              <div v-if="paged" class="viewer">
                <div class="row wrap tools" role="toolbar" aria-label="页面工具">
                  <template v-if="total > 1">
                    <button class="btn icon-only" aria-label="上一页" :disabled="page <= 1" @click="goto(page - 1)"><Icon name="chevron_left" /></button>
                    <input v-model="pageInput" class="input sm pg" inputmode="numeric" aria-label="页码" @change="commitInput" @keydown.enter="commitInput" />
                    <span class="muted">/ {{ total }}</span>
                    <button class="btn icon-only" aria-label="下一页" :disabled="page >= total" @click="goto(page + 1)"><Icon name="chevron_right" /></button>
                  </template>
                  <span class="grow" />
                  <div class="seg" role="group" aria-label="缩放">
                    <button v-for="z in ZOOMS" :key="z.id" :aria-pressed="zoom === z.id" @click="zoom = z.id">{{ z.label }}</button>
                  </div>
                </div>
                <div class="stage-wrap">
                  <nav v-if="thumbs" class="thumbs" aria-label="页面缩略图">
                    <button v-for="p in total" :key="p" class="thumb" :aria-current="p === page" :aria-label="`第 ${p} 页`" @click="goto(p)">
                      <img :src="thumbUrl(p)" :alt="`第 ${p} 页缩略图`" loading="lazy" />
                      <span class="t-label">{{ p }}</span>
                    </button>
                  </nav>
                  <div class="stage" data-testid="preview-stage">
                    <div v-if="imgState === 'loading'" class="skeleton sk" role="status" aria-label="加载中" />
                    <div v-if="imgState === 'error'" class="center err" role="alert">
                      <Icon name="broken_image" class="big" /><div>页面加载失败</div>
                      <button class="btn tonal has-icon" @click="retryImg"><Icon name="refresh" sm />重试</button>
                    </div>
                    <img v-show="imgState === 'ok'" :key="page + '-' + nonce" class="pimg" :src="src" :alt="`${title} 第 ${page} 页`" :style="{ width: zoomWidth(zoom, natural) }" @load="onImgLoad" @error="imgState = 'error'" />
                  </div>
                </div>
              </div>

              <!-- Word 文档：结构化渲染，绝不使用 v-html -->
              <div v-else-if="pv.kind === 'document'" class="paper-wrap">
                <article class="paper" data-testid="preview-document">
                  <p v-if="pv.header" class="hf">{{ pv.header }}</p>
                  <template v-for="(b, i) in pv.blocks ?? []" :key="i">
                    <component :is="'h' + headingLevel(b.level)" v-if="b.type === 'heading'" class="blk-h">{{ b.text }}</component>
                    <p v-else-if="b.type === 'paragraph'" :style="{ textAlign: b.align ?? 'left' }">
                      <template v-if="b.runs?.length"><span v-for="(r, j) in b.runs" :key="j" :class="{ b: r.bold, it: r.italic }">{{ r.text }}</span></template>
                      <template v-else>{{ b.text }}</template>
                    </p>
                    <div v-else-if="b.type === 'list_item'" class="li" :style="{ paddingLeft: (b.level ?? 0) * 1.6 + 'em' }">
                      <span class="dot" aria-hidden="true">•</span>
                      <span><template v-if="b.runs?.length"><span v-for="(r, j) in b.runs" :key="j" :class="{ b: r.bold, it: r.italic }">{{ r.text }}</span></template><template v-else>{{ b.text }}</template></span>
                    </div>
                    <div v-else-if="b.type === 'table'" class="tw"><table class="dt"><tbody><tr v-for="(row, ri) in b.rows ?? []" :key="ri"><td v-for="(c, ci) in row" :key="ci">{{ c }}</td></tr></tbody></table></div>
                  </template>
                  <p v-if="pv.footer" class="hf">{{ pv.footer }}</p>
                </article>
              </div>

              <!-- Excel -->
              <div v-else-if="pv.kind === 'spreadsheet'" class="sheet-view" data-testid="preview-sheet">
                <div v-if="(pv.sheets?.length ?? 0) > 1" class="row tabs" role="tablist" aria-label="工作表">
                  <button v-for="(s, i) in pv.sheets" :key="i" role="tab" class="tab" :aria-selected="i === sheetIdx" @click="sheetIdx = i">{{ s.name }}</button>
                </div>
                <template v-if="sheet">
                  <div v-if="sheet.truncated" class="banner wn" role="status"><Icon name="content_cut" /><div>{{ rowLimitMessage(sheet.rows.length, sheet.rows_total) }}</div></div>
                  <p v-if="!sheet.rows.length" class="muted pad">此工作表为空。</p>
                  <div v-else class="grid-wrap">
                    <table class="grid">
                      <thead><tr><th class="rn" scope="col">#</th><th v-for="c in colIdx(sheetCols)" :key="c" scope="col">{{ sheetParts.head[c] ?? '' }}</th></tr></thead>
                      <tbody><tr v-for="(row, ri) in sheetParts.body" :key="ri"><th class="rn" scope="row">{{ ri + 2 }}</th><td v-for="c in colIdx(sheetCols)" :key="c">{{ row[c] ?? '' }}</td></tr></tbody>
                    </table>
                  </div>
                </template>
              </div>

              <!-- 不支持 -->
              <div v-else class="center pad" data-testid="preview-unsupported">
                <Icon name="visibility_off" class="big" />
                <div class="t-title-m">无法预览此文件</div>
                <p class="muted">{{ pv.message || '暂不支持预览该格式。旧版 .doc/.xls 需先转换为 .docx/.xlsx。' }}</p>
                <a v-if="canDownload" class="btn filled has-icon" :href="downloadUrl" :download="title"><Icon name="download" sm />下载原件</a>
              </div>
            </template>
          </StateView>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
.scrim { position: fixed; inset: 0; background: rgb(0 0 0 / .45); display: grid; place-items: center; z-index: 110; padding: 16px; animation: fade var(--dur-m); }
.pv { width: min(1200px, 100%); height: min(92vh, 1000px); display: flex; flex-direction: column; background: var(--c-surface-2); border-radius: var(--shape-xl); box-shadow: var(--elev-3); outline: 0; overflow: hidden; }
.hd { padding: 12px 12px 12px 24px; border-bottom: 1px solid var(--md-sys-color-outline-variant); }
.hd h2 { margin: 0; max-width: 60vw; }
.meta { gap: 8px; font-size: 12px; }
.wn { margin: 12px 24px 0; }
.bd { flex: 1; min-height: 0; overflow: auto; display: flex; flex-direction: column; }
.bd > :deep(.st) { flex: 1; }
.viewer { flex: 1; min-height: 0; display: flex; flex-direction: column; }
.tools { padding: 8px 16px; border-bottom: 1px solid var(--md-sys-color-outline-variant); gap: 8px; }
.pg { width: 56px; text-align: center; }
.stage-wrap { flex: 1; min-height: 0; display: flex; }
.thumbs { width: 128px; overflow: auto; padding: 8px; display: flex; flex-direction: column; gap: 8px; border-right: 1px solid var(--md-sys-color-outline-variant); flex: none; }
.thumb { all: unset; box-sizing: border-box; cursor: pointer; display: flex; flex-direction: column; align-items: center; gap: 4px; padding: 6px; border-radius: var(--shape-sm); border: 2px solid transparent; min-height: 100px; }
.thumb:hover { background: var(--c-surface-3); }
.thumb[aria-current='true'] { border-color: var(--md-sys-color-primary); background: var(--md-sys-color-secondary-container); }
.thumb:focus-visible { outline: 3px solid var(--md-sys-color-primary); }
.thumb img { width: 100%; height: auto; background: #fff; box-shadow: var(--elev-1); }
.stage { flex: 1; min-width: 0; overflow: auto; padding: 16px; background: var(--c-surface-3); }
.pimg { display: block; margin: 0 auto; max-width: none; background: #fff; box-shadow: var(--elev-2); }
.sk { width: min(640px, 100%); aspect-ratio: 1 / 1.414; margin: 0 auto; }
.center { display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 10px; text-align: center; padding: 48px 24px; }
.center.pad { flex: 1; }
.big { font-size: 48px; color: var(--md-sys-color-outline); } .err .big { color: var(--md-sys-color-error); }
.pad { padding: 24px; }
.paper-wrap { flex: 1; overflow: auto; padding: 24px; background: var(--c-surface-3); }
.paper { max-width: 794px; margin: 0 auto; background: #fff; color: #1b1b1b; padding: 56px 64px; box-shadow: var(--elev-2); line-height: 1.8; font-size: 15px; word-break: break-word; }
.paper p { margin: 0 0 .6em; white-space: pre-wrap; }
.paper .blk-h { margin: 1em 0 .5em; line-height: 1.4; font-weight: 700; }
.paper h1 { font-size: 26px; text-align: center; } .paper h2 { font-size: 21px; } .paper h3 { font-size: 18px; } .paper h4, .paper h5, .paper h6 { font-size: 16px; }
.paper .hf { color: #888; font-size: 12px; text-align: center; border-bottom: 1px dashed #ddd; padding-bottom: .4em; }
.paper .hf:last-child { border-bottom: 0; border-top: 1px dashed #ddd; padding: .4em 0 0; margin-top: 1.5em; }
.paper .b { font-weight: 700; } .paper .it { font-style: italic; }
.li { display: flex; gap: 8px; margin-bottom: .3em; } .li .dot { flex: none; }
.tw { overflow-x: auto; margin: .6em 0 1em; }
.dt { border-collapse: collapse; width: 100%; font-size: 14px; }
.dt td { border: 1px solid #999; padding: 6px 10px; vertical-align: top; }
.sheet-view { flex: 1; min-height: 0; display: flex; flex-direction: column; }
.tabs { gap: 4px; padding: 8px 16px 0; border-bottom: 1px solid var(--md-sys-color-outline-variant); overflow-x: auto; flex: none; }
.tab { border: 0; background: transparent; color: inherit; font: 500 13px var(--font); padding: 8px 16px; cursor: pointer; border-bottom: 3px solid transparent; white-space: nowrap; }
.tab:hover { background: var(--c-surface-3); }
.tab[aria-selected='true'] { border-bottom-color: var(--md-sys-color-primary); color: var(--md-sys-color-primary); }
.grid-wrap { flex: 1; min-height: 0; overflow: auto; margin: 12px 16px 16px; border: 1px solid var(--md-sys-color-outline-variant); border-radius: var(--shape-sm); background: var(--md-sys-color-surface); }
.grid { border-collapse: separate; border-spacing: 0; font-size: 13px; min-width: 100%; }
.grid th, .grid td { border-right: 1px solid var(--md-sys-color-outline-variant); border-bottom: 1px solid var(--md-sys-color-outline-variant); padding: 6px 10px; text-align: left; white-space: nowrap; max-width: 320px; overflow: hidden; text-overflow: ellipsis; }
.grid thead th { position: sticky; top: 0; z-index: 2; background: var(--c-surface-3); font-weight: 600; }
.grid .rn { position: sticky; left: 0; z-index: 1; background: var(--c-surface-3); color: var(--md-sys-color-on-surface-variant); text-align: right; font-weight: 400; min-width: 44px; }
.grid thead .rn { z-index: 3; }
@keyframes fade { from { opacity: 0 } }
</style>
