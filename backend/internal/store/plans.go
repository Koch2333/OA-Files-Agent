package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"oa-agent/backend/internal/archive"
	"oa-agent/backend/internal/sqldb"

	"oa-agent/backend/internal/imports"
)

// CreatePlan stores a freshly generated plan and links it to the task, replacing any unapproved earlier plan.
func (s *Store) CreatePlan(ctx context.Context, taskID string, ruleID *string, items []PlanItem) (Plan, error) {
	id, err := imports.NewID()
	if err != nil {
		return Plan{}, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Plan{}, err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `DELETE FROM archive_plans WHERE task_id=$1 AND status IN ('draft','awaiting_review')`, taskID); err != nil {
		return Plan{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO archive_plans(id,task_id,status,rule_id) VALUES ($1,$2,'awaiting_review',$3::uuid)`, id, taskID, ruleID); err != nil {
		return Plan{}, err
	}
	for i, it := range items {
		w, _ := json.Marshal(nonNil(it.Warnings))
		if _, err = tx.Exec(ctx, `INSERT INTO archive_plan_items(plan_id,file_id,position,original_name,suggested_name,source_path,destination_path,department,document_type,document_date,warnings,review_required,fallback_fields) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::date,$11,$12,$13)`,
			id, it.FileID, i, it.OriginalName, it.SuggestedName, it.SourcePath, it.DestinationPath, it.Department, it.DocumentType, it.DocumentDate, sqldb.JSON(w), it.ReviewRequired, joinFallback(it.FallbackFields)); err != nil {
			return Plan{}, err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE import_jobs SET plan_id=$2, updated_at=now() WHERE id=$1`, taskID, id); err != nil {
		return Plan{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Plan{}, err
	}
	return s.GetPlan(ctx, id)
}

func (s *Store) GetPlan(ctx context.Context, id string) (Plan, error) {
	return getPlan(ctx, s.Pool, id, false)
}

type rowQuerier interface {
	Query(context.Context, string, ...any) (sqldb.Rows, error)
	QueryRow(context.Context, string, ...any) sqldb.Row
}

func getPlan(ctx context.Context, q rowQuerier, id string, lock bool) (Plan, error) {
	var p Plan
	if !validUUID(id) {
		return p, ErrNotFound
	}
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	err := q.QueryRow(ctx, `SELECT id::text, task_id::text, status, version, requires_approval, approved_by, approved_at, rule_id::text FROM archive_plans WHERE id=$1`+suffix, id).
		Scan(&p.ID, &p.TaskID, &p.Status, &p.Version, &p.RequiresApproval, &p.ApprovedBy, &p.ApprovedAt, &p.RuleID)
	if errors.Is(err, sqldb.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	_ = q.QueryRow(ctx, `SELECT id::text FROM archive_operations WHERE plan_id=$1 AND status='completed' ORDER BY created_at DESC LIMIT 1`, id).Scan(&p.OperationID)
	if p.OperationID == nil && p.Status == "awaiting_review" {
		// the leftover plan of an automatic task points at the automatic operation that archived the rest
		_ = q.QueryRow(ctx, `SELECT o.id::text FROM archive_operations o JOIN archive_plans sp ON sp.id=o.plan_id WHERE sp.task_id=$1 AND o.status='completed' AND o.decision='ai_auto' ORDER BY o.created_at DESC LIMIT 1`, p.TaskID).Scan(&p.OperationID)
	}
	rows, err := q.Query(ctx, `SELECT file_id::text, original_name, suggested_name, source_path, destination_path, department, document_type, to_char(document_date,'YYYY-MM-DD'), warnings, review_required, fallback_fields FROM archive_plan_items WHERE plan_id=$1 ORDER BY position`, id)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	p.Items = []PlanItem{}
	for rows.Next() {
		var it PlanItem
		var w []byte
		var fb string
		if err := rows.Scan(&it.FileID, &it.OriginalName, &it.SuggestedName, &it.SourcePath, &it.DestinationPath, &it.Department, &it.DocumentType, &it.DocumentDate, &w, &it.ReviewRequired, &fb); err != nil {
			return p, err
		}
		it.FallbackFields = splitFallback(fb)
		if json.Unmarshal(w, &it.Warnings) != nil || it.Warnings == nil {
			it.Warnings = []string{}
		}
		p.Items = append(p.Items, it)
	}
	if err := rows.Err(); err != nil {
		return p, err
	}
	rows.Close()
	markBasis(ctx, q, &p)
	return p, nil
}

// markBasis sets Basis/BasisNote on every item from its file's first evidence line (cheap: evidence is a short array).
func markBasis(ctx context.Context, q rowQuerier, p *Plan) {
	for i := range p.Items {
		p.Items[i].Basis = "content"
	}
	rs, err := q.Query(ctx, `SELECT id::text, evidence FROM file_records WHERE task_id=$1`, p.TaskID)
	if err != nil {
		return
	}
	defer rs.Close()
	note := map[string]string{}
	for rs.Next() {
		var id string
		var ev []byte
		var lines []string
		if rs.Scan(&id, &ev) != nil || json.Unmarshal(ev, &lines) != nil || len(lines) == 0 {
			continue
		}
		if strings.HasPrefix(lines[0], FilenameOnlyMarker) {
			note[id] = lines[0]
		}
	}
	for i := range p.Items {
		if n, ok := note[p.Items[i].FileID]; ok {
			p.Items[i].Basis = "filename_only"
			p.Items[i].BasisNote = &n
		}
	}
}

// PatchPlan applies manual edits under an optimistic lock. Derived name/path are computed by the
// caller-supplied function when the patch changes metadata but not name/path explicitly.
func (s *Store) PatchPlan(ctx context.Context, id string, version int, patches []PlanPatch, derive func(PlanItem) (name, dest string, warnings []string, err error)) (Plan, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Plan{}, err
	}
	defer tx.Rollback(context.Background())
	p, err := getPlan(ctx, tx, id, true)
	if err != nil {
		return p, err
	}
	if p.Version != version {
		return p, ErrConflict
	}
	if p.Status != "awaiting_review" && p.Status != "draft" {
		return p, ErrState
	}
	byID := map[string]*PlanItem{}
	for i := range p.Items {
		byID[p.Items[i].FileID] = &p.Items[i]
	}
	for _, pt := range patches {
		it := byID[pt.FileID]
		if it == nil {
			return p, fmt.Errorf("%w: file %s is not in this plan", ErrNotFound, pt.FileID)
		}
		set := func(dst **string, v *string) {
			if v == nil {
				return
			}
			if strings.TrimSpace(*v) == "" {
				*dst = nil
			} else {
				t := strings.TrimSpace(*v)
				*dst = &t
			}
		}
		set(&it.Department, pt.Department)
		set(&it.DocumentType, pt.DocumentType)
		set(&it.DocumentDate, pt.DocumentDate)
		// a field the person touched is confirmed: it is no longer a system placeholder (clearing it re-applies 其他)
		if pt.Department != nil {
			it.FallbackFields = dropField(it.FallbackFields, FallbackDepartment)
		}
		if pt.DocumentType != nil {
			it.FallbackFields = dropField(it.FallbackFields, FallbackType)
		}
		it.Warnings = stripFallbackWarnings(it.Warnings)
		ApplyFallbackPlaceholder(it)
		if pt.SuggestedName == nil || pt.DestinationPath == nil {
			name, dest, warns, derr := derive(*it)
			if derr != nil {
				return p, derr
			}
			if pt.SuggestedName == nil {
				it.SuggestedName = name
			}
			if pt.DestinationPath == nil {
				it.DestinationPath = dest
			}
			it.Warnings = warns
		}
		if pt.SuggestedName != nil {
			it.SuggestedName = *pt.SuggestedName
		}
		if pt.DestinationPath != nil {
			it.DestinationPath = *pt.DestinationPath
		}
		it.ReviewRequired = it.Department == nil || it.DocumentType == nil || it.DocumentDate == nil || len(it.FallbackFields) > 0
		if it.DocumentDate == nil {
			it.Warnings = append(it.Warnings, "字段仍不完整，需人工确认")
		}
		it.Warnings = stripFallbackWarnings(it.Warnings)
		ApplyFallbackPlaceholder(it)
		w, _ := json.Marshal(nonNil(it.Warnings))
		if _, err = tx.Exec(ctx, `UPDATE archive_plan_items SET department=$3, document_type=$4, document_date=$5::date, suggested_name=$6, destination_path=$7, warnings=$8, review_required=$9, fallback_fields=$10 WHERE plan_id=$1 AND file_id=$2`,
			id, it.FileID, it.Department, it.DocumentType, it.DocumentDate, it.SuggestedName, it.DestinationPath, sqldb.JSON(w), it.ReviewRequired, joinFallback(it.FallbackFields)); err != nil {
			return p, err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE archive_plans SET version=version+1, updated_at=now() WHERE id=$1`, id); err != nil {
		return p, err
	}
	if err = tx.Commit(ctx); err != nil {
		return p, err
	}
	return s.GetPlan(ctx, id)
}

// ApprovePlan: awaiting_review -> approved. Refused while any item still needs human review.
func (s *Store) ApprovePlan(ctx context.Context, id string, version int, actor string) (Plan, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Plan{}, err
	}
	defer tx.Rollback(context.Background())
	p, err := getPlan(ctx, tx, id, true)
	if err != nil {
		return p, err
	}
	if p.Version != version {
		return p, ErrConflict
	}
	if p.Status != "awaiting_review" && p.Status != "draft" {
		return p, ErrState
	}
	// While an automatic plan of the same task is being executed, the leftover plan must not be approved concurrently.
	var jobStatus string
	if err = tx.QueryRow(ctx, `SELECT status FROM import_jobs WHERE id=$1 FOR UPDATE`, p.TaskID).Scan(&jobStatus); err != nil {
		return p, err
	}
	if jobStatus == "approved" || jobStatus == "archiving" {
		return p, ErrState
	}
	for _, it := range p.Items {
		if it.ReviewRequired {
			return p, ErrReviewRequired
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE archive_plans SET status='approved', version=version+1, approved_by=$2, approved_at=now(), updated_at=now() WHERE id=$1`, id, actor); err != nil {
		return p, err
	}
	if _, err = tx.Exec(ctx, `UPDATE import_jobs SET status='approved', updated_at=now() WHERE id=$1 AND status='awaiting_review'`, p.TaskID); err != nil {
		return p, err
	}
	if err = tx.Commit(ctx); err != nil {
		return p, err
	}
	return s.GetPlan(ctx, id)
}

var ErrReviewRequired = errors.New("plan has items requiring review")

// BeginExecution claims the plan for execution. replay=true means the plan was already executed
// (idempotent re-request) and the existing plan should be returned unchanged.
func (s *Store) BeginExecution(ctx context.Context, planID, key string) (op Operation, plan Plan, replay bool, err error) {
	return s.BeginExecutionAs(ctx, planID, key, DecisionManual, "")
}

// Decision values recorded on operations and ledger rows.
const (
	DecisionManual = "manual"
	DecisionAIAuto = "ai_auto"
)

// BeginExecutionAs is BeginExecution with the decision mode (manual | ai_auto) and, for ai_auto, the model name
// that decided; both are stored with the operation and copied onto its ledger rows.
func (s *Store) BeginExecutionAs(ctx context.Context, planID, key, decision, model string) (op Operation, plan Plan, replay bool, err error) {
	if decision == "" {
		decision = DecisionManual
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return
	}
	defer tx.Rollback(context.Background())
	plan, err = getPlan(ctx, tx, planID, true)
	if err != nil {
		return
	}
	switch plan.Status {
	case "executed":
		replay = true
		return
	case "approved", "executing":
	default:
		err = ErrState
		return
	}
	var results []byte
	err = tx.QueryRow(ctx, `SELECT id::text, status, results, decision, decision_model FROM archive_operations WHERE plan_id=$1 AND idempotency_key=$2`, planID, key).Scan(&op.ID, &op.Status, &results, &op.Decision, &op.DecisionModel)
	if err == nil {
		op.PlanID, op.Key = planID, key
		_ = json.Unmarshal(results, &op.Results)
		if op.Status == "running" {
			// resumed after crash or concurrent duplicate; journal makes re-execution idempotent
		}
		return
	}
	if !errors.Is(err, sqldb.ErrNoRows) {
		return
	}
	if plan.Status == "executing" {
		err = fmt.Errorf("%w: another execution is in progress", ErrConflict)
		return
	}
	op.ID, err = imports.NewID()
	if err != nil {
		return
	}
	op.PlanID, op.Key, op.Status, op.Decision, op.DecisionModel = planID, key, "running", decision, model
	if _, err = tx.Exec(ctx, `INSERT INTO archive_operations(id,plan_id,idempotency_key,status,decision,decision_model) VALUES ($1,$2,$3,'running',$4,$5)`, op.ID, planID, key, decision, model); err != nil {
		return
	}
	if _, err = tx.Exec(ctx, `UPDATE archive_plans SET status='executing', updated_at=now() WHERE id=$1`, planID); err != nil {
		return
	}
	if _, err = tx.Exec(ctx, `UPDATE import_jobs SET status='archiving', updated_at=now() WHERE id=$1`, plan.TaskID); err != nil {
		return
	}
	err = tx.Commit(ctx)
	return
}

// FinishExecution records results, writes ledger rows for published files and settles plan/task state.
func (s *Store) FinishExecution(ctx context.Context, op Operation, plan Plan, results []OpResult) (Plan, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return plan, err
	}
	defer tx.Rollback(context.Background())
	byFile := map[string]PlanItem{}
	for _, it := range plan.Items {
		byFile[it.FileID] = it
	}
	failed := 0
	for _, r := range results {
		it := byFile[r.FileID]
		if r.Status == "failed" {
			failed++
			if _, err = tx.Exec(ctx, `UPDATE file_records SET archive_status='failed', error=$2 WHERE id=$1`, r.FileID, "归档失败："+r.Err); err != nil {
				return plan, err
			}
			continue
		}
		canonical := r.FinalRel[strings.LastIndex(r.FinalRel, "/")+1:]
		lid, _ := imports.NewID()
		if _, err = tx.Exec(ctx, `INSERT INTO ledger_entries(id,file_id,task_id,plan_id,operation_id,original_name,canonical_name,department,document_type,document_date,archive_path,sha256,decision,decision_model,executed_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,now()) ON CONFLICT (plan_id,file_id) DO NOTHING`,
			lid, r.FileID, plan.TaskID, plan.ID, op.ID, it.OriginalName, canonical, deref(it.Department), deref(it.DocumentType), deref(it.DocumentDate), r.FinalRel, r.SHA256, decisionOr(op.Decision), op.DecisionModel); err != nil {
			return plan, err
		}
		if _, err = tx.Exec(ctx, `UPDATE file_records SET archive_status='archived', detected_department=$2, detected_type=$3, detected_date=$4::date, review_required=false WHERE id=$1`, r.FileID, it.Department, it.DocumentType, it.DocumentDate); err != nil {
			return plan, err
		}
	}
	rj, _ := json.Marshal(results)
	opStatus, planStatus, jobStatus := "completed", "executed", "completed"
	var priorFailures int
	_ = tx.QueryRow(ctx, `SELECT failure_count FROM import_jobs WHERE id=$1`, plan.TaskID).Scan(&priorFailures)
	if priorFailures > 0 {
		jobStatus = "partial_failure"
	}
	if failed > 0 {
		opStatus, planStatus, jobStatus = "failed", "failed", "partial_failure"
	}
	if _, err = tx.Exec(ctx, `UPDATE archive_operations SET status=$2, results=$3, updated_at=now() WHERE id=$1`, op.ID, opStatus, string(rj)); err != nil {
		return plan, err
	}
	if _, err = tx.Exec(ctx, `UPDATE archive_plans SET status=$2, executed_at=now(), updated_at=now() WHERE id=$1`, plan.ID, planStatus); err != nil {
		return plan, err
	}
	// Automatic mode may leave a separate plan with the items the model could not settle: the task then keeps
	// waiting for a human on that plan instead of being reported as completed.
	var leftover *string
	_ = tx.QueryRow(ctx, `SELECT id::text FROM archive_plans WHERE task_id=$1 AND status='awaiting_review' AND id<>$2 LIMIT 1`, plan.TaskID, plan.ID).Scan(&leftover)
	if leftover != nil {
		if _, err = tx.Exec(ctx, `UPDATE import_jobs SET plan_id=$2 WHERE id=$1`, plan.TaskID, *leftover); err != nil {
			return plan, err
		}
		if failed == 0 {
			jobStatus = "awaiting_review"
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE import_jobs SET status=$2, updated_at=now() WHERE id=$1`, plan.TaskID, jobStatus); err != nil {
		return plan, err
	}
	if err = tx.Commit(ctx); err != nil {
		return plan, err
	}
	return s.GetPlan(ctx, plan.ID)
}

func decisionOr(d string) string {
	if d == DecisionAIAuto {
		return d
	}
	return DecisionManual
}

// RetryExecution lets a failed plan be executed again under a new key (only failed files are redone by the journal).
func (s *Store) ReopenFailedPlan(ctx context.Context, planID string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE archive_plans SET status='approved', updated_at=now() WHERE id=$1 AND status='failed'`, planID)
	return err
}

func (s *Store) Operation(ctx context.Context, id string) (Operation, error) {
	var op Operation
	var res []byte
	if !validUUID(id) {
		return op, ErrNotFound
	}
	err := s.Pool.QueryRow(ctx, `SELECT id::text, plan_id::text, idempotency_key, status, results, decision, decision_model FROM archive_operations WHERE id=$1`, id).Scan(&op.ID, &op.PlanID, &op.Key, &op.Status, &res, &op.Decision, &op.DecisionModel)
	if errors.Is(err, sqldb.ErrNoRows) {
		return op, ErrNotFound
	}
	_ = json.Unmarshal(res, &op.Results)
	return op, err
}

// MarkRolledBack flags ledger rows and files after a successful rollback of an operation.
func (s *Store) MarkRolledBack(ctx context.Context, op Operation) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `UPDATE ledger_entries SET status='rolled_back' WHERE operation_id=$1`, op.ID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE file_records SET archive_status='pending' WHERE id IN (SELECT file_id FROM ledger_entries WHERE operation_id=$1)`, op.ID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE archive_operations SET status='rolled_back', updated_at=now() WHERE id=$1`, op.ID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE archive_plans SET status='approved', updated_at=now() WHERE id=$1`, op.PlanID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE import_jobs SET updated_at=now(), status=CASE WHEN EXISTS (SELECT 1 FROM archive_plans x WHERE x.task_id=import_jobs.id AND x.status='awaiting_review') THEN 'awaiting_review' ELSE 'approved' END WHERE id=(SELECT task_id FROM archive_plans WHERE id=$1)`, op.PlanID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ---- ledger ----

var ledgerSort = map[string]string{"original_name": "original_name", "canonical_name": "canonical_name", "department": "department", "document_type": "document_type", "document_date": "document_date", "archive_path": "archive_path", "executed_at": "executed_at"}

func ledgerWhere(f LedgerFilter) (string, []any) {
	conds := []string{"status='archived'"}
	var args []any
	add := func(c string, v any) {
		args = append(args, v)
		conds = append(conds, strings.ReplaceAll(c, "$$", fmt.Sprintf("$%d", len(args))))
	}
	if f.Q != "" {
		add(`(original_name ILIKE $$ OR canonical_name ILIKE $$ OR department ILIKE $$ OR document_type ILIKE $$ OR archive_path ILIKE $$ OR document_date ILIKE $$)`, likeEscape(f.Q))
	}
	if f.Department != "" {
		categoryCond("department", f.Department, &conds, &args)
	}
	if f.Type != "" {
		categoryCond("document_type", f.Type, &conds, &args)
	}
	if f.DateFrom != "" {
		add(`document_date>=$$`, f.DateFrom)
	}
	if f.DateTo != "" {
		add(`document_date<=$$`, f.DateTo)
	}
	if f.Decision != "" {
		add(`decision=$$`, f.Decision)
	}
	return "WHERE " + strings.Join(conds, " AND "), args
}

// categoryCond filters a department/type column. The reserved value 其他 matches the explicit value as well
// as empty and placeholder (待审核) values, because readers see those as 其他 too.
func categoryCond(col, v string, conds *[]string, args *[]any) {
	if archive.CategoryKey(v) != archive.CategoryKey(archive.CategoryOther) {
		*args = append(*args, v)
		*conds = append(*conds, fmt.Sprintf("%s=$%d", col, len(*args)))
		return
	}
	vals := append([]string{archive.CategoryOther}, archive.UnknownCategoryValues()...)
	ph := make([]string, len(vals))
	for i, x := range vals {
		*args = append(*args, x)
		ph[i] = fmt.Sprintf("$%d", len(*args))
	}
	*conds = append(*conds, fmt.Sprintf("%s IN (%s)", col, strings.Join(ph, ",")))
}

func ledgerOrder(f LedgerFilter) string {
	col, ok := ledgerSort[f.Sort]
	if !ok {
		col = "executed_at"
	}
	dir := "DESC"
	if strings.EqualFold(f.Order, "asc") {
		dir = "ASC"
	}
	return fmt.Sprintf("ORDER BY %s %s, id", col, dir)
}

const ledgerCols = `id::text, original_name, canonical_name, department, document_type, document_date, archive_path, sha256, task_id::text, status, decision, decision_model, executed_at`

func scanLedger(rows sqldb.Rows) ([]LedgerEntry, error) {
	defer rows.Close()
	out := []LedgerEntry{}
	for rows.Next() {
		var e LedgerEntry
		if err := rows.Scan(&e.ID, &e.OriginalName, &e.CanonicalName, &e.Department, &e.DocumentType, &e.DocumentDate, &e.ArchivePath, &e.SHA256, &e.TaskID, &e.Status, &e.Decision, &e.DecisionModel, &e.ExecutedAt); err != nil {
			return nil, err
		}
		e.Department, e.DocumentType = archive.DisplayCategory(e.Department), archive.DisplayCategory(e.DocumentType)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) Ledger(ctx context.Context, p Page, f LedgerFilter) ([]LedgerEntry, int, error) {
	page, size := p.norm()
	where, args := ledgerWhere(f)
	var total int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM ledger_entries `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, size, (page-1)*size)
	rows, err := s.Pool.Query(ctx, fmt.Sprintf(`SELECT `+ledgerCols+` FROM ledger_entries %s %s LIMIT $%d OFFSET $%d`, where, ledgerOrder(f), len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	out, err := scanLedger(rows)
	return out, total, err
}

// LedgerAll returns every matching row (bounded) for Excel export.
func (s *Store) LedgerAll(ctx context.Context, f LedgerFilter, max int) ([]LedgerEntry, error) {
	where, args := ledgerWhere(f)
	args = append(args, max)
	rows, err := s.Pool.Query(ctx, fmt.Sprintf(`SELECT `+ledgerCols+` FROM ledger_entries %s %s LIMIT $%d`, where, ledgerOrder(f), len(args)), args...)
	if err != nil {
		return nil, err
	}
	return scanLedger(rows)
}

// ArchivedHashExists supports duplicate detection by content hash.
func (s *Store) ArchivedByHash(ctx context.Context, sha string) (string, bool, error) {
	var p string
	err := s.Pool.QueryRow(ctx, `SELECT archive_path FROM ledger_entries WHERE sha256=$1 AND status='archived' ORDER BY executed_at DESC LIMIT 1`, sha).Scan(&p)
	if errors.Is(err, sqldb.ErrNoRows) {
		return "", false, nil
	}
	return p, err == nil, err
}

var _ = time.Now

// RunningOperation returns the unfinished operation of an executing plan (crash recovery).
func (s *Store) RunningOperation(ctx context.Context, planID string) (Operation, error) {
	var op Operation
	var res []byte
	err := s.Pool.QueryRow(ctx, `SELECT id::text, plan_id::text, idempotency_key, status, results, decision, decision_model FROM archive_operations WHERE plan_id=$1 AND status='running' ORDER BY created_at DESC LIMIT 1`, planID).Scan(&op.ID, &op.PlanID, &op.Key, &op.Status, &res, &op.Decision, &op.DecisionModel)
	if errors.Is(err, sqldb.ErrNoRows) {
		return op, ErrNotFound
	}
	_ = json.Unmarshal(res, &op.Results)
	return op, err
}

// ---- fallback placeholder ----

const (
	// FallbackDepartment and FallbackType name the plan-item fields that may hold the placeholder 其他.
	FallbackDepartment = "department"
	FallbackType       = "document_type"
)

func joinFallback(f []string) string { return strings.Join(f, ",") }

func splitFallback(s string) []string {
	out := []string{}
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func hasField(f []string, name string) bool {
	for _, x := range f {
		if x == name {
			return true
		}
	}
	return false
}

func dropField(f []string, name string) []string {
	out := []string{}
	for _, x := range f {
		if x != name {
			out = append(out, x)
		}
	}
	return out
}

// ApplyFallbackPlaceholder is the plan layer's deterministic rule: a department/type that is not determined
// (nil) becomes the reserved value 其他 and is flagged as a system-assigned placeholder (FallbackFields), the
// item stays review_required and a warning explains it. The model's own output is never changed. It returns
// true when it changed the item. Idempotent. It removes flags of fields that now hold a real value.
func ApplyFallbackPlaceholder(it *PlanItem) bool {
	changed := false
	apply := func(field string, p **string, label string) {
		if *p == nil {
			other := archive.CategoryOther
			*p = &other
			if !hasField(it.FallbackFields, field) {
				it.FallbackFields = append(it.FallbackFields, field)
			}
			changed = true
		}
		if hasField(it.FallbackFields, field) {
			it.ReviewRequired = true
			w := "未能识别" + label + "，已暂填「" + archive.CategoryOther + "」，请确认或修改"
			for _, e := range it.Warnings {
				if e == w {
					return
				}
			}
			it.Warnings = append(it.Warnings, w)
		}
	}
	apply(FallbackDepartment, &it.Department, "部门")
	apply(FallbackType, &it.DocumentType, "文件类型")
	if it.FallbackFields == nil {
		it.FallbackFields = []string{}
	}
	return changed
}

// stripFallbackWarnings removes earlier placeholder explanations so they can be recomputed from FallbackFields.
func stripFallbackWarnings(w []string) []string {
	out := make([]string, 0, len(w))
	for _, e := range w {
		if strings.HasPrefix(e, "未能识别") && strings.Contains(e, "已暂填") {
			continue
		}
		out = append(out, e)
	}
	return out
}
