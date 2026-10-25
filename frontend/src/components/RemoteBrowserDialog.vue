<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { api, type StorageConnection, type StorageEntry, type StorageListing } from '../api'
import { errMsg } from '../utils/useAsync'
import { fmtBytes, fmtTime } from '../utils/format'
import { crumbs, kindIcon, quotaHint, REMOTE_IMPORT_EXT } from '../utils/storage'
import Dialog from './Dialog.vue'
import StateView from './StateView.vue'
import Icon from './Icon.vue'

const props = withDefaults(defineProps<{
  connections: StorageConnection[]
  initialId?: string
  /** false = 仅浏览（存储连接页）；true = 可勾选并导入。 */
  selectable?: boolean
  busy?: boolean
  error?: string | null
}>(), { selectable: true, initialId: '', busy: false, error: null })
const emit = defineEmits<{ close: []; import: [payload: { connectionId: string; paths: string[] }] }>()

const usable = computed(() => props.connections.filter((c) => (props.selectable ? c.enabled && c.use_for_import : true)))
const connId = ref(props.initialId && usable.value.some((c) => c.id === props.initialId) ? props.initialId : usable.value[0]?.id ?? '')
const path = ref('')
const listing = ref<StorageListing | null>(null)
const loading = ref(false)
const err = ref<string | null>(null)
const selected = ref<Map<string, StorageEntry>>(new Map())
let seq = 0

const conn = computed(() => usable.value.find((c) => c.id === connId.value) ?? null)
const trail = computed(() => crumbs(path.value))
const supported = (e: StorageEntry) => e.is_dir || REMOTE_IMPORT_EXT.test(e.name)

async function load() {
  if (!connId.value) return
  const my = ++seq
  loading.value = true; err.value = null
  try {
    const l = await api.browseStorage(connId.value, path.value)
    if (my === seq) listing.value = l
  } catch (e) { if (my === seq) { err.value = errMsg(e); listing.value = null } } finally { if (my === seq) loading.value = false }
}
function go(p: string) { path.value = p; void load() }
onMounted(load)
watch(connId, () => { selected.value = new Map(); go('') })

const entries = computed(() => listing.value?.entries ?? [])
function toggle(e: StorageEntry) {
  const m = new Map(selected.value)
  if (m.has(e.path)) m.delete(e.path); else m.set(e.path, e)
  selected.value = m
}
const selFiles = computed(() => [...selected.value.values()].filter((e) => !e.is_dir))
const selDirs = computed(() => [...selected.value.values()].filter((e) => e.is_dir))
const allChecked = computed(() => { const sel = entries.value.filter(supported); return !!sel.length && sel.every((e) => selected.value.has(e.path)) })
function toggleAll() {
  const m = new Map(selected.value)
  const sel = entries.value.filter(supported)
  if (allChecked.value) sel.forEach((e) => m.delete(e.path)); else sel.forEach((e) => m.set(e.path, e))
  selected.value = m
}
function submit() { emit('import', { connectionId: connId.value, paths: [...selected.value.keys()] }) }
</script>

<template>
  <Dialog :title="selectable ? '从存储连接导入' : '浏览存储连接'" width="860px" @close="emit('close')">
    <div class="row wrap">
      <div class="field" style="min-width: 240px">
        <label for="rb-conn">存储连接</label>
        <select id="rb-conn" v-model="connId" class="input">
          <option v-for="c in usable" :key="c.id" :value="c.id">{{ c.name }}</option>
        </select>
      </div>
      <Icon v-if="conn" :name="kindIcon(conn.kind)" class="muted" />
    </div>

    <nav class="row wrap crumbs" aria-label="远端路径">
      <button class="btn sm" type="button" @click="go('')"><Icon name="home" sm />根目录</button>
      <template v-for="c in trail" :key="c.path"><Icon name="chevron_right" sm /><button class="btn sm" type="button" @click="go(c.path)">{{ c.name }}</button></template>
      <span class="grow" />
      <button v-if="listing?.parent !== null && listing" class="btn sm has-icon" type="button" @click="go(listing.parent ?? '')"><Icon name="arrow_upward" sm />上一级</button>
    </nav>

    <div class="listbox">
      <StateView :loading="loading" :error="err" :empty="!!listing && !entries.length" empty-icon="folder_off" empty-title="此目录为空" @retry="load">
        <table v-if="listing" class="tbl">
          <thead>
            <tr>
              <th style="width: 44px"><input v-if="selectable" type="checkbox" :checked="allChecked" aria-label="全选当前目录中可导入的项目" @change="toggleAll" /></th>
              <th>名称</th><th>大小</th><th>修改时间</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="e in entries" :key="e.path" :class="{ selected: selected.has(e.path) }" :aria-label="`${e.is_dir ? '文件夹' : '文件'} ${e.name}`">
              <td><input v-if="selectable" type="checkbox" :checked="selected.has(e.path)" :disabled="!supported(e)" :aria-label="`选择 ${e.name}`" @change="toggle(e)" /></td>
              <td>
                <button v-if="e.is_dir" class="linkbtn" type="button" @click="go(e.path)"><Icon name="folder" sm />{{ e.name }}</button>
                <span v-else class="row" style="gap: 6px"><Icon name="description" sm />{{ e.name }}<span v-if="!supported(e)" class="chip attention">不支持</span></span>
              </td>
              <td class="nowrap muted">{{ e.is_dir ? '—' : fmtBytes(e.size_bytes) }}</td>
              <td class="nowrap muted">{{ e.modified_at ? fmtTime(e.modified_at) : '—' }}</td>
            </tr>
          </tbody>
        </table>
        <p v-if="listing?.truncated" class="muted" style="padding: 8px 16px">目录项过多，仅显示前 5000 项。</p>
      </StateView>
    </div>

    <template v-if="selectable">
      <div class="row wrap">
        <span data-testid="remote-count">已选 <b>{{ selected.size }}</b> 项（{{ selFiles.length }} 个文件，{{ selDirs.length }} 个文件夹，文件夹递归导入）</span>
      </div>
      <p class="muted small">不限制文件数量和大小；仅支持 docx/xlsx/pdf/png/jpg，格式不支持或空文件会作为失败项计入，不会让整批失败。超大文件只做元数据判断并标记为待人工审核。</p>
      <div v-if="conn && quotaHint(conn.kind)" class="banner info"><Icon name="info" sm />{{ quotaHint(conn.kind) }}</div>
      <div v-if="error" class="banner error" role="alert"><Icon name="error" sm /><div>{{ error }}</div></div>
    </template>

    <template #actions>
      <button class="btn" type="button" @click="emit('close')">{{ selectable ? '取消' : '关闭' }}</button>
      <button v-if="selectable" class="btn filled has-icon" type="button" data-testid="remote-import" :disabled="!selected.size || busy" @click="submit">
        <Icon :name="busy ? 'progress_activity' : 'download'" :class="{ spin: busy }" sm />{{ busy ? '正在导入…' : `导入（${selected.size}）` }}
      </button>
    </template>
  </Dialog>
</template>

<style scoped>
.crumbs { gap: 4px; }
.listbox { border: 1px solid var(--md-sys-color-outline-variant); border-radius: var(--shape-md); max-height: 340px; overflow: auto; min-height: 120px; }
.linkbtn { display: inline-flex; gap: 6px; align-items: center; background: none; border: 0; padding: 4px 6px; margin: -4px -6px; color: var(--md-sys-color-primary); font: 500 14px var(--font); cursor: pointer; border-radius: var(--shape-sm); }
.linkbtn:hover { text-decoration: underline; }
.linkbtn:focus-visible { outline: 2px solid var(--md-sys-color-primary); }
.small { font-size: 12px; margin: 0; }
</style>
