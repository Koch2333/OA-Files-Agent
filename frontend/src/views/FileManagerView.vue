<script setup lang="ts">
import { useAuth } from '../stores/auth'
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api, type FsEntry, type TrashItem } from '../api'
import { errMsg, useAsync } from '../utils/useAsync'
import { fmtBytes, fmtTime } from '../utils/format'
import { basename, breadcrumbs, dirname, isInside, join, normalize, validateName, validatePath } from '../utils/fsPaths'
import { useUi } from '../stores/ui'
import PageHeader from '../components/PageHeader.vue'
import StateView from '../components/StateView.vue'
import Icon from '../components/Icon.vue'
import Dialog from '../components/Dialog.vue'
import FilePreview from '../components/FilePreview.vue'

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
watch(path, () => { filter.value = ''; void list.run() })
watch(tab, (t) => { if (t === 'trash') void trash.run() })

const listing = computed(() => list.data.value)
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
  if (e.is_dir) return 'folder'
  const n = e.name.toLowerCase()
  if (/\.pdf$/.test(n)) return 'picture_as_pdf'
  if (/\.(png|jpe?g)$/.test(n)) return 'image'
  if (/\.xlsx?$/.test(n)) return 'table_chart'
  if (/\.docx?$/.test(n)) return 'description'
  return 'draft'
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
function download(e: FsEntry) {
  if (demoBlocked()) return
  const a = document.createElement('a')
  a.href = api.contentUrl(e.path, true)
  a.download = e.name
  document.body.appendChild(a); a.click(); a.remove()
}
async function reveal(p: string) {
  try { await api.revealInExplorer(p); ui.toast('已在资源管理器中打开', 'success') } catch (e) { ui.toast('无法打开资源管理器：' + errMsg(e), 'error') }
}

// ---- 对话框：新建 / 重命名 / 移动 / 删除 ----
const dlg = ref<null | 'mkdir' | 'rename' | 'move' | 'delete'>(null)
const target = ref<FsEntry | null>(null)
const nameInput = ref('')
const dlgErr = ref<string | null>(null)
const busy = ref(false)

function openDlg(kind: 'mkdir' | 'rename' | 'move' | 'delete', e?: FsEntry) {
  dlg.value = kind; target.value = e ?? null; dlgErr.value = null; busy.value = false
  if (kind === 'mkdir') nameInput.value = ''
  if (kind === 'rename' && e) nameInput.value = e.name
  if (kind === 'move' && e) { moveDir.value = dirname(e.path); void initTree(dirname(e.path)) }
}
function closeDlg() { if (!busy.value) dlg.value = null }

async function submit(fn: () => Promise<string>) {
  busy.value = true; dlgErr.value = null
  try { const msg = await fn(); dlg.value = null; ui.toast(msg, 'success'); refresh() }
  catch (e) { dlgErr.value = errMsg(e) }
  finally { busy.value = false }
}
function doMkdir() {
  const err = validateName(nameInput.value)
  if (err) { dlgErr.value = err; return }
  const name = nameInput.value.trim()
  void submit(async () => { await api.createFolder(join(path.value, name)); return `已新建文件夹“${name}”` })
}
function doRename() {
  const e = target.value!
  const err = validateName(nameInput.value)
  if (err) { dlgErr.value = err; return }
  const name = nameInput.value.trim()
  if (name === e.name) { dlgErr.value = '新名称与原名称相同'; return }
  void submit(async () => { const r = await api.moveEntry(e.path, join(dirname(e.path), name)); return `已重命名为“${name}”${ledgerNote(r.ledger_updated)}` })
}
function doMove() {
  const e = target.value!
  const dir = normalize(moveDir.value)
  const err = validatePath(dir)
  if (err) { dlgErr.value = err; return }
  if (e.is_dir && isInside(dir, e.path)) { dlgErr.value = '不能把文件夹移动到它自身内部'; return }
  const to = join(dir, e.name)
  if (to === e.path) { dlgErr.value = '目标就是当前位置'; return }
  void submit(async () => { const r = await api.moveEntry(e.path, to); return `已移动到“${dir || '归档根目录'}”${ledgerNote(r.ledger_updated)}` })
}
function doDelete() {
  const e = target.value!
  void submit(async () => { await api.trashEntry(e.path); return `已将“${e.name}”移入回收站；对应台账记录已标记为已移除` })
}

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
  const walk = (p: string, depth: number) => {
    const n = node(p)
    out.push({ path: p, depth, n })
    if (n.open) for (const c of n.children) {
      if (target.value?.is_dir && isInside(c, target.value.path)) continue
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
    const r = await api.restoreTrash(t.id)
    ui.toast(`已还原到“${r.path}”${ledgerNote(r.ledger_updated)}`, 'success')
    void trash.run(); void list.run()
  } catch (e) { ui.toast('还原失败：' + errMsg(e), 'error') } finally { restoring.value = null }
}
</script>

<template>
  <PageHeader title="文件管理" subtitle="浏览归档目录，重命名、移动、删除到回收站；台账记录会同步更新。">
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

    <div class="card flat">
      <StateView :loading="list.loading.value && !listing" :error="list.error.value" :empty="!rows.length" :empty-title="filter ? `没有匹配“${filter}”的项目` : '此文件夹为空'" :empty-hint="filter ? '清除筛选条件后重试。' : '可以新建文件夹，或先在“智能归档”中导入文件。'" empty-icon="folder_off" @retry="list.run">
        <div class="table-wrap">
          <table class="tbl">
            <thead>
              <tr>
                <th class="sortable" :aria-sort="ariaSort('name')" tabindex="0" @click="sortBy('name')" @keydown.enter="sortBy('name')">名称<Icon v-if="sort === 'name'" :name="order === 'asc' ? 'arrow_upward' : 'arrow_downward'" sm /></th>
                <th>台账信息</th>
                <th class="sortable" :aria-sort="ariaSort('size')" tabindex="0" @click="sortBy('size')" @keydown.enter="sortBy('size')">大小<Icon v-if="sort === 'size'" :name="order === 'asc' ? 'arrow_upward' : 'arrow_downward'" sm /></th>
                <th class="sortable" :aria-sort="ariaSort('modified')" tabindex="0" @click="sortBy('modified')" @keydown.enter="sortBy('modified')">修改时间<Icon v-if="sort === 'modified'" :name="order === 'asc' ? 'arrow_upward' : 'arrow_downward'" sm /></th>
                <th><span class="sr-only">操作</span></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="e in rows" :key="e.path" class="clickable" tabindex="0" :aria-label="(e.is_dir ? '文件夹 ' : '文件 ') + e.name" @click="openEntry(e)" @keydown.enter.self="openEntry(e)">
                <td><div class="row nm"><Icon :name="iconOf(e)" :fill="e.is_dir" :class="{ dir: e.is_dir }" /><span class="ellipsis" style="max-width: 360px" :title="e.name">{{ e.name }}</span></div></td>
                <td>
                  <span v-if="e.ledger" class="chip" :title="`原文件名：${e.ledger.original_name}`">{{ e.ledger.department }}·{{ e.ledger.document_type }}·{{ e.ledger.document_date }}</span>
                </td>
                <td class="muted nowrap">{{ e.is_dir ? '—' : fmtBytes(e.size_bytes) }}</td>
                <td class="muted nowrap">{{ fmtTime(e.modified_at) }}</td>
                <td class="nowrap acts" @click.stop>
                  <button v-if="!e.is_dir" class="btn icon-only" :aria-label="'预览 ' + e.name" title="预览" data-testid="fm-preview" @click="openEntry(e)"><Icon name="visibility" /></button>
                  <button v-if="!e.is_dir" class="btn icon-only" :aria-label="'下载 ' + e.name" title="下载" @click="download(e)"><Icon name="download" /></button>
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
  </template>

  <template v-else>
    <div class="row wrap bar">
      <p class="muted grow" style="margin: 0">删除的项目保存在回收站，可随时还原；原位置被占用时会自动追加序号。</p>
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

  <Dialog v-if="dlg === 'move' && target" :title="`移动“${target.name}”`" width="520px" @close="closeDlg">
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

  <Dialog v-if="dlg === 'delete' && target" title="删除到回收站" width="480px" @close="closeDlg">
    <p style="margin: 0">确定删除{{ target.is_dir ? '文件夹' : '文件' }}“<strong>{{ target.name }}</strong>”吗？</p>
    <p class="muted" style="margin: 0">它会被移入回收站，之后可以还原；对应的台账记录会被标记为已移除{{ target.is_dir ? '（包含文件夹内的全部文件）' : '' }}。</p>
    <div v-if="dlgErr" class="banner error" role="alert"><Icon name="error" /><div>{{ dlgErr }}</div></div>
    <template #actions><button class="btn" :disabled="busy" @click="closeDlg">取消</button><button class="btn filled danger-fill" :disabled="busy" @click="doDelete">删除</button></template>
  </Dialog>
  <FilePreview v-if="previewing" :source="{ kind: 'archived', path: previewing.path }" :name="previewing.name" :download-url="api.contentUrl(previewing.path, true)" @close="previewing = null" />
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
</style>
