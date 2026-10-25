# 首次运行

## Windows 桌面版

桌面版是单用户 `local` 模式：没有登录页、没有初始化令牌，任何能打开页面的人都是管理员（所以默认只监听本机）。首次运行依次做：

1. 等待启动画面完成（首次需初始化内置数据库）。
2. 选择归档文件夹。
3. （可选）[配置大模型](./llm-keys)。

## Linux 企业版：初始化向导

首次打开访问地址会自动进入 `/setup`。共 7 步（后来加入的“大模型”步骤在企业模式下是第 8 步，可显式跳过）：

| 步骤 | 说明 |
|---|---|
| 环境检查 | 向导会检查运行环境 |
| 初始化令牌 | 输入安装结束时打印的 `SETUP TOKEN`；从服务器本机访问可跳过 |
| 运行模式 | 保持默认 **enterprise**。`local` 只有从服务器本机访问时才可选，且之后只能升级为企业模式，不能改回 |
| 创建管理员 | 用户名与密码 |
| 域名与安全策略 | **RP ID** 是 Passkey 绑定的域名（不含协议与端口），“允许的来源”是精确的访问地址；管理员 MFA 默认强制 |
| 绑定第一个 MFA | Passkey 或验证器 App（TOTP）；随后显示 **10 个一次性恢复码，只显示这一次**，请保存并勾选确认 |
| 完成 | 向导此后不再出现 |

要点：

- 向导由服务端强制，可从中断处恢复；未完成前除 `/v1/setup/*`、`/healthz`、`/readyz` 外的接口返回 503 `SETUP_REQUIRED`。
- Passkey 只能在 HTTPS（或 localhost）下使用；没有域名时只能用 TOTP。
- 之后修改域名会使所有已注册 Passkey 失效（TOTP 不受影响）。

### 找回初始化令牌

令牌每次服务启动时在日志里以 `SETUP TOKEN:` 打印（初始化完成前）。

| 部署 | 命令 |
|---|---|
| Docker（推荐） | `oa-agentctl setup-token` |
| Docker（手动） | `docker compose logs api \| grep SETUP` |
| 容器内 | `oa-admin setup-token` |

已完成初始化后不再生成令牌。

## 创建用户与角色

管理员在“管理 → 用户管理”新建用户。临时密码和邀请链接**只显示一次**；用户首次用临时密码登录必须改密；邀请链接 24 小时内有效、仅限一次使用。

| 角色 | 能做什么 |
|---|---|
| 管理员 | 全部权限 |
| 操作员 | 导入、审批、执行、纠错、文件管理、模板与规则；看不到存储连接与管理页 |
| 只读 | 只能查看任务、文件、台账、预览与导出 |

## 应急：管理员锁在门外

在服务器上用命令行（不依赖网页）：

```bash
oa-agentctl admin reset-password <用户>
oa-agentctl admin reset-mfa <用户>
oa-agentctl admin unlock <用户>
oa-agentctl admin create-admin <用户>
```

`oa-admin` 的子命令（`setup-token`、`status`、`list-users`、`create-admin`、`reset-password`、`reset-mfa`、`unlock`、`disable-user` / `enable-user` 等）操作会写入审计日志（`oa-admin(cli)`）。

## 配置大模型

向导的最后一步（企业模式）或“设置 → 识别引擎”。见[配置大模型密钥](./llm-keys)。
