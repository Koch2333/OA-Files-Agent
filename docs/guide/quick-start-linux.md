# Linux 企业版快速开始

企业版是 Docker Compose 栈：PostgreSQL 16 + API（含前端、迁移与 `oa-admin`）+ 文档/OCR Worker，可选 Caddy 自动 HTTPS。多用户、管理员默认强制 MFA、带审计日志。

::: warning 验证范围
安装脚本与 `oa-agentctl` 有离线 shim 测试（1300 多条断言）和 Docker Desktop 上的真实引擎端到端测试。**未验证**：真实发行版上用包管理器装 Docker、Caddy 真实证书申请、真实阿里云 ACR 与 GitHub 托管 runner、MySQL / SQL Server 作为外部库的安装器端到端、ARM64、systemd 单元、Podman / rootless Docker。首次部署请先在测试机上运行 `--check`。完整声明见[部署指南](/部署指南-Linux)开头。
:::

## 系统要求

| 项目 | 要求 |
|---|---|
| 系统 | Ubuntu / Debian、CentOS / Rocky / Alma / RHEL / Fedora、Alpine（先 `apk add bash`），x86_64 或 aarch64（尽力支持） |
| 资源 | ≥2 核、≥4 GB 内存（OCR 占内存，<2 GB 警告）、≥5 GB 可用磁盘，归档另算 |
| 软件 | Docker Engine ≥ 20.10 + Compose v2（缺失时脚本会询问后安装；外部数据库层需要 Compose ≥ 2.24） |
| 其他 | 系统时钟必须同步（TOTP 依赖）；HTTPS 需 80/443 对公网开放且域名已解析 |

## 安装

先只读检查，确认全是 `[PASS]`、了解每条 `[WARN]`：

```bash
sudo bash install.sh --check            # 只读，不改任何东西；有 [FAIL] 时退出码 3
```

三种取得脚本的方式：

```bash
# A. 仓库检出内
sudo bash deploy/linux/install.sh

# B. 远程一键（install.sh 是发行版资产，脚本会校验源码包的 SHA-256）
curl -fsSL https://github.com/Koch2333/OA-Files-Agent/releases/latest/download/install.sh \
  | sudo bash -s -- --domain oa.example.com

# C. 从容器镜像仓库安装（不在本机编译），<R> = 仓库地址[/命名空间]
docker run --rm <R>/oa-agent-deploy:latest | sudo bash -s -- --registry <R>
```

::: tip 本站未验证的一点
方式 B 依赖 Releases 页存在该版本的 `install.sh` 资产（见[发布流程](/dev/release)）；本站编写时没有对真实发布的资产做过下载验证。
:::

非交互示例：

```bash
sudo bash install.sh --yes --domain oa.example.com --email admin@example.com \
     --archive-dir /srv/oa/archive --extra-mount nas1=/mnt/nas1:ro --mirror cn
```

## 安装器怎么工作：先检测，再决定，再提问

1. **检测**（只读，不需要 root）：系统资源、前置工具、systemd、Docker / Compose 版本、已有安装（`/etc/oa-agent/install.json`、容器标签、`.env`）、旧数据、端口占用者、已有数据库服务、防火墙、网络。结果打印成一张组件表（`[PASS]` `[FOUND]` `[MISS]` `[TODO]` `[WARN]` `[FAIL]`）。
2. **只补缺少的**：已有 Docker 与 Compose 就不重装；缺什么前置包只装什么。
3. **向导**（仅全新安装，一次问一个问题）：安装目录 → HTTP 端口 → 域名 / HTTPS → 数据库（内置 PostgreSQL、已有 PostgreSQL / MySQL 8+ / SQL Server 2019+）→ 存储目录 → 镜像源 → 镜像标签 → 复核页（`[Y]继续 / [e]修改 / [c]取消`）。
4. 预检闸门：有 `[FAIL]` 就退出（码 3），此时尚未做任何改动。外部数据库会在安装前先用 `oa-admin db-check` 验证。
5. 对**健康的同版本安装**重新运行只显示状态，不拉镜像。

提问读自 `/dev/tty`，所以 `curl … | sudo bash` 也能交互。没有终端又没有 `--yes` 时，脚本打印说明并以退出码 2 结束，不会悄悄装一个纯 HTTP 实例。

## 常用参数

| 参数 | 作用 |
|---|---|
| `--check` / `--dry-run` | 只读预检并打印将要做的事 |
| `--status` | 只读显示已有安装的状态与访问信息 |
| `--config <文件>` | 从 `key=value` 文件读取答案；文件权限必须是 600；**数据库密码只能写在这里** |
| `--print-config` | 把本次答案（不含密码）按 `--config` 格式输出后退出，供第二台服务器复用 |
| `--offline <镜像包.tar>` | 离线安装；包由 `scripts/save-images.sh` 在能联网的机器上生成（含 `.sha256`） |
| `--registry HOST[/NS]` / `--image-tag T` | 从镜像仓库安装 / 指定标签 |
| `--dir` `--port` `--domain` `--email` | 安装目录（默认 `/opt/oa-agent`）、端口、域名、证书邮箱 |
| `--archive-dir` `--spool-dir` `--data-dir` `--pg-dir` `--extra-mount name=/路径[:ro\|:rw]` | 存储位置；归档目录可以是任意本机路径或 NAS 挂载点 |
| `--db bundled\|postgres\|mysql\|mssql` | 数据库；外部库另加 `--db-host` `--db-port` `--db-name` `--db-user` `--db-tls` `--db-ca-file` `--db-schema`，密码绝不接受命令行 |
| `--behind-proxy --domain <域名>` | 放在已有反向代理（nginx / Caddy / 1Panel）之后 |
| `--lang zh\|en` | 界面语言；不指定时交互运行先问一次（默认取自系统区域设置），无人值守按区域设置并打印一行说明 |
| `--docker-source auto\|official\|aliyun\|azure\|tencent\|tuna\|ustc\|<https 地址>` | 缺少 Docker 时的安装源，默认 `auto`：查 ipinfo.io 国家（第三方；`--no-geo-lookup` 关闭）+ 本地信号，大陆则按速度试可达镜像，最后官方；始终打印检测结果 |
| `--mirror cn` | 构建镜像源（goproxy.cn 等），并视为在中国大陆（跳过国家查询） |
| `--install-docker` | `--yes` 下允许自动安装 Docker |
| `--upgrade` / `--reinstall` / `--uninstall [--purge]` | 升级 / 重建容器（保留数据）/ 卸载（`--purge` 才删数据） |
| `--help` | 完整列表 |

优先级：命令行参数 > `OA_INSTALL_<键>` 环境变量 > `--config` 文件 > 向导 > 默认值。

第二台服务器复用答案：

```bash
sudo bash install.sh --print-config > answers.conf
# 在新机器上补 db_password=…（如用外部库）
chmod 600 answers.conf
sudo bash install.sh --config answers.conf --yes
```

离线安装：

```bash
# 能联网的机器
scripts/save-images.sh --registry <R> --tag sha-0a1b2c3 --out ./pack    # 要用域名 / HTTPS 时加 --with-caddy
# 把 pack/ 下三个文件拷到服务器
sudo bash install.sh --offline oa-agent-images-sha-0a1b2c3-amd64.tar --yes --archive-dir /srv/oa/archive
```

## 退出码

| 码 | 含义 |
|---|---|
| `0` | 成功 |
| `1` | 失败 |
| `2` | 参数 / 取值错误（含没有终端又没有 `--yes`） |
| `3` | 环境检查未通过（有 `[FAIL]`，尚未改动任何东西；`--check` 同样） |
| `4` | 用户取消 |
| `130` | 被 Ctrl-C / TERM 中断，可安全重跑 |

## 安装之后

- 结束时打印访问地址、**初始化令牌**、目录和管理命令。请**立即**完成初始化向导：拿到令牌的人可以抢先成为管理员。令牌每次重启都会更换，`oa-agentctl setup-token` 可重新查看。下一步见[首次运行](./first-run)。
- 日常运维用 `oa-agentctl`（状态、备份、升级、重置密码等），见[oa-agentctl 命令](/reference/oa-agentctl)。
- **有域名时**启用 Caddy 自动 HTTPS，Passkey 才能用；没有域名则是纯 HTTP，只能用 TOTP，口令与会话明文传输，强烈建议配域名。
- 更换域名会使已注册的 Passkey 失效。
- 前面有自己的反向代理时：必须传 `X-Forwarded-Proto` / `X-Forwarded-For`，支持 SSE（关闭缓冲）和不限大小的上传，见[大文件与批量导入](/大文件与批量导入)。
- **备份** `OA_SECRET_KEY`（`backup` 已包含，但请异地保存）：丢失后已保存的 API 密钥和存储凭据无法解密。

更完整的内容（目录结构、备份还原、安全清单、故障排查、镜像仓库模式）见[Linux 服务器部署指南](/部署指南-Linux)。
