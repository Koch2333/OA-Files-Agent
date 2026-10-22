# 存储连接（云存储 / 企业内部存储）设计

可选功能：默认不配置任何连接时行为与以前完全一致。连接用于两个方向：**从远端导入**（浏览远端目录，选文件/文件夹，拉取到本机后走原有识别/审批流程）和**归档后同步到远端**（归档仍先写本机，再把成品文件**复制**到远端，只增不改不删）。

## 支持的存储类型（`internal/cloud` 目录，Kind.ID）
| ID | 说明 | 设置项（Values） | 凭据（Secrets，加密保存） |
|---|---|---|---|
| `s3` | S3 兼容：AWS S3、阿里云 OSS、腾讯云 COS、华为云 OBS、MinIO、Cloudflare R2、GCS（S3 互操作 HMAC 密钥）等 | `endpoint`（留空=AWS）、`region`、`bucket`*、`use_ssl`(true/false，默认 true)、`path_style`(auto/true/false，默认 auto：OSS/COS/OBS 用虚拟主机风格，IP/MinIO 用路径风格)、`insecure_skip_verify` | `access_key`*、`secret_key`*、`session_token` |
| `azure_blob` | Azure Blob Storage（含世纪互联 `.blob.core.chinacloudapi.cn`） | `account`*、`container`*、`endpoint`（可选）、`auth`(account_key/sas，默认 account_key) | `account_key`（auth=account_key）/`sas_token`（auth=sas） |
| `webdav` | WebDAV：Nextcloud/ownCloud、群晖、SharePoint 文档库、**OpenList/AList 网关（`http://host:5244/dav`）** | `url`*（完整 URL）、`auth`(basic/bearer，默认 basic)、`username`、`insecure_skip_verify` | `password`（basic）/`token`（bearer） |
| `sftp` | SFTP（SSH） | `host`*、`port`(22)、`username`*、`auth`(password/key，默认 password)、`host_key`（**托管**：已信任的主机密钥指纹 SHA256，首次连接由用户确认后写入） | `password`/`private_key`(textarea)/`passphrase` |
| `ftp` | FTP / FTPS | `host`*、`port`(21)、`tls`(none/explicit/implicit，默认 explicit)、`username`、`insecure_skip_verify` | `password` |
| `smb` | SMB/CIFS 共享（Windows 文件服务器、NAS）；仅 SMB2/3 | `host`*、`port`(445)、`share`*、`domain`、`username`* | `password` |
| `dingtalk` | 钉钉钉盘（企业存储）：企业内部应用 API | `app_key`*、`space_id`*、`union_id`*（操作人 unionId）、`endpoint`（默认 https://api.dingtalk.com） | `app_secret`* |

通用设置（不属于具体类型，由服务层管理）：`name`、`base_path`（起始目录，所有浏览/导入/同步都被限制在其内）、`enabled`、`use_for_import`、`replicate`(off/manual/auto)。

## 接口契约
见 `backend/internal/cloud/cloud.go`：`Provider{List, Stat, Open, PutNew, Remove, Close}`、`Kind`/`Field`（驱动前端动态表单）、`Validate`、`CleanRel`、`SafeDialer`、`CheckHost`、`Probe`、`Describe`。
- **每个提供者文件用 `init()` 调 `cloud.Register(cloud.Kind{…})`**，字段表与上表一致（键名一字不差）。
- **所有出站连接必须使用 `cloud.SafeDialer(timeout).DialContext`**（HTTP 用 `http.Transport{DialContext: …}`，SSH/FTP/SMB 先用它拨号再握手），禁止直接 `net.Dial`。主机名由 `cloud.CheckHost` 预检。
- 路径一律相对 `Settings.BasePath`，用 `cloud.Join(base, rel)` 拼接；调用方已用 `CleanRel` 校验，提供者仍要防止越出根（例如对 `..` 再次拒绝）。
- `PutNew`：目标已存在 → `ErrExists` 且不得改动；先写临时名再原子重命名/或使用“不覆盖”条件头（S3 `If-None-Match: *`，Azure `If-None-Match: *`，WebDAV `If-None-Match: *`/先 `Stat`，SFTP/SMB/FTP 先 `Stat` + 写临时文件 + 无覆盖重命名）；创建父目录；大小以 `size` 为准，写完校验远端大小。
- 错误映射：认证失败 → `ErrAuth`，权限 → `ErrPermission`，不存在 → `ErrNotFound`，已存在 → `ErrExists`，连不上/超时 → `ErrUnreachable`；**错误信息不得包含凭据、完整响应体、内网地址细节**（用 `%w` 包装哨兵错误，原始错误不外泄）。
- TLS 默认校验证书；只有 `insecure_skip_verify=true` 才跳过。SFTP 必须校验 `host_key`：未固定 → 返回 `*cloud.HostKeyError{Fingerprint}`（不连接数据通道）；不一致 → `HostKeyError{Changed:true}`。
- 每次调用遵守 `ctx`；默认单次操作超时 30 s（传输按数据量放宽）；`List` 单次最多返回 5000 项（超出截断）。
- 测试：每个提供者必须有不依赖外部服务的单元测试（进程内假服务器：S3 用 `gofakes3`，WebDAV 用 `golang.org/x/net/webdav` 服务端，SFTP 用 `pkg/sftp` 内置服务端 + `x/crypto/ssh`，FTP 用 `ftpserverlib`，SMB/Azure/钉钉用 `httptest`/可得的最佳替身并在报告里如实说明覆盖范围）。

## 本地目录 / 挂载目录（`localdir`）
- 用途：Linux 服务器（Docker）上，运维把 NAS/NFS/磁盘目录 bind-mount 到 api 容器的 `/mnt/storage/<name>`，即可作为导入来源与同步目标。设置项：`path`*（绝对路径）、`read_only`（`false`/`true`，默认 `false`；`true` 时 `PutNew`/`Remove` 返回 `ErrPermission`，用于只导入的挂载）。无凭据。
- 允许的根目录**只来自服务器环境变量 `OA_LOCAL_ROOTS`**（按 `os.PathListSeparator` 分隔的绝对路径；Linux 为 `:`）。未配置时目录里仍列出该类型，但创建连接一律失败：“服务器未配置可用的本地挂载目录（OA_LOCAL_ROOTS）”。用户填的 `path` 经 `EvalSymlinks`+`Clean` 后必须位于某个允许根之内（或等于它），并且与 `OA_ARCHIVE_ROOT`、`OA_SPOOL_ROOT`、数据目录（`OA_SPOOL_ROOT` 的上级目录）**双向都不得重叠**。
- 访问限制：所有读写经 `os.Root`（openat 风格，内核级限制在连接目录内，`..` 与越界符号链接直接拒绝），并逐级 `Lstat`：任何符号链接组件一律拒绝（不跟随，哪怕指向根内）；以 `.` 开头的名称不可见/不可访问（仅连接测试的 `.oa-agent-write-test-*` 标记文件例外）；`Open` 打开后再 `fstat` 复核必须是普通文件（设备/FIFO/套接字拒绝，`O_NONBLOCK` 防阻塞）；`List` 跳过隐藏项、符号链接与非普通文件。
- `PutNew`：父目录 0750；在目标目录写隐藏临时文件 `.oa-agent-tmp-*`（0640），校验写入字节数等于 `size`，`fsync` 后用 `link(2)` 发布（目标已存在 → `ErrExists`，永不覆盖），再删除临时名并 `fsync` 目录；失败/取消/大小不符都会清理临时文件。`Remove` 只删除单个普通文件（不删目录、不删符号链接）。
- 残余风险：Lstat 与打开之间的竞态只可能把组件换成指向连接目录**内部**的符号链接（`os.Root` 仍保证无法越界）；连接目录在连接建立后被替换挂载点不在本包控制范围内。建议挂载时加 `nosuid,nodev`，仅导入的挂载同时用只读挂载和 `read_only=true`。

## 钉钉钉盘（官方 API，企业内部应用）
- 凭据：企业内部应用的 AppKey/AppSecret；换取 accessToken：`POST https://api.dingtalk.com/v1.0/oauth2/accessToken`（body `{"appKey","appSecret"}` → `accessToken`、`expireIn`）；之后所有请求带头 `x-acs-dingtalk-access-token`。缓存 token 至过期前 5 分钟。
- 所有业务调用需 query `unionId`（操作人，须是组织成员，且对空间有相应权限）；应用需开通企业存储读/写权限（`Storage.File.Read`/`Storage.File.Write`）并配置服务器 IP 白名单。仅企业内部应用可用；**标准版下载信息接口每月 10,000 次调用额度**——导入大批量文件时要提示。
- 已核对：`GET /v1.0/storage/spaces/{spaceId}/dentries?parentId&unionId&maxResults&nextToken`（分层列表）；`POST /v1.0/storage/spaces/{spaceId}/dentries/{dentryId}/downloadInfos/query?unionId` → `{protocol:"HEADER_SIGNATURE", headerSignatureInfo:{resourceUrls[], headers{}, expirationSeconds}}`，下载即对 `resourceUrls[0]` 发 GET 并带返回的 headers；`POST /v2.0/storage/spaces/files/{parentDentryUuid}/uploadInfos/query?unionId`（body `{protocol:"HEADER_SIGNATURE", option:{storageDriver:"DINGTALK", preCheckParam:{name,size}}}` → `uploadKey` + `resourceUrls/headers`）→ `PUT` 内容到 `resourceUrls[0]`（带 headers）→ `POST /v2.0/storage/spaces/files/{parentDentryUuid}/commit?unionId`（body `{uploadKey,name,option:{conflictStrategy}}`，**必须用 `RETURN_ERROR_IF_EXISTS` 以保证不覆盖**；其它取值 `AUTO_RENAME`/`OVERWRITE`/`RETURN_DENTRY_IF_EXISTS`）。
- 需要实现者自行核对官方文档（help.dingtalk.io 上各页面的 `.md` 版本可读）：根目录的 `parentId` 取值、`dentryId`↔`dentryUuid` 的转换接口、新建文件夹接口、列表分页上限、错误码；文档与此处不一致时以官方为准并在报告说明。
- 路径→ID：钉盘按 ID 寻址，提供者在内存中按路径逐级列表解析并缓存（TTL 60 s）；同名文件夹/文件冲突时报错而不是猜测。

## OpenList（AList 社区分支）怎么用
- OpenList 是 AGPL-3.0 的多存储聚合程序：自带数十种驱动（阿里云盘、百度网盘、123 云盘、OneDrive/SharePoint、Google Drive、Dropbox、Azure Blob、S3、SMB、FTP/SFTP 等），并对外提供 WebDAV。
- **集成方式 = 网关，不嵌入代码**：用户自行部署 OpenList，本系统用 `webdav` 类型连接它的 `/dav`，即可间接访问其全部驱动（含个人网盘）。这样既无需逐个实现 OAuth 类网盘，也避免把 AGPL 代码并入本项目。
- 注意：OpenList 的钉钉驱动只覆盖“钉钉文档（alidocs）”，**不是钉盘**；钉盘走本系统的 `dingtalk` 官方 API 类型。
- 局限：经网关的传输速度/稳定性取决于上游网盘；个人网盘 Cookie/Token 过期需在 OpenList 侧维护；不建议将个人网盘用于涉密文件。

## 服务层（`internal/storage`）
- 表 `storage_connections(id, name, kind, settings JSONB, secrets_enc BYTEA, base_path, enabled, use_for_import, replicate, host_key, created_at, updated_at)`；凭据用 `secrets.Box` 加密，永不返回，仅返回 `secrets_set{key:bool}`。
- 远端导入：`POST /v1/imports/remote`；同步：表 `replications`，后台工作者带指数退避重试，只增不覆盖不删除（远端同名且大小一致→标记已存在；大小不同→追加序号名）；归档后（`auto`）自动入队，`manual` 由用户触发。移动/删除/撤销归档**不会**传播到远端。
- 审计：创建/修改/删除连接、测试、导入、同步均写审计（不含凭据）。
