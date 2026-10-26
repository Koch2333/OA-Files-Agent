package sqldb

import (
	"strings"
	"testing"
)

// The classification options and the ledger "其他" filter use only the portable subset; this pins the output
// per dialect (the values travel as parameters, so no N'...' literal issue on SQL Server).
func TestTranslateClassificationQueries(t *testing.T) {
	filter := `SELECT count(*) FROM ledger_entries WHERE status='archived' AND department IN ($1,$2,$3) AND document_type=$4`
	if got := norm(translate(MySQL, filter).sql); got != `SELECT count(*) FROM ledger_entries WHERE status='archived' AND department IN (?,?,?) AND document_type=?` {
		t.Errorf("mysql filter: %s", got)
	}
	if got := norm(translate(MSSQL, filter).sql); got != `SELECT count(*) FROM ledger_entries WHERE status='archived' AND department IN (@p1,@p2,@p3) AND document_type=@p4` {
		t.Errorf("mssql filter: %s", got)
	}
	usage := `SELECT department, count(*) FROM ledger_entries WHERE status='archived' AND department<>'' GROUP BY department ORDER BY count(*) DESC, department LIMIT $1`
	if got := norm(translate(MySQL, usage).sql); !strings.HasSuffix(got, "ORDER BY count(*) DESC, department LIMIT ?") {
		t.Errorf("mysql usage: %s", got)
	}
	got := norm(translate(MSSQL, usage).sql)
	if !strings.HasPrefix(got, "SELECT TOP (@p1) department, count(*)") || strings.Contains(got, "LIMIT") {
		t.Errorf("mssql usage must use TOP: %s", got)
	}
}
