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
安装 `setup.exe`（或解压便携 zip）后双击 `OA 文件智能归档`；服务端版解压后双击 `start.bat`。详见 [docs/用户手册.md](docs/用户手册.md)。无需安装 Python/数据库/Office。

## Linux 服务器部署（企业版，Docker）
```bash
# 在仓库检出内：
sudo bash deploy/linux/install.sh              # 交互式；--yes 为非交互，--domain oa.example.com 启用 HTTPS
# 远程一键（<OA_REPO_URL> 为占位，请替换为你的发行页地址）：
curl -fsSL <OA_REPO_URL>/releases/latest/download/install.sh | sudo bash -s -- --domain oa.example.com
```
安装 Docker 栈（PostgreSQL + API + 文档 Worker，可选 Caddy 自动 HTTPS），归档目录可指定任意本机路径或 NAS 挂载点，日常管理使用 `oa-agentctl`。Windows 版面向个人单机使用，Linux 版是企业版（多用户、MFA、审计）。详见 [docs/部署指南-Linux.md](docs/部署指南-Linux.md)；`deploy/linux/` 内的部署脚本仅经离线 shim 测试，尚未在真实 Docker 环境验证。

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

## Windows 版（桌面版，单用户免登录）
完整构建（安装包 + 便携 zip + 服务端 zip + 校验和，输出 `dist\release\<版本>\`）：
```powershell
powershell -ExecutionPolicy Bypass -File scripts\dist-windows.ps1 [-SkipTests]
```
- **安装版**：`oa-agent-<版本>-windows-x64-setup.exe`（当前用户/所有用户可选，可自选安装目录与归档工作目录）。
- **便携版**：`oa-agent-<版本>-windows-x64.zip`，解压后运行 `OA-Agent-Desktop.exe`，数据在程序旁 `data\`。
- **服务端版**：`oa-agent-<版本>-windows-x64-server.zip`（无 Electron，`start.bat` 启动，浏览器访问）；旧入口 `deploy\windows\build.ps1` 仍可单独生成该文件夹。
- 归档工作目录可随时在“设置 → 桌面版设置…”（用户菜单、设置页卡片或托盘图标）修改（迁移并校验，失败自动回滚）。
- 前置条件、各脚本说明、签名（可选，未签名会触发 SmartScreen 提示）、发布清单见 [docs/构建与发布.md](docs/构建与发布.md)，桌面外壳说明见 [desktop/README.md](desktop/README.md)。**在目标环境发布前请查看 [docs/THIRD-PARTY-LICENSES.md](docs/THIRD-PARTY-LICENSES.md)（PyMuPDF 为 AGPL）。**

## 关键设计
- **识别引擎可配置。** 设置页提供 DeepSeek（默认推荐）、通义千问等预设，Base URL 与模型名可自由修改，可拉取模型列表、测试连接（含工具调用检测）；配置存数据库、密钥 AES-GCM 加密（密钥文件在 `data/secret.key`，或 `OA_SECRET_KEY`），保存后热生效。换服务商不会沿用旧密钥；公网 http、链路本地地址一律拒绝。
- **OCR 抢救技能。** OCR 质量差时 Agent 按“技能”按需加载一套流程：评估质量 → 增强/旋转/纠偏/高清/二值化重试并择优 →（可选、需显式开启）视觉模型转写页面；对 OCR 常见混淆（O/0、l/1）的日期做容错；仍差则强制待审核。设计见 `docs/ocr-rescue-design.md`。
- **文档预览。** PDF/图片按页，Word/Excel 由 Worker 解析为结构化内容再在前端安全渲染（不使用 v-html）；覆盖导入原件与归档文件。
- **文件管理。** 浏览/预览/下载归档目录，新建文件夹、重命名/移动（不覆盖、同步台账）、删除到可恢复的回收站；Windows 本机可“在资源管理器中显示”（仅回环请求、仅限归档根目录内、不会运行文件；`OA_ALLOW_REVEAL=0` 关闭）。所有路径经 `internal/fsman` 校验，`.` 开头的内部目录不可见。
- **存储连接（可选）。** S3 兼容/Azure Blob/WebDAV（可经 OpenList 网关）/SFTP/FTP/SMB/钉钉钉盘；作为导入来源与归档副本的只增备份目的地；凭据加密、不覆盖、不删远端原文件；`OA_STORAGE=off` 关闭。设计见 `docs/storage-design.md`。
- **本机服务防护。** 跨站写请求与非本机 Host 头被拒绝（`CROSS_ORIGIN`/`HOST_NOT_ALLOWED`），避免网页脚本操控本地归档。
- **Agent 建议，Go 确定性代码执行。** LLM/规则只能通过只读、带 Schema 的工具读取当前文件；文件内容当作数据（防提示词注入）。路径规范化、冲突、写入全部在 `internal/archive`。
- **先预览、后审批、再执行。** 批准与执行是两个接口；执行带 `Idempotency-Key`，有日志（journal），可恢复、可回滚；原件只复制不移动，不覆盖，重名自动加序号。
- **证据不足就留空。** 无法确认的字段为 `null` 并要求人工确认；仅凭正文关键词推断的值也会要求确认。
- **PostgreSQL 为真相源**，Excel 只是导出。任务队列持久化，重启后自动续跑。

## 许可证

AGPL-3.0，见 `LICENSE`。第三方组件与改编来源（含 OpenList）见 `docs/THIRD-PARTY-LICENSES.md`。
