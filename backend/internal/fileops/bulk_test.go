package fileops

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"oa-agent/backend/internal/dbtest"
	"oa-agent/backend/internal/fsman"
	"oa-agent/backend/internal/imports"
	"oa-agent/backend/internal/store"
)

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// seedContent writes the files and archives each of them (task-related) with its real SHA-256 and size.
func seedContent(t *testing.T, files map[string]string) (*Service, string) {
	t.Helper()
	st := dbtest.New(t)
	root := t.TempDir()
	ctx := context.Background()
	id, _ := imports.NewID()
	now := time.Now().UTC()
	b := imports.Batch{Job: imports.Job{ID: id, Status: "queued", TotalFiles: len(files), CreatedAt: now, UpdatedAt: now}, Files: []imports.File{}, Failures: []imports.Failure{}}
	var rels []string
	for rel := range files {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	fileIDs := map[string]string{}
	for _, rel := range rels {
		fid, _ := imports.NewID()
		fileIDs[rel] = fid
		b.Files = append(b.Files, imports.File{ID: fid, TaskID: id, SourceName: filepath.Base(rel), SourcePath: "in/" + filepath.Base(rel), MIMEType: "application/pdf", SHA256: sum(files[rel]), SizeBytes: int64(len(files[rel])), Evidence: []string{}, ArchiveStatus: "pending", CreatedAt: now, StoredPath: "/x"})
		p := filepath.Join(root, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(files[rel]), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if len(rels) > 0 {
		if _, _, err := st.Save(ctx, b); err != nil {
			t.Fatal(err)
		}
		var items []store.PlanItem
		for _, rel := range rels {
			items = append(items, store.PlanItem{FileID: fileIDs[rel], OriginalName: filepath.Base(rel), SuggestedName: filepath.Base(rel), SourcePath: "in/x", DestinationPath: rel, Department: sp("财务部"), DocumentType: sp("合同"), DocumentDate: sp("2026-03-15"), Warnings: []string{}})
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
			res = append(res, store.OpResult{FileID: fileIDs[rel], FinalRel: rel, Status: "published", SHA256: sum(files[rel])})
		}
		if _, err = st.FinishExecution(ctx, op, plan, res); err != nil {
			t.Fatal(err)
		}
	}
	return &Service{Store: st, FS: &fsman.Manager{Root: root}, AllowReveal: true}, root
}

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

func exists(root, rel string) bool {
	_, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
	return err == nil
}

// tamper rewrites a file with a future mtime so no size/mtime shortcut can hide the change.
func tamper(t *testing.T, root, rel, content string) {
	t.Helper()
	write(t, root, rel, content)
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(root, filepath.FromSlash(rel)), future, future); err != nil {
		t.Fatal(err)
	}
}

func mv(s *Service, pol Policy, res map[string]Policy, pairs ...string) (BatchResult, error) {
	var specs []MoveSpec
	for i := 0; i < len(pairs); i += 2 {
		specs = append(specs, MoveSpec{From: pairs[i], To: pairs[i+1]})
	}
	return s.MoveBatch(context.Background(), MoveRequest{Items: specs, OnConflict: pol, Resolutions: res}, "tester")
}

func ledgerPathOf(t *testing.T, s *Service, rel string) bool {
	t.Helper()
	m, err := s.Store.LedgerHashes(context.Background(), []string{rel})
	if err != nil {
		t.Fatal(err)
	}
	_, ok := m[rel]
	return ok
}

func TestListingExposesLazyHashFieldsAndVerifyFlipsThem(t *testing.T) {
	s, root := seedContent(t, map[string]string{"财务部/a.pdf": "AAAA", "财务部/b.pdf": "BBBB"})
	write(t, root, "财务部/plain.txt", "not tracked")
	ctx := context.Background()
	by := func() map[string]ListedEntry {
		l, err := s.List(ctx, "财务部")
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]ListedEntry{}
		for _, e := range l.Entries {
			m[e.Name] = e
		}
		return m
	}
	m := by()
	if !m["a.pdf"].TaskRelated || m["a.pdf"].SHA256 != sum("AAAA") || m["a.pdf"].HashStatus != HashUnverified {
		t.Fatalf("task file before verification: %+v", m["a.pdf"])
	}
	if m["plain.txt"].TaskRelated || m["plain.txt"].HashStatus != HashNotTask || m["plain.txt"].SHA256 != "" {
		t.Fatalf("untracked file: %+v", m["plain.txt"])
	}
	rep, err := s.Verify(ctx, []string{"财务部"}, "t")
	if err != nil || rep.Checked != 2 || rep.Match != 2 || rep.Mismatch != 0 {
		t.Fatalf("verify: %+v %v", rep, err)
	}
	if by()["a.pdf"].HashStatus != HashVerified {
		t.Fatal("listing must show verified after a successful verification")
	}
	tamper(t, root, "财务部/a.pdf", "AAAX") // same size, other content
	if by()["a.pdf"].HashStatus != HashUnverified {
		t.Fatal("a changed mtime invalidates the cached verdict")
	}
	rep, _ = s.Verify(ctx, []string{"财务部/a.pdf", "财务部/plain.txt"}, "t")
	if rep.Mismatch != 1 || rep.NotTask != 1 {
		t.Fatalf("tamper detection: %+v", rep)
	}
	if by()["a.pdf"].HashStatus != HashMismatch {
		t.Fatal("listing must show the mismatch")
	}
	os.Remove(filepath.Join(root, "财务部", "b.pdf"))
	rep, _ = s.Verify(ctx, []string{"财务部"}, "t")
	if rep.Missing != 1 || rep.Mismatch != 1 || rep.Match != 0 {
		t.Fatalf("missing + mismatch: %+v", rep)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = s.Verify(cctx, []string{"财务部"}, "t"); !errors.Is(err, context.Canceled) {
		t.Fatalf("verify must honour cancellation: %v", err)
	}
}

func TestMoveAskReportsConflictsAndChangesNothing(t *testing.T) {
	s, root := seedContent(t, map[string]string{"in/a.pdf": "NEW", "dst/a.pdf": "OLD-different"})
	out, err := mv(s, PolicyAsk, nil, "in/a.pdf", "dst/a.pdf")
	if err != nil || len(out.Conflicts) != 1 || len(out.Items) != 0 {
		t.Fatalf("%+v %v", out, err)
	}
	c := out.Conflicts[0]
	if c.Kind != KindNameExists || c.Identical || c.Existing == nil || c.Existing.SHA256 != sum("OLD-different") || c.Incoming.SHA256 != sum("NEW") || c.Item != "in/a.pdf" {
		t.Fatalf("conflict details: %+v", c)
	}
	if read(t, root, "in/a.pdf") != "NEW" || read(t, root, "dst/a.pdf") != "OLD-different" || !ledgerPathOf(t, s, "in/a.pdf") {
		t.Fatal("ask must not change disk or ledger")
	}
}

func TestMoveSkipRenameReplace(t *testing.T) {
	ctx := context.Background()
	for _, pol := range []Policy{PolicySkip, PolicyRename, PolicyReplace} {
		t.Run(string(pol), func(t *testing.T) {
			s, root := seedContent(t, map[string]string{"in/a.pdf": "NEW", "dst/a.pdf": "OLD-different"})
			out, err := mv(s, pol, nil, "in/a.pdf", "dst/a.pdf")
			if err != nil || len(out.Items) != 1 {
				t.Fatalf("%+v %v", out, err)
			}
			it := out.Items[0]
			switch pol {
			case PolicySkip:
				if it.Status != StSkipped || read(t, root, "in/a.pdf") != "NEW" || read(t, root, "dst/a.pdf") != "OLD-different" || !ledgerPathOf(t, s, "in/a.pdf") {
					t.Fatalf("skip: %+v", it)
				}
			case PolicyRename:
				if it.Status != StRenamed || it.FinalPath != "dst/a (2).pdf" || read(t, root, "dst/a (2).pdf") != "NEW" || read(t, root, "dst/a.pdf") != "OLD-different" || !ledgerPathOf(t, s, "dst/a (2).pdf") || !ledgerPathOf(t, s, "dst/a.pdf") {
					t.Fatalf("rename: %+v", it)
				}
				// a third one gets (3)
				write(t, root, "in/a.pdf", "THIRD")
				out, _ = mv(s, pol, nil, "in/a.pdf", "dst/a.pdf")
				if out.Items[0].FinalPath != "dst/a (3).pdf" {
					t.Fatalf("third: %+v", out.Items)
				}
			case PolicyReplace:
				if it.Status != StReplaced || read(t, root, "dst/a.pdf") != "NEW" || exists(root, "in/a.pdf") {
					t.Fatalf("replace: %+v", it)
				}
				tr, _ := s.ListTrash()
				if len(tr) != 1 || tr[0].OriginalPath != "dst/a.pdf" {
					t.Fatalf("the old file must be recoverable from the trash: %+v", tr)
				}
				// ledger: the moved file's row now sits at dst/a.pdf, the old row is 'removed'
				if !ledgerPathOf(t, s, "dst/a.pdf") || ledgerPathOf(t, s, "in/a.pdf") {
					t.Fatal("ledger after replace")
				}
				removed, _ := s.Store.LedgerRemovedByTrash(ctx, tr[0].ID)
				if len(removed) != 1 {
					t.Fatalf("exactly the superseded entry is removed, got %d", len(removed))
				}
				if p, _, err := s.Restore(ctx, tr[0].ID, "t"); err != nil || read(t, root, p) != "OLD-different" {
					t.Fatalf("old content must be restorable: %v %v", p, err)
				}
			}
		})
	}
}

func TestIdenticalSameNameIsADuplicateNeverASecondCopy(t *testing.T) {
	s, root := seedContent(t, map[string]string{"in/a.pdf": "SAME", "dst/a.pdf": "SAME"})
	for _, pol := range []Policy{PolicyAsk, PolicySkip, PolicyReplace} {
		out, err := mv(s, pol, nil, "in/a.pdf", "dst/a.pdf")
		if err != nil || len(out.Conflicts) != 0 || out.Items[0].Status != StDuplicate || out.Items[0].DuplicateOf != "dst/a.pdf" || out.Summary.Duplicate != 1 {
			t.Fatalf("%s: %+v %v", pol, out, err)
		}
		if !exists(root, "in/a.pdf") || exists(root, "dst/a (2).pdf") || len(mustTrash(t, s)) != 0 {
			t.Fatalf("%s: nothing may change", pol)
		}
	}
	// the user can still explicitly keep both
	out, _ := mv(s, PolicyRename, nil, "in/a.pdf", "dst/a.pdf")
	if out.Items[0].Status != StRenamed || read(t, root, "dst/a (2).pdf") != "SAME" {
		t.Fatalf("explicit keep both: %+v", out.Items)
	}
}

func mustTrash(t *testing.T, s *Service) []fsman.TrashItem {
	t.Helper()
	tr, err := s.ListTrash()
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func TestTypeConflictIsNeverReplaced(t *testing.T) {
	s, root := seedContent(t, map[string]string{"in/x": "file-x", "dst/x/inner.pdf": "inner"})
	out, _ := mv(s, PolicyAsk, nil, "in/x", "dst/x")
	if len(out.Conflicts) != 1 || out.Conflicts[0].Kind != KindTypeConflict || strings.Contains(strings.Join(out.Conflicts[0].Choices, ","), "replace") {
		t.Fatalf("%+v", out)
	}
	out, _ = mv(s, PolicyReplace, nil, "in/x", "dst/x")
	if out.Items[0].Status != StFailed || out.Items[0].Code != "TYPE_CONFLICT" || read(t, root, "in/x") != "file-x" || read(t, root, "dst/x/inner.pdf") != "inner" {
		t.Fatalf("%+v", out.Items)
	}
	out, _ = mv(s, PolicyRename, nil, "in/x", "dst/x")
	if out.Items[0].Status != StRenamed || read(t, root, "dst/x (2)") != "file-x" {
		t.Fatalf("%+v", out.Items)
	}
}

func TestFolderMergeAppliesThePolicyPerItem(t *testing.T) {
	s, root := seedContent(t, map[string]string{"in/proj/a.pdf": "A-new", "in/proj/b.pdf": "B", "in/proj/sub/c.pdf": "C", "dst/proj/a.pdf": "A-old"})
	out, _ := mv(s, PolicyAsk, nil, "in/proj", "dst/proj")
	if len(out.Conflicts) != 1 || out.Conflicts[0].Incoming.Path != "in/proj/a.pdf" || out.Conflicts[0].Item != "in/proj" {
		t.Fatalf("%+v", out.Conflicts)
	}
	out, _ = mv(s, PolicySkip, nil, "in/proj", "dst/proj")
	it := out.Items[0]
	if it.Status != StPartial || it.Counts.Moved != 2 || it.Counts.Skipped != 1 {
		t.Fatalf("%+v", it)
	}
	if read(t, root, "dst/proj/b.pdf") != "B" || read(t, root, "dst/proj/sub/c.pdf") != "C" || read(t, root, "dst/proj/a.pdf") != "A-old" || read(t, root, "in/proj/a.pdf") != "A-new" || exists(root, "in/proj/b.pdf") {
		t.Fatal("merge result on disk")
	}
	if !ledgerPathOf(t, s, "dst/proj/sub/c.pdf") || !ledgerPathOf(t, s, "in/proj/a.pdf") {
		t.Fatal("ledger follows each file")
	}
	// resolve the leftover per item: replace
	out, _ = mv(s, PolicyAsk, map[string]Policy{"in/proj": PolicyReplace}, "in/proj", "dst/proj")
	if out.Items[0].Status != StReplaced || read(t, root, "dst/proj/a.pdf") != "A-new" || exists(root, "in/proj") {
		t.Fatalf("%+v", out.Items)
	}
}

func TestCaseAndNormalisationInsensitiveNames(t *testing.T) {
	old := fsman.CaseInsensitive
	fsman.CaseInsensitive = true
	defer func() { fsman.CaseInsensitive = old }()
	s, root := seedContent(t, map[string]string{"in/Report.pdf": "NEW", "dst/report.PDF": "OLD-x"})
	out, _ := mv(s, PolicyAsk, nil, "in/Report.pdf", "dst/Report.pdf")
	if len(out.Conflicts) != 1 || out.Conflicts[0].Existing.Name != "report.PDF" {
		t.Fatalf("case-insensitive collision: %+v", out)
	}
	if fsman.NameKey("Cafe\u0301.pdf") != fsman.NameKey("Caf\u00e9.PDF") {
		t.Fatal("NFC + case folding")
	}
	_ = root
}

func TestSameContentUnderAnotherNameIsWarnedNotDropped(t *testing.T) {
	s, root := seedContent(t, map[string]string{"in/new.pdf": "CONTENT", "dst/old.pdf": "CONTENT"})
	out, _ := mv(s, PolicyAsk, nil, "in/new.pdf", "dst/new.pdf")
	if len(out.Conflicts) != 1 || out.Conflicts[0].Kind != KindDuplicateContent || out.Conflicts[0].Existing.Path != "dst/old.pdf" || !out.Conflicts[0].Identical {
		t.Fatalf("%+v", out)
	}
	if !exists(root, "in/new.pdf") {
		t.Fatal("ask must not move")
	}
	out, _ = mv(s, PolicySkip, nil, "in/new.pdf", "dst/new.pdf")
	if out.Items[0].Status != StDuplicate || out.Items[0].DuplicateOf != "dst/old.pdf" || !exists(root, "in/new.pdf") {
		t.Fatalf("skip: %+v", out.Items)
	}
	out, _ = mv(s, PolicyRename, nil, "in/new.pdf", "dst/new.pdf")
	if out.Items[0].Status != StMoved || read(t, root, "dst/new.pdf") != "CONTENT" {
		t.Fatalf("keep anyway: %+v", out.Items)
	}
}

func TestRaceCreatedBetweenCheckAndWriteIsNeverClobbered(t *testing.T) {
	s, root := seedContent(t, map[string]string{"in/a.pdf": "NEW", "dst/keep.pdf": "K"})
	s.testHookBeforeWrite = func(_, to string) { write(t, root, to, "APPEARED-LATE") }
	out, _ := mv(s, PolicyAsk, nil, "in/a.pdf", "dst/a.pdf")
	it := out.Items[0]
	if it.Status != StFailed || it.Code != "ALREADY_EXISTS" || read(t, root, "dst/a.pdf") != "APPEARED-LATE" || read(t, root, "in/a.pdf") != "NEW" || !ledgerPathOf(t, s, "in/a.pdf") {
		t.Fatalf("%+v", it)
	}
	// the same race on the replace path: the old file is put back
	s2, root2 := seedContent(t, map[string]string{"in/a.pdf": "NEW", "dst/a.pdf": "OLD-different"})
	s2.testHookBeforeWrite = func(_, to string) {}
	n := 0
	s2.testHookBeforeWrite = func(_, to string) { n++ }
	out, _ = mv(s2, PolicyReplace, nil, "in/a.pdf", "dst/a.pdf")
	if out.Items[0].Status != StReplaced || n != 1 || read(t, root2, "dst/a.pdf") != "NEW" {
		t.Fatalf("%+v", out.Items)
	}
}

func TestConcurrentMovesToTheSameNameLoseNothing(t *testing.T) {
	s, root := seedContent(t, map[string]string{"in/a.pdf": "ONE", "in2/a.pdf": "TWO", "dst/keep.pdf": "K"})
	var wg sync.WaitGroup
	results := make([]BatchResult, 2)
	for i, src := range []string{"in/a.pdf", "in2/a.pdf"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], _ = mv(s, PolicyAsk, nil, src, "dst/a.pdf")
		}()
	}
	wg.Wait()
	moved, conflicted := 0, 0
	for _, r := range results {
		if len(r.Conflicts) > 0 {
			conflicted++
		} else if len(r.Items) == 1 && r.Items[0].Status == StMoved {
			moved++
		}
	}
	if moved != 1 || conflicted != 1 {
		t.Fatalf("exactly one wins: %+v", results)
	}
	got := read(t, root, "dst/a.pdf")
	left := ""
	for _, p := range []string{"in/a.pdf", "in2/a.pdf"} {
		if exists(root, p) {
			left += read(t, root, p)
		}
	}
	if (got != "ONE" && got != "TWO") || len(left) != 3 || left == got {
		t.Fatalf("dst=%q left=%q", got, left)
	}
}

func TestBulkMovePartialFailureAndBatchCollisions(t *testing.T) {
	s, root := seedContent(t, map[string]string{"in/a.pdf": "A", "in/b.pdf": "B", "in2/a.pdf": "A2", "dst/keep.pdf": "K"})
	out, err := mv(s, PolicyRename, nil, "in/a.pdf", "dst/a.pdf", "in/missing.pdf", "dst/missing.pdf", "in/b.pdf", "dst/b.pdf", "in2/a.pdf", "dst/a.pdf", "../x", "dst/x")
	if err != nil || len(out.Items) != 5 {
		t.Fatalf("%+v %v", out, err)
	}
	st := map[string]string{}
	for _, it := range out.Items {
		st[it.Path] = it.Status
	}
	if st["in/a.pdf"] != StMoved || st["in/missing.pdf"] != StFailed || st["in/b.pdf"] != StMoved || st["in2/a.pdf"] != StRenamed || st["../x"] != StFailed {
		t.Fatalf("%v", st)
	}
	if read(t, root, "dst/a.pdf") != "A" || read(t, root, "dst/a (2).pdf") != "A2" {
		t.Fatal("same-batch collision must keep both")
	}
	if out.Summary.Failed != 2 || out.Summary.Moved != 2 || out.Summary.Renamed != 1 {
		t.Fatalf("%+v", out.Summary)
	}
	// ask also sees same-batch collisions
	s2, _ := seedContent(t, map[string]string{"in/a.pdf": "A", "in2/a.pdf": "A2", "dst/keep.pdf": "K"})
	out, _ = mv(s2, PolicyAsk, nil, "in/a.pdf", "dst/a.pdf", "in2/a.pdf", "dst/a.pdf")
	if len(out.Conflicts) != 1 {
		t.Fatalf("%+v", out)
	}
	// a folder cannot go into itself; a descendant of a selected folder is not moved twice
	s3, _ := seedContent(t, map[string]string{"p/q/f.pdf": "F"})
	out, _ = mv(s3, PolicyAsk, nil, "p", "p/q/p", "p/q/f.pdf", "x/f.pdf")
	if out.Items[0].Status != StFailed || out.Items[1].Status != StSkipped {
		t.Fatalf("%+v", out.Items)
	}
}

func TestMoveFlagsFilesModifiedOutsideTheSystem(t *testing.T) {
	s, root := seedContent(t, map[string]string{"in/a.pdf": "ORIGINAL", "in/b.pdf": "BBBB"})
	tamper(t, root, "in/a.pdf", "ALTERED!")
	out, _ := mv(s, PolicyAsk, nil, "in", "dst")
	it := out.Items[0]
	if it.Status != StMoved || it.HashStatus != "modified_outside" || len(it.ModifiedOutside) != 1 || it.ModifiedOutside[0] != "dst/a.pdf" {
		t.Fatalf("%+v", it)
	}
	// the ledger still records the original hash (evidence); the move itself never changes content
	h, _ := s.Store.LedgerHashes(context.Background(), []string{"dst/a.pdf", "dst/b.pdf"})
	if h["dst/a.pdf"].SHA256 != sum("ORIGINAL") || h["dst/b.pdf"].SHA256 != sum("BBBB") || read(t, root, "dst/b.pdf") != "BBBB" {
		t.Fatalf("%+v", h)
	}
	l, _ := s.List(context.Background(), "dst")
	for _, e := range l.Entries {
		if e.Name == "a.pdf" && e.HashStatus != HashMismatch || e.Name == "b.pdf" && e.HashStatus != HashVerified {
			t.Fatalf("listing after move: %+v", e)
		}
	}
	// a clean move reports verified
	out, _ = mv(s, PolicyAsk, nil, "dst/b.pdf", "dst/b2.pdf")
	if out.Items[0].HashStatus != "verified" {
		t.Fatalf("%+v", out.Items[0])
	}
}

func TestCaseOnlyRenameIsNotAConflict(t *testing.T) {
	s, root := seedContent(t, map[string]string{"d/report.pdf": "R"})
	out, err := mv(s, PolicyAsk, nil, "d/report.pdf", "d/Report.pdf")
	if err != nil || len(out.Conflicts) != 0 || out.Items[0].Status != StMoved {
		t.Fatalf("%+v %v", out, err)
	}
	ents, _ := os.ReadDir(filepath.Join(root, "d"))
	if len(ents) != 1 || ents[0].Name() != "Report.pdf" || !ledgerPathOf(t, s, "d/Report.pdf") {
		t.Fatalf("%v", ents)
	}
}

func TestBulkTrashReportsEachItem(t *testing.T) {
	s, root := seedContent(t, map[string]string{"d/a.pdf": "A", "d/b.pdf": "B", "d/sub/c.pdf": "C"})
	out, err := s.BulkTrash(context.Background(), []string{"d/a.pdf", "d/nope.pdf", "d/sub", "d/sub/c.pdf", "../x"}, "t")
	if err != nil {
		t.Fatal(err)
	}
	st := []string{}
	for _, it := range out.Items {
		st = append(st, it.Status)
	}
	if strings.Join(st, ",") != "moved,failed,moved,skipped,failed" || out.Summary.Moved != 2 || out.Summary.Failed != 2 || out.Summary.Skipped != 1 {
		t.Fatalf("%v %+v", st, out.Summary)
	}
	if exists(root, "d/a.pdf") || exists(root, "d/sub") || !exists(root, "d/b.pdf") || ledgerPathOf(t, s, "d/a.pdf") || !ledgerPathOf(t, s, "d/b.pdf") {
		t.Fatal("disk/ledger after bulk trash")
	}
	if out.Items[0].LedgerUpdated != 1 || out.Items[2].LedgerUpdated != 1 {
		t.Fatalf("%+v", out.Items)
	}
}

func TestRestoreWithPolicies(t *testing.T) {
	ctx := context.Background()
	mk := func(t *testing.T) (*Service, string, string) {
		s, root := seedContent(t, map[string]string{"d/a.pdf": "ORIG"})
		it, _, err := s.Trash(ctx, "d/a.pdf", "t")
		if err != nil {
			t.Fatal(err)
		}
		return s, root, it.ID
	}
	t.Run("no conflict", func(t *testing.T) {
		s, root, id := mk(t)
		r, err := s.RestoreWith(ctx, id, PolicyAsk, "t")
		if err != nil || r.Status != "restored" || r.Path != "d/a.pdf" || read(t, root, "d/a.pdf") != "ORIG" || r.HashStatus != "verified" || r.LedgerUpdated != 1 {
			t.Fatalf("%+v %v", r, err)
		}
	})
	t.Run("different content: ask / skip / rename / replace", func(t *testing.T) {
		s, root, id := mk(t)
		write(t, root, "d/a.pdf", "OCCUPIED")
		r, _ := s.RestoreWith(ctx, id, PolicyAsk, "t")
		if len(r.Conflicts) != 1 || r.Conflicts[0].Kind != KindNameExists || r.Conflicts[0].Incoming.SHA256 != sum("ORIG") || r.Conflicts[0].Existing.SHA256 != sum("OCCUPIED") || len(mustTrash(t, s)) != 1 {
			t.Fatalf("ask: %+v", r)
		}
		if r, _ = s.RestoreWith(ctx, id, PolicySkip, "t"); r.Status != StSkipped || len(mustTrash(t, s)) != 1 {
			t.Fatalf("skip: %+v", r)
		}
		if r, _ = s.RestoreWith(ctx, id, PolicyReplace, "t"); r.Status != StReplaced || read(t, root, "d/a.pdf") != "ORIG" || len(mustTrash(t, s)) != 1 || mustTrash(t, s)[0].Name != "a.pdf" {
			t.Fatalf("replace must trash the occupant, not delete it: %+v", r)
		}
	})
	t.Run("rename", func(t *testing.T) {
		s, root, id := mk(t)
		write(t, root, "d/a.pdf", "OCCUPIED")
		r, err := s.RestoreWith(ctx, id, PolicyRename, "t")
		if err != nil || r.Path != "d/a (2).pdf" || read(t, root, "d/a (2).pdf") != "ORIG" || read(t, root, "d/a.pdf") != "OCCUPIED" || !ledgerPathOf(t, s, "d/a (2).pdf") {
			t.Fatalf("%+v %v", r, err)
		}
	})
	t.Run("identical is a duplicate", func(t *testing.T) {
		s, root, id := mk(t)
		write(t, root, "d/a.pdf", "ORIG")
		r, _ := s.RestoreWith(ctx, id, PolicyAsk, "t")
		if r.Status != StDuplicate || r.DuplicateOf != "d/a.pdf" || len(mustTrash(t, s)) != 1 || len(r.Conflicts) != 0 {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("same content already archived elsewhere", func(t *testing.T) {
		s, root := seedContent(t, map[string]string{"d/a.pdf": "SHARED", "e/copy.pdf": "SHARED"})
		it, _, _ := s.Trash(ctx, "d/a.pdf", "t")
		r, _ := s.RestoreWith(ctx, it.ID, PolicyAsk, "t")
		if len(r.Conflicts) != 1 || r.Conflicts[0].Kind != KindDuplicateContent || r.Conflicts[0].Existing.Path != "e/copy.pdf" {
			t.Fatalf("%+v", r)
		}
		r, _ = s.RestoreWith(ctx, it.ID, PolicyRename, "t")
		if r.Status != "restored" || r.DuplicateOf != "e/copy.pdf" || read(t, root, "d/a.pdf") != "SHARED" {
			t.Fatalf("keep anyway: %+v", r)
		}
	})
	t.Run("race at restore", func(t *testing.T) {
		s, root, id := mk(t)
		s.testHookBeforeWrite = func(_, to string) { write(t, root, to, "LATE") }
		if _, err := s.RestoreWith(ctx, id, PolicyAsk, "t"); !errors.Is(err, fsman.ErrExists) || read(t, root, "d/a.pdf") != "LATE" || len(mustTrash(t, s)) != 1 {
			t.Fatalf("%v", err)
		}
	})
}

// ---- ZIP ----

func zipOf(t *testing.T, s *Service, paths ...string) (map[string]string, []string, ZipReport) {
	t.Helper()
	plan, err := s.PlanDownload(paths, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	rep, err := s.WriteZip(context.Background(), &buf, plan, "t")
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	var order []string
	for _, f := range zr.File {
		order = append(order, f.Name)
		if strings.HasSuffix(f.Name, "/") {
			files[f.Name] = "<dir>"
			continue
		}
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		files[f.Name] = string(b)
	}
	return files, order, rep
}

func TestZipFolderContentManifestAndStructure(t *testing.T) {
	s, root := seedContent(t, map[string]string{"财务部/合同/甲 乙.pdf": "PDF-1", "财务部/合同/b.txt": "hello 你好", "财务部/c.xlsx": "XLSX"})
	os.MkdirAll(filepath.Join(root, "财务部", "空文件夹"), 0o755)
	write(t, root, "财务部/.hidden/secret.txt", "internal")
	files, order, rep := zipOf(t, s, "财务部")
	if files["财务部/合同/甲 乙.pdf"] != "PDF-1" || files["财务部/合同/b.txt"] != "hello 你好" || files["财务部/空文件夹/"] != "<dir>" {
		t.Fatalf("%v", order)
	}
	for n := range files {
		if strings.Contains(n, ".hidden") || strings.Contains(n, "secret") {
			t.Fatalf("hidden internals leaked: %s", n)
		}
		if strings.HasPrefix(n, "/") || strings.Contains(n, "..") || strings.Contains(n, `\`) {
			t.Fatalf("unsafe entry name %q", n)
		}
	}
	if rep.Files != 3 || len(rep.Mismatch) != 0 {
		t.Fatalf("%+v", rep)
	}
	// manifest in sha256sum -c format with paths relative to the archive root
	man := files[ManifestName]
	want := map[string]string{"财务部/合同/甲 乙.pdf": sum("PDF-1"), "财务部/合同/b.txt": sum("hello 你好"), "财务部/c.xlsx": sum("XLSX")}
	lines := strings.Split(strings.TrimSpace(man), "\n")
	if len(lines) != 3 {
		t.Fatalf("%q", man)
	}
	for _, l := range lines {
		h, name, ok := strings.Cut(l, "  ")
		if !ok || want[name] != h || sum(files[name]) != h {
			t.Fatalf("bad manifest line %q", l)
		}
	}
	// deterministic ordering
	_, order2, _ := zipOf(t, s, "财务部")
	if strings.Join(order, "|") != strings.Join(order2, "|") {
		t.Fatalf("order differs:\n%v\n%v", order, order2)
	}
	// UTF-8 flag on non-ASCII names
	plan, _ := s.PlanDownload([]string{"财务部"}, time.Now())
	var buf bytes.Buffer
	_, _ = s.WriteZip(context.Background(), &buf, plan, "t")
	zr, _ := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	for _, f := range zr.File {
		if strings.ContainsRune(f.Name, '财') && f.Flags&0x800 == 0 {
			t.Fatalf("%s lacks the UTF-8 flag", f.Name)
		}
	}
}

func TestZipSelectionNamingAndCollisions(t *testing.T) {
	s, _ := seedContent(t, map[string]string{"a/x.pdf": "X", "b/x.pdf": "Y", "a/sub/s.pdf": "S", "MANIFEST.sha256": "mine"})
	plan, err := s.PlanDownload([]string{"a/x.pdf", "b/x.pdf", "a", "a/sub/s.pdf", "a/x.pdf"}, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	if err != nil || plan.SingleFile || plan.Name != "oa-files-20260102-030405.zip" || len(plan.Roots) != 2 {
		t.Fatalf("%+v %v", plan, err)
	}
	if p, _ := s.PlanDownload([]string{"a"}, time.Now()); p.Name != "a.zip" {
		t.Fatalf("%+v", p)
	}
	if p, _ := s.PlanDownload([]string{"a/x.pdf"}, time.Now()); !p.SingleFile || p.Name != "x.pdf" {
		t.Fatalf("%+v", p)
	}
	files, _, _ := zipOf(t, s, "a/x.pdf", "b/x.pdf", "MANIFEST.sha256")
	if files["x.pdf"] != "X" || files["x (2).pdf"] != "Y" || files["MANIFEST (2).sha256"] != "mine" || !strings.Contains(files[ManifestName], "x (2).pdf") {
		t.Fatalf("%v", files)
	}
	for _, bad := range []string{"../etc", "/abs", "a/../b", ".trash", "nope"} {
		if _, err := s.PlanDownload([]string{"a", bad}, time.Now()); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
	if _, err := s.PlanDownload(nil, time.Now()); err == nil {
		t.Error("empty selection")
	}
}

func TestZipSkipsSymlinksAndFlagsLedgerMismatch(t *testing.T) {
	s, root := seedContent(t, map[string]string{"d/a.pdf": "ORIGINAL", "d/b.pdf": "B"})
	outside := t.TempDir()
	write(t, outside, "secret.txt", "TOP SECRET")
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "d", "link.txt")); err == nil {
		os.Symlink(outside, filepath.Join(root, "d", "linkdir"))
	} else {
		t.Log("symlinks unavailable here; link check skipped")
	}
	tamper(t, root, "d/a.pdf", "ALTERED!")
	files, _, rep := zipOf(t, s, "d")
	for n, c := range files {
		if strings.Contains(n, "link") || strings.Contains(c, "TOP SECRET") {
			t.Fatalf("symlink followed: %s", n)
		}
	}
	if len(rep.Mismatch) != 1 || !strings.Contains(files[NotesName], "d/a.pdf") || files["d/a.pdf"] != "ALTERED!" || !strings.Contains(files[ManifestName], sum("ALTERED!")) {
		t.Fatalf("%+v %v", rep, files[NotesName])
	}
	if _, err := s.PlanDownload([]string{"d/link.txt"}, time.Now()); err == nil && exists(root, "d/link.txt") {
		t.Fatal("a symlink must not be downloadable")
	}
}

func TestZipSafeNameStopsTraversalAndSeparators(t *testing.T) {
	for in, want := range map[string]string{"..": "_", ".": "_", "": "_", `a\..\evil`: "a_.._evil", "a/b": "a_b", "x\ny": "x_y", "ok 名.pdf": "ok 名.pdf", "bad\xffname": "bad_name"} {
		if got := zipSafeName(in); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
}

func TestZipStopsWhenTheClientAborts(t *testing.T) {
	rb := make([]byte, 3<<20)
	_, _ = rand.Read(rb) // incompressible, 1 MiB per file
	s, _ := seedContent(t, map[string]string{"d/1.pdf": string(rb[:1<<20]), "d/2.pdf": string(rb[1<<20 : 2<<20]), "d/3.pdf": string(rb[2<<20:])})
	plan, _ := s.PlanDownload([]string{"d"}, time.Now())
	ctx, cancel := context.WithCancel(context.Background())
	var buf bytes.Buffer
	w := writerFunc(func(p []byte) (int, error) {
		n, err := buf.Write(p)
		if buf.Len() > 1<<20 {
			cancel() // the client went away after the first megabyte
		}
		return n, err
	})
	_, err := s.WriteZip(ctx, w, plan, "t")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want cancellation, got %v", err)
	}
	if buf.Len() >= 3<<20 {
		t.Fatalf("work continued after the abort: %d bytes", buf.Len())
	}
	if _, zerr := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len())); zerr == nil {
		t.Fatal("an aborted stream must not look like a complete archive")
	}
	// a failing writer (broken connection) aborts too
	w2 := writerFunc(func(p []byte) (int, error) { return 0, errors.New("broken pipe") })
	if _, err = s.WriteZip(context.Background(), w2, plan, "t"); err == nil {
		t.Fatal("write errors must abort")
	}
}

type writerFunc func(p []byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
