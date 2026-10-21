// Package store is the PostgreSQL repository. It is the source of truth for tasks, files,
// plans, the ledger, templates, rules, audit log and agent events.
package store

import (
	"errors"
	"time"
)

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
	ErrState    = errors.New("invalid state")
)

type Page struct{ Page, Size int }

func (p Page) norm() (int, int) {
	if p.Page < 1 {
		p.Page = 1
	}
	if p.Size < 1 {
		p.Size = 20
	}
	if p.Size > 200 {
		p.Size = 200
	}
	return p.Page, p.Size
}

type PlanItem struct {
	FileID          string   `json:"file_id"`
	OriginalName    string   `json:"original_name"`
	SuggestedName   string   `json:"suggested_name"`
	SourcePath      string   `json:"source_path"`
	DestinationPath string   `json:"destination_path"`
	Department      *string  `json:"department"`
	DocumentType    *string  `json:"document_type"`
	DocumentDate    *string  `json:"document_date"`
	Warnings        []string `json:"warnings"`
	ReviewRequired  bool     `json:"review_required"`
}

type Plan struct {
	ID               string     `json:"id"`
	TaskID           string     `json:"task_id"`
	Items            []PlanItem `json:"items"`
	Status           string     `json:"status"`
	Version          int        `json:"version"`
	RequiresApproval bool       `json:"requires_approval"`
	ApprovedBy       *string    `json:"approved_by"`
	ApprovedAt       *time.Time `json:"approved_at"`
	RuleID           *string    `json:"-"`
	OperationID      *string    `json:"operation_id"`
}

// PlanPatch changes one item. Nil pointers leave a field unchanged; empty string clears department/type/date.
type PlanPatch struct {
	FileID          string  `json:"file_id"`
	Department      *string `json:"department"`
	DocumentType    *string `json:"document_type"`
	DocumentDate    *string `json:"document_date"`
	SuggestedName   *string `json:"suggested_name"`
	DestinationPath *string `json:"destination_path"`
}

type LedgerEntry struct {
	ID            string    `json:"id"`
	OriginalName  string    `json:"original_name"`
	CanonicalName string    `json:"canonical_name"`
	Department    string    `json:"department"`
	DocumentType  string    `json:"document_type"`
	DocumentDate  string    `json:"document_date"`
	ArchivePath   string    `json:"archive_path"`
	SHA256        string    `json:"sha256"`
	TaskID        string    `json:"task_id"`
	Status        string    `json:"status"`
	ExecutedAt    time.Time `json:"executed_at"`
}

type LedgerFilter struct {
	Q, Department, Type, DateFrom, DateTo, Sort, Order string
}

type FileFilter struct {
	Q, Department, Type, DateFrom, DateTo, ArchiveStatus, TaskID string
	ReviewRequired                                               *bool
}

type Template struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	Enabled       bool           `json:"enabled"`
	Priority      int            `json:"priority"`
	CreatedAt     time.Time      `json:"created_at"`
	MatchRules    map[string]any `json:"match_rules"`
	FieldMappings map[string]any `json:"field_mappings"`
}

type Rule struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	NamingPattern    string `json:"naming_pattern"`
	DirectoryPattern string `json:"directory_pattern"`
	Enabled          bool   `json:"enabled"`
}

type AuditLog struct {
	ID        string    `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	Actor     string    `json:"actor"`
	Action    string    `json:"action"`
	Target    string    `json:"target"`
	Summary   string    `json:"summary"`
}

type Event struct {
	ID        int64          `json:"id"`
	TaskID    string         `json:"task_id"`
	Timestamp time.Time      `json:"timestamp"`
	Phase     string         `json:"phase"`
	EventType string         `json:"event_type"`
	FileID    *string        `json:"file_id,omitempty"`
	Summary   string         `json:"summary"`
	Details   map[string]any `json:"details,omitempty"`
}

type Change struct {
	Field  string  `json:"field"`
	Label  string  `json:"label"`
	Before *string `json:"before"`
	After  *string `json:"after"`
}

type Impact struct {
	AffectedFiles int    `json:"affected_files"`
	Scope         string `json:"scope"`
	RuleChanges   bool   `json:"rule_changes"`
}

type Correction struct {
	ID          string   `json:"id"`
	FileID      string   `json:"file_id"`
	Instruction string   `json:"instruction"`
	Status      string   `json:"status"`
	Changes     []Change `json:"changes"`
	Impact      Impact   `json:"impact"`
}

// OpResult mirrors archive.OpResult as persisted in archive_operations.results.
type OpResult struct {
	OpID        string   `json:"op_id,omitempty"`
	CreatedDirs []string `json:"created_dirs,omitempty"`
	FileID      string   `json:"file_id"`
	FinalRel    string   `json:"final_rel"`
	Status      string   `json:"status"`
	Err         string   `json:"err,omitempty"`
	SHA256      string   `json:"sha256"`
}

type Operation struct {
	ID      string
	PlanID  string
	Key     string
	Status  string
	Results []OpResult
}

// AnalysisResult is what the pipeline stores for one analysed file.
type AnalysisResult struct {
	Department, DocumentType, DocumentDate *string
	Evidence                               []string
	ReviewRequired                         bool
	TemplateID                             *string
	OCRUsed                                bool
	Error                                  *string
	Analysis                               map[string]any
}
