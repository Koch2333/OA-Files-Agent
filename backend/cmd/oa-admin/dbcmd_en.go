package main

import (
	"regexp"
	"strings"
)

// The checklist texts of internal/dbadmin are Chinese (they also feed the web UI). For db-check/db-init run with OA_LANG=en the
// CLI translates the known fixed phrases; anything it does not know stays as it is (never an invented translation).
var titleEN = map[string]string{
	"connect": "Connection", "schema": "Target database / schema", "migration": "Schema version", "isolation": "Isolation (dedicated database/schema)",
	"version": "Server version", "charset": "Character set / collation", "timezone": "Time zone", "perm_schema": "Permissions",
	"least_privilege": "Least privilege", "compat": "Compatibility level / collation",
}

type phrase struct {
	re *regexp.Regexp
	en string
}

func p(re, en string) phrase { return phrase{regexp.MustCompile(re), en} }

var detailEN = []phrase{
	p(`^延迟 (\d+) ms$`, "latency $1 ms"),
	p(`^已初始化，版本 (\d+)$`, "initialised, version $1"),
	p(`^目标库为空，需要管理员批准后初始化$`, "the target is empty; the schema is created only after the administrator approves it"),
	p(`^上次迁移未完成（dirty），需要先恢复$`, "the last migration did not finish (dirty); recover it first"),
	p(`^版本 (\d+)，需要升级到 (\d+)（需要管理员批准）$`, "version $1, needs an upgrade to $2 (needs the administrator's approval)"),
	p(`^(.+) 中存在非 OA Agent 的表（(.*)）。请使用专用数据库或专用 schema（空的或仅包含 OA Agent 表）；用表名前缀不算隔离。$`, "$1 contains tables that do not belong to OA Agent ($2). Use a dedicated database or schema (empty, or holding only OA Agent tables); a table-name prefix is not isolation."),
	p(`^(.+) 仅包含 OA Agent 的表（同库其他 schema 有其他应用的表，不会被触碰）$`, "$1 holds only OA Agent tables (other schemas of this database belong to other applications and are never touched)"),
	p(`^(.+) 仅包含 OA Agent 的表（专用数据库）$`, "$1 holds only OA Agent tables (dedicated database)"),
	p(`^PostgreSQL (.+) 低于最低要求 14$`, "PostgreSQL $1 is below the minimum 14"),
	p(`^UTF8；比较区分大小写（搜索使用不区分大小写的 ILIKE）$`, "UTF8; comparisons are case-sensitive (searches use case-insensitive ILIKE)"),
	p(`^数据库编码为 (.+)，需要 UTF8$`, "the database encoding is $1, UTF8 is required"),
	p(`^连接固定使用 UTC（当前会话 (.+)），时间一律以 UTC 存储$`, "connections are pinned to UTC (current session $1); times are always stored in UTC"),
	p(`^搜索路径中没有可用的 schema$`, "no usable schema in the search path"),
	p(`^账号对 schema (.+) 没有 USAGE 权限$`, "the account has no USAGE privilege on schema $1"),
	p(`^账号对 schema (.+) 没有 CREATE 权限，无法建表$`, "the account has no CREATE privilege on schema $1, so it cannot create tables"),
	p(`^账号没有 CREATE 权限：可运行，但无法执行后续升级迁移$`, "the account has no CREATE privilege: it can run, but cannot apply later upgrade migrations"),
	p(`^对 schema (.+) 具有 USAGE/CREATE$`, "USAGE/CREATE on schema $1"),
	p(`^账号拥有 (.+)，权限超出所需。建议使用只拥有专用 schema 的普通账号。$`, "the account has $1, more than needed. Use an ordinary account that owns only the dedicated schema."),
	p(`^账号不是超级用户，也没有 CREATEROLE / CREATEDB$`, "the account is not a superuser and has no CREATEROLE / CREATEDB"),
	p(`^检测到 MariaDB（(.+)），未支持/未测试；请使用 MySQL 8.0.16\+$`, "MariaDB detected ($1): not supported or tested; use MySQL 8.0.16+"),
	p(`^无法解析版本号 (.+)$`, "cannot parse the version number $1"),
	p(`^MySQL (.+) 低于最低要求 8.0.16（需要 CHECK 约束、函数索引、SKIP LOCKED）$`, "MySQL $1 is below the minimum 8.0.16 (CHECK constraints, functional indexes and SKIP LOCKED are required)"),
	p(`^(.+) / (.+)（表在创建时显式使用 utf8mb4，支持 emoji；默认排序规则不区分大小写，路径与用户名列使用二进制排序规则）$`, "$1 / $2 (tables explicitly use utf8mb4, emoji are supported; the default collation is case-insensitive, path and user-name columns use a binary collation)"),
	p(`^数据库默认字符集为 (.+)；OA Agent 的表显式使用 utf8mb4，但建议把库也设为 utf8mb4$`, "the database default character set is $1; OA Agent tables explicitly use utf8mb4, but setting the database to utf8mb4 is recommended"),
	p(`^会话固定为 UTC（(.+)），事务隔离 (.+)$`, "the session is pinned to UTC ($1), transaction isolation $2"),
	p(`^没有选择数据库$`, "no database selected"),
	p(`^不能使用系统库 (.+)；请创建专用数据库$`, "the system database $1 cannot be used; create a dedicated database"),
	p(`^无法读取 SHOW GRANTS：(.*)$`, "cannot read SHOW GRANTS: $1"),
	p(`^账号在数据库 (.+) 上缺少权限：(.*)$`, "the account lacks privileges on database $1: $2"),
	p(`^账号缺少 (.+)：可运行，但无法执行升级迁移$`, "the account lacks $1: it can run, but cannot apply upgrade migrations"),
	p(`^账号在数据库 (.+) 上拥有运行与迁移所需的权限$`, "the account has the privileges needed to run and migrate on database $1"),
	p(`^账号拥有全局权限（(.*)），超出所需。建议只授予 (.+)\.\* 的权限。$`, "the account has global privileges ($1), more than needed. Grant only privileges on $2.* ."),
	p(`^账号还拥有其他数据库的权限，超出所需；OA Agent 只使用 (.+)。$`, "the account also has privileges on other databases, more than needed; OA Agent uses only $1."),
	p(`^授权仅覆盖数据库 (.+)$`, "the grants cover only database $1"),
	p(`^SQL Server (.+) 低于最低要求 2019（15\.x）$`, "SQL Server $1 is below the minimum 2019 (15.x)"),
	p(`^Azure SQL（(.+)）未经测试，按 SQL Server 兼容路径使用$`, "Azure SQL ($1) is untested and used through the SQL Server compatible path"),
	p(`^数据库兼容级别 (\d+) 低于 130（需要 ISJSON 等）$`, "database compatibility level $1 is below 130 (ISJSON etc. are required)"),
	p(`^兼容级别 (\d+)（SQL Server 2019 为 150）；排序规则 (.+)$`, "compatibility level $1 (SQL Server 2019 is 150); collation $2"),
	p(`^兼容级别 (\d+)；排序规则 (.+)（路径与用户名列使用二进制排序规则，文本使用 NVARCHAR）$`, "compatibility level $1; collation $2 (path and user-name columns use a binary collation, text uses NVARCHAR)"),
	p(`^时间一律以 UTC 存储（DATETIME2 \+ SYSUTCDATETIME）$`, "times are always stored in UTC (DATETIME2 + SYSUTCDATETIME)"),
	p(`^账号在 (.+) 上缺少：(.*)$`, "the account lacks on $1: $2"),
	p(`^账号对 (.+) 具有建表与读写权限$`, "the account can create tables and read/write on $1"),
	p(`^账号属于 sysadmin，权限远超所需。建议只授予专用库/schema 的 db_ddladmin \+ 读写权限。$`, "the account is a sysadmin, far more than needed. Grant only db_ddladmin + read/write on the dedicated database/schema."),
	p(`^账号属于 db_owner，超出所需（db_ddladmin \+ 读写权限即可）。$`, "the account is db_owner, more than needed (db_ddladmin + read/write is enough)."),
	p(`^账号不是 db_owner / sysadmin$`, "the account is neither db_owner nor sysadmin"),
}

// localize returns the English title and detail of a check when english is set; unknown texts are returned unchanged.
func localize(english bool, id, title, detail string) (string, string) {
	if !english {
		return title, detail
	}
	if t, ok := titleEN[id]; ok {
		title = t
	}
	for _, ph := range detailEN {
		if ph.re.MatchString(detail) {
			detail = ph.re.ReplaceAllString(detail, ph.en)
			break
		}
	}
	return title, strings.TrimSpace(strings.ReplaceAll(detail, " 等", " and more"))
}
