# HTTP API 与契约

## 唯一契约源

[`contracts/openapi.yaml`](https://github.com/Koch2333/OA-Files-Agent/blob/main/contracts/openapi.yaml)（OpenAPI 3.1）是接口的唯一编辑源，涵盖导入、任务、归档计划、台账、模板与规则、文件管理、大模型配置、存储连接、登录与账号、管理、诊断等路由（100 多条路径）。SSE 事件协议见 [`contracts/events.md`](https://github.com/Koch2333/OA-Files-Agent/blob/main/contracts/events.md)。本页不重复规范，只讲怎么用。

运行中的服务提供：`/docs`（交互文档）、`/openapi.yaml`（契约）、`/healthz`、`/readyz`，例如 `http://127.0.0.1:8080/docs`。

修改契约：编辑 `contracts/openapi.yaml` → `python scripts/sync_contract.py`（同步 Go 内嵌副本）→ `python scripts/check_contracts.py`（可加 `--recorded <文件>`，用 `go run ./cmd/e2e -record` 录下的真实响应校验）→ 前端 `npm run gen:api`。

## 约定

| 项 | 约定 |
|---|---|
| 成功 | `{"data": ...}`；列表为 `{"data": {"items","total","page","page_size"}}` |
| 错误 | `{"error": {"code","message","request_id","details?"}}` |
| 审批与执行 | 两个独立接口；执行必须带 `Idempotency-Key` |
| 乐观并发 | 修订与批准带 `version`，冲突返回 409 |

横切错误：

| 状态 | 代码 | 含义 |
|---|---|---|
| 401 | `UNAUTHENTICATED` | 未登录 |
| 403 | `FORBIDDEN` | 角色不足 |
| 403 | `CSRF_INVALID` | CSRF 校验失败 |
| 403 | `CROSS_ORIGIN` | 跨站写请求（Origin / Sec-Fetch-Site 不同源） |
| 403 | `HOST_NOT_ALLOWED` | 仅监听本机时 Host 不是 localhost / 127.0.0.1 / ::1（防 DNS 重绑定） |
| 409 | `WIZARD_REQUIRED` | 初始化向导尚未完成 |
| 503 | `SETUP_REQUIRED` | 尚未初始化（除 `/v1/setup/*`、`/healthz`、`/readyz`） |

## 认证

- `enterprise` 模式必须登录：会话 Cookie `oa_session`（HttpOnly）+ 非 GET 请求带 `X-CSRF-Token`（取自登录 / `GET /v1/auth/me` 响应）。设计见[登录与账号管理设计](/auth-design)。
- `local` 模式（Windows 桌面版）本机单用户免登录。
- 每条路由的最小角色在 `backend/internal/api/authz.go` 的单一路由表里（默认拒绝，有测试覆盖每条路由）：管理员 > 操作员 > 只读。
- 登录授权回调 `GET /v1/llm/oauth/callback` 是唯一刻意公开的、不需要会话的业务路由。

## API 代数（`api_level`）

`GET /v1/setup/status` 返回 `api_level`，即后端实现的契约代数，对应 Go 常量 `api.APILevel`（`backend/internal/api/setup.go`，当前为 **8**）。前端带同一个数字（`frontend/src/utils/apiLevel.ts` 的 `REQUIRED_API_LEVEL`，当前也是 8）；前端依赖新接口或字段时必须**同时递增**两处，不一致时界面显示“版本不匹配”横幅。`server_version` 是产品版本（当前 0.5.0）。注意 `info.version: 1.0.0` 是契约文档自己的版本，不是产品版本。

## 任务事件（SSE）

`GET /v1/tasks/{id}/events`，`text/event-stream`。事件 `id` 是任务内单调递增整数；断线后带 `Last-Event-ID` 重连只会收到更新的事件；空闲时每 15 秒一条 `: keepalive` 心跳。内容只含脱敏摘要（工具名、次数、字段名、文件显示名），不含正文、服务器路径、提示词、密钥。客户端应忽略未知事件类型。

## 导入相关路由

| 路由 | 用途 |
|---|---|
| `POST /v1/imports` | 一次性 multipart（无上限，流式落盘） |
| `POST /v1/imports/uploads` → `PUT …/{id}?offset=N` → `POST …/{id}/complete` → `POST /v1/imports/batches/{batch}/commit` | 可续传分片上传；`offset` 必须等于服务器已收到的长度，否则 409 并返回当前值 |
| `POST /v1/imports/from-paths` | 本机路径导入，仅 `local` 模式且回环请求，否则 403 `LOCAL_ONLY` |
| `GET /v1/imports/config` | 当前上限与能力 |
| `POST /v1/tasks/{id}/retry` | 只重新识别失败的文件 |
| `POST /v1/archive-plans/{id}/approve` / `…/execute` | 批准 / 执行（`Idempotency-Key`） |
| `POST /v1/archive-operations/{id}/rollback` | 撤销一次归档（仅删除本次发布且未被改动的文件） |

详见[大文件与批量导入](/大文件与批量导入)。

## 本机服务防护

跨站写请求与非本机 Host 头被拒绝，避免网页脚本操控本地归档。CORS 允许名单只接受精确来源，绝不使用 `*`（`OA_CORS_ORIGINS`）。
