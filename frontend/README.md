# OA 文件智能归档 · 前端

Vue 3 + TypeScript + Vite + Vue Router + Pinia，Material Design 3（种子色动态配色、明/暗/跟随系统、Lucide 内联 SVG 图标，可离线）。

## 运行
```bash
npm install
npm run dev        # http://localhost:5173
npm run build      # 类型检查 + 生产构建
npm test           # 单元测试
```
- 开发默认 **演示数据模式**（`.env.development` 中 `VITE_USE_MOCK=true`），顶部常驻提示条；可在“设置”中切换。
- 连接真实后端：设置中关闭演示数据，或设 `VITE_USE_MOCK=false`。`/v1`、`/healthz`、`/readyz` 经 Vite 代理到 `OA_API_TARGET`（默认 `http://127.0.0.1:8080`）。
- 若在 WSL 下无 Node，可使用 Windows 的 Node（`cmd.exe /c "cd /d <路径> && npm run dev"`）。

## 结构
- `src/api/` 仅 HTTP/SSE：`types.ts`（草案类型）、`http.ts`、`sse.ts`（Last-Event-ID 续传 + 退避重连）、`real.ts`、`index.ts`（真实/演示切换）。
- `src/mocks/` 仅演示数据，不与真实流程混用。
- `src/views/` 页面：工作台、导入、任务中心/详情、归档计划与审批、文件库、台账、模板与规则、设置。
- `src/components/` 通用组件与纠错抽屉。`src/theme/` M3 配色。

## 状态
页面、路由、主题、演示模式与真实后端联调均已完成；类型由 OpenAPI 生成。真实浏览器端到端测试：`UI_BASE=http://127.0.0.1:8080 UI_SAMPLES=<样本目录> node e2e/ui.mjs`（需运行中的后端并由其提供 `dist`）。截图在 `e2e/screenshots/`。接口缺口历史记录见 [CONTRACT-GAPS.md](CONTRACT-GAPS.md)（现均已由后端实现）。

## 登录与账号（Passkey + TOTP）
- 会话是 HttpOnly Cookie（`oa_session`），前端读不到；CSRF 令牌只放内存（登录/`/v1/auth/me` 返回），非 GET 请求自动带 `X-CSRF-Token`，`CSRF_INVALID` 时重取并重试一次。令牌/密钥/恢复码从不写入 localStorage 或日志。
- 公开路由：`/login`、`/setup`（首次安装向导，初始化后不再出现）、`/invite/:token`。其余路由需会话；`scope=enroll_mfa` 只能进入 `/account/security`；`must_change_password` 强制改密。管理页 `/admin/*` 仅管理员；操作员可进入设置（仅个人大模型）但看不到存储连接；只读用户看不到任何写入按钮。后端同样强制校验，界面只是不提供不允许的操作。
- 页面：登录、安装向导（企业模式 8 步，含“配置全局大模型”）、邀请、账号安全、用户管理、安全策略、会话管理、审计日志。Passkey 使用 `@simplewebauthn/browser`，二维码用 `qrcode` 在本地渲染。
- 演示模式：已自动以演示管理员登录；退出后可用 `admin` / `operator` / `viewer`，密码 `demo-password`，验证码 `123456`，恢复码 `DEMO1-DEMO2`；WebAuthn 为模拟。地址加 `?mock_setup=1` 可演示安装向导。
- 真实浏览器测试（需运行中的企业模式后端，使用 DevTools 虚拟认证器，不要对生产环境运行）：`UI_SETUP_TOKEN=<令牌> UI_ADMIN_PASSWORD=<满足策略的密码> node e2e/ui_auth.mjs`；已初始化的服务器另设 `UI_ADMIN_USER`、`UI_TOTP_SECRET`。`e2e/ui.mjs` 在企业模式下通过 `UI_ADMIN_USER`/`UI_ADMIN_PASSWORD`/`UI_TOTP_SECRET` 先登录。

## AI 工作台、全自动模式与运行诊断
- 路由：`/ai`（AI 工作台，操作员及以上）、`/admin/diagnostics`（运行诊断，管理员）、`/settings#llm`（大模型设置，操作员及以上可进入，管理员项按角色显示）。
- 演示模式可完整演示：`?mock_llm=none|global|personal` 选择大模型状态（默认 `global`）；`?mock_unclean=0` 隐藏“上次异常结束”样例，`?mock_disk=low` 显示磁盘告警；切换角色请退出后用 `operator` / `viewer` 登录（密码 `demo-password`，验证码 `123456`）。
- 全自动模式的确认只保存在当前页面会话的内存里，刷新后会重新询问；诊断包在演示模式下是一个小文本文件。
- 开发服务器（`npm run dev`）额外提供 `window.__oaTestError()` 用于触发前端错误上报；生产构建没有。对真实后端做诊断页浏览器测试：`OA_API_TARGET=http://127.0.0.1:8080 npm run dev`，另一终端 `UI_BASE=http://127.0.0.1:5173 UI_DEV=1 UI_ADMIN_USER=… UI_ADMIN_PASSWORD=… UI_TOTP_SECRET=… node e2e/ui_diag.mjs`。
- AI 工作台浏览器测试（需运行中的完整栈和一个 OpenAI 兼容的假模型服务）：`UI_FAKE_LLM_URL=http://127.0.0.1:18080/v1 UI_SAMPLES=<样本目录> UI_ADMIN_USER=… UI_ADMIN_PASSWORD=… UI_TOTP_SECRET=… node e2e/ui_ai.mjs`（会改动全局大模型与 AI 策略，请用一次性实例）。
