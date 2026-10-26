# 这是什么

OA 文件智能归档智能体把散落的办公文件整理成有规则的归档目录和可搜索的台账。

## 它做什么

1. 导入：拖入文件或整个文件夹（Word / Excel / PPT 含旧版 `.doc` `.xls` `.ppt`、PDF、图片、文本、网页、邮件；其他格式也会接收，只按文件名处理）。
2. 识别：结合文件名、正文和 OCR，判断所属部门、文件类型、日期。
3. 计划：按命名与目录规则生成“原名 → 新名、原路径 → 目标路径”的归档计划。
4. 审批：你逐项核对、可手动修订，先批准，再执行。
5. 台账：每次归档自动记录，可搜索、筛选、导出 Excel。

![归档计划与审批（演示数据）](/screenshots/04-plan.png)

::: info 关于截图
截图取自仓库 `frontend/e2e/screenshots/`，由界面的自动化测试在**演示数据**模式下生成，不代表真实客户数据。
:::

## 两个版本

| | 个人版（Windows 桌面） | 企业版（Linux） |
|---|---|---|
| 形态 | Electron 桌面程序，内置后端、文档 Worker 与 PostgreSQL | Docker Compose：PostgreSQL + API + 文档 Worker，可选 Caddy 自动 HTTPS |
| 用户 | 单用户，免登录（`local` 模式） | 多用户，初始化向导默认 `enterprise` 模式，管理员默认强制 MFA，审计日志 |
| 数据库 | 内置 PostgreSQL，或复用已有 MySQL / SQL Server | 内置 PostgreSQL 16，或已有 PostgreSQL / MySQL / SQL Server |
| 入口 | [Windows 快速开始](./quick-start-windows) | [Linux 快速开始](./quick-start-linux) |

## 设计原则

- Agent 只建议，确定性的 Go 代码执行。大模型只能通过只读、带 Schema 的工具读取当前文件；路径规范化、冲突处理、写入全部在 `internal/archive`，模型没有 shell，也没有写权限。
- 文档正文是数据，不是指令（防提示词注入）。
- 证据不足留空，并要求人工确认。
- PostgreSQL 等数据库是真相源，Excel 只是导出。任务队列持久化，重启后自动续跑。
- 大模型是可选项：不配置时使用离线规则，不联网。

## 诚实的限制

- Windows 安装包**未签名**，首次运行 SmartScreen 会提示（“更多信息 → 仍要运行”）。
- Windows ARM64 包为**实验性**：主程序与后端是原生 arm64，文档 Worker 与内置 PostgreSQL 是 x64（仿真运行），未在真实 ARM64 硬件上测试。
- Linux 部署脚本经过离线 shim 测试，并在 Docker Desktop 上做过真实引擎的端到端验证；真实发行版装 Docker、Caddy 证书申请、ARM64 等未验证，详见[部署指南](/部署指南-Linux)开头的声明。首次部署请先在测试机上走一遍。
- 没有用真实的 DeepSeek / 通义千问 / OpenRouter 账号做过端到端验证（见[配置大模型密钥](./llm-keys)）。
- 使用大模型时，文档文字会发送给所选服务商；涉密文件请用离线规则或本机模型（Ollama）。
- 读不了内容的文件（加密、损坏、格式不识别、过大、空文件）不会失败，只依据文件名判断并留给人工确认；不支持压缩包。

## 许可证

AGPL-3.0。第三方组件见[第三方组件与许可证](/THIRD-PARTY-LICENSES)；其中 PyMuPDF 同为 AGPL-3.0，对外发行前请评审。
