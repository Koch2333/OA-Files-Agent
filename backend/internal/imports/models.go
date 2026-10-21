package imports

import (
	"crypto/rand"
	"fmt"
	"time"
)

type Job struct {
	ID             string    `json:"id"`
	Status         string    `json:"status"`
	TotalFiles     int       `json:"total_files"`
	ProcessedFiles int       `json:"processed_files"`
	SuccessCount   int       `json:"success_count"`
	FailureCount   int       `json:"failure_count"`
	PlanID         *string   `json:"plan_id"`
	TemplateID     *string   `json:"template_id,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
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
	TemplateID     *string   `json:"-"`
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
