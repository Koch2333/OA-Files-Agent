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
	// OCRProfile selects a preprocessing profile (see OCRProfiles); empty
	// means the worker default. Pages (1-based) restricts OCR to those pages.
	OCRProfile string `json:"ocr_profile,omitempty"`
	Pages      []int  `json:"pages,omitempty"`
}

// ExtractResult mirrors the worker "data" object.
type ExtractResult struct {
	Text      string       `json:"text"`
	Pages     []Page       `json:"pages"`
	OCRUsed   bool         `json:"ocr_used"`
	Truncated bool         `json:"truncated"`
	Warnings  []string     `json:"warnings"`
	Metadata  FileMetadata `json:"metadata"`
	Quality   *OCRQuality  `json:"quality,omitempty"`
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
	// Instruction is the operator's free-text task description (already length-limited by the API). It reaches the
	// model only as a delimited block in the system prompt and never as tool access or rule changes.
	Instruction string
	// Provider, when set, is the model used for this file instead of the agent's default (per-task configuration:
	// a personal key, the global engine, or the offline Heuristic for tasks without a model).
	Provider Provider
	// Vision, when set, replaces Options.Vision for this file (a personal configuration brings its own vision model;
	// the function may return nil = vision off).
	Vision func() VisionTranscriber
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
	// OCR summarises OCR/vision use; nil when OCR was not used.
	OCR *OCRSummary `json:"ocr"`
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

// OCRProfiles is the closed set of OCR profiles the worker accepts.
var OCRProfiles = []string{"default", "enhanced", "rotate", "deskew", "high_dpi", "binarize"}

// LowConfLine is one low-confidence OCR line (document text: untrusted).
type LowConfLine struct {
	Page       int     `json:"page"`
	Text       string  `json:"text"`
	Confidence float64 `json:"confidence"`
}

// OCRQuality mirrors the worker "quality" object (nil when OCR was not used).
type OCRQuality struct {
	MeanConfidence     *float64      `json:"mean_confidence"`
	LowConfidenceRatio float64       `json:"low_confidence_ratio"`
	Chars              int           `json:"chars"`
	CJKRatio           float64       `json:"cjk_ratio"`
	NoiseRatio         float64       `json:"noise_ratio"`
	Lines              int           `json:"lines"`
	Profile            string        `json:"profile"`
	Rotation           int           `json:"rotation"`
	WeakPages          []int         `json:"weak_pages"`
	LowConfidenceLines []LowConfLine `json:"low_confidence_lines"`
}

// RenderRequest mirrors POST /v1/render of the worker.
type RenderRequest struct {
	Path    string
	Page    int
	MaxSide int
}

// RenderResult is the worker render "data" object.
type RenderResult struct {
	ImageBase64 string `json:"image_base64"`
	MIMEType    string `json:"mime_type"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	Page        int    `json:"page"`
	Pages       int    `json:"pages"`
}

// PageRenderer renders one page as an image. WorkerExtractor implements it;
// an Extractor that does not disables the read_page_image tool.
type PageRenderer interface {
	Render(ctx context.Context, req RenderRequest) (RenderResult, error)
}

// VisionTranscriber turns a page image into text with a vision model. The
// returned text is untrusted document data.
type VisionTranscriber interface {
	Transcribe(ctx context.Context, image []byte, mime, instruction string) (string, error)
}

// OCRSummary reports how the text was obtained. Quality is the final verdict
// (good|fair|poor); the empty string marshals to JSON null.
type OCRSummary struct {
	Used           bool     `json:"used"`
	Quality        string   `json:"quality"`
	MeanConfidence *float64 `json:"mean_confidence"`
	Profile        string   `json:"profile"`
	Attempts       int      `json:"attempts"`
	VisionUsed     bool     `json:"vision_used"`
}

// MarshalJSON emits quality as null when no verdict exists.
func (o OCRSummary) MarshalJSON() ([]byte, error) {
	var q *string
	if o.Quality != "" {
		q = &o.Quality
	}
	return json.Marshal(struct {
		Used           bool     `json:"used"`
		Quality        *string  `json:"quality"`
		MeanConfidence *float64 `json:"mean_confidence"`
		Profile        string   `json:"profile"`
		Attempts       int      `json:"attempts"`
		VisionUsed     bool     `json:"vision_used"`
	}{o.Used, q, o.MeanConfidence, o.Profile, o.Attempts, o.VisionUsed})
}
