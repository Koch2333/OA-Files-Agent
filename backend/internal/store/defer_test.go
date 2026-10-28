package store

import (
	"context"
	"errors"
	"testing"
)

func TestDeferPlanItemsSplitsThePlanAndFeedsTheQueue(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, p := seedPlan(t, s, false)
	if len(p.Items) != 2 {
		t.Fatalf("seed: %d items", len(p.Items))
	}
	a, b := p.Items[0].FileID, p.Items[1].FileID

	for name, tc := range map[string]struct {
		version int
		ids     []string
		want    error
	}{
		"stale version":   {p.Version + 3, []string{b}, ErrConflict},
		"empty selection": {p.Version, nil, ErrBadSelection},
		"unknown file":    {p.Version, []string{"00000000-0000-4000-8000-000000000000"}, ErrBadSelection},
		"every item":      {p.Version, []string{a, b}, ErrBadSelection},
	} {
		if _, _, err := s.DeferPlanItems(ctx, p.ID, tc.version, tc.ids); !errors.Is(err, tc.want) {
			t.Fatalf("%s: %v, want %v", name, err, tc.want)
		}
	}
	if cur, _ := s.GetPlan(ctx, p.ID); len(cur.Items) != 2 || cur.Version != p.Version {
		t.Fatal("a rejected request must change nothing")
	}

	rest, deferredID, err := s.DeferPlanItems(ctx, p.ID, p.Version, []string{b})
	if err != nil {
		t.Fatal(err)
	}
	if len(rest.Items) != 1 || rest.Items[0].FileID != a || rest.Version != p.Version+1 || rest.Status != "awaiting_review" {
		t.Fatalf("remaining plan: %+v", rest)
	}
	later, err := s.GetPlan(ctx, deferredID)
	if err != nil || later.Status != "awaiting_review" || len(later.Items) != 1 || later.Items[0].FileID != b || later.TaskID != p.TaskID || !later.RequiresApproval {
		t.Fatalf("deferred plan: %+v %v", later, err)
	}

	q, err := s.ReviewQueue(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var sawRest, sawLater bool
	for _, e := range q {
		switch e.PlanID {
		case p.ID:
			sawRest = true
			if e.Deferred || e.Items != 1 {
				t.Fatalf("queue entry of the original plan: %+v", e)
			}
		case deferredID:
			sawLater = true
			if !e.Deferred || e.Items != 1 || e.FirstName != later.Items[0].OriginalName {
				t.Fatalf("queue entry of the deferred plan: %+v", e)
			}
		}
	}
	if !sawRest || !sawLater {
		t.Fatalf("both plans wait for review: %+v", q)
	}

	// the rest is approved on its own; the deferred plan stays open
	if _, err := s.ApprovePlan(ctx, p.ID, rest.Version, "u"); err != nil {
		t.Fatal(err)
	}
	if still, _ := s.GetPlan(ctx, deferredID); still.Status != "awaiting_review" {
		t.Fatalf("the deferred plan must stay under review: %s", still.Status)
	}
	if q, _ := s.ReviewQueue(ctx); func() bool {
		for _, e := range q {
			if e.PlanID == p.ID {
				return true
			}
		}
		return false
	}() {
		t.Fatal("an approved plan leaves the queue")
	}
}

func TestReturnItemsForReanalysisMovesOnlyTheChosenFilesToANewTask(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	b, p := seedPlan(t, s, false)
	a, c := p.Items[0].FileID, p.Items[1].FileID

	for name, tc := range map[string]struct {
		version int
		ids     []string
		want    error
	}{
		"stale version": {p.Version + 2, []string{c}, ErrConflict},
		"nothing":       {p.Version, nil, ErrBadSelection},
		"not in plan":   {p.Version, []string{"00000000-0000-4000-8000-000000000000"}, ErrBadSelection},
		"everything":    {p.Version, []string{a, c}, ErrBadSelection},
	} {
		if _, _, err := s.ReturnItemsForReanalysis(ctx, p.ID, tc.version, tc.ids, ""); !errors.Is(err, tc.want) {
			t.Fatalf("%s: %v, want %v", name, err, tc.want)
		}
	}
	rest, taskID, err := s.ReturnItemsForReanalysis(ctx, p.ID, p.Version, []string{c}, "按项目名归档")
	if err != nil {
		t.Fatal(err)
	}
	if len(rest.Items) != 1 || rest.Items[0].FileID != a || rest.Version != p.Version+1 {
		t.Fatalf("the rest of the plan stays: %+v", rest)
	}
	nj, err := s.GetJob(ctx, taskID)
	if err != nil || nj.Status != "queued" || nj.TotalFiles != 1 || nj.Automation != "review" || nj.Instruction != "按项目名归档" {
		t.Fatalf("new task: %+v %v", nj, err)
	}
	files, _, err := s.ListFiles(ctx, Page{Page: 1, Size: 50}, FileFilter{TaskID: taskID})
	if err != nil || len(files) != 1 || files[0].ID != c || files[0].ArchiveStatus != "pending" || !files[0].ReviewRequired || files[0].Department != nil {
		t.Fatalf("the returned file is reset for a fresh analysis: %+v %v", files, err)
	}
	oj, _ := s.GetJob(ctx, b.Job.ID)
	if oj.TotalFiles != 1 {
		t.Fatalf("the original task keeps only its remaining file: %+v", oj)
	}
	claimed := map[string]bool{}
	for i := 0; i < 2; i++ { // the seed task is queued too: both are picked up, the new one included
		if got, err := s.ClaimJob(ctx); err == nil {
			claimed[got.ID] = true
		}
	}
	if !claimed[taskID] {
		t.Fatalf("the pipeline must pick the new task up: %v", claimed)
	}
}
