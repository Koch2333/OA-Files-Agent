package supervisor

import (
	"path/filepath"
	"testing"
)

func TestNewLayoutDataDirOverride(t *testing.T) {
	root := filepath.Join("prog", "oa")
	t.Setenv("OA_DATA_DIR", "")
	if l := NewLayout(root); l.Data != filepath.Join(root, "data") || l.PGDir != filepath.Join(root, "pgsql") {
		t.Fatalf("default layout wrong: %+v", l)
	}
	data := filepath.Join("user", "data")
	t.Setenv("OA_DATA_DIR", data)
	l := NewLayout(root)
	if l.Data != data || l.Logs != filepath.Join(data, "logs") || l.Spool != filepath.Join(data, "spool") || l.Archive != filepath.Join(data, "archive") {
		t.Fatalf("data dir override not applied: %+v", l)
	}
	if l.Web != filepath.Join(root, "web") || l.WorkerExe == "" || l.Migrations != filepath.Join(root, "migrations") {
		t.Fatalf("program paths must stay under root: %+v", l)
	}
}
