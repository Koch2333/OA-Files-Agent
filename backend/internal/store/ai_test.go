package store

import (
	"context"
	"errors"
	"testing"

	"oa-agent/backend/internal/imports"
)

func TestAIPolicyDefaultsAndPersonalConfigCRUD(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, err := s.LoadAIPolicy(ctx)
	if err != nil || p.AllowPersonalLLM || p.AllowPersonalPrivateHosts || !p.AllowAutoArchive || p.UpdatedAt != nil {
		t.Fatalf("defaults: %+v %v", p, err)
	}
	p.AllowPersonalLLM, p.AllowAutoArchive = true, false
	saved, err := s.SaveAIPolicy(ctx, p)
	if err != nil || !saved.AllowPersonalLLM || saved.AllowAutoArchive || saved.UpdatedAt == nil {
		t.Fatalf("save: %+v %v", saved, err)
	}
	if got, _ := s.LoadAIPolicy(ctx); !got.AllowPersonalLLM || got.AllowAutoArchive {
		t.Fatalf("reload: %+v", got)
	}

	if _, ok, err := s.LoadPersonalLLM(ctx, "u1"); ok || err != nil {
		t.Fatalf("empty: %v %v", ok, err)
	}
	cfg := LLMSettings{Mode: "llm", PresetID: "custom", BaseURL: "https://x.example/v1", Model: "m1", APIKeyEnc: []byte{1, 2, 3}, VisionModel: "vm", AllowVision: true}
	if _, err := s.SavePersonalLLM(ctx, "u1", cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Model = "m2"
	if _, err := s.SavePersonalLLM(ctx, "u1", cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SavePersonalLLM(ctx, "local", LLMSettings{Mode: "heuristic"}); err != nil { // local mode user id is not a UUID
		t.Fatal(err)
	}
	got, ok, err := s.LoadPersonalLLM(ctx, "u1")
	if err != nil || !ok || got.Model != "m2" || string(got.APIKeyEnc) != "\x01\x02\x03" || !got.AllowVision || got.UpdatedAt.IsZero() {
		t.Fatalf("load: %+v %v %v", got, ok, err)
	}
	if other, ok, _ := s.LoadPersonalLLM(ctx, "u2"); ok || other.Model != "" {
		t.Fatal("configs are per user")
	}
	if err := s.DeletePersonalLLM(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.LoadPersonalLLM(ctx, "u1"); ok {
		t.Fatal("deleted")
	}
}

func TestTaskAIColumnsRoundTripAndDefaults(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	plain, _, err := s.Save(ctx, newBatch(t, "", "", "a.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	if j := plain.Job; j.Automation != "review" || j.LLMSource != "none" || j.Instruction != "" || j.LLMModel != "" || j.AckAt != nil {
		t.Fatalf("defaults: %+v", j)
	}
	b := newBatch(t, "ai-key", "fp", "a.pdf")
	b.Job.Instruction, b.Job.Automation, b.Job.LLMSource, b.Job.LLMOwner, b.Job.LLMModel = "只处理采购", "auto", "personal", "user-1", "qwen-plus"
	b.Job.AckVersion, b.Job.AckBy = "v-test", "alice"
	now := b.Job.CreatedAt
	b.Job.AckAt = &now
	saved, _, err := s.Save(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	j, err := s.GetJob(ctx, saved.Job.ID)
	if err != nil || j.Instruction != "只处理采购" || j.Automation != "auto" || j.LLMSource != "personal" || j.LLMOwner != "user-1" || j.LLMModel != "qwen-plus" || j.AckVersion != "v-test" || j.AckBy != "alice" || j.AckAt == nil {
		t.Fatalf("round trip: %+v %v", j, err)
	}
	if err := s.SetJobModel(ctx, j.ID, "qwen-max"); err != nil {
		t.Fatal(err)
	}
	if j, _ = s.GetJob(ctx, j.ID); j.LLMModel != "qwen-max" {
		t.Fatal("SetJobModel")
	}
	// the claimed job carries its AI columns (the pipeline reads them from there)
	for {
		c, err := s.ClaimJob(ctx)
		if err != nil {
			t.Fatal("expected to claim the personal job")
		}
		if c.ID == j.ID {
			if c.LLMOwner != "user-1" || c.Automation != "auto" || c.Instruction == "" {
				t.Fatalf("claimed job lost its AI columns: %+v", c)
			}
			break
		}
	}
}

func seedAutoItems(t *testing.T, s *Store) (imports.Batch, []PlanItem, []PlanItem) {
	t.Helper()
	nb := newBatch(t, "", "", "a.pdf", "b.pdf", "c.pdf")
	nb.Job.Automation, nb.Job.LLMSource, nb.Job.LLMModel, nb.Job.AckBy = "auto", "global", "qwen-plus", "alice"
	b, _, err := s.Save(context.Background(), nb)
	if err != nil {
		t.Fatal(err)
	}
	var ready, left []PlanItem
	for i, f := range b.Files {
		it := PlanItem{FileID: f.ID, OriginalName: f.SourceName, SuggestedName: f.SourceName, SourcePath: f.SourcePath, DestinationPath: "财务部/2026/" + f.SourceName, Department: str("财务部"), DocumentType: str("合同"), DocumentDate: str("2026-03-15"), Warnings: []string{}}
		if i == 2 {
			it.ReviewRequired, it.Department = true, nil
			left = append(left, it)
			continue
		}
		ready = append(ready, it)
	}
	return b, ready, left
}

func TestAutoPlansSplitExecuteMarkDecisionAndKeepLeftoverPending(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	b, ready, left := seedAutoItems(t, s)
	auto, leftover, err := s.CreateAutoPlans(ctx, b.Job.ID, nil, ready, left, "ai-auto:alice")
	if err != nil {
		t.Fatal(err)
	}
	if auto.Status != "approved" || auto.RequiresApproval || auto.ApprovedBy == nil || *auto.ApprovedBy != "ai-auto:alice" || len(auto.Items) != 2 {
		t.Fatalf("auto plan: %+v", auto)
	}
	if leftover == nil || leftover.Status != "awaiting_review" || len(leftover.Items) != 1 || !leftover.Items[0].ReviewRequired || !leftover.RequiresApproval {
		t.Fatalf("leftover plan: %+v", leftover)
	}
	job, _ := s.GetJob(ctx, b.Job.ID)
	if job.PlanID == nil || *job.PlanID != leftover.ID || job.Status != "approved" {
		t.Fatalf("task must point at the plan awaiting a human: %+v", job)
	}
	// the leftover cannot be approved while the automatic plan is still being executed
	if _, err := s.ApprovePlan(ctx, leftover.ID, leftover.Version, "human"); !errors.Is(err, ErrState) && !errors.Is(err, ErrReviewRequired) {
		t.Fatalf("leftover approval during auto run: %v", err)
	}
	// crash recovery sees the never-executed automatic plan
	refs, err := s.RecoverAutoPlans(ctx)
	if err != nil || len(refs) != 1 || refs[0].PlanID != auto.ID || refs[0].TaskID != b.Job.ID {
		t.Fatalf("recover: %+v %v", refs, err)
	}

	op, plan, replay, err := s.BeginExecutionAs(ctx, auto.ID, "auto:"+b.Job.ID, DecisionAIAuto, "qwen-plus")
	if err != nil || replay || op.Decision != DecisionAIAuto || op.DecisionModel != "qwen-plus" {
		t.Fatalf("begin: %+v %v", op, err)
	}
	var results []OpResult
	for _, it := range plan.Items {
		results = append(results, OpResult{FileID: it.FileID, FinalRel: it.DestinationPath, Status: "published", SHA256: "ab"})
	}
	done, err := s.FinishExecution(ctx, op, plan, results)
	if err != nil || done.Status != "executed" {
		t.Fatalf("finish: %+v %v", done, err)
	}
	job, _ = s.GetJob(ctx, b.Job.ID)
	if job.Status != "awaiting_review" || job.PlanID == nil || *job.PlanID != leftover.ID {
		t.Fatalf("with leftovers the task keeps waiting for a human: %+v", job)
	}
	lp, _ := s.GetPlan(ctx, leftover.ID)
	if lp.OperationID == nil || *lp.OperationID != op.ID {
		t.Fatalf("the leftover plan must expose the automatic operation id: %+v", lp.OperationID)
	}
	if _, n, _ := s.Ledger(ctx, Page{}, LedgerFilter{Decision: DecisionManual}); n != 0 {
		t.Fatalf("manual filter: %d", n)
	}
	rows, total, _ := s.Ledger(ctx, Page{}, LedgerFilter{Decision: DecisionAIAuto})
	if total != 2 {
		t.Fatalf("ledger rows = %d", total)
	}
	for _, r := range rows {
		if r.Decision != DecisionAIAuto || r.DecisionModel != "qwen-plus" {
			t.Fatalf("ledger decision: %+v", r)
		}
	}
	if got, _ := s.Operation(ctx, op.ID); got.Decision != DecisionAIAuto || got.DecisionModel != "qwen-plus" {
		t.Fatalf("operation row: %+v", got)
	}
	if refs, _ = s.RecoverAutoPlans(ctx); len(refs) != 0 {
		t.Fatalf("an executed plan must not be recovered: %+v", refs)
	}
	// rollback keeps working; a rolled-back automatic plan is never re-run by recovery
	if err := s.MarkRolledBack(ctx, op); err != nil {
		t.Fatal(err)
	}
	if refs, _ = s.RecoverAutoPlans(ctx); len(refs) != 0 {
		t.Fatalf("a rolled-back automatic plan must not be re-executed automatically: %+v", refs)
	}
	job, _ = s.GetJob(ctx, b.Job.ID)
	if job.Status != "awaiting_review" {
		t.Fatalf("after rollback the leftover is still waiting: %s", job.Status)
	}
	// the human can now handle the leftover: fix the item, approve, execute: decision manual
	fixed, err := s.PatchPlan(ctx, leftover.ID, leftover.Version, []PlanPatch{{FileID: leftover.Items[0].FileID, Department: str("财务部")}},
		func(it PlanItem) (string, string, []string, error) {
			return it.OriginalName, "财务部/2026/" + it.OriginalName, nil, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	ap, err := s.ApprovePlan(ctx, fixed.ID, fixed.Version, "human")
	if err != nil {
		t.Fatalf("approve leftover: %v", err)
	}
	op2, plan2, _, err := s.BeginExecution(ctx, ap.ID, "k-human")
	if err != nil || op2.Decision != DecisionManual {
		t.Fatalf("manual begin: %+v %v", op2, err)
	}
	if _, err := s.FinishExecution(ctx, op2, plan2, []OpResult{{FileID: plan2.Items[0].FileID, FinalRel: plan2.Items[0].DestinationPath, Status: "published", SHA256: "cd"}}); err != nil {
		t.Fatal(err)
	}
	rows, _, _ = s.Ledger(ctx, Page{}, LedgerFilter{})
	if len(rows) != 1 || rows[0].Decision != DecisionManual || rows[0].DecisionModel != "" {
		t.Fatalf("manual rows: %+v", rows)
	}
	if job, _ = s.GetJob(ctx, b.Job.ID); job.Status != "completed" {
		t.Fatalf("done: %s", job.Status)
	}
}

func TestAutoPlanWithoutLeftoversCompletesTheTask(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	b, ready, _ := seedAutoItems(t, s)
	auto, leftover, err := s.CreateAutoPlans(ctx, b.Job.ID, nil, ready, nil, "ai-auto:alice")
	if err != nil || leftover != nil {
		t.Fatalf("%v %v", err, leftover)
	}
	op, plan, _, err := s.BeginExecutionAs(ctx, auto.ID, "auto:x", DecisionAIAuto, "m")
	if err != nil {
		t.Fatal(err)
	}
	var results []OpResult
	for _, it := range plan.Items {
		results = append(results, OpResult{FileID: it.FileID, FinalRel: it.DestinationPath, Status: "published", SHA256: "ab"})
	}
	if _, err := s.FinishExecution(ctx, op, plan, results); err != nil {
		t.Fatal(err)
	}
	if job, _ := s.GetJob(ctx, b.Job.ID); job.Status != "completed" || job.PlanID == nil || *job.PlanID != auto.ID {
		t.Fatalf("nothing pending: the task completes: %+v", job)
	}
}
