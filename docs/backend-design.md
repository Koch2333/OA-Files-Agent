# 后端实现设计（接手版）

约定：所有路径为 Windows 风格（Go 与 Python 均为 Windows 进程）。Go 通过 `go.exe`（`/mnt/c/Program Files/Go/bin`），Python 为 `C:\Users\14823\AppData\Local\Programs\Python\Python312\python.exe`。
PostgreSQL 16 在 WSL 运行于 `127.0.0.1:5432`，用户 `oa`，信任认证，库 `postgres`；测试 DSN：`postgres://oa@127.0.0.1:5432/postgres?sslmode=disable`。

## 包分工
| 包 | 职责 |
|---|---|
| `workers/document-ai` | Python 解析/OCR 进程（见下 Worker 协议） |
| `internal/harness` | Agent 循环、Tool Registry、Provider（OpenAI 兼容 + 启发式离线）、模板匹配 |
| `internal/archive` | 命名/目录模板渲染、路径策略、冲突处理、暂存+原子发布、回滚（仅标准库） |
| `internal/store` | PostgreSQL 仓储（任务/文件/计划/台账/模板/规则/审计/事件） |
| `internal/pipeline` | 持久化任务队列 + 有界 worker pool，驱动 scan→extract→analyze→plan，断点恢复 |
| `internal/api` | HTTP 路由、统一错误、SSE、Excel 导出 |

## Worker 协议（回环 + Bearer token，`OA_WORKER_TOKEN`）
`GET /healthz` → `{"data":{"status":"ok","extraction_available":bool,"ocr_available":bool}}`

`POST /v1/extract`，JSON：`{"path":"<绝对路径，必须位于 OA_SPOOL_ROOT 之内>","filename":"原名","mime_type":"...","ocr":true}`
成功 200：
```json
{"data":{"text":"全文(截断至 200000 字符)","pages":[{"page":1,"text":"...","source":"native|ocr","confidence":0.93}],
 "ocr_used":true,"truncated":false,"warnings":["..."],"metadata":{"title":null,"author":null,"created":null,"modified":null}}}
```
错误统一 `{"error":{"code","message"}}`：401 UNAUTHORIZED；400 INVALID_REQUEST；403 PATH_NOT_ALLOWED；404 FILE_NOT_FOUND；413 FILE_TOO_LARGE；422 UNREADABLE_FILE / ENCRYPTED / UNSUPPORTED_FORMAT；503 OCR_UNAVAILABLE；504 EXTRACT_TIMEOUT。
解析在子进程中执行并有超时，崩溃不影响服务。日志不得包含正文或路径。

## harness 接口（Go）
```go
package harness
type Extractor interface { Extract(ctx context.Context, req ExtractRequest) (ExtractResult, error) } // 由 worker 客户端实现
type Template struct { ID, Name string; Enabled bool; Priority int; FilenameKeywords, TextKeywords []string; Department, DocumentType, DateSource string }
type FileInput struct { FileID, SourceName, SourcePath, MIMEType, StoredPath string }
type Result struct { Department, DocumentType, DocumentDate *string; Evidence []string; ReviewRequired bool; TemplateID *string; Trace []ToolCall; Warnings []string }
func (a *Agent) AnalyzeFile(ctx context.Context, f FileInput, tpls []Template, emit func(Event)) (Result, error)
```
`DocumentDate` 为 `YYYY-MM-DD`；证据不足返回 nil 且 `ReviewRequired=true`，禁止编造。

## archive 接口（Go）
```go
package archive
type Rule struct{ NamingPattern, DirectoryPattern string } // 占位符 {date}{year}{month}{day}{department}{type}{original}{stem}{ext}
type Meta struct{ Department, DocumentType, Date *string }
func Render(rule Rule, original string, m Meta) (name, dir string, warnings []string, err error)
type Op struct{ FileID, SourceStored, DestRel string }
type Executor struct{ Root string } // 归档根目录
func (e *Executor) Execute(ctx, ops []Op) []OpResult
func (e *Executor) Rollback(ctx, results []OpResult) error
```

## AI 工作台：全局/个人大模型、任务说明、全自动归档（migration 008）
**配置与解析**：`/v1/llm/config|test|models|presets` 仍是**全局**配置（管理员）。个人配置存 `llm_personal`（每用户一行，`user_id` 为 TEXT：local 模式的内置用户不在 users 表；密钥 AES-GCM 加密，永不返回，hint/`api_key_set` 语义同全局）。管理员策略在单行表 `ai_policy`（默认：个人 API 关、个人内网地址关、全自动开），关闭个人 API 不删除个人配置，只是不再使用（任务回落全局，`/v1/llm/personal*` 返回 403 `PERSONAL_LLM_DISABLED`；DELETE 始终允许）。
个人 Base URL：默认只允许 https + 公网主机（字面 IP、`localhost`、单标签名、`.local/.internal/.lan…` 以及解析到非公网地址的主机名均拒绝）；策略放开后与全局规则相同；链路本地/元数据地址**永远**拒绝。个人请求走专用 HTTP 客户端：拨号时（`net.Dialer.Control`）按解析后的 IP 再检查（防 DNS 重绑定），不跟随重定向，不使用环境代理，策略收紧后立即对已保存配置生效。个人 test/models 每用户 10 次/分钟（429 + Retry-After，拒绝的请求也计数）。
`source=auto`：用户个人配置（策略允许、角色≥operator、mode=llm 且状态 ok）→ 全局（mode=llm 且 ok）→ `none`（离线规则）。`GET /v1/llm/status` 汇总，不含密钥/Base URL（provider_label 仅为预设名）。

**任务绑定**：创建任务时把 `instruction/automation/llm_source/llm_owner/llm_model/auto_ack_*` 写入 `import_jobs`；后台 worker 没有 HTTP 用户，`pipeline.process` 用 `llm.Manager.TaskProvider(source, owner)` 取模型：none→离线 Heuristic；global→当前全局 Switch（热切换照常生效，若全局已不是可用 LLM 则失败）；personal→按 owner 的**最新**已存配置现建 provider（模型名变化会回写 `llm_model`）。**不做跨来源回落**：个人配置被删/策略关闭/不可用时，该任务的文件以明确原因失败，并调用 `reportError`（`pipeline.ErrorReportHook`，由诊断模块接线）。
任务说明 `instruction`：≤2000 字；仅在解析来源不是 none 时允许（否则 422 `LLM_REQUIRED`）。它作为系统提示中独立的 `【用户任务说明】` 块（`<<<USER_INSTRUCTION id=nonce>>>`，伪造的分隔符被替换）送给模型，声明来自已登录操作员，只能影响部门/类型/日期的倾向或要求跳过某类文件，不能授予工具、改路径/规则、关闭安全检查；文档文字仍是 DOCUMENT_DATA。真正写盘的仍是 Go 的 validate/路径/命名/冲突逻辑。

**全自动（automation=auto）服务端强制**（`api/ai.go taskAI`，在落盘前）：角色≥operator；策略 `allow_auto_archive` 否则 403 `AUTO_ARCHIVE_DISABLED`；解析来源必须是 mode=llm 否则 422 `LLM_REQUIRED`；`options.auto_ack={version==llm.AutoAckVersion, accepted:true}` 否则 422 `AUTO_ACK_REQUIRED`。风险文本由服务端常量 `llm.AutoAckText` 提供（经 `LlmStatus.auto_ack_text`）。任务记录 ack 版本/时间/用户，审计 `ai.auto_archive.accepted`（用户、任务、模型、版本，无密钥；幂等重放不重复记）。
**规划后的行为与余项处理**：`buildPlan` 照常生成条目；自动任务把 `review_required` 的条目（含“与已归档文件内容相同”——重复导入属人工决定）与可确定条目拆开：`store.CreateAutoPlans` 在一个事务里写入 ①已批准的自动计划（`approved_by=ai-auto:<username>`、`requires_approval=false`，只含完全确定的条目）和 ②（有余项时）一个新的 `awaiting_review` 计划，只含余项；任务的 `plan_id` 指向②（没有余项则指向①）。随后 `pipeline.runAuto` 以幂等键 `auto:<taskID>`、actor `ai-auto:<username>` 执行①：操作与台账行记 `decision=ai_auto` + `decision_model`，审计 `plan.execute` 与 `ai.auto_archive.executed`，SSE 事件 `auto_plan_ready / auto_archive_started / archive_completed(automation=auto)`。结束状态：无余项 `completed`（有导入失败则 `partial_failure`），有余项 `awaiting_review`；②计划的 `operation_id` 指向自动操作（供“撤销本次全自动归档”），人工修订并批准②后按普通人工流程执行（decision=manual）。没有任何可确定条目时不拆分，事件 `auto_nothing_ready`，任务落入普通 `awaiting_review`。绝不为了放行而补字段。①执行期间禁止批准②（409）。回滚沿用原机制并审计；回滚后任务回到 `awaiting_review`（有余项）或 `approved`，启动恢复只会重跑“没有任何操作记录”的已批准自动计划，因此不会重放已回滚的计划。崩溃恢复：`RecoverAutoPlans`。
**台账/导出**：`ledger_entries` 与 `archive_operations` 增加 `decision/decision_model`（旧行 manual）；`GET /v1/ledger` 与导出支持 `decision=manual|ai_auto` 筛选；Excel 末列“决策方式”：“人工审核”/“全自动（模型：xxx）”。
