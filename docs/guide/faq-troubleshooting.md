# 常见问题与排错

## 安装与启动

| 现象 | 原因 / 处理 |
|---|---|
| Windows 提示“已保护你的电脑” | 安装包未签名。点“更多信息 → 仍要运行”，并用 `SHA256SUMS` 校验 |
| 杀毒软件拦截 | 后端、Worker、PostgreSQL 可能被误报，加入白名单 |
| 桌面版首次启动很慢 | 首次要初始化内置数据库，可能一两分钟；启动画面失败时按钮有“重试 / 查看日志 / 数据文件夹”，并会识别端口占用、安全软件拦截、磁盘不足等 |
| 端口 8080 被占用 | 桌面版可在“桌面版设置…”固定端口；服务端版改 `oa-agent.env` 的 `OA_LISTEN_ADDR`；Linux 用 `--port` 或 `oa-agentctl config set OA_PORT 9090` |
| Linux 安装退出码 3 | 预检有 `[FAIL]`，尚未做任何改动；看 `--check` 报告逐项处理 |
| Linux 安装退出码 2 | 参数错误，或没有终端又没带 `--yes`（`curl \| bash` 取答案自 `/dev/tty`） |
| api 不健康 | `oa-agentctl logs api`。常见：数据库未就绪、`OA_WORKER_TOKEN` 少于 32 字符、目录权限 |
| worker 不健康 | 内存不足，调大 `OA_WORKER_MEM`；`oa-agentctl logs worker` |
| 归档目录写入失败 | 目录属主应为 uid 10001；NAS 检查 root_squash 与只读挂载 |
| Caddy 申请证书失败 | 检查 DNS、80/443 可达、`oa-agentctl logs caddy` |
| 想检查整体状态 | `oa-agentctl doctor` |

## 登录与账号

| 现象 | 处理 |
|---|---|
| 看不到初始化令牌 | `oa-agentctl setup-token`；已完成初始化则不再生成 |
| Passkey 不可用 | 需要 HTTPS（或 localhost）和正确域名；否则先用 TOTP |
| TOTP 码总是错 | 校准服务器时间：`timedatectl`、chrony |
| 忘记密码 / 丢失验证方式 / 被锁定 | 在服务器上 `oa-agentctl admin reset-password\|reset-mfa\|unlock <用户>` |
| 更换域名后 Passkey 失效 | 预期行为，Passkey 绑定在旧域名；在 设置 → 安全 更新 RP ID，TOTP 不受影响 |

## 大模型

| 现象 | 处理 |
|---|---|
| “服务器未设置对外访问地址（OA_PUBLIC_URL）” | 企业版使用 OpenRouter 登录授权前必须设置 `OA_PUBLIC_URL` 并重启 |
| “授权码无效或已过期” | 授权码 10 分钟有效且只能用一次，重新发起 |
| 已连接但“连接测试未通过” | 默认模型 `openrouter/auto` 可能路由到不支持工具调用的模型；高级设置里改选明确支持工具调用的模型 |
| 401 | Key 无效，重新复制 |
| 429 | 限速或额度，等倒计时后点“重试” |
| 5xx / 超时 / 连不上 | 服务商故障或网络；检查服务器出站网络与代理 |
| 保存时被拒 | 公网地址必须 https；链路本地 / 元数据地址一律拒绝；个人配置可能被 AI 使用策略关闭 |

更多见[配置大模型密钥](./llm-keys)。

## 导入与识别

| 现象 | 处理 |
|---|---|
| HTTP 507 “磁盘空间不足” | 按“原文件 + 归档副本”扩容；预留空间由 `OA_DISK_RESERVE_BYTES` 控制 |
| nginx 下大文件上传失败 | `client_max_body_size 0; proxy_request_buffering off; proxy_buffering off; proxy_read_timeout 3600s;`。分片上传每个请求只有 8 MiB，浏览器端即使保留 16m 限制也能工作 |
| 进度不更新 | 代理需关闭缓冲以支持 SSE（任务进度） |
| 文件显示“待人工审核（元数据判断）” | 文件超过 `OA_EXTRACT_MAX_BYTES`（默认 256 MiB）或证据不足，手动指定部门 / 类型 / 日期后归档 |
| 文件显示“仅依据文件名” | 内容读不了（加密、损坏、格式不识别、过大、空文件或扫描件没识别出文字）。系统只按文件名判断并留给你确认；加密的请先去掉密码，损坏的请换一份，过大的可由管理员调大 `OA_EXTRACT_MAX_BYTES` 后重试任务 |
| 扫描件识别差 | 系统会自动用增强、旋转、纠偏、高清、二值化重试并择优；仍不可靠就标“待审核”。可选的视觉识别默认关闭（页面图片会发给服务商） |
| 台账里的文件显示“缺失” | 改过归档目录却没有复制文件；用“桌面版设置…”或 `change-workdir.bat` 迁移 |

## 数据与安全

| 问题 | 回答 |
|---|---|
| 密钥丢了怎么办 | `OA_SECRET_KEY`（或 `data/secret.key`）丢失后，已保存的 API 密钥和存储凭据无法解密，需重新输入。备份时一并异地保存 |
| 能在 PostgreSQL / MySQL / SQL Server 之间迁移数据吗 | 不能，未实现，见[数据库选择](./databases) |
| 文档内容会发给谁 | 使用大模型时，文字会发给所选服务商；离线规则与本机 Ollama 不外发 |
| 日志在哪 | 桌面版：数据目录 `logs\`；Docker：`oa-agentctl logs [服务]`，应用日志与错误报告在 `data/logs/`（`OA_LOG_DIR` 可改）。见[错误报告与诊断](/diagnostics) |

## 还是解决不了

在“设置与管理 → 运行诊断”（管理员）查看错误报告并导出诊断包（已脱敏）。提交 Issue 时附上请求编号（界面错误信息里的“请求编号”），不要附密钥、令牌或文档内容：<https://github.com/Koch2333/OA-Files-Agent/issues>。
