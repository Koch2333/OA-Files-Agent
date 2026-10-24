// Package dbadmin implements the administrator workflow around the database: connection tests, permission and
// isolation validation, saving a pending configuration, the approved schema initialisation and the start-up
// promotion of a pending configuration.
package dbadmin

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"oa-agent/backend/internal/db"
	"oa-agent/backend/internal/sqldb"
)

// Check statuses.
const (
	Pass = "pass"
	Warn = "warn"
	Fail = "fail"
	Info = "info"
)

// Check is one line of the validation checklist.
type Check struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// Isolation results.
const (
	IsoDedicatedDatabase = "dedicated_database"
	IsoDedicatedSchema   = "dedicated_schema"
	IsoNotIsolated       = "not_isolated"
	IsoUnknown           = "unknown"
)

// Report is the outcome of Inspect.
type Report struct {
	Provider      string    `json:"provider"`
	ServerVersion string    `json:"server_version"`
	LatencyMS     int64     `json:"latency_ms"`
	Checks        []Check   `json:"checks"`
	OK            bool      `json:"ok"` // no failed check
	Isolation     string    `json:"isolation"`
	Schema        db.Status `json:"schema"`
}

func (r *Report) add(id, title, status, detail string) {
	r.Checks = append(r.Checks, Check{id, title, status, detail})
	if status == Fail {
		r.OK = false
	}
}

func scanStr(ctx context.Context, d *sqldb.DB, q string, dest ...any) error {
	return d.QueryRow(ctx, q).Scan(dest...)
}

// Inspect runs the read-only checks against the target (no DDL, no writes).
func Inspect(ctx context.Context, d *sqldb.DB) Report {
	r := Report{Provider: string(d.Provider()), OK: true, Isolation: IsoUnknown, Checks: []Check{}}
	t0 := time.Now()
	if err := d.Ping(ctx); err != nil {
		r.add("connect", "连接数据库", Fail, sqldb.SafeError(err, d.Config()))
		return r
	}
	r.LatencyMS = time.Since(t0).Milliseconds()
	r.add("connect", "连接数据库", Pass, fmt.Sprintf("延迟 %d ms", r.LatencyMS))
	st, err := db.SchemaStatus(ctx, d)
	if err != nil {
		r.add("schema", "读取表结构状态", Fail, sqldb.SafeError(err, d.Config()))
		return r
	}
	r.Schema = st
	switch d.Provider() {
	case sqldb.Postgres:
		inspectPG(ctx, d, &r)
	case sqldb.MySQL:
		inspectMySQL(ctx, d, &r)
	case sqldb.MSSQL:
		inspectMSSQL(ctx, d, &r)
	}
	switch st.State {
	case "ok":
		r.add("migration", "表结构版本", Pass, fmt.Sprintf("已初始化，版本 %d", st.Version))
	case "empty":
		r.add("migration", "表结构版本", Info, "目标库为空，需要管理员批准后初始化")
	case "dirty":
		r.add("migration", "表结构版本", Fail, "上次迁移未完成（dirty），需要先恢复")
	default:
		r.add("migration", "表结构版本", Warn, fmt.Sprintf("版本 %d，需要升级到 %d（需要管理员批准）", st.Version, st.Latest))
	}
	return r
}

func isolationFromTables(r *Report, foreign []string, scope string, otherTables int) {
	if len(foreign) > 0 {
		sample := foreign
		if len(sample) > 5 {
			sample = sample[:5]
		}
		r.Isolation = IsoNotIsolated
		r.add("isolation", "隔离性：专用库/schema", Fail, fmt.Sprintf("%s 中存在非 OA Agent 的表（%s%s）。请使用专用数据库或专用 schema（空的或仅包含 OA Agent 表）；用表名前缀不算隔离。", scope, strings.Join(sample, ", "), map[bool]string{true: " 等", false: ""}[len(foreign) > 5]))
		return
	}
	if otherTables > 0 {
		r.Isolation = IsoDedicatedSchema
		r.add("isolation", "隔离性：专用库/schema", Pass, scope+" 仅包含 OA Agent 的表（同库其他 schema 有其他应用的表，不会被触碰）")
		return
	}
	r.Isolation = IsoDedicatedDatabase
	r.add("isolation", "隔离性：专用库/schema", Pass, scope+" 仅包含 OA Agent 的表（专用数据库）")
}

// ---- PostgreSQL ----

func inspectPG(ctx context.Context, d *sqldb.DB, r *Report) {
	var vnum int
	var vtxt string
	if err := scanStr(ctx, d, `SHOW server_version_num`, &vnum); err == nil {
		_ = scanStr(ctx, d, `SHOW server_version`, &vtxt)
		r.ServerVersion = vtxt
		if vnum < 140000 {
			r.add("version", "服务器版本", Fail, fmt.Sprintf("PostgreSQL %s 低于最低要求 14", vtxt))
		} else {
			r.add("version", "服务器版本", Pass, "PostgreSQL "+vtxt)
		}
	}
	var enc string
	if scanStr(ctx, d, `SHOW server_encoding`, &enc) == nil {
		if strings.EqualFold(enc, "UTF8") {
			r.add("charset", "字符集", Pass, "UTF8；比较区分大小写（搜索使用不区分大小写的 ILIKE）")
		} else {
			r.add("charset", "字符集", Fail, "数据库编码为 "+enc+"，需要 UTF8")
		}
	}
	var tz string
	if scanStr(ctx, d, `SHOW TimeZone`, &tz) == nil {
		r.add("timezone", "时区", Pass, "连接固定使用 UTC（当前会话 "+tz+"），时间一律以 UTC 存储")
	}
	var schema *string
	_ = scanStr(ctx, d, `SELECT current_schema()`, &schema)
	if schema == nil {
		r.add("schema", "目标 schema", Fail, "搜索路径中没有可用的 schema")
		return
	}
	var canCreate, canUse bool
	_ = scanStr(ctx, d, `SELECT has_schema_privilege(current_schema(), 'CREATE'), has_schema_privilege(current_schema(), 'USAGE')`, &canCreate, &canUse)
	st := r.Schema
	switch {
	case !canUse:
		r.add("perm_schema", "schema 权限", Fail, "账号对 schema "+*schema+" 没有 USAGE 权限")
	case !canCreate && st.State != "ok":
		r.add("perm_schema", "schema 权限", Fail, "账号对 schema "+*schema+" 没有 CREATE 权限，无法建表")
	case !canCreate:
		r.add("perm_schema", "schema 权限", Warn, "账号没有 CREATE 权限：可运行，但无法执行后续升级迁移")
	default:
		r.add("perm_schema", "schema 权限", Pass, "对 schema "+*schema+" 具有 USAGE/CREATE")
	}
	var su, cr, cd bool
	if scanStr(ctx, d, `SELECT rolsuper, rolcreaterole, rolcreatedb FROM pg_roles WHERE rolname = current_user`, &su, &cr, &cd) == nil {
		var over []string
		if su {
			over = append(over, "SUPERUSER")
		}
		if cr {
			over = append(over, "CREATEROLE")
		}
		if cd {
			over = append(over, "CREATEDB")
		}
		if len(over) > 0 {
			r.add("least_privilege", "最小权限", Warn, "账号拥有 "+strings.Join(over, "/")+"，权限超出所需。建议使用只拥有专用 schema 的普通账号。")
		} else {
			r.add("least_privilege", "最小权限", Pass, "账号不是超级用户，也没有 CREATEROLE / CREATEDB")
		}
	}
	var other int
	_ = scanStr(ctx, d, `SELECT count(*) FROM pg_tables WHERE schemaname NOT IN ('pg_catalog','information_schema') AND schemaname <> current_schema()`, &other)
	isolationFromTables(r, r.Schema.ForeignTables, "schema "+*schema, other)
}

// ---- MySQL ----

var mysqlVersionRe = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)`)

var mysqlRequired = []string{"SELECT", "INSERT", "UPDATE", "DELETE", "CREATE", "ALTER", "INDEX", "REFERENCES"}

func inspectMySQL(ctx context.Context, d *sqldb.DB, r *Report) {
	var ver string
	if scanStr(ctx, d, `SELECT VERSION()`, &ver) == nil {
		r.ServerVersion = ver
		m := mysqlVersionRe.FindStringSubmatch(ver)
		switch {
		case strings.Contains(strings.ToLower(ver), "mariadb"):
			r.add("version", "服务器版本", Fail, "检测到 MariaDB（"+ver+"），未支持/未测试；请使用 MySQL 8.0.16+")
		case m == nil:
			r.add("version", "服务器版本", Warn, "无法解析版本号 "+ver)
		default:
			maj, _ := strconv.Atoi(m[1])
			min, _ := strconv.Atoi(m[2])
			pat, _ := strconv.Atoi(m[3])
			if maj < 8 || (maj == 8 && min == 0 && pat < 16) {
				r.add("version", "服务器版本", Fail, "MySQL "+ver+" 低于最低要求 8.0.16（需要 CHECK 约束、函数索引、SKIP LOCKED）")
			} else {
				r.add("version", "服务器版本", Pass, "MySQL "+ver)
			}
		}
	}
	var cs, coll string
	if scanStr(ctx, d, `SELECT @@character_set_database, @@collation_database`, &cs, &coll) == nil {
		if strings.HasPrefix(cs, "utf8mb4") {
			r.add("charset", "字符集与排序规则", Pass, cs+" / "+coll+"（表在创建时显式使用 utf8mb4，支持 emoji；默认排序规则不区分大小写，路径与用户名列使用二进制排序规则）")
		} else {
			r.add("charset", "字符集与排序规则", Warn, "数据库默认字符集为 "+cs+"；OA Agent 的表显式使用 utf8mb4，但建议把库也设为 utf8mb4")
		}
	}
	var tz, iso string
	if scanStr(ctx, d, `SELECT @@session.time_zone, @@transaction_isolation`, &tz, &iso) == nil {
		r.add("timezone", "时区与事务隔离", Pass, "会话固定为 UTC（"+tz+"），事务隔离 "+iso)
	}
	var name *string
	_ = scanStr(ctx, d, `SELECT DATABASE()`, &name)
	if name == nil || *name == "" {
		r.add("schema", "目标数据库", Fail, "没有选择数据库")
		return
	}
	switch strings.ToLower(*name) {
	case "mysql", "sys", "information_schema", "performance_schema":
		r.add("schema", "目标数据库", Fail, "不能使用系统库 "+*name+"；请创建专用数据库")
		return
	}
	checkMySQLGrants(ctx, d, r, *name)
	isolationFromTables(r, r.Schema.ForeignTables, "数据库 "+*name, 0)
}

var grantRe = regexp.MustCompile("(?s)^GRANT (.+?) ON (.+?) TO ")

func unescapeIdent(s string) string {
	s = strings.Trim(s, "`")
	return strings.NewReplacer("``", "`", `\_`, "_", `\%`, "%", `\\`, `\`).Replace(s)
}

func grantMatchesDB(obj, db string) (scope string) { // "global", "db", "" (other)
	if obj == "*.*" {
		return "global"
	}
	i := strings.LastIndex(obj, ".")
	if i < 0 {
		return ""
	}
	dbPart, tbl := obj[:i], obj[i+1:]
	if tbl != "*" {
		return ""
	}
	pat := strings.Trim(dbPart, "`")
	if strings.EqualFold(unescapeIdent(dbPart), db) && !strings.Contains(strings.ReplaceAll(pat, `\_`, ""), "_") {
		return "db"
	}
	// wildcard patterns (db_%): translate to a regexp
	re := "^" + strings.NewReplacer(`\_`, "\x00", `\%`, "\x01").Replace(regexp.QuoteMeta(strings.ReplaceAll(pat, "``", "`"))) + "$"
	re = strings.NewReplacer(`_`, ".", `%`, ".*", "\x00", "_", "\x01", "%").Replace(re)
	if ok, _ := regexp.MatchString("(?i)"+re, db); ok {
		return "db"
	}
	return ""
}

func checkMySQLGrants(ctx context.Context, d *sqldb.DB, r *Report, dbName string) {
	rows, err := d.Query(ctx, `SHOW GRANTS FOR CURRENT_USER()`)
	if err != nil {
		r.add("perm_schema", "权限（GRANTS）", Warn, "无法读取 SHOW GRANTS："+sqldb.SafeError(err, d.Config()))
		return
	}
	defer rows.Close()
	have := map[string]bool{}
	var over []string
	other := false
	for rows.Next() {
		var line string
		if rows.Scan(&line) != nil {
			continue
		}
		m := grantRe.FindStringSubmatch(line)
		if m == nil {
			continue // role grants and proxy grants
		}
		privs := strings.Split(m[1], ",")
		scope := grantMatchesDB(strings.TrimSpace(m[2]), dbName)
		withGrant := strings.Contains(strings.ToUpper(line), "WITH GRANT OPTION")
		for _, p := range privs {
			p = strings.ToUpper(strings.TrimSpace(p))
			if p == "USAGE" {
				continue
			}
			if i := strings.Index(p, "("); i > 0 {
				p = strings.TrimSpace(p[:i])
			}
			switch scope {
			case "global", "db":
				if p == "ALL PRIVILEGES" || p == "ALL" {
					for _, q := range mysqlRequired {
						have[q] = true
					}
				}
				have[p] = true
				if scope == "global" {
					over = append(over, p+" ON *.*")
				}
			default:
				other = true
			}
		}
		if withGrant {
			over = append(over, "WITH GRANT OPTION")
		}
	}
	var missing []string
	for _, p := range mysqlRequired {
		if !have[p] {
			missing = append(missing, p)
		}
	}
	switch {
	case len(missing) > 0 && r.Schema.State != "ok":
		r.add("perm_schema", "权限（GRANTS）", Fail, "账号在数据库 "+dbName+" 上缺少权限："+strings.Join(missing, ", "))
	case len(missing) > 0:
		r.add("perm_schema", "权限（GRANTS）", Warn, "账号缺少 "+strings.Join(missing, ", ")+"：可运行，但无法执行升级迁移")
	default:
		r.add("perm_schema", "权限（GRANTS）", Pass, "账号在数据库 "+dbName+" 上拥有运行与迁移所需的权限")
	}
	switch {
	case len(over) > 0:
		r.add("least_privilege", "最小权限", Warn, "账号拥有全局权限（"+strings.Join(uniq(over), "; ")+"），超出所需。建议只授予 "+dbName+".* 的权限。")
	case other:
		r.add("least_privilege", "最小权限", Warn, "账号还拥有其他数据库的权限，超出所需；OA Agent 只使用 "+dbName+"。")
	default:
		r.add("least_privilege", "最小权限", Pass, "授权仅覆盖数据库 "+dbName)
	}
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// ---- SQL Server ----

func inspectMSSQL(ctx context.Context, d *sqldb.DB, r *Report) {
	var ver, edition string
	var major, engine int
	if scanStr(ctx, d, `SELECT CAST(SERVERPROPERTY('ProductVersion') AS NVARCHAR(50)), CAST(SERVERPROPERTY('ProductMajorVersion') AS INT), CAST(SERVERPROPERTY('EngineEdition') AS INT), CAST(SERVERPROPERTY('Edition') AS NVARCHAR(100))`, &ver, &major, &engine, &edition) == nil {
		r.ServerVersion = ver + " (" + edition + ")"
		switch {
		case major < 15:
			r.add("version", "服务器版本", Fail, "SQL Server "+ver+" 低于最低要求 2019（15.x）")
		case engine == 5 || engine == 8:
			r.add("version", "服务器版本", Warn, "Azure SQL（"+edition+"）未经测试，按 SQL Server 兼容路径使用")
		default:
			r.add("version", "服务器版本", Pass, "SQL Server "+ver+" "+edition)
		}
	}
	var compat int
	var coll *string
	var rcsi bool
	if scanStr(ctx, d, `SELECT compatibility_level, collation_name, is_read_committed_snapshot_on FROM sys.databases WHERE name = DB_NAME()`, &compat, &coll, &rcsi) == nil {
		c := ""
		if coll != nil {
			c = *coll
		}
		switch {
		case compat < 130:
			r.add("compat", "兼容级别与排序规则", Fail, fmt.Sprintf("数据库兼容级别 %d 低于 130（需要 ISJSON 等）", compat))
		case compat < 150:
			r.add("compat", "兼容级别与排序规则", Warn, fmt.Sprintf("兼容级别 %d（SQL Server 2019 为 150）；排序规则 %s", compat, c))
		default:
			r.add("compat", "兼容级别与排序规则", Pass, fmt.Sprintf("兼容级别 %d；排序规则 %s（路径与用户名列使用二进制排序规则，文本使用 NVARCHAR）", compat, c))
		}
	}
	r.add("timezone", "时区", Pass, "时间一律以 UTC 存储（DATETIME2 + SYSUTCDATETIME）")
	var dbName, schema, user string
	_ = scanStr(ctx, d, `SELECT DB_NAME(), SCHEMA_NAME(), USER_NAME()`, &dbName, &schema, &user)
	var perms [6]int
	if scanStr(ctx, d, `SELECT ISNULL(HAS_PERMS_BY_NAME(DB_NAME(), 'DATABASE', 'CREATE TABLE'), 0), ISNULL(HAS_PERMS_BY_NAME(SCHEMA_NAME(), 'SCHEMA', 'ALTER'), 0),
		ISNULL(HAS_PERMS_BY_NAME(SCHEMA_NAME(), 'SCHEMA', 'SELECT'), 0), ISNULL(HAS_PERMS_BY_NAME(SCHEMA_NAME(), 'SCHEMA', 'INSERT'), 0),
		ISNULL(HAS_PERMS_BY_NAME(SCHEMA_NAME(), 'SCHEMA', 'UPDATE'), 0), ISNULL(HAS_PERMS_BY_NAME(SCHEMA_NAME(), 'SCHEMA', 'DELETE'), 0)`,
		&perms[0], &perms[1], &perms[2], &perms[3], &perms[4], &perms[5]) == nil {
		var missing []string
		for i, n := range []string{"CREATE TABLE（数据库）", "ALTER（schema）", "SELECT", "INSERT", "UPDATE", "DELETE"} {
			if perms[i] != 1 {
				missing = append(missing, n)
			}
		}
		switch {
		case len(missing) > 0 && r.Schema.State != "ok":
			r.add("perm_schema", "权限", Fail, "账号在 "+dbName+"."+schema+" 上缺少："+strings.Join(missing, ", "))
		case len(missing) > 0:
			r.add("perm_schema", "权限", Warn, "账号缺少 "+strings.Join(missing, ", ")+"：可运行，但无法执行升级迁移")
		default:
			r.add("perm_schema", "权限", Pass, "账号对 "+dbName+"."+schema+" 具有建表与读写权限")
		}
	}
	var owner, sysadmin *int
	_ = scanStr(ctx, d, `SELECT IS_ROLEMEMBER('db_owner'), IS_SRVROLEMEMBER('sysadmin')`, &owner, &sysadmin)
	switch {
	case sysadmin != nil && *sysadmin == 1:
		r.add("least_privilege", "最小权限", Warn, "账号属于 sysadmin，权限远超所需。建议只授予专用库/schema 的 db_ddladmin + 读写权限。")
	case owner != nil && *owner == 1:
		r.add("least_privilege", "最小权限", Warn, "账号属于 db_owner，超出所需（db_ddladmin + 读写权限即可）。")
	default:
		r.add("least_privilege", "最小权限", Pass, "账号不是 db_owner / sysadmin")
	}
	var other int
	_ = scanStr(ctx, d, `SELECT count(*) FROM sys.tables t JOIN sys.schemas s ON s.schema_id = t.schema_id WHERE s.name <> SCHEMA_NAME()`, &other)
	isolationFromTables(r, r.Schema.ForeignTables, "schema "+dbName+"."+schema, other)
}
