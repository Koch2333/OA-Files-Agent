import { router } from './router'
import { setAuthHooks, setCsrfToken } from './api/http'
import { api } from './api'
import { useAuth } from './stores/auth'
import { safeRedirect } from './utils/authz'
import { createManagedPoller, resumeAllPollers } from './utils/schedule'

/** 在 pinia 就绪后安装：路由守卫、401/503/409/CSRF 钩子、会话到期探测。 */
export function installAuth() {
  const auth = useAuth()

  router.beforeEach(async (to) => {
    if (!auth.ready) await auth.init()
    // 未登录时重新探测初始化状态不必要；已知未初始化的页面保持原样
    return auth.guard({ path: to.path, fullPath: to.fullPath, meta: to.meta })
  })

  setAuthHooks({
    onUnauthenticated() {
      auth.expire()
      const cur = router.currentRoute.value
      if (cur.meta.public) return
      void router.replace({ path: '/login', query: { redirect: safeRedirect(cur.fullPath), expired: '1' } })
    },
    onSetupRequired() {
      auth.setup = auth.setup ? { ...auth.setup, initialized: false } : auth.setup
      if (router.currentRoute.value.path !== '/setup') void router.replace('/setup')
    },
    onWizardRequired() {
      // 服务端闸门：向导尚未完成（本地状态落后）——标记并回到向导，从服务端状态恢复到正确的步骤
      auth.markWizardUnfinished()
      if (router.currentRoute.value.path !== '/setup') void router.replace('/setup')
    },
    async refreshCsrf() {
      const me = await api.getMe({ silent: true })
      setCsrfToken(me.csrf_token)
    },
  })

  // 绝对超时/空闲超时：会话到期后探测一次（401 会走 onUnauthenticated）。
  // 只在“有会话且系统已初始化”时运行；标签页隐藏时暂停；探测失败（503/网络）时指数退避。
  // 未登录的访问者（登录页/向导）不会产生任何周期请求。
  createManagedPoller({
    intervalMs: 30_000, maxIntervalMs: 300_000,
    enabled: () => !!auth.me && !!auth.setup?.initialized,
    run: async () => {
      const m = auth.me
      if (m && Date.parse(m.session_expires_at) < Date.now()) auth.me = await api.getMe()
    },
  }).start()

  document.addEventListener('visibilitychange', () => { if (!document.hidden) resumeAllPollers() })
  window.addEventListener('online', () => resumeAllPollers())
}
