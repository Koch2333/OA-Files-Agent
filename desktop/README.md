# OA 文件智能归档 —— Windows 桌面版（Electron 外壳）

单用户、免登录（后端 `local` 模式）。外壳只做：启动画面、启动/停止本机后端（`oa-agent.exe`，由它再管理文档 Worker 与内置 PostgreSQL）、加载界面、菜单/托盘、工作目录管理。

## 目录与数据
| 位置 | 内容 |
|---|---|
| `resources\app-server\` | 后端打包产物（只读，来自 `dist\server`） |
| `%LOCALAPPDATA%\OA-Agent`（便携版：程序旁 `data\`；`OA_AGENT_DATA_DIR` 可覆盖） | 数据目录：`pg\` 数据库、`spool\`、`logs\`（`desktop.log`、`oa-agent.log`、`postgres.log`、`worker.log`）、`electron\`（浏览器缓存）、`desktop-config.json` |
| 归档文件夹（默认 `文档\OA-Agent\归档`） | 归档结果，可自定义 |

引导文件（固定在 `%LOCALAPPDATA%\OA-Agent`）：`datadir.json`（自定义数据目录的指针）、`install-preset.ini`（安装向导选择的工作目录，首次运行读取）。

环境变量（测试/运维）：`OA_AGENT_DATA_DIR`、`OA_AGENT_ARCHIVE_ROOT`、`OA_AGENT_SERVER_DIR`、`OA_AGENT_BOOTSTRAP_DIR`、`OA_AGENT_START_TIMEOUT_MS`。

## 启动画面（splash）
应用就绪后第一件事就是创建启动窗口（无边框 480×320、置顶、不进任务栏，约 300 ms 内可见），再启动后端。
- 本地 `splash.html`，只用内联 CSS/SVG，无网络与外部字体；跟随系统深色模式；`prefers-reduced-motion` 时关闭动画。
- 阶段进度由后端真实日志驱动：`starting embedded PostgreSQL` → 正在启动数据库；`starting document worker` → 正在启动文档识别服务；`database migrations applied` → 正在准备数据；`OA API listening` → 正在启动服务；`/healthz` 通过并完成本机初始化 → 正在加载界面。每个阶段最短显示 450 ms；9 秒内进度不前进则切换为不定长进度条；20 秒后提示“首次启动需要初始化数据库，可能需要一两分钟”。
- 交接：主窗口先隐藏加载界面，`did-finish-load` 后才显示并关闭启动窗口；界面加载失败则保留启动窗口并显示错误。
- 失败/超时：启动窗口切换为错误态，按钮“重试 / 查看日志 / 数据文件夹 / 退出”（通过只给启动窗口使用的 preload + IPC，`contextIsolation` 开启，不暴露 Node）。错误会按日志识别：端口占用、安全软件拦截、安装文件缺失、数据库损坏、磁盘空间不足。
- 退出时同一窗口显示“正在安全停止服务…”。

## 工作目录（归档文件夹）与数据目录
- 首次运行（或安装向导没有选择时）弹出选择窗口：默认位置、剩余空间、校验（绝对路径、可写、非系统目录、不在安装目录/数据目录内；网络位置/可移动磁盘警告），“使用默认位置”按钮；“高级：数据目录”可展开。便携版默认在程序旁 `归档\`。
- “桌面版设置…”窗口（左下角用户菜单、设置页的“桌面版”卡片、托盘图标，快捷键 Ctrl+,）里的“更改…”：选择新目录后二选一——**迁移**（复制到新位置，目标非空时合并、绝不覆盖同名文件；逐文件校验大小并做最终整体校验，全部成功才切换；旧目录默认保留，完成后询问是否删除）或**直接使用新位置**（不复制，台账中找不到的文件显示为缺失；空目录会明确警告）。流程：停止后端（含子进程）→（复制）→写入配置 → 以新 `OA_ARCHIVE_ROOT` 重启 → 重新加载界面；复制或重启失败则自动回滚到旧目录并重启。
- “桌面版设置…”窗口里的数据目录“迁移…”：先安全停止内置数据库，复制并校验后写入 `datadir.json`，自动重启程序；旧目录保留。便携模式或设置了 `OA_AGENT_DATA_DIR` 时不可用。
- 关于对话框与“打开归档文件夹”菜单项显示当前工作目录。

## 开发与测试
```
cd desktop && npm ci
npm test                      # 纯 Node 单测（校验、复制校验、切换/回滚、错误识别、环境清理）
npm start                     # 需要 dist\server（scripts\build-server-bundle.ps1）
SMOKE_EXE=... SMOKE_WORK=... node test/smoke.mjs   # CDP 冒烟（见 test/smoke.mjs 顶部说明）
npm run icons                 # 由 build/icon.svg 重新生成 icon.png/icon.ico（需要 Edge/Chrome）
```
打包见 `docs/构建与发布.md`。

## 设置界面（没有原生菜单栏）
窗口不再显示原生的“文件/视图/窗口/帮助”菜单栏。桌面专属设置集中在一个“设置”窗口里：工作目录（更改…，可迁移）、数据目录（迁移…）、登录 Windows 时自动启动、关闭窗口时最小化到托盘、固定本机服务端口、打开日志 / 复制诊断信息。入口：应用左下角用户菜单 → “桌面版设置…”、设置页顶部的“桌面版（本机）”卡片、托盘图标 → “设置…”、快捷键 `Ctrl+,`。所有选择保存在数据目录的 `desktop-config.json`（自动启动保存在系统登录项），**下次启动不会再询问**；已保存的工作目录暂时不可用（磁盘未连接等）时，只弹出“重试 / 更改 / 退出”，不会清除设置。快捷键：`F5`/`Ctrl+R` 刷新、`Ctrl+=` `Ctrl+-` `Ctrl+0` 缩放、`F11` 全屏。网页只能通过 `window.oaDesktop`（`src/app-preload.js`）调用三个固定的、校验来源的接口。

## 密钥窗口（API Key 的一切都在应用内完成）
设置页的大模型卡片在桌面版里通过 `window.oaDesktop.openKeyHelper(providerId, { mode })` 打开一个应用自带的窗口（`src/keyhelper-ctl.js`；纯逻辑 `src/keyhelper-core.js`；面板 `keyhelper.html` / `keyhelper.js` / `keyhelper-preload.js`），不再把用户送到系统浏览器：

- `create`：服务商真实的密钥页（沙箱 `WebContentsView`，无 preload，独立持久分区 `persist:oa-keyhelper-<id>`）+ 右侧指引面板；剪贴板里出现符合该服务商格式的密钥时在面板里显示尾号，用户点“使用这个密钥”后才经 `onKeyHelper` 事件交给设置表单。
- `login`：OpenRouter 授权页（授权地址由设置页从后端取得，主进程校验主机白名单和 `callback_url` 必须是本应用回环回调）；识别到回调后窗口自动关闭，设置页轮询状态。
- `manage`：已保存密钥的管理视图；窗口通过 `keyhelper:event` 的 `request` 事件请设置页执行 状态/测试/删除，设置页用 `keyHelperReply` 回复（回复里没有密钥）。
- 数据来源：服务商、`key_url`、`key_hosts` 白名单、`key_pattern`、`key_guide` 全部来自本机后端的 `GET /v1/llm/presets`（`backend/internal/llm/presets.json`），网页只传预设 id。白名单、IPC 校验、权限、下载、弹窗、剪贴板、日志都有单元测试（`test/keyhelper-core.test.js`）。
- 测试开关：`OA_KEYHELPER_TEST_HOSTS=127.0.0.1:<端口>`（只接受回环 `host:port`，只在主进程环境变量里读取，打包版忽略）。真机端到端：`node test/e2e-keyhelper.mjs`（需要 `KH_ELECTRON`、`KH_SERVER`、`KH_WORK`，说明见脚本头部；会占用并清空 Windows 剪贴板）。
- 隐私与白名单说明见 `docs/LLM账号登录配置.md` §3.6。
