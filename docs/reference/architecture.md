# 架构

## 组件

| 组件 | 技术 | 职责 |
|---|---|---|
| 前端 | Vue 3 + TypeScript + Vite，Material Design 3 | 界面；类型由 `contracts/openapi.yaml` 生成；构建产物可由 API 直接提供 |
| 桌面外壳 | Electron（`desktop/`） | 仅 Windows：启动画面、启动 / 停止本机后端、加载界面、托盘、工作目录管理、应用内密钥窗口 |
| API 与调度 | Go（Gin 只做路由、恢复与 CORS，处理器是 `net/http`） | HTTP / SSE、登录与权限、持久化任务队列、归档执行与回滚 |
| 文档 Worker | Python 3.12（python-docx、openpyxl、PyMuPDF、RapidOCR） | 解析 Word / Excel / PDF 与图片 OCR、预览渲染；子进程隔离，有超时 |
| 数据库 | PostgreSQL（默认）/ MySQL 8.0+ / SQL Server 2019+ | 真相源：任务、文件、计划、台账、审计、事件 |
| 反向代理（可选） | Caddy | 企业版自动 HTTPS |

## 数据流

```
浏览器 / Electron 界面
   │  HTTP + SSE（Cookie 会话 + CSRF；local 模式免登录）
   ▼
Go API ──► 导入（分片 / multipart / 本机路径）──► 暂存区 spool（SHA-256，原子落盘）
   │
   ├─► 持久化任务队列 ──► scan → extract → analyze → plan
   │                         │          │
   │            回环 + Bearer │          └─ Agent 循环：只读、带 Schema 的工具 ──► 离线规则 / 大模型（OpenAI 兼容）
   │                         ▼
   │                  文档 Worker（解析 / OCR，只读 spool）
   ▼
归档计划（待审批）──► 批准 ──► 执行（Idempotency-Key、journal）──► internal/archive：命名、冲突、暂存 + 原子发布
   │                                                                    │
   ▼                                                                    ▼
数据库（台账、审计、事件）◄──────────────────────────────── 归档根目录（原件只复制，不覆盖）
```

要点：

- **Agent 建议，Go 确定性代码执行。** 模型只能通过只读工具读取当前文件；路径规范化、冲突处理、写入全部在 `internal/archive`。模型没有 shell，没有写权限；文档正文是数据，不是指令。
- **先预览、后审批、再执行。** 批准与执行是两个接口；执行带 `Idempotency-Key`，有日志，可恢复、可回滚。
- **证据不足留空。** 无法确认的字段为 `null` 并要求人工确认。
- **任务可恢复。** 队列持久化，重启后自动续跑；SSE 事件随任务保存在库里，支持 `Last-Event-ID` 断点重连。
- **Worker 不对外。** API 与 Worker 之间用回环 http + `OA_WORKER_TOKEN`；Worker 只读取 `OA_SPOOL_ROOT`（及显式配置的 `OA_ALLOWED_ROOTS`）下的文件。Docker 中二者共用网络命名空间，Worker 不发布端口。

## 部署形态

| | Windows 桌面 / 独立包 | Linux 企业版 |
|---|---|---|
| 进程 | Electron → `oa-agent.exe`（独立启动器）→ 管理文档 Worker 与内置 PostgreSQL | Compose：`postgres`（不发布端口）、`api`（唯一发布端口）、`worker`、可选 `caddy` |
| 模式 | `local`，免登录 | `enterprise`，多用户、MFA、审计 |
| 容器加固 | — | 非 root、`no-new-privileges`、`cap_drop: ALL`、只读根文件系统、日志轮转、内存上限 |
| 管理 | “桌面版设置…”窗口 | `oa-agentctl`、`oa-admin` |

## Go 后端包

| 包 | 职责 |
|---|---|
| `api` | HTTP、SSE、Excel 导出；`authz.go` 是唯一的“路由 → 最小角色”表，默认拒绝 |
| `auth` | 登录与账号：argon2id、TOTP、恢复码、会话 / CSRF、限流 / 锁定、WebAuthn、初始化向导 |
| `store` / `sqldb` / `dbconfig` / `dbadmin` | 仓储与多数据库方言、连接配置、库结构的初始化与批准 |
| `pipeline` | 持久化队列、规划、执行与回滚 |
| `harness` | Agent 循环、工具、提供商（含重试与错误分类）、规则、OCR 抢救技能 |
| `archive` | 命名、冲突、原子发布、回滚（仅标准库） |
| `imports` | multipart / 分片接收 |
| `llm` / `secrets` | 引擎预设、热切换、OpenRouter OAuth；API Key 的 AES-GCM 加密 |
| `fsman` / `fileops` | 安全的文件管理路径与回收站；与台账同步 |
| `cloud` / `storage` | 可选存储连接（s3、azure_blob、webdav、sftp、ftp、smb、dingtalk 等）与只增备份复制 |
| `diag` | 错误报告、脱敏、崩溃文件 |
| `supervisor` | 独立模式下管理 Worker 与内置 PostgreSQL |

命令：`cmd/oa-agent`（服务与独立启动器）、`cmd/migrate`、`cmd/oa-admin`（应急命令行）、`cmd/oa-agentctl`（Docker 部署管理）、`cmd/e2e`（端到端验收）、`cmd/contract-samples`。

延伸阅读：[后端实现设计](/backend-design)、[登录与账号管理设计](/auth-design)、[存储连接设计](/storage-design)、[OCR 抢救技能与文档预览设计](/ocr-rescue-design)、[多数据库兼容设计](/多数据库兼容-设计)。
