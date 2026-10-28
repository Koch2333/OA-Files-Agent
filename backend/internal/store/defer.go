package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"oa-agent/backend/internal/imports"
	"oa-agent/backend/internal/sqldb"
)

// ErrBadSelection means a defer request that names no item of the plan, or every item (something must stay to be approved).
var ErrBadSelection = errors.New("invalid item selection")

// DeferPlanItems moves the chosen items of a plan that is still under review into a NEW plan of the same task that stays in
// "awaiting_review" ("review later"). The rest of the plan can then be approved and executed on its own. The new plan is an ordinary plan:
// it is reviewed, edited, approved and executed exactly like any other. Both plans change in one transaction.
func (s *Store) DeferPlanItems(ctx context.Context, planID string, version int, fileIDs []string) (remaining Plan, deferredID string, err error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Plan{}, "", err
	}
	defer tx.Rollback(context.Background())
	p, err := getPlan(ctx, tx, planID, true)
	if err != nil {
		return p, "", err
	}
	if p.Version != version {
		return p, "", ErrConflict
	}
	if p.Status != "awaiting_review" && p.Status != "draft" {
		return p, "", ErrState
	}
	chosen := map[string]bool{}
	for _, id := range fileIDs {
		chosen[id] = true
	}
	var moved, kept []PlanItem
	for _, it := range p.Items {
		if chosen[it.FileID] {
			moved = append(moved, it)
		} else {
			kept = append(kept, it)
		}
	}
	if len(moved) == 0 || len(moved) != len(chosen) || len(kept) == 0 {
		return p, "", ErrBadSelection
	}
	if deferredID, err = imports.NewID(); err != nil {
		return p, "", err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO archive_plans(id,task_id,status,rule_id,requires_approval) VALUES ($1,$2,'awaiting_review',$3::uuid,true)`, deferredID, p.TaskID, p.RuleID); err != nil {
		return p, "", err
	}
	for i, it := range moved {
		w, _ := json.Marshal(nonNil(it.Warnings))
		if _, err = tx.Exec(ctx, `INSERT INTO archive_plan_items(plan_id,file_id,position,original_name,suggested_name,source_path,destination_path,department,document_type,document_date,warnings,review_required,fallback_fields) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::date,$11,$12,$13)`,
			deferredID, it.FileID, i, it.OriginalName, it.SuggestedName, it.SourcePath, it.DestinationPath, it.Department, it.DocumentType, it.DocumentDate, sqldb.JSON(w), it.ReviewRequired, joinFallback(it.FallbackFields)); err != nil {
			return p, "", err
		}
		if _, err = tx.Exec(ctx, `DELETE FROM archive_plan_items WHERE plan_id=$1 AND file_id=$2`, planID, it.FileID); err != nil {
			return p, "", err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE archive_plans SET version=version+1, updated_at=now() WHERE id=$1`, planID); err != nil {
		return p, "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return p, "", err
	}
	remaining, err = s.GetPlan(ctx, planID)
	return remaining, deferredID, err
}

// QueueEntry is one plan waiting for a human decision (the review queue).
type QueueEntry struct {
	PlanID    string    `json:"plan_id"`
	TaskID    string    `json:"task_id"`
	Items     int       `json:"items"`
	NeedsEdit int       `json:"needs_review"`
	FirstName string    `json:"first_name"`
	TaskState string    `json:"task_status"`
	Deferred  bool      `json:"deferred"` // split off with "review later" (the task's current plan is another one)
	UpdatedAt time.Time `json:"updated_at"`
}

// ReviewQueue lists every plan still awaiting review, newest first (bounded).
func (s *Store) ReviewQueue(ctx context.Context) ([]QueueEntry, error) {
	rows, err := s.Pool.Query(ctx, `SELECT p.id::text, p.task_id::text, p.updated_at, j.status, CASE WHEN j.plan_id IS NULL OR j.plan_id <> p.id THEN 1 ELSE 0 END,
		(SELECT count(*) FROM archive_plan_items i WHERE i.plan_id=p.id),
		(SELECT count(*) FROM archive_plan_items i WHERE i.plan_id=p.id AND i.review_required),
		COALESCE((SELECT min(i.original_name) FROM archive_plan_items i WHERE i.plan_id=p.id),'')
		FROM archive_plans p JOIN import_jobs j ON j.id=p.task_id WHERE p.status='awaiting_review' ORDER BY p.updated_at DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []QueueEntry{}
	for rows.Next() {
		var e QueueEntry
		var deferred int
		if err := rows.Scan(&e.PlanID, &e.TaskID, &e.UpdatedAt, &e.TaskState, &deferred, &e.Items, &e.NeedsEdit, &e.FirstName); err != nil {
			return nil, err
		}
		e.Deferred = deferred != 0
		if e.Items > 0 {
			out = append(out, e)
		}
	}
	return out, rows.Err()
}

// ReturnItemsForReanalysis sends the chosen items of a plan under review back to the analysis: they leave the plan (the rest stays
// untouched, edits included, and can be approved at once) and form a NEW task that is queued with the same template, model and
// account as the original (never the automatic mode) and an optional new instruction. Only these files are analysed again, so only
// these files cost model calls. When that task is planned, its plan appears in the review queue like any other.
func (s *Store) ReturnItemsForReanalysis(ctx context.Context, planID string, version int, fileIDs []string, instruction string) (remaining Plan, newTaskID string, err error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Plan{}, "", err
	}
	defer tx.Rollback(context.Background())
	p, err := getPlan(ctx, tx, planID, true)
	if err != nil {
		return p, "", err
	}
	if p.Version != version {
		return p, "", ErrConflict
	}
	if p.Status != "awaiting_review" && p.Status != "draft" {
		return p, "", ErrState
	}
	in := map[string]bool{}
	for _, it := range p.Items {
		in[it.FileID] = true
	}
	chosen := map[string]bool{}
	for _, id := range fileIDs {
		if !in[id] {
			return p, "", ErrBadSelection
		}
		chosen[id] = true
	}
	if len(chosen) == 0 || len(chosen) >= len(p.Items) {
		return p, "", ErrBadSelection
	}
	if newTaskID, err = imports.NewID(); err != nil {
		return p, "", err
	}
	if instruction == "" {
		if err = tx.QueryRow(ctx, `SELECT COALESCE(instruction,'') FROM import_jobs WHERE id=$1`, p.TaskID).Scan(&instruction); err != nil {
			return p, "", err
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO import_jobs(id,status,total_files,processed_files,success_count,failure_count,template_id,created_at,updated_at,instruction,automation,llm_source,llm_owner,llm_model,auto_ack_version,auto_ack_at,auto_ack_by)
		SELECT $2::uuid,'queued',$3,0,0,0,template_id,now(),now(),$4,'review',llm_source,llm_owner,llm_model,auto_ack_version,auto_ack_at,auto_ack_by FROM import_jobs WHERE id=$1::uuid`,
		p.TaskID, newTaskID, len(chosen), instruction); err != nil {
		return p, "", err
	}
	for id := range chosen {
		t, e := tx.Exec(ctx, `UPDATE file_records SET task_id=$2::uuid, processed=false, error=NULL, detected_department=NULL, detected_type=NULL, detected_date=NULL, evidence='[]', review_required=true, archive_status='pending' WHERE id=$1::uuid AND task_id=$3::uuid AND archive_status<>'archived'`,
			id, newTaskID, p.TaskID)
		if e != nil {
			return p, "", e
		}
		if t.RowsAffected() != 1 {
			return p, "", ErrState
		}
		if _, err = tx.Exec(ctx, `DELETE FROM archive_plan_items WHERE plan_id=$1 AND file_id=$2`, planID, id); err != nil {
			return p, "", err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE archive_plans SET version=version+1, updated_at=now() WHERE id=$1`, planID); err != nil {
		return p, "", err
	}
	if _, err = tx.Exec(ctx, `UPDATE import_jobs SET updated_at=now(),
		total_files=(SELECT count(*) FROM file_records WHERE task_id=$1),
		processed_files=(SELECT count(*) FROM file_records WHERE task_id=$1 AND processed=true),
		failure_count=(SELECT count(*) FROM file_records WHERE task_id=$1 AND processed=true AND error IS NOT NULL),
		success_count=(SELECT count(*) FROM file_records WHERE task_id=$1 AND processed=true AND error IS NULL) WHERE id=$1`, p.TaskID); err != nil {
		return p, "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return p, "", err
	}
	remaining, err = s.GetPlan(ctx, planID)
	return remaining, newTaskID, err
}
