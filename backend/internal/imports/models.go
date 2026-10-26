package imports

import (
	"context"
	"crypto/rand"
	"fmt"
	"time"
)

type Job struct {
	ID             string  `json:"id"`
	Status         string  `json:"status"`
	TotalFiles     int     `json:"total_files"`
	ProcessedFiles int     `json:"processed_files"`
	SuccessCount   int     `json:"success_count"`
	FailureCount   int     `json:"failure_count"`
	PlanID         *string `json:"plan_id"`
	TemplateID     *string `json:"template_id,omitempty"`
	// AI settings fixed at creation (see TaskAI). Owner and acknowledgement details stay server-side.
	Instruction string     `json:"instruction"`
	Automation  string     `json:"automation"`
	LLMSource   string     `json:"llm_source"`
	LLMModel    string     `json:"llm_model"`
	LLMOwner    string     `json:"-"`
	AckVersion  string     `json:"-"`
	AckAt       *time.Time `json:"-"`
	AckBy       string     `json:"-"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// Automation modes and model sources of a task.
const (
	AutomationReview = "review"
	AutomationAuto   = "auto"
	SourceNone       = "none"
	SourceGlobal     = "global"
	SourcePersonal   = "personal"
)

// TaskAI is the resolved, validated AI configuration of a new task. The API layer builds it after checking the
// caller's role, the administrator policy and the risk acknowledgement; imports only carries it into the task row.
type TaskAI struct {
	Instruction string
	Automation  string // review | auto
	Source      string // none | global | personal
	Owner       string // user id who created the task (its personal configuration is used when Source=personal)
	Model       string
	AckVersion  string
	AckBy       string // username recorded with the acknowledgement (auto only)
	// Fingerprint binds the AI options into the idempotency fingerprint (same key + different options = conflict).
	Fingerprint string
}

type aiCtxKey struct{}
type optsCtxKey struct{}

// WithAI hands a resolved TaskAI to Import / ImportRemote through the context.
func WithAI(ctx context.Context, ai TaskAI) context.Context {
	return context.WithValue(ctx, aiCtxKey{}, ai)
}

// AIFrom returns the TaskAI carried by ctx (the zero value means "no options": review mode, no model).
func AIFrom(ctx context.Context) TaskAI {
	ai, _ := ctx.Value(aiCtxKey{}).(TaskAI)
	return ai
}

// OptionsHandler receives the raw JSON of the multipart "options" part as soon as it has been read (before any file
// is stored) and returns the resolved TaskAI, or an error that aborts the import (for example *Error with 422).
type OptionsHandler func(raw []byte) (TaskAI, error)

// WithOptionsHandler enables the multipart "options" part for one Import call.
func WithOptionsHandler(ctx context.Context, h OptionsHandler) context.Context {
	return context.WithValue(ctx, optsCtxKey{}, h)
}

// applyAI copies a TaskAI into a new job (defaults: review, no model).
func applyAI(j *Job, ai TaskAI) {
	j.Instruction, j.Automation, j.LLMSource, j.LLMModel, j.LLMOwner = ai.Instruction, ai.Automation, ai.Source, ai.Model, ai.Owner
	if j.Automation == "" {
		j.Automation = AutomationReview
	}
	if j.LLMSource == "" {
		j.LLMSource = SourceNone
	}
	if ai.AckVersion != "" {
		now := time.Now().UTC()
		j.AckVersion, j.AckAt, j.AckBy = ai.AckVersion, &now, ai.AckBy
	}
}

type File struct {
	ID             string    `json:"id"`
	TaskID         string    `json:"task_id"`
	SourceName     string    `json:"source_name"`
	SourcePath     string    `json:"source_path"`
	MIMEType       string    `json:"mime_type"`
	SHA256         string    `json:"sha256"`
	SizeBytes      int64     `json:"size_bytes"`
	Department     *string   `json:"detected_department"`
	DocumentType   *string   `json:"detected_type"`
	Date           *string   `json:"detected_date"`
	Evidence       []string  `json:"evidence"`
	ReviewRequired bool      `json:"review_required"`
	ArchiveStatus  string    `json:"archive_status"`
	Error          *string   `json:"error"`
	CreatedAt      time.Time `json:"created_at"`
	StoredPath     string    `json:"-"`
	OCRUsed        bool      `json:"-"`
	OCR            *OCRInfo  `json:"ocr"`
	TemplateID     *string   `json:"-"`
	// LLMError is set when the file failed because of a model-provider call (nil otherwise); retrying the task re-runs it.
	LLMError *LLMError `json:"llm_error"`
	// Content says how much of the file's content could be read (nil before analysis or when the file failed).
	Content *ContentInfo `json:"content"`
}

// ContentInfo is the readability verdict of one file. Basis "filename_only" means the content could not be read and the
// classification came from the file name alone (always review-required); Reason is encrypted | damaged | unsupported |
// too_large | timeout | ocr_empty | ocr_unavailable | empty (unreadable) or capped | damaged | encoding (partial).
// FormatMismatch holds the real format when the extension did not match the content (e.g. "html" for a web export
// saved as .xls).
type ContentInfo struct {
	Status         string  `json:"status"`
	Reason         *string `json:"reason"`
	Basis          string  `json:"basis"`
	FormatMismatch *string `json:"format_mismatch"`
}

// LLMError is the user-safe description of a provider failure (never a key, request or document text).
type LLMError struct {
	Code              string `json:"code"`
	Status            int    `json:"status"`
	Retryable         bool   `json:"retryable"`
	RetryAfterSeconds int    `json:"retry_after_seconds"`
	Attempts          int    `json:"attempts"`
	Message           string `json:"message"`
}

// OCRInfo summarises how OCR went for a file (nil when OCR was not used).
type OCRInfo struct {
	Used           bool     `json:"used"`
	Quality        *string  `json:"quality"`
	MeanConfidence *float64 `json:"mean_confidence"`
	Profile        string   `json:"profile"`
	Attempts       int      `json:"attempts"`
	VisionUsed     bool     `json:"vision_used"`
}

type Failure struct {
	Index      int    `json:"index"`
	SourcePath string `json:"source_path"`
	Code       string `json:"code"`
	Message    string `json:"message"`
	SHA256     string `json:"-"`
	SizeBytes  int64  `json:"-"`
	MIMEType   string `json:"-"`
}

type Batch struct {
	Key         string    `json:"-"`
	Fingerprint string    `json:"-"`
	TemplateID  string    `json:"-"`
	Job         Job       `json:"task"`
	Files       []File    `json:"files"`
	Failures    []Failure `json:"failures"`
	// Replay is true when an Idempotency-Key matched an earlier request and the stored task was returned.
	Replay bool `json:"-"`
}

func NewID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}
