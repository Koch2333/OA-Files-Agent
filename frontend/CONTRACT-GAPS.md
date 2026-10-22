> **状态：以下缺口已全部由后端实现并写入 `contracts/openapi.yaml`（2026-10-22）。本文件仅保留为历史记录；实际字段名以契约为准（与此处的假设有出入，例如导入使用 `paths` 清单、台账字段见契约）。**

# 前端 → 后端：契约缺口清单

依据：`contracts/openapi.yaml` v0.0.3（仅冻结 `/healthz`、`/readyz`；`ImportJob`/`FileRecord` 为草案）与 `contracts/events.md`（草案，明确“不可据此联调”）。
前端类型目前手写于 `src/api/types.ts`（来源 `04-接口契约规范.md`），契约冻结后用 openapi-typescript 替换。
`src/api/real.ts` 中标 `[GAP]` 的路径是前端提议，**不是**已有契约。请 Codex 先更新 OpenAPI，再通知前端对齐。

## 需要补充的接口 / 字段

| # | 缺口 | 前端用途 |
|---|---|---|
| 1 | `POST /v1/imports` 的请求格式：multipart 字段名（现假定 `files` + 平行的 `relative_paths` + 可选 `template_id`）、单批大小/数量上限、413 行为 | R01 导入 |
| 2 | 所有业务接口的响应包裹（现兼容 `{data}` 与裸对象）与分页结构 `{items,total,page,page_size}` | 列表页 |
| 3 | `ImportJob` 缺 `plan_id`（或 `GET /v1/archive-plans?task_id=`） | 任务 → 审批页跳转 |
| 4 | `GET /v1/files?task_id=` 过滤；`FileRecord.error`（单文件解析失败原因）、`archive_status` 与失败文件的关系 | R02 格式错误提示 |
| 5 | `ArchivePlan.status` 枚举（现假定 draft/awaiting_review/approved/executing/executed/failed）；approve/patch 请求体（现假定 `{version, items[]}`） | 审批流 |
| 6 | 计划条目冲突表示：重名/路径冲突是 `warnings[]` 文本还是结构化码 | R07 冲突提示 |
| 7 | `LedgerEntry` 字段名（现假定 original_name/canonical_name/department/document_type/document_date/archive_path/sha256/task_id/executed_at）；`/v1/ledger` 的 `q`/`department`/`type`/`date_from`/`date_to`/`sort`/`order` 参数；`/v1/ledger/export` 是否接受同样筛选并返回 `Content-Disposition` | R08–R10 |
| 8 | `OATemplate.match_rules` / `field_mappings` 的结构（现假定 `{filename_keywords[], text_keywords[]}` 与 `{department, document_type, date_source}`）；`POST …/test` 入参与返回 | R03 |
| 9 | `ArchiveRule` 模型（现假定 name/naming_pattern/directory_pattern/enabled）及更新/删除 | 命名规则 |
| 10 | 任务重试接口（现无，前端按钮禁用）；`POST /v1/tasks/{id}/retry` | 任务中心 |
| 11 | SSE：认证方式、事件 `event_type` 枚举、保留时长；断线后 `Last-Event-ID` 重放行为 | 实时轨迹 |
| 12 | 模型 Provider 状态接口（仅名称/模型/可用状态，不含密钥） | 设置页（现显示“契约未提供”） |
| 13 | `GET /v1/audit-logs` 条目字段 | 设置页 |
| 14 | 纠错 `correction-proposals` 的请求/响应结构（现假定含 `changes[{field,before,after}]` 与 `impact`） | 纠错抽屉 |
| 15 | `/readyz` 503 时前端想读取 `dependencies`；当前 `json()` 对非 2xx 丢弃了响应体，需契约确认 503 体即为 `Readiness` | 设置/工作台 |
| 16 | 文件夹导入：Windows 本机目录授权的启动器协议 | R01 本机目录场景 |
| 17 | `.doc` / `.xls` 旧格式是否支持（现仅在界面提示“需转换”） | R02 |

## 前端已遵守的语义
- 审批与执行是两个按钮；未批准时“执行”禁用，执行带 `Idempotency-Key`。
- PATCH/approve 携带 `version`，409 时自动刷新最新版本。
- 无法确认的字段显示“待审核”，不虚构。
- 相对路径在前端先做规范化与 `..` 拒绝（后端仍须校验）。

## 登录与账号：前端假定（超出契约文字的部分，待真实后端验证）
1. 没有公开的“登录策略”接口：`Policy` 只随 `/auth/me` 返回，登录页在登录前无法得知 `allow_password_login`，因此同时显示密码表单与 Passkey 按钮（`SetupStatus.passkey_available` 用于 Passkey 可用性）。
2. “需要近期认证”的信号契约未明确：按设计文档假定 `403` 且 `error.details.reason == 'reauth_required'`（兼容 code `REAUTH_REQUIRED`；或 `FORBIDDEN` 且 `me.fresh=false` 且无其他 reason）。
3. `RATE_LIMITED`（429）读取 `Retry-After` 头（秒或 HTTP 日期）；`ACCOUNT_LOCKED`（423）可选读取 `details.locked_until`。
4. 邀请 `InviteCreated.path` 为 `/invite/<token>`，前端用 `location.origin` 拼完整链接。
5. 修改 RP ID/来源缺少确认时，后端回 409/422 均按普通错误显示（前端已强制勾选确认并发送 `confirm_passkey_reset`）。
6. 向导：`complete` 在“域名与策略”步骤末尾调用；`needs_token=false` 时令牌可选填（契约写 enterprise 必须带令牌，与 `needs_token` 描述有歧义，故允许填写）。local 模式只发送 `{mode}`（可带令牌）。
7. local 模式下 `GET /v1/auth/me` 返回内置用户（admin，`mfa=local`），前端不显示退出登录；`fresh` 视为有效。
8. `enroll_mfa` 受限会话可调用 `/auth/totp/*`、`/auth/passkeys/*`、`/auth/security`、`/auth/sessions`，并且绑定成功响应的 `me` 带新的 `scope=full`。
9. `SessionInfo.mfa`、`AdminUser.status` 等枚举按契约原样显示。`GET /v1/admin/users?page_size` 最大 200（会话页用于筛选下拉）。

## AI 工作台 / 运行诊断：前端假定与契约缺口（待真实后端验证）
**契约缺口（未改契约，前端已绕开）**
1. `LedgerEntry.decision` 有，但 `GET /v1/ledger` 与 `/v1/ledger/export` 没有 `decision` 过滤参数：前端发送 `decision=ai_auto`（仅“仅全自动”开关打开时），并在客户端再过滤当前页；若后端忽略该参数，总数与导出会包含全部记录。建议在契约中增加该参数。
2. `FileRecord` 没有决策字段：文件库/任务页的文件详情用所属任务的 `automation` 推导“全自动 · 模型”。
3. `SystemInfo` 没有“异常退出对应的错误报告 ID”：横幅链接到 `/admin/diagnostics?source=supervisor&state=all`。
4. `ImportJob` 没有全自动结果汇总（已归档/留待审核数）：前端由 `listTaskFiles`（`archive_status`、`review_required`）与 `getPlan`（`items` = 留待审核项、`operation_id`）推导。
5. `LlmStatus` 没有 `allow_personal_private_hosts`：个人配置无法在前端预判“更严格地址规则”，只能依赖服务端 422。
6. `ErrorReport.id` 是 string，而路径参数 `Id` 声明为 uuid：前端按不透明字符串处理并 `encodeURIComponent`。
7. `openapi-typescript` 把带默认值的 `ImportOptions.automation/llm_source` 生成为必填，前端用 `Partial<>`。
8. 非管理员无法读取 `GET /v1/admin/ai-policy`，设置页对他们只依赖 `LlmStatus.personal_allowed`。

**行为假定（超出契约文字）**
1. 全自动运行结束后：已归档文件 `archive_status=archived`；留待人工的文件仍为 `pending` 且 `review_required=true`（或仍在计划 `items` 中）；`ArchivePlan.items` 只剩留待审核项，`operation_id` 为本次自动操作（用于“撤销本次全自动归档”）；有留待项时任务状态为 `awaiting_review`，否则 `completed`/`partial_failure`。撤销后条目回到待审核计划。计划页“撤销本次归档”改为只要 `operation_id` 存在就显示（不再要求 `status=executed`）。
2. `GET /v1/llm/status` 对所有已登录角色可用；`GET /v1/llm/presets` 对操作员可读（个人配置卡片需要）；`GET /v1/llm/config` 只由管理员调用。`GET /v1/llm/personal` 在未配置时返回 `LlmConfig`（`mode=heuristic`、`state=unconfigured`），不是 404。
3. `/settings` 路由降为 `operator`：后端需允许操作员访问 `/v1/llm/status`、`/v1/llm/presets`、`/v1/llm/personal*`；`/v1/admin/*` 仍仅管理员。
4. 错误码的 HTTP 状态：403 `AUTO_ARCHIVE_DISABLED` / `PERSONAL_LLM_DISABLED`，422 `AUTO_ACK_REQUIRED` / `LLM_REQUIRED`（`error.code`）。个人配置的地址违规是 422；测试/模型列表被限速是 429（`Retry-After` 秒）。
5. 前端从不发送 `ImportOptions.llm_source`（保持 `auto`）；`instruction` 仅在已配置大模型时发送；全自动一律带 `auto_ack={version: LlmStatus.auto_ack_version, accepted: true}`。
6. `POST /v1/client-errors` 以 `silent` 方式调用（401 不触发登录跳转），不发送 `request_id`；返回 `{report_id}` 原样展示为“编号”。仅已登录时上报。
7. 诊断包用 `fetch` + `Blob` 下载（整包在内存中），文件名取自 `Content-Disposition`，缺省 `oa-diagnostics.zip`。
8. 后端不可用判定：`fetch` 本身失败（连接被拒绝等）；恢复探测是 `GET /healthz`（公开，无需会话），每 5 秒一次。
9. 安装向导的“大模型”一步在绑定 MFA 之后（此时会话为 full），调用 `PUT /v1/llm/config`（全局）、`/v1/llm/presets|test|models`，若开启“允许员工配置个人 API”则 `PATCH /v1/admin/ai-policy {allow_personal_llm:true}`；本机（local）模式向导无此步。
10. 全自动确认只保存在页面内存（不落任何存储）；`auto_ack_version` 变化后需重新确认。
