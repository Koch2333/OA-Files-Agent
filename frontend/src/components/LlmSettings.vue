<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import { api } from '../api'
import type { LlmConfig, LlmConfigInput, LlmPreset, LlmTestInput, LlmTestResult } from '../api'
import { useAsync } from '../utils/useAsync'
import { useUi } from '../stores/ui'
import { useAi } from '../stores/ai'
import { applyPreset, buildInput, canFetchModels, engineLabel, formFromConfig, isDirty, llmErrorText, modelOptions, savedKeyUsable, validateForm, visionOptions, type LlmForm, type LlmScope } from '../utils/llm'
import StateView from './StateView.vue'
import ConfirmDialog from './ConfirmDialog.vue'
import ModelCombobox from './ModelCombobox.vue'
import Icon from './Icon.vue'

/** 一个组件同时实现“全局”与“个人”两种配置：区别只在接口与文案（scope），表单/测试/模型发现逻辑完全共用。 */
export interface LlmFns {
  presets: () => Promise<LlmPreset[]>
  get: () => Promise<LlmConfig>
  save: (i: LlmConfigInput) => Promise<LlmConfig>
  test: (i: LlmTestInput) => Promise<LlmTestResult>
  models: (i: { base_url: string; api_key?: string }) => Promise<string[]>
  remove?: () => Promise<void>
}
const props = withDefaults(defineProps<{ scope?: LlmScope; fns?: LlmFns; /** 无卡片外框与标题（向导中使用） */ embedded?: boolean }>(), { scope: 'global', fns: undefined, embedded: false })
const emit = defineEmits<{ saved: [cfg: LlmConfig]; removed: [] }>()
const isPersonal = computed(() => props.scope === 'personal')
const fns = computed<LlmFns>(() => props.fns ?? (isPersonal.value
  ? { presets: () => api.listLlmPresets(), get: () => api.getPersonalLlm(), save: (i) => api.savePersonalLlm(i), test: (i) => api.testPersonalLlm(i), models: (i) => api.listPersonalLlmModels(i), remove: () => api.deletePersonalLlm() }
  : { presets: () => api.listLlmPresets(), get: () => api.getLlmConfig(), save: (i) => api.saveLlmConfig(i), test: (i) => api.testLlm(i), models: (i) => api.listLlmModels(i) }))
/** 元素 id 与 data-testid 前缀：全局保持原值（e2e 依赖），个人加前缀避免同页冲突 */
const pfx = computed(() => (isPersonal.value ? 'pllm' : 'llm'))
const tid = (n: string) => (isPersonal.value ? `personal-llm-${n}` : `llm-${n}`)

const ui = useUi()
const ai = useAi()
const load = useAsync(async () => {
  const [presets, config] = await Promise.all([fns.value.presets(), fns.value.get()])
  return { presets, config }
})
const confirmRemove = ref(false)
const removing = ref(false)
const presets = computed<LlmPreset[]>(() => load.data.value?.presets ?? [])
const saved = shallowRef<LlmConfig>()
const form = ref<LlmForm>({ mode: 'heuristic', preset_id: '', base_url: '', model: '', api_key: '', clear_key: false, vision_model: '', allow_vision: false })
const replacing = ref(false)
const touched = ref(false)
const advOpen = ref(false)
const testing = ref(false)
const saving = ref(false)
const testResult = ref<LlmTestResult | null>(null)
const testError = ref<string | null>(null)
const saveError = ref<string | null>(null)

// 服务商返回的实时模型列表
const live = ref<string[]>([])
const fetchedCount = ref<number | null>(null)
const fetching = ref(false)
const fetchError = ref<string | null>(null)
let fetchSeq = 0
let timer: ReturnType<typeof setTimeout> | undefined

async function init() {
  await load.run()
  if (!load.data.value) return
  saved.value = load.data.value.config
  form.value = formFromConfig(saved.value, presets.value)
  if (isPersonal.value) form.value.mode = 'llm' // 个人配置没有“离线规则”：删除即回到全局
  advOpen.value = form.value.allow_vision
  replacing.value = false
  touched.value = false
}
onMounted(init)
onBeforeUnmount(() => { clearTimeout(timer); fetchSeq++ })

const preset = computed(() => presets.value.find((p) => p.id === form.value.preset_id))
const errors = computed(() => validateForm(form.value, preset.value, saved.value))
const dirty = computed(() => isDirty(form.value, saved.value))
const hasSaved = computed(() => saved.value?.mode === 'llm' && saved.value.state !== 'unconfigured')
const valid = computed(() => form.value.mode === 'heuristic' || Object.keys(errors.value).length === 0)
const suggestions = computed(() => modelOptions(preset.value, []))
const visionSuggestions = computed(() => visionOptions(preset.value))
const keyUsable = computed(() => savedKeyUsable(form.value, saved.value))
const showSavedKey = computed(() => keyUsable.value && !replacing.value)
const engine = computed(() => engineLabel(saved.value, presets.value))
const showErr = (k: keyof typeof errors.value) => (touched.value ? errors.value[k] : undefined)

// 参数变化后，之前的测试结果不再适用
watch(() => [form.value.base_url, form.value.model, form.value.api_key, form.value.preset_id, form.value.vision_model], () => { testResult.value = null; testError.value = null })
// 换服务商 / Base URL：旧列表作废
watch(() => [form.value.base_url, form.value.preset_id], () => { live.value = []; fetchedCount.value = null; fetchError.value = null; fetchSeq++ })

/** 拉取实时模型列表；auto 为自动触发（失败只显示错误状态，不弹提示）。 */
async function fetchModels() {
  const my = ++fetchSeq
  fetching.value = true; fetchError.value = null
  try {
    const list = await fns.value.models({ base_url: form.value.base_url.trim(), api_key: typedKey() })
    if (my !== fetchSeq) return
    live.value = list; fetchedCount.value = list.length
  } catch (e) {
    if (my !== fetchSeq) return
    live.value = []; fetchedCount.value = null; fetchError.value = llmErrorText(e, props.scope)
  } finally { if (my === fetchSeq) fetching.value = false }
}
function refreshModels() {
  touched.value = true
  if (errors.value.base_url) return
  clearTimeout(timer)
  void fetchModels()
}
// 自动获取：条件满足后防抖 600ms；条件不满足则取消
watch(() => [form.value.mode, form.value.base_url, form.value.preset_id, form.value.api_key, keyUsable.value, preset.value?.needs_api_key], () => {
  clearTimeout(timer)
  if (!canFetchModels(form.value, preset.value, saved.value)) return
  timer = setTimeout(() => { void fetchModels() }, 600)
})

async function removePersonal() {
  if (!fns.value.remove) return
  removing.value = true; saveError.value = null
  try {
    await fns.value.remove()
    confirmRemove.value = false
    ui.toast('已删除个人配置，之后的任务使用全局大模型', 'success')
    await init()
    emit('removed')
    void ai.refresh()
  } catch (e) { confirmRemove.value = false; saveError.value = llmErrorText(e, props.scope) } finally { removing.value = false }
}
function setMode(m: 'heuristic' | 'llm') { form.value.mode = m; saveError.value = null }
function pick(p: LlmPreset) { form.value = applyPreset(form.value, p); replacing.value = false; touched.value = false }
function startReplace() { replacing.value = true; form.value.clear_key = false }
function clearKey() { form.value.clear_key = true; form.value.api_key = ''; replacing.value = true }
function undoClear() { form.value.clear_key = false; replacing.value = false }

/** 测试/拉取只发送刚输入的密钥；留空则由服务端使用已保存的密钥。 */
function typedKey() { return form.value.api_key.trim() || undefined }

async function runTest() {
  touched.value = true
  if (errors.value.base_url || errors.value.model) return
  testing.value = true; testResult.value = null; testError.value = null
  try { testResult.value = await fns.value.test({ base_url: form.value.base_url.trim(), model: form.value.model.trim(), vision_model: form.value.vision_model.trim() || undefined, api_key: typedKey() }) }
  catch (e) { testError.value = llmErrorText(e, props.scope) }
  finally { testing.value = false }
}

async function save() {
  touched.value = true
  if (errors.value.vision_model) advOpen.value = true
  if (!valid.value) return
  saving.value = true; saveError.value = null
  try {
    saved.value = await fns.value.save(buildInput(form.value))
    form.value = { ...form.value, api_key: '', clear_key: false }
    replacing.value = false
    ui.toast(isPersonal.value ? '个人大模型已保存，你的任务将优先使用它' : '已生效，无需重启', 'success')
    emit('saved', saved.value)
    void ai.refresh()
  } catch (e) { saveError.value = llmErrorText(e, props.scope) }
  finally { saving.value = false }
}
</script>

<template>
  <section :class="embedded ? 'col' : 'card col llm'" :aria-label="isPersonal ? '我的个人大模型' : '识别引擎'" :data-scope="scope" :data-testid="isPersonal ? 'personal-llm-card' : 'global-llm-card'">
    <div class="row wrap">
      <h2 v-if="!embedded" class="t-title-m grow">{{ isPersonal ? '我的个人大模型' : '全局大模型（全员共用）' }}</h2>
      <span v-else class="grow" />
      <span v-if="saved && (!isPersonal || hasSaved)" class="chip" :class="saved.mode === 'llm' ? (saved.state === 'ok' ? 'success' : 'error') : ''" :data-testid="tid('engine')"><Icon :name="saved.mode === 'llm' ? 'cloud' : 'offline_bolt'" sm />{{ isPersonal ? '我的配置' : '当前' }}：{{ engine }}</span>
      <span v-else-if="isPersonal && saved" class="chip" :data-testid="tid('engine')">未设置个人配置</span>
    </div>
    <p v-if="!embedded && !isPersonal" class="muted note" data-testid="global-llm-note">模型 Provider：这里配置的大模型是全员默认使用的；员工的任务都会使用它，除非员工在“我的个人大模型”中设置了自己的配置（需管理员在下方“AI 使用策略”中允许）。</p>
    <p v-if="isPersonal" class="muted note" data-testid="personal-llm-note">只对你本人提交的任务生效，优先于全局大模型；密钥加密保存在服务器，只有尾号可见。<b>地址规则比全局更严格</b>：默认只允许公网 https 地址，不能填写内网/本机地址（管理员可放开）。文档文字会发送到你填写的服务商，请遵守公司的数据规定。</p>
    <StateView :loading="load.loading.value && !load.data.value" :error="load.error.value" :empty="false" :rows="3" @retry="init">
      <template v-if="load.data.value">
        <div v-if="!isPersonal" class="field"><span class="lbl" :id="pfx + '-mode-lbl'">识别方式</span>
          <div class="tiles" role="radiogroup" :aria-labelledby="pfx + '-mode-lbl'">
            <button class="tile" role="radio" data-mode="heuristic" :aria-checked="form.mode === 'heuristic'" @click="setMode('heuristic')">
              <Icon name="offline_bolt" /><span class="tt">离线规则</span><span class="muted td">不联网、无需密钥</span>
            </button>
            <button class="tile" role="radio" data-mode="llm" :aria-checked="form.mode === 'llm'" @click="setMode('llm')">
              <Icon name="smart_toy" /><span class="tt">大模型</span><span class="muted td">DeepSeek/通义千问等，文档文字会发送给服务商</span>
            </button>
          </div>
        </div>

        <p v-if="form.mode === 'heuristic'" class="muted">使用本机规则与关键词识别文档，不会联网，也不会把文档文字发送给任何模型服务商。</p>

        <template v-else>
          <div class="field"><span class="lbl" :id="pfx + '-preset-lbl'">模型服务商</span>
            <div class="chips" role="radiogroup" :aria-labelledby="pfx + '-preset-lbl'">
              <button v-for="p in presets" :key="p.id" class="pchip" role="radio" :aria-checked="form.preset_id === p.id" :data-preset="p.id" @click="pick(p)">{{ p.name }}</button>
            </div>
            <p v-if="preset" class="muted hint info" :data-testid="tid('preset-info')">
              {{ preset.description }}<template v-if="preset.note"> · {{ preset.note }}</template>
              <template v-if="preset.docs_url"> · <a :href="preset.docs_url" target="_blank" rel="noopener noreferrer">文档链接</a></template>
              <template v-if="preset.checked_at"> · 建议模型核对于 {{ preset.checked_at }}，以服务商返回的列表为准</template>
            </p>
          </div>

          <div class="grid2">
            <div class="field full"><label :for="pfx + '-base'">Base URL</label>
              <input :id="pfx + '-base'" v-model="form.base_url" class="input mono" type="text" spellcheck="false" autocomplete="off" placeholder="https://api.example.com/v1" :aria-invalid="!!showErr('base_url')" />
              <span v-if="showErr('base_url')" class="err-text" role="alert">{{ showErr('base_url') }}</span>
              <span v-else class="muted hint">需为 OpenAI 兼容接口（请求发往 {Base URL}/chat/completions）。公网地址须使用 https，http 仅允许本机或内网地址。</span>
            </div>

            <div class="field"><label :for="pfx + '-model'">模型</label>
              <ModelCombobox :id="pfx + '-model'" v-model="form.model" :live="live" :suggestions="suggestions" placeholder="输入或选择模型名" :invalid="!!showErr('model')" refreshable :fetching="fetching" :fetch-error="fetchError" :fetched-count="fetchedCount" @refresh="refreshModels" />
              <span v-if="showErr('model')" class="err-text" role="alert">{{ showErr('model') }}</span>
            </div>

            <div class="field"><label :for="pfx + '-key'">API Key</label>
              <div v-if="showSavedKey" class="row wrap">
                <span class="chip success" :data-testid="tid('key-saved')"><Icon name="key" sm />已保存 {{ saved?.api_key_hint }}</span>
                <button class="btn sm" @click="startReplace">更换</button>
                <button class="btn sm danger" @click="clearKey">清除</button>
              </div>
              <template v-else>
                <input :id="pfx + '-key'" v-model="form.api_key" class="input mono" type="password" autocomplete="off" spellcheck="false" :placeholder="preset?.needs_api_key ? '粘贴 API Key' : '本地/自建服务可留空'" :aria-invalid="!!showErr('api_key')" />
                <span v-if="showErr('api_key')" class="err-text" role="alert">{{ showErr('api_key') }}</span>
                <span v-else-if="form.clear_key" class="muted hint">保存后将清除已保存的密钥。<button class="btn sm" @click="undoClear">撤销</button></span>
                <span v-else-if="saved?.api_key_set && saved.preset_id !== form.preset_id" class="muted hint">已保存的密钥属于其他服务商，换服务商请重新填写。</span>
                <span v-else-if="replacing" class="muted hint">输入新密钥以替换已保存的密钥，留空则保持不变。</span>
              </template>
            </div>
          </div>

          <div class="adv">
            <button class="adv-toggle" type="button" :aria-expanded="advOpen" :aria-controls="pfx + '-adv'" @click="advOpen = !advOpen">
              <Icon :name="advOpen ? 'expand_more' : 'chevron_right'" sm />高级：视觉识别（可选，默认关闭）
              <span v-if="form.allow_vision" class="chip success">已开启</span>
            </button>
            <fieldset v-show="advOpen" :id="pfx + '-adv'" class="col vision" :data-testid="tid('vision')">
              <legend class="sr-only">视觉识别（可选，默认关闭）</legend>
              <label class="row sw"><input v-model="form.allow_vision" type="checkbox" role="switch" :data-testid="tid('allow-vision')" /><span>允许在 OCR 质量差时把页面图片发送给视觉模型</span></label>
              <div class="field"><label :for="pfx + '-vision-model'">视觉模型</label>
                <ModelCombobox :id="pfx + '-vision-model'" v-model="form.vision_model" :live="live" :suggestions="visionSuggestions" placeholder="输入或选择支持图片的模型名" :invalid="!!showErr('vision_model')" />
                <span v-if="showErr('vision_model')" class="err-text" role="alert">{{ showErr('vision_model') }}</span>
                <span v-else class="muted hint">仅当扫描件/图片的 OCR 质量判定为差时才会调用；需选择支持图片输入的模型。</span>
              </div>
              <div class="banner" role="note"><Icon name="warning" /><span>页面图片会发送给所选模型服务商；涉密文件请勿开启。</span></div>
            </fieldset>
          </div>
        </template>

        <div v-if="saveError" class="banner error" role="alert"><Icon name="error" /><span>{{ saveError }}</span></div>
        <div class="row wrap bar">
          <button v-if="form.mode === 'llm'" class="btn outlined has-icon" :disabled="testing" @click="runTest"><Icon name="network_check" sm :class="{ spin: testing }" />{{ testing ? '测试中…' : '测试连接' }}</button>
          <button class="btn filled has-icon" :disabled="!dirty || saving || (touched && !valid)" @click="save"><Icon name="save" sm :class="{ spin: saving }" />{{ saving ? '保存中…' : '保存并生效' }}</button>
          <span v-if="dirty" class="muted hint" role="status">有未保存的更改</span>
          <span v-else-if="!isPersonal || hasSaved" class="muted hint">当前设置已生效</span>
          <span class="grow" />
          <button v-if="isPersonal && hasSaved" class="btn danger has-icon" :disabled="removing" data-testid="personal-llm-remove" @click="confirmRemove = true"><Icon name="delete" sm />删除个人配置，改用全局</button>
        </div>
        <template v-if="form.mode === 'llm'">
          <ul v-if="testResult" class="checks" role="status" :data-testid="tid('test')">
            <li :class="testResult.ok ? 'okc' : 'badc'"><Icon :name="testResult.ok ? 'check_circle' : 'cancel'" sm />连接{{ testResult.ok ? '成功' : '失败' }} · {{ testResult.latency_ms }} ms</li>
            <li v-if="testResult.ok && testResult.tool_calling != null" :class="testResult.tool_calling ? 'okc' : 'warnc'"><Icon :name="testResult.tool_calling ? 'check_circle' : 'warning'" sm />工具调用：{{ testResult.tool_calling ? '支持' : '未检测到——识别效果可能受影响' }}</li>
            <li v-if="testResult.ok && testResult.vision_ok != null" :class="testResult.vision_ok ? 'okc' : 'badc'" :data-testid="tid('vision-ok')"><Icon :name="testResult.vision_ok ? 'check_circle' : 'cancel'" sm />视觉：{{ testResult.vision_ok ? '视觉模型可用' : '视觉模型不可用' }}</li>
            <li v-if="testResult.message" class="muted">{{ testResult.message }}</li>
          </ul>
          <span v-if="testError" class="err-text" role="alert">{{ testError }}</span>
        </template>
        <p class="muted note">密钥只保存在本机服务器（加密），不会显示或写入日志；文档文字会发送给所选模型服务商，涉密文件请使用离线规则或本地模型（如 Ollama）。</p>
      </template>
    </StateView>
    <ConfirmDialog v-if="confirmRemove" title="删除个人大模型配置？" confirm-label="删除并改用全局" danger :busy="removing" @confirm="removePersonal" @close="confirmRemove = false">
      <p>将删除你保存的个人配置与密钥。之后你的任务使用管理员配置的全局大模型（若未配置，则 AI 工作台不可用）。已提交的任务不受影响。</p>
    </ConfirmDialog>
  </section>
</template>

<style scoped>
.llm { grid-column: 1 / -1; }
.tiles { display: grid; grid-template-columns: repeat(auto-fit, minmax(240px, 1fr)); gap: 12px; }
.tile { display: grid; grid-template-columns: auto 1fr; column-gap: 12px; row-gap: 2px; align-items: center; text-align: left; padding: 14px 16px; border: 1px solid var(--md-sys-color-outline); border-radius: var(--shape-md); background: transparent; color: inherit; font: inherit; cursor: pointer; }
.tile .icon { grid-row: span 2; font-size: 28px; }
.tile .tt { font-weight: 600; }
.tile .td { font-size: 12px; grid-column: 2; }
.tile:hover { background: color-mix(in srgb, var(--md-sys-color-primary) 8%, transparent); }
.tile[aria-checked='true'] { background: var(--md-sys-color-secondary-container); color: var(--md-sys-color-on-secondary-container); border-color: var(--md-sys-color-primary); box-shadow: inset 0 0 0 1px var(--md-sys-color-primary); }
.tile[aria-checked='true'] .muted { color: inherit; opacity: .8; }
.chips { display: grid; grid-template-columns: repeat(auto-fit, minmax(128px, 1fr)); gap: 8px; }
.pchip { height: 36px; padding: 0 12px; border: 1px solid var(--md-sys-color-outline); border-radius: var(--shape-full); background: transparent; color: inherit; font: 500 13px var(--font); cursor: pointer; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.pchip:hover { background: color-mix(in srgb, var(--md-sys-color-primary) 8%, transparent); }
.pchip[aria-checked='true'] { background: var(--md-sys-color-secondary-container); color: var(--md-sys-color-on-secondary-container); border-color: var(--md-sys-color-primary); }
.info { margin: 6px 0 0; line-height: 1.6; }
.grid2 { display: grid; grid-template-columns: 1fr 1fr; gap: 16px; align-items: start; }
.grid2 > .field { min-width: 0; }
.grid2 .full { grid-column: 1 / -1; }
@media (max-width: 720px) { .grid2 { grid-template-columns: 1fr; } }
.adv-toggle { all: unset; box-sizing: border-box; display: inline-flex; align-items: center; gap: 6px; cursor: pointer; padding: 6px 8px; margin-left: -8px; border-radius: var(--shape-sm); font-weight: 500; }
.adv-toggle:hover { background: var(--c-surface-3); }
.adv-toggle:focus-visible { outline: 3px solid var(--md-sys-color-primary); }
.vision { border: 1px solid var(--md-sys-color-outline-variant); border-radius: var(--shape-md); padding: 12px 16px; margin: 8px 0 0; min-width: 0; }
.sw { gap: 10px; cursor: pointer; }
.bar { padding-top: 4px; }
.checks { list-style: none; margin: 0; padding: 10px 14px; display: flex; flex-direction: column; gap: 6px; background: var(--c-surface-3); border-radius: var(--shape-md); font-size: 13px; }
.checks li { display: flex; align-items: center; gap: 8px; }
.okc { color: var(--c-on-success-container, inherit); } .okc .icon { color: var(--c-success, #2e7d32); }
.badc .icon { color: var(--md-sys-color-error); } .warnc .icon { color: #b26a00; }
.hint { font-size: 12px; }
.err-text { font-size: 12px; color: var(--md-sys-color-error); }
.note { font-size: 12px; }
</style>
