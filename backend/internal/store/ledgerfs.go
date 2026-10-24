package store

import (
	"context"
	"strconv"
	"strings"

	"oa-agent/backend/internal/sqldb"
)

// LedgerRef is the ledger information shown next to a file in the file manager.
type LedgerRef struct {
	ID           string `json:"id"`
	OriginalName string `json:"original_name"`
	Department   string `json:"department"`
	DocumentType string `json:"document_type"`
	DocumentDate string `json:"document_date"`
}

// inChunk is how many values one IN (...) list carries (SQL Server allows 2100 parameters per statement).
const inChunk = 500

// placeholders renders "$from,$from+1,..." for n values.
func placeholders(from, n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString("$" + strconv.Itoa(from+i))
	}
	return b.String()
}

func chunks(v []string) [][]string {
	var out [][]string
	for len(v) > inChunk {
		out = append(out, v[:inChunk])
		v = v[inChunk:]
	}
	if len(v) > 0 {
		out = append(out, v)
	}
	return out
}

func toAny(v []string) []any {
	out := make([]any, len(v))
	for i, s := range v {
		out[i] = s
	}
	return out
}

// LedgerByPaths returns the archived ledger rows whose archive_path is in paths.
func (s *Store) LedgerByPaths(ctx context.Context, paths []string) (map[string]LedgerRef, error) {
	out := map[string]LedgerRef{}
	for _, part := range chunks(paths) {
		rows, err := s.Pool.Query(ctx, `SELECT archive_path, id::text, original_name, department, document_type, document_date FROM ledger_entries WHERE status='archived' AND archive_path IN (`+placeholders(1, len(part))+`)`, toAny(part)...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var p string
			var r LedgerRef
			if err := rows.Scan(&p, &r.ID, &r.OriginalName, &r.Department, &r.DocumentType, &r.DocumentDate); err != nil {
				rows.Close()
				return nil, err
			}
			out[p] = r
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// LedgerMovePath follows a rename/move: a file keeps its row but gets the new path and canonical name;
// a folder rewrites the path prefix of every archived row below it.
func (s *Store) LedgerMovePath(ctx context.Context, oldRel, newRel string, isDir bool) (int64, error) {
	if !isDir {
		name := newRel[strings.LastIndex(newRel, "/")+1:]
		t, err := s.Pool.Exec(ctx, `UPDATE ledger_entries SET archive_path=$2, canonical_name=$3 WHERE archive_path=$1 AND status='archived'`, oldRel, newRel, name)
		return t.RowsAffected(), err
	}
	t, err := s.Pool.Exec(ctx, `UPDATE ledger_entries SET archive_path=concat($1::text, substr(archive_path, $2, 100000)) WHERE status='archived' AND left(archive_path, $2) = $3`,
		newRel, len([]rune(oldRel))+1, oldRel+"/")
	return t.RowsAffected(), err
}

type ledgerFile struct{ ledgerID, fileID string }

func scanLedgerFiles(rows sqldb.Rows) ([]ledgerFile, error) {
	defer rows.Close()
	var out []ledgerFile
	for rows.Next() {
		var l ledgerFile
		if err := rows.Scan(&l.ledgerID, &l.fileID); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func setFileStatus(ctx context.Context, tx sqldb.Tx, status string, ids []string) error {
	for _, part := range chunks(ids) {
		if _, err := tx.Exec(ctx, `UPDATE file_records SET archive_status=$1 WHERE id IN (`+placeholders(2, len(part))+`)`, append([]any{status}, toAny(part)...)...); err != nil {
			return err
		}
	}
	return nil
}

func fileIDs(ls []ledgerFile) []string {
	ids := make([]string, len(ls))
	for i, l := range ls {
		ids[i] = l.fileID
	}
	return ids
}

// LedgerRemove marks the archived rows at (or below) rel as removed into the given trash item and puts
// their files back to 'pending' so the library does not claim they are archived.
func (s *Store) LedgerRemove(ctx context.Context, rel string, isDir bool, trashID string) (int64, error) {
	var n int64
	err := s.Pool.InTx(ctx, func(tx sqldb.Tx) error {
		var rows sqldb.Rows
		var err error
		if isDir {
			rows, err = tx.Query(ctx, `SELECT id::text, file_id::text FROM ledger_entries WHERE status='archived' AND left(archive_path, $1) = $2 FOR UPDATE`, len([]rune(rel))+1, rel+"/")
		} else {
			rows, err = tx.Query(ctx, `SELECT id::text, file_id::text FROM ledger_entries WHERE status='archived' AND archive_path=$1 FOR UPDATE`, rel)
		}
		if err != nil {
			return err
		}
		ls, err := scanLedgerFiles(rows)
		if err != nil {
			return err
		}
		for _, part := range chunks(func() []string {
			ids := make([]string, len(ls))
			for i, l := range ls {
				ids[i] = l.ledgerID
			}
			return ids
		}()) {
			if _, err = tx.Exec(ctx, `UPDATE ledger_entries SET status='removed', removed_trash_id=$1::uuid WHERE id IN (`+placeholders(2, len(part))+`)`, append([]any{trashID}, toAny(part)...)...); err != nil {
				return err
			}
		}
		if err = setFileStatus(ctx, tx, "pending", fileIDs(ls)); err != nil {
			return err
		}
		n = int64(len(ls))
		return nil
	})
	return n, err
}

// LedgerRestore reverses LedgerRemove for one trash item; paths are rewritten when the restore got a new name.
func (s *Store) LedgerRestore(ctx context.Context, trashID, origRel, newRel string, isDir bool) (int64, error) {
	var n int64
	err := s.Pool.InTx(ctx, func(tx sqldb.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text, file_id::text FROM ledger_entries WHERE status='removed' AND removed_trash_id=$1::uuid FOR UPDATE`, trashID)
		if err != nil {
			return err
		}
		ls, err := scanLedgerFiles(rows)
		if err != nil {
			return err
		}
		if len(ls) == 0 {
			n = 0
			return nil
		}
		if isDir {
			_, err = tx.Exec(ctx, `UPDATE ledger_entries SET status='archived', removed_trash_id=NULL, archive_path=concat($2::text, substr(archive_path, $3, 100000)) WHERE status='removed' AND removed_trash_id=$1::uuid`,
				trashID, newRel, len([]rune(origRel))+1)
		} else {
			name := newRel[strings.LastIndex(newRel, "/")+1:]
			_, err = tx.Exec(ctx, `UPDATE ledger_entries SET status='archived', removed_trash_id=NULL, archive_path=$2, canonical_name=$3 WHERE status='removed' AND removed_trash_id=$1::uuid`,
				trashID, newRel, name)
		}
		if err != nil {
			return err
		}
		if err = setFileStatus(ctx, tx, "archived", fileIDs(ls)); err != nil {
			return err
		}
		n = int64(len(ls))
		return nil
	})
	return n, err
}

// OperationLedger lists the ledger state of the files an archive operation published
// (used to refuse a rollback after the files were moved or removed in the file manager).
func (s *Store) OperationLedger(ctx context.Context, opID string) (map[string]struct{ Path, Status string }, error) {
	rows, err := s.Pool.Query(ctx, `SELECT file_id::text, archive_path, status FROM ledger_entries WHERE operation_id=$1`, opID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]struct{ Path, Status string }{}
	for rows.Next() {
		var f string
		var v struct{ Path, Status string }
		if err := rows.Scan(&f, &v.Path, &v.Status); err != nil {
			return nil, err
		}
		out[f] = v
	}
	return out, rows.Err()
}
