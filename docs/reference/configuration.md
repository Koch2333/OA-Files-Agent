# 配置项（环境变量）

后端**不会**自动加载 `.env`：请导出环境变量；Windows 独立安装由启动器读取 `oa-agent.env`（只认 `OA_` 开头的键，已设置的环境变量优先）；Docker 部署由 `oa-agentctl` 管理 `.env`（权限 0600）。

来源：`.env.example`、`deploy/linux/.env.example`、`deploy/windows/oa-agent.env.template` 与后端代码。标有“Docker”的变量由 Compose / 安装器读取，不是后端进程直接读取。

::: tip 通常不用手改
大模型在界面“设置 → 识别引擎”里配置并存数据库；Docker 部署用 `oa-agentctl config set KEY 值`（白名单键，密钥用 `-` 从标准输入读）。
:::

## 服务与进程

| 变量 | 默认 | 说明 |
|---|---|---|
| `OA_LISTEN_ADDR` | `127.0.0.1:8080` | API 监听地址。局域网访问需自行评估风险 |
| `OA_WORKER_URL` | `http://127.0.0.1:8090` | 文档 Worker 地址；只接受回环 http |
| `OA_WORKER_TOKEN` | 无（必填） | API 与 Worker 共用的 Bearer token，≥32 字符的随机串。独立启动器自动生成 |
| `OA_SPOOL_ROOT` | `./data/spool` | 暂存目录；API 与 Worker 必须一致，Worker 只读取其下文件 |
| `OA_ARCHIVE_ROOT` | `./data/archive` | 归档根目录 |
| `OA_FRONTEND_DIR` | 空 | 指向构建好的前端目录时由 API 直接提供界面 |
| `OA_MIGRATIONS_DIR` | `../migrations` | 迁移目录 |
| `OA_AUTO_MIGRATE` | `0` | `1`：启动时自动应用迁移（Docker 默认 1；独立启动器强制 1） |
| `OA_LOG_DIR` | 暂存目录同级的 `logs` | 日志与错误报告目录 |
| `OA_LOG_LEVEL` | 空 | `info`：关闭 debug 构建里的调试日志 |
| `OA_ALLOWED_ROOTS` | 空 | 仅 Worker：额外的只读预览根目录；手动运行 Worker 时设为归档根 |
| `OA_WORKER_PORT` | `8090` | 仅 Worker：监听端口 |
| `OA_EXTRACT_TIMEOUT` | Worker 内置默认 | 仅 Worker：单次解析超时（秒） |

## 访问与安全

| 变量 | 默认 | 说明 |
|---|---|---|
| `OA_PUBLIC_URL` | 空 | 用户实际访问的地址（如 `https://oa.example.com`）；预填向导的 RP ID / 来源；**企业版使用 OpenRouter 登录授权必须设置** |
| `OA_TRUSTED_PROXY` | 空 | `1`：前面有会设置 `X-Forwarded-For` / `X-Forwarded-Proto` 的反向代理（客户端 IP、Secure Cookie、回环判断）；否则这些头被忽略 |
| `OA_CORS_ORIGINS` | 空 | 逗号分隔的精确来源，允许跨域调用 API；空为仅同源；`*` 被拒绝。debug 构建默认带 Vite 开发来源 |
| `OA_SECRET_KEY` | 空 | 加密已保存密钥的 64 位十六进制串；未设置时自动创建 `data/secret.key`（0600）。丢失后已保存的密钥无法解密 |
| `OA_ALLOW_REVEAL` | `1` | `0`：关闭“在资源管理器中显示”（仅 Windows、仅回环请求） |
| `OA_STORAGE` | `on` | `off`：关闭可选的存储连接功能 |
| `OA_LOCAL_ROOTS` | 空 | 存储连接“本地目录”类型允许的根目录；空则该类型的连接全部失败（Docker 中为 `/mnt/storage`） |

## 数据库

优先级：环境变量 > 数据目录 `database.json` > 内嵌 PostgreSQL（仅 Windows 独立安装）。

| 变量 | 说明 |
|---|---|
| `OA_DB_PROVIDER` | `postgres` / `mysql` / `mssql` |
| `OA_DB_HOST` `OA_DB_PORT` `OA_DB_NAME` `OA_DB_USER` `OA_DB_SCHEMA` | 连接参数 |
| `OA_DB_PASSWORD` / `OA_DB_PASSWORD_FILE` | 密码或密码文件（Docker / Kubernetes secret） |
| `OA_DB_TLS_MODE` | `disable` / `require` / `verify-ca` / `verify-full` |
| `OA_DB_TLS_CA_PEM` / `OA_DB_TLS_CA_FILE` `OA_DB_TLS_SERVER_NAME` `OA_DB_TLS_CERT_FILE` `OA_DB_TLS_KEY_FILE` | TLS 材料 |
| `OA_DB_DSN` | 完整 URL（`postgres://`、`mysql://`、`sqlserver://`…）；与 `OA_DB_PROVIDER` 的类型不一致会报错 |
| `OA_DATABASE_URL` | 旧变量，视为 PostgreSQL（除非协议另有说明） |
| `OA_POSTGRES_PASSWORD` | Docker / Windows 包：内置 PostgreSQL 的口令（安装器生成） |
| `OA_TEST_DATABASE_URL` `OA_TEST_MYSQL_URL` `OA_TEST_MSSQL_URL` | **仅测试**：一次性数据库，切勿指向生产 |

## 大模型

| 变量 | 默认 | 说明 |
|---|---|---|
| `OA_LLM_BASE_URL` `OA_LLM_MODEL` `OA_LLM_API_KEY` | 空 | **仅**在尚未保存任何配置时作为首次种子；之后以界面设置为准 |
| `OA_LLM_TIMEOUT_SECONDS` | `60` | 单次调用超时 |
| `OA_LLM_PRESETS_FILE` | 空 | 用 JSON 文件替换内置模型预设（无效时忽略并保留内置） |
| `OA_LLM_OAUTH_OPENROUTER_AUTHORIZE_URL` `OA_LLM_OAUTH_OPENROUTER_EXCHANGE_URL` | 空 | 测试 / 镜像用，必须同时设置，仅允许 https（本机回环除外） |

## 导入

| 变量 | 默认 | 说明 |
|---|---|---|
| `OA_MAX_UPLOAD_BYTES` | 空（不限） | 可选安全阀：单文件上限（字节） |
| `OA_MAX_BATCH_FILES` | 空（不限） | 可选安全阀：每批文件数上限 |
| `OA_DISK_RESERVE_BYTES` | `536870912` | 数据卷始终保留的剩余空间（512 MiB） |
| `OA_UPLOAD_SESSION_TTL_HOURS` | `24` | 放弃的分片上传多久后清理 |
| `OA_EXTRACT_MAX_BYTES` | `268435456` | 仅 Worker：超过则不做全文提取 / OCR，转人工审核；`0` = 不设保护 |

## Windows 独立安装 / 桌面版

| 变量 | 说明 |
|---|---|
| `OA_STANDALONE` | `1`：独立启动模式（helper 进程在可执行文件旁，读取 `oa-agent.env`；由 `start.bat` 设置） |
| `OA_HOME` | 独立模式的程序根目录（默认可执行文件所在目录） |
| `OA_DATA_DIR` | 数据目录（数据库、暂存、日志），默认程序旁 `data\` |
| `OA_OPEN_BROWSER` | `0`：独立启动时不自动打开浏览器 |
| `OA_EXIT_ON_STDIN_EOF` | `1`：标准输入关闭时退出（桌面外壳用来保证后端随外壳退出） |
| `OA_AGENT_DATA_DIR` `OA_AGENT_ARCHIVE_ROOT` | 桌面外壳：数据目录 / 归档根（便携与测试用，见 `desktop/README.md`） |

## Docker 部署（`deploy/linux/.env`）

由安装器写入，用 `oa-agentctl config set` 修改白名单键。下表是常用项，完整注释见 `deploy/linux/.env.example`。

| 变量 | 默认 | 说明 |
|---|---|---|
| `COMPOSE_FILE` | 由安装器生成 | Compose 层顺序，勿手改 |
| `OA_PORT` | `8080` | 对宿主机发布的端口（API / UI，唯一发布的端口） |
| `OA_BIND_ADDR` | `0.0.0.0` | 有域名时自动为 `127.0.0.1` |
| `OA_DOMAIN` `OA_ACME_EMAIL` | 空 | HTTPS 域名与证书邮箱 |
| `OA_HTTP_PORT` `OA_HTTPS_PORT` | `80` `443` | Caddy 端口 |
| `OA_ARCHIVE_DIR` `OA_SPOOL_DIR` `OA_APP_DIR` `OA_PG_DIR` `OA_DATA_DIR` | `./data/...` | 宿主机路径，由 `mounts.json` 同步，用 `oa-agentctl mounts` 修改 |
| `OA_VERSION` `OA_IMAGE_REGISTRY` | `latest` / `oa-agent` | 镜像标签与仓库 |
| `OA_PG_IMAGE` `OA_CADDY_IMAGE` | `postgres:16-alpine` `caddy:2-alpine` | 基础镜像 |
| `OA_TZ` | `UTC` | 容器时区，例如 `Asia/Shanghai`；时钟必须正确（TOTP） |
| `OA_PG_MEM` `OA_API_MEM` `OA_WORKER_MEM` `OA_CADDY_MEM` | `1g` `1g` `3g` `256m` | 内存上限 |
| `OA_LOG_MAX_SIZE` `OA_LOG_MAX_FILE` | `10m` `5` | 容器日志轮转 |
| `OA_BEHIND_PROXY` | 空 | `1`：放在已有反向代理之后（无 Caddy 层） |
| `OA_BUILD_GOPROXY` `OA_BUILD_NPM_REGISTRY` `OA_BUILD_PIP_INDEX` | 空 | 源码构建镜像（`--mirror cn`） |

## 开发辅助

`OA_ALLOW_RESET=1`：允许 `cmd/migrate -reset`（清空开发库，仅限开发）。

更多：[大文件与批量导入 §7](/大文件与批量导入)、[多数据库部署与迁移指南 §2](/多数据库部署与迁移指南)、[Linux 部署指南](/部署指南-Linux)。
