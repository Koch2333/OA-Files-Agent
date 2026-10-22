<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import { api, type ReplicatePolicy, type StorageConnection, type StorageField, type StorageKind, type StorageTestResult } from '../api'
import { errMsg } from '../utils/useAsync'
import {
  buildCreatePayload, buildPatchPayload, buildTestPayload, emptyForm, formFromConnection, groupKinds, KIND_HINTS, kindIcon, REPLICATE_LABEL,
  validateForm, visibleFields, type FormState,
} from '../utils/storage'
import Dialog from './Dialog.vue'
import Icon from './Icon.vue'

const props = defineProps<{ kinds: StorageKind[]; connection?: StorageConnection | null }>()
const emit = defineEmits<{ close: []; saved: [c: StorageConnection] }>()

const editing = computed(() => props.connection ?? null)
const kind = ref<StorageKind | null>(editing.value ? props.kinds.find((k) => k.id === editing.value!.kind) ?? null : null)
const step = ref<1 | 2>(kind.value ? 2 : 1)
const form = reactive<FormState>(kind.value ? (editing.value ? formFromConnection(kind.value, editing.value) : emptyForm(kind.value)) : { name: '', base_path: '', enabled: true, use_for_import: true, replicate: 'off', settings: {}, secrets: {}, clearSecrets: [] })
const replacing = reactive<Record<string, boolean>>({})
const submitted = ref(false)
const saving = ref(false)
const serverErr = ref<string | null>(null)

const groups = computed(() => groupKinds(props.kinds))
const fields = computed(() => (kind.value ? visibleFields(kind.value, form.settings) : []))
const errs = computed(() => (kind.value ? validateForm(kind.value, form, editing.value) : {}))
const hint = computed(() => (kind.value ? KIND_HINTS[kind.value.id] ?? null : null))
const unknownKind = computed(() => !!editing.value && !kind.value)

function pick(k: StorageKind) {
  kind.value = k
  Object.assign(form, emptyForm(k))
  for (const key of Object.keys(replacing)) delete replacing[key]
  testRes.value = null
  step.value = 2
}

const POLICY_HELP: Record<ReplicatePolicy, string> = {
  off: '不向远端同步。',
  manual: '由你在“存储连接”页点击“同步缺失项”时才同步。',
  auto: '自动只同步启用之后新归档的文件。',
}

// ---- 凭据字段 ----
const isSaved = (f: StorageField) => !!editing.value?.secrets_set?.[f.key]
const isCleared = (f: StorageField) => form.clearSecrets.includes(f.key)
function startReplace(f: StorageField) { replacing[f.key] = true; form.clearSecrets = form.clearSecrets.filter((k) => k !== f.key) }
function cancelReplace(f: StorageField) { replacing[f.key] = false; delete form.secrets[f.key] }
function clearSecret(f: StorageField) { replacing[f.key] = false; delete form.secrets[f.key]; if (!isCleared(f)) form.clearSecrets.push(f.key) }
function undoClear(f: StorageField) { form.clearSecrets = form.clearSecrets.filter((k) => k !== f.key) }
function visibleSecretMode(f: StorageField): 'input' | 'saved' | 'cleared' {
  if (!isSaved(f)) return 'input'
  if (isCleared(f)) return 'cleared'
  return replacing[f.key] ? 'input' : 'saved'
}

// ---- 测试连接 / SFTP 主机指纹 ----
const testing = ref(false)
const testRes = ref<StorageTestResult | null>(null)
const testErr = ref<string | null>(null)
const trusted = ref(false)

async function runTest(accept?: string) {
  if (!kind.value) return
  submitted.value = true
  if (Object.keys(errs.value).length) { testErr.value = '请先补全必填项：' + Object.values(errs.value).join('；'); testRes.value = null; return }
  testing.value = true; testErr.value = null
  try {
    const res = await api.testStorageDraft(buildTestPayload(kind.value, form, { connectionId: editing.value?.id, acceptHostKey: accept }))
    testRes.value = res
    // 用户已核对并确认指纹，服务器不再要求确认：把指纹写入托管字段
    if (accept && !res.host_key) form.settings.host_key = accept
  } catch (e) { testRes.value = null; testErr.value = errMsg(e) } finally { testing.value = false }
}
function trustAndRetest() { if (testRes.value?.host_key && trusted.value) { const fp = testRes.value.host_key.fingerprint; trusted.value = false; void runTest(fp) } }
function clearPin() { form.settings.host_key = ''; testRes.value = null; trusted.value = false; testErr.value = null }

// ---- 保存 ----
async function save() {
  if (!kind.value) return
  submitted.value = true; serverErr.value = null
  if (Object.keys(errs.value).length) return
  saving.value = true
  try {
    const saved = editing.value
      ? await api.updateStorageConnection(editing.value.id, buildPatchPayload(kind.value, form, editing.value))
      : await api.createStorageConnection(buildCreatePayload(kind.value, form))
    emit('saved', saved)
  } catch (e) { serverErr.value = errMsg(e) } finally { saving.value = false }
}
const title = computed(() => (editing.value ? '编辑存储连接' : step.value === 1 ? '添加存储连接：选择类型' : `添加存储连接：${kind.value?.name ?? ''}`))
</script>

<template>
  <Dialog :title="title" width="760px" @close="emit('close')">
    <div v-if="unknownKind" class="banner error" role="alert"><Icon name="error" />当前后端不再提供类型“{{ editing?.kind }}”，无法编辑。</div>

    <!-- 第一步：选择类型 -->
    <template v-else-if="step === 1">
      <section v-for="g in groups" :key="g.group" class="col" style="gap: 8px">
        <h3 class="t-label">{{ g.label }}</h3>
        <div class="tiles">
          <button v-for="k in g.kinds" :key="k.id" type="button" class="tile" :data-kind="k.id" @click="pick(k)">
            <Icon :name="kindIcon(k.id)" class="ti" />
            <span class="grow"><b>{{ k.name }}</b><span class="muted desc">{{ k.description }}</span></span>
          </button>
        </div>
      </section>
      <p class="muted">存储连接是可选功能；不配置任何连接时，系统行为与以前完全一致。</p>
    </template>

    <!-- 第二步：动态表单 -->
    <template v-else-if="kind">
      <div class="row"><Icon :name="kindIcon(kind.id)" /><b>{{ kind.name }}</b><span class="muted grow">{{ kind.description }}</span>
        <button v-if="!editing" class="btn sm" type="button" @click="step = 1">更换类型</button></div>
      <div v-if="hint" class="banner info"><Icon name="info" sm /><div>{{ hint }}</div></div>
      <div v-if="serverErr" class="banner error" role="alert" data-testid="storage-server-error"><Icon name="error" sm /><div>{{ serverErr }}</div></div>

      <div class="grid2">
        <div class="field">
          <label for="sc-name">名称 <span class="req" aria-hidden="true">*</span></label>
          <input id="sc-name" v-model="form.name" class="input" maxlength="80" placeholder="例如：总部文件服务器" :aria-invalid="submitted && !!errs.name" />
          <span v-if="submitted && errs.name" class="err">{{ errs.name }}</span>
        </div>
        <div class="field">
          <label for="sc-base">起始目录（可选）</label>
          <input id="sc-base" v-model="form.base_path" class="input" placeholder="例如：OA归档/2026" :aria-invalid="submitted && !!errs.base_path" />
          <span v-if="submitted && errs.base_path" class="err">{{ errs.base_path }}</span>
          <span v-else class="help">浏览/导入/同步都被限制在此目录内。使用相对路径，不要以 / 开头。</span>
        </div>
      </div>

      <div class="grid2">
        <div v-for="f in fields" :key="f.key" class="field" :class="{ wide: f.type === 'textarea' }">
          <label :for="`sc-${f.key}`">{{ f.label }} <span v-if="f.required && !f.managed" class="req" aria-hidden="true">*</span><span v-if="f.required && !f.managed" class="sr-only">（必填）</span></label>

          <template v-if="f.managed">
            <input :id="`sc-${f.key}`" class="input mono" :value="form.settings[f.key] || '（尚未信任）'" readonly aria-readonly="true" />
            <button v-if="form.settings[f.key] && editing" class="btn sm danger link" type="button" @click="clearPin">清除已信任指纹</button>
          </template>

          <template v-else-if="f.secret">
            <div v-if="visibleSecretMode(f) === 'saved'" class="row secret">
              <span class="input saved" :id="`sc-${f.key}`">已保存 ••••</span>
              <button class="btn sm" type="button" @click="startReplace(f)">更换</button>
              <button class="btn sm danger" type="button" @click="clearSecret(f)">清除</button>
            </div>
            <div v-else-if="visibleSecretMode(f) === 'cleared'" class="row secret">
              <span class="input saved">保存后将清除</span>
              <button class="btn sm" type="button" @click="undoClear(f)">撤销</button>
            </div>
            <div v-else class="row secret">
              <textarea v-if="f.type === 'textarea'" :id="`sc-${f.key}`" v-model="form.secrets[f.key]" class="input mono" rows="4" autocomplete="off" spellcheck="false" :placeholder="f.placeholder" :aria-invalid="submitted && !!errs[f.key]" />
              <input v-else :id="`sc-${f.key}`" v-model="form.secrets[f.key]" class="input grow" :type="f.type === 'password' ? 'password' : 'text'" autocomplete="new-password" spellcheck="false" :placeholder="f.placeholder" :aria-invalid="submitted && !!errs[f.key]" />
              <button v-if="isSaved(f)" class="btn sm" type="button" @click="cancelReplace(f)">取消</button>
            </div>
          </template>

          <select v-else-if="f.type === 'select'" :id="`sc-${f.key}`" v-model="form.settings[f.key]" class="input">
            <option v-for="o in f.options" :key="o.value" :value="o.value">{{ o.label }}</option>
          </select>
          <textarea v-else-if="f.type === 'textarea'" :id="`sc-${f.key}`" v-model="form.settings[f.key]" class="input mono" rows="4" :placeholder="f.placeholder" :aria-invalid="submitted && !!errs[f.key]" />
          <input v-else :id="`sc-${f.key}`" v-model="form.settings[f.key]" class="input" :type="f.type === 'number' ? 'number' : f.type === 'password' ? 'password' : 'text'" :placeholder="f.placeholder || f.default" :aria-invalid="submitted && !!errs[f.key]" />

          <span v-if="submitted && errs[f.key]" class="err">{{ errs[f.key] }}</span>
          <span v-else-if="f.help" class="help">{{ f.help }}</span>
        </div>
      </div>

      <fieldset class="col opts">
        <legend class="t-label">使用方式</legend>
        <label class="switch"><input v-model="form.enabled" type="checkbox" role="switch" />启用此连接</label>
        <label class="switch"><input v-model="form.use_for_import" type="checkbox" role="switch" />用于导入（可在“智能归档”页从该存储选择文件）</label>
        <div class="field">
          <label for="sc-rep">同步策略</label>
          <select id="sc-rep" v-model="form.replicate" class="input" style="max-width: 220px">
            <option v-for="(label, k) in REPLICATE_LABEL" :key="k" :value="k">{{ label }}</option>
          </select>
          <span class="help">{{ POLICY_HELP[form.replicate] }}同步只增不改不删；在本系统内移动、删除或撤销归档不会传播到远端。</span>
        </div>
      </fieldset>

      <!-- 测试连接 -->
      <section class="col testbox" aria-live="polite">
        <div class="row">
          <button class="btn tonal has-icon" type="button" :disabled="testing" data-testid="storage-test" @click="runTest()"><Icon :name="testing ? 'progress_activity' : 'network_check'" :class="{ spin: testing }" sm />测试连接</button>
          <span class="muted">{{ form.replicate !== 'off' ? '将同时检查写入权限（同步需要）。' : '同步策略为关闭，仅检查连接与读取目录。' }}</span>
        </div>
        <div v-if="testErr" class="banner error" role="alert"><Icon name="error" sm /><div>{{ testErr }}</div></div>
        <template v-if="testRes">
          <ul class="steps">
            <li v-for="s in testRes.steps" :key="s.name" :class="s.ok ? 'ok' : 'bad'"><Icon :name="s.ok ? 'check_circle' : 'cancel'" sm /><b>{{ s.name }}</b><span>{{ s.message }}</span></li>
          </ul>
          <div v-if="testRes.ok" class="banner info" style="background: var(--c-success-container); color: var(--c-on-success-container)"><Icon name="check_circle" sm /><div>测试通过（{{ testRes.latency_ms }} ms）。</div></div>
          <div v-else-if="!testRes.host_key" class="banner error" role="alert"><Icon name="error" sm /><div>测试未通过，请根据上方提示修改后重试。</div></div>

          <div v-if="testRes.host_key" class="hostkey" :class="{ changed: testRes.host_key.changed }" data-testid="host-key">
            <template v-if="testRes.host_key.changed">
              <div class="row"><Icon name="gpp_bad" /><b>警告：主机密钥已变化</b></div>
              <p>服务器出示的指纹与之前信任的不一致，可能遭遇中间人攻击，或服务器被重装。请先通过可信渠道向管理员核实，再决定是否信任新指纹。</p>
              <code class="fp">{{ testRes.host_key.fingerprint }}</code>
              <p class="muted">出于安全，不提供一键信任。如确认服务器确已更换密钥，请主动“清除已信任指纹”，之后重新测试并按首次连接流程核对。</p>
              <button class="btn sm danger" type="button" @click="clearPin">清除已信任指纹</button>
            </template>
            <template v-else>
              <div class="row"><Icon name="fingerprint" /><b>首次连接：请通过可信渠道核对该指纹</b></div>
              <code class="fp">{{ testRes.host_key.fingerprint }}</code>
              <label class="check"><input v-model="trusted" type="checkbox" data-testid="host-key-trust" />我已核对指纹，信任该主机</label>
              <button class="btn sm filled" type="button" :disabled="!trusted || testing" @click="trustAndRetest">信任并重新测试</button>
            </template>
          </div>
        </template>
      </section>

      <div class="banner info"><Icon name="privacy_tip" sm /><div>通过连接导入或同步的文档会经过/保存到所选存储；涉密文件请谨慎。</div></div>
    </template>

    <template #actions>
      <button class="btn" type="button" @click="emit('close')">取消</button>
      <button v-if="step === 2 && !editing" class="btn" type="button" @click="step = 1">上一步</button>
      <button v-if="step === 2 && kind" class="btn filled" type="button" :disabled="saving" data-testid="storage-save" @click="save">{{ saving ? '正在保存…' : editing ? '保存修改' : '创建连接' }}</button>
    </template>
  </Dialog>
</template>

<style scoped>
.tiles { display: grid; grid-template-columns: repeat(auto-fill, minmax(260px, 1fr)); gap: 10px; }
.tile { display: flex; gap: 12px; align-items: flex-start; text-align: left; padding: 14px; border: 1px solid var(--md-sys-color-outline-variant); border-radius: var(--shape-md); background: transparent; color: inherit; font: inherit; cursor: pointer; }
.tile:hover { background: color-mix(in srgb, var(--md-sys-color-primary) 8%, transparent); border-color: var(--md-sys-color-primary); }
.tile:focus-visible { outline: 2px solid var(--md-sys-color-primary); outline-offset: 2px; }
.ti { color: var(--md-sys-color-primary); }
.desc { display: block; font-size: 12px; margin-top: 2px; }
.grid2 { display: grid; grid-template-columns: 1fr 1fr; gap: 14px 16px; }
.wide { grid-column: 1 / -1; }
.req { color: var(--md-sys-color-error); }
.err { color: var(--md-sys-color-error); font-size: 12px; }
.help { color: var(--md-sys-color-on-surface-variant); font-size: 12px; }
.secret { align-items: flex-start; gap: 8px; }
.saved { display: inline-flex; align-items: center; flex: 1; color: var(--md-sys-color-on-surface-variant); }
.link { align-self: flex-start; }
.opts { border: 0; padding: 0; margin: 0; }
.testbox { border: 1px solid var(--md-sys-color-outline-variant); border-radius: var(--shape-md); padding: 14px; }
.steps { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 6px; }
.steps li { display: flex; gap: 8px; align-items: center; }
.steps li.ok .icon { color: var(--c-success, #2e7d32); } .steps li.bad .icon { color: var(--md-sys-color-error); }
.steps li span { color: var(--md-sys-color-on-surface-variant); }
.hostkey { border: 2px solid var(--md-sys-color-primary); border-radius: var(--shape-md); padding: 14px; display: flex; flex-direction: column; gap: 8px; align-items: flex-start; }
.hostkey.changed { border-color: var(--md-sys-color-error); background: var(--md-sys-color-error-container); color: var(--md-sys-color-on-error-container); }
.hostkey p { margin: 0; }
.fp { font: 600 14px var(--mono); word-break: break-all; padding: 8px 10px; background: var(--c-surface-3); color: var(--md-sys-color-on-surface); border-radius: var(--shape-sm); }
.check { display: flex; gap: 8px; align-items: center; cursor: pointer; }
@media (max-width: 720px) { .grid2 { grid-template-columns: 1fr; } }
</style>
