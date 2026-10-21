# OA 文件智能归档 · 前端

Vue 3 + TypeScript + Vite + Vue Router + Pinia，Material Design 3（种子色动态配色、明/暗/跟随系统、Material Symbols 本地字体，可离线）。

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
