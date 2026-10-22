# 错误报告与诊断

目标：任何错误都有迹可循。后端进程被强杀、崩溃或某个后台协程 panic 时，管理员能在产品里看到发生了什么，而不是只能翻日志。

## 记录什么

| 来源 `source` | 触发 |
| --- | --- |
| `api` | 任何 5xx 响应（共享的 `fail` 路径）、处理器 panic（Gin 恢复中间件） |
| `pipeline` | 任务/文件的内部失败（不含加密、损坏、不支持、过大等用户错误）、任务循环 panic |
| `worker` | 文档解析 Worker 5xx/不可用、Worker 进程意外退出 |
| `llm` | 大模型调用失败（HTTP 状态与错误类别；不含提示词、密钥、回复） |
| `storage` | 存储副本同步的永久失败、存储协程 panic |
| `client` | 前端上报（`POST /v1/client-errors`） |
| `supervisor` | 上次运行异常结束；运行期致命错误 |
| `startup` | 启动阶段的致命错误 |

严重度：`error`、`panic`（含调用栈）、`fatal`（进程退出前写入）。

同一缺陷按**指纹**聚合：`来源 + 错误码 + 归一化消息 + 栈顶 3 帧`。归一化会把 UUID、时间戳、路径、十六进制、数字替换为占位符，前端压缩代码的构建哈希与行列号不参与指纹。每组保留次数、首次/最近时间、是否已处理，以及最近 20 次发生（时间、请求 ID、用户）。已处理的报告再次出现会自动重新打开。

报告错误编号 = 报告 ID 前 8 位；界面与任务/文件的失败原因里会显示“内部错误，错误编号 xxxxxxxx”，可在错误报告页搜索。

## 脱敏规则

写入数据库、日志、崩溃文件、诊断包之前统一经过 `diag.Redact`：

- API Key（`sk-…` 等）、`Bearer …`、`Authorization/Cookie/Set-Cookie` 头、`oa_session`、JWT、AWS/GitHub 令牌、PEM 私钥
- URL 中的口令、`password/api_key/secret/token/…` 键值对（JSON、表单、`k=v`、Go map 形式）
- 初始化令牌、TOTP 密钥、`otpauth://`、恢复码、长十六进制与 Base64 密钥
- 邮箱地址（`[EMAIL]`）、主目录里的用户名（`[USER]`）
- 启动时从环境读取的 `OA_WORKER_TOKEN`、`OA_SECRET_KEY`、`OA_LLM_API_KEY`、数据库口令按**原值**屏蔽

文档正文、文件内容、OCR 文本、提示词永远不进入报告；任务/文件相关的报告只带 ID、扩展名、大小等元数据。脱敏是模式匹配，不能保证万无一失，发送诊断包前请快速浏览。

## 不会拖垮调用方

- `diag.Report` 不阻塞、不 panic：内存队列（1024）+ 异步写库，同一指纹在内存中合并后批量写入；全局每秒 200 条上限；队列满则丢弃并计数。
- 数据库不可用：报告留在内存（最多 500 组）并写入 `data/logs/error-reports-fallback.jsonl`，库恢复后补写；每条错误同时写一行可读日志到 `oa-agent.log`（每个指纹 30 秒一条）。
- `diag.Go / Safe / Loop` 包装所有后台协程：panic 记为 `panic` 报告；长循环（任务队列、存储同步、清理）带退避重启；单个任务/文件 panic 只让该任务/文件失败并显示错误编号，进程继续运行。

## 文件在哪里（`data/logs/`，独立运行包为安装目录下的 `data\logs`；可用 `OA_LOG_DIR` 改）

| 文件 | 内容 |
| --- | --- |
| `oa-agent.log` | 后端日志（独立运行模式） |
| `worker.log`、`postgres.log` | 文档 Worker、内置 PostgreSQL 日志 |
| `crash-<UTC 时间>.txt` | 致命错误/主协程 panic：消息、调用栈、版本、OA_* 环境摘要（值已屏蔽） |
| `runtime-crash.txt` | Go 运行时致命错误（内存耗尽、并发写 map 等，无法被 recover）的转储；下次启动转存为 `crash-…-runtime.txt` |
| `run.lock` | 运行锁：`{pid, started_at}`，正常退出（信号、关闭、标准输入关闭）时删除 |
| `error-reports-fallback.jsonl` | 数据库不可用时的报告兜底 |

### 上次运行异常结束

启动时若发现 `run.lock` 属于已不存在的进程，说明上次被强杀或崩溃：记录一条 `supervisor` 报告“上次运行异常结束，可能被强制终止或崩溃”（附同期的 crash 文件名），`GET /v1/admin/system` 的 `last_exit_unclean` 保持为 true，直到管理员把这条报告标记为已处理。锁属于仍在运行的其它进程时只给出警告，不当作异常结束。

### 读崩溃文件

打开最新的 `crash-*.txt`：`kind` 为 `fatal`（启动/配置错误，`message` 即原因）或 `panic`；`stack` 的第一个本项目帧（`oa-agent/backend/...`）是出错位置；`environment` 只列 OA_* 变量名，敏感项显示 `[set]`。

## 发送诊断包

管理员：系统状态/错误报告页“下载诊断包”，或 `GET /v1/admin/diagnostics/bundle`（zip，总大小上限 20 MiB，操作写入审计 `diagnostics.bundle`）。包含：`VERSION.txt`、`environment.txt`、`config.json`（生效的非密钥配置；LLM 仅模式/模型/服务主机；存储仅连接种类）、`system.json`、`migrations.json`、`logs/*.log` 的尾部（脱敏）、`crash/`、`error-reports.json`、`README.txt`。不含密钥、令牌、密码、文档内容、数据库内容。把 zip 发给维护者即可。

## 保留策略

最多 5000 组（超出时先删已处理的最旧组，再删最旧的）；已处理超过 90 天的组自动删除；每组只留最近 20 次发生。清理随认证清理循环每 10 分钟执行一次。

## 接口

`GET /v1/admin/system`、`GET /v1/admin/error-reports`（`source/severity/resolved|state/q` 与分页；`state=all|open|resolved` 是 `resolved` 的别名）、`GET/PATCH/DELETE /v1/admin/error-reports/{id}`、`GET /v1/admin/diagnostics/bundle` 仅管理员；`POST /v1/client-errors` 任何已登录角色，每用户 20 次/分钟（429 + `Retry-After`），正文上限 8 KiB。所有 5xx/panic 响应带 `request_id`，并在 `details.report_id` 给出报告 ID。

## 测试用故障注入

`-tags faultinject` 构建才会包含 `GET /v1/test/fault?kind=panic|error|goroutine`（仅管理员），供 `cmd/e2e` 第 10 部分触发真实的服务器故障；普通/发行构建不含此端点。
