<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { dbAdminApi, DB_PROVIDERS, defaultPort, type DbInitPreview, type DbReport, type DbStatus, type DbTestResult } from '../../api/dbAdmin'
import { buildDbRequest, emptyForm, validateDbForm } from '../../utils/dbForm'
import { errMsg } from '../../utils/errorInfo'
import { useUi } from '../../stores/ui'
import Icon from '../Icon.vue'

const ui = useUi()
const status = ref<DbStatus | null>(null)
const loadError = ref('')
const form = ref(emptyForm())
const busy = ref('')
const formError = ref('')
const testRes = ref<DbTestResult | null>(null)
const report = ref<DbReport | null>(null)
const initPreview = ref<DbInitPreview | null>(null)
const tlsOpen = ref(false)

const readOnly = computed(() => !!status.value?.read_only)
const hasSaved = computed(() => !!(status.value?.pending?.has_password))
const needsConfirm = computed(() => !!status.value?.pending?.current_has_data || !status.value?.active?.embedded)
const providerName = (id: string) => DB_PROVIDERS.find((p) => p.id === id)?.name ?? id
const chipOf = (s: string) => (s === 'pass' || s === 'ok' ? 'success' : s === 'fail' ? 'error' : 'warn')

async function load() {
  loadError.value = ''
  try {
    status.value = await dbAdminApi.status()
    const p = status.value.pending
    if (p && !form.value.host) form.value = { ...form.value, provider: p.provider, host: p.host, port: p.port, name: p.name, user: p.user, schema: p.schema ?? '', tls_mode: p.tls_mode, server_name: p.server_name ?? '' }
  } catch (e) { loadError.value = errMsg(e) }
}
onMounted(load)
watch(() => form.value.provider, (n, o) => { if (form.value.port === defaultPort(o)) form.value.port = defaultPort(n) })

async function run<T>(key: string, fn: () => Promise<T>): Promise<T | undefined> {
  formError.value = validateDbForm(form.value, hasSaved.value)
  if (formError.value) return undefined
  busy.value = key
  try { return await fn() } catch (e) { formError.value = errMsg(e); return undefined } finally { busy.value = '' }
}
const req = () => buildDbRequest(form.value, hasSaved.value)

async function doTest() { testRes.value = null; const r = await run('test', () => dbAdminApi.test(req())); if (r) testRes.value = r }
async function doValidate() { report.value = null; const r = await run('validate', () => dbAdminApi.validate(req())); if (r) report.value = r }
async function doSave() {
  const r = await run('save', () => dbAdminApi.save({ ...req(), confirm_switch: form.value.confirm_switch } as never))
  if (r) { status.value = r; form.value.password = ''; ui.toast('已保存为待生效配置，重启服务并经批准后才会切换', 'success') }
}
async function doDiscard() { busy.value = 'discard'; try { await dbAdminApi.discard(); await load() } catch (e) { formError.value = errMsg(e) } finally { busy.value = '' } }
async function planInit() { busy.value = 'init'; try { initPreview.value = await dbAdminApi.initialize({ target: 'pending', dry_run: true }) } catch (e) { formError.value = errMsg(e) } finally { busy.value = '' } }
async function runInit() {
  if (!initPreview.value) return
  busy.value = 'init'
  try { initPreview.value = await dbAdminApi.initialize({ target: 'pending', confirmation_token: initPreview.value.confirmation_token }); ui.toast('库结构已初始化', 'success'); initPreview.value = null; await load() }
  catch (e) { formError.value = errMsg(e) } finally { busy.value = '' }
}
</script>

<template>
  <section class="card col" aria-label="数据库" data-testid="db-settings">
    <div class="row wrap">
      <h2 class="t-title-m grow">数据库</h2>
      <button class="btn icon-only sm" aria-label="刷新" @click="load"><Icon name="refresh" sm /></button>
    </div>
    <p v-if="loadError" class="banner error" role="alert"><Icon name="error" />{{ loadError }}</p>

    <div v-if="status?.active" class="col" data-testid="db-status">
      <div class="row wrap">
        <span class="chip" :class="status.active.connected ? 'success' : 'error'"><Icon :name="status.active.connected ? 'check_circle' : 'error'" sm />{{ status.active.connected ? '已连接' : '无法连接' }}</span>
        <span class="chip"><Icon name="database" sm />{{ providerName(status.active.provider) }} {{ status.active.server_version }}</span>
        <span class="chip" :class="status.active.migration?.dirty ? 'error' : status.active.migration?.state === 'current' ? 'success' : 'warn'">迁移 {{ status.active.migration?.version }}/{{ status.active.migration?.latest }}{{ status.active.migration?.dirty ? '（中断）' : '' }}</span>
        <span class="chip"><Icon name="lock" sm />TLS：{{ status.active.tls_mode }}</span>
      </div>
      <p class="muted">{{ status.active.host }}:{{ status.active.port }} / {{ status.active.name }}<template v-if="status.active.embedded">（内嵌 PostgreSQL，随服务启动）</template></p>
    </div>

    <p v-if="status?.read_only" class="banner info" data-testid="db-readonly"><Icon name="info" />数据库由部署配置（环境变量）指定，此处只读：{{ status.read_only_reason }}</p>
    <p v-for="w in status?.warnings ?? []" :key="w" class="banner"><Icon name="warning" />{{ w }}</p>

    <div v-if="status?.pending" class="banner info" data-testid="db-pending">
      <Icon name="info" />
      <div class="col grow">
        <b>待生效配置：{{ providerName(status.pending.provider) }} {{ status.pending.host }}:{{ status.pending.port }}/{{ status.pending.name }}</b>
        <span>{{ status.pending.will_apply_on_restart ? '将在下次重启时生效。' : '尚不会生效：' + (status.pending.blockers ?? []).join('；') }}</span>
        <div class="row wrap">
          <button v-if="status.pending.needs_initialize" class="btn tonal sm" :disabled="!!busy" @click="planInit">预览并初始化库结构</button>
          <button class="btn text sm" :disabled="!!busy || readOnly" @click="doDiscard">放弃</button>
        </div>
      </div>
    </div>

    <div v-if="initPreview" class="card flat col" data-testid="db-init-preview">
      <b>将执行 {{ initPreview.statement_count }} 条 DDL（{{ initPreview.transactional_ddl ? '事务内，失败自动回滚' : '分步执行，失败可续跑' }}）</b>
      <p v-if="initPreview.destructive" class="banner error"><Icon name="warning" />包含破坏性变更，需要管理员显式批准。</p>
      <div class="row"><button class="btn filled" :disabled="!!busy" @click="runInit">批准并执行</button><button class="btn text" @click="initPreview = null">取消</button></div>
    </div>

    <template v-if="!readOnly">
      <p class="banner" data-testid="db-switch-warning"><Icon name="warning" />切换数据库不会自动搬迁已有数据：已有数据的安装不会被指向空库；新配置只在重启时、通过校验并经批准后生效，原库数据不会被删除。请先备份。</p>
      <div class="grid2">
        <div class="field"><label for="db-provider">数据库类型</label>
          <select id="db-provider" v-model="form.provider" class="input"><option v-for="p in DB_PROVIDERS" :key="p.id" :value="p.id">{{ p.name }}（{{ p.minVersion }}）</option></select></div>
        <div class="field"><label for="db-host">主机</label><input id="db-host" v-model="form.host" class="input mono" autocomplete="off" spellcheck="false" placeholder="db.example.com" /></div>
        <div class="field"><label for="db-port">端口</label><input id="db-port" v-model.number="form.port" class="input mono" type="number" min="1" max="65535" /></div>
        <div class="field"><label for="db-name">数据库名（专用）</label><input id="db-name" v-model="form.name" class="input mono" autocomplete="off" spellcheck="false" /></div>
        <div class="field"><label for="db-user">用户名</label><input id="db-user" v-model="form.user" class="input mono" autocomplete="off" spellcheck="false" /></div>
        <div class="field"><label for="db-pass">密码</label><input id="db-pass" v-model="form.password" class="input mono" type="password" autocomplete="new-password" :placeholder="hasSaved ? '已保存，留空保持不变' : ''" />
          <span class="muted hint">只写字段：保存后加密存放，不会再显示。</span></div>
        <div v-if="form.provider === 'postgres'" class="field"><label for="db-schema">Schema（可选）</label><input id="db-schema" v-model="form.schema" class="input mono" autocomplete="off" /></div>
      </div>
      <button class="btn text sm" :aria-expanded="tlsOpen" @click="tlsOpen = !tlsOpen"><Icon name="lock" sm />TLS 设置</button>
      <div v-if="tlsOpen" class="grid2" data-testid="db-tls">
        <div class="field"><label for="db-tls">TLS 模式</label>
          <select id="db-tls" v-model="form.tls_mode" class="input"><option value="disable">disable（不加密）</option><option value="require">require（加密，不校验证书）</option><option value="verify-ca">verify-ca（校验 CA）</option><option value="verify-full">verify-full（校验 CA 与主机名）</option></select></div>
        <div class="field"><label for="db-sn">服务器名称（可选）</label><input id="db-sn" v-model="form.server_name" class="input mono" autocomplete="off" /></div>
        <div class="field full"><label for="db-ca">CA 证书（PEM，可选）</label><textarea id="db-ca" v-model="form.ca_pem" class="input mono" rows="4" spellcheck="false" placeholder="-----BEGIN CERTIFICATE-----" /></div>
      </div>
      <label v-if="needsConfirm" class="switch"><input v-model="form.confirm_switch" type="checkbox" role="switch" />我已备份，并确认把本安装切换到上述数据库</label>
      <p v-if="formError" class="banner error" role="alert" data-testid="db-error"><Icon name="error" />{{ formError }}</p>
      <div class="row wrap">
        <button class="btn outlined" :disabled="!!busy" @click="doTest"><Icon name="sync_alt" sm />测试连接</button>
        <button class="btn outlined" :disabled="!!busy" @click="doValidate"><Icon name="verified_user" sm />校验权限与隔离</button>
        <button class="btn filled" :disabled="!!busy || (needsConfirm && !form.confirm_switch)" @click="doSave"><Icon name="save" sm />保存为待生效</button>
      </div>
      <p v-if="testRes" class="banner" :class="testRes.ok ? 'info' : 'error'" data-testid="db-test-result"><Icon :name="testRes.ok ? 'check_circle' : 'error'" />{{ testRes.message }}（{{ testRes.server_version }}，{{ testRes.latency_ms }} ms）</p>
      <div v-if="report" class="col" data-testid="db-report">
        <p class="banner" :class="report.ok ? 'info' : 'error'"><Icon :name="report.ok ? 'check_circle' : 'error'" />{{ report.ok ? '校验通过' : '校验未通过' }}，隔离：{{ report.isolation }}</p>
        <div v-for="c in report.checks" :key="c.id" class="row"><span class="chip" :class="chipOf(c.status)">{{ c.status }}</span><span class="grow">{{ c.title }}<span class="muted">：{{ c.detail }}</span></span></div>
      </div>
    </template>
  </section>
</template>

<style scoped>
.grid2 { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 16px; } .full { grid-column: 1 / -1; }
@media (max-width: 700px) { .grid2 { grid-template-columns: 1fr; } }
</style>
