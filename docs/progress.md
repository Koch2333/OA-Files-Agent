# 开发进度

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
