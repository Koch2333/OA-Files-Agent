package harness

import "time"

// Options configures an Agent. Zero values select safe defaults.
type Options struct {
	MaxTurns        int           // model round trips (default 14: the OCR rescue path alone needs ~10 turns)
	MaxFailures     int           // tool/format failures tolerated (default 3)
	MaxContextChars int           // total characters sent to the model across turns (default 120000)
	MaxTokens       int           // total provider-reported tokens; 0 = unlimited
	MaxTextChars    int           // characters of document text shown to the model per call (default 4000)
	ProviderTimeout time.Duration // per Chat call (default 90s)
	ToolTimeout     time.Duration // per non-OCR tool (default 60s)
	OCRTimeout      time.Duration // run_ocr (default 180s)

	Departments   []VocabEntry // default DefaultDepartments()
	DocumentTypes []VocabEntry // default DefaultDocumentTypes()
	// StrictVocabulary makes out-of-vocabulary department/type values null +
	// review_required instead of being passed through.
	StrictVocabulary bool

	// CheckDuplicates enables the check_duplicates tool when non-nil.
	CheckDuplicates DuplicateChecker
	// Vision returns the vision transcriber, or nil when vision is disabled.
	// It is called each time vision is considered so settings can change
	// without a restart. read_page_image also needs an Extractor that
	// implements PageRenderer.
	Vision func() VisionTranscriber
	// Now is the clock for date plausibility (default time.Now).
	Now func() time.Time
}

const (
	maxFieldRunes    = 64
	maxEvidenceRunes = 300
	maxEvidenceItems = 20
	maxWarnings      = 20
)

func (o Options) withDefaults() Options {
	if o.MaxTurns <= 0 {
		o.MaxTurns = 14
	}
	if o.MaxFailures <= 0 {
		o.MaxFailures = 3
	}
	if o.MaxContextChars <= 0 {
		o.MaxContextChars = 120000
	}
	if o.MaxTextChars <= 0 {
		o.MaxTextChars = 4000
	}
	if o.ProviderTimeout <= 0 {
		o.ProviderTimeout = 90 * time.Second
	}
	if o.ToolTimeout <= 0 {
		o.ToolTimeout = 60 * time.Second
	}
	if o.OCRTimeout <= 0 {
		o.OCRTimeout = 180 * time.Second
	}
	if len(o.Departments) == 0 {
		o.Departments = DefaultDepartments()
	}
	if len(o.DocumentTypes) == 0 {
		o.DocumentTypes = DefaultDocumentTypes()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}
