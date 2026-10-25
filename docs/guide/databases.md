# 数据库选择

数据库是真相源（任务、文件、计划、台账、审计都在里面），Excel 只是导出。

## 支持矩阵

| 引擎 | 最低版本 | 实测版本 | 说明 |
|---|---|---|---|
| PostgreSQL | 14 | `postgres:16` | 默认。Windows 桌面版与 Linux 企业版都内置（Linux 为 `postgres:16-alpine`） |
| MySQL | 8.0 | `mysql:8.0`（8.0.46） | InnoDB、utf8mb4；不支持 5.7 与 MariaDB |
| SQL Server | 2019 | `mssql/server:2019-latest` | 未测 Azure SQL |
| SQLite | 不支持 | - | 缺少 `SKIP LOCKED` 与并发写模型，明确不做 |

未测试：MySQL 8.4、SQL Server 2022、MariaDB、Azure SQL、企业 CA 真实证书链的 TLS（仅单元级校验）。

## 怎么选

- 个人版：用内置 PostgreSQL，什么都不用做。
- 企业版：安装向导里选“内置 PostgreSQL（推荐）”，或“已有 PostgreSQL / MySQL 8+ / SQL Server 2019+”。
- 已有数据库：为 OA Agent 准备**专用库**（PostgreSQL 可用专用 schema）和最小权限账号。目标里如果有非 OA 的表，校验会直接拒绝；账号权限过大只告警。建库与授权的 SQL 见[多数据库部署与迁移指南 §3](/多数据库部署与迁移指南)。

选择优先级：环境变量 > 数据目录 `database.json`（设置页保存，重启后生效）> 内嵌 PostgreSQL（仅 Windows 独立安装）。用环境变量配置时，设置页“数据库”卡片只读。

```ini
OA_DB_PROVIDER=postgres|mysql|mssql
OA_DB_HOST=…  OA_DB_PORT=…  OA_DB_NAME=…  OA_DB_USER=…  OA_DB_SCHEMA=…
OA_DB_PASSWORD=…              # 或 OA_DB_PASSWORD_FILE（Docker/Kubernetes secret）
OA_DB_TLS_MODE=disable|require|verify-ca|verify-full
# 或一个完整 URL：OA_DB_DSN=mysql://user:pw@host:3306/oa   （旧的 OA_DATABASE_URL 视为 PostgreSQL）
```

完整变量见[配置项](/reference/configuration#数据库)。

## 容器里的外部数据库

- 数据库主机**不能写 `localhost`**（客户端在容器里）；宿主机用 `host.docker.internal`。
- 需要 Docker Compose ≥ 2.24（外部库覆盖层）。
- 安装器在装任何东西之前先用 `oa-admin db-check` 验证（连接、版本、权限、隔离性、空库或只含 OA 表），通过后显示 DDL 预览，第二次确认后才建表；密码只经标准输入，不进日志和进程参数。
- 数据库密码只能写在 `--config` 文件里（权限 600）或交互输入，不接受命令行与环境变量。

## 初始化与升级（需管理员批准）

- 命令行：`go run ./cmd/migrate -dir ../migrations` 只打印 DDL 预览，加 `-apply` 才执行；含 `-- oa:destructive` 的迁移还需 `-allow-destructive`。
- 设置页：保存配置 → “预览并初始化库结构” → “批准并执行”。
- Docker 默认 `OA_AUTO_MIGRATE=1`（部署级批准）。隔离与版本检查先于任何建表。
- PostgreSQL / SQL Server 使用事务 DDL，失败整体回滚；MySQL 的 DDL 自动提交，用步骤标记表续跑并检测中断状态。`*.down.sql` 永不自动执行。

## TLS

`disable` 不加密；`require` 加密但不校验证书；`verify-ca` 校验证书链；`verify-full` 另校验主机名。生产建议 `verify-full`。

## 备份

用各引擎自带工具（`pg_dump`、`mysqldump --single-transaction`、`BACKUP DATABASE`），文件目录另行备份。切换数据库前必须备份。Docker 版的 `oa-agentctl backup` 针对内置 PostgreSQL（`pg_dump -Fc` + 归档目录 + `secret.key` + `.env`）。

## 跨库数据迁移：没有实现

本版本**没有**在 PostgreSQL / MySQL / SQL Server 之间搬迁数据，界面也没有这个入口。设置页保存新数据库配置不会自动搬数据，对已有数据的安装需要显式确认；换库请用空库新装，或自行用引擎工具迁移后再切换。

## 已知限制

- 只有真实容器上测过的组合才保证可用。
- 搜索忽略大小写但不折叠重音。
- MySQL / SQL Server 作为外部库的**安装器**端到端未在真实引擎上验证（后端 `dbadmin` 有自己的真实引擎测试）。

深入阅读：[多数据库部署与迁移指南](/多数据库部署与迁移指南)、[多数据库兼容设计](/多数据库兼容-设计)。
