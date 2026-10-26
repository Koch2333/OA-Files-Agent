# 界面规范：Material Design 3

前端（`frontend/`）按标准 Material Design 3（M3）实现：配色、形状、层级、字阶、动效全部由 token 驱动，组件为手写样式（不引入 UI 框架，不依赖 CDN，离线可用，中文友好）。本文是新增或修改界面时的依据。

- 颜色角色由 `src/theme/theme.ts` 在运行时写入 `:root`（`--md-sys-color-*`，含 `surface-container-lowest … highest`、`surface-dim/bright`，取自种子色的 neutral 色板）。
- 其余 token 与全局组件类集中在 `src/styles/base.css`；视图只写布局，不写颜色/字号常量。
- 旧名称兼容：`--c-surface-1/2/3`（= surface-container low / container / high）、`.t-title`、`.t-title-m`、`.t-label`、`.t-display` 仍可用。

## 1. Token

### 1.1 形状（圆角）

| Token | 值 | 用途 |
|---|---|---|
| `--shape-xs` | 4px | 输入框、菜单、Snackbar、kbd |
| `--shape-sm` | 8px | 标签（Chip）、骨架 |
| `--shape-md` | 12px | 卡片、横幅 |
| `--shape-lg` | 16px | FAB |
| `--shape-xl` | 28px | 对话框、侧边抽屉 |
| `--shape-full` | 999px | 按钮、分段按钮、搜索框、导航指示器 |

### 1.2 层级（阴影）

`--elev-1` … `--elev-5` 对应 M3 Level 1–5。卡片 elevated = 1，菜单 = 2，对话框/Snackbar/FAB = 3。

### 1.3 动效

| Token | 值 | 用途 |
|---|---|---|
| `--ease` | cubic-bezier(.2,0,0,1) | 强调（标准）缓动 |
| `--ease-decel` | cubic-bezier(.05,.7,.1,1) | 进入（对话框、菜单、Snackbar、抽屉） |
| `--ease-accel` | cubic-bezier(.3,0,.8,.15) | 退出 |
| `--dur-1/s/m/l/xl` | 100/200/300/400/500ms | 微交互 / 状态层 / 进入 / 大范围 / 页面级 |

`prefers-reduced-motion: reduce` 时所有动画与过渡缩为约 0ms。

### 1.4 状态层与禁用

`--state-hover` 8%、`--state-focus` 10%、`--state-pressed` 10%、`--state-dragged` 16%。实现方式：元素内 `::before` 覆盖层使用 `currentColor`，无水波纹。禁用：内容 `--opacity-disabled-content`（38%），容器 `--opacity-disabled-container`（12%）。焦点环：3px `--md-sys-color-secondary`，偏移 2px，跟随元素自身圆角。

### 1.5 字阶

字体栈不变（Segoe UI Variable / Microsoft YaHei UI / PingFang SC / Noto Sans SC / system-ui）。每个角色一个 token 与一个类：

| 角色 | token（font 简写） | 类 |
|---|---|---|
| display large / medium / small | 400 57/64、45/52、36/44 | `.type-display-*` |
| headline large / medium / small | 400 32/40、28/36、24/32 | `.type-headline-*`（页面标题 = headline-small，对话框标题同） |
| title large / medium / small | 400 22/28、500 16/24、500 14/20 | `.type-title-*`（`.t-title` / `.t-title-m`） |
| body large / medium / small | 400 16/24、14/20、12/16 | `.type-body-*`（正文默认 body-medium） |
| label large / medium / small | 500 14/20、12/16、11/16 | `.type-label-*`（按钮、导航、Chip；`.t-label`） |

中文没有细字重，因此 display/headline 采用 400 而非 300，正文字距取较小值。

### 1.6 扩展颜色

成功 `--c-success*`、警告 `--c-warn*`（含 container / on-container，亮/暗各一套，均满足 4.5:1）。新增语义色请照此在 `base.css` 的三处（默认、`prefers-color-scheme: dark`、`data-dark`）成对定义，不要写死十六进制。

## 2. 组件清单

| 组件 | 类 / 文件 | M3 规格要点 |
|---|---|---|
| 导航抽屉 | `App.vue` `.nav` `.item` | 宽 280，surface-container-low，项高 56，全圆角激活指示器（secondary-container），label-large |
| 导航栏（Rail） | `App.vue`，折叠或 ≤1000px 自动 | 宽 80，56×32 指示器药丸 + 其下 label-medium 标签，徽标 16dp |
| 顶栏 / 页头 | `PageHeader.vue` | headline-small 标题 + 操作区；保持粘性页头与负 `top` 偏移（`top: -padding`），否则会切掉下一块内容 |
| 按钮 | `.btn` + `.filled/.tonal/.outlined/.elevated`，文本按钮为默认 | 高 40，全圆角，label-large，状态层 8/10/10；文本 12dp、其他 24dp 水平内边距，带图标时左侧 8/16dp；`.sm` 为 32 高（密集） |
| 图标按钮 | `.btn.icon-only`（+ `.filled/.tonal/.outlined`） | 40dp 容器，48dp 触控区（`::after` 外扩），`aria-pressed` 为选中色 |
| FAB | `.fab` | 56dp，primary-container，Level 3 |
| 文本框 | `.field` + `.input`（outlined）/ `.input.filled` | `.field` 内 56dp 高、4dp 圆角、1px outline，hover on-surface，焦点 2px primary，错误 `aria-invalid="true"` 变 error；支持文本 `.supporting`（`.error`）；工具栏内独立 `.input` 40dp，`.input.sm` 32dp（密集，窄屏回到 44 触控高度）；原生 `<input type=date>` 同样式 |
| 选择 / 菜单 | `select.input`、`.md-menu` + `.md-menu-item` | 菜单 surface-container、4dp 圆角、Level 2、项高 48、状态层 |
| 复选框 / 单选 | 原生 + `accent-color` | 18dp，primary |
| 开关 | `.switch` | 52×32 轨道，24dp 图标拇指（✕ / ✓），无障碍保持原生 checkbox |
| 分段按钮 | `.seg` | 高 40，选中 secondary-container |
| 标签页 | `.tabs` + `[role=tab]` | 高 48，激活指示 3dp |
| 标签（Chip） | `.chip`（只读标签）、`button.chip`（assist/filter/input/suggestion） | 高 32、圆角 8、label-large；表格内 `.tbl .chip`/`.chip.sm` 为 24 高（密集）；`aria-pressed="true"` 为选中 |
| 卡片 | `.card`（filled）、`.card.elevated`、`.card.outlined` | 12dp 圆角；filled = surface-container-highest，elevated = container-low + Level 1，outlined = surface + outline-variant |
| 可输入下拉框（combobox） | `ComboField.vue`（纯逻辑 `utils/classification.ts`） | M3 outlined 文本框 + 末尾 `arrow_drop_down`/清除图标按钮；ARIA 1.2 combobox（`role=combobox`、`aria-expanded`、`aria-controls`、`aria-activedescendant`）+ listbox；输入即过滤，任意文本可输入，列表底部“新建值”；分组“已使用 / 预设 / 兜底分类”；↑↓ Enter Esc Home End Alt+↓；列表 Teleport 到 body（Dialog 内不被裁剪），选项高 48（粗指针）/40；错误显示在输入框下方（`.supporting.error`）；值纯文本渲染 |
| 步骤按钮 | `StepButtons.vue`（纯逻辑 `utils/planSteps.ts`） | ①②编号徽标 + chevron 连接；当前步 tonal，已完成 outlined + check，下一步 filled，锁定步 lock 图标 + `aria-disabled`（仍可聚焦/点击）；悬停/聚焦 plain tooltip，点击弹出带动作的气泡（`role=dialog` 非模态，Esc 关闭）；首次提示 coach 气泡（不抢焦点，记住在 localStorage，存储失败时不显示）；提示脉冲遵守 `prefers-reduced-motion` |
| 对话框 | `Dialog.vue` | 28dp 圆角，surface-container-high，headline-small 标题，body-medium 说明，操作右对齐 8dp 间距，遮罩 32%，进入用 emphasized decelerate |
| 侧边抽屉 | `Drawer.vue` | surface-container-low，左侧 28dp 圆角，宽 ≤480 |
| Snackbar | `Toasts.vue` | inverse-surface，4dp 圆角，最小高 48，操作为文本按钮（inverse-primary）；错误用 error-container |
| 进度 | `.progressbar` | 线性 4dp，轨道 surface-container-highest；旋转图标用 `progress_activity` + `.spin` |
| 列表 | `.md-list` `.md-list-item(.two/.three)` | 单/双/三行 56/72/88dp |
| 工具提示 | `.md-tooltip`（现用原生 `title`） | inverse-surface，body-small |
| 徽标 | 导航徽标；`.badge-m3`（`.dot`） | 大徽标 16dp，小徽标 6dp |
| 横幅 | `.banner`（默认警告、`.error`、`.info`） | tonal container，12dp 圆角 |
| 数据表 | `table.tbl` | 保持密集；表头 title-small，正文 body-medium，行 hover 8% 状态层，选中 secondary-container；`data-density=compact` 更紧 |

### 已知偏离（有意）

1. 文本框标签在输入框上方（非浮动标签）；改成浮动标签需要改动所有表单标记。
2. 表格内 Chip 24dp、`.btn.sm` 32dp、独立 `.input` 40dp 为密集桌面场景（M3 密度 −1/−2）。
3. 窄屏（≤1000px）使用 Rail 而非底部导航栏；“设置与管理”分组仍以展开子项形式出现。
4. 原生 `<select>` 下拉箭头与弹出层由浏览器绘制。

## 3. 图标策略

- 图标集：**Material Symbols Outlined，字重 400，grade 0，光学尺寸 24**（npm `@material-symbols/svg-400`，Apache-2.0）。
- 离线：路径数据由 `npm run gen:icons`（`frontend/scripts/gen-icons.mjs`）生成为 `src/components/iconGlyphs.ts`，只包含 `iconMap.ts` 中登记的图标；没有字体文件，没有 CDN。
- 唯一入口：`<Icon name="…" />`（`sm` = 18dp，`md` = 20dp，默认 24dp；颜色继承 `currentColor`；默认 `aria-hidden`，需要朗读时传 `label`）。
- 选中/激活用填充变体：`<Icon fill>`，仅对 `iconMap.ts` 中 `FILLED` 列出的图标有效，其余仍用轮廓。
- **新增图标**：在 `iconMap.ts` 的 `NAMES` 里加 `界面名: 'material_symbol_name'`（名称查 fonts.google.com/icons，下划线形式），需要填充变体就加入 `FILLED`，运行 `npm run gen:icons`，提交 `iconGlyphs.ts`。`iconMap.test.ts` 会扫描 `src/` 中所有 `<Icon name>`，未登记即测试失败，并禁止引入其他图标库。
- 保留了旧（Lucide 时代）别名：`trash`、`spinner`、`loading`、`success`、`failure`、`attention`、`ai`、`files`、`security` 等。

## 4. 新组件检查清单

- [ ] 颜色只用 `--md-sys-color-*` / `--c-success*` / `--c-warn*`，无十六进制（文档预览纸张除外）；亮色与暗色都看过。
- [ ] 文字与背景对比度 ≥ 4.5:1（大字 3:1）；不用 `opacity` 降低文字对比度，禁用态除外（38%）。
- [ ] 字号与行高来自字阶 token，不写 `font-size: 13px` 之类常量。
- [ ] 圆角用 `--shape-*`，阴影用 `--elev-*`，动效用 `--ease*` 与 `--dur-*`（200–500ms）。
- [ ] 可交互元素有 hover 8% / focus 10% / pressed 10% 状态层、可见焦点环、禁用态 38%/12%。
- [ ] 触控目标 ≥ 48dp（图标按钮用 `::after` 外扩；窄屏 `min-height: 44px` 规则已全局提供）。
- [ ] 图标用 `<Icon>`，已在 `iconMap.ts` 登记并运行过 `npm run gen:icons`。
- [ ] 对话框用 `Dialog.vue`（焦点陷阱、Esc、`role`）；提示用 `ui.toast`；不要自造弹层。
- [ ] 尊重 `prefers-reduced-motion`；不依赖悬停传达信息。
- [ ] 中文文案不被截断：窄屏（390/820）截图检查，运行 `node e2e/ui_clip.mjs` 与 `node e2e/ui_layout.mjs`；粘性页头不要改 `top`。
- [ ] 保留 `data-testid`、`aria-*`、路由、事件与 props；不在视图里写业务样式覆盖全局类。

## 5. 验证

- `npm run build`、`npm test`（含图标映射测试）。
- 视觉巡检：`UI_BASE=http://127.0.0.1:5199 node e2e/ui_m3.mjs`（演示数据模式，亮/暗 × 1440/820/390 宽度），截图输出到 `frontend/e2e/screenshots/m3/`。
