<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { api } from '../api'
import type { LlmConfig, LlmPreset, LlmTestResult } from '../api'
import { desktop, type KeyHelperEvent } from '../utils/desktop'
import { helperButtonText, helperMode, helperModeText, keyGuideOf, sanitizeKeyLabel } from '../utils/keyHelper'
import { useUi } from '../stores/ui'
import { useAi } from '../stores/ai'
import { llmErrorText, type LlmFns, type LlmScope } from '../utils/llm'
import { classifyError, classifyTestResult, type LlmFailure } from '../utils/llmError'
import {
  badgeText, canAutoReadClipboard, canSave, checkKeyFormat, cleanKey, clipboardOffer, guideFor, guideReducer, initialGuide, keyDisplay, OLLAMA_DEFAULT_HOST,
  ollamaBase, ollamaHost, pickRecommendedModel, readClipboardSafe, shortName, simpleCards, type ClipboardOffer, type GuideEvent,
} from '../utils/llmGuide'
import { loadPending, safeKeyUrl } from '../utils/llmOauth'
import Icon from './Icon.vue'
import LlmFailureBanner from './LlmFailureBanner.vue'
import KeyGuidePanel from './KeyGuidePanel.vue'
import LlmLoginPanel from './LlmLoginPanel.vue'

/**
 * 简单模式：选服务商 → （登录 / 粘贴密钥 / 检测本机）→ 自动测试并选好模型 → 一键保存。
 * 不询问 Base URL 与模型；这些只在“高级设置”里出现。所有对服务商的调用失败都带“重试”。
 */
const props = defineProps<{ scope: LlmScope; presets: LlmPreset[]; saved?: LlmConfig; fns: LlmFns; allowLocal: boolean }>()
const emit = defineEmits<{ saved: [cfg: LlmConfig]; changed: []; advanced: [presetId?: string] }>()

const ui = useUi()
const ai = useAi()
const tid = (n: string) => (props.scope === 'personal' ? `personal-llm-simple-${n}` : `llm-simple-${n}`)

const cards = computed(() => simpleCards(props.presets, { allowLocal: props.allowLocal }))
const pending = loadPending() // 网页版登录授权回来后要续接：直接打开对应服务商那一步
const startId = pending?.scope === props.scope && cards.value.some((c) => c.guide.id === pending.provider)
  ? pending.provider
  : props.saved?.mode === 'llm' && cards.value.some((c) => c.guide.id === props.saved?.preset_id) ? props.saved.preset_id : null
const g = ref(initialGuide(startId))
const dispatch = (e: GuideEvent) => { g.value = guideReducer(g.value, e) }
const preset = computed(() => props.presets.find((p) => p.id === g.value.presetId))
const guide = computed(() => guideFor(g.value.presetId))
const loginPanel = ref<InstanceType<typeof LlmLoginPanel> | null>(null)
const managing = ref(false) // 这个设置卡片打开了“管理密钥”窗口：只有它回答窗口的请求（页面上可能同时有全局和个人两张卡片）
const inApp = typeof desktop()?.openKeyHelper === 'function'
let lastTest: { ok: boolean; message: string; at: number } | null = null
const kind = computed(() => guide.value?.kind)
const pname = computed(() => shortName(preset.value))
const keyUrl = computed(() => safeKeyUrl(preset.value))
const keyGuide = computed(() => keyGuideOf(preset.value))
const mode = helperMode(desktop())

// 表单状态
const key = ref('')
const showKey = ref(false)
const keyLabel = ref('') // 你给密钥起的名字（可选，只用于状态行显示）
const guidePanel = ref(false) // 网页版：设置表单旁边的浮动指引
const helperOpen = ref(false) // 桌面版：向导窗口已打开，等待密钥
const pasteInstead = ref(false) // OpenRouter 也可改为粘贴密钥
const host = ref(ollamaHost(props.saved?.preset_id === 'ollama' ? props.saved.base_url : ''))
const model = ref('')
const live = ref<string[]>([])
const replacing = ref(false)
const testResult = ref<LlmTestResult | null>(null)
const testFailure = ref<LlmFailure | null>(null)
const modelsFailure = ref<LlmFailure | null>(null)
const saveError = ref<string | null>(null)
const busyTest = ref(false)
const busyModels = ref(false)
let seq = 0

const typed = computed(() => cleanKey(key.value))
const check = computed(() => checkKeyFormat(g.value.presetId ?? '', key.value))
const savedKeyHere = computed(() => !!props.saved?.api_key_set && props.saved.preset_id === g.value.presetId && !replacing.value)
const usesKey = computed(() => preset.value?.needs_api_key !== false && kind.value !== 'local')
const showPaste = computed(() => kind.value === 'paste' || (kind.value === 'login' && pasteInstead.value))
const baseUrl = computed(() => (kind.value === 'local' ? ollamaBase(host.value) : preset.value?.base_url ?? ''))
const connectedSaved = computed(() => !!props.saved && props.saved.mode === 'llm' && props.saved.state === 'ok' && props.saved.preset_id === g.value.presetId && savedKeyHere.value)
const verified = computed(() => g.value.check === 'ok')
const working = computed(() => g.value.check === 'working')

function choose(id: string) {
  seq++; key.value = ''; live.value = []; model.value = ''; testResult.value = null; testFailure.value = null; modelsFailure.value = null
  saveError.value = null; replacing.value = false; pasteInstead.value = false; offer.value = null; armed.value = false
  keyLabel.value = ''; guidePanel.value = false; helperOpen.value = false; clipNote.value = ''
  dispatch({ type: 'choose', presetId: id })
  const p = props.presets.find((x) => x.id === id)
  if (id === 'ollama') host.value = ollamaHost(props.saved?.preset_id === 'ollama' ? props.saved.base_url : p?.base_url)
}
function back() { seq++; dispatch({ type: 'back' }) }
function startReplace() { replacing.value = true; testResult.value = null; testFailure.value = null; dispatch({ type: 'keyEdited' }) }

// ---------- 对服务商的三个动作（每个失败都可以“原样重试”） ----------

/** 取模型列表并自动选推荐模型。返回是否成功。 */
async function fetchModelsAction(): Promise<boolean> {
  const my = seq
  busyModels.value = true; modelsFailure.value = null
  try {
    const list = await props.fns.models({ base_url: baseUrl.value, api_key: usesKey.value && typed.value ? typed.value : undefined })
    if (my !== seq) return false
    live.value = list
    model.value = pickRecommendedModel(preset.value, list) || model.value
    return true
  } catch (e) {
    if (my !== seq) return false
    modelsFailure.value = classifyError(e)
    if (!model.value) model.value = preset.value?.default_model ?? ''
    return false
  } finally { if (my === seq) busyModels.value = false }
}

async function testAction(): Promise<boolean> {
  const my = seq
  dispatch({ type: 'checkStart' })
  busyTest.value = true; testFailure.value = null; testResult.value = null
  try {
    const r = await props.fns.test({ base_url: baseUrl.value, model: model.value, api_key: usesKey.value && typed.value ? typed.value : undefined })
    if (my !== seq) return false
    testResult.value = r
    const f = classifyTestResult(r)
    lastTest = { ok: !f, message: f ? f.message : '连接成功', at: Date.now() }
    if (f) {
      testFailure.value = f
      if (modelsFailure.value?.kind === f.kind) {
        // 同一个原因只提示一次；限速时保留带 Retry-After 倒计时的那条
        if (modelsFailure.value.retryAfter && !f.retryAfter) testFailure.value = null
        else modelsFailure.value = null
      }
      dispatch({ type: 'checkFail' })
      return false
    }
    dispatch({ type: 'checkOk' })
    return true
  } catch (e) {
    if (my !== seq) return false
    testFailure.value = classifyError(e)
    dispatch({ type: 'checkFail' })
    return false
  } finally { if (my === seq) busyTest.value = false }
}

/** 自动流程：取模型列表 → 选模型 → 测试连接。 */
async function connect() {
  if (working.value) return
  const my = seq
  dispatch({ type: 'checkStart' })
  const ok = await fetchModelsAction()
  if (my !== seq) return
  if (!ok && (!model.value || kind.value === 'local')) { dispatch({ type: 'checkFail' }); return }
  if (!model.value) {
    modelsFailure.value = { kind: 'unknown', retryable: false, message: kind.value === 'local' ? '没有找到可用的模型：请先在 Ollama 里下载一个模型（例如 ollama pull qwen3.8:27b）。' : '服务商没有返回可用的模型，请到“高级设置”手动填写模型名称。' }
    dispatch({ type: 'checkFail' }); return
  }
  await testAction()
}

function onLocalModel() { seq++; testResult.value = null; testFailure.value = null; dispatch({ type: 'keyEdited' }); void testAction() }
function retryTest() { void testAction() }
async function retryModels() {
  const ok = await fetchModelsAction()
  // 列表失败曾经挡住了测试（没有默认模型）：成功后接着测试
  if (ok && !verified.value && !testFailure.value && model.value) await testAction()
  else if (!ok && g.value.check === 'working') dispatch({ type: 'checkFail' })
}
/** 已保存的密钥：直接用服务端保存的密钥重新测试 */
async function retestSaved() {
  model.value = props.saved?.model ?? model.value
  await testAction()
}

async function save() {
  if (!canSave(g.value)) return
  dispatch({ type: 'saveStart' }); saveError.value = null
  try {
    const keep = props.saved?.preset_id === g.value.presetId ? props.saved : undefined
    const cfg = await props.fns.save({
      mode: 'llm', preset_id: g.value.presetId ?? '', base_url: baseUrl.value, model: model.value,
      ...(usesKey.value && typed.value ? { api_key: typed.value } : {}),
      ...(usesKey.value && typed.value && sanitizeKeyLabel(keyLabel.value) ? { key_label: sanitizeKeyLabel(keyLabel.value) } : {}),
      allow_vision: !!keep?.allow_vision, vision_model: keep?.vision_model ?? '',
    })
    dispatch({ type: 'saveDone' })
    key.value = ''; replacing.value = false
    ui.toast(props.scope === 'personal' ? '已保存并启用：你的任务将优先使用它' : '已保存并启用，无需重启', 'success')
    emit('saved', cfg)
    void ai.refresh()
  } catch (e) { dispatch({ type: 'saveFail' }); saveError.value = llmErrorText(e, props.scope) }
}

// ---------- 输入：粘贴 / 键入 ----------
const autoNext = ref(false)
let debounce: ReturnType<typeof setTimeout> | undefined
function onPaste() { autoNext.value = true; setTimeout(() => { autoNext.value = false }, 0) }
watch(key, (v, old) => {
  if (v === old) return
  clearTimeout(debounce)
  seq++; testResult.value = null; testFailure.value = null; modelsFailure.value = null; live.value = []; saveError.value = null
  dispatch({ type: 'keyEdited' })
  const lvl = checkKeyFormat(g.value.presetId ?? '', v).level
  if (autoNext.value) { autoNext.value = false; if (lvl === 'ok' || lvl === 'warn') void connect(); return }
  if (lvl === 'ok') debounce = setTimeout(() => { void connect() }, 800)
})
function fill(text: string) { autoNext.value = true; key.value = cleanKey(text); offer.value = null }

// ---------- 打开密钥页面 + 剪贴板提示 ----------
const armed = ref(false)
const offer = ref<ClipboardOffer | null>(null)
const clipNote = ref('')
let dismissed: string | null = null
let autoRead = false
const opened = ref(false)

async function openKeyPage() {
  const url = keyUrl.value
  if (!url) return
  const bridge = desktop()
  let ok = true
  if (bridge?.openExternal) ok = await bridge.openExternal(url)
  else window.open(url, '_blank', 'noopener,noreferrer')
  opened.value = ok
  clipNote.value = ok ? '' : '无法打开系统浏览器，请手动访问该服务商的 API Key 页面。'
  armed.value = ok
  autoRead = await canAutoReadClipboard()
}
async function onFocusBack() {
  if (!armed.value || !autoRead || key.value || !showPaste.value) return
  const text = await readClipboardSafe()
  offer.value = clipboardOffer(text, g.value.presetId ?? '', key.value, dismissed)
}
function acceptOffer() { if (offer.value) fill(offer.value.key) }
function dismissOffer() { if (offer.value) dismissed = offer.value.key; offer.value = null }
async function pasteFromClipboard() {
  clipNote.value = ''
  const text = await readClipboardSafe()
  if (!text) { clipNote.value = '没能读取剪贴板（浏览器可能不允许）。请点一下输入框，按 Ctrl+V 粘贴。'; return }
  if (!cleanKey(text)) { clipNote.value = '剪贴板是空的，请先在服务商页面点“复制”。'; return }
  fill(text)
}
function onVisible() { if (document.visibilityState === 'visible') void onFocusBack() }
// ---------- 向导（桌面：向导窗口；网页：新标签页 + 浮动指引） ----------
async function openHelper() {
  const bridge = desktop()
  const kg = keyGuide.value
  clipNote.value = ''
  if (mode === 'desktop' && bridge?.openKeyHelper) {
    const r = await bridge.openKeyHelper(g.value.presetId ?? '', { mode: 'create' })
    helperOpen.value = !!r?.ok
    if (!r?.ok) clipNote.value = r?.message || '没能打开向导窗口，请用下面的“打开密钥页面”。'
    return
  }
  guidePanel.value = true
  if (kg && !keyLabel.value) keyLabel.value = kg.suggested_name
  await openKeyPage()
}
/** 桌面版：在应用自带的窗口里管理已保存的密钥（服务商、名称、尾号、上次测试；重新测试 / 更换 / 换服务商 / 删除）。 */
async function openManageWindow() {
  const bridge = desktop()
  if (!bridge?.openKeyHelper || !props.saved?.api_key_set) return
  managing.value = true
  const r = await bridge.openKeyHelper(props.saved.preset_id, { mode: 'manage' })
  if (!r?.ok) { managing.value = false; clipNote.value = r?.message || '没能打开管理窗口。' }
}

/** 管理窗口的请求：它不持有登录会话，所以由这里用正常的接口完成并回复（回复里没有密钥）。 */
async function onRequest(id: number, op: 'status' | 'test' | 'remove') {
  const bridge = desktop()
  if (!bridge?.keyHelperReply) return
  const reply = (d: unknown) => { void bridge.keyHelperReply?.(id, d) }
  const s = props.saved
  if (op === 'status') {
    const ps = props.presets.find((p) => p.id === s?.preset_id)
    return reply({ provider: shortName(ps) || s?.preset_id || '', presetId: s?.preset_id ?? '', keyLabel: s?.key_label ?? '', hint: s?.api_key_hint ?? '', model: s?.model ?? '', source: s?.key_source === 'oauth' ? 'oauth' : 'manual', configured: !!s?.api_key_set && s?.mode === 'llm', state: s?.state ?? '', test: lastTest })
  }
  if (!s) return reply({ ok: false, message: '还没有保存任何设置。' })
  if (op === 'test') {
    try {
      const f = classifyTestResult(await props.fns.test({ base_url: s.base_url, model: s.model }))
      lastTest = { ok: !f, message: f ? f.message : '连接成功', at: Date.now() }
    } catch (e) { lastTest = { ok: false, message: classifyError(e).message, at: Date.now() } }
    return reply({ ok: lastTest.ok, message: lastTest.message, test: lastTest })
  }
  try {
    if (props.scope === 'personal' && props.fns.remove) await props.fns.remove()
    else if (s.key_source === 'oauth') await api.disconnectLlmOauth(props.scope)
    else await props.fns.save({ mode: 'heuristic', clear_api_key: true })
    lastTest = null
    emit('changed'); void ai.refresh()
    reply({ ok: true, message: '已删除密钥。' })
  } catch (e) { reply({ ok: false, message: llmErrorText(e, props.scope) }) }
}

function onHelperEvent(ev: KeyHelperEvent) {
  if (ev.type === 'closed') { helperOpen.value = false; managing.value = false; return }
  if (!helperOpen.value && !managing.value) return // 另一张卡片（全局/个人）打开的窗口，不是这里的
  if (ev.type === 'request') { if (managing.value) void onRequest(ev.id, ev.op); return }
  if (ev.type === 'opened') { helperOpen.value = true; return }
  if (ev.type === 'switch-provider') { managing.value = false; back(); return }
  if (ev.type === 'replace') {
    if (!cards.value.some((c) => c.guide.id === ev.provider)) return
    if (g.value.presetId !== ev.provider) choose(ev.provider)
    void nextTick(() => { void loginPanel.value?.start() })
    return
  }
  if (ev.type !== 'key' || typeof ev.key !== 'string' || !ev.key) return
  if (connectedSaved.value) startReplace()
  if (g.value.presetId !== ev.provider) {
    if (!cards.value.some((c) => c.guide.id === ev.provider)) return
    choose(ev.provider)
  }
  helperOpen.value = false
  keyLabel.value = sanitizeKeyLabel(ev.label)
  clipNote.value = ev.clipboardCleared ? '已从向导窗口收到密钥，并清空了剪贴板里的这把密钥。' : '已从向导窗口收到密钥。'
  fill(ev.key)
}
function focusKey() { document.getElementById(tid('key'))?.focus() }
let offHelper: (() => void) | undefined
onMounted(() => {
  window.addEventListener('focus', onFocusBack); document.addEventListener('visibilitychange', onVisible)
  offHelper = desktop()?.onKeyHelper?.(onHelperEvent)
})
onBeforeUnmount(() => { offHelper?.(); seq++;
 clearTimeout(debounce); window.removeEventListener('focus', onFocusBack); document.removeEventListener('visibilitychange', onVisible) })

const steps = computed(() => [
  `点下面的按钮，打开 ${pname.value} 的密钥页面并登录（没有账号就先注册）。`,
  '在页面里点“创建 API Key”（或类似的按钮），然后点“复制”。',
  '回到这里，把密钥粘贴到下面的框里，其余的交给我们。',
])
const resultOk = computed(() => verified.value && testResult.value?.ok)
const toolWarn = computed(() => resultOk.value && testResult.value?.tool_calling === false)
</script>

<template>
  <div class="simple col" :data-testid="tid('root')">
    <!-- 第 1 步 -->
    <section class="col sec" :aria-labelledby="tid('s1')">
      <h3 :id="tid('s1')" class="step-title"><span class="stepno" aria-hidden="true">1</span>选择一个服务商</h3>
      <div class="cards" role="radiogroup" :aria-labelledby="tid('s1')">
        <button v-for="c in cards" :key="c.guide.id" class="pcard" type="button" role="radio" :aria-checked="g.presetId === c.guide.id" :data-preset="c.guide.id" :data-testid="tid('card-' + c.guide.id)" @click="choose(c.guide.id)">
          <span class="ptop">
            <span class="pname">{{ shortName(c.preset) }}</span>
            <span v-if="c.guide.recommended" class="tag rec"><Icon name="star" sm />推荐</span>
          </span>
          <span class="ptag muted">{{ c.guide.tagline }}</span>
          <span class="pbadge" :class="c.guide.kind"><Icon :name="c.guide.kind === 'login' ? 'login' : c.guide.kind === 'paste' ? 'key' : 'computer'" sm />{{ badgeText(c.guide.kind) }}</span>
        </button>
        <button class="pcard other" type="button" :data-testid="tid('card-other')" @click="emit('advanced', 'custom')">
          <span class="ptop"><span class="pname">其他服务</span></span>
          <span class="ptag muted">公司内部网关、Kimi、智谱、Gemini 等，需要手动填写地址和模型。</span>
          <span class="pbadge"><Icon name="tune" sm />高级设置</span>
        </button>
      </div>
    </section>

    <!-- 第 2 步 -->
    <section v-if="preset && guide" class="col sec" :aria-labelledby="tid('s2')" :data-testid="tid('step2')">
      <h3 :id="tid('s2')" class="step-title"><span class="stepno" aria-hidden="true">2</span>{{ kind === 'login' && !pasteInstead ? `登录 ${pname}，自动设置` : kind === 'local' ? '连接你电脑上的模型' : `获取并粘贴 ${pname} 的密钥` }}</h3>

      <div class="s2layout" :class="{ two: guidePanel && !!keyGuide }"><div class="formcol col">
      <!-- 已保存的密钥（非登录授权） -->
      <div v-if="connectedSaved && !(kind === 'login' && saved?.key_source === 'oauth')" class="card outlined col savedbox" :data-testid="tid('saved')">
        <div class="row wrap">
          <Icon name="check_circle" class="okic" />
          <span class="grow"><b>已连接，正在使用。</b>密钥 <span class="mono">{{ keyDisplay(saved) }}</span> · 模型 <span class="mono">{{ saved?.model }}</span></span>
          <button class="btn outlined sm has-icon" type="button" :disabled="busyTest" :data-testid="tid('retest')" @click="retestSaved"><Icon name="network_check" sm :class="{ spin: busyTest }" />{{ busyTest ? '测试中…' : '重新测试' }}</button>
          <button v-if="inApp" class="btn sm outlined" type="button" :data-testid="tid('manage')" @click="openManageWindow">管理密钥</button>
          <button class="btn sm" type="button" :data-testid="tid('replace')" @click="startReplace">更换密钥</button>
        </div>
        <span v-if="resultOk" class="okline" role="status"><Icon name="check_circle" sm />连接成功，可以使用了</span>
      </div>

      <!-- 一键登录 -->
      <template v-else-if="kind === 'login' && !pasteInstead">
        <p class="muted lead">点击后会在 {{ pname }} 官方页面登录并点“授权”，本应用不会接触你的密码。授权完成后，会自动创建专用密钥、设置好并测试连接。</p>
        <LlmLoginPanel ref="loginPanel" :scope="scope" :preset="preset" :saved="saved" variant="simple" :test-fn="fns.test" @connected="emit('changed')" @disconnected="emit('changed')" @manage="openManageWindow" />
        <button v-if="saved?.key_source !== 'oauth' || saved?.preset_id !== preset.id" class="btn sm link" type="button" :data-testid="tid('paste-instead')" @click="pasteInstead = true">不想登录？改为手动粘贴密钥</button>
      </template>

      <!-- 本机模型 -->
      <template v-else-if="kind === 'local'">
        <p class="muted lead">请先在电脑上安装并运行 Ollama，下载好模型。然后点“检测”，我们会自动找到你电脑上已有的模型。文档内容不会离开这台电脑。</p>
        <div class="field"><label :for="tid('host')">Ollama 地址（一般不用改）</label>
          <div class="row wrap">
            <input :id="tid('host')" v-model="host" class="input mono grow" type="text" spellcheck="false" autocomplete="off" :placeholder="OLLAMA_DEFAULT_HOST" />
            <button class="btn tonal has-icon" type="button" :disabled="busyModels || busyTest" :data-testid="tid('detect')" @click="connect"><Icon name="search" sm :class="{ spin: busyModels }" />{{ busyModels ? '检测中…' : '检测' }}</button>
          </div>
        </div>
        <div v-if="live.length" class="field"><label :for="tid('local-model')">选择模型</label>
          <select :id="tid('local-model')" v-model="model" class="input" :data-testid="tid('local-model')" @change="onLocalModel">
            <option v-for="m in live" :key="m" :value="m">{{ m }}</option>
          </select>
        </div>
      </template>

      <!-- 粘贴密钥 -->
      <template v-else-if="showPaste">
        <div v-if="keyGuide" class="helperbox card outlined col" :data-testid="tid('helper')">
          <p class="muted lead">{{ pname }} 没有“用账号一键授权”的接口，密钥需要在它的网站上创建并复制一次。我们一步步带你做，你只需要登录和点按钮。</p>
          <div class="row wrap">
            <button class="btn filled has-icon" type="button" :data-testid="tid('open-helper')" @click="openHelper"><Icon :name="mode === 'desktop' ? 'computer' : 'open_in_new'" sm />{{ helperButtonText(mode) }}</button>
            <button class="btn outlined" type="button" :data-testid="tid('have-key')" @click="focusKey">我已经有 Key：直接粘贴</button>
          </div>
          <p class="muted hint" :data-testid="tid('helper-mode')">{{ helperModeText(mode) }}</p>
          <div v-if="helperOpen" class="banner info" role="status" :data-testid="tid('helper-open')"><Icon name="info" /><span class="grow">向导窗口已打开：在那里登录、创建并复制密钥，复制后会自动识别，不用回来粘贴。</span><button class="btn sm" type="button" @click="openHelper">找不到窗口？重新打开</button></div>
        </div>
        <ol v-else class="mini" :data-testid="tid('guide')">
          <li v-for="(t, i) in steps" :key="i"><span class="n" aria-hidden="true">{{ i + 1 }}</span><span>{{ t }}</span></li>
        </ol>
        <div v-if="!keyGuide" class="row wrap">
          <button v-if="keyUrl" class="btn tonal has-icon" type="button" :data-testid="tid('open-key-page')" @click="openKeyPage"><Icon name="open_in_new" sm />打开 {{ pname }} 密钥页面</button>
          <span class="muted hint">{{ pname }} 没有“用账号一键授权”的接口，所以密钥需要你自己复制一次。</span>
        </div>

        <div v-if="offer" class="banner info offer" role="status" :data-testid="tid('clip-offer')">
          <Icon name="content_paste" />
          <span class="grow">检测到剪贴板里有一把密钥（<span class="mono">{{ offer.masked }}</span>），要粘贴它吗？</span>
          <button class="btn filled sm" type="button" :data-testid="tid('clip-accept')" @click="acceptOffer">粘贴这个密钥</button>
          <button class="btn sm" type="button" :data-testid="tid('clip-dismiss')" @click="dismissOffer">不用了</button>
        </div>

        <div class="field"><label :for="tid('key')">{{ keyGuide ? '已经有 Key？粘贴到这里' : '粘贴 API Key（密钥）' }}</label>
          <div class="row keyrow">
            <input :id="tid('key')" v-model="key" class="input mono grow bigbox" :type="showKey ? 'text' : 'password'" autocomplete="off" spellcheck="false" :placeholder="guide.keyExample ?? '粘贴 API Key'" :aria-invalid="check.level === 'bad'" :aria-describedby="tid('keymsg')" :data-testid="tid('key')" @paste="onPaste" />
            <button class="btn icon-only" type="button" :aria-label="showKey ? '隐藏密钥' : '显示密钥'" :aria-pressed="showKey" @click="showKey = !showKey"><Icon :name="showKey ? 'visibility_off' : 'visibility'" /></button>
            <button class="btn outlined has-icon" type="button" :data-testid="tid('clip-btn')" @click="pasteFromClipboard"><Icon name="content_paste" sm />从剪贴板粘贴</button>
          </div>
          <span :id="tid('keymsg')" class="keymsg" :class="check.level" role="status" :data-testid="tid('keymsg')">
            <template v-if="check.level === 'bad' || check.level === 'warn'">{{ check.message }}</template>
            <template v-else-if="check.level === 'ok' && g.check === 'idle'">格式正确，正在自动连接…</template>
          </span>
          <span v-if="clipNote" class="muted hint" role="status" :data-testid="tid('clip-note')">{{ clipNote }}</span>
        </div>
        <div class="field"><label :for="tid('label')">密钥名称（可选）</label>
          <input :id="tid('label')" v-model="keyLabel" class="input" type="text" maxlength="80" autocomplete="off" spellcheck="false" placeholder="你在服务商那边给它起的名字" :data-testid="tid('label')" />
          <span class="muted hint">只用于在这里显示“哪一把密钥”，不会发给服务商。</span>
        </div>
      </template>

      <!-- 自动检测的结果 -->
      <div v-if="working" class="working" role="status" :data-testid="tid('working')"><Icon name="hourglass_top" sm class="spin" />{{ busyModels ? '正在获取可用模型…' : '正在测试连接…' }}</div>
      <LlmFailureBanner v-if="testFailure" :failure="testFailure" :busy="busyTest" :testid="tid('test-failure')" what="测试连接没有通过。" @retry="retryTest" />
      <LlmFailureBanner v-if="modelsFailure" :failure="modelsFailure" :busy="busyModels" :testid="tid('models-failure')" what="获取模型列表没有成功。" @retry="retryModels" />

      <div v-if="resultOk && !connectedSaved" class="col resultbox" :data-testid="tid('result-ok')">
        <span class="okline big" role="status"><Icon name="check_circle" />连接成功，可以使用了</span>
        <span class="muted hint">已自动选好模型：<span class="mono">{{ model }}</span>（想换可在“高级设置”里改）</span>
        <div v-if="toolWarn" class="banner" role="note"><Icon name="warning" /><span>这个模型好像不支持“工具调用”，识别效果可能变差。建议到“高级设置”换一个模型。</span></div>
      </div>
      <div v-if="saveError" class="banner error" role="alert"><Icon name="error" /><span>{{ saveError }}</span></div>

      <div v-if="(showPaste || kind === 'local') && !connectedSaved" class="row wrap bar">
        <button class="btn filled has-icon" type="button" :disabled="!canSave(g)" :data-testid="tid('save')" @click="save"><Icon name="save" sm :class="{ spin: g.saving }" />{{ g.saving ? '保存中…' : '保存并启用' }}</button>
        <button class="btn" type="button" @click="back">换一个服务商</button>
        <span v-if="!verified" class="muted hint">{{ kind === 'local' ? '先点“检测”，成功后才能保存。' : '粘贴密钥后会自动测试，成功后才能保存。' }}</span>
      </div>
      <div v-else-if="connectedSaved" class="row wrap bar">
        <button class="btn" type="button" @click="back">换一个服务商</button>
      </div>
      </div>
      <KeyGuidePanel v-if="guidePanel && keyGuide && kind !== 'local'" :provider="pname" :guide="keyGuide" :mode="mode" :detected="!!offer" :testid="tid('guide-panel')" @reopen="openKeyPage" @close="guidePanel = false" />
      </div>
    </section>

    <p v-else class="muted hint" :data-testid="tid('pick-hint')">选好服务商后，下面会出现下一步。</p>
  </div>
</template>

<style scoped>
.simple { gap: 20px; }
.sec { gap: 12px; }
.step-title { margin: 0; display: flex; align-items: center; gap: 10px; font: var(--md-sys-typescale-title-medium); }
.stepno { display: inline-grid; place-items: center; width: 24px; height: 24px; border-radius: 50%; background: var(--md-sys-color-primary); color: var(--md-sys-color-on-primary); font: var(--md-sys-typescale-label-medium); flex: none; }
.cards { display: grid; grid-template-columns: repeat(auto-fill, minmax(220px, 1fr)); gap: 12px; }
.pcard { display: flex; flex-direction: column; align-items: flex-start; gap: 6px; text-align: left; padding: 14px 16px; border: 1px solid var(--md-sys-color-outline-variant); border-radius: var(--shape-md); background: var(--md-sys-color-surface-container-low); color: inherit; font: inherit; cursor: pointer; min-width: 0; }
.pcard:hover { background: color-mix(in srgb, var(--md-sys-color-primary) 8%, var(--md-sys-color-surface-container-low)); }
.pcard:focus-visible { outline: 3px solid var(--md-sys-color-secondary); outline-offset: 2px; }
.pcard[aria-checked='true'] { background: var(--md-sys-color-secondary-container); color: var(--md-sys-color-on-secondary-container); border-color: var(--md-sys-color-primary); box-shadow: inset 0 0 0 1px var(--md-sys-color-primary); }
.pcard[aria-checked='true'] .muted { color: inherit; opacity: .85; }
.pcard.other { border-style: dashed; }
.ptop { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; width: 100%; }
.pname { font: var(--md-sys-typescale-title-medium); }
.ptag { font-size: 13px; line-height: 1.5; flex: 1; }
.tag { display: inline-flex; align-items: center; gap: 2px; height: 22px; padding: 0 8px; border-radius: var(--shape-sm); font: var(--md-sys-typescale-label-small); background: var(--md-sys-color-tertiary-container); color: var(--md-sys-color-on-tertiary-container); }
.pbadge { display: inline-flex; align-items: center; gap: 4px; height: 24px; padding: 0 10px; border-radius: var(--shape-full); font: var(--md-sys-typescale-label-small); background: var(--md-sys-color-surface-container-highest); color: var(--md-sys-color-on-surface-variant); }
.pbadge.login { background: var(--c-success-container); color: var(--c-on-success-container); }
.lead { margin: 0; line-height: 1.6; }
.s2layout.two { display: grid; grid-template-columns: minmax(0, 1fr) 340px; gap: 16px; align-items: start; }
@media (max-width: 960px) { .s2layout.two { grid-template-columns: minmax(0, 1fr); } }
.formcol { gap: 12px; min-width: 0; }
.helperbox { gap: 10px; padding: 14px 16px; }
.mini { list-style: none; margin: 0; padding: 12px 16px; display: flex; flex-direction: column; gap: 8px; background: var(--md-sys-color-surface-container); border-radius: var(--shape-md); }
.mini li { display: flex; gap: 10px; align-items: flex-start; line-height: 1.5; }
.mini .n { display: inline-grid; place-items: center; width: 20px; height: 20px; border-radius: 50%; background: var(--md-sys-color-secondary-container); color: var(--md-sys-color-on-secondary-container); font: var(--md-sys-typescale-label-small); flex: none; margin-top: 1px; }
.keyrow { align-items: center; gap: 8px; flex-wrap: wrap; }
.keyrow .grow { flex: 1 1 260px; }
.bigbox { height: 56px; font-size: 14px; }
.keymsg { font-size: 12px; min-height: 18px; color: var(--md-sys-color-on-surface-variant); }
.keymsg.bad { color: var(--md-sys-color-error); }
.keymsg.warn { color: var(--c-warn); }
.okline { display: inline-flex; gap: 6px; align-items: center; color: var(--c-on-success-container, inherit); }
.okline .icon, .okic { color: var(--c-success); }
.okline.big { font: var(--md-sys-typescale-title-medium); }
.working { display: flex; gap: 8px; align-items: center; color: var(--md-sys-color-on-surface-variant); }
.resultbox { gap: 6px; padding: 12px 16px; border-radius: var(--shape-md); background: var(--c-success-container); color: var(--c-on-success-container); }
.resultbox .muted { color: inherit; opacity: .85; }
.savedbox { gap: 8px; }
.offer .grow { min-width: 160px; }
.link { align-self: flex-start; }
.hint { font-size: 12px; }
.bar { padding-top: 4px; }
</style>
