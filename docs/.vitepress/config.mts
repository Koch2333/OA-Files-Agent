import { defineConfig } from 'vitepress'

const repo = 'https://github.com/Koch2333/OA-Files-Agent'
const base = normalizeBase(process.env.DOCS_BASE)

function normalizeBase(v?: string): string {
  if (!v) return '/'
  let b = v.trim()
  if (!b.startsWith('/')) b = '/' + b
  if (!b.endsWith('/')) b += '/'
  return b
}

const guide = [
  {
    text: '开始使用',
    items: [
      { text: '这是什么', link: '/guide/what-is-it' },
      { text: 'Windows 桌面版快速开始', link: '/guide/quick-start-windows' },
      { text: 'Linux 企业版快速开始', link: '/guide/quick-start-linux' },
      { text: '首次运行', link: '/guide/first-run' },
    ],
  },
  {
    text: '日常使用',
    items: [
      { text: '配置大模型密钥', link: '/guide/llm-keys' },
      { text: '导入与归档', link: '/guide/import-and-archive' },
      { text: '数据库选择', link: '/guide/databases' },
      { text: '常见问题与排错', link: '/guide/faq-troubleshooting' },
    ],
  },
  {
    text: '详细手册（原文）',
    items: [
      { text: '用户手册', link: '/用户手册' },
      { text: '大文件与批量导入', link: '/大文件与批量导入' },
      { text: 'Linux 服务器部署指南', link: '/部署指南-Linux' },
      { text: '多数据库部署与迁移指南', link: '/多数据库部署与迁移指南' },
      { text: '用平台账号登录授权配置 API Key', link: '/LLM账号登录配置' },
      { text: '界面指南（工作台与导航）', link: '/ui-guide' },
    ],
  },
]

const reference = [
  {
    text: '参考',
    items: [
      { text: '配置项（环境变量）', link: '/reference/configuration' },
      { text: 'oa-agentctl 命令', link: '/reference/oa-agentctl' },
      { text: 'HTTP API 与契约', link: '/reference/api' },
      { text: '架构', link: '/reference/architecture' },
    ],
  },
  {
    text: '原文参考',
    items: [
      { text: 'oa-agentctl 使用说明', link: '/oa-agentctl' },
      { text: '错误报告与诊断', link: '/diagnostics' },
      { text: '第三方组件与许可证', link: '/THIRD-PARTY-LICENSES' },
    ],
  },
]

const dev = [
  {
    text: '开发与发布',
    items: [
      { text: '参与开发', link: '/dev/contributing' },
      { text: '发布流程', link: '/dev/release' },
      { text: '构建与发布（原文）', link: '/构建与发布' },
    ],
  },
]

const design = {
  text: '设计文档 / Design notes',
  collapsed: true,
  items: [
    { text: '后端实现设计', link: '/backend-design' },
    { text: '登录与账号管理设计', link: '/auth-design' },
    { text: '存储连接设计', link: '/storage-design' },
    { text: 'OCR 抢救技能与文档预览设计', link: '/ocr-rescue-design' },
    { text: '多数据库兼容设计', link: '/多数据库兼容-设计' },
    { text: '界面规范：Material Design 3', link: '/界面规范-Material3' },
    { text: '安装脚本对比：1Panel', link: '/安装脚本对比-1Panel' },
  ],
}

export default defineConfig({
  lang: 'zh-CN',
  title: 'OA 文件智能归档',
  description: '企业 OA 文件智能归档智能体：批量识别、标准命名、先预览后审批再执行、自动台账。',
  base,
  cleanUrls: true,
  lastUpdated: false,
  appearance: 'dark',
  // docs/progress.md is a dated development log, not user documentation.
  srcExclude: ['progress.md', 'node_modules/**'],
  head: [['link', { rel: 'icon', href: `${base}favicon.svg`, type: 'image/svg+xml' }]],
  themeConfig: {
    siteTitle: 'OA 文件智能归档',
    nav: [
      { text: '指南', link: '/guide/what-is-it', activeMatch: '^/(guide/|用户手册|大文件|部署指南|多数据库部署|LLM账号|ui-guide)' },
      { text: '参考', link: '/reference/configuration', activeMatch: '^/(reference/|oa-agentctl|diagnostics|THIRD)' },
      { text: '开发', link: '/dev/contributing', activeMatch: '^/(dev/|构建与发布)' },
      {
        text: '设计文档',
        items: [
          { text: '后端实现设计', link: '/backend-design' },
          { text: '登录与账号管理设计', link: '/auth-design' },
          { text: '存储连接设计', link: '/storage-design' },
          { text: 'OCR 抢救与预览设计', link: '/ocr-rescue-design' },
          { text: '多数据库兼容设计', link: '/多数据库兼容-设计' },
          { text: '界面规范 Material 3', link: '/界面规范-Material3' },
        ],
      },
      { text: 'v1.0.0', items: [{ text: '发行版', link: `${repo}/releases` }, { text: '许可证 AGPL-3.0', link: `${repo}/blob/main/LICENSE` }] },
    ],
    sidebar: {
      '/guide/': [...guide, design],
      '/reference/': [...reference, design],
      '/dev/': [...dev, design],
      '/': [...guide, ...reference, ...dev, design],
    },
    socialLinks: [{ icon: 'github', link: repo }],
    search: {
      provider: 'local',
      options: {
        translations: {
          button: { buttonText: '搜索文档', buttonAriaLabel: '搜索文档' },
          modal: {
            noResultsText: '没有找到结果',
            resetButtonTitle: '清除',
            footer: { selectText: '选择', navigateText: '切换', closeText: '关闭' },
          },
        },
      },
    },
    editLink: { pattern: `${repo}/edit/main/docs/:path`, text: '在 GitHub 上编辑此页' },
    outline: { label: '本页目录', level: [2, 3] },
    docFooter: { prev: '上一页', next: '下一页' },
    darkModeSwitchLabel: '外观',
    sidebarMenuLabel: '菜单',
    returnToTopLabel: '回到顶部',
    langMenuLabel: '语言',
    lightModeSwitchTitle: '切换到浅色模式',
    darkModeSwitchTitle: '切换到深色模式',
    footer: { message: '以 AGPL-3.0 许可证发布', copyright: 'OA 文件智能归档智能体 · 1.0.0' },
  },
})
