package provider

import (
	"context"
	"testing"
)

func TestStubDoesNotInventMetadata(t *testing.T) {
	m, err := (Stub{}).Extract(context.Background(), Input{Filename: "财务合同.docx"})
	if err != nil || m.Department != nil || m.DocumentType != nil || m.DocumentDate != nil || !m.ReviewRequired {
		t.Fatalf("unexpected metadata: %+v, %v", m, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (Stub{}).Extract(ctx, Input{}); err != context.Canceled {
		t.Fatalf("expected cancellation, got %v", err)
	}
}
