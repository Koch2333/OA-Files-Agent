# 登录与账号管理设计（Passkey + TOTP）

状态：设计（2026-10-22）。接口以 `contracts/openapi.yaml`（tag `auth` / `setup` / `admin-users`）为准，本文给出语义、数据模型与安全要求。

## 1. 目标与模式

- 企业级登录：用户名+密码，叠加 TOTP 或 Passkey（WebAuthn）作为第二因素；也支持 **Passkey 无密码登录**（可发现凭据 + userVerification=required，本身即多因素）。
- 管理后台（Web）：用户、角色、安全策略、会话、审计；另有服务器侧命令行 `oa-admin`（应急：重置管理员/MFA、解锁、查看初始化令牌）。
- 首次安装向导（Setup Wizard）。
- 两种运行模式（向导里二选一，存于 `auth_settings.mode`）：
  - `enterprise`：必须登录（Linux 服务器部署的默认建议）。
  - `local`：本机单用户，不登录（Windows 单机包）。**仅当向导请求来自本机回环地址时才允许选择**；之后所有请求视为内置用户 `local`（角色 admin）。可由管理员在 `enterprise` 与 `local` 之间切换的唯一方向是 local→enterprise（需要创建管理员）；enterprise→local 不提供（防止降级攻击）。
- 未完成初始化（`setup_completed=false`）时，除 `/healthz`、`/readyz`、`/v1/setup/*` 与静态前端外，所有 `/v1/*` 返回 503 `SETUP_REQUIRED`。

## 2. 角色与权限

| 角色 | 能做什么 |
|---|---|
| `admin` | 全部，含用户管理、安全策略、模型/存储连接设置、审计日志 |
| `operator` | 导入、审批/执行/回滚、纠错、文件管理、模板与规则编辑、台账导出；不能改模型/存储连接/用户 |
| `viewer` | 只读：任务、文件、台账、预览、下载、导出 |

路由权限表集中放在一处（`internal/api/authz.go`），默认拒绝：每个路由必须显式映射到最低角色；新增路由未登记时测试失败。粗规则：`GET`（非 admin 资源）→ viewer；其他写方法 → operator；`/v1/admin/*`、`/v1/llm/*` 写、`/v1/storage/*` 全部、`/v1/audit-logs`、`/v1/fs/reveal` → admin。

## 3. 数据模型（迁移 `007_auth`）

- `auth_settings`（单行，id=1）：`setup_completed`、`setup_token_hash`、`mode`（enterprise|local）、`rp_id`、`rp_name`、`origins text[]`、`mfa_policy`（`off`|`admins`|`all`，默认 `admins`）、`allow_password_login`（默认 true；false = 仅 Passkey）、`session_idle_minutes`（默认 60）、`session_max_hours`（默认 12）、`password_min_length`（默认 12）、`lockout_threshold`（默认 5）、`lockout_minutes`（默认 15）、`updated_at`。
- `users`：`id uuid`、`username`（唯一，不区分大小写，3–64，`[A-Za-z0-9._@-]`）、`display_name`、`role`、`status`（active|disabled）、`password_hash`（argon2id，可空=仅 Passkey）、`must_change_password`、`totp_secret_enc`（secrets.Box 加密）、`totp_confirmed_at`、`totp_last_step`（防重放）、`webauthn_handle bytea`（随机 64 字节，不是用户名）、`failed_logins`、`locked_until`、`last_login_at`、`created_at`、`updated_at`。
- `webauthn_credentials`：`id bytea pk`、`user_id`、`name`、`public_key`、`sign_count`、`aaguid`、`transports text[]`、`backup_eligible`、`backup_state`、`created_at`、`last_used_at`。
- `recovery_codes`：`user_id`、`code_hash`（sha256+盐无需，高熵 10 位 base32 分组）、`used_at`。每次生成 10 个，重新生成会作废旧码。
- `sessions`：`id`（sha256(cookie 值) 的 hex）、`user_id`、`created_at`、`last_seen_at`、`expires_at`、`ip`、`user_agent`、`mfa`（none|totp|passkey|recovery）、`scope`（`full`|`enroll_mfa`：策略要求 MFA 但用户尚未绑定时的受限会话，只允许 `/v1/auth/*` 绑定接口）、`csrf_hash`。
- `auth_challenges`：`id`、`kind`（login_mfa|webauthn_register|webauthn_login|webauthn_mfa|totp_enroll）、`user_id` 可空、`data jsonb`（WebAuthn SessionData 或待确认 TOTP 密钥的密文）、`expires_at`（5 分钟）、一次性消费。
- 审计沿用 `audit_logs`（actor=用户名）。必须记录：登录成功/失败（不含密码）、MFA 失败、锁定、Passkey/TOTP 增删、密码修改、会话吊销、用户增删改、策略变更、初始化完成。

## 4. 登录流程

1. `POST /v1/auth/login {username,password}`：
   - 未知用户与错误密码返回同样的 401 `INVALID_CREDENTIALS`，并对不存在的用户也做一次哈希运算（抗用户枚举/计时）。
   - 账户锁定（连续失败达阈值）→ 423 `ACCOUNT_LOCKED`（不泄露是否存在则在未知用户上走同样的计数逻辑：按「用户名+IP」限速）。
   - 密码正确且用户已绑定 MFA → 200 `{status:"mfa_required", mfa_token, methods:[totp|passkey|recovery]}`；`mfa_token` 是 5 分钟一次性挑战。
   - 密码正确且无 MFA：策略要求（admins/all 命中）→ 200 `{status:"enroll_mfa", user}` 并下发 `scope=enroll_mfa` 会话；否则 → 200 `{status:"ok", user, csrf_token}` 下发完整会话。
2. MFA：`POST /v1/auth/mfa/totp {mfa_token, code}`；`POST /v1/auth/mfa/recovery {mfa_token, code}`；Passkey 作二因素：`POST /v1/auth/mfa/passkey/begin {mfa_token}` → `finish`。TOTP 码按步长防重放（`totp_last_step`），窗口 ±1。
3. Passkey 无密码：`POST /v1/auth/passkey/login/begin`（不带用户名，discoverable）→ `finish`。要求 UV；账户停用/锁定同样拒绝。该路径产生的会话 `mfa=passkey`。
4. 会话：随机 32 字节，cookie `oa_session`（HttpOnly、SameSite=Strict、Path=/、HTTPS 下 `Secure`；Secure 判定使用 `r.TLS` 或在 `OA_TRUSTED_PROXY=1` 时的 `X-Forwarded-Proto`）。服务器只存哈希。空闲超时与绝对超时都生效；每次请求滑动更新 `last_seen_at`（最多每分钟写一次）。登录成功时轮换会话（防固定）。
5. CSRF：登录响应与 `GET /v1/auth/me` 返回 `csrf_token`；所有非 GET 的认证请求必须带 `X-CSRF-Token`（常量时间比较），否则 403 `CSRF_INVALID`。现有 Origin/Sec-Fetch-Site 守卫保留。
6. 限速：登录/MFA/Passkey 端点按 IP 与「用户名」双维度令牌桶（内存即可），超限 429 `RATE_LIMITED` 且带 `Retry-After`。失败计数写库，成功清零。

## 5. 注册与管理

- 无自助注册。管理员创建用户：给定用户名/显示名/角色，系统生成一次性临时密码（只显示一次，`must_change_password=true`），或创建「仅 Passkey」用户并生成一次性**邀请链接**（`/invite/<token>`，24 小时，绑定 Passkey 后失效）——邀请令牌高熵、仅存哈希。
- 管理员操作：编辑角色/状态、重置密码（再次显示临时密码）、重置 MFA（清 TOTP/Passkey/恢复码，下次登录按策略重新绑定）、解锁、吊销全部会话、删除用户。**不得**：停用/删除/降级最后一个启用的 admin；管理员不能通过接口读取他人的 TOTP 密钥/Passkey 公钥。
- 用户自助：改密码（需当前密码，成功后吊销其他会话）、绑定/删除 TOTP（绑定需用一次有效码确认）、注册/重命名/删除 Passkey（至少保留一种可登录方式；策略要求 MFA 时不能删到零个 MFA）、重新生成恢复码（需密码或近期 MFA 复验）、查看与吊销自己的会话。
- 敏感操作（改密码、删 MFA、生成恢复码）要求「近期认证」：会话 `fresh_until`（登录后 10 分钟内，或再次输入密码延长）。

## 6. 初始化向导

- 首次启动且未初始化时，服务生成 **初始化令牌**（24 字节随机，base32），只存哈希，**明文只在服务日志与 `oa-admin setup-token` 命令里出现一次**（Docker：`docker compose logs api | grep SETUP`；安装脚本结束时直接打印）。作用：防止服务暴露在公网时被他人抢先完成初始化。`local` 模式向导来自回环地址时可免令牌。
- 步骤（前端 `/setup`）：1 欢迎与环境检查（`GET /v1/setup/status`：数据库、Worker、HTTPS 状态、能否使用 Passkey）→ 2 输入初始化令牌 → 3 选择模式 → 4 创建管理员（用户名、显示名、密码，密码强度提示）→ 5 域名与安全策略（RP ID、允许的来源、MFA 策略、会话时长）→ 6 绑定第一个 MFA（Passkey 或 TOTP，可显示二维码与手动密钥，并生成/保存恢复码）→ 7 完成，进入系统。
- `POST /v1/setup/complete` 一步写入：管理员、策略、模式，使 `setup_completed=true` 并返回登录会话（受限或完整，视策略）。之后 `/v1/setup/*` 除 status 外返回 409 `ALREADY_SETUP`。该调用原子（事务 + 行锁），并发只有一个成功。
- WebAuthn 配置：`rp_id` 必须是请求域名的后缀（`localhost` 允许 http）；`origins` 必须为精确 origin。非 HTTPS 且非 localhost 时 `passkey_available=false`，向导隐藏 Passkey 并解释原因（可只用 TOTP）。`OA_PUBLIC_URL` 环境变量可预填 RP ID/来源。

## 7. 与现有系统的衔接

- 中间件：Gin 层 `authn`（解析会话 → 注入用户与角色到 context）、`authz`（路由表）、CSRF；`actorOf(r)` 返回登录用户名（local 模式为 `local`）。
- `OA_AUTH=off` 不提供（避免绕过）；安全默认 enterprise。Windows 单机包向导默认建议 `local`。
- SSE：`GET /v1/tasks/{id}/events` 使用 cookie 会话（同源 fetch 流，已无 Authorization 头需求）。
- CORS：跨域需要带 cookie 时 `AllowCredentials` 必须为 true 且 origin 精确；仅当 `OA_CORS_ORIGINS` 非空时启用，且 cookie 为 SameSite=Strict 不适用跨站，所以跨域前端开发（Vite 代理）仍走同源代理，CORS 不用于认证场景（保持 AllowCredentials=false；文档说明）。
- 密钥：TOTP 密文使用 `internal/secrets` 同一把主密钥（`OA_SECRET_KEY`/`data/secret.key`）；容器部署必须持久化该密钥。

## 8. 测试与验证要求

- 单元：密码哈希/策略、TOTP（含 RFC 6238 向量、重放、窗口）、恢复码、会话过期（可注入时钟）、CSRF、限速、锁定、角色权限矩阵（表驱动覆盖**每个**路由）、最后一个管理员保护、setup 并发。
- 集成（真实 PostgreSQL）：完整 API 流程；`cmd/e2e` 新增 part8：向导 → 登录 → TOTP → 权限 → 会话吊销 → 管理员操作 → 审计。
- 浏览器（puppeteer + Edge + CDP `WebAuthn.addVirtualAuthenticator`）：向导中注册 Passkey、Passkey 无密码登录、Passkey 作二因素、删除。
- 安全回归：未登录访问任意 `/v1/*` 为 401；viewer 写操作 403；跨站写 403；Cookie 属性；响应与日志中无密码/TOTP 密钥/令牌。

## 9. 实现说明与偏差（2026-10-22，以代码为准）

- **CSRF**：令牌 = HMAC-SHA256(key=会话 cookie 值, "oa-agent csrf v1")，无需存储，`/me` 可随时重发；因此 `sessions` 表没有 `csrf_hash`，另有 `fresh_until`（近期认证窗口，10 分钟）。
- **初始化令牌**：来自本机回环（RemoteAddr 回环且无 X-Forwarded-For/Forwarded/X-Real-IP，或可信代理下转发地址为回环）的请求在 enterprise 与 local 两种模式下都可免令牌；**提供了令牌就必须正确**。`local` 模式下，非回环请求一律 403 FORBIDDEN。local→enterprise 的切换**未实现**。
- **错误码补充**：近期认证、受限会话、临时密码都用 403 `FORBIDDEN` + `details.reason`（`reauth_required` / `enroll_mfa` / `must_change_password`）；MFA 令牌无效 401 `MFA_TOKEN_INVALID`，挑战过期 401 `CHALLENGE_INVALID`（绑定类 422 `CHALLENGE_EXPIRED`/`INVALID_CODE`/`INVALID_CREDENTIAL`）；`WEAK_PASSWORD` 422；`USER_EXISTS`/`CANNOT_DELETE_SELF`/`LAST_LOGIN_METHOD`/`MFA_REQUIRED_BY_POLICY`/`LOGIN_METHOD_IN_USE`/`PASSKEY_UNAVAILABLE`/`TOTP_ALREADY_ENABLED` 409；修改 RP ID/来源未确认 422 `PASSKEY_RESET_REQUIRED`。
- **近期认证**覆盖：关闭 TOTP、删除 Passkey、重新生成恢复码、管理员的重置密码/重置 MFA/删除用户/修改安全设置。改密码需要当前密码，不需要近期认证。
- **未知用户的锁定**：内存中的“幽灵锁”按用户名模拟同样的阈值与 423，避免通过 423 枚举账号。限速：每 IP 60 次突发/1 次每秒，每用户名（与 mfa_token、绑定确认）12 次突发/每 10 秒 1 次。
- **MFA 令牌**：5 分钟、最多 5 次错误（之后作废），错误也计入账号锁定。恢复码为 12 位 base32（XXXX-XXXX-XXXX，60 bit）。
- **权限表差异**：存储连接列表与浏览（`GET /v1/storage/connections`、`.../{id}/browse`）开放给 operator（远端导入需要），其余 `/v1/storage/*` 仍仅 admin；`POST /v1/imports/remote` 为 operator；`GET /v1/llm/presets|config` 为 viewer。
- **密码策略**：长度（按字符计，≤128）、不含用户名、非常见弱口令、至少两类字符；初始化时必须允许密码登录，禁用密码登录要求所有启用账号都有 Passkey。管理员创建的 Passkey 邀请用户首次接受邀请后即登录（`mfa=passkey`），恢复码在账号页生成。
- 策略收紧（如 mfa_policy→all）对已登录会话不追溯，下次登录生效。公开的 `GET /v1/setup/status` 在已初始化时返回 `mode` 与 `allow_password_login`，登录页据此隐藏密码表单。

## 6a. 向导闸门（服务端强制，可恢复）

`POST /v1/setup/complete` 创建管理员并登录，但向导尚有后续步骤（首次 MFA、恢复码、全局 LLM）。`auth_settings.wizard_finished`（迁移 010，升级前已初始化的安装回填为 true；`local` 模式在 complete 时直接为 true）在 `POST /v1/setup/finish` 之前为 false。此期间已认证请求除 `/v1/auth/*`、`/v1/setup/*`、`/v1/llm/*`、`/v1/admin/ai-policy`、`/v1/client-errors` 外一律 409 `WIZARD_REQUIRED`。`finish` 需要管理员完整会话；策略要求 MFA 而尚未绑定 → 409 `MFA_REQUIRED_BY_POLICY`；幂等，审计 `setup.finished`。前端守卫在 `initialized && !wizard_finished` 时把除 `/setup`、`/login`、邀请页以外的路由全部送回 `/setup`，向导按服务端状态（me.scope、auth/security）恢复步骤。

`GET /v1/setup/status` 还返回 `server_version` 与 `api_level`（Go 常量 `api.APILevel`，前端 `REQUIRED_API_LEVEL`，二者同时递增），不一致时前端显示版本横幅。
