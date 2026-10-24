package sqldb

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net"
	"strings"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	mssql "github.com/microsoft/go-mssqldb"
)

// Dialect is the small amount of engine knowledge the application needs beyond SQL translation.
type Dialect interface {
	Provider() Provider
	// StringsArg encodes a []string for a column that holds a list (PG text[], otherwise a JSON array).
	StringsArg(v []string) any
	// ScanStrings returns a Scan destination that fills *dst from such a column.
	ScanStrings(dst *[]string) any
	IsUniqueViolation(err error) bool
	IsDeadlock(err error) bool
	IsSerialization(err error) bool
	IsConnLost(err error) bool
	// IsRetryable is true for transient errors where re-running a whole transaction is safe.
	IsRetryable(err error) bool
	Quote(ident string) string
}

func DialectOf(p Provider) Dialect {
	switch p {
	case MySQL:
		return mysqlDialect{}
	case MSSQL:
		return mssqlDialect{}
	}
	return pgDialect{}
}

type pgDialect struct{}
type mysqlDialect struct{}
type mssqlDialect struct{}

func (pgDialect) Provider() Provider    { return Postgres }
func (mysqlDialect) Provider() Provider { return MySQL }
func (mssqlDialect) Provider() Provider { return MSSQL }

func (pgDialect) Quote(s string) string    { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
func (mysqlDialect) Quote(s string) string { return "`" + strings.ReplaceAll(s, "`", "``") + "`" }
func (mssqlDialect) Quote(s string) string { return "[" + strings.ReplaceAll(s, "]", "]]") + "]" }

func (pgDialect) StringsArg(v []string) any {
	if v == nil {
		return []string{}
	}
	return v
}
func (pgDialect) ScanStrings(dst *[]string) any { return dst }

func jsonStrings(v []string) any {
	if v == nil {
		v = []string{}
	}
	b, _ := json.Marshal(v)
	return string(b)
}
func (mysqlDialect) StringsArg(v []string) any     { return jsonStrings(v) }
func (mssqlDialect) StringsArg(v []string) any     { return jsonStrings(v) }
func (mysqlDialect) ScanStrings(dst *[]string) any { return &stringsScanner{dst} }
func (mssqlDialect) ScanStrings(dst *[]string) any { return &stringsScanner{dst} }

type stringsScanner struct{ dst *[]string }

func (s *stringsScanner) Scan(v any) error {
	*s.dst = []string{}
	var b []byte
	switch x := v.(type) {
	case nil:
		return nil
	case []byte:
		b = x
	case string:
		b = []byte(x)
	default:
		return fmt.Errorf("sqldb: cannot scan %T into a string list", v)
	}
	if len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, s.dst)
}

var _ sql.Scanner = (*stringsScanner)(nil)

func pgCode(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

func mysqlNum(err error) uint16 {
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		return me.Number
	}
	return 0
}

func mssqlNum(err error) int32 {
	var ve mssql.Error
	if errors.As(err, &ve) {
		return ve.Number
	}
	var pe *mssql.Error
	if errors.As(err, &pe) && pe != nil {
		return pe.Number
	}
	return 0
}

func netLost(err error) bool {
	var ne net.Error
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, driver.ErrBadConn) || errors.As(err, &ne) ||
		errors.Is(err, net.ErrClosed)
}

func (pgDialect) IsUniqueViolation(err error) bool { return pgCode(err) == "23505" }
func (pgDialect) IsDeadlock(err error) bool        { return pgCode(err) == "40P01" }
func (pgDialect) IsSerialization(err error) bool   { return pgCode(err) == "40001" }
func (pgDialect) IsConnLost(err error) bool {
	c := pgCode(err)
	return strings.HasPrefix(c, "08") || c == "57P01" || c == "57P02" || c == "57P03" || (c == "" && netLost(err))
}
func (d pgDialect) IsRetryable(err error) bool { return d.IsDeadlock(err) || d.IsSerialization(err) }

func (mysqlDialect) IsUniqueViolation(err error) bool { return mysqlNum(err) == 1062 }
func (mysqlDialect) IsDeadlock(err error) bool        { return mysqlNum(err) == 1213 }
func (mysqlDialect) IsSerialization(err error) bool   { return mysqlNum(err) == 1205 }
func (mysqlDialect) IsConnLost(err error) bool {
	n := mysqlNum(err)
	return n == 2006 || n == 2013 || n == 1927 || errors.Is(err, mysql.ErrInvalidConn) || (n == 0 && netLost(err))
}
func (d mysqlDialect) IsRetryable(err error) bool { return d.IsDeadlock(err) || d.IsSerialization(err) }

func (mssqlDialect) IsUniqueViolation(err error) bool {
	n := mssqlNum(err)
	return n == 2627 || n == 2601
}
func (mssqlDialect) IsDeadlock(err error) bool { return mssqlNum(err) == 1205 }
func (mssqlDialect) IsSerialization(err error) bool {
	n := mssqlNum(err)
	return n == 3960 || n == 3961
}
func (mssqlDialect) IsConnLost(err error) bool {
	n := mssqlNum(err)
	return n == 0 && netLost(err) || n == 10054 || n == 10053
}
func (d mssqlDialect) IsRetryable(err error) bool { return d.IsDeadlock(err) || d.IsSerialization(err) }

// lockName derives a stable server-side lock name from an application key.
func lockName(key string) string {
	h := sha1.Sum([]byte(key))
	return "oa:" + hex.EncodeToString(h[:])[:32]
}

func lockHash(key string) int64 {
	h := fnv.New64a()
	h.Write([]byte(key))
	return int64(h.Sum64() >> 1)
}

func errLock(what string, err error) error { return fmt.Errorf("%s: %w", what, err) }

var _ = context.Background

var errBadConn = driver.ErrBadConn
