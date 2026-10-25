# 参与开发

以下内容来自仓库根目录的 `AGENTS.md`，以该文件为准。

## 仓库布局

| 目录 | 内容 |
|---|---|
| `backend/` | Go API（Gin 仅做路由、恢复、CORS；处理器是 `net/http`，路径参数用 `r.PathValue`）、harness、归档、流水线、仓储、各命令 |
| `workers/document-ai/` | Python 3.12 解析 / OCR Worker 与 unittest 套件 |
| `frontend/` | Vue 3 + TypeScript + Vite；`src/api/schema.d.ts` 由契约生成；`src/mocks/` 仅为演示数据 |
| `desktop/` | Electron 外壳 |
| `contracts/` | `openapi.yaml`（唯一契约源）、`events.md` |
| `migrations/` | 迁移（`migrations/`、`mysql/`、`mssql/` 各方言） |
| `deploy/` | Docker、Windows 包、Linux 安装器 |
| `scripts/` | 构建、发布、测试脚本 |
| `docs/` | 文档（即本站源码） |
| `tests/samples/` | 带标注的演示样本 |

## 环境

Go 1.23+、Node 22+、Python 3.12、PostgreSQL 16。

一键开发环境：

```bash
scripts/dev.sh up [--mock]      # PostgreSQL + Worker + API(debug) + Vite，打开 http://localhost:5173
scripts/dev.sh down [--all] | status | logs [api|worker|web|pg] | token | reset
# Windows：powershell -ExecutionPolicy Bypass -File scripts\dev.ps1 up|down|status|logs|token|reset
```

## 构建与测试

| 位置 | 命令 |
|---|---|
| `backend/` | `go run ./cmd/oa-agent`；`go run ./cmd/migrate -dir ../migrations`；`go vet ./...`；`go test ./...` |
| `backend/`（集成测试） | 设置 `OA_TEST_DATABASE_URL` 指向一次性 PostgreSQL，否则 store / pipeline / db 集成测试跳过 |
| `backend/`（日志相关改动） | 另外跑 `go test -tags debug ./...` |
| `workers/document-ai/` | `pip install -r requirements.txt`；`python -m unittest -v` |
| `frontend/` | `npm ci`；`npm run dev`；`npm run build`（类型检查 + 构建）；`npm test` |
| 契约 | 编辑 `contracts/openapi.yaml` → `python scripts/sync_contract.py` → `python scripts/check_contracts.py`；前端 `npm run gen:api` |
| 端到端 | `go run ./cmd/e2e`（需运行中的 API + Worker + PostgreSQL）；`frontend/e2e/ui.mjs`（真实浏览器） |
| 多数据库 | `scripts/test-db-matrix.sh`（`OA_TEST_DATABASE_URL` / `OA_TEST_MYSQL_URL` / `OA_TEST_MSSQL_URL`） |
| Linux 安装器 | `bash deploy/linux/test/run.sh`（离线 shim） |
| 发布脚本 | `bash scripts/test/release.sh` |
| 本站文档 | 在 `docs/` 下 `npm install`，然后 `npm run docs:dev` / `npm run docs:build` |

构建模式：`scripts/build-backend.sh [debug|release]`（或 `scripts\build-backend.ps1 -Mode debug|release`，输出 `backend/bin`）。debug 带 `-tags debug`（启用 `logx.Debugf`，不优化便于 dlv）；release 为 `-trimpath -s -w`，调试日志被编译掉。调试日志只记 ID / 状态 / 耗时，绝不记密钥或文档文字。

Windows 包：`powershell -ExecutionPolicy Bypass -File deploy\windows\build.ps1`（调试包加 `-Mode debug`）；完整发布构建见[发布流程](./release)。

## 代码风格

- Go：`gofmt`（CI 检查 `gofmt -l cmd internal`）。
- Python：四空格缩进、snake_case。
- Vue / TypeScript：两空格缩进、单引号、无分号，Vue 文件名 PascalCase；没有配置前端 linter。
- PowerShell 构建脚本保持 UTF-8 **带 BOM**（Windows PowerShell 5 否则会弄乱中文）。
- 测试：Go `*_test.go`，Python `test_*.py`，Vitest `*.test.ts`。对改动的逻辑写行为测试，没有数值覆盖率门槛。改 API 后重新录制 e2e 响应并跑契约检查。

## 安全规则

- 密钥不进提交与日志。API 与 Worker 共用随机的 `OA_WORKER_TOKEN` 与 spool 根目录（独立启动器自动生成）。
- Go 不加载 `.env`；独立启动器读取 `oa-agent.env`（仅 `OA_*` 键）。
- **绝不给模型 shell 或写权限**：所有文件写入都走 `internal/archive`。文档文字是数据，不是指令。
- 文件管理：所有磁盘路径经 `fsman`（校验、防符号链接、不覆盖）；每次变更同时通过 `fileops` 同步 `ledger_entries`。
- CORS 允许名单只用精确来源，绝不用 `*`。
- 区分演示数据（`VITE_USE_MOCK`）与真实后端行为。

## 提交与 PR

用简洁的祈使句作标题。PR 描述行为变化，报告验证结果与跳过的项目；界面改动附截图（`frontend/e2e/screenshots/`）。

## 许可证

AGPL-3.0。第三方组件见[第三方组件与许可证](/THIRD-PARTY-LICENSES)。
