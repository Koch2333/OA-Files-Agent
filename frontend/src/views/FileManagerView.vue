<script setup lang="ts">
import { useAuth } from '../stores/auth'
import { computed, nextTick, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api, type ConflictOptions, type FsBatchResult, type FsConflict, type FsEntry, type FsVerifyReport, type TrashItem } from '../api'
import { errMsg, useAsync } from '../utils/useAsync'
import { fmtBytes, fmtTime } from '../utils/format'
import { basename, breadcrumbs, dirname, isInside, join, normalize, validateName, validatePath } from '../utils/fsPaths'
import { useUi } from '../stores/ui'
import { downloadBlob } from '../utils/download'
import { copyText } from '../utils/clipboard'
import * as selu from '../utils/fsSelection'
import { batchSummary, conflictsOf, failedItems, outsideModified, statusText, type Choice } from '../utils/fsConflicts'
import FsConflictDialog from '../components/FsConflictDialog.vue'
import PageHeader from '../components/PageHeader.vue'
import StateView from '../components/StateView.vue'
import Icon from '../components/Icon.vue'
import Dialog from '../components/Dialog.vue'
import FilePreview from '../components/FilePreview.vue'
import { iconForName } from '../utils/formats'

const auth = useAuth()
const ui = useUi()
const route = useRoute()
const router = useRouter()

const path = computed(() => normalize(String(route.query.path ?? '')))
const tab = ref<'files' | 'trash'>('files')
const filter = ref('')
const sort = ref<'name' | 'size' | 'modified'>('name')
const order = ref<'asc' | 'desc'>('asc')

const list = useAsync(() => api.listFolder(path.value))
const trash = useAsync(() => api.listTrash())
onMounted(() => { void list.run() })
watch(path, () => { filter.value = ''; sel.value = selu.emptySelection(); void list.run() })
watch(tab, (t) => { if (t === 'trash') void trash.run() })

const listing = computed(() => list.data.value)

// ---- 多选（纯逻辑在 utils/fsSelection）：排序/筛选不丢选择，切换文件夹清空 ----
const sel = ref<selu.Selection>(selu.emptySelection())
const visiblePaths = computed(() => rows.value.map((e) => e.path))
const selCount = computed(() => selectedEntries.value.length)
const selectedEntries = computed(() => (listing.value?.entries ?? []).filter((e) => sel.value.paths.has(e.path)))
const hiddenSelected = computed(() => selCount.value - visiblePaths.value.filter((p) => sel.value.paths.has(p)).length)
const allChecked = computed(() => selu.allSelected(sel.value, visiblePaths.value))
const someChecked = computed(() => selu.someSelected(sel.value, visiblePaths.value))
watch(listing, (l) => { if (l) sel.value = selu.prune(sel.value, l.entries.map((e) => e.path)) })
const isSel = (e: FsEntry) => sel.value.paths.has(e.path)
let lastMods: selu.Mods = {}
function onCheckClick(ev: MouseEvent) { lastMods = { shift: ev.shiftKey, ctrl: ev.ctrlKey, meta: ev.metaKey } }
function onCheckChange(ev: Event, e: FsEntry) {
  sel.value = selu.click(sel.value, visiblePaths.value, e.path, lastMods); lastMods = {}
  const box = ev.target as HTMLInputElement
  void nextTick(() => { box.checked = sel.value.paths.has(e.path) })
}
function onCheckAll() { sel.value = selu.toggleAll(sel.value, visiblePaths.value) }
function clearSel() { sel.value = selu.emptySelection() }
function onRowClick(e: FsEntry, ev: MouseEvent) {
  if (ev.ctrlKey || ev.metaKey || ev.shiftKey) { ev.preventDefault(); sel.value = selu.click(sel.value, visiblePaths.value, e.path, { shift: ev.shiftKey, ctrl: ev.ctrlKey, meta: ev.metaKey }); return }
  openEntry(e)
}
function onTableKey(ev: KeyboardEvent) {
  const tgt = ev.target as HTMLElement
  if (tgt.tagName === 'INPUT' && (tgt as HTMLInputElement).type !== 'checkbox') return
  const focused = (tgt.closest('tr[data-path]') as HTMLElement | null)?.dataset.path ?? null
  const next = selu.key(sel.value, visiblePaths.value, ev.key, focused, { shift: ev.shiftKey, ctrl: ev.ctrlKey, meta: ev.metaKey })
  if (next) { ev.preventDefault(); sel.value = next }
}
const crumbs = computed(() => breadcrumbs(path.value))
const canReveal = computed(() => !!listing.value?.capabilities.reveal)
const rootAbs = computed(() => listing.value?.capabilities.root ?? null)

function go(p: string) { void router.push({ path: '/manage', query: p ? { path: p } : {} }) }
function refresh() { void list.run(); if (tab.value === 'trash') void trash.run() }

const rows = computed(() => {
  const k = filter.value.trim().toLowerCase()
  const dir = order.value === 'asc' ? 1 : -1
  const items = (listing.value?.entries ?? []).filter((e) => !k || e.name.toLowerCase().includes(k) || (e.ledger && `${e.ledger.department}${e.ledger.document_type}${e.ledger.original_name}`.toLowerCase().includes(k)))
  return [...items].sort((a, b) => {
    if (a.is_dir !== b.is_dir) return a.is_dir ? -1 : 1
    let c = 0
    if (sort.value === 'name') c = a.name.localeCompare(b.name, 'zh-CN')
    else if (sort.value === 'size') c = a.size_bytes - b.size_bytes
    else c = +new Date(a.modified_at) - +new Date(b.modified_at)
    return c * dir
  })
})
function sortBy(k: 'name' | 'size' | 'modified') { if (sort.value === k) order.value = order.value === 'asc' ? 'desc' : 'asc'; else { sort.value = k; order.value = 'asc' } }
const ariaSort = (k: string) => (sort.value === k ? (order.value === 'asc' ? 'ascending' : 'descending') : 'none')

function iconOf(e: FsEntry): string {
  return e.is_dir ? 'folder' : iconForName(e.name)
}
const ledgerNote = (n: number) => (n > 0 ? `，已同步更新 ${n} 条台账记录` : '')

// ---- 打开 / 预览 / 下载 ----
function demoBlocked(): boolean {
  if (api.mode === 'mock') { ui.toast('演示模式下无法预览或下载文件', 'info'); return true }
  return false
}
const previewing = ref<FsEntry | null>(null)
function openEntry(e: FsEntry) {
  if (e.is_dir) { go(e.path); return }
  previewing.value = e
}
function clickAnchor(href: string, name = '') {
  const a = document.createElement('a')
  a.href = href
  a.download = name // 空串：沿用服务端 Content-Disposition 的文件名（ZIP 名由服务端决定）
  document.body.appendChild(a); a.click(); a.remove()
}
/** 单个文件原样下载；文件夹或多选由服务端流式打包成 ZIP（链接放得下时用普通链接，浏览器直接流式保存）。 */
async function downloadPaths(paths: string[], single?: FsEntry) {
  if (demoBlocked()) return
  if (single && !single.is_dir) { clickAnchor(api.contentUrl(single.path, true), single.name); return }
  const url = api.downloadUrl(paths)
  if (url.length <= 6000) { clickAnchor(url); ui.toast('已开始下载，文件夹会自动打包为 ZIP（含 MANIFEST.sha256 校验清单）', 'info'); return }
  try {
    ui.toast('选择项目较多，正在打包，请稍候……', 'info')
    const r = await api.downloadSelection(paths)
    downloadBlob(r.blob, r.filename)
  } catch (e) { ui.toast('打包下载失败：' + errMsg(e), 'error') }
}
const download = (e: FsEntry) => downloadPaths([e.path], e)
function downloadSelected() {
  const items = selectedEntries.value
  void downloadPaths(items.map((e) => e.path), items.length === 1 ? items[0] : undefined)
}
async function reveal(p: string) {
  try { await api.revealInExplorer(p); ui.toast('已在资源管理器中打开', 'success') } catch (e) { ui.toast('无法打开资源管理器：' + errMsg(e), 'error') }
}

// ---- 冲突：先预检（ask，不改动），有冲突则让用户逐项选择后再执行 ----
const conflictAsk = ref<null | { conflicts: FsConflict[]; verb: string; resolve: (r: Record<string, Choice> | null) => void }>(null)
function askConflicts(conflicts: FsConflict[], verb: string): Promise<Record<string, Choice> | null> {
  return new Promise((resolve) => { conflictAsk.value = { conflicts, verb, resolve } })
}
function answerConflicts(r: Record<string, Choice> | null) { const a = conflictAsk.value; conflictAsk.value = null; a?.resolve(r) }
/** 以 ask 调用；409 带冲突列表时弹出对话框，带着用户的选择重试；取消返回 null。 */
async function withConflicts<T>(verb: string, run: (o: ConflictOptions) => Promise<T>): Promise<T | null> {
  let opts: ConflictOptions = { on_conflict: 'ask' }
  for (let i = 0; i < 5; i++) {
    try { return await run(opts) } catch (e) {
      const cs = conflictsOf(e)
      if (!cs) throw e
      const res = await askConflicts(cs, verb)
      if (!res) return null
      opts = { on_conflict: 'ask', resolutions: res }
    }
  }
  throw new Error('冲突仍未解决，请刷新后重试')
}

// ---- 对话框：新建 / 重命名 / 移动 / 删除 / 校验 / 结果 ----
const dlg = ref<null | 'mkdir' | 'rename' | 'move' | 'delete' | 'verify' | 'result' | 'hash'>(null)
const target = ref<FsEntry | null>(null)
const moveItems = ref<FsEntry[]>([])
const deleteItems = ref<FsEntry[]>([])
const nameInput = ref('')
const dlgErr = ref<string | null>(null)
const busy = ref(false)
const resultBatch = ref<{ title: string; res: FsBatchResult } | null>(null)
const verifyRep = ref<FsVerifyReport | null>(null)
const verifyBusy = ref(false)
const hashEntry = ref<FsEntry | null>(null)

function openDlg(kind: 'mkdir' | 'rename' | 'move' | 'delete', e?: FsEntry) {
  dlg.value = kind; target.value = e ?? null; dlgErr.value = null; busy.value = false
  if (kind === 'mkdir') nameInput.value = ''
  if (kind === 'rename' && e) nameInput.value = e.name
  if (kind === 'move') {
    moveItems.value = e ? [e] : [...selectedEntries.value]
    const first = moveItems.value[0]
    moveDir.value = dirname(first.path); void initTree(dirname(first.path))
  }
  if (kind === 'delete') deleteItems.value = e ? [e] : [...selectedEntries.value]
}
function closeDlg() { if (!busy.value) dlg.value = null }

async function submit(fn: () => Promise<string | null>) {
  busy.value = true; dlgErr.value = null
  try { const msg = await fn(); if (msg === null) return; dlg.value = null; ui.toast(msg, 'success'); refresh() }
  catch (e) { dlgErr.value = errMsg(e) }
  finally { busy.value = false }
}
function doMkdir() {
  const err = validateName(nameInput.value)
  if (err) { dlgErr.value = err; return }
  const name = nameInput.value.trim()
  void submit(async () => { await api.createFolder(join(path.value, name)); return `已新建文件夹“${name}”` })
}
/** 单项移动/重命名的结果文案（跳过、重复、替换都如实说明）。 */
function moveMessage(r: Awaited<ReturnType<typeof api.moveEntry>>, ok: string): string {
  const extra = ledgerNote(r.ledger_updated) + (r.hash_status === 'modified_outside' ? '；注意：该文件的内容与台账记录的 SHA-256 不一致（可能在系统外被修改），已标记' : '')
  switch (r.status) {
    case 'skipped': return '已跳过，未做改动'
    case 'duplicate': return `目标已有相同内容的文件（${r.duplicate_of ?? ''}），未重复放入，源文件保持原样`
    case 'renamed': return `已保留两者，新名称“${basename(r.final_path ?? '')}”${extra}`
    case 'replaced': return `已替换；旧文件在回收站，可还原${extra}`
    default: return ok + extra
  }
}
function doRename() {
  const e = target.value!
  const err = validateName(nameInput.value)
  if (err) { dlgErr.value = err; return }
  const name = nameInput.value.trim()
  if (name === e.name) { dlgErr.value = '新名称与原名称相同'; return }
  void submit(async () => {
    const r = await withConflicts('重命名', (o) => api.moveEntry(e.path, join(dirname(e.path), name), o))
    return r ? moveMessage(r, `已重命名为“${name}”`) : null
  })
}
function doMove() {
  const items = moveItems.value
  const dir = normalize(moveDir.value)
  const err = validatePath(dir)
  if (err) { dlgErr.value = err; return }
  if (items.some((e) => e.is_dir && isInside(dir, e.path))) { dlgErr.value = '不能把文件夹移动到它自身内部'; return }
  if (items.every((e) => dirname(e.path) === dir)) { dlgErr.value = '目标就是当前位置'; return }
  const where = dir || '归档根目录'
  if (items.length === 1) {
    const e = items[0]
    void submit(async () => {
      const r = await withConflicts('移动', (o) => api.moveEntry(e.path, join(dir, e.name), o))
      return r ? moveMessage(r, `已移动到“${where}”`) : null
    })
    return
  }
  void submit(async () => {
    const r = await withConflicts('移动', (o) => api.bulkMove(items.map((e) => e.path), dir, o))
    if (!r) return null
    clearSel()
    return reportBatch(`移动到“${where}”`, r, '移动')
  })
}
/** 批量结果：一句话摘要；有失败/部分/重复/被外部修改的项目时再弹出逐项结果。 */
function reportBatch(title: string, r: FsBatchResult, verb: string): string {
  if (failedItems(r).length || outsideModified(r).length || r.summary.duplicate || r.summary.skipped) { resultBatch.value = { title, res: r }; void nextTick(() => { dlg.value = 'result' }) }
  return `${title}：${batchSummary(r, verb)}`
}
function doDelete() {
  const items = deleteItems.value
  if (items.length === 1) {
    const e = items[0]
    void submit(async () => { await api.trashEntry(e.path); return `已将“${e.name}”移入回收站；对应台账记录已标记为已移除` })
    return
  }
  void submit(async () => {
    const r = await api.bulkTrash(items.map((e) => e.path))
    clearSel()
    return reportBatch('删除到回收站', r, '已删除')
  })
}

// ---- 哈希：校验与详情 ----
const HASH_TEXT: Record<string, string> = { not_task: '非任务文件', unverified: '未校验', verified: '已校验', mismatch: '与台账不一致' }
const hashIcon = (s: string) => (s === 'verified' ? 'verified' : s === 'mismatch' ? 'warning' : 'tag')
async function runVerify(paths: string[], showDialog: boolean) {
  verifyBusy.value = true; verifyRep.value = null
  if (showDialog) { dlg.value = 'verify'; dlgErr.value = null }
  try { verifyRep.value = await api.verifyFiles(paths); void list.run() }
  catch (e) { dlgErr.value = errMsg(e) }
  finally { verifyBusy.value = false }
}
function verifySelected() { void runVerify(selectedEntries.value.map((e) => e.path), true) }
function openHash(e: FsEntry) { hashEntry.value = e; dlg.value = 'hash'; dlgErr.value = null }
const hashLive = computed(() => (hashEntry.value ? listing.value?.entries.find((x) => x.path === hashEntry.value!.path) ?? hashEntry.value : null))
async function recheck(e: FsEntry) {
  verifyBusy.value = true; dlgErr.value = null
  try {
    const r = await api.verifyFiles([e.path])
    await list.run()
    ui.toast(r.mismatch ? '校验完成：与台账不一致，文件可能在系统外被修改' : r.missing ? '校验完成：文件缺失' : '校验完成：与台账一致', r.mismatch || r.missing ? 'error' : 'success')
  } catch (err) { dlgErr.value = errMsg(err) } finally { verifyBusy.value = false }
}
async function copyHash(h?: string) { if (h && await copyText(h)) ui.toast('已复制 SHA-256', 'success') }

// ---- 移动：文件夹树选择器（按需加载） ----
interface TNode { open: boolean; loading: boolean; loaded: boolean; children: string[]; error?: string }
const tree = reactive<Record<string, TNode>>({})
const moveDir = ref('')
function node(p: string): TNode { return (tree[p] ??= { open: false, loading: false, loaded: false, children: [] }) }
async function loadNode(p: string) {
  const n = node(p)
  if (n.loaded || n.loading) return
  n.loading = true; n.error = undefined
  try { n.children = (await api.listFolder(p)).entries.filter((x) => x.is_dir).map((x) => x.path); n.loaded = true }
  catch (e) { n.error = errMsg(e) } finally { n.loading = false }
}
async function initTree(select: string) {
  for (const k of Object.keys(tree)) delete tree[k]
  node('').open = true
  await loadNode('')
  // 逐级展开到当前所在目录
  let acc = ''
  for (const part of select.split('/').filter(Boolean)) {
    acc = join(acc, part)
    const parent = dirname(acc)
    node(parent).open = true
    await loadNode(parent)
    if (!node(parent).children.includes(acc)) break
  }
  if (select) { node(select).open = true; void loadNode(select) }
}
async function toggle(p: string) { const n = node(p); n.open = !n.open; if (n.open) await loadNode(p) }
const flat = computed(() => {
  const out: { path: string; depth: number; n: TNode }[] = []
  const dirs = moveItems.value.filter((e) => e.is_dir).map((e) => e.path)
  const walk = (p: string, depth: number) => {
    const n = node(p)
    out.push({ path: p, depth, n })
    if (n.open) for (const c of n.children) {
      if (dirs.some((d) => isInside(c, d))) continue
      walk(c, depth + 1)
    }
  }
  walk('', 0)
  return out
})

// ---- 回收站 ----
const restoring = ref<string | null>(null)
async function restore(t: TrashItem) {
  restoring.value = t.id
  try {
    const r = await withConflicts('还原', async (o) => {
      const choice = o.resolutions?.[t.id]
      return api.restoreTrash(t.id, choice ?? 'ask')
    })
    if (!r) return
    if (r.status === 'skipped') ui.toast('已跳过，项目仍在回收站', 'info')
    else if (r.status === 'duplicate') ui.toast(`原位置已有相同内容的文件（${r.duplicate_of ?? r.path}），未重复还原；项目仍在回收站`, 'info')
    else {
      const warn = r.hash_status === 'modified_outside' ? '；注意：内容与台账 SHA-256 不一致（可能在系统外被修改）' : ''
      ui.toast(`${r.status === 'replaced' ? '已替换原文件（旧文件在回收站）并' : ''}已还原到“${r.path}”${ledgerNote(r.ledger_updated)}${warn}`, 'success')
    }
    void trash.run(); void list.run()
  } catch (e) { ui.toast('还原失败：' + errMsg(e), 'error') } finally { restoring.value = null }
}
</script>

<template>
  <PageHeader title="文件管理" subtitle="浏览归档目录，支持多选：批量下载（文件夹自动打包 ZIP）、移动、删除到回收站；台账与 SHA-256 校验同步。">
    <template #actions>
      <div class="seg" role="group" aria-label="视图">
        <button :aria-pressed="tab === 'files'" @click="tab = 'files'"><Icon name="folder" sm />文件</button>
        <button :aria-pressed="tab === 'trash'" @click="tab = 'trash'"><Icon name="delete" sm />回收站</button>
      </div>
    </template>
  </PageHeader>

  <template v-if="tab === 'files'">
    <nav class="row wrap crumbs" aria-label="路径导航">
      <template v-for="(c, i) in crumbs" :key="c.path">
        <Icon v-if="i" name="chevron_right" sm class="muted" />
        <span v-if="i === crumbs.length - 1" class="cur" aria-current="page">{{ c.label }}</span>
        <button v-else class="btn sm crumb" @click="go(c.path)">{{ c.label }}</button>
      </template>
    </nav>
    <div class="row wrap bar">
      <div class="search grow" style="min-width: 220px"><Icon name="search" /><input v-model="filter" class="input" placeholder="在当前文件夹中筛选" aria-label="筛选当前文件夹" /></div>
      <button v-if="auth.canWrite" class="btn filled has-icon" @click="openDlg('mkdir')"><Icon name="create_new_folder" sm />新建文件夹</button>
      <button class="btn tonal has-icon" :disabled="list.loading.value" @click="refresh"><Icon name="refresh" sm />刷新</button>
      <button v-if="canReveal && auth.isAdmin" class="btn outlined has-icon" :title="rootAbs ? `归档根目录：${rootAbs}` : undefined" @click="reveal(path)"><Icon name="open_in_new" sm />在资源管理器中打开</button>
    </div>
    <p v-if="rootAbs" class="muted caption mono">归档根目录：{{ rootAbs }}</p>

    <div class="card flat" @keydown="onTableKey">
      <StateView :loading="list.loading.value && !listing" :error="list.error.value" :empty="!rows.length" :empty-title="filter ? `没有匹配“${filter}”的项目` : '此文件夹为空'" :empty-hint="filter ? '清除筛选条件后重试。' : '可以新建文件夹，或先在“智能归档”中导入文件。'" empty-icon="folder_off" @retry="list.run">
        <div class="table-wrap">
          <table class="tbl">
            <thead>
              <tr>
                <th class="selcol"><input type="checkbox" :checked="allChecked" :indeterminate.prop="someChecked" aria-label="全选当前文件夹中显示的项目" data-testid="fm-select-all" @change="onCheckAll" /></th>
                <th class="sortable" :aria-sort="ariaSort('name')" tabindex="0" @click="sortBy('name')" @keydown.enter="sortBy('name')">名称<Icon v-if="sort === 'name'" :name="order === 'asc' ? 'arrow_upward' : 'arrow_downward'" sm /></th>
                <th>台账 / 校验</th>
                <th class="sortable" :aria-sort="ariaSort('size')" tabindex="0" @click="sortBy('size')" @keydown.enter="sortBy('size')">大小<Icon v-if="sort === 'size'" :name="order === 'asc' ? 'arrow_upward' : 'arrow_downward'" sm /></th>
                <th class="sortable" :aria-sort="ariaSort('modified')" tabindex="0" @click="sortBy('modified')" @keydown.enter="sortBy('modified')">修改时间<Icon v-if="sort === 'modified'" :name="order === 'asc' ? 'arrow_upward' : 'arrow_downward'" sm /></th>
                <th><span class="sr-only">操作</span></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="e in rows" :key="e.path" class="clickable" :class="{ selected: isSel(e) }" :data-path="e.path" tabindex="0" :aria-label="(e.is_dir ? '文件夹 ' : '文件 ') + e.name + (isSel(e) ? '（已选中）' : '')" data-testid="fm-row" @click="onRowClick(e, $event)" @keydown.enter.self="openEntry(e)">
                <td class="selcol" @click.stop><input type="checkbox" :checked="isSel(e)" :aria-label="'选择 ' + e.name" data-testid="fm-select" @click="onCheckClick" @change="onCheckChange($event, e)" /></td>
                <td><div class="row nm"><Icon :name="iconOf(e)" :fill="e.is_dir" :class="{ dir: e.is_dir }" /><span class="ellipsis" style="max-width: 360px" :title="e.name">{{ e.name }}</span></div></td>
                <td>
                  <span v-if="e.ledger" class="chip" :title="`原文件名：${e.ledger.original_name}`">{{ e.ledger.department }}·{{ e.ledger.document_type }}·{{ e.ledger.document_date }}</span>
                  <button v-if="e.task_related" class="chip sm hs" :class="{ success: e.hash_status === 'verified', error: e.hash_status === 'mismatch' }" :title="e.hash_status === 'mismatch' ? '与台账记录的 SHA-256 不一致，文件可能在系统外被修改；点击查看' : e.hash_status === 'verified' ? '已重新计算 SHA-256，与台账一致；点击查看' : '台账已记录 SHA-256，尚未重新校验；点击查看'" :aria-label="`${e.name}：${HASH_TEXT[e.hash_status]}，查看哈希`" data-testid="fm-hash" @click.stop="openHash(e)"><Icon :name="hashIcon(e.hash_status)" sm />{{ HASH_TEXT[e.hash_status] }}</button>
                  <span v-else-if="!e.is_dir" class="muted hs" title="台账中没有记录的文件，不做哈希校验">– {{ HASH_TEXT.not_task }}</span>
                </td>
                <td class="muted nowrap">{{ e.is_dir ? '—' : fmtBytes(e.size_bytes) }}</td>
                <td class="muted nowrap">{{ fmtTime(e.modified_at) }}</td>
                <td class="nowrap acts" @click.stop>
                  <button v-if="!e.is_dir" class="btn icon-only" :aria-label="'预览 ' + e.name" title="预览" data-testid="fm-preview" @click="openEntry(e)"><Icon name="visibility" /></button>
                  <button class="btn icon-only" :aria-label="e.is_dir ? `下载 ${e.name}（打包为 ZIP）` : '下载 ' + e.name" :title="e.is_dir ? '下载（自动打包为 ZIP）' : '下载'" @click="download(e)"><Icon :name="e.is_dir ? 'folder_zip' : 'download'" /></button>
                  <button v-if="auth.canWrite" class="btn icon-only" :aria-label="'重命名 ' + e.name" title="重命名" @click="openDlg('rename', e)"><Icon name="edit" /></button>
                  <button v-if="auth.canWrite" class="btn icon-only" :aria-label="'移动 ' + e.name" title="移动到…" @click="openDlg('move', e)"><Icon name="drive_file_move" /></button>
                  <button v-if="canReveal && auth.isAdmin" class="btn icon-only" :aria-label="'在资源管理器中显示 ' + e.name" :title="rootAbs ? `在资源管理器中显示（根目录：${rootAbs}）` : '在资源管理器中显示'" @click="reveal(e.path)"><Icon name="folder_open" /></button>
                  <button v-if="auth.canWrite" class="btn icon-only danger" :aria-label="'删除 ' + e.name" title="删除" @click="openDlg('delete', e)"><Icon name="delete" /></button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </StateView>
    </div>
    <div v-if="selCount" class="selbar" role="region" aria-label="已选项目操作" data-testid="fm-selbar">
      <span class="grow" role="status" aria-live="polite" data-testid="fm-selcount">{{ selu.barText(selCount, hiddenSelected) }}</span>
      <button class="btn has-icon" @click="downloadSelected"><Icon name="folder_zip" sm />下载</button>
      <button class="btn has-icon" :disabled="verifyBusy" @click="verifySelected"><Icon name="tag" sm />校验</button>
      <button v-if="auth.canWrite" class="btn has-icon" data-testid="fm-bulk-move" @click="openDlg('move')"><Icon name="drive_file_move" sm />移动</button>
      <button v-if="auth.canWrite" class="btn has-icon" data-testid="fm-bulk-delete" @click="openDlg('delete')"><Icon name="delete" sm />删除到回收站</button>
      <button class="btn has-icon" @click="clearSel"><Icon name="close" sm />取消选择</button>
    </div>
  </template>

  <template v-else>
    <div class="row wrap bar">
      <p class="muted grow" style="margin: 0">删除的项目保存在回收站，可随时还原；原位置被占用时会列出冲突（同名同内容视为重复，不会重复还原），由你选择跳过、保留两者或替换（旧文件进回收站）。</p>
      <button class="btn tonal has-icon" :disabled="trash.loading.value" @click="trash.run"><Icon name="refresh" sm />刷新</button>
    </div>
    <div class="card flat">
      <StateView :loading="trash.loading.value && !trash.data.value" :error="trash.error.value" :empty="!trash.data.value?.length" empty-title="回收站是空的" empty-icon="delete_sweep" empty-hint="删除文件或文件夹后会出现在这里。" @retry="trash.run">
        <div class="table-wrap">
          <table class="tbl">
            <thead><tr><th>名称</th><th>原位置</th><th>大小</th><th>删除时间</th><th><span class="sr-only">操作</span></th></tr></thead>
            <tbody>
              <tr v-for="t in trash.data.value" :key="t.id">
                <td><div class="row nm"><Icon :name="t.is_dir ? 'folder' : 'draft'" :fill="t.is_dir" :class="{ dir: t.is_dir }" /><span class="ellipsis" style="max-width: 280px" :title="t.name">{{ t.name }}</span></div></td>
                <td class="mono muted ellipsis" style="max-width: 320px" :title="t.original_path">{{ dirname(t.original_path) || '归档根目录' }}</td>
                <td class="muted nowrap">{{ t.is_dir && !t.size_bytes ? '—' : fmtBytes(t.size_bytes) }}</td>
                <td class="muted nowrap">{{ fmtTime(t.deleted_at) }}</td>
                <td class="nowrap"><button v-if="auth.canWrite" class="btn tonal sm has-icon" :disabled="restoring === t.id" :aria-label="'还原 ' + t.name" @click="restore(t)"><Icon name="restore_from_trash" sm />还原</button></td>
              </tr>
            </tbody>
          </table>
        </div>
      </StateView>
    </div>
  </template>

  <Dialog v-if="dlg === 'mkdir'" title="新建文件夹" width="440px" @close="closeDlg">
    <p class="muted" style="margin: 0">位置：{{ path || '归档根目录' }}</p>
    <div class="field"><label for="fm-name">文件夹名称</label><input id="fm-name" v-model="nameInput" class="input" :aria-invalid="!!dlgErr" autocomplete="off" @keydown.enter="doMkdir" /></div>
    <div v-if="dlgErr" class="banner error" role="alert"><Icon name="error" /><div>{{ dlgErr }}</div></div>
    <template #actions><button class="btn" :disabled="busy" @click="closeDlg">取消</button><button class="btn filled" :disabled="busy" @click="doMkdir">创建</button></template>
  </Dialog>

  <Dialog v-if="dlg === 'rename' && target" title="重命名" width="480px" @close="closeDlg">
    <p class="muted" style="margin: 0">仍保留在“{{ dirname(target.path) || '归档根目录' }}”中。</p>
    <div class="field"><label for="fm-rename">新名称</label><input id="fm-rename" v-model="nameInput" class="input" :aria-invalid="!!dlgErr" autocomplete="off" @keydown.enter="doRename" /></div>
    <div v-if="dlgErr" class="banner error" role="alert"><Icon name="error" /><div>{{ dlgErr }}</div></div>
    <template #actions><button class="btn" :disabled="busy" @click="closeDlg">取消</button><button class="btn filled" :disabled="busy" @click="doRename">重命名</button></template>
  </Dialog>

  <Dialog v-if="dlg === 'move' && moveItems.length" :title="moveItems.length === 1 ? `移动“${moveItems[0].name}”` : `移动 ${moveItems.length} 项`" width="520px" @close="closeDlg">
    <p v-if="moveItems.length > 1" class="muted" style="margin: 0">将保持原名移入所选文件夹；同名内容相同的文件视为重复不会重复放入，同名内容不同的会先让你选择处理方式。</p>
    <div class="picker" role="tree" aria-label="选择目标文件夹">
      <div v-for="r in flat" :key="r.path" class="trow" role="treeitem" :aria-selected="normalize(moveDir) === r.path" :class="{ sel: normalize(moveDir) === r.path }" :style="{ paddingLeft: 8 + r.depth * 18 + 'px' }">
        <button class="tg" :aria-label="r.n.open ? '收起' : '展开'" :disabled="r.n.loaded && !r.n.children.length" @click="toggle(r.path)"><Icon :name="r.n.loading ? 'progress_activity' : r.n.open ? 'expand_more' : 'chevron_right'" sm :class="{ spin: r.n.loading }" /></button>
        <button class="pick" @click="moveDir = r.path"><Icon name="folder" sm fill />{{ r.path ? basename(r.path) : '归档根目录' }}</button>
        <span v-if="r.n.error" class="muted err">{{ r.n.error }}</span>
      </div>
    </div>
    <div class="field"><label for="fm-dest">目标文件夹（可直接输入路径，空 = 归档根目录）</label><input id="fm-dest" v-model="moveDir" class="input mono" :aria-invalid="!!dlgErr" autocomplete="off" @keydown.enter="doMove" /></div>
    <div v-if="dlgErr" class="banner error" role="alert"><Icon name="error" /><div>{{ dlgErr }}</div></div>
    <template #actions><button class="btn" :disabled="busy" @click="closeDlg">取消</button><button class="btn filled" :disabled="busy" @click="doMove">移动到此处</button></template>
  </Dialog>

  <Dialog v-if="dlg === 'delete' && deleteItems.length" title="删除到回收站" width="480px" @close="closeDlg">
    <p v-if="deleteItems.length === 1" style="margin: 0">确定删除{{ deleteItems[0].is_dir ? '文件夹' : '文件' }}“<strong>{{ deleteItems[0].name }}</strong>”吗？</p>
    <p v-else style="margin: 0">确定删除所选的 <strong>{{ deleteItems.length }}</strong> 项吗？</p>
    <p class="muted" style="margin: 0">{{ deleteItems.length === 1 ? '它会' : '它们会' }}被移入回收站，之后可以还原；对应的台账记录会被标记为已移除{{ deleteItems.some((e) => e.is_dir) ? '（包含文件夹内的全部文件）' : '' }}。</p>
    <div v-if="dlgErr" class="banner error" role="alert"><Icon name="error" /><div>{{ dlgErr }}</div></div>
    <template #actions><button class="btn" :disabled="busy" @click="closeDlg">取消</button><button class="btn filled danger-fill" :disabled="busy" @click="doDelete">删除</button></template>
  </Dialog>
  <FsConflictDialog v-if="conflictAsk" :conflicts="conflictAsk.conflicts" :verb="conflictAsk.verb" @confirm="answerConflicts" @cancel="answerConflicts(null)" />

  <Dialog v-if="dlg === 'result' && resultBatch" :title="`${resultBatch.title}：逐项结果`" width="640px" testid="fm-result-dialog" @close="dlg = null">
    <p style="margin: 0">{{ batchSummary(resultBatch.res) }}。</p>
    <div v-if="outsideModified(resultBatch.res).length" class="banner" role="status"><Icon name="warning" /><div>以下任务相关文件的内容与台账记录的 SHA-256 不一致（可能在系统外被修改），已标记并写入审计：<div v-for="p in outsideModified(resultBatch.res)" :key="p" class="mono">{{ p }}</div></div></div>
    <ul class="res" aria-label="逐项结果">
      <li v-for="it in resultBatch.res.items" :key="it.path" class="row" :class="{ bad: it.status === 'failed' }">
        <Icon :name="it.status === 'failed' ? 'error' : it.status === 'partial' || it.status === 'skipped' || it.status === 'duplicate' ? 'info' : 'check_circle'" sm />
        <span class="ellipsis grow" :title="it.path">{{ basename(it.path) }}</span>
        <span class="muted">{{ statusText(it.status) }}</span>
        <span v-if="it.message" class="muted small" :title="it.message">{{ it.message }}</span>
      </li>
    </ul>
    <template #actions><button class="btn filled" @click="dlg = null">知道了</button></template>
  </Dialog>

  <Dialog v-if="dlg === 'verify'" title="校验 SHA-256" width="640px" testid="fm-verify-dialog" @close="dlg = null">
    <div v-if="verifyBusy" class="row" role="status"><Icon name="progress_activity" class="spin" />正在逐个读取文件并重新计算哈希，请稍候……</div>
    <div v-if="dlgErr" class="banner error" role="alert"><Icon name="error" /><div>{{ dlgErr }}</div></div>
    <template v-if="verifyRep">
      <p style="margin: 0" data-testid="fm-verify-summary">已校验 {{ verifyRep.checked }} 个任务相关文件：<strong>{{ verifyRep.match }}</strong> 个一致，<strong :class="{ bad: verifyRep.mismatch }">{{ verifyRep.mismatch }}</strong> 个与台账不一致，{{ verifyRep.missing }} 个缺失<template v-if="verifyRep.not_task">；{{ verifyRep.not_task }} 个非任务文件未校验</template>。</p>
      <ul v-if="verifyRep.items.some((i) => i.status !== 'match')" class="res" aria-label="需要注意的文件">
        <li v-for="i in verifyRep.items.filter((x) => x.status !== 'match')" :key="i.path" class="row">
          <Icon :name="i.status === 'not_task' ? 'info' : 'warning'" sm /><span class="ellipsis grow" :title="i.path">{{ i.path }}</span><span class="muted small">{{ i.status === 'mismatch' ? '与台账不一致' : i.status === 'missing' ? '文件缺失' : '非任务文件' }}</span>
        </li>
      </ul>
    </template>
    <template #actions><button class="btn filled" @click="dlg = null">完成</button></template>
  </Dialog>

  <Dialog v-if="dlg === 'hash' && hashLive" :title="`哈希：${hashLive.name}`" width="560px" testid="fm-hash-dialog" @close="dlg = null">
    <div class="row"><span class="chip" :class="{ success: hashLive.hash_status === 'verified', error: hashLive.hash_status === 'mismatch' }"><Icon :name="hashIcon(hashLive.hash_status)" sm />{{ HASH_TEXT[hashLive.hash_status] }}</span></div>
    <div class="field"><label for="fm-sha">台账记录的 SHA-256</label><div class="row" style="flex-wrap: nowrap"><input id="fm-sha" class="input mono grow" readonly :value="hashLive.sha256 ?? '（无）'" /><button class="btn icon-only" aria-label="复制 SHA-256" title="复制" :disabled="!hashLive.sha256" @click="copyHash(hashLive.sha256)"><Icon name="content_copy" /></button></div></div>
    <p class="muted" style="margin: 0">列表不会自动计算哈希；“重新校验”会读取磁盘上的文件并与台账比对。不一致说明文件在本系统之外被修改过——系统不会改动它，也不会改写台账里的原始哈希。</p>
    <div v-if="dlgErr" class="banner error" role="alert"><Icon name="error" /><div>{{ dlgErr }}</div></div>
    <template #actions><button class="btn" @click="dlg = null">关闭</button><button class="btn filled has-icon" :disabled="verifyBusy" @click="recheck(hashLive)"><Icon :name="verifyBusy ? 'progress_activity' : 'tag'" sm :class="{ spin: verifyBusy }" />重新校验</button></template>
  </Dialog>

  <FilePreview v-if="previewing" :source="{ kind: 'archived', path: previewing.path }" :name="previewing.name" :download-url="api.contentUrl(previewing.path, true)" @close="previewing = null">
    <template #meta>
      <button v-if="listing?.entries.find((x) => x.path === previewing!.path)?.task_related" class="chip sm hs" data-testid="fm-preview-hash" @click="openHash(listing!.entries.find((x) => x.path === previewing!.path)!)"><Icon :name="hashIcon(listing!.entries.find((x) => x.path === previewing!.path)!.hash_status)" sm />{{ HASH_TEXT[listing!.entries.find((x) => x.path === previewing!.path)!.hash_status] }}</button>
      <button v-if="listing?.entries.find((x) => x.path === previewing!.path)?.sha256" class="btn sm has-icon" aria-label="复制 SHA-256" @click="copyHash(listing!.entries.find((x) => x.path === previewing!.path)!.sha256)"><Icon name="content_copy" sm />复制哈希</button>
    </template>
  </FilePreview>
</template>

<style scoped>
.crumbs { gap: 2px; margin-bottom: 12px; }
.crumb { height: 32px; padding: 0 10px; }
.cur { font-weight: 500; padding: 0 10px; }
.bar { margin-bottom: 12px; }
.caption { font-size: 12px; margin: -4px 0 12px; word-break: break-all; }
.nm { gap: 10px; flex-wrap: nowrap; } .dir { color: var(--md-sys-color-primary); }
.acts { text-align: right; }
.acts .btn.icon-only { width: 36px; height: 36px; padding: 0; justify-content: center; }
.tbl tr:focus-visible { outline: 3px solid var(--md-sys-color-primary); outline-offset: -3px; }
.btn.filled.danger-fill { background: var(--md-sys-color-error); color: var(--md-sys-color-on-error); }
.picker { border: 1px solid var(--md-sys-color-outline-variant); border-radius: var(--shape-md); max-height: 240px; overflow: auto; padding: 4px 0; }
.trow { display: flex; align-items: center; gap: 2px; } .trow.sel { background: var(--md-sys-color-secondary-container); }
.trow button { all: unset; cursor: pointer; display: inline-flex; align-items: center; gap: 6px; padding: 5px 6px; border-radius: var(--shape-sm); }
.trow button:focus-visible { outline: 3px solid var(--md-sys-color-primary); } .trow button:disabled { opacity: .3; cursor: default; }
.trow .pick { flex: 1; min-width: 0; } .trow .pick:hover { background: var(--c-surface-1); }
.trow .err { color: var(--md-sys-color-error); font-size: 12px; }
.spin { animation: spin 1s linear infinite; }
.selcol { width: 44px; padding-right: 0; } .selcol input { width: 18px; height: 18px; accent-color: var(--md-sys-color-primary); cursor: pointer; }
.hs { margin-left: 6px; } button.hs { cursor: pointer; }
.selbar { position: sticky; bottom: 12px; z-index: 5; display: flex; flex-wrap: wrap; align-items: center; gap: 4px 8px; margin-top: 12px; padding: 8px 12px 8px 20px; border-radius: var(--shape-lg); background: var(--md-sys-color-secondary-container); color: var(--md-sys-color-on-secondary-container); box-shadow: var(--elev-3); font: var(--md-sys-typescale-label-large); }
.selbar .btn { color: inherit; }
.res { list-style: none; margin: 0; padding: 0; max-height: 40vh; overflow: auto; display: flex; flex-direction: column; gap: 4px; }
.res li { gap: 8px; flex-wrap: nowrap; padding: 4px 0; } .res li.bad, strong.bad { color: var(--md-sys-color-error); }
.small { font: var(--md-sys-typescale-body-small); }
</style>
