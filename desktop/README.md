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
- 菜单“文件 → 更改工作目录…”（托盘也有）：选择新目录后二选一——**迁移**（复制到空文件夹，逐文件校验大小并做最终整体校验，全部成功才切换；旧目录默认保留，完成后询问是否删除）或**直接使用新位置**（不复制，台账中找不到的文件显示为缺失；空目录会明确警告）。流程：停止后端（含子进程）→（复制）→写入配置 → 以新 `OA_ARCHIVE_ROOT` 重启 → 重新加载界面；复制或重启失败则自动回滚到旧目录并重启。
- 菜单“文件 → 迁移数据目录…”：先安全停止内置数据库，复制并校验后写入 `datadir.json`，自动重启程序；旧目录保留。便携模式或设置了 `OA_AGENT_DATA_DIR` 时不可用。
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
