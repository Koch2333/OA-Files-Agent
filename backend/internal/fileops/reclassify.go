package fileops

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"oa-agent/backend/internal/archive"
	"oa-agent/backend/internal/fsman"
	"oa-agent/backend/internal/store"
)

// Reclass is a correction of an archived file's classification. A nil field stays as it is; a date of "" clears it.
type Reclass struct {
	Department   *string
	DocumentType *string
	DocumentDate *string
}

// ErrReclassInvalid marks a rejected correction (bad value, not an archived file).
var ErrReclassInvalid = errors.New("invalid correction")

var isoDate = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// Derive computes the standard name and destination for a plan-shaped item (pipeline.Service.Derive).
type Derive func(ctx context.Context, it store.PlanItem) (name, dest string, warnings []string, err error)

// Reclassify corrects department / type / date of one archived ledger entry AFTER archiving (the way to fix the placeholders
// 其他 / 未知日期 that the automatic mode leaves behind): the standard name and folder are derived again from the new values, the file
// is moved there (never overwriting; the move keeps the ledger path in sync) and the ledger and file record take the new values.
func (s *Service) Reclassify(ctx context.Context, id string, rc Reclass, derive Derive, actor string) (store.LedgerEntry, error) {
	e, err := s.Store.LedgerEntryByID(ctx, id)
	if err != nil {
		return store.LedgerEntry{}, err
	}
	if e.Status != "archived" {
		return store.LedgerEntry{}, fmt.Errorf("%w: the file is not archived", ErrReclassInvalid)
	}
	dept, typ, date := e.Department, e.DocumentType, e.DocumentDate
	if rc.Department != nil {
		dept = strings.TrimSpace(*rc.Department)
	}
	if rc.DocumentType != nil {
		typ = strings.TrimSpace(*rc.DocumentType)
	}
	if rc.DocumentDate != nil {
		date = strings.TrimSpace(*rc.DocumentDate)
	}
	for _, v := range []string{dept, typ} {
		if v == "" {
			return store.LedgerEntry{}, fmt.Errorf("%w: department and type cannot be empty (use 其他)", ErrReclassInvalid)
		}
		if err := archive.ValidateCategory(v); err != nil {
			return store.LedgerEntry{}, fmt.Errorf("%w: %v", ErrReclassInvalid, err)
		}
	}
	if date != "" && !isoDate.MatchString(date) {
		return store.LedgerEntry{}, fmt.Errorf("%w: the date must be YYYY-MM-DD", ErrReclassInvalid)
	}
	var dp *string
	if date != "" {
		dp = &date
	}
	_, dest, _, err := derive(ctx, store.PlanItem{OriginalName: e.OriginalName, Department: &dept, DocumentType: &typ, DocumentDate: dp})
	if err != nil {
		return store.LedgerEntry{}, fmt.Errorf("%w: %v", ErrReclassInvalid, err)
	}
	if dest != e.ArchivePath {
		if err := s.mkdirAll(ctx, parentDir(dest), actor); err != nil {
			return store.LedgerEntry{}, err
		}
		if _, _, err := s.Move(ctx, e.ArchivePath, dest, actor); err != nil {
			return store.LedgerEntry{}, err
		}
	}
	if err := s.Store.LedgerSetClassification(ctx, id, dept, typ, date); err != nil {
		return store.LedgerEntry{}, err
	}
	s.Store.Audit(ctx, actor, "ledger.reclassify", id, fmt.Sprintf("修订已归档文件：部门=%s 类型=%s 日期=%s → %s", dept, typ, date, dest))
	return s.Store.LedgerEntryByID(ctx, id)
}

func parentDir(rel string) string {
	if i := strings.LastIndex(rel, "/"); i >= 0 {
		return rel[:i]
	}
	return ""
}

// mkdirAll creates the folders of rel one level at a time through the safe file manager (existing ones are fine).
func (s *Service) mkdirAll(ctx context.Context, rel, actor string) error {
	if rel == "" {
		return nil
	}
	cur := ""
	for _, seg := range strings.Split(rel, "/") {
		if cur == "" {
			cur = seg
		} else {
			cur += "/" + seg
		}
		if _, err := s.FS.Mkdir(cur); err != nil && !errors.Is(err, fsman.ErrExists) {
			return err
		}
	}
	s.Store.Audit(ctx, actor, "fs.mkdir", rel, "修订归档时按需新建文件夹")
	return nil
}
