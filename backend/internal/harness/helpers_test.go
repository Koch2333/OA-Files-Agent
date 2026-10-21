package harness

import (
	"context"
	"sync"
	"time"
)

type fakeExtractor struct {
	mu      sync.Mutex
	text    string
	ocrText string // returned when OCR=true (defaults to text)
	meta    FileMetadata
	err     error
	delay   time.Duration
	calls   []ExtractRequest
}

func (f *fakeExtractor) Extract(ctx context.Context, req ExtractRequest) (ExtractResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, req)
	f.mu.Unlock()
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return ExtractResult{}, ctx.Err()
		}
	}
	if f.err != nil {
		return ExtractResult{}, f.err
	}
	t := f.text
	if req.OCR && f.ocrText != "" {
		t = f.ocrText
	}
	return ExtractResult{Text: t, Pages: []Page{{Page: 1, Text: t, Source: "native"}}, OCRUsed: req.OCR, Metadata: f.meta, Warnings: []string{}}, nil
}

func (f *fakeExtractor) ocrCalls() int {
	n := 0
	for _, c := range f.calls {
		if c.OCR {
			n++
		}
	}
	return n
}

func fixedNow() time.Time { return time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC) }

func testOpts() Options { return Options{Now: fixedNow} }

func fileIn(name, mime string) FileInput {
	return FileInput{FileID: "f1", SourceName: name, SourcePath: "/src/" + name, MIMEType: mime, StoredPath: "/spool/f1"}
}

func sp(s string) *string { return &s }

func deref(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}
