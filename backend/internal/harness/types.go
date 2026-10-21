// Package harness implements the Agent Harness: a bounded tool-calling loop
// whose tools are read-only views over one file. The model never receives a
// shell or any filesystem write capability; file contents are treated as data.
package harness

import (
	"context"
	"encoding/json"
)

// Page is one extracted page as returned by the document worker.
type Page struct {
	Page       int      `json:"page"`
	Text       string   `json:"text"`
	Source     string   `json:"source"` // native | ocr
	Confidence *float64 `json:"confidence,omitempty"`
}

// FileMetadata is the container metadata reported by the worker.
type FileMetadata struct {
	Title    *string `json:"title"`
	Author   *string `json:"author"`
	Created  *string `json:"created"`
	Modified *string `json:"modified"`
}

// ExtractRequest mirrors POST /v1/extract of the worker.
type ExtractRequest struct {
	Path     string `json:"path"`
	Filename string `json:"filename"`
	MIMEType string `json:"mime_type"`
	OCR      bool   `json:"ocr"`
}

// ExtractResult mirrors the worker "data" object.
type ExtractResult struct {
	Text      string       `json:"text"`
	Pages     []Page       `json:"pages"`
	OCRUsed   bool         `json:"ocr_used"`
	Truncated bool         `json:"truncated"`
	Warnings  []string     `json:"warnings"`
	Metadata  FileMetadata `json:"metadata"`
}

// Extractor extracts text from a stored file. Implemented by WorkerExtractor.
type Extractor interface {
	Extract(ctx context.Context, req ExtractRequest) (ExtractResult, error)
}

// Template is an administrator-defined OA template. A higher Priority wins.
// DateSource is one of ocr_text, filename, file_metadata (or empty).
type Template struct {
	ID               string
	Name             string
	Enabled          bool
	Priority         int
	FilenameKeywords []string
	TextKeywords     []string
	Department       string
	DocumentType     string
	DateSource       string
}

// FileInput describes the file under analysis. StoredPath is the spool path
// handed to the worker; neither path is ever shown to the model.
type FileInput struct {
	FileID     string
	SourceName string
	SourcePath string
	MIMEType   string
	StoredPath string
}

// ToolCall is a model-requested tool invocation. In Result.Trace the Arguments
// are dropped and only the redacted outcome is kept.
type ToolCall struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	Arguments  json.RawMessage `json:"arguments,omitempty"`
	OK         bool            `json:"ok"`
	ErrorCode  string          `json:"error_code,omitempty"`
	DurationMS int64           `json:"duration_ms"`
}

// Result is the validated outcome of AnalyzeFile. Nil fields mean "unknown".
type Result struct {
	Department     *string    `json:"department"`
	DocumentType   *string    `json:"document_type"`
	DocumentDate   *string    `json:"document_date"` // YYYY-MM-DD
	Evidence       []string   `json:"evidence"`
	ReviewRequired bool       `json:"review_required"`
	TemplateID     *string    `json:"template_id"`
	Trace          []ToolCall `json:"trace"`
	Warnings       []string   `json:"warnings"`
}

// Event is emitted for progress reporting. Details never contain document
// text, prompts, file names or paths: only tool names, counts and field names.
type Event struct {
	Phase   string         `json:"phase"`
	Type    string         `json:"type"`
	Summary string         `json:"summary"`
	Details map[string]any `json:"details,omitempty"`
}

// DuplicateRef identifies a possible duplicate returned by DuplicateChecker.
type DuplicateRef struct {
	FileID string `json:"file_id"`
	Name   string `json:"name"`
}

// DuplicateChecker is injected by the pipeline (e.g. a SHA-256 lookup).
type DuplicateChecker func(ctx context.Context, f FileInput) ([]DuplicateRef, error)

// VocabEntry maps a canonical value to the keywords/synonyms that indicate it.
type VocabEntry struct {
	Name     string
	Keywords []string
}
