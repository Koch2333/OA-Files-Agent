import type { App, ComponentPublicInstance } from 'vue'
import { api } from './api'
import { router } from './router'
import { useAuth } from './stores/auth'
import { useUi } from './stores/ui'
import { copyText } from './utils/clipboard'
import { createReporter, type Reporter } from './utils/errorReporter'

const version = typeof __APP_VERSION__ !== 'undefined' ? __APP_VERSION__ : 'dev'
let reporter: Reporter | null = null

/** 全局唯一上报器；仅已登录会话才会上报（演示模式下写入演示数据，便于展示）。 */
export function getReporter(): Reporter {
  if (reporter) return reporter
  const auth = useAuth(), ui = useUi()
  reporter = createReporter({
    send: (input) => api.reportClientError(input),
    enabled: () => auth.loggedIn,
    route: () => router.currentRoute.value.fullPath,
    appVersion: version,
    notify: ({ message, copy }) => ui.toast(message, 'error', { label: '复制详情', run: () => { void copyText(copy).then((ok) => { if (ok) ui.toast('已复制', 'success') }) } }),
  })
  return reporter
}

function componentName(inst: ComponentPublicInstance | null): string | undefined {
  const o = inst?.$options as { name?: string; __name?: string } | undefined
  return o?.name || o?.__name
}

/** Vue errorHandler + window.onerror + unhandledrejection；开发服务器额外提供 window.__oaTestError 供浏览器测试。 */
export function installErrorReporting(app: App) {
  app.config.errorHandler = (err, inst, info) => {
    console.error(err)
    void getReporter().report(err, { component: componentName(inst) ?? info })
  }
  window.addEventListener('error', (e) => {
    if (!e.error && !e.message) return
    void getReporter().report(e.error ?? e.message)
  })
  window.addEventListener('unhandledrejection', (e) => { void getReporter().report(e.reason) })
  if (import.meta.env.DEV) {
    window.__oaTestError = (message = 'e2e test error') => { setTimeout(() => { throw new Error(message) }, 0) }
    window.__oaTestRejection = (message = 'e2e test rejection') => { void Promise.reject(new Error(message)) }
  }
}
