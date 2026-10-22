# 开发进度

## 2026-10-22 · 文件管理与识别引擎设置

### 文件管理（新增）
- `/v1/fs/*`：浏览、预览/下载（pdf/png/jpg 内联，其它强制下载，支持 Range）、新建文件夹、重命名/移动（**不覆盖**、文件夹不能移入自身）、删除到可恢复的回收站、恢复（原位置被占用自动加序号）。路径全部经 `internal/fsman`：校验、符号链接安全、`.` 开头的内部目录（.staging/.journal/.trash）不可见。
- 台账同步（`internal/fileops`）：重命名/移动更新台账路径与规范文件名，删除把台账记为“已移除”（文件库同步回“未归档”），恢复再还原；台账更新失败时磁盘改动自动撤回。归档后被改动的文件，整体“撤销归档”会被拒绝（`FILES_CHANGED`）。
- **Windows 资源管理器**：`POST /v1/fs/reveal` 打开文件夹或选中文件；仅 Windows、仅回环请求、仅限归档根目录内，不会打开/运行文件本身；`OA_ALLOW_REVEAL=0` 关闭。
- 本机服务防护：跨站写请求（Origin/Sec-Fetch-Site）与非本机 Host 头被拒绝（`CROSS_ORIGIN`/`HOST_NOT_ALLOWED`）。

### 识别引擎设置（新增）
- 预设：DeepSeek（默认推荐）、通义千问（国内站/国际站）、OpenAI、Ollama 本机、自定义；**Base URL 与模型名随时可改**，可从服务商 `/models` 拉取列表，可“测试连接”（含工具调用检测与可读错误）。
- 配置存数据库（迁移 004），API Key AES-GCM 加密（`data/secret.key` 或 `OA_SECRET_KEY`），永不返回、永不入日志/审计；保存后**热生效**无需重启；`OA_LLM_*` 仅作首次启动的种子。
- 安全规则：公网必须 https（http 只允许本机/内网）；链路本地/元数据地址一律拒绝；不跟随重定向；更换 Base URL 不会沿用旧服务商的密钥。
- Worker：解析子进程崩溃/输出异常时重试一次（超时不重试），避免瞬时故障把正常文件标成“损坏”。

### 验证
- `go vet`、`go test ./...`（含真实 PostgreSQL 的 fileops/store/pipeline/db 集成测试）通过；Worker 单测 25 个通过（1 个跳过）。
- e2e 新增 part4（文件管理）、part5（识别引擎设置，用本地假 OpenAI 兼容服务验证热切换、密钥不回显、跨服务商不沿用密钥、识别确实经由所配模型）全部通过；契约校验用 210 条真实响应通过。
- **未验证**：没有用真实的 DeepSeek/通义千问密钥测试（只测了 OpenAI 兼容协议与本地假服务）；Windows 资源管理器真正弹出窗口需用 `go run ./cmd/e2e -reveal` 在桌面会话中手动确认；e2e 中一次扫描 PDF 的 OCR 子进程偶发失败（单独重复 32 次均未复现，根因未确认，已加重试兜底）。

## 2026-10-22 · 后端接手完成（M0–M4 主体）、前端联调

由 Claude 接手 Codex 未完成的后端并完成端到端联调（用户明确要求）。

### 已实现并实测
- **契约**：`contracts/openapi.yaml` 1.0.0 覆盖全部业务路由；前端类型由它生成。`scripts/check_contracts.py --recorded` 用 e2e 记录的 145+ 条真实响应校验契约。
- **导入**：multipart `paths` 清单 + `files`，保留子目录、100 个/批、20 MiB/文件，单文件失败不拖垮整批；Idempotency-Key 重放；路径穿越/保留名拒绝。
- **解析/OCR**（Python Worker，子进程隔离、超时、路径限定在 spool）：docx、xlsx、原生 PDF、扫描 PDF、png/jpg；加密/损坏/旧版 `.doc/.xls` 给出可读错误。
- **Agent Harness**：Tool Registry（inspect_file、extract_text、run_ocr、detect_template、extract_metadata、check_duplicates、validate_plan）、回合/失败/预算上限、OpenAI 兼容 Provider、离线规则 Provider、提示词注入防护、事件脱敏。
- **识别规则**：文件名约定 `部门_标题_日期` 的部门为字面量并最优先；落款/抬头；标签字段；关键词同义词仅作兜底且要求人工确认。
- **模板与规则**：OA 模板 CRUD/启停/样本测试/导入时指定；命名与目录规则（占位符、校验）。
- **审批流**：计划 PATCH（乐观锁）→ 批准（有待确认项则拒绝）→ 执行（幂等、暂存+原子发布、哈希校验、冲突加序号、journal、崩溃恢复）→ 台账 → 回滚。
- **台账**：关键词（`%`/`_` 字面匹配）、筛选、排序、分页，Excel 导出（excelize，冻结首行+筛选）。
- **可靠性**：任务队列持久化（`FOR UPDATE SKIP LOCKED`）、重启后恢复、取消/重试、SSE 断线续传、审计日志、单文件纠错（先对比后应用；已批准计划被改动则退回待审批）。
- **Windows 开箱即用包**：`oa-agent.exe` 管理 Worker 与内置 PostgreSQL（随机端口、随机密码、仅回环），首次启动自动初始化；`deploy/windows/build.ps1` 一键构建。

### 实际执行与结果（本机：Windows Go/Node/Python + WSL 里的 PostgreSQL 16）
- `go vet ./...`、`go test ./...` 全部通过（含真实 PostgreSQL：db、store、pipeline 集成测试）。
- Python Worker 单测 24 个（1 个因本机无符号链接权限而跳过）。
- `go run ./cmd/e2e`：全部检查通过（R01–R10 及幂等、冲突、回滚、取消/重试、模板、纠错、校验错误、SSE）。
- 真实赛题样本 10 份：部门 10/10、日期 10/10（文件名约定）；`-blind`（仅正文）部门 4/10，其余均为“待审核”或被标记需确认，无自信的错误。
- `frontend/e2e/ui.mjs`：真实 Edge + 真实后端 24 项检查通过，截图见 `frontend/e2e/screenshots/`。
- 在 `dist\oa-agent` 解压即用形态下重复 e2e 与 UI 测试，通过；冷启动约 6 秒。

### 已知限制与风险
- **PyMuPDF 为 AGPL-3.0**，对外发行需评审或购买商业许可。
- 默认识别为离线规则，**未用真实 LLM 实测**（Provider 代码有 httptest 覆盖）；真实模型的字段只经过 Go 侧校验。
- OCR 仅在清晰的合成图像上验证；真实手机拍摄/倾斜/手写扫描件准确率未知。每个 OCR 请求在新子进程中加载模型（约 1–3 秒）。
- 未在**干净的 Windows 机器**上验证安装；未做代码签名；安装包约 524 MB（OCR 依赖占大头）。
- 跨卷归档的 rename 回退路径、符号链接逃逸测试因本机无权限未执行；归档根目录不得被其它进程写入。
- NFC 规范化仅覆盖拉丁组合字符；认证/RBAC/SSO 为占位（单机本地用户）；无 `propose_archive` 工具；无并发压测。
- 旧版 `.doc/.xls` 不转换，仅给出明确提示。
- CI 配置已更新（含 PostgreSQL 服务），尚未在远程运行。

### 之前（Codex）
M0 第 1–4 步：服务骨架、PostgreSQL 接入、Worker 探活、离线 API 文档、契约检查（历史记录见 git 之外的旧版 progress）。

### 存储连接（可选）
- 7 类提供方（S3 兼容、Azure Blob、WebDAV、SFTP、FTP/FTPS、SMB、钉钉钉盘），导入来源 + 归档副本只增同步；迁移 006；契约/前端/文档已更新。
- 已验证：真实 WebDAV 服务端到端（连接 CRUD、测试、浏览、远端导入全流程、同步字节一致、不改远端原文件、无凭据回显、审计）；契约检查 277 条真实响应；浏览器页面检查。
- 未真实验证：SMB 数据通路、Azure、钉钉（仅模拟）、S3/SFTP/FTP 仅进程内服务、Nextcloud/OpenList 实机。
- 新增 9 种提供方（改编自 OpenList，AGPL-3.0）：OneDrive、Google Drive、Dropbox、阿里云盘、百度网盘、123云盘、OpenList、Seafile、Cloudreve；OAuth 刷新令牌轮换经 `Settings.Persist` 加密回存。仅模拟服务测试，未对接真实账号。
- 路由换用 Gin（仅路由/恢复/CORS，业务处理器仍为 net/http）；新增 `OA_CORS_ORIGINS` 精确来源白名单（拒绝 `*`/`null`，不带凭据），调试构建默认放行 Vite 开发来源；本机防护（CROSS_ORIGIN/HOST_NOT_ALLOWED）对白名单来源放行写请求，其余不变。

### 登录与账号（Passkey + TOTP，后端）
- 迁移 007（auth_settings/users/webauthn_credentials/recovery_codes/sessions/auth_challenges/invitations，SchemaVersion=7）；`internal/auth`（argon2id、TOTP、恢复码、会话/CSRF、限速、锁定、WebAuthn、初始化向导、用户管理，无 HTTP 依赖）、`internal/auth/softauth`（测试用软件认证器）、`internal/store/auth.go`；Gin 层 authn → 初始化闸 → scope/临时密码闸 → CSRF → 中央路由权限表（`internal/api/authz.go`，默认拒绝，路由表测试覆盖全部路由）；`cmd/oa-admin` 应急命令行。
- 每次启动未初始化时重新生成并在日志打印一次 `SETUP TOKEN: …`（库中只存哈希）；回环请求可免令牌（令牌若提供则必须正确）。
- 已验证：真实 PostgreSQL 下的单元/集成测试（并发初始化、最后管理员、一次性挑战/恢复码/邀请、WebAuthn 软件认证器全流程、角色矩阵）与 `cmd/e2e` part8（见下方命令）。
- 未验证：真实浏览器 Passkey（需 puppeteer+CDP 虚拟认证器）、真实反向代理、HTTPS 部署下的 Secure Cookie、local→enterprise 切换（未实现）。

### 登录/账号、Linux 与 Windows 发行（2026-10-22 验证）
- 登录系统（密码 + TOTP/Passkey、角色、向导、管理后台、`oa-admin`）：Go 全量测试（含 `-tags debug`）通过；e2e 第 1–8 部分 390+ 项（真实 PostgreSQL、企业模式登录）；契约校验 465 条真实响应；真实浏览器 + CDP 虚拟 Passkey 认证器 55 项通过（向导注册 Passkey、无密码登录、TOTP、恢复码、角色菜单、会话吊销）；`local` 模式下旧 UI 套件 50 项通过。
- Linux：Docker/compose/安装脚本/`oa-agentctl`（TUI）仅用伪造 docker 命令测试（20 项脚本测试、WSL 内 Go 测试），未对真实 Docker 验证。
- Windows：`dist-windows.ps1` 产出安装包 + ZIP + 服务端 ZIP；便携 ZIP 冒烟通过（启动画面、local 模式自动初始化、单实例、退出无残留进程）。安装包向导页与“更改工作目录”菜单流程未实机走通。
- 注意：`data/e2e-samples.json` 含一次性临时密码，勿提交（`data/` 已忽略）。

### 独立运行包异常退出 / 数据库残留（排查与修复）
- 现象：独立包（start.bat）的后端在运行几分钟后“消失”，嵌入式 PostgreSQL 仍在，下次启动报 `pg_ctl ... postgres.log Permission denied`。
- 日志对时：两次退出（19:5x、20:08:29）与开发脚本 `dev.sh/dev.ps1 down` 执行时间吻合——旧版 `down` 会 `taskkill /F /T` **占用 8080 的任意进程**，误杀了独立包的后端；强杀不会执行 `defer pg.Stop()`，于是数据库残留。
- 修复：① 开发脚本 `down` 只停止自己启动的进程（pid 文件 + 进程名/端口属主校验），对他人占用的端口只提示；② 后端在独立模式启动时把自身放入 Windows Job Object（KILL_ON_JOB_CLOSE），被 `taskkill /F`、崩溃或 `log.Fatal` 杀死时，worker 与 PostgreSQL 随之结束，不再残留（已实测：强杀后无 postgres/worker 进程，重启正常）；③ 启动时若发现上次遗留的嵌入式 PostgreSQL，先 `pg_ctl stop -m fast` 再启动；④ 修复定时清理 SQL（`invitations` 的 `$1 - interval` 类型错误，原先被忽略）并让测试检查其错误；⑤ `start.bat` 改为纯 ASCII（中文提示放入 `start-banner.txt`/`start-stopped.txt` 用 `type` 输出），避免 `chcp 65001` 下 cmd 把多字节字符错误解析成命令（截图里的 “'��可停止全部服务。' is not recognized”）。

### 错误报告与诊断（后端）
- 起因：独立包后端“消失”、嵌入式 PostgreSQL 残留，而产品里没有任何提示。新增 `internal/diag`：脱敏库（`Redact`，语料测试）、按指纹聚合的 `error_reports`/`error_occurrences`（迁移 009）、不阻塞的异步写入（库不可用时落日志文件）、`diag.Go/Safe/Loop` 协程保护、崩溃文件 `data/logs/crash-*.txt`、运行锁 `run.lock`（异常结束 → `supervisor` 报告与 `SystemInfo.last_exit_unclean`）、`diag.Fatalf/RecoverMain`。
- API：`/v1/admin/system`、`/v1/admin/error-reports`（含 `state` 别名）、诊断包 zip（脱敏、≤20 MiB、审计）、`/v1/client-errors`（限速 20/分、8 KiB）；所有 5xx/panic 带 `request_id` 与 `details.report_id`。详见 `docs/diagnostics.md`。
- 验证：见交付报告（单元/集成/e2e 第 10 部分）。未验证：Windows 上真实强杀后的异常退出检测（仅 Linux 进程探测单测 + 手工重启）、真实浏览器前端上报。
