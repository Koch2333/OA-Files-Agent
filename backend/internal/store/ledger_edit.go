package store

import (
	"context"
	"errors"
)

// LedgerEntryByID returns one ledger row (any status).
func (s *Store) LedgerEntryByID(ctx context.Context, id string) (LedgerEntry, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+ledgerCols+` FROM ledger_entries WHERE id=$1::uuid`, id)
	if err != nil {
		return LedgerEntry{}, err
	}
	out, err := scanLedger(rows)
	if err != nil {
		return LedgerEntry{}, err
	}
	if len(out) == 0 {
		return LedgerEntry{}, ErrNotFound
	}
	return out[0], nil
}

// LedgerSetClassification stores the corrected department / type / date of an archived entry and mirrors it to the file record, so
// the task view and the ledger never disagree. An empty date clears it.
func (s *Store) LedgerSetClassification(ctx context.Context, id, department, docType, date string) error {
	t, err := s.Pool.Exec(ctx, `UPDATE ledger_entries SET department=$2, document_type=$3, document_date=$4 WHERE id=$1::uuid AND status='archived'`, id, department, docType, date)
	if err != nil {
		return err
	}
	if t.RowsAffected() == 0 {
		return ErrNotFound
	}
	var d *string
	if date != "" {
		d = &date
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE file_records SET detected_department=$2, detected_type=$3, detected_date=$4::date, review_required=false WHERE id=(SELECT file_id FROM ledger_entries WHERE id=$1::uuid)`, id, department, docType, d); err != nil {
		return errors.Join(errors.New("ledger updated but the file record was not"), err)
	}
	return nil
}
