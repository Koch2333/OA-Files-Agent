package provider

import "context"

type Input struct {
	Filename string `json:"filename"`
	Text     string `json:"text"`
}

type Metadata struct {
	Department     *string  `json:"department"`
	DocumentType   *string  `json:"document_type"`
	DocumentDate   *string  `json:"document_date"`
	Evidence       []string `json:"evidence"`
	ReviewRequired bool     `json:"review_required"`
}

type Provider interface {
	Extract(context.Context, Input) (Metadata, error)
}

// Stub deliberately returns no inferred facts. It is for plumbing tests only.
type Stub struct{}

func (Stub) Extract(ctx context.Context, _ Input) (Metadata, error) {
	if err := ctx.Err(); err != nil {
		return Metadata{}, err
	}
	return Metadata{Evidence: []string{}, ReviewRequired: true}, nil
}
