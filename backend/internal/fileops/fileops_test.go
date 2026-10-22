package fileops

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"oa-agent/backend/internal/dbtest"
	"oa-agent/backend/internal/fsman"
	"oa-agent/backend/internal/imports"
	"oa-agent/backend/internal/store"
)

// seedArchive writes real files and a matching ledger (via a real plan execution record).
func seedArchive(t *testing.T) (*Service, string, map[string]string) {
	t.Helper()
	st := dbtest.New(t)
	root := t.TempDir()
	ctx := context.Background()
	id, _ := imports.NewID()
	now := time.Now().UTC()
	b := imports.Batch{Job: imports.Job{ID: id, Status: "queued", TotalFiles: 3, CreatedAt: now, UpdatedAt: now}, Files: []imports.File{}, Failures: []imports.Failure{}}
	rels := []string{"财务部/2026/合同/a.pdf", "财务部/2026/合同/b.pdf", "人事部/2026/通知/c.pdf"}
	fileIDs := map[string]string{}
	for _, rel := range rels {
		fid, _ := imports.NewID()
		fileIDs[rel] = fid
		b.Files = append(b.Files, imports.File{ID: fid, TaskID: id, SourceName: filepath.Base(rel), SourcePath: "in/" + filepath.Base(rel), MIMEType: "application/pdf", SHA256: strings.Repeat("a", 64), SizeBytes: 1, Evidence: []string{}, ArchiveStatus: "pending", CreatedAt: now, StoredPath: "/x"})
		p := filepath.Join(root, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("data-"+rel), 0o644)
	}
	if _, _, err := st.Save(ctx, b); err != nil {
		t.Fatal(err)
	}
	var items []store.PlanItem
	for _, rel := range rels {
		items = append(items, store.PlanItem{FileID: fileIDs[rel], OriginalName: filepath.Base(rel), SuggestedName: filepath.Base(rel), SourcePath: "in/x", DestinationPath: rel, Department: sp(strings.Split(rel, "/")[0]), DocumentType: sp("合同"), DocumentDate: sp("2026-03-15"), Warnings: []string{}})
	}
	plan, err := st.CreatePlan(ctx, id, nil, items)
	if err != nil {
		t.Fatal(err)
	}
	plan, _ = st.ApprovePlan(ctx, plan.ID, plan.Version, "u")
	op, plan, _, err := st.BeginExecution(ctx, plan.ID, "k")
	if err != nil {
		t.Fatal(err)
	}
	var res []store.OpResult
	for _, rel := range rels {
		res = append(res, store.OpResult{FileID: fileIDs[rel], FinalRel: rel, Status: "published", SHA256: "ab"})
	}
	if _, err = st.FinishExecution(ctx, op, plan, res); err != nil {
		t.Fatal(err)
	}
	return &Service{Store: st, FS: &fsman.Manager{Root: root}, AllowReveal: true}, root, fileIDs
}

func sp(s string) *string { return &s }

func ledgerPaths(t *testing.T, s *Service) map[string]bool {
	t.Helper()
	rows, _, err := s.Store.Ledger(context.Background(), store.Page{Size: 100}, store.LedgerFilter{})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, r := range rows {
		out[r.ArchivePath] = true
	}
	return out
}

func TestListShowsLedgerInfoForArchivedFiles(t *testing.T) {
	s, _, _ := seedArchive(t)
	l, err := s.List(context.Background(), "财务部/2026/合同")
	if err != nil || len(l.Entries) != 2 || l.Entries[0].Ledger == nil || l.Entries[0].Ledger.Department != "财务部" || l.Entries[0].Ledger.OriginalName != "a.pdf" {
		t.Fatalf("listing: %v %+v", err, l)
	}
	if l.Parent == nil || *l.Parent != "财务部/2026" {
		t.Fatalf("parent = %v", l.Parent)
	}
	top, _ := s.List(context.Background(), "")
	if top.Parent != nil || top.Entries[0].Ledger != nil {
		t.Fatalf("root has no parent and folders carry no ledger info: %+v", top)
	}
}

func TestRenameAndFolderMoveKeepTheLedgerTruthful(t *testing.T) {
	s, root, _ := seedArchive(t)
	ctx := context.Background()
	e, n, err := s.Move(ctx, "财务部/2026/合同/a.pdf", "财务部/2026/合同/甲方合同.pdf", "u")
	if err != nil || n != 1 || e.Ledger == nil || e.Name != "甲方合同.pdf" {
		t.Fatalf("rename: %v n=%d %+v", err, n, e)
	}
	paths := ledgerPaths(t, s)
	if !paths["财务部/2026/合同/甲方合同.pdf"] || paths["财务部/2026/合同/a.pdf"] {
		t.Fatalf("ledger paths after rename: %v", paths)
	}
	rows, _, _ := s.Store.Ledger(ctx, store.Page{}, store.LedgerFilter{Q: "甲方合同"})
	if len(rows) != 1 || rows[0].CanonicalName != "甲方合同.pdf" || rows[0].OriginalName != "a.pdf" {
		t.Fatalf("canonical name follows the rename while the original name is preserved: %+v", rows)
	}
	if _, n, err = s.Move(ctx, "财务部", "行政部", "u"); err != nil || n != 2 {
		t.Fatalf("folder rename must rewrite every ledger path below it: %v n=%d", err, n)
	}
	paths = ledgerPaths(t, s)
	if !paths["行政部/2026/合同/甲方合同.pdf"] || !paths["行政部/2026/合同/b.pdf"] || !paths["人事部/2026/通知/c.pdf"] || len(paths) != 3 {
		t.Fatalf("ledger paths after folder move: %v", paths)
	}
	if _, err := os.Stat(filepath.Join(root, "行政部", "2026", "合同", "b.pdf")); err != nil {
		t.Fatal("disk must match")
	}
	if _, _, err = s.Move(ctx, "行政部/2026/合同/b.pdf", "人事部/2026/通知/c.pdf", "u"); err == nil {
		t.Fatal("moving onto an existing file must fail")
	}
	if !ledgerPaths(t, s)["行政部/2026/合同/b.pdf"] {
		t.Fatal("a refused move must not touch the ledger")
	}
}

func TestTrashRemovesFromLedgerAndRestoreBringsItBack(t *testing.T) {
	s, root, ids := seedArchive(t)
	ctx := context.Background()
	it, n, err := s.Trash(ctx, "财务部/2026/合同", "u")
	if err != nil || n != 2 || !it.IsDir {
		t.Fatalf("trash folder: %v n=%d %+v", err, n, it)
	}
	if len(ledgerPaths(t, s)) != 1 {
		t.Fatalf("removed files must leave the ledger: %v", ledgerPaths(t, s))
	}
	f, _ := s.Store.GetFile(ctx, ids["财务部/2026/合同/a.pdf"])
	if f.ArchiveStatus != "pending" {
		t.Fatalf("file library must stop claiming it is archived: %s", f.ArchiveStatus)
	}
	if _, err := os.Stat(filepath.Join(root, "财务部", "2026", "合同")); !os.IsNotExist(err) {
		t.Fatal("folder must be gone from the archive")
	}
	// occupy the old location so the restore gets a suffix and the ledger follows
	os.MkdirAll(filepath.Join(root, "财务部", "2026", "合同"), 0o755)
	p, n, err := s.Restore(ctx, it.ID, "u")
	if err != nil || n != 2 || p != "财务部/2026/合同 (已恢复)" {
		t.Fatalf("restore: %v n=%d %q", err, n, p)
	}
	paths := ledgerPaths(t, s)
	if !paths["财务部/2026/合同 (已恢复)/a.pdf"] || !paths["财务部/2026/合同 (已恢复)/b.pdf"] || len(paths) != 3 {
		t.Fatalf("ledger after restore: %v", paths)
	}
	f, _ = s.Store.GetFile(ctx, ids["财务部/2026/合同/a.pdf"])
	if f.ArchiveStatus != "archived" {
		t.Fatalf("restored file is archived again: %s", f.ArchiveStatus)
	}
	if _, _, err := s.Restore(ctx, it.ID, "u"); err == nil {
		t.Fatal("a trash item can only be restored once")
	}
}

func TestTrashingOneFileOnlyAffectsItsOwnLedgerRow(t *testing.T) {
	s, _, _ := seedArchive(t)
	ctx := context.Background()
	it1, n, err := s.Trash(ctx, "财务部/2026/合同/a.pdf", "u")
	if err != nil || n != 1 {
		t.Fatalf("%v %d", err, n)
	}
	if _, n, err = s.Trash(ctx, "财务部/2026/合同/b.pdf", "u"); err != nil || n != 1 {
		t.Fatalf("%v %d", err, n)
	}
	if _, n, err = s.Restore(ctx, it1.ID, "u"); err != nil || n != 1 {
		t.Fatalf("restoring one item must restore only its ledger row: %v n=%d", err, n)
	}
	paths := ledgerPaths(t, s)
	if !paths["财务部/2026/合同/a.pdf"] || paths["财务部/2026/合同/b.pdf"] || len(paths) != 2 {
		t.Fatalf("ledger: %v", paths)
	}
}

func TestRevealRespectsConfigAndPlatform(t *testing.T) {
	s, _, _ := seedArchive(t)
	s.AllowReveal = false
	if _, err := s.Reveal(context.Background(), "", "u"); err != ErrRevealDisabled {
		t.Fatalf("disabled: %v", err)
	}
	s.AllowReveal = true
	if !revealSupported() {
		if _, err := s.Reveal(context.Background(), "", "u"); err != ErrRevealUnsupported {
			t.Fatalf("non-Windows: %v", err)
		}
	}
	if s.CanReveal() != revealSupported() {
		t.Fatal("CanReveal must mirror platform support")
	}
	if _, err := s.Reveal(context.Background(), "../../Windows", "u"); err == nil {
		t.Fatal("paths outside the archive root can never be revealed")
	}
}
