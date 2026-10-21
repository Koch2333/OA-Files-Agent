import { realApi } from './real'
import { mockApi } from '../mocks/mockApi'
import type { Api } from './types'

export * from './types'

const KEY = 'oa.useMock'
function initial(): boolean {
  try { const v = localStorage.getItem(KEY); if (v !== null) return v === '1' } catch { /* 隐私模式 */ }
  return import.meta.env.VITE_USE_MOCK === 'true'
}
let useMock = initial()

/** 演示数据开关（设置页）。切换后整页刷新，避免真实/演示数据混用。 */
export function isMock() { return useMock }
export function setMock(v: boolean) {
  try { localStorage.setItem(KEY, v ? '1' : '0') } catch { /* ignore */ }
  location.reload()
}

/** 代理转发，每次调用时选择实现；业务代码只依赖 Api 接口。 */
export const api: Api = new Proxy({} as Api, {
  get: (_t, k) => (useMock ? mockApi : realApi)[k as keyof Api],
})
