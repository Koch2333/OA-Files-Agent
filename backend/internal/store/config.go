package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"oa-agent/backend/internal/sqldb"

	"oa-agent/backend/internal/imports"
)

// ---- OA templates ----

const tplCols = `id::text, name, enabled, priority, created_at, match_rules, field_mappings`

func scanTemplate(r sqldb.Row) (Template, error) {
	var t Template
	var mr, fm []byte
	err := r.Scan(&t.ID, &t.Name, &t.Enabled, &t.Priority, &t.CreatedAt, &mr, &fm)
	if errors.Is(err, sqldb.ErrNoRows) {
		return t, ErrNotFound
	}
	if err != nil {
		return t, err
	}
	_ = json.Unmarshal(mr, &t.MatchRules)
	_ = json.Unmarshal(fm, &t.FieldMappings)
	if t.MatchRules == nil {
		t.MatchRules = map[string]any{}
	}
	if t.FieldMappings == nil {
		t.FieldMappings = map[string]any{}
	}
	return t, nil
}

func (s *Store) Templates(ctx context.Context) ([]Template, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+tplCols+` FROM oa_templates ORDER BY priority, created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Template{}
	for rows.Next() {
		t, err := scanTemplate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) Template(ctx context.Context, id string) (Template, error) {
	if !validUUID(id) {
		return Template{}, ErrNotFound
	}
	return scanTemplate(s.Pool.QueryRow(ctx, `SELECT `+tplCols+` FROM oa_templates WHERE id=$1`, id))
}

func (s *Store) CreateTemplate(ctx context.Context, t Template) (Template, error) {
	id, err := imports.NewID()
	if err != nil {
		return t, err
	}
	mr, _ := json.Marshal(t.MatchRules)
	fm, _ := json.Marshal(t.FieldMappings)
	if _, err = s.Pool.Exec(ctx, `INSERT INTO oa_templates(id,name,enabled,priority,match_rules,field_mappings) VALUES ($1,$2,$3,$4,$5,$6)`, id, t.Name, t.Enabled, t.Priority, sqldb.JSON(mr), sqldb.JSON(fm)); err != nil {
		return t, err
	}
	return scanTemplate(s.Pool.QueryRow(ctx, `SELECT `+tplCols+` FROM oa_templates WHERE id=$1`, id))
}

// UpdateTemplate replaces the template's mutable fields (callers merge a PATCH onto the current value first).
func (s *Store) UpdateTemplate(ctx context.Context, t Template) (Template, error) {
	mr, _ := json.Marshal(t.MatchRules)
	fm, _ := json.Marshal(t.FieldMappings)
	if _, err := s.Pool.Exec(ctx, `UPDATE oa_templates SET name=$2, enabled=$3, priority=$4, match_rules=$5, field_mappings=$6, updated_at=now() WHERE id=$1`, t.ID, t.Name, t.Enabled, t.Priority, sqldb.JSON(mr), sqldb.JSON(fm)); err != nil {
		return t, err
	}
	return scanTemplate(s.Pool.QueryRow(ctx, `SELECT `+tplCols+` FROM oa_templates WHERE id=$1`, t.ID))
}

func (s *Store) DeleteTemplate(ctx context.Context, id string) error {
	if !validUUID(id) {
		return ErrNotFound
	}
	t, err := s.Pool.Exec(ctx, `DELETE FROM oa_templates WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if t.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- archive rules ----

func (s *Store) Rules(ctx context.Context) ([]Rule, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id::text, name, naming_pattern, directory_pattern, enabled FROM archive_rules ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Rule{}
	for rows.Next() {
		var r Rule
		if err := rows.Scan(&r.ID, &r.Name, &r.NamingPattern, &r.DirectoryPattern, &r.Enabled); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) CreateRule(ctx context.Context, r Rule) (Rule, error) {
	id, err := imports.NewID()
	if err != nil {
		return r, err
	}
	r.ID = id
	_, err = s.Pool.Exec(ctx, `INSERT INTO archive_rules(id,name,naming_pattern,directory_pattern,enabled) VALUES ($1,$2,$3,$4,$5)`, id, r.Name, r.NamingPattern, r.DirectoryPattern, r.Enabled)
	return r, err
}

// ActiveRule is the first enabled rule by creation time (newest enabled rule wins).
func (s *Store) ActiveRule(ctx context.Context) (Rule, error) {
	var r Rule
	err := s.Pool.QueryRow(ctx, `SELECT id::text, name, naming_pattern, directory_pattern, enabled FROM archive_rules WHERE enabled=true ORDER BY created_at DESC LIMIT 1`).Scan(&r.ID, &r.Name, &r.NamingPattern, &r.DirectoryPattern, &r.Enabled)
	if errors.Is(err, sqldb.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// ---- corrections ----

func (s *Store) CreateCorrection(ctx context.Context, c Correction) (Correction, error) {
	id, err := imports.NewID()
	if err != nil {
		return c, err
	}
	c.ID, c.Status = id, "proposed"
	ch, _ := json.Marshal(c.Changes)
	im, _ := json.Marshal(c.Impact)
	_, err = s.Pool.Exec(ctx, `INSERT INTO correction_proposals(id,file_id,instruction,changes,impact) VALUES ($1,$2,$3,$4,$5)`, id, c.FileID, c.Instruction, sqldb.JSON(ch), sqldb.JSON(im))
	return c, err
}

func (s *Store) Correction(ctx context.Context, id string) (Correction, error) {
	var c Correction
	var ch, im []byte
	if !validUUID(id) {
		return c, ErrNotFound
	}
	err := s.Pool.QueryRow(ctx, `SELECT id::text, file_id::text, instruction, status, changes, impact FROM correction_proposals WHERE id=$1`, id).Scan(&c.ID, &c.FileID, &c.Instruction, &c.Status, &ch, &im)
	if errors.Is(err, sqldb.ErrNoRows) {
		return c, ErrNotFound
	}
	_ = json.Unmarshal(ch, &c.Changes)
	_ = json.Unmarshal(im, &c.Impact)
	return c, err
}

// ApplyCorrection updates the single file (and its pending plan item) and marks the proposal approved.
func (s *Store) ApplyCorrection(ctx context.Context, c Correction, derive func(PlanItem) (name, dest string, warnings []string, err error)) (Correction, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return c, err
	}
	defer tx.Rollback(context.Background())
	var status string
	if err = tx.QueryRow(ctx, `SELECT status FROM correction_proposals WHERE id=$1 FOR UPDATE`, c.ID).Scan(&status); err != nil {
		return c, err
	}
	if status != "proposed" {
		return c, ErrState
	}
	var arch string
	if err = tx.QueryRow(ctx, `SELECT archive_status FROM file_records WHERE id=$1 FOR UPDATE`, c.FileID).Scan(&arch); err != nil {
		return c, err
	}
	if arch == "archived" {
		return c, fmt.Errorf("%w: file already archived; use rollback first", ErrState)
	}
	for _, ch := range c.Changes {
		col := map[string]string{"department": "detected_department", "document_type": "detected_type", "document_date": "detected_date"}[ch.Field]
		if col == "" {
			continue
		}
		cast := ""
		if col == "detected_date" {
			cast = "::date"
		}
		if _, err = tx.Exec(ctx, fmt.Sprintf(`UPDATE file_records SET %s=$2%s WHERE id=$1`, col, cast), c.FileID, ch.After); err != nil {
			return c, err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE file_records SET review_required=CASE WHEN detected_department IS NULL OR detected_type IS NULL OR detected_date IS NULL THEN true ELSE false END WHERE id=$1`, c.FileID); err != nil {
		return c, err
	}
	// Mirror into an unapproved plan, if any.
	var planID string
	err = tx.QueryRow(ctx, `SELECT p.id::text FROM archive_plans p JOIN archive_plan_items i ON i.plan_id=p.id WHERE i.file_id=$1 AND p.status IN ('awaiting_review','approved') LIMIT 1`, c.FileID).Scan(&planID)
	if err == nil {
		p, perr := getPlan(ctx, tx, planID, true)
		if perr != nil {
			return c, perr
		}
		for i := range p.Items {
			it := &p.Items[i]
			if it.FileID != c.FileID {
				continue
			}
			for _, ch := range c.Changes {
				switch ch.Field {
				case "department":
					it.Department = ch.After
					if ch.After != nil {
						it.FallbackFields = dropField(it.FallbackFields, FallbackDepartment)
					}
				case "document_type":
					it.DocumentType = ch.After
					if ch.After != nil {
						it.FallbackFields = dropField(it.FallbackFields, FallbackType)
					}
				case "document_date":
					it.DocumentDate = ch.After
				}
			}
			ApplyFallbackPlaceholder(it)
			name, dest, warns, derr := derive(*it)
			if derr != nil {
				return c, derr
			}
			it.Warnings = stripFallbackWarnings(warns)
			it.ReviewRequired = it.Department == nil || it.DocumentType == nil || len(it.FallbackFields) > 0
			ApplyFallbackPlaceholder(it)
			w, _ := json.Marshal(nonNil(it.Warnings))
			if _, err = tx.Exec(ctx, `UPDATE archive_plan_items SET department=$3, document_type=$4, document_date=$5::date, suggested_name=$6, destination_path=$7, warnings=$8, review_required=$9, fallback_fields=$10 WHERE plan_id=$1 AND file_id=$2`,
				planID, c.FileID, it.Department, it.DocumentType, it.DocumentDate, name, dest, sqldb.JSON(w), it.ReviewRequired, joinFallback(it.FallbackFields)); err != nil {
				return c, err
			}
		}
		// Any change after approval invalidates it: the plan must be re-approved.
		if _, err = tx.Exec(ctx, `UPDATE archive_plans SET version=version+1, status='awaiting_review', approved_by=NULL, approved_at=NULL, updated_at=now() WHERE id=$1`, planID); err != nil {
			return c, err
		}
		if _, err = tx.Exec(ctx, `UPDATE import_jobs SET status='awaiting_review', updated_at=now() WHERE id=(SELECT task_id FROM archive_plans WHERE id=$1) AND status='approved'`, planID); err != nil {
			return c, err
		}
	} else if !errors.Is(err, sqldb.ErrNoRows) {
		return c, err
	}
	if _, err = tx.Exec(ctx, `UPDATE correction_proposals SET status='approved' WHERE id=$1`, c.ID); err != nil {
		return c, err
	}
	if err = tx.Commit(ctx); err != nil {
		return c, err
	}
	c.Status = "approved"
	return c, nil
}

// ---- LLM settings (single row) ----

type LLMSettings struct {
	Mode        string
	PresetID    string
	BaseURL     string
	Model       string
	APIKeyEnc   []byte
	VisionModel string
	AllowVision bool
	UpdatedAt   time.Time
}

func (s *Store) LoadLLM(ctx context.Context) (LLMSettings, bool, error) {
	var v LLMSettings
	err := s.Pool.QueryRow(ctx, `SELECT mode, preset_id, base_url, model, api_key_enc, vision_model, allow_vision, updated_at FROM llm_settings WHERE id=1`).Scan(&v.Mode, &v.PresetID, &v.BaseURL, &v.Model, &v.APIKeyEnc, &v.VisionModel, &v.AllowVision, &v.UpdatedAt)
	if errors.Is(err, sqldb.ErrNoRows) {
		return v, false, nil
	}
	return v, err == nil, err
}

func (s *Store) SaveLLM(ctx context.Context, v LLMSettings) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO llm_settings(id, mode, preset_id, base_url, model, api_key_enc, vision_model, allow_vision, updated_at) VALUES (1,$1,$2,$3,$4,$5,$6,$7,now())
		ON CONFLICT (id) DO UPDATE SET mode=$1, preset_id=$2, base_url=$3, model=$4, api_key_enc=$5, vision_model=$6, allow_vision=$7, updated_at=now()`, v.Mode, v.PresetID, v.BaseURL, v.Model, v.APIKeyEnc, v.VisionModel, v.AllowVision)
	return err
}
