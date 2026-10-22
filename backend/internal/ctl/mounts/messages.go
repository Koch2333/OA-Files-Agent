package mounts

import "fmt"

// messages is the single place for the user-facing text of this package (Chinese; keyed so an
// English table can be added next to it).
var messages = map[string]string{
	"bad_name":         "名称 %q 不合法：只能用小写字母、数字、下划线和连字符，以字母或数字开头，最长 31 个字符",
	"reserved_name":    "名称 %q 是保留名称，请换一个",
	"empty":            "路径不能为空",
	"not_abs":          "必须是绝对路径（以 / 开头）：%s",
	"dotdot":           "路径不能包含 “..”：%s",
	"bad_char":         "路径 %s 含有不允许的字符 %q（只允许字母、数字和 _ . / -，与安装脚本一致）",
	"root":             "不能把根目录 / 作为挂载路径",
	"forbidden":        "%s 位于系统目录 %s 之下，拒绝挂载",
	"forbidden_parent": "%s 包含系统目录 %s，拒绝挂载",
	"install_dir":      "%s 包含安装目录 %s（其中的 .env 存放密钥），拒绝挂载",
	"dup_name":         "名称 %q 重复",
	"bad_container":    "容器内路径 %q 不合法，应为 %q",
	"dup_container":    "容器内路径 %s 已被 %s 使用",
	"overlap":          "%s（%s）与 %s（%s）互相嵌套或重合，会造成数据混杂，不允许",

	"missing":       "目录不存在：%s",
	"not_dir":       "%s 不是目录",
	"owner":         "属主为 %d:%d，容器内进程使用 %d:%d，可能无权读写",
	"pg_owner":      "PostgreSQL 数据目录属主为 %d:%d，必须是 %d:%d，否则数据库无法启动",
	"pg_mode":       "PostgreSQL 数据目录权限为 %o，应为 700（或 750）",
	"not_readable":  "容器用户（uid %d）对 %s 没有读取权限",
	"not_writable":  "容器用户（uid %d）对 %s 没有写入权限，但该挂载是读写模式",
	"fs_type":       "%s 位于 %s 文件系统上：%s",
	"fs_pg":         "数据库放在 %s 上不可靠（可能丢数据或损坏），建议使用本地磁盘（ext4/xfs）",
	"fs_volatile":   "重启后内容会丢失，不适合存放归档数据",
	"fs_net":        "网络/FUSE 文件系统，权限与锁语义可能不同，且断网时容器会卡住",
	"low_space":     "剩余空间仅 %.1f GiB（建议至少 20 GiB）",
	"probe_failed":  "无法检查 %s：%v",
	"rw_default_ro": "外部目录默认以只读方式挂载；要允许写入请显式使用 --rw",

	"archive_relocate": "归档目录将从 %s 改为 %s，而旧目录里已有数据。本工具【不会】移动或删除任何文件：" +
		"台账（数据库）里记录的文件仍在旧目录，改指向新目录后这些文件会“消失”（预览、导出、回滚都会失败）。" +
		"请先把旧目录内容复制到新目录（例如 cp -a '%s/.' '%s/'，或使用 --copy），确认无误后再切换",
	"archive_new_empty": "新的归档目录 %s 是空的，旧目录 %s 的数据不会随之出现",
	"archive_new_dirty": "新的归档目录 %s 已有内容，请确认其中的文件与台账一致，否则会和旧数据混杂",
	"pgdata_running":    "数据库数据目录只能在服务全部停止后更改（当前有运行中的容器：%s）。先运行 oa-agentctl stop",
	"pgdata_relocate": "数据库目录将从 %s 改为 %s。本工具不会移动数据库文件：新目录若为空，应用会初始化一个【全新的空数据库】，" +
		"现有账号与台账将无法使用。要迁移请先 stop，再 cp -a '%s/.' '%s/'，核对后再切换",
	"pgdata_new_has": "新数据库目录 %s 已有内容，将直接使用其中的数据库（请确认它来自同一套部署）",
	"app_relocate":   "应用数据目录将从 %s 改为 %s。其中的 secret.key 用于解密已保存的凭据；新目录没有该文件时已保存的存储凭据将无法解密",
	"spool_relocate": "暂存目录将从 %s 改为 %s；旧目录中尚未处理完的文件不会被迁移",
	"ro_to_rw":       "挂载 %s 将从只读改为读写：容器内程序将能修改或删除该目录中的文件",
	"no_change":      "没有变化",
}

func msg(code string, args ...any) string {
	f, ok := messages[code]
	if !ok {
		return code
	}
	return fmt.Sprintf(f, args...)
}

// Msg exposes the table to other packages (CLI/TUI) for codes they raise themselves.
func Msg(code string, args ...any) string { return msg(code, args...) }
