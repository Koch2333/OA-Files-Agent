package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"oa-agent/backend/internal/imports"
)

type Store struct{ Pool *pgxpool.Pool }

func Open(ctx context.Context, url string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, errors.New("invalid database URL")
	}
	cfg.MaxConns = 10
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, errors.New("database connection failed")
	}
	if err = pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, errors.New("database connection failed")
	}
	return &Store{Pool: pool}, nil
}

func (s *Store) Close() { s.Pool.Close() }

const jobCols = `id::text, status, total_files, processed_files, success_count, failure_count, plan_id::text, template_id::text, instruction, automation, llm_source, llm_model, llm_owner, auto_ack_version, auto_ack_at, auto_ack_by, created_at, updated_at`

func scanJob(r pgx.Row) (imports.Job, error) {
	var j imports.Job
	err := r.Scan(&j.ID, &j.Status, &j.TotalFiles, &j.ProcessedFiles, &j.SuccessCount, &j.FailureCount, &j.PlanID, &j.TemplateID, &j.Instruction, &j.Automation, &j.LLMSource, &j.LLMModel, &j.LLMOwner, &j.AckVersion, &j.AckAt, &j.AckBy, &j.CreatedAt, &j.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return j, ErrNotFound
	}
	return j, err
}

const fileCols = `id::text, task_id::text, source_name, source_path, mime_type, sha256, size_bytes, detected_department, detected_type, to_char(detected_date,'YYYY-MM-DD'), evidence, review_required, archive_status, error, created_at, COALESCE(stored_path,''), ocr_used, template_id::text, analysis->'ocr'`

func scanFile(r pgx.Row) (imports.File, error) {
	var f imports.File
	var ev, ocr []byte
	err := r.Scan(&f.ID, &f.TaskID, &f.SourceName, &f.SourcePath, &f.MIMEType, &f.SHA256, &f.SizeBytes, &f.Department, &f.DocumentType, &f.Date, &ev, &f.ReviewRequired, &f.ArchiveStatus, &f.Error, &f.CreatedAt, &f.StoredPath, &f.OCRUsed, &f.TemplateID, &ocr)
	if errors.Is(err, pgx.ErrNoRows) {
		return f, ErrNotFound
	}
	if err != nil {
		return f, err
	}
	if json.Unmarshal(ev, &f.Evidence) != nil || f.Evidence == nil {
		f.Evidence = []string{}
	}
	if len(ocr) > 0 && string(ocr) != "null" {
		var info imports.OCRInfo
		if json.Unmarshal(ocr, &info) == nil && info.Used {
			f.OCR = &info
		}
	}
	return f, nil
}

func nullStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Save persists an import batch atomically. A repeated Idempotency-Key with the same fingerprint replays
// the stored batch; with a different fingerprint it is a conflict.
func (s *Store) Save(ctx context.Context, b imports.Batch) (imports.Batch, bool, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return b, false, err
	}
	defer tx.Rollback(context.Background())
	if b.Key != "" {
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "import:"+b.Key); err != nil {
			return b, false, err
		}
		var id, fp string
		err = tx.QueryRow(ctx, `SELECT id::text, COALESCE(request_fingerprint,'') FROM import_jobs WHERE idempotency_key=$1`, b.Key).Scan(&id, &fp)
		if err == nil {
			if fp != b.Fingerprint {
				return b, false, ErrConflict
			}
			existing, e := s.Task(ctx, id)
			return existing, true, e
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return b, false, err
		}
	}
	j := b.Job
	automation, source := j.Automation, j.LLMSource
	if automation == "" {
		automation = imports.AutomationReview
	}
	if source == "" {
		source = imports.SourceNone
	}
	if _, err = tx.Exec(ctx, `INSERT INTO import_jobs(id,status,total_files,processed_files,success_count,failure_count,template_id,idempotency_key,request_fingerprint,created_at,updated_at,instruction,automation,llm_source,llm_owner,llm_model,auto_ack_version,auto_ack_at,auto_ack_by) VALUES ($1,$2,$3,$4,0,$5,$6::uuid,$7,$8,$9,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
		j.ID, j.Status, j.TotalFiles, j.ProcessedFiles, j.FailureCount, nullStr(b.TemplateID), nullStr(b.Key), nullStr(b.Fingerprint), j.CreatedAt,
		j.Instruction, automation, source, j.LLMOwner, j.LLMModel, j.AckVersion, j.AckAt, j.AckBy); err != nil {
		return b, false, err
	}
	for _, f := range b.Files {
		if _, err = tx.Exec(ctx, `INSERT INTO file_records(id,task_id,source_name,source_path,mime_type,sha256,size_bytes,evidence,review_required,archive_status,stored_path,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,'[]',true,'pending',$8,$9)`,
			f.ID, f.TaskID, f.SourceName, f.SourcePath, f.MIMEType, f.SHA256, f.SizeBytes, f.StoredPath, f.CreatedAt); err != nil {
			return b, false, err
		}
	}
	for i, fl := range b.Failures {
		id, e := imports.NewID()
		if e != nil {
			return b, false, e
		}
		sha := fl.SHA256
		if len(sha) != 64 {
			sha = strings.Repeat("0", 64)
		}
		name := fl.SourcePath[strings.LastIndex(fl.SourcePath, "/")+1:]
		msg := fl.Message
		if _, err = tx.Exec(ctx, `INSERT INTO file_records(id,task_id,source_name,source_path,mime_type,sha256,size_bytes,evidence,review_required,archive_status,error,processed,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,'[]',true,'failed',$8,true,$9)`,
			id, j.ID, name, fl.SourcePath, fl.MIMEType, sha, fl.SizeBytes, "["+fl.Code+"] "+msg, j.CreatedAt); err != nil {
			return b, false, err
		}
		_ = i
	}
	// Rejected-at-intake files count as processed failures so progress stays consistent.
	if _, err = tx.Exec(ctx, `UPDATE import_jobs SET processed_files=$2, failure_count=$2 WHERE id=$1`, j.ID, len(b.Failures)); err != nil {
		return b, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return b, false, err
	}
	saved, err := s.Task(ctx, j.ID)
	return saved, false, err
}

// Task returns the batch view of a job (job + its files).
func (s *Store) Task(ctx context.Context, id string) (imports.Batch, error) {
	var b imports.Batch
	j, err := s.GetJob(ctx, id)
	if err != nil {
		return b, err
	}
	files, err := s.JobFiles(ctx, id)
	if err != nil {
		return b, err
	}
	return imports.Batch{Job: j, Files: files, Failures: []imports.Failure{}}, nil
}

func (s *Store) GetJob(ctx context.Context, id string) (imports.Job, error) {
	if !validUUID(id) {
		return imports.Job{}, ErrNotFound
	}
	return scanJob(s.Pool.QueryRow(ctx, `SELECT `+jobCols+` FROM import_jobs WHERE id=$1`, id))
}

func (s *Store) ListJobs(ctx context.Context, p Page, status string) ([]imports.Job, int, error) {
	page, size := p.norm()
	where, args := "", []any{}
	if status != "" {
		where, args = "WHERE status=$1", append(args, status)
	}
	var total int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM import_jobs `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, size, (page-1)*size)
	rows, err := s.Pool.Query(ctx, fmt.Sprintf(`SELECT `+jobCols+` FROM import_jobs %s ORDER BY created_at DESC LIMIT $%d OFFSET $%d`, where, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []imports.Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, j)
	}
	return out, total, rows.Err()
}

func (s *Store) JobFiles(ctx context.Context, taskID string) ([]imports.File, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+fileCols+` FROM file_records WHERE task_id=$1 ORDER BY source_path, id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []imports.File{}
	for rows.Next() {
		f, err := scanFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Store) GetFile(ctx context.Context, id string) (imports.File, error) {
	if !validUUID(id) {
		return imports.File{}, ErrNotFound
	}
	return scanFile(s.Pool.QueryRow(ctx, `SELECT `+fileCols+` FROM file_records WHERE id=$1`, id))
}

func likeEscape(s string) string {
	return "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s) + "%"
}

func (s *Store) ListFiles(ctx context.Context, p Page, f FileFilter) ([]imports.File, int, error) {
	page, size := p.norm()
	var conds []string
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		conds = append(conds, strings.ReplaceAll(cond, "$$", fmt.Sprintf("$%d", len(args))))
	}
	if f.Q != "" {
		add(`(source_name ILIKE $$ OR source_path ILIKE $$)`, likeEscape(f.Q))
	}
	if f.Department != "" {
		add(`detected_department=$$`, f.Department)
	}
	if f.Type != "" {
		add(`detected_type=$$`, f.Type)
	}
	if f.DateFrom != "" {
		add(`detected_date>=$$::date`, f.DateFrom)
	}
	if f.DateTo != "" {
		add(`detected_date<=$$::date`, f.DateTo)
	}
	if f.ArchiveStatus != "" {
		add(`archive_status=$$`, f.ArchiveStatus)
	}
	if f.TaskID != "" {
		add(`task_id=$$::uuid`, f.TaskID)
	}
	if f.ReviewRequired != nil {
		add(`review_required=$$`, *f.ReviewRequired)
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}
	var total int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM file_records `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, size, (page-1)*size)
	rows, err := s.Pool.Query(ctx, fmt.Sprintf(`SELECT `+fileCols+` FROM file_records %s ORDER BY created_at DESC, source_path LIMIT $%d OFFSET $%d`, where, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []imports.File{}
	for rows.Next() {
		f, err := scanFile(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, f)
	}
	return out, total, rows.Err()
}

// ---- job queue ----

// ClaimJob atomically moves one queued job to scanning. Returns ErrNotFound when the queue is empty.
func (s *Store) ClaimJob(ctx context.Context) (imports.Job, error) {
	return scanJob(s.Pool.QueryRow(ctx, `UPDATE import_jobs SET status='scanning', updated_at=now()
		WHERE id=(SELECT id FROM import_jobs WHERE status='queued' AND NOT cancel_requested AND total_files > failure_count ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1)
		RETURNING `+jobCols))
}

// RecoverJobs puts interrupted in-flight jobs back in the queue (processing is idempotent per file).
func (s *Store) RecoverJobs(ctx context.Context) (int64, error) {
	t, err := s.Pool.Exec(ctx, `UPDATE import_jobs SET status='queued', updated_at=now() WHERE status IN ('scanning','extracting','analyzing','planning')`)
	return t.RowsAffected(), err
}

// RecoverArchiving returns jobs left in 'archiving' (crash mid-execution) with their plan IDs.
func (s *Store) RecoverArchiving(ctx context.Context) ([]string, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id::text FROM archive_plans WHERE status='executing'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) SetJobStatus(ctx context.Context, id, status string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE import_jobs SET status=$2, updated_at=now() WHERE id=$1`, id, status)
	return err
}

func (s *Store) FailJob(ctx context.Context, id, msg string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE import_jobs SET status='failed', last_error=$2, updated_at=now() WHERE id=$1`, id, msg)
	return err
}

func (s *Store) CancelRequested(ctx context.Context, id string) (bool, error) {
	var c bool
	err := s.Pool.QueryRow(ctx, `SELECT cancel_requested OR status='cancelled' FROM import_jobs WHERE id=$1`, id).Scan(&c)
	return c, err
}

// CancelJob requests cancellation. Queued jobs are cancelled immediately; running jobs stop at the next checkpoint.
func (s *Store) CancelJob(ctx context.Context, id string) (imports.Job, error) {
	j, err := s.GetJob(ctx, id)
	if err != nil {
		return j, err
	}
	switch j.Status {
	case "completed", "failed", "cancelled", "partial_failure", "approved", "archiving":
		return j, ErrState
	}
	if j.Status == "queued" || j.Status == "awaiting_review" {
		_, err = s.Pool.Exec(ctx, `UPDATE import_jobs SET status='cancelled', cancel_requested=true, updated_at=now() WHERE id=$1 AND status IN ('queued','awaiting_review')`, id)
	} else {
		_, err = s.Pool.Exec(ctx, `UPDATE import_jobs SET cancel_requested=true, updated_at=now() WHERE id=$1`, id)
	}
	if err != nil {
		return j, err
	}
	return s.GetJob(ctx, id)
}

// RetryJob re-queues a failed/cancelled job: files without a stored analysis are processed again.
func (s *Store) RetryJob(ctx context.Context, id string) (imports.Job, error) {
	j, err := s.GetJob(ctx, id)
	if err != nil {
		return j, err
	}
	if j.Status != "failed" && j.Status != "cancelled" && j.Status != "partial_failure" {
		return j, ErrState
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return j, err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `UPDATE file_records SET processed=false, error=NULL WHERE task_id=$1 AND stored_path IS NOT NULL AND stored_path<>'' AND archive_status<>'archived'`, id); err != nil {
		return j, err
	}
	if _, err = tx.Exec(ctx, `UPDATE import_jobs SET status='queued', cancel_requested=false, last_error=NULL, plan_id=NULL, updated_at=now(),
		processed_files=(SELECT count(*) FROM file_records WHERE task_id=$1 AND processed),
		failure_count=(SELECT count(*) FROM file_records WHERE task_id=$1 AND processed AND error IS NOT NULL),
		success_count=(SELECT count(*) FROM file_records WHERE task_id=$1 AND processed AND error IS NULL) WHERE id=$1`, id); err != nil {
		return j, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM archive_plans WHERE task_id=$1 AND status IN ('draft','awaiting_review','approved')`, id); err != nil {
		return j, err
	}
	if err = tx.Commit(ctx); err != nil {
		return j, err
	}
	return s.GetJob(ctx, id)
}

func (s *Store) SaveAnalysis(ctx context.Context, fileID string, r AnalysisResult) error {
	ev, _ := json.Marshal(nonNil(r.Evidence))
	an, _ := json.Marshal(r.Analysis)
	if r.Analysis == nil {
		an = []byte("{}")
	}
	_, err := s.Pool.Exec(ctx, `UPDATE file_records SET detected_department=$2, detected_type=$3, detected_date=$4::date, evidence=$5, review_required=$6, template_id=$7::uuid, ocr_used=$8, error=$9, analysis=$10, processed=true WHERE id=$1`,
		fileID, r.Department, r.DocumentType, r.DocumentDate, ev, r.ReviewRequired, r.TemplateID, r.OCRUsed, r.Error, an)
	return err
}

// Progress recomputes counters from file rows so a re-run never double counts.
func (s *Store) Progress(ctx context.Context, taskID string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE import_jobs SET updated_at=now(),
		processed_files=(SELECT count(*) FROM file_records WHERE task_id=$1 AND processed),
		failure_count=(SELECT count(*) FROM file_records WHERE task_id=$1 AND processed AND error IS NOT NULL),
		success_count=(SELECT count(*) FROM file_records WHERE task_id=$1 AND processed AND error IS NULL) WHERE id=$1`, taskID)
	return err
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// ---- events ----

func (s *Store) AppendEvent(ctx context.Context, taskID string, phase, typ string, fileID *string, summary string, details map[string]any) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	var seq int64
	if err = tx.QueryRow(ctx, `UPDATE import_jobs SET event_seq=event_seq+1 WHERE id=$1 RETURNING event_seq`, taskID).Scan(&seq); err != nil {
		return err
	}
	var dj []byte
	if details != nil {
		dj, _ = json.Marshal(details)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO agent_events(task_id,seq,phase,event_type,file_id,summary,details) VALUES ($1,$2,$3,$4,$5::uuid,$6,$7)`, taskID, seq, phase, typ, fileID, summary, dj); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) Events(ctx context.Context, taskID string, after int64, limit int) ([]Event, error) {
	rows, err := s.Pool.Query(ctx, `SELECT seq, task_id::text, ts, phase, event_type, file_id::text, summary, details FROM agent_events WHERE task_id=$1 AND seq>$2 ORDER BY seq LIMIT $3`, taskID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		var d []byte
		if err := rows.Scan(&e.ID, &e.TaskID, &e.Timestamp, &e.Phase, &e.EventType, &e.FileID, &e.Summary, &d); err != nil {
			return nil, err
		}
		if len(d) > 0 {
			_ = json.Unmarshal(d, &e.Details)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---- audit ----

func (s *Store) Audit(ctx context.Context, actor, action, target, summary string) {
	id, _ := imports.NewID()
	_, _ = s.Pool.Exec(ctx, `INSERT INTO audit_logs(id,actor,action,target,summary) VALUES ($1,$2,$3,$4,$5)`, id, actor, action, target, summary)
}

func (s *Store) AuditLogs(ctx context.Context, p Page) ([]AuditLog, int, error) {
	page, size := p.norm()
	var total int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.Pool.Query(ctx, `SELECT id::text, ts, actor, action, target, summary FROM audit_logs ORDER BY ts DESC, id LIMIT $1 OFFSET $2`, size, (page-1)*size)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []AuditLog{}
	for rows.Next() {
		var a AuditLog
		if err := rows.Scan(&a.ID, &a.Timestamp, &a.Actor, &a.Action, &a.Target, &a.Summary); err != nil {
			return nil, 0, err
		}
		out = append(out, a)
	}
	return out, total, rows.Err()
}

func validUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if c != '-' {
				return false
			}
		case !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'):
			return false
		}
	}
	return true
}

var _ = time.Now
