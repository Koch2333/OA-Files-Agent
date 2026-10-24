package sqldb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgxpool"
	mssql "github.com/microsoft/go-mssqldb"
	"github.com/microsoft/go-mssqldb/msdsn"
)

// Open connects, pings and returns the pool. Errors never contain the password or a credential-bearing URL.
func Open(ctx context.Context, c Config) (*DB, error) {
	c = c.Normalize()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	var db *DB
	var err error
	switch c.Provider {
	case Postgres:
		db, err = openPG(ctx, c)
	case MySQL:
		db, err = openMySQL(c)
	case MSSQL:
		db, err = openMSSQL(c)
	default:
		return nil, fmt.Errorf("unsupported database provider %q", c.Provider)
	}
	if err != nil {
		return nil, errors.New(SafeError(err, c))
	}
	pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err = db.Ping(pctx); err != nil {
		db.Close()
		return nil, errors.New(SafeError(err, c))
	}
	return db, nil
}

func pgURL(c Config) string {
	if c.URL != "" {
		return c.URL
	}
	u := url.URL{Scheme: "postgres", Host: hostPort(c), Path: "/" + c.Name}
	u.User = url.UserPassword(c.User, c.Password)
	q := url.Values{}
	if c.TLSMode == "" || c.TLSMode == TLSDisable {
		q.Set("sslmode", "disable")
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func hostPort(c Config) string {
	h := c.Host
	if strings.Contains(h, ":") && !strings.HasPrefix(h, "[") {
		h = "[" + h + "]"
	}
	return h + ":" + strconv.Itoa(c.Port)
}

func openPG(ctx context.Context, c Config) (*DB, error) {
	pc, err := pgxpool.ParseConfig(pgURL(c))
	if err != nil {
		return nil, errors.New("invalid database URL")
	}
	pc.MaxConns = int32(c.MaxConns)
	if c.Schema != "" {
		pc.ConnConfig.RuntimeParams["search_path"] = c.Schema
	}
	pc.ConnConfig.RuntimeParams["timezone"] = "UTC"
	if c.TLSMode != "" && (c.URL == "" || c.TLSMode != TLSDisable) && c.TLSMode != TLSDisable {
		tc, err := c.TLSConfig()
		if err != nil {
			return nil, err
		}
		if c.URL != "" && c.CAPEM == "" && c.ClientCertPEM == "" && c.ServerName == "" {
			tc = nil // keep what the URL's sslmode produced
		}
		if tc != nil {
			if tc.ServerName == "" {
				tc.ServerName = pc.ConnConfig.Host
			}
			pc.ConnConfig.TLSConfig = tc
			pc.ConnConfig.Fallbacks = nil
		}
	}
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, err
	}
	return &DB{b: &pgBackend{pool}, dialect: pgDialect{}, cfg: c}, nil
}

func mysqlConfig(c Config) (*mysql.Config, error) {
	var mc *mysql.Config
	if c.URL != "" && strings.HasPrefix(strings.ToLower(c.URL), "mysql://") {
		u, err := url.Parse(c.URL)
		if err != nil {
			return nil, errors.New("invalid database URL")
		}
		mc = mysql.NewConfig()
		mc.User = u.User.Username()
		mc.Passwd, _ = u.User.Password()
		mc.Net = "tcp"
		host := u.Hostname()
		port := u.Port()
		if port == "" {
			port = "3306"
		}
		mc.Addr = net_JoinHostPort(host, port)
		mc.DBName = strings.TrimPrefix(u.Path, "/")
		switch u.Query().Get("tls") {
		case "true", "required":
			if c.TLSMode == "" {
				c.TLSMode = TLSVerifyFull
			}
		case "skip-verify", "preferred":
			if c.TLSMode == "" {
				c.TLSMode = TLSRequire
			}
		}
	} else if c.URL != "" {
		var err error
		if mc, err = mysql.ParseDSN(c.URL); err != nil {
			return nil, errors.New("invalid database DSN")
		}
	} else {
		mc = mysql.NewConfig()
		mc.User, mc.Passwd, mc.Net, mc.Addr, mc.DBName = c.User, c.Password, "tcp", hostPort(c), c.Name
	}
	mc.ParseTime = true
	mc.Loc = time.UTC
	mc.Collation = "utf8mb4_0900_ai_ci"
	mc.ClientFoundRows = true // RowsAffected = matched rows, like PostgreSQL and SQL Server
	mc.InterpolateParams = false
	mc.Timeout = 10 * time.Second
	mc.AllowNativePasswords = true
	if mc.Params == nil {
		mc.Params = map[string]string{}
	}
	mc.Params["transaction_isolation"] = "'READ-COMMITTED'"
	mc.Params["time_zone"] = "'+00:00'"
	mc.Params["sql_mode"] = "'STRICT_TRANS_TABLES,NO_ENGINE_SUBSTITUTION,ONLY_FULL_GROUP_BY,NO_ZERO_DATE,NO_ZERO_IN_DATE,ERROR_FOR_DIVISION_BY_ZERO'"
	mc.TLSConfig = ""
	mc.TLS = nil
	if c.TLSMode != "" && c.TLSMode != TLSDisable {
		tc, err := c.TLSConfig()
		if err != nil {
			return nil, err
		}
		if tc.ServerName == "" {
			tc.ServerName, _, _ = splitHost(mc.Addr)
		}
		mc.TLS = tc
	}
	return mc, nil
}

func openMySQL(c Config) (*DB, error) {
	mc, err := mysqlConfig(c)
	if err != nil {
		return nil, err
	}
	conn, err := mysql.NewConnector(mc)
	if err != nil {
		return nil, err
	}
	sdb := sql.OpenDB(conn)
	tuneSQL(sdb, c)
	d := mysqlDialect{}
	return &DB{b: &sqlBackend{db: sdb, p: MySQL, d: d}, dialect: d, cfg: c}, nil
}

func tuneSQL(sdb *sql.DB, c Config) {
	sdb.SetMaxOpenConns(c.MaxConns)
	sdb.SetMaxIdleConns(c.MaxConns)
	sdb.SetConnMaxLifetime(30 * time.Minute)
	sdb.SetConnMaxIdleTime(5 * time.Minute)
}

func mssqlDSN(c Config) string {
	if c.URL != "" {
		return c.URL
	}
	q := url.Values{}
	q.Set("database", c.Name)
	q.Set("app name", "oa-agent")
	switch c.TLSMode {
	case "", TLSDisable:
		q.Set("encrypt", "disable")
	case TLSRequire:
		q.Set("encrypt", "true")
		q.Set("TrustServerCertificate", "true")
	default:
		q.Set("encrypt", "true")
	}
	u := url.URL{Scheme: "sqlserver", Host: hostPort(c), RawQuery: q.Encode()}
	u.User = url.UserPassword(c.User, c.Password)
	return u.String()
}

func openMSSQL(c Config) (*DB, error) {
	mc, err := msdsn.Parse(mssqlDSN(c))
	if err != nil {
		return nil, errors.New("invalid database DSN")
	}
	if c.TLSMode != "" && c.TLSMode != TLSDisable {
		tc, err := c.TLSConfig()
		if err != nil {
			return nil, err
		}
		if tc.ServerName == "" {
			tc.ServerName = mc.Host
		}
		mc.TLSConfig = tc
		if mc.Encryption == msdsn.EncryptionOff || mc.Encryption == msdsn.EncryptionDisabled {
			mc.Encryption = msdsn.EncryptionRequired
		}
	}
	mc.DialTimeout = 10 * time.Second
	conn := mssql.NewConnectorConfig(mc)
	sdb := sql.OpenDB(conn)
	tuneSQL(sdb, c)
	d := mssqlDialect{}
	return &DB{b: &sqlBackend{db: sdb, p: MSSQL, d: d}, dialect: d, cfg: c}, nil
}

// OpenURL opens a database from a bare URL (postgres://, mysql://, sqlserver://).
func OpenURL(ctx context.Context, raw string) (*DB, error) {
	c, err := FromURL(raw)
	if err != nil {
		return nil, err
	}
	return Open(ctx, c)
}

var (
	reURLPass  = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://[^:/@\s]*):[^@\s]*@`)
	reKVPass   = regexp.MustCompile(`(?i)\b(password|pwd|passwd)\s*=\s*('[^']*'|"[^"]*"|[^;&\s]*)`)
	reMyDSN    = regexp.MustCompile(`([A-Za-z0-9_.-]+):[^@\s/]*@(tcp|unix)\(`)
	reQueryKey = regexp.MustCompile(`(?i)([?&])(password|pwd|sslkey|sslpassword)=[^&\s]*`)
)

// RedactDSN removes passwords from URLs, MySQL DSNs and key=value connection strings.
func RedactDSN(s string) string {
	s = reURLPass.ReplaceAllString(s, "${1}:***@")
	s = reMyDSN.ReplaceAllString(s, "${1}:***@${2}(")
	s = reQueryKey.ReplaceAllString(s, "${1}${2}=***")
	s = reKVPass.ReplaceAllString(s, "${1}=***")
	return s
}

// SafeError renders an error for users and logs: DSNs are redacted and the configured password is removed even if a
// driver echoed it.
func SafeError(err error, c Config) string {
	if err == nil {
		return ""
	}
	m := RedactDSN(err.Error())
	if len(c.Password) >= 3 {
		m = strings.ReplaceAll(m, c.Password, "***")
	}
	if c.URL != "" {
		m = strings.ReplaceAll(m, c.URL, RedactDSN(c.URL))
	}
	if len(m) > 400 {
		m = m[:400] + "…"
	}
	return m
}

func net_JoinHostPort(h, p string) string {
	if strings.Contains(h, ":") && !strings.HasPrefix(h, "[") {
		h = "[" + h + "]"
	}
	return h + ":" + p
}

func splitHost(addr string) (string, string, error) {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return addr, "", nil
	}
	return strings.Trim(addr[:i], "[]"), addr[i+1:], nil
}
