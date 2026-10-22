package tui

// All user-visible TUI text lives here (Chinese). An English table can replace these variables
// without touching the screens.
var (
	sAppTitle      = "OA Agent 管理"
	sQuit          = "退出"
	sBack          = "返回"
	sTooSmall      = "终端太小：需要至少 80x24（当前 %dx%d），请放大窗口"
	sNotTTY        = "标准输入/输出不是终端，无法启动交互界面。请使用子命令（oa-agentctl help）。"
	sLoading       = "加载中 …"
	sErrPrefix     = "错误："
	sPressAnyKey   = "按任意键继续"
	sHelpMenu      = "↑/↓ 选择  Enter 进入  1-8 直达  q 退出"
	sHelpList      = "↑/↓ 选择  r 刷新  Esc 返回"
	sHelpRun       = "Esc 返回（不会中断已发出的 docker 操作）"
	sHelpConfirm   = "输入确认词后按 Enter  Esc 取消"
	sHelpForm      = "Tab 补全/切换  Enter 提交  Esc 取消"
	sRunning       = "运行中 …"
	sDone          = "完成"
	sFailed        = "失败"
	sTypeToConfirm = "请输入 %s 确认："
	sYNConfirm     = "确认？ y 是 / n 否"

	menuItems = []string{
		"仪表盘", "用户与密码", "存储挂载", "服务管理", "日志", "备份与还原", "配置", "诊断",
	}

	// dashboard
	sDashServices = "服务"
	sDashDisk     = "磁盘"
	sDashSetup    = "初始化状态"
	sDashURL      = "访问地址"
	sColService   = "服务"
	sColState     = "状态"
	sColHealth    = "健康"
	sColPorts     = "端口"
	sColUptime    = "运行时间"

	// users
	sUsersHelp       = "↑↓ 选择 p 重置密码 m 重置MFA u 解锁 d 停用 a 新建 r 刷新 Esc 返回"
	sColUser         = "用户名"
	sColRole         = "角色"
	sColStatus       = "状态"
	sColMFA          = "MFA"
	sColLocked       = "锁定"
	sSecretTitle     = "临时密码（仅显示这一次）"
	sSecretHint      = "按任意键继续——密码不会再次显示"
	sSecretUnparsed  = "无法识别密码，以下为 oa-admin 原始输出（仅显示这一次）"
	sNewAdminPrompt  = "新管理员用户名："
	sConfirmReset    = "重置 %s 的密码，旧密码立即失效。"
	sConfirmDisable  = "停用 %s，其所有会话将失效。"
	sConfirmMFA      = "清除 %s 的 TOTP/Passkey/恢复码，下次登录需重新绑定。"
	sConfirmUnlock   = "解锁 %s。"
	sConfirmCreate   = "创建管理员 %s，并生成一次性临时密码。"
	sActionDone      = "%s：完成"
	sNoUsers         = "没有用户（或 oa-admin 暂不可用）"
	sUsersUnavail    = "无法获取用户列表：%v"
	sYes, sNo        = "是", "否"
	sMounts          = "存储挂载"
	sMountsHelp      = "↑↓ 选择 a 添加 e 修改 x 移除 A 应用并重启 r 刷新 Esc 返回"
	sColName         = "名称"
	sColHost         = "主机路径"
	sColContainer    = "容器路径"
	sColMode         = "模式"
	sColFree         = "剩余"
	sColFS           = "文件系统"
	sFormName        = "名称"
	sFormPath        = "主机路径"
	sFormMode        = "模式（空格切换）"
	sModeRO          = "只读"
	sModeRW          = "读写"
	sReviewTitle     = "确认变更"
	sReviewHelp      = "Enter/y 应用并重启  w 仅写入不重启  Esc 取消"
	sReviewAckNeeded = "此变更涉及已有数据，不会移动或删除任何文件。请输入 %s 以继续，或按 Esc 取消。"
	sReviewBlocked   = "存在必须先解决的错误，无法应用。按 Esc 返回修改。"
	sRolledBack      = "应用失败，已回滚到之前的配置。"
	sApplied         = "挂载已应用。"
	sPending         = "有尚未应用的变更（按 A 应用并重启）"
	sMissingDirHint  = "目录不存在或属主不符；请先以 root 执行：%s"
	sNoExtras        = "尚无外部目录挂载。按 a 添加（默认只读）。"
	sRemoveConfirm   = "移除外部挂载 %s（主机上的数据不受影响）"

	// services
	sSvcMenu      = []string{"启动全部", "停止全部", "重启全部", "重启 api+worker", "升级（拉取镜像并重启）"}
	sSvcHelp      = "↑/↓ 选择  Enter 执行  Esc 返回"
	sSvcStopWord  = "STOP"
	sSvcUpWord    = "UPGRADE"
	sSvcUpConfirm = "升级会先备份、拉取镜像并重启；数据库迁移不会自动回退。"

	// logs
	sLogsPick   = "选择服务（Enter 查看）"
	sLogsHelp   = "↑↓/PgUp/PgDn 滚动 f 跟随 / 搜索 n/N 下一个/上一个 Esc 返回"
	sLogsSearch = "搜索："
	sLogsFollow = "跟随"
	sLogsPaused = "已暂停"

	// backup
	sBackupMenu    = []string{"立即备份（含归档）", "立即备份（不含归档）", "从备份还原 …"}
	sRestoreWord   = "RESTORE"
	sRestoreWarn   = "还原会覆盖当前数据库与归档目录（旧归档目录改名保留）。"
	sNoBackups     = "备份目录中没有找到 oa-agent-backup-*.tar.gz"
	sPickBackup    = "选择要还原的备份"
	sBackupWarning = "备份含加密密钥与数据库口令，请像密码一样保管。"

	// config
	sConfigHelp   = "↑↓ 选择 e 修改 d 域名/HTTPS Esc 返回"
	sConfigEdit   = "新值（Enter 保存，空值清空）："
	sConfigSaved  = "已保存 %s；重启后生效（服务管理 → 重启）"
	sConfigLocked = "此键不可通过界面修改（密钥/路径请用专用命令）"
	sDomainPrompt = "域名（如 oa.example.com，留空表示改回 HTTP）："
	sDomainWarn   = "更换域名会使已注册的 Passkey 失效（绑定在旧域名上），需在 设置→安全 中更新 RP ID/来源；TOTP 不受影响。需要 80/443 端口对公网开放且域名已解析到本机。"
	sDomainWord   = "DOMAIN"

	// doctor
	sDoctorHelp    = "↑/↓ 滚动  r 重新诊断  Esc 返回"
	sDoctorSummary = "诊断完成：%d 项失败，%d 项警告"
)
