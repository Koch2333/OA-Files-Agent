package store

import (
	"context"
	"errors"
	"testing"

	"oa-agent/backend/internal/archive"
)

func TestApplyFallbackPlaceholderIsDeterministicAndFlagged(t *testing.T) {
	it := PlanItem{Department: nil, DocumentType: str("合同"), DocumentDate: str("2026-01-02")}
	if !ApplyFallbackPlaceholder(&it) {
		t.Fatal("a nil department must be replaced")
	}
	if it.Department == nil || *it.Department != archive.CategoryOther || len(it.FallbackFields) != 1 || it.FallbackFields[0] != FallbackDepartment || !it.ReviewRequired {
		t.Fatalf("placeholder wrong: %+v", it)
	}
	if len(it.Warnings) != 1 {
		t.Fatalf("one explanation expected: %v", it.Warnings)
	}
	if ApplyFallbackPlaceholder(&it) || len(it.Warnings) != 1 || len(it.FallbackFields) != 1 {
		t.Fatalf("must be idempotent: %+v", it)
	}
	if it.DocumentType == nil || *it.DocumentType != "合同" {
		t.Fatal("determined fields must stay untouched")
	}
	both := PlanItem{}
	ApplyFallbackPlaceholder(&both)
	if len(both.FallbackFields) != 2 || *both.DocumentType != archive.CategoryOther {
		t.Fatalf("both fields: %+v", both)
	}
}

func TestFallbackBlocksApprovalUntilConfirmed(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, p := seedPlan(t, s, false)
	derive := func(it PlanItem) (string, string, []string, error) {
		return "x_" + it.OriginalName, *it.Department + "/" + it.OriginalName, nil, nil
	}
	// clearing a department re-applies the flagged placeholder
	p, err := s.PatchPlan(ctx, p.ID, p.Version, []PlanPatch{{FileID: p.Items[0].FileID, Department: str("")}}, derive)
	if err != nil {
		t.Fatal(err)
	}
	it := p.Items[0]
	if *it.Department != "其他" || len(it.FallbackFields) != 1 || !it.ReviewRequired || it.DestinationPath != "其他/a.pdf" {
		t.Fatalf("cleared department must become flagged 其他: %+v", it)
	}
	if _, err := s.ApprovePlan(ctx, p.ID, p.Version, "u"); !errors.Is(err, ErrReviewRequired) {
		t.Fatalf("approval must stay blocked: %v", err)
	}
	// reloading keeps the flag
	cur, _ := s.GetPlan(ctx, p.ID)
	if len(cur.Items[0].FallbackFields) != 1 {
		t.Fatalf("flag must persist: %+v", cur.Items[0])
	}
	// explicit confirmation with the very same value
	p, err = s.PatchPlan(ctx, p.ID, p.Version, []PlanPatch{{FileID: p.Items[0].FileID, Department: str("其他")}}, derive)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Items[0].FallbackFields) != 0 || p.Items[0].ReviewRequired || len(p.Items[0].Warnings) != 0 {
		t.Fatalf("confirmed 其他 must clear flag and warning: %+v", p.Items[0])
	}
	if _, err := s.ApprovePlan(ctx, p.ID, p.Version, "u"); err != nil {
		t.Fatalf("approve after confirmation: %v", err)
	}
}

func TestLedgerOtherFilterAndDisplay(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	b, p := seedPlan(t, s, false)
	p, _ = s.ApprovePlan(ctx, p.ID, p.Version, "u")
	op, plan, _, _ := s.BeginExecution(ctx, p.ID, "k")
	var res []OpResult
	for _, it := range plan.Items {
		res = append(res, OpResult{FileID: it.FileID, FinalRel: it.DestinationPath, Status: "published", SHA256: "ab"})
	}
	if _, err := s.FinishExecution(ctx, op, plan, res); err != nil {
		t.Fatal(err)
	}
	// older rows: blank department, placeholder type, explicit 其他 department on the other row
	if _, err := s.Pool.Exec(ctx, `UPDATE ledger_entries SET department=$2, document_type=$3 WHERE file_id=$1`, b.Files[0].ID, "", "待审核"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE ledger_entries SET department=$2 WHERE file_id=$1`, b.Files[1].ID, "其他"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		f    LedgerFilter
		want int
	}{
		{LedgerFilter{Department: "其他"}, 2},
		{LedgerFilter{Type: "其他"}, 1},
		{LedgerFilter{Department: "其他", Type: "合同"}, 1},
		{LedgerFilter{Department: "财务部"}, 0},
		{LedgerFilter{Type: "合同"}, 1},
	} {
		rows, total, err := s.Ledger(ctx, Page{}, c.f)
		if err != nil || total != c.want || len(rows) != c.want {
			t.Errorf("%+v: total=%d err=%v want %d", c.f, total, err, c.want)
		}
	}
	rows, _, _ := s.Ledger(ctx, Page{}, LedgerFilter{Sort: "original_name", Order: "asc"})
	if rows[0].Department != "其他" || rows[0].DocumentType != "其他" || rows[1].DocumentType != "合同" {
		t.Fatalf("blank/placeholder values must display as 其他: %+v", rows)
	}
	all, _ := s.LedgerAll(ctx, LedgerFilter{}, 100)
	for _, e := range all {
		if e.Department == "" || e.DocumentType == "" {
			t.Fatalf("export rows must never be blank: %+v", e)
		}
	}
	dep, _, err := s.ClassificationUsage(ctx, "department", "")
	if err != nil || len(dep) != 1 || dep[0].Value != "其他" {
		// only the explicit row counts as "used" (blank is excluded); 其他 is dropped later by the merge
		t.Fatalf("usage: %v %+v", err, dep)
	}
	typ, plan2, err := s.ClassificationUsage(ctx, "document_type", b.Job.ID)
	if err != nil || len(typ) != 2 || len(plan2) != 1 || plan2[0].Value != "合同" || plan2[0].Count != 2 {
		t.Fatalf("usage types: %v %+v %+v", err, typ, plan2)
	}
}
