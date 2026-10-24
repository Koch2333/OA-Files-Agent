package sqldb

import (
	"reflect"
	"strings"
	"testing"
)

func norm(s string) string { return strings.Join(strings.Fields(s), " ") }

func TestTranslateTable(t *testing.T) {
	cases := []struct {
		name, in   string
		pg, my, ms string
		myArgs     []int
	}{
		{
			name: "casts and placeholders",
			in:   `SELECT id::text FROM t WHERE id=$1::uuid AND d>=$2::date`,
			pg:   `SELECT id::text FROM t WHERE id=$1::uuid AND d>=$2::date`,
			my:   `SELECT id FROM t WHERE id=? AND d>=?`, myArgs: []int{0, 1},
			ms: `SELECT id FROM t WHERE id=@p1 AND d>=@p2`,
		},
		{
			name: "reused parameter",
			in:   `UPDATE t SET a=$2, b=$2 WHERE id=$1`,
			pg:   `UPDATE t SET a=$2, b=$2 WHERE id=$1`,
			my:   `UPDATE t SET a=?, b=? WHERE id=?`, myArgs: []int{1, 1, 0},
			ms: `UPDATE t SET a=@p2, b=@p2 WHERE id=@p1`,
		},
		{
			name: "now and booleans",
			in:   `UPDATE t SET updated_at=now() WHERE enabled=true AND x<>false`,
			pg:   `UPDATE t SET updated_at=now() WHERE enabled=true AND x<>false`,
			my:   `UPDATE t SET updated_at=UTC_TIMESTAMP(6) WHERE enabled=true AND x<>false`,
			ms:   `UPDATE t SET updated_at=SYSUTCDATETIME() WHERE enabled=1 AND x<>0`,
		},
		{
			name: "strings untouched",
			in:   `SELECT 'now() $1 true ::x' FROM t WHERE a=$1`,
			pg:   `SELECT 'now() $1 true ::x' FROM t WHERE a=$1`,
			my:   `SELECT 'now() $1 true ::x' FROM t WHERE a=?`, myArgs: []int{0},
			ms: `SELECT 'now() $1 true ::x' FROM t WHERE a=@p1`,
		},
		{
			name: "ilike",
			in:   `SELECT 1 FROM t WHERE (a.name ILIKE $1 OR path ILIKE $1)`,
			pg:   `SELECT 1 FROM t WHERE (a.name ILIKE $1 ESCAPE '!' OR path ILIKE $1 ESCAPE '!')`,
			my:   `SELECT 1 FROM t WHERE (LOWER(a.name) LIKE LOWER(?) ESCAPE '!' OR LOWER(path) LIKE LOWER(?) ESCAPE '!')`, myArgs: []int{0, 0},
			ms: `SELECT 1 FROM t WHERE (LOWER(a.name) LIKE LOWER(@p1) ESCAPE '!' OR LOWER(path) LIKE LOWER(@p1) ESCAPE '!')`,
		},
		{
			name: "limit offset",
			in:   `SELECT a FROM t WHERE x=$1 ORDER BY a LIMIT $2 OFFSET $3`,
			pg:   `SELECT a FROM t WHERE x=$1 ORDER BY a LIMIT $2 OFFSET $3`,
			my:   `SELECT a FROM t WHERE x=? ORDER BY a LIMIT ? OFFSET ?`, myArgs: []int{0, 1, 2},
			ms: `SELECT a FROM t WHERE x=@p1 ORDER BY a OFFSET @p3 ROWS FETCH NEXT @p2 ROWS ONLY`,
		},
		{
			name: "limit without order by",
			in:   `SELECT a FROM t LIMIT $1 OFFSET $2`,
			pg:   `SELECT a FROM t LIMIT $1 OFFSET $2`,
			my:   `SELECT a FROM t LIMIT ? OFFSET ?`, myArgs: []int{0, 1},
			ms: `SELECT a FROM t ORDER BY (SELECT NULL) OFFSET @p2 ROWS FETCH NEXT @p1 ROWS ONLY`,
		},
		{
			name: "limit 1 becomes top",
			in:   `SELECT id FROM t WHERE a=$1 ORDER BY created_at DESC LIMIT 1`,
			pg:   `SELECT id FROM t WHERE a=$1 ORDER BY created_at DESC LIMIT 1`,
			my:   `SELECT id FROM t WHERE a=? ORDER BY created_at DESC LIMIT 1`, myArgs: []int{0},
			ms: `SELECT TOP (1) id FROM t WHERE a=@p1 ORDER BY created_at DESC`,
		},
		{
			name: "nested limit",
			in:   `SELECT x FROM u WHERE id=(SELECT id FROM t ORDER BY a LIMIT 1) LIMIT 5`,
			pg:   `SELECT x FROM u WHERE id=(SELECT id FROM t ORDER BY a LIMIT 1) LIMIT 5`,
			my:   `SELECT x FROM u WHERE id=(SELECT id FROM t ORDER BY a LIMIT 1) LIMIT 5`,
			ms:   `SELECT TOP (5) x FROM u WHERE id=(SELECT TOP (1) id FROM t ORDER BY a)`,
		},
		{
			name: "for update skip locked",
			in:   `SELECT id FROM import_jobs WHERE status='queued' ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED`,
			pg:   `SELECT id FROM import_jobs WHERE status='queued' ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED`,
			my:   `SELECT id FROM import_jobs WHERE status='queued' ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED`,
			ms:   `SELECT TOP (1) id FROM import_jobs WITH (UPDLOCK, ROWLOCK, READPAST) WHERE status='queued' ORDER BY created_at`,
		},
		{
			name: "for update of alias",
			in:   `SELECT r.id FROM replications r JOIN storage_connections c ON c.id=r.connection_id WHERE c.enabled=true LIMIT 1 FOR UPDATE OF r SKIP LOCKED`,
			pg:   `SELECT r.id FROM replications r JOIN storage_connections c ON c.id=r.connection_id WHERE c.enabled=true LIMIT 1 FOR UPDATE OF r SKIP LOCKED`,
			my:   `SELECT r.id FROM replications r JOIN storage_connections c ON c.id=r.connection_id WHERE c.enabled=true LIMIT 1 FOR UPDATE OF r SKIP LOCKED`,
			ms:   `SELECT TOP (1) r.id FROM replications r WITH (UPDLOCK, ROWLOCK, READPAST) JOIN storage_connections c ON c.id=r.connection_id WHERE c.enabled=1`,
		},
		{
			name: "plain for update",
			in:   `SELECT status FROM archive_plans WHERE id=$1 FOR UPDATE`,
			pg:   `SELECT status FROM archive_plans WHERE id=$1 FOR UPDATE`,
			my:   `SELECT status FROM archive_plans WHERE id=? FOR UPDATE`, myArgs: []int{0},
			ms: `SELECT status FROM archive_plans WITH (UPDLOCK, ROWLOCK) WHERE id=@p1`,
		},
		{
			name: "upsert do update",
			in:   `INSERT INTO llm_settings(id, mode, updated_at) VALUES (1,$1,now()) ON CONFLICT (id) DO UPDATE SET mode=$1, updated_at=now()`,
			pg:   `INSERT INTO llm_settings(id, mode, updated_at) VALUES (1,$1,now()) ON CONFLICT (id) DO UPDATE SET mode=$1, updated_at=now()`,
			my:   `INSERT INTO llm_settings(id, mode, updated_at) VALUES (1,?,UTC_TIMESTAMP(6)) ON DUPLICATE KEY UPDATE mode=?, updated_at=UTC_TIMESTAMP(6)`, myArgs: []int{0, 0},
			ms: `MERGE INTO llm_settings WITH (HOLDLOCK) AS tgt USING (SELECT 1 AS id, @p1 AS mode, SYSUTCDATETIME() AS updated_at) AS src ON (tgt.id = src.id) WHEN NOT MATCHED THEN INSERT (id, mode, updated_at) VALUES (src.id, src.mode, src.updated_at) WHEN MATCHED THEN UPDATE SET mode=@p1, updated_at=SYSUTCDATETIME();`,
		},
		{
			name: "upsert do nothing",
			in:   `INSERT INTO ledger_entries(id,plan_id,file_id) VALUES ($1,$2,$3) ON CONFLICT (plan_id,file_id) DO NOTHING`,
			pg:   `INSERT INTO ledger_entries(id,plan_id,file_id) VALUES ($1,$2,$3) ON CONFLICT (plan_id,file_id) DO NOTHING`,
			my:   `INSERT INTO ledger_entries(id,plan_id,file_id) VALUES (?,?,?) ON DUPLICATE KEY UPDATE plan_id=plan_id`, myArgs: []int{0, 1, 2},
			ms: `MERGE INTO ledger_entries WITH (HOLDLOCK) AS tgt USING (SELECT @p1 AS id, @p2 AS plan_id, @p3 AS file_id) AS src ON (tgt.plan_id = src.plan_id AND tgt.file_id = src.file_id) WHEN NOT MATCHED THEN INSERT (id, plan_id, file_id) VALUES (src.id, src.plan_id, src.file_id);`,
		},
		{
			name: "insert select do nothing",
			in:   `INSERT INTO r(id, c) SELECT gen_random_uuid(), $1::uuid FROM l ON CONFLICT (c, id) DO NOTHING`,
			pg:   `INSERT INTO r(id, c) SELECT gen_random_uuid(), $1::uuid FROM l ON CONFLICT (c, id) DO NOTHING`,
			my:   `INSERT INTO r(id, c) SELECT UUID(), ? FROM l ON DUPLICATE KEY UPDATE c=c`, myArgs: []int{0},
			ms: `INSERT INTO r(id, c) SELECT LOWER(CONVERT(CHAR(36), NEWID())), @p1 FROM l`,
		},
		{
			name: "to_char and substr",
			in:   `SELECT to_char(document_date,'YYYY-MM-DD'), substr(p, $1, 100000) FROM t`,
			pg:   `SELECT to_char(document_date,'YYYY-MM-DD'), substr(p, $1, 100000) FROM t`,
			my:   `SELECT DATE_FORMAT(document_date, '%Y-%m-%d'), substr(p, ?, 100000) FROM t`, myArgs: []int{0},
			ms: `SELECT CONVERT(CHAR(10), document_date, 23), SUBSTRING(p, @p1, 100000) FROM t`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := norm(translate(Postgres, c.in).sql); got != norm(c.pg) {
				t.Errorf("postgres:\n got %s\nwant %s", got, norm(c.pg))
			}
			my := translate(MySQL, c.in)
			if got := norm(my.sql); got != norm(c.my) {
				t.Errorf("mysql:\n got %s\nwant %s", got, norm(c.my))
			}
			if !reflect.DeepEqual(my.argIdx, c.myArgs) {
				t.Errorf("mysql args: got %v want %v", my.argIdx, c.myArgs)
			}
			if got := norm(translate(MSSQL, c.in).sql); got != norm(c.ms) {
				t.Errorf("mssql:\n got %s\nwant %s", got, norm(c.ms))
			}
		})
	}
}

func TestRedactDSN(t *testing.T) {
	for in, want := range map[string]string{
		"postgres://oa:s3cret@db:5432/oa?sslmode=disable":   "postgres://oa:***@db:5432/oa?sslmode=disable",
		"mysql://oa:s3cret@db:3306/oa":                      "mysql://oa:***@db:3306/oa",
		"oa:s3cret@tcp(db:3306)/oa?parseTime=true":          "oa:***@tcp(db:3306)/oa?parseTime=true",
		"sqlserver://sa:S3cret!@db:1433?database=x":         "sqlserver://sa:***@db:1433?database=x",
		"Server=db;User Id=sa;Password=S3cret!;Database=x":  "Server=db;User Id=sa;Password=***;Database=x",
		"host=db user=oa password=s3cret dbname=oa":         "host=db user=oa password=*** dbname=oa",
		"sqlserver://db?user id=sa&password=abc&database=x": "sqlserver://db?user id=sa&password=***&database=x",
	} {
		if got := RedactDSN(in); got != want {
			t.Errorf("RedactDSN(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEscapeLike(t *testing.T) {
	if got := EscapeLike(`50%_a!b`); got != `50!%!_a!!b` {
		t.Fatal(got)
	}
}
