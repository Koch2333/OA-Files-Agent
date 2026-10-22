package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"oa-agent/backend/internal/imports"
)

// AIPolicy is the administrator policy for AI features (singleton row, migration 008).
type AIPolicy struct {
	AllowPersonalLLM          bool       `json:"allow_personal_llm"`
	AllowPersonalPrivateHosts bool       `json:"allow_personal_private_hosts"`
	AllowAutoArchive          bool       `json:"allow_auto_archive"`
	UpdatedAt                 *time.Time `json:"updated_at"`
}

func (s *Store) LoadAIPolicy(ctx context.Context) (AIPolicy, error) {
	var p AIPolicy
	err := s.Pool.QueryRow(ctx, `SELECT allow_personal_llm, allow_personal_private_hosts, allow_auto_archive, updated_at FROM ai_policy WHERE id=1`).
		Scan(&p.AllowPersonalLLM, &p.AllowPersonalPrivateHosts, &p.AllowAutoArchive, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) { // row missing (manual deletion): documented defaults
		return AIPolicy{AllowAutoArchive: true}, nil
	}
	return p, err
}

func (s *Store) SaveAIPolicy(ctx context.Context, p AIPolicy) (AIPolicy, error) {
	err := s.Pool.QueryRow(ctx, `INSERT INTO ai_policy(id, allow_personal_llm, allow_personal_private_hosts, allow_auto_archive, updated_at) VALUES (1,$1,$2,$3,now())
		ON CONFLICT (id) DO UPDATE SET allow_personal_llm=$1, allow_personal_private_hosts=$2, allow_auto_archive=$3, updated_at=now()
		RETURNING allow_personal_llm, allow_personal_private_hosts, allow_auto_archive, updated_at`, p.AllowPersonalLLM, p.AllowPersonalPrivateHosts, p.AllowAutoArchive).
		Scan(&p.AllowPersonalLLM, &p.AllowPersonalPrivateHosts, &p.AllowAutoArchive, &p.UpdatedAt)
	return p, err
}

// ---- personal LLM configuration (one row per user) ----

func (s *Store) LoadPersonalLLM(ctx context.Context, userID string) (LLMSettings, bool, error) {
	var v LLMSettings
	err := s.Pool.QueryRow(ctx, `SELECT mode, preset_id, base_url, model, api_key_enc, vision_model, allow_vision, updated_at FROM llm_personal WHERE user_id=$1`, userID).
		Scan(&v.Mode, &v.PresetID, &v.BaseURL, &v.Model, &v.APIKeyEnc, &v.VisionModel, &v.AllowVision, &v.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, false, nil
	}
	return v, err == nil, err
}

func (s *Store) SavePersonalLLM(ctx context.Context, userID string, v LLMSettings) (time.Time, error) {
	var at time.Time
	err := s.Pool.QueryRow(ctx, `INSERT INTO llm_personal(user_id, mode, preset_id, base_url, model, api_key_enc, vision_model, allow_vision, updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,now())
		ON CONFLICT (user_id) DO UPDATE SET mode=$2, preset_id=$3, base_url=$4, model=$5, api_key_enc=$6, vision_model=$7, allow_vision=$8, updated_at=now() RETURNING updated_at`,
		userID, v.Mode, v.PresetID, v.BaseURL, v.Model, v.APIKeyEnc, v.VisionModel, v.AllowVision).Scan(&at)
	return at, err
}

func (s *Store) DeletePersonalLLM(ctx context.Context, userID string) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM llm_personal WHERE user_id=$1`, userID)
	return err
}

// ---- automatic archive plans ----

// CreateAutoPlans stores the plans of an automatic task: an already approved plan with the items the model fully
// determined (ready) and, when some items still need a human, a second awaiting_review plan with the rest (left).
// The task points at the awaiting plan when there is one (that is what the user must look at), otherwise at the
// automatic plan. Earlier unapproved plans of the task are replaced. The automatic plan is only written; executing it
// is the caller's job (so that every step stays a separate, auditable operation).
func (s *Store) CreateAutoPlans(ctx context.Context, taskID string, ruleID *string, ready, left []PlanItem, approvedBy string) (auto Plan, leftover *Plan, err error) {
	autoID, err := imports.NewID()
	if err != nil {
		return Plan{}, nil, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Plan{}, nil, err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `DELETE FROM archive_plans WHERE task_id=$1 AND status IN ('draft','awaiting_review')`, taskID); err != nil {
		return Plan{}, nil, err
	}
	insert := func(id, status string, items []PlanItem, by *string) error {
		if _, e := tx.Exec(ctx, `INSERT INTO archive_plans(id,task_id,status,rule_id,requires_approval,approved_by,approved_at) VALUES ($1,$2,$3,$4::uuid,$5,$6,CASE WHEN $6::text IS NULL THEN NULL ELSE now() END)`,
			id, taskID, status, ruleID, by == nil, by); e != nil {
			return e
		}
		for i, it := range items {
			w, _ := json.Marshal(nonNil(it.Warnings))
			if _, e := tx.Exec(ctx, `INSERT INTO archive_plan_items(plan_id,file_id,position,original_name,suggested_name,source_path,destination_path,department,document_type,document_date,warnings,review_required) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::date,$11,$12)`,
				id, it.FileID, i, it.OriginalName, it.SuggestedName, it.SourcePath, it.DestinationPath, it.Department, it.DocumentType, it.DocumentDate, w, it.ReviewRequired); e != nil {
				return e
			}
		}
		return nil
	}
	if err = insert(autoID, "approved", ready, &approvedBy); err != nil {
		return Plan{}, nil, err
	}
	pointAt := autoID
	var leftID string
	if len(left) > 0 {
		if leftID, err = imports.NewID(); err != nil {
			return Plan{}, nil, err
		}
		if err = insert(leftID, "awaiting_review", left, nil); err != nil {
			return Plan{}, nil, err
		}
		pointAt = leftID
	}
	if _, err = tx.Exec(ctx, `UPDATE import_jobs SET plan_id=$2, status='approved', updated_at=now() WHERE id=$1`, taskID, pointAt); err != nil {
		return Plan{}, nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Plan{}, nil, err
	}
	if auto, err = s.GetPlan(ctx, autoID); err != nil {
		return Plan{}, nil, err
	}
	if leftID != "" {
		lp, e := s.GetPlan(ctx, leftID)
		if e != nil {
			return auto, nil, e
		}
		leftover = &lp
	}
	return auto, leftover, nil
}

// AutoPlanRef names an automatic plan that was approved but never executed (crash between planning and execution).
type AutoPlanRef struct{ PlanID, TaskID string }

// RecoverAutoPlans lists approved automatic plans that have no operation at all. A plan whose operation was rolled
// back has one, so a rollback is never silently re-applied.
func (s *Store) RecoverAutoPlans(ctx context.Context) ([]AutoPlanRef, error) {
	rows, err := s.Pool.Query(ctx, `SELECT p.id::text, p.task_id::text FROM archive_plans p JOIN import_jobs j ON j.id=p.task_id
		WHERE p.status='approved' AND j.automation='auto' AND p.approved_by LIKE 'ai-auto:%' AND j.status='approved'
		AND NOT EXISTS (SELECT 1 FROM archive_operations o WHERE o.plan_id=p.id)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AutoPlanRef
	for rows.Next() {
		var r AutoPlanRef
		if err := rows.Scan(&r.PlanID, &r.TaskID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetJobModel records the model that actually runs a task (a personal configuration may have been edited since creation).
func (s *Store) SetJobModel(ctx context.Context, id, model string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE import_jobs SET llm_model=$2 WHERE id=$1`, id, model)
	return err
}
