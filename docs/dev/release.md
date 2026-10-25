# 发布流程

发布由 `v<版本>` 标签触发（`.github/workflows/release.yml`）。完整说明、Windows ARM64 决策表与回滚见[构建与发布](/构建与发布)，本页是摘要。

## 发布产物

资产名只定义在 `scripts/release-assets.sh`（版本号不带 `v`），共 14 个文件：

| 资产 | 说明 |
|---|---|
| `oa-agent-<版本>-windows-{x64,arm64}-{setup.exe,portable.zip,server.zip}` | Windows 安装版 / 便携版 / 仅服务端（arm64 实验性） |
| `oa-agent-<版本>-linux-{amd64,arm64}.tar.gz` | Linux 分架构安装包 |
| `install.sh` | Linux 一键安装脚本 |
| `oa-agentctl-linux-{amd64,arm64}` | 管理工具（静态 Go 二进制） |
| `oa-agent-source.tar.gz` 与 `.sha256` | 源码 + 部署文件 |
| `SHA256SUMS` | 以上全部资产的校验和 |

容器镜像（`api`、`worker`、`deploy`）由 `.github/workflows/images.yml` 在每次推送 / 标签时构建并推送，凭据来自 `ACR_*` Actions 的 secrets / variables（不在仓库里；PR 与 fork 只构建不推送）。

## 步骤

1. 修改根目录 `VERSION`（当前 `0.5.0`），更新文档，提交并推送到 `main`，等 `check.yml` 通过。
2. 建议先演练：Actions → `Release` → Run workflow，保持 `dry_run` 勾选，只上传 workflow 产物，不发布。
3. 打标签并推送：`git tag v0.5.0 && git push origin v0.5.0`。**标签必须等于 `VERSION`**（`release-check.sh` 校验；格式 `v主.次.修[-后缀]`，不允许 `+`）。含 `-` 的版本自动标为预发布。
4. 工作流依次：`prepare`（校验）→ `windows`（x64 / arm64 矩阵）与 `linux` → `release`（唯一拥有 `contents: write`，先建草稿、上传并核对全部资产、全部正确才公开）。
5. 在 Releases 页检查安装、解压与校验和。

## 要点与限制

- 重跑、演练、手动发布、撤回发布的做法见[构建与发布](/构建与发布)。版本号不要复用，修正后用新补丁号。
- Windows 签名是可选的：添加 Actions secrets `CSC_LINK` 与 `CSC_KEY_PASSWORD` 才会签名；否则发布说明里已标注 SmartScreen 提示。
- Windows ARM64：主程序与后端原生 arm64，Worker 与内置 PostgreSQL 是 x64（仿真）；**未在真实 ARM64 硬件上测试**。
- 不提供 riscv64。
- 离线验证发布脚本：`bash scripts/test/release.sh`、`bash scripts/test/linux-bundles.sh`。
- 发布前请查看[第三方组件与许可证](/THIRD-PARTY-LICENSES)（PyMuPDF 为 AGPL-3.0）。

## 本文档站

文档站由 `.github/workflows/docs.yml` 构建：推送到 `main` 且改动 `docs/**` 时部署到 GitHub Pages，PR 只构建不部署。需要在仓库 Settings → Pages 把 Source 设为 “GitHub Actions”。站点的 `base` 取自环境变量 `DOCS_BASE`（工作流设为 `/<仓库名>/`，本地默认 `/`）。
