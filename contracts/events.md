# 任务事件（SSE）协议 —— 已实现

路由：`GET /v1/tasks/{id}/events`，`Content-Type: text/event-stream`。

- 每条事件：`id: <n>`、`event: <event_type>`、`data: <AgentEvent JSON>`（字段见 openapi.yaml 的 `AgentEvent`）。
- `id` 是**任务内**持久化的单调递增整数（与状态变更同库保存）。客户端断线后带 `Last-Event-ID: <n>` 重连，只收到 `id > n` 的事件；非负整数以外的值返回 400；任务不存在返回 404。
- 服务器每 500 ms 查询新事件并推送；空闲时每 15 s 发送一行注释心跳 `: keepalive`。连接在客户端断开前保持打开（含已结束的任务）。
- 内容只含脱敏摘要：工具名、次数、字段名、文件显示名。**不含**文档正文、服务器路径、提示词、密钥。
- 认证：当前为本机单用户占位；接入 SSO/RBAC 时在同一路由加鉴权，事件内容不变。
- 保留：事件随任务保存在 `agent_events` 表，随任务删除而级联删除；没有单独的过期策略。

常见 `event_type`：`task_created`、`phase_started`、`started`/`turn`/`tool_call`/`finished`（Agent 循环，`phase=analyze`）、`file_classified`、`file_failed`、`plan_ready`、`plan_approved`、`archive_completed`、`archive_partial`、`cancel_requested`、`task_cancelled`、`task_retry`、`task_failed`、`task_finished`。客户端应忽略未知类型。
