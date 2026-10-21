package pipeline

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"

	"oa-agent/backend/internal/api"
	"oa-agent/backend/internal/harness"
	"oa-agent/backend/internal/store"
)

var dateRe = regexp.MustCompile(`(20\d{2})\s*[-/.年]\s*(\d{1,2})\s*[-/.月]\s*(\d{1,2})`)

// ProposeCorrection maps a natural-language instruction to concrete field changes for ONE file.
// It is deliberately deterministic (vocabulary + date matching): the result is shown as a before/after
// diff and applied only after explicit approval; it never touches global rules.
func (s *Service) ProposeCorrection(ctx context.Context, fileID, instruction string) (store.Correction, error) {
	f, err := s.cfg.Store.GetFile(ctx, fileID)
	if err != nil {
		return store.Correction{}, err
	}
	var changes []store.Change
	pick := func(vocab []harness.VocabEntry) (string, bool) {
		var best string
		bestLen := 0
		for _, e := range vocab {
			for _, k := range append([]string{e.Name}, e.Keywords...) {
				if k != "" && strings.Contains(instruction, k) && len([]rune(k)) > bestLen {
					best, bestLen = e.Name, len([]rune(k))
				}
			}
		}
		return best, best != ""
	}
	if d, ok := pick(harness.DefaultDepartments()); ok && (f.Department == nil || *f.Department != d) {
		changes = append(changes, store.Change{Field: "department", Label: "所属部门", Before: f.Department, After: &d})
	}
	if t, ok := pick(harness.DefaultDocumentTypes()); ok && (f.DocumentType == nil || *f.DocumentType != t) {
		changes = append(changes, store.Change{Field: "document_type", Label: "文件类型", Before: f.DocumentType, After: &t})
	}
	if m := dateRe.FindStringSubmatch(instruction); m != nil {
		y, _ := strconv.Atoi(m[1])
		mo, _ := strconv.Atoi(m[2])
		d, _ := strconv.Atoi(m[3])
		t := time.Date(y, time.Month(mo), d, 0, 0, 0, 0, time.UTC)
		if t.Year() == y && int(t.Month()) == mo && t.Day() == d {
			iso := t.Format("2006-01-02")
			if f.Date == nil || *f.Date != iso {
				changes = append(changes, store.Change{Field: "document_date", Label: "生成日期", Before: f.Date, After: &iso})
			}
		}
	}
	if len(changes) == 0 {
		return store.Correction{}, &api.UnderstandError{Msg: "未能识别出可修改的字段，请说明要改成的部门、文件类型或日期（如“应属财务部，类型是报表，日期 2026-03-15”）"}
	}
	return s.cfg.Store.CreateCorrection(ctx, store.Correction{FileID: fileID, Instruction: instruction, Changes: changes, Impact: store.Impact{AffectedFiles: 1, Scope: "single_file", RuleChanges: false}})
}
