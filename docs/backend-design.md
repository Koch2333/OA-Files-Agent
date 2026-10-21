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
