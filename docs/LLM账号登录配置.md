# 用平台账号登录授权，自动配置 API Key

除了在“设置 → 识别引擎”里手动粘贴 API Key，OA Agent 还支持“**使用 OpenRouter 账号登录授权**”：在服务商官方页面登录并点“授权”后，服务商为本应用生成一把专用 API Key，由服务端加密保存并自动测试连接。

> 原则：只使用服务商**公开提供给第三方应用**的授权流程。不抓取登录页、不自动化浏览器登录、不保存用户密码、不复用其他应用（Claude Code、ChatGPT、Copilot CLI 等）的订阅令牌。没有合规流程的平台，界面只给“打开密钥页面”链接和手动粘贴。

## 1. 调研结论（核查日期 2026-10-24）

| 平台 | 流程类型 | 状态 | 原因 | 来源 | 核查日期 |
|---|---|---|---|---|---|
| OpenRouter | OAuth 授权码 + PKCE（S256），兑换后返回用户专属 API Key | **implemented** | 官方文档面向第三方应用；本机回调任意端口可用；无需客户端密钥；授权码单次有效，10 分钟过期 | https://openrouter.ai/docs/use-cases/oauth-pkce | 2026-10-24 |
| Anthropic（Claude API） | 无 | **not possible** | 开发者须用 Console 的 API Key（或云厂商凭据 / Workload Identity Federation）。Claude.ai 账号的 OAuth 仅限 Claude Code 与 Anthropic 自有应用；条款明确“不允许第三方提供 Claude.ai 登录，也不得代用户走 Free/Pro/Max 凭据，不得收集或转手 Claude.ai 凭据” | https://code.claude.com/docs/en/legal-and-compliance ；https://platform.claude.com/docs/en/api/overview | 2026-10-24 |
| OpenAI | 无 | **link-only** | 官方只有 API Key（平台页创建）。“Sign in with ChatGPT”仅限 Codex 工具，是身份/订阅登录，不向第三方应用发放 API Key；未找到面向第三方的 Key 发放流程（官方文档页对自动抓取返回 403，结论基于搜索结果与官方公开说明，未能逐页核实） | https://platform.openai.com/api-keys ；https://techcrunch.com/2025/05/27/openai-may-soon-let-you-sign-in-with-chatgpt-for-other-apps/ | 2026-10-24 |
| Google Gemini | OAuth 2.0（令牌，不是 API Key） | **link-only**（不实现） | 官方 OAuth 只产出访问令牌，需要使用者自建 Cloud 项目、同意屏幕、OAuth 客户端；文档称 API Key 是“最简单方式”，OAuth 用于更严格的访问控制，快速入门仅适合测试。本应用走 OpenAI 兼容 `/chat/completions` + Bearer Key，令牌流程既无客户端 ID 可发布，也会引入需验证的 Google 应用审核，不值得 | https://ai.google.dev/gemini-api/docs/oauth | 2026-10-24 |
| DeepSeek | 无 | **link-only** | 文档只有 Bearer API Key，未提及 OAuth；Key 在平台创建 | https://api-docs.deepseek.com/api/deepseek-api ；密钥页 https://platform.deepseek.com/api_keys | 2026-10-24 |
| 通义千问 / 阿里云百炼 | 无 | **link-only** | 官方说明仅通过控制台创建 API Key，无第三方授权登录 | https://help.aliyun.com/zh/model-studio/get-api-key ；密钥页 https://bailian.console.aliyun.com/cn-beijing/model/settings/api-key | 2026-10-24 |
| Moonshot / Kimi | 无（开放平台） | **link-only** | 开放平台用 API Key；Kimi Code 会员登录是其自有 CLI 的流程，不是面向第三方的 Key 发放（来源为第三方指南，未见官方说明有此流程） | https://platform.kimi.com/docs/guide/start-using-kimi-api | 2026-10-24（未逐页核实） |
| 智谱 BigModel / GLM | 无 | **link-only** | 快速入门只展示 Bearer API Key，未提及第三方授权 | https://docs.bigmodel.cn/cn/guide/start/quick-start ；密钥页 https://bigmodel.cn/usercenter/proj-mgmt/apikeys | 2026-10-24 |
| SiliconFlow（硅基流动） | 无 | **link-only** | 文档只描述 API 密钥，未提及第三方应用授权 | https://docs.siliconflow.cn/cn/userguide/quickstart ；密钥页 https://cloud.siliconflow.cn/account/ak | 2026-10-24 |
| Ollama（本机） | 不适用 | **not possible** | 本机服务，无账号、无需密钥 | https://ollama.com/ | 2026-10-24 |

说明：
- “link-only”＝本应用内置预设里提供“打开 API Key 页面”链接（预设字段 `key_url`），并写明“该平台没有面向第三方应用的登录授权接口”；手动粘贴始终可用。目前内置预设：DeepSeek、通义千问、OpenAI、Ollama、OpenRouter、自定义；未内置预设的平台（Anthropic、Gemini、Kimi、智谱、SiliconFlow）可用“自定义（OpenAI 兼容）”手动填写。
- 以上结论来自公开文档与搜索，各家政策变化很快；标注“未逐页核实”的条目请以官方最新说明为准。**没有用任何真实账号做过登录验证。**

## 2. 工作原理（OpenRouter）

1. 前端调用 `POST /v1/llm/oauth/start {provider, scope, client}`。服务端生成：不可猜测的 `state`（256 位）、PKCE `code_verifier`（仅保存在服务端内存，永不返回）、`code_challenge = BASE64URL(SHA256(verifier))`，记录发起者用户、范围（`personal` 个人 / `global` 全局，全局仅管理员）、10 分钟有效期、单次使用。返回 `authorize_url` 与 `state`。
2. 浏览器到 `https://openrouter.ai/auth?callback_url=…&code_challenge=…&code_challenge_method=S256&state=…&key_label=OA Agent` 登录并授权。
3. OpenRouter 重定向到 `callback_url`（`GET /v1/llm/oauth/callback?code&state`）。该路由**无需登录**（会话 Cookie 是 `SameSite=Strict`，从其他站点跳回时不会携带），只做一件事：为未用过、未过期的 `state` 记录授权码。回调页不回显任何输入。
4. 前端轮询 `GET /v1/llm/oauth/status?state=…`。**只有发起者本人的会话**能读到该流程；服务端在此时用授权码 + `code_verifier` 向 `https://openrouter.ai/api/v1/auth/keys` 兑换 Key（只兑换一次），用与手动粘贴相同的 AES-GCM 存储，设置 OpenRouter 预设（Base URL、默认模型；重新连接时保留已选模型），并自动执行“测试连接”，结果在状态里返回。
5. 断开：`POST /v1/llm/oauth/disconnect {scope}` 删除保存的密钥（仅限登录获得的密钥）。本应用**无法**代为吊销 Key（OpenRouter 的授权接口没有提供），请到 https://openrouter.ai/settings/keys 删除。

### 安全要点
- PKCE S256，无客户端密钥；verifier 与授权码只在服务端；`state` 单次有效、10 分钟过期、按发起用户绑定；重放得到“已使用”。
- 密钥永不出现在任何响应、日志、审计、诊断报告中（既有 `sk-…` 脱敏规则已覆盖 `sk-or-v1-…`，并有测试）。审计只记录“发起 / 连接 / 断开 + 平台 + 范围”。
- 密钥来源标记（`key_source: manual | oauth`）放在被加密的数据内部（以控制字符开头，手填密钥不可能冒充），无需改表结构。
- 待处理流程只存在内存：重启会取消正在进行的授权，重新点击即可；每用户同时最多 5 个、全局 500 个。
- 个人流程受“AI 使用策略”约束（管理员未开放个人配置时被拒绝）；`start` 与现有个人测试共用限流（每分钟 10 次）。
- 桌面版：Electron 只允许打开 **https** 且主机在白名单（目前仅 `openrouter.ai`）内、无账号口令、无非默认端口的地址（`desktop/src/external-url.js`，有单元测试）。

## 3. 网页版与桌面版的回调要求

| | 网页版 / 企业版 | Windows 桌面版（local 模式） |
|---|---|---|
| 回调地址 | `OA_PUBLIC_URL` + `/v1/llm/oauth/callback`，必须设置，否则返回 `OAUTH_PUBLIC_URL_REQUIRED`（不信任请求的 Host 头） | `http://127.0.0.1:<端口>/v1/llm/oauth/callback`（取自当前回环 Host），OpenRouter 允许 localhost 任意端口 |
| 浏览器 | 当前页面整页跳转，授权后 302 回 `/settings?llm_oauth=ok/denied/expired/invalid/used`，设置页续接并轮询 | 系统浏览器打开授权页；回调显示“可关闭此页面”；应用窗口轮询 |
| 服务商侧显示 | 公网地址：完整应用归属 | 本机回调：显示“主机:端口”，不进入 OpenRouter 的应用榜单 |

反向代理：需把 `OA_PUBLIC_URL`（例如 `https://oa.example.com`）与代理对外地址保持一致，`/v1/llm/oauth/callback` 不能被代理的认证拦截；如启用 `OA_TRUSTED_PROXY=1`，同时保证 `X-Forwarded-Proto` 正确。无需配置任何客户端 ID / 密钥。

测试 / 镜像用环境变量（一般不需要）：`OA_LLM_OAUTH_OPENROUTER_AUTHORIZE_URL` 与 `OA_LLM_OAUTH_OPENROUTER_EXCHANGE_URL` 必须同时设置，只允许 https（本机回环除外）。

## 4. 排错

| 现象 | 原因 / 处理 |
|---|---|
| “服务器未设置对外访问地址（OA_PUBLIC_URL）” | 企业版必须设置 `OA_PUBLIC_URL` 后重启 |
| 授权页点了“拒绝” | OpenRouter 拒绝时不回传 `state`，网页版会提示“拒绝/未完成”；桌面版点“取消”或等待 10 分钟过期 |
| “授权码无效或已过期” | 授权码 10 分钟有效且只能用一次；重新发起 |
| “无法连接 / 暂时不可用 / 超时” | 服务端到 `openrouter.ai` 的网络或对方故障；检查出站网络与代理后重试 |
| 已连接但“连接测试未通过” | 默认模型 `openrouter/auto` 会路由到不同模型，识别需要工具调用；点“获取模型列表”改选明确支持工具调用的模型 |
| 关闭浏览器后没有反应（桌面版） | 授权可能已完成；回到应用窗口等待，或重新发起 |
| 个人流程被拒绝 | 管理员未开放个人大模型（AI 使用策略） |

## 5. 未验证项
- 没有用真实 OpenRouter 账号做过端到端登录；测试使用假的 PKCE 服务（`backend/internal/llm/oauth_test.go`）。
- 其他平台的结论依赖公开文档与搜索结果，见表中标注。
