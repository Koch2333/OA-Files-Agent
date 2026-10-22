# oa-agentctl 使用说明

`oa-agentctl` 是单个静态 Go 程序（Linux amd64/arm64，无需安装运行时），用来管理 OA Agent 的 Docker Compose 部署：不带参数在终端中运行进入全屏界面（TUI），带子命令则是可脚本化的非交互命令。安装脚本同时提供 bash 备用工具 `oa-agentctl.sh`（等待就绪、卸载等），两者命令集兼容。

只通过 `docker compose`（v2）工作，所有调用都是参数数组（不拼 shell 字符串），工作目录为安装目录，`COMPOSE_FILE` / `COMPOSE_PROFILES` 取自 `.env`。运行期不做任何网络访问（只经由 docker）。`.env` 中的密钥在所有输出和日志里都会被遮蔽。

## 1. 安装目录与文件

安装目录按以下顺序确定：`--dir` → 环境变量 `OA_AGENT_DIR` → `/etc/oa-agent/install_dir` → `/opt/oa-agent`。

| 文件 | 说明 |
| --- | --- |
| `.env` | 配置与密钥，权限 0600，本工具只原子改写（先写临时文件再 rename），其余行原样保留 |
| `docker-compose.yml` | 基础编排 |
| `mounts.json` | **存储挂载的唯一事实来源**（权限 0640） |
| `docker-compose.mounts.yml` | 由 `mounts apply` 生成（0644，文件头注明“generated — do not edit”），请勿手改 |
| `backups/` | 默认备份目录 |
| `*.bak-<时间>` | 每次修改前自动保留的 mounts.json / 覆盖文件 / .env 旧版本（各保留最近若干份） |

`COMPOSE_FILE` 的层顺序：`docker-compose.yml : docker-compose.mounts.yml : [docker-compose.https.yml] : [docker-compose.build.yml]`。

## 2. 命令一览

通用选项（可放在任意位置）：`--dir <目录>`、`--json`（机器可读）、`--yes`/`-y`（跳过 y/N 确认）、`--dry-run`（只演示不写入、不调用会改动状态的 docker 命令）。

退出码：`0` 成功；`1` 失败；`2` 用法错误（含“不在终端里却不带子命令”）；`3` 未找到安装；`4` 需要确认但未确认/被拒绝；`5` docker 错误或未安装 docker。

```
oa-agentctl                          # 终端中：进入交互界面；非终端：提示并以 2 退出
oa-agentctl status [--json]          # 服务状态、端口、访问地址
oa-agentctl start | stop | ps
oa-agentctl restart [postgres|api|worker|caddy]   # api 与 worker 总是一起重建
oa-agentctl logs [服务] [-f] [--tail N]
oa-agentctl upgrade [--version V] [--no-backup]   # 先备份 → pull/build → up -d → 等待健康
oa-agentctl backup [--out 目录] [--skip-archive]
oa-agentctl restore <备份文件>
oa-agentctl doctor [--json]          # 有失败项时退出码 1
oa-agentctl version
oa-agentctl setup-token              # 初始化令牌（先查 api 日志，再调用 oa-admin setup-token）
oa-agentctl admin <oa-admin 参数…>   # 原样透传，在终端里自动分配 tty
oa-agentctl users list | reset-password|reset-mfa|unlock|disable-user|create-admin <用户>
oa-agentctl mounts list | check [--fix]
oa-agentctl mounts add <名称> <主机路径> [--ro|--rw] [--replace] [--accept-data-move] [--restart]
oa-agentctl mounts remove <名称> [--restart]
oa-agentctl mounts set <archive|spool|app|pgdata> <主机路径> [--copy] [--accept-data-move] [--restart]
oa-agentctl mounts apply [--restart]
oa-agentctl config show | config set <KEY> <值|->
oa-agentctl domain <域名> | --none [--no-start]
```

`oa-admin` 在 api 容器运行时用 `docker compose exec -T api oa-admin …`，api 未运行时自动改用 `docker compose run --rm -T api oa-admin …`。

`config set` 只接受白名单键（端口、`OA_PUBLIC_URL`、内存上限、日志轮转、时区、LLM 基础地址/模型/密钥、版本、镜像仓库等），并逐键校验取值；密钥类键、`COMPOSE_FILE`、路径类键请用专用命令。值为 `-` 时从标准输入读取（避免密钥出现在命令行历史里）。

## 3. 交互界面（TUI）

要求：SSH 终端，最小 80x24，随窗口缩放；纯键盘操作；遵循 `NO_COLOR`；标准输入/输出不是终端时给出明确提示并以 2 退出。底部是当前屏幕的快捷键栏。

```
┌ 主菜单 ───────────────────────────────────────────────┐
│ OA Agent 管理                                          │
│                                                        │
│   1. 仪表盘          5. 日志                           │
│   2. 用户与密码      6. 备份与还原                     │
│   3. 存储挂载        7. 配置                           │
│   4. 服务管理        8. 诊断                           │
│  ↑/↓ 选择  Enter 进入  1-8 直达  q 退出                │
└────────────────────────────────────────────────────────┘
```

**仪表盘**：每 5 秒刷新。

```
 服务         状态       健康       端口                        运行时间
 ✔ postgres   running    healthy    -                           2 hours
 ✔ api        running    healthy    0.0.0.0:8080->8080/tcp      2 hours
 ✔ worker     running    -          -                           2 hours
 磁盘
 ✔ archive  /srv/oa/archive   [####......] 剩余 500.0 GiB / 900.0 GiB
 初始化状态：…        访问地址：http://srv:8080
```

**用户与密码**：列表（角色/状态/MFA/锁定），`p` 重置密码、`m` 重置 MFA、`u` 解锁、`d` 停用、`a` 新建管理员。重置密码/停用/重置 MFA 需**输入用户名**确认，解锁/创建为 y/n。临时密码只在高亮框里显示一次：

```
   ╭────────────────────────────────╮
   │  临时密码（仅显示这一次）      │
   │                                │
   │    alice                       │
   │    Zx9-Tmp-Secret              │
   ╰────────────────────────────────╯
   按任意键继续——密码不会再次显示
```

按任意键后密码从内存中清除，不写日志、不写磁盘。`oa-admin list-users` 优先用 `--json`，不支持时宽容地解析表格输出。

**存储挂载**：列表显示名称、主机路径、容器路径、模式、剩余空间、文件系统、状态图标。`a` 添加外部目录（默认只读）、`e` 修改（外部目录或核心路径）、`x` 移除外部目录、`A` 按当前 mounts.json 应用并重启。表单中 **Tab 补全目录**，输入过程中实时显示校验结果：

```
 添加外部目录
 名称：nas1
 主机路径：> /srv/oa/archive/sub
 ✘ archive（/srv/oa/archive）与 nas1（/srv/oa/archive/sub）互相嵌套或重合……
```

提交后进入“确认变更”页（差异摘要、全部警告与可复制的修复命令）；涉及已有数据时必须输入 `I-UNDERSTAND`。`Enter/y` 应用并重启，`w` 只写入不重启。应用时有进度视图；`docker compose up` 失败，或 api 在超时内没有变健康，会**自动回滚**到之前的 mounts.json / 覆盖文件 / .env 并用旧配置重新启动 api+worker。

**服务管理**：启动、停止（输入 `STOP`）、重启全部、重启 api+worker、升级（输入 `UPGRADE`），实时滚动输出。**日志**：先选服务，再 `f` 跟随开关、`/` 搜索、`n/N` 跳转、PgUp/PgDn 滚动。**备份与还原**：立即备份（含/不含归档）；还原先选备份文件，输入 `RESTORE` 确认。**配置**：密钥遮蔽显示，`e` 修改白名单键（实时校验），`d` 切换域名/HTTPS（显示 Passkey 失效警告，输入 `DOMAIN` 确认）。**诊断**：同 `doctor`，带修复建议。

## 4. 存储挂载规则

挂载点（`mounts.json`）：

```json
{"version":1,
 "archive":{"host":"/srv/oa/archive"}, "spool":{"host":"/srv/oa/spool"},
 "app":{"host":"/srv/oa/app"}, "pgdata":{"host":"/srv/oa/pgdata"},
 "extra":[{"name":"nas1","host":"/mnt/nas1","container":"/mnt/storage/nas1","readonly":true}]}
```

生成的覆盖文件（与 `deploy/linux/docker-compose.mounts.example.yml` 完全一致的结构）：postgres → `/var/lib/postgresql/data`；api → `/data`（app）、`/data/spool`、`/data/archive`、外部目录 `/mnt/storage/<名称>`（只读加 `:ro`），并设置 `OA_LOCAL_ROOTS=/mnt/storage`；worker → `/data/spool` 与只读的 `/data/archive`（外部目录**不**挂进 worker）。外部目录按名称排序，输出稳定、可复现。`mounts apply` 同时把四个核心路径同步到 `.env` 的 `OA_ARCHIVE_DIR / OA_SPOOL_DIR / OA_APP_DIR / OA_PG_DIR`，并确保 `COMPOSE_FILE` 含覆盖文件。`mounts apply` 不会提示确认（安装脚本会无人值守地调用它），无效输入以非 0 退出并且不改动任何文件。

路径规则（与安装脚本一致，违反即拒绝）：

- 必须是绝对路径；只允许字符 `A-Za-z0-9_./-`；不得含 `..`；不得是 `/`。
- 不得位于或包含这些系统目录：`/etc /usr /bin /sbin /lib* /boot /proc /sys /dev /run /var/lib/docker`（例如 `/var/lib` 因包含 docker 目录也被拒绝）。
- 不得包含安装目录（其中的 `.env` 存放密钥）。
- archive、spool、app、pgdata 与各外部目录两两之间不得相同或互相嵌套（符号链接解析后同样检查）。
- 外部目录名称 `^[a-z0-9][a-z0-9_-]{0,30}$`，唯一，且不能用保留名（archive spool app pgdata data postgres api worker caddy storage tmp default none root pg）；容器内路径固定为 `/mnt/storage/<名称>`。
- 外部目录默认只读，需要显式 `--rw`；已有只读目录改成读写要额外确认；外部目录必须已存在（工具从不替你创建别人的目录）。

环境检查（`mounts check`、TUI 实时提示）：目录存在、是目录；核心目录属主应为容器用户（pgdata：70:70 且权限 700/750，其余 10001:10001），不符时给出准确命令（如 `chown -R 70:70 '/srv/oa/pgdata'`），以 root 运行时可自动执行（`mounts check --fix`，或应用时确认）；外部目录检查容器用户读/写权限；显示文件系统类型与剩余空间——pgdata 放在 tmpfs/overlay/网络或 FUSE 文件系统上会警告，核心目录剩余不足 20 GiB 会警告。

### 更换归档目录（数据不会被工具移动或删除）

归档目录里存放着台账（数据库）引用的全部文件。把 `archive` 指到另一个目录而不复制旧数据，台账里的文件会“消失”。因此：

1. 旧目录非空时，`mounts set archive` 会显示旧路径并解释风险，**必须**显式确认：交互界面输入 `I-UNDERSTAND`；命令行加 `--accept-data-move`（`--yes` 不够）。新目录为空或已有内容时分别给出额外警告。
2. 建议先停止服务，自行复制并核对：`cp -a '/旧/archive/.' '/新/archive/'`，再 `oa-agentctl mounts set archive /新/archive --restart`。
3. 或者让工具代劳：`oa-agentctl mounts set archive /新/archive --copy`——先输入 `COPY`（带 `--yes` 则免输入）确认，工具执行 `cp -a`，再用文件数与总字节数校验，校验通过才切换；**源目录始终保持不变**，需要时请你在确认无误后自行删除。复制期间服务仍在运行时，新写入的文件可能遗漏，请先 `stop`。
4. 切换后用 `oa-agentctl doctor` 确认，并抽查几个文档的预览/导出。

spool、app 同理（app 目录里有 `secret.key`，新目录没有该文件时已保存的存储凭据将无法解密，需要确认）。

### 更换数据库目录

`pgdata` 只能在**所有容器停止**后更改，否则直接拒绝（先 `oa-agentctl stop`）。旧目录非空时同样需要显式确认：新目录若为空，应用会初始化一个全新的空数据库。迁移方法：`stop` → `cp -a '/旧/pg/.' '/新/pg/'`（属主保持 70:70）→ `mounts set pgdata /新/pg --accept-data-move` → `start`。

## 5. 重置用户密码（应急）

1. `oa-agentctl users list` 确认用户名与状态。
2. `oa-agentctl users reset-password <用户>`（需 `--yes` 或在终端确认）。临时密码**只显示这一次**，请直接转交本人；用户首次登录必须修改。需要脚本取用时用 `--json`，并自行保证不落盘/不进日志。
3. 该用户有 MFA 且丢失设备：`users reset-mfa <用户>`（清除 TOTP/Passkey/恢复码，下次登录按策略重新绑定）；被锁定：`users unlock <用户>`。
4. 不会有“最后一个启用的管理员被停用”之类的保护被绕过：这些规则由 oa-admin 保证，工具只转发其错误信息。
5. 管理员全部丢失：`oa-agentctl users create-admin <用户>`，或 `oa-agentctl admin help` 查看 oa-admin 全部命令。

## 6. 备份与还原

备份是一个 `tar.gz`（权限 0600），结构与 bash 备用工具一致：`db.dump`（`pg_dump -Fc`）、`env`、`app/secret.key`、`archive/…`、`MANIFEST`。**包含加密密钥与数据库口令，请像密码一样保管。** 还原会覆盖当前数据库与归档目录，旧归档目录被改名为 `archive.pre-restore-<时间>`（不删除）；备份里的 `OA_SECRET_KEY` 与当前不同时采用备份里的（否则已保存的凭据无法解密）。解包时拒绝路径穿越，符号链接/设备文件一律不还原。

## 7. 故障排查

| 现象 | 处理 |
| --- | --- |
| `未找到安装`（退出码 3） | 用 `--dir` 指定，或设置 `OA_AGENT_DIR` |
| 退出码 5 / `未找到 docker` | 安装 Docker 与 Compose v2；确认当前用户能访问 docker.sock |
| 应用挂载后自动回滚 | 看进度里 docker 的错误（常见：目录不存在、属主不对、SELinux 需 `:z`）；先 `mounts check`，修复后重试；旧配置的备份在 `*.bak-<时间>` |
| api 一直不健康 | `oa-agentctl logs api --tail 200`；`doctor` 会给出修复命令 |
| `oa-admin` 无输出/报错 | api 未运行时会自动 `compose run --rm`，需要 postgres 可启动；`oa-agentctl admin help` |
| TUI 一直空白几秒 | 终端不回应背景色查询时有短暂等待，属正常 |
| TOTP 总是失败 | `doctor` 的时间同步检查；启用 chrony / systemd-timesyncd |
| 想完全手动 | `oa-agentctl.sh`（bash 备用）提供 status/start/stop/logs/backup/restore/upgrade/domain 等；生成挂载覆盖文件只能用本程序 |

## 8. 开发与测试

代码在 `backend/cmd/oa-agentctl` 与 `backend/internal/ctl/*`（纯逻辑包 `mounts` `compose` `oaadmin` `doctor` `apply` `ops` 与 bubbletea 界面 `tui` 分离，docker 调用经 `compose.Runner` 接口，测试使用 `compose.Fake`）。

```
cd backend
gofmt -l ./cmd/oa-agentctl ./internal/ctl
go vet ./cmd/oa-agentctl ./internal/ctl/...
go test ./cmd/oa-agentctl ./internal/ctl/...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o oa-agentctl ./cmd/oa-agentctl
```

`cmd/oa-agentctl/e2e_linux_test.go` 仅在 Linux 运行：编译真实二进制（或用 `OA_CTL_BIN` 指定），把假的 `docker` 脚本放进 PATH 记录调用，对临时安装目录跑完整流程。
