import { roleAtLeast, type Role } from './authz'

/** 侧栏导航模型（纯数据 + 纯函数，便于测试）。路由路径不变，只是重新分组。 */
export type NavBadge = 'review' | 'diag'
export interface NavItem { to: string; name: string; label: string; icon: string; minRole?: Role; badge?: NavBadge; hint?: string }

/** 日常功能，≤7 项，按使用频率排序。 */
export const PRIMARY_NAV: readonly NavItem[] = [
  { to: '/', name: 'dashboard', label: '工作台', icon: 'space_dashboard' },
  { to: '/import', name: 'import', label: '智能归档', icon: 'upload_file', minRole: 'operator' },
  { to: '/ai', name: 'ai', label: 'AI 工作台', icon: 'smart_toy', minRole: 'operator' },
  { to: '/tasks', name: 'tasks', label: '任务中心', icon: 'task_alt', badge: 'review', hint: '待你审批的任务数' },
  { to: '/files', name: 'files', label: '文件库', icon: 'folder_open' },
  { to: '/ledger', name: 'ledger', label: '台账（目录日志）', icon: 'table_view' },
  { to: '/manage', name: 'manage', label: '文件管理', icon: 'folder_managed' },
]

/** 低频管理功能，收进“设置与管理”分组。 */
export const SETTINGS_NAV: readonly NavItem[] = [
  { to: '/templates', name: 'templates', label: '模板与规则', icon: 'rule_settings' },
  { to: '/storage', name: 'storage', label: '存储连接', icon: 'cloud_sync', minRole: 'admin' },
  { to: '/settings', name: 'settings', label: '设置', icon: 'settings', minRole: 'operator', hint: '大模型 / 个人设置' },
  { to: '/admin/users', name: 'admin-users', label: '用户管理', icon: 'group', minRole: 'admin' },
  { to: '/admin/security', name: 'admin-security', label: '安全策略', icon: 'shield_lock', minRole: 'admin' },
  { to: '/admin/sessions', name: 'admin-sessions', label: '会话管理', icon: 'devices', minRole: 'admin' },
  { to: '/admin/audit', name: 'admin-audit', label: '审计日志', icon: 'history', minRole: 'admin' },
  { to: '/admin/diagnostics', name: 'admin-diagnostics', label: '运行诊断', icon: 'monitor_heart', minRole: 'admin', badge: 'diag' },
]
export const SETTINGS_GROUP_LABEL = '设置与管理'

const visible = (items: readonly NavItem[], role: Role | null | undefined) => items.filter((n) => !n.minRole || roleAtLeast(role, n.minRole))

export interface NavModel { primary: NavItem[]; settings: NavItem[] }
export function buildNav(role: Role | null | undefined): NavModel {
  return { primary: visible(PRIMARY_NAV, role), settings: visible(SETTINGS_NAV, role) }
}
/** 当前高亮项属于“设置与管理”时，分组需要自动展开。active 为 meta.parent ?? route.name。 */
export function inSettingsGroup(activeName: string): boolean { return SETTINGS_NAV.some((n) => n.name === activeName) }
/** 分组是否展开：用户本会话手动选择优先；没有选择时，路由落在分组内就展开，否则默认收起。进入分组时 App 会把选择置为展开。 */
export function settingsExpanded(activeName: string, userChoice: boolean | null): boolean { return userChoice ?? inSettingsGroup(activeName) }
/** 分组头徽标：收起时把子项徽标汇总到分组头，避免错误报告被藏起来。 */
export function groupBadge(items: readonly NavItem[], badges: Partial<Record<NavBadge, string>>): string {
  for (const n of items) if (n.badge && badges[n.badge]) return badges[n.badge]!
  return ''
}
