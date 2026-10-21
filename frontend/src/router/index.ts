import { createRouter, createWebHistory } from 'vue-router'

export const NAV = [
  { to: '/', name: 'dashboard', label: '工作台', icon: 'space_dashboard' },
  { to: '/import', name: 'import', label: '智能归档', icon: 'upload_file' },
  { to: '/tasks', name: 'tasks', label: '任务中心', icon: 'task_alt' },
  { to: '/files', name: 'files', label: '文件库', icon: 'folder_open' },
  { to: '/ledger', name: 'ledger', label: '结构化台账', icon: 'table_view' },
  { to: '/templates', name: 'templates', label: '模板与规则', icon: 'rule_settings' },
  { to: '/settings', name: 'settings', label: '设置', icon: 'settings' },
] as const

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', name: 'dashboard', component: () => import('../views/DashboardView.vue'), meta: { title: '工作台' } },
    { path: '/import', name: 'import', component: () => import('../views/ImportView.vue'), meta: { title: '批量导入 / 智能归档' } },
    { path: '/tasks', name: 'tasks', component: () => import('../views/TasksView.vue'), meta: { title: '任务中心' } },
    { path: '/tasks/:id', name: 'task', component: () => import('../views/TaskDetailView.vue'), props: true, meta: { title: '任务详情', parent: 'tasks' } },
    { path: '/plans/:id', name: 'plan', component: () => import('../views/PlanView.vue'), props: true, meta: { title: '归档计划与审批', parent: 'tasks' } },
    { path: '/files', name: 'files', component: () => import('../views/FilesView.vue'), meta: { title: '文件库' } },
    { path: '/ledger', name: 'ledger', component: () => import('../views/LedgerView.vue'), meta: { title: '结构化台账' } },
    { path: '/templates', name: 'templates', component: () => import('../views/TemplatesView.vue'), meta: { title: 'OA 模板与规则' } },
    { path: '/settings', name: 'settings', component: () => import('../views/SettingsView.vue'), meta: { title: '设置' } },
    { path: '/:pathMatch(.*)*', name: 'notfound', component: () => import('../views/NotFoundView.vue'), meta: { title: '页面不存在' } },
  ],
})
router.afterEach((to) => { document.title = `${String(to.meta.title ?? '')} · OA 文件智能归档` })
