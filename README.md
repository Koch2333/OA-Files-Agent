# 企业 OA 文件智能归档智能体

批量导入本地文件/文件夹（Word、Excel、PDF、图片/扫描件）→ 文件名 + 正文/OCR 联合识别部门、类型、日期 → 按规则标准命名与分类 → **预览、审批、再执行** → 自动台账（搜索、Excel 导出）。

| 目录 | 内容 |
|---|---|
| `backend/` | Go API、Agent Harness、归档服务、流水线、PostgreSQL 仓储、启动器 |
| `workers/document-ai/` | Python 解析/OCR Worker（python-docx、openpyxl、PyMuPDF、RapidOCR） |
| `frontend/` | Vue 3 + TypeScript + Material Design 3 界面 |
| `contracts/` | `openapi.yaml`（唯一契约源）、`events.md` |
| `migrations/` | PostgreSQL 迁移 |
| `deploy/windows/` | Windows 单文件夹包的构建脚本与启动器 |
| `docs/` | 用户手册、后端设计、进度、第三方许可证 |
| `tests/samples/` | 带标注的演示样本（含损坏/加密文件）与 `manifest.json` |

## 最终用户（Windows）
解压发布包，双击 `start.bat`，详见 [docs/用户手册.md](docs/用户手册.md)。无需安装 Python/数据库/Office。

## 开发
需要 Go 1.23+、Node 22+、Python 3.12、PostgreSQL 16。

```bash
# 数据库与迁移（开发库；-reset 会清空，仅限开发）
export OA_DATABASE_URL='postgres://oa@127.0.0.1:5432/postgres?sslmode=disable'
cd backend && go run ./cmd/migrate -dir ../migrations

# Worker（与 API 使用同一 token 与 spool 目录）
cd workers/document-ai && python -m venv .venv && .venv/Scripts/pip install -r requirements.txt
OA_WORKER_TOKEN=<32位以上随机串> OA_SPOOL_ROOT=<绝对路径> .venv/Scripts/python server.py

# API（同样的 OA_WORKER_TOKEN / OA_SPOOL_ROOT；OA_FRONTEND_DIR 指向 frontend/dist 可由 API 直接提供界面）
cd backend && go run ./cmd/oa-agent

# 前端
cd frontend && npm ci && npm run dev      # 开发默认“演示数据”模式，设置页可切换到真实后端
```
环境变量见 `.env.example`。API 文档：`http://127.0.0.1:8080/docs`，契约：`/openapi.yaml`。

## 测试
```bash
cd backend && go vet ./... && go test ./...                   # 含真实 PostgreSQL 的集成测试需设置 OA_TEST_DATABASE_URL
cd workers/document-ai && python -m unittest -v
python scripts/check_contracts.py                             # 契约 + 处理器样本
cd frontend && npm run build && npm test
```
端到端（需运行中的 API + Worker + PostgreSQL）：
```bash
cd backend && go run ./cmd/e2e -samples ../tests/samples -archive <归档根目录> -record ../data/e2e-samples.json
python scripts/check_contracts.py --recorded data/e2e-samples.json     # 用真实响应校验 OpenAPI
go run ./cmd/e2e -examples <真实样本目录> [-blind]                       # 真实样本；-blind 仅用正文
cd frontend && UI_BASE=http://127.0.0.1:8080 UI_SAMPLES=<样本目录> node e2e/ui.mjs   # 真实浏览器 UI 测试（puppeteer-core + Edge/Chrome）
```

## Windows 发布包
```powershell
powershell -ExecutionPolicy Bypass -File deploy\windows\build.ps1
```
产物 `dist\oa-agent\`：`oa-agent.exe`、`web/`、`worker/`（PyInstaller onedir）、`pgsql/`（内置 PostgreSQL 16）、`migrations/`、`start.bat`、`oa-agent.env`、手册、样本。需先建立 `workers/document-ai/.venv` 并 `pip install -r requirements.txt pyinstaller`。**在目标环境发布前请查看 [docs/THIRD-PARTY-LICENSES.md](docs/THIRD-PARTY-LICENSES.md)（PyMuPDF 为 AGPL）。**

## 关键设计
- **Agent 建议，Go 确定性代码执行。** LLM/规则只能通过只读、带 Schema 的工具读取当前文件；文件内容当作数据（防提示词注入）。路径规范化、冲突、写入全部在 `internal/archive`。
- **先预览、后审批、再执行。** 批准与执行是两个接口；执行带 `Idempotency-Key`，有日志（journal），可恢复、可回滚；原件只复制不移动，不覆盖，重名自动加序号。
- **证据不足就留空。** 无法确认的字段为 `null` 并要求人工确认；仅凭正文关键词推断的值也会要求确认。
- **PostgreSQL 为真相源**，Excel 只是导出。任务队列持久化，重启后自动续跑。
