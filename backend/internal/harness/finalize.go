package harness

import (
	"strings"
	"time"
	"unicode/utf8"
)

// FinalAnswer is the model's (or heuristic's) proposed result, before Go
// validation.
type FinalAnswer struct {
	Department     *string  `json:"department"`
	DocumentType   *string  `json:"document_type"`
	DocumentDate   *string  `json:"document_date"`
	Evidence       []string `json:"evidence"`
	ReviewRequired bool     `json:"review_required"`
}

func cleanStr(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > max {
		s = string([]rune(s)[:max])
	}
	return s
}

func cleanPtr(p *string, max int) *string {
	if p == nil {
		return nil
	}
	s := cleanStr(*p, max)
	if s == "" {
		return nil
	}
	return &s
}

// validateFinal deterministically validates a proposed answer: strings are
// trimmed and capped, vocabulary is normalised (and enforced when strict),
// the date must be a real, plausible YYYY-MM-DD, and any populated field must
// be backed by evidence. Anything failing becomes nil + review_required.
func validateFinal(fa FinalAnswer, o Options, now time.Time) (Result, []string) {
	r := Result{ReviewRequired: fa.ReviewRequired, Evidence: []string{}, Warnings: []string{}}
	var warns []string
	r.Department = cleanPtr(fa.Department, maxFieldRunes)
	r.DocumentType = cleanPtr(fa.DocumentType, maxFieldRunes)
	vocab := func(field string, p *string, v []VocabEntry) *string {
		if p == nil {
			return nil
		}
		if n, ok := resolveExact(v, *p); ok {
			return &n
		}
		if o.StrictVocabulary {
			warns = append(warns, field+"不在配置词表内，已置空")
			return nil
		}
		return p
	}
	r.Department = vocab("部门", r.Department, o.Departments)
	r.DocumentType = vocab("类型", r.DocumentType, o.DocumentTypes)
	if fa.DocumentDate != nil {
		if d, ok := ParseISODate(strings.TrimSpace(*fa.DocumentDate), now); ok {
			r.DocumentDate = &d
		} else {
			warns = append(warns, "日期不是有效且合理的 YYYY-MM-DD，已置空")
		}
	}
	for _, e := range fa.Evidence {
		if e = cleanStr(e, maxEvidenceRunes); e != "" && len(r.Evidence) < maxEvidenceItems {
			r.Evidence = append(r.Evidence, e)
		}
	}
	if len(r.Evidence) == 0 && (r.Department != nil || r.DocumentType != nil || r.DocumentDate != nil) {
		r.Department, r.DocumentType, r.DocumentDate = nil, nil, nil
		warns = append(warns, "字段缺少证据，已置空")
	}
	if r.Department == nil || r.DocumentType == nil || r.DocumentDate == nil || len(warns) > 0 {
		r.ReviewRequired = true
	}
	return r, warns
}

// resolveExact maps a value that equals a canonical name or an exact keyword.
func resolveExact(v []VocabEntry, s string) (string, bool) {
	for _, e := range v {
		if s == e.Name {
			return e.Name, true
		}
		for _, k := range e.Keywords {
			if s == k {
				return e.Name, true
			}
		}
	}
	return "", false
}
