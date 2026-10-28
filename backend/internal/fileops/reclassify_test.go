package fileops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"oa-agent/backend/internal/store"
)

// testDerive mimics the archive rule: <dept>/<year or 未知日期>/<type>/<name>.
func testDerive(_ context.Context, it store.PlanItem) (string, string, []string, error) {
	year := "未知日期"
	if it.DocumentDate != nil && len(*it.DocumentDate) >= 4 {
		year = (*it.DocumentDate)[:4]
	}
	return it.OriginalName, *it.Department + "/" + year + "/" + *it.DocumentType + "/" + it.OriginalName, nil, nil
}

func TestReclassifyMovesTheFileAndUpdatesTheLedger(t *testing.T) {
	s, root, ids := seedArchive(t)
	ctx := context.Background()
	rows, _, _ := s.Store.Ledger(ctx, store.Page{}, store.LedgerFilter{})
	var id string
	for _, r := range rows {
		if r.ArchivePath == "财务部/2026/合同/a.pdf" {
			id = r.ID
		}
	}
	if id == "" {
		t.Fatal("seed entry missing")
	}
	dept, date := "采购部", "2026-05-06"
	e, err := s.Reclassify(ctx, id, Reclass{Department: &dept, DocumentDate: &date}, testDerive, "alice")
	if err != nil {
		t.Fatal(err)
	}
	want := "采购部/2026/合同/a.pdf"
	if e.ArchivePath != want || e.Department != "采购部" || e.DocumentDate != "2026-05-06" || e.DocumentType != "合同" {
		t.Fatalf("ledger entry: %+v", e)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(want))); err != nil {
		t.Fatalf("file not at the new path: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "财务部", "2026", "合同", "a.pdf")); !os.IsNotExist(err) {
		t.Fatal("the old copy must be gone (moved, not copied)")
	}
	_ = ids
	// clearing the date moves it under 未知日期
	empty := ""
	e, err = s.Reclassify(ctx, id, Reclass{DocumentDate: &empty}, testDerive, "alice")
	if err != nil || e.ArchivePath != "采购部/未知日期/合同/a.pdf" || e.DocumentDate != "" {
		t.Fatalf("clear date: %+v %v", e, err)
	}
	// invalid input is rejected and nothing moves
	bad := "2026/5/6"
	if _, err := s.Reclassify(ctx, id, Reclass{DocumentDate: &bad}, testDerive, "alice"); !errors.Is(err, ErrReclassInvalid) {
		t.Fatalf("bad date: %v", err)
	}
	blank := " "
	if _, err := s.Reclassify(ctx, id, Reclass{Department: &blank}, testDerive, "alice"); !errors.Is(err, ErrReclassInvalid) {
		t.Fatalf("blank department: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "采购部", "未知日期", "合同", "a.pdf")); err != nil {
		t.Fatalf("a rejected correction must not move anything: %v", err)
	}
	if _, err := s.Reclassify(ctx, "00000000-0000-0000-0000-000000000000", Reclass{Department: &dept}, testDerive, "alice"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown entry: %v", err)
	}
	if !strings.Contains(e.ArchivePath, "未知日期") {
		t.Fatal("unreachable")
	}
}
