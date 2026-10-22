/// <reference types="vite/client" />
declare module '*.vue' {
  import type { DefineComponent } from 'vue'
  const component: DefineComponent<object, object, unknown>
  export default component
}
declare const __APP_VERSION__: string
interface Window {
  /** 仅开发服务器（import.meta.env.DEV）安装：用于浏览器测试触发一个未捕获的前端错误 */
  __oaTestError?: (message?: string) => void
  __oaTestRejection?: (message?: string) => void
}
