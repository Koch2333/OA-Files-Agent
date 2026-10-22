import { createRouter, createWebHistory } from 'vue-router'
import type { Role } from '../utils/authz'

declare module 'vue-router' {
  interface RouteMeta {
    title?: string
    /** 详情页高亮所属的侧栏项 */
    parent?: string
    /** 公开路由：无需会话（/login、/setup、/invite/:token） */
    public?: boolean
    /** 访问所需的最低角色（后端同样强制校验） */
    minRole?: Role
    /** enroll_mfa 受限会话 / 必须改密时仍允许访问 */
    allowRestricted?: boolean
  }
}

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/login', name: 'login', component: () => import('../views/LoginView.vue'), meta: { title: '登录', public: true } },
    { path: '/setup', name: 'setup', component: () => import('../views/SetupView.vue'), meta: { title: '初始化向导', public: true } },
    { path: '/invite/:token', name: 'invite', component: () => import('../views/InviteView.vue'), props: true, meta: { title: '接受邀请', public: true } },
    { path: '/forbidden', name: 'forbidden', component: () => import('../views/ForbiddenView.vue'), meta: { title: '无权访问', allowRestricted: false } },
    { path: '/account/security', name: 'account-security', component: () => import('../views/AccountSecurityView.vue'), meta: { title: '账号安全', allowRestricted: true } },
    { path: '/admin/users', name: 'admin-users', component: () => import('../views/AdminUsersView.vue'), meta: { title: '用户管理', minRole: 'admin' } },
    { path: '/admin/security', name: 'admin-security', component: () => import('../views/AdminSecurityView.vue'), meta: { title: '安全策略', minRole: 'admin' } },
    { path: '/admin/sessions', name: 'admin-sessions', component: () => import('../views/AdminSessionsView.vue'), meta: { title: '会话管理', minRole: 'admin' } },
    { path: '/admin/audit', name: 'admin-audit', component: () => import('../views/AdminAuditView.vue'), meta: { title: '审计日志', minRole: 'admin' } },
    { path: '/admin/diagnostics', name: 'admin-diagnostics', component: () => import('../views/DiagnosticsView.vue'), meta: { title: '运行诊断', minRole: 'admin' } },
    { path: '/ai', name: 'ai', component: () => import('../views/AiWorkbenchView.vue'), meta: { title: 'AI 工作台', minRole: 'operator' } },
    { path: '/', name: 'dashboard', component: () => import('../views/DashboardView.vue'), meta: { title: '工作台' } },
    { path: '/import', name: 'import', component: () => import('../views/ImportView.vue'), meta: { title: '批量导入 / 智能归档', minRole: 'operator' } },
    { path: '/tasks', name: 'tasks', component: () => import('../views/TasksView.vue'), meta: { title: '任务中心' } },
    { path: '/tasks/:id', name: 'task', component: () => import('../views/TaskDetailView.vue'), props: true, meta: { title: '任务详情', parent: 'tasks' } },
    { path: '/plans/:id', name: 'plan', component: () => import('../views/PlanView.vue'), props: true, meta: { title: '归档计划与审批', parent: 'tasks' } },
    { path: '/files', name: 'files', component: () => import('../views/FilesView.vue'), meta: { title: '文件库' } },
    { path: '/manage', name: 'manage', component: () => import('../views/FileManagerView.vue'), meta: { title: '文件管理' } },
    { path: '/storage', name: 'storage', component: () => import('../views/StorageView.vue'), meta: { title: '存储连接', minRole: 'admin' } },
    { path: '/ledger', name: 'ledger', component: () => import('../views/LedgerView.vue'), meta: { title: '结构化台账' } },
    { path: '/templates', name: 'templates', component: () => import('../views/TemplatesView.vue'), meta: { title: 'OA 模板与规则' } },
    { path: '/settings', name: 'settings', component: () => import('../views/SettingsView.vue'), meta: { title: '设置', minRole: 'operator' } },
    { path: '/:pathMatch(.*)*', name: 'notfound', component: () => import('../views/NotFoundView.vue'), meta: { title: '页面不存在' } },
  ],
})
router.afterEach((to) => { document.title = `${String(to.meta.title ?? '')} · OA 文件智能归档` })
