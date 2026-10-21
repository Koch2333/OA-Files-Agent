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
