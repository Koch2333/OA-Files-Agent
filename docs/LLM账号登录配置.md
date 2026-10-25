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
- 密钥来源标记（`key_source: manual | oauth`）与密钥名称（`key_label`）放在被加密的数据内部（以控制字符开头，手填密钥不可能冒充；格式 `\x00oa:oauth:<平台>[\x01<名称>]\x00<密钥>`，旧数据无名称照常读取），无需改表结构。
- 密钥名称：兑换成功后服务端用新密钥请求 `GET https://openrouter.ai/api/v1/key`（Bearer，返回 `data.label`；文档 https://openrouter.ai/docs/api-reference/limits ，核查 2026-10-25），取回的名称去掉控制字符并截断到 80 字，随配置返回为 `LlmConfig.key_label`（尾号仍是 `api_key_hint`）。请求失败、超时、非 200 都**不影响授权流程**，界面退回显示尾号。密钥只发往该流程自己的地址，不写日志，不出现在任何响应里（有测试）。
- 待处理流程只存在内存：重启会取消正在进行的授权，重新点击即可；每用户同时最多 5 个、全局 500 个。
- 个人流程受“AI 使用策略”约束（管理员未开放个人配置时被拒绝）；`start` 与现有个人测试共用限流（每分钟 10 次）。
- 桌面版（系统浏览器备用入口）：Electron 只允许打开 **https** 且主机在白名单内、无账号口令、无非默认端口的地址（`desktop/src/external-url.js`）。白名单 = OpenRouter 登录页 + 内置预设 `key_url` 的主机（`openrouter.ai`、`platform.deepseek.com`、`bailian.console.aliyun.com`、`platform.openai.com`）；单元测试读取 `presets.json`，预设与白名单不一致就失败。桌面版同时放行 `clipboard-read`（仅应用内部页面），供“检测到剪贴板里有密钥”的提示使用；网页版浏览器只在已授权时才在回到窗口时自动读取，否则只提供“从剪贴板粘贴”按钮（点击即用户手势）。

## 3. 网页版与桌面版的回调要求

| | 网页版 / 企业版 | Windows 桌面版（local 模式） |
|---|---|---|
| 回调地址 | `OA_PUBLIC_URL` + `/v1/llm/oauth/callback`，必须设置，否则返回 `OAUTH_PUBLIC_URL_REQUIRED`（不信任请求的 Host 头） | `http://127.0.0.1:<端口>/v1/llm/oauth/callback`（取自当前回环 Host），OpenRouter 允许 localhost 任意端口 |
| 浏览器 | 当前页面整页跳转，授权后 302 回 `/settings?llm_oauth=ok/denied/expired/invalid/used`，设置页续接并轮询 | **应用内密钥窗口**（见 3.6）打开授权页；回调页只在该窗口里显示，窗口识别到回调后自动关闭；设置页按状态轮询并显示“已为你创建专用 Key”。系统浏览器只作为窗口里明确的“改用系统浏览器”备用入口 |
| 服务商侧显示 | 公网地址：完整应用归属 | 本机回调：显示“主机:端口”，不进入 OpenRouter 的应用榜单 |

反向代理：需把 `OA_PUBLIC_URL`（例如 `https://oa.example.com`）与代理对外地址保持一致，`/v1/llm/oauth/callback` 不能被代理的认证拦截；如启用 `OA_TRUSTED_PROXY=1`，同时保证 `X-Forwarded-Proto` 正确。无需配置任何客户端 ID / 密钥。

测试 / 镜像用环境变量（一般不需要）：`OA_LLM_OAUTH_OPENROUTER_AUTHORIZE_URL` 与 `OA_LLM_OAUTH_OPENROUTER_EXCHANGE_URL` 必须同时设置，只允许 https（本机回环除外）。

## 3.5 新手向导（简单模式）：哪些是自动的，哪些不能自动

设置页的大模型卡片（全局与个人）默认是“简单模式”：选服务商（带“推荐”“一键登录”“粘贴密钥”标记）→ 登录或粘贴密钥 → 自动测试并选好模型 → 一次“保存并启用”。Base URL、模型列表、视觉识别、自定义服务只在“高级设置”里（偏好记在浏览器 `localStorage: oa.llm.ui`）。顶部始终显示连接状态：“已连接：服务商 · 模型 · 密钥名称/尾号”或“还没有设置”并高亮下一步。

| 步骤 | OpenRouter（一键登录） | DeepSeek / 通义千问 / OpenAI（粘贴密钥） | Ollama（本机） |
|---|---|---|---|
| 创建 API Key | **自动**（授权后由 OpenRouter 为本应用生成，显示“已为你创建专用 Key：名称（••••尾号）”） | **必须用户自己做**：这些平台没有面向第三方应用的授权接口，也不允许代为登录创建。桌面版：主按钮“打开向导窗口，一步步带你拿到 Key”，在应用自带的窗口里打开服务商真实的密钥页，右侧是分步指引（3.6）；网页版：主按钮“打开密钥页面并显示指引”，新标签页 `noopener` 打开服务商页面，指引显示在设置表单旁边。两种版本都保留“我已经有 Key：直接粘贴” | 不需要密钥 |
| 填入密钥 | 自动（服务端加密保存，浏览器永远拿不到） | 半自动：复制密钥后，向导窗口（桌面）识别出密钥并显示“识别到一把密钥，尾号 ••••abcd”，点“使用这个密钥”才会填入设置表单并自动测试；网页版回到页面时若剪贴板里是该平台格式的密钥，出现“粘贴这个密钥”确认条（需点击，绝不静默读取）；也可手动粘贴 | — |
| 连接测试 / 取模型列表 / 选推荐模型 | 自动 | 粘贴后自动 | 点“检测”后自动 |
| 保存并启用 | 授权完成即已保存启用（服务端已写入默认模型 `openrouter/auto`；测试显示不支持工具调用时提示去高级设置换模型） | 一次点击“保存并启用” | 一次点击 |

任何调用服务商的失败（测试连接、取模型列表、登录后的自动测试）都用白话说明原因并带 HTTP 状态（401“Key 无效，请重新复制”、429 限速/额度、5xx 服务商故障、超时/连不上），并给出“重试”按钮：原样重复失败的动作、不需要重新输入任何内容；重试中禁用并显示忙碌，429 时若接口带 `Retry-After` 则显示倒计时（`frontend/src/utils/llmError.ts`）。

网页版回调注意：设置页以 `route.fullPath` 为视图 key，回调后不能用 `router.replace` 去掉 `?llm_oauth=`（会重建页面并取消轮询）；面板改用 `history.replaceState`，并在有待续接的授权时自动展开“大模型”区域与对应服务商。

## 3.6 应用内密钥窗口（桌面版）：创建、登录授权、管理都在窗口里完成

Electron 本身就是 Chromium，所以桌面版里**所有和 API Key 有关的操作都在应用自带的一个窗口里完成**，用户不用切到系统浏览器、也不用在别处读说明。窗口（`desktop/src/keyhelper-ctl.js`，纯逻辑 `keyhelper-core.js`，面板 `keyhelper.html/js`）有三种视图：

| 视图 | 打开方式 | 内容 |
|---|---|---|
| **向导**（创建密钥） | DeepSeek / 通义千问 / OpenAI 卡片里的主按钮“打开向导窗口，一步步带你拿到 Key” | 左边是服务商**真实的密钥页**（独立沙箱视图），右边约 340 px 的指引面板（可收起）：当前网址与 🔒、编号步骤并高亮当前步、“这一步完成了/上一步”、建议的密钥名称（`OA Agent`，可改）、登录/费用提示、“改用系统浏览器”“退出登录并清除本窗口数据”。打开时已经停在密钥页就自动跳过前两步 |
| **登录授权**（OpenRouter） | OpenRouter 卡片的“登录 OpenRouter，自动完成设置” | 同样的窗口加载 OpenRouter 授权页；用户登录并点“Authorize”；回调 `http://127.0.0.1:<端口>/v1/llm/oauth/callback` 被识别后窗口自动关闭，设置页（轮询状态）显示“已为你创建专用 Key：名称（••••尾号）”并自动测试。关闭窗口而没有完成授权，设置页会结束等待并提示 |
| **管理**（已有密钥） | 已连接状态里的“管理密钥”按钮（粘贴密钥与登录授权两种来源都有） | 显示服务商、密钥名称、尾号、来源、当前模型、上次测试结果；按钮：**重新测试**、**更换密钥**（粘贴类服务商直接在同一窗口切到向导；登录类服务商交给设置页重新发起登录）、**换一个服务商**、**删除密钥**（必须先点确认；全局配置删除后改用离线规则，个人配置直接删除；登录授权的密钥等同“断开”） |

**自动与手动**：窗口按用户的操作开关、复制、点击；应用自动做的只有：把 `key_url` 作为起始页打开、按网址推进步骤、轮询剪贴板、识别回调、把确认后的密钥交给设置表单、取模型列表/选推荐模型/测试连接（带重试）、清空剪贴板。登录、注册、验证码、点“创建”、点“复制”都必须用户自己做。

**隐私与安全声明（照此实现并有测试）**

- 应用**从不读取、注入脚本、截图或自动点击服务商页面**；服务商页面所在视图没有 preload、没有 Node（`sandbox: true`、`contextIsolation: true`、`nodeIntegration: false`），读不到应用的任何接口；应用也不读它的 Cookie、表单或密码。
- 唯一读取的是**剪贴板文本**：主进程、仅在“向导”视图且窗口有焦点时每 700 ms 检查一次，只有文本**完全符合**该服务商的密钥格式才会提示；面板里只显示尾号（`••••abcd`），完整密钥只在用户点“使用这个密钥”后才经应用桥交给设置表单（不进日志、不发往其他地方）。窗口一关轮询立即停止；打开窗口前剪贴板里已有的内容不会被提示。确认后若剪贴板里仍恰好是这把密钥，会被清空并在设置页提示“清空了剪贴板”。
- 每个服务商一个**持久独立分区**（`persist:oa-keyhelper-<id>`），下次免登录；“退出登录并清除本窗口数据”清空该分区。
- 导航只允许白名单：`https`、无账号口令、默认端口、主机等于预设 `key_hosts` 的某一项，或以 `.` 开头的后缀项（匹配该域名及子域，`evil-deepseek.com`、`deepseek.com.evil.example`、`user@host`、IDN/末尾点都不匹配）。主框架跳转、重定向、弹窗都按此检查；不在名单的跳转被拦截并在面板里说明，弹窗在名单内则在同一视图打开、其余只有通过 `external-url.js` 白名单才交给系统浏览器；子框架（验证码、登录小组件）只要求 https；所有请求只允许 https（回环测试主机与登录回调除外）。证书错误不绕过。拒绝下载和除 `clipboard-sanitized-write` 以外的全部权限请求。
- IPC：只有主窗口（应用源）可以打开窗口、回复请求；只有指引面板自己的 webContents 且页面地址正确才能发送面板动作，动作名在白名单内。登录视图的授权地址必须在服务商白名单内，且 `callback_url` 必须正好是本应用的回环回调，否则拒绝。
- 管理视图本身不持有登录会话：它向设置页发请求（状态/测试/删除），设置页用正常接口完成并只回复摘要（没有密钥）。

**每家服务商的白名单（`backend/internal/llm/presets.json` 的 `key_hosts`，后端加载时校验，桌面端再校验一次）**

| 服务商 | `key_hosts` | 依据与说明 |
|---|---|---|
| DeepSeek | `platform.deepseek.com` | 密钥页 `https://platform.deepseek.com/api_keys`；登录页在同一主机。未核实它是否会跳到其他域名做验证码/短信，若跳转被拦会提示并可改用系统浏览器 |
| 通义千问 / 阿里云百炼 | `.aliyun.com` | 控制台 `bailian.console.aliyun.com`；阿里云登录会经过 `account.aliyun.com`、`signin.aliyun.com`、`passport.aliyun.com` 等同一注册域下的子域，所以用后缀。公开文档只确认了 `signin.aliyun.com` 的登录令牌机制，具体跳转链未用真实账号核实；支付宝/淘宝扫码登录使用的其他域名未加入 |
| OpenAI | `platform.openai.com`、`auth.openai.com` | OpenAI 帮助文档确认可用邮箱/Google/Microsoft/Apple/手机号登录；**Google/Microsoft/Apple 单点登录的主机没有加入**（Google 对内嵌浏览器还常常拒绝登录），需要时面板提示改用邮箱登录或“改用系统浏览器” |
| OpenRouter | `.openrouter.ai` | 授权页 `openrouter.ai/auth`；登录组件可能用其子域；Google/GitHub 单点登录同样没有加入 |

Windows 上的真实服务商页面需要账号，自动化测试用本机假服务商（`desktop/test/e2e-keyhelper.mjs`，环境变量 `OA_KEYHELPER_TEST_HOSTS=127.0.0.1:<端口>`：只接受回环 `host:port`，只在主进程环境里读取，**打包版（`app.isPackaged`）完全忽略**，网页内容无法设置环境变量）。

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
- 没有用真实账号验证 `GET /api/v1/key` 的 `data.label`（依据官方文档；测试用假服务）。
- 应用内密钥窗口没有用真实的 DeepSeek / 阿里云 / OpenAI / OpenRouter 页面和真实的单点登录流程验证（需要账号）；白名单依据公开文档和已知行为，真实跳转链可能还需要补充主机（被拦截时面板会提示并可改用系统浏览器）。剪贴板轮询在真实桌面会话里依赖窗口获得焦点，自动化测试用 `AppActivate` 取得焦点。
- 没有用真实 OpenRouter 账号做过端到端登录；测试使用假的 PKCE 服务（`backend/internal/llm/oauth_test.go`）。
- 其他平台的结论依赖公开文档与搜索结果，见表中标注。
