# Windows 桌面版快速开始

适用于 Windows 10/11。无需安装 Python、数据库或 Office。

## 选择安装包

在 [Releases](https://github.com/Koch2333/OA-Files-Agent/releases) 下载（`<版本>` 不带 `v`），每个版本附统一的 `SHA256SUMS`。

| 机器 | 安装版 | 便携版 | 仅服务端 |
|---|---|---|---|
| Intel / AMD（x64） | `oa-agent-<版本>-windows-x64-setup.exe` | `…-windows-x64-portable.zip` | `…-windows-x64-server.zip` |
| Windows 11 on Arm（实验性） | `…-windows-arm64-setup.exe` | `…-windows-arm64-portable.zip` | `…-windows-arm64-server.zip` |

校验：`Get-FileHash <文件> -Algorithm SHA256`，与 `SHA256SUMS` 中的值对比。

::: warning 未签名
安装包没有代码签名，SmartScreen 会提示“已保护你的电脑”，点“更多信息 → 仍要运行”。杀毒软件可能误报后端、Worker 或 PostgreSQL，必要时加入白名单。
:::

## 步骤

1. **安装版**：运行 `setup.exe`（可选当前用户/所有用户，可选安装目录与归档工作目录），然后启动“OA 文件智能归档”。
   **便携版**：解压 zip，运行 `OA-Agent-Desktop.exe`，数据保存在程序旁的 `data\`。
   **服务端版**（无 Electron，用浏览器访问）：解压后双击 `start.bat`，约 30 秒后自动打开 `http://127.0.0.1:8080`；关闭黑色命令窗口即停止服务。
2. 首次启动要初始化内置数据库，可能需要一两分钟，启动画面会显示进度。
3. 首次运行会弹出窗口选择**归档文件夹**（桌面版默认 `文档\OA-Agent\归档`，便携版默认程序旁 `归档\`）。
4. 桌面版是单用户 `local` 模式，不需要登录，也不需要初始化令牌。
5. （可选）在“设置 → 识别引擎”配置大模型，见[配置大模型密钥](./llm-keys)。不配置则使用离线规则。
6. 在“智能归档”页拖入文件，见[导入与归档](./import-and-archive)。

## 数据放在哪里

| 内容 | 位置 |
|---|---|
| 数据目录（数据库、暂存、日志） | `%LOCALAPPDATA%\OA-Agent`；便携版为程序旁 `data\`；环境变量 `OA_AGENT_DATA_DIR` 可覆盖 |
| 日志 | 数据目录下 `logs\`（`desktop.log`、`oa-agent.log`、`postgres.log`、`worker.log`） |
| 归档结果 | 你选的归档文件夹 |

归档文件夹可随时在“设置 → 桌面版设置…”（用户菜单、设置页卡片或托盘图标，快捷键 `Ctrl+,`）里更改：可选“迁移”（复制并逐文件校验，失败自动回滚，绝不覆盖）或“直接使用新位置”。

## 局域网访问

默认只监听本机（`127.0.0.1:8080`）。想让别人访问请改用[企业版](./quick-start-linux)，不建议把单用户免登录的桌面版暴露到局域网。

## 已知限制

- ARM64 包为实验性，OCR 在仿真下较慢。
- 桌面外壳与应用内密钥窗口没有用真实的服务商页面端到端验证（需要账号），见[配置大模型密钥](./llm-keys)。

更多细节：[用户手册](/用户手册)、桌面外壳说明 [`desktop/README.md`](https://github.com/Koch2333/OA-Files-Agent/blob/main/desktop/README.md)。
