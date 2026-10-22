package store

import (
	"context"
	"strings"
)

// LedgerRef is the ledger information shown next to a file in the file manager.
type LedgerRef struct {
	ID           string `json:"id"`
	OriginalName string `json:"original_name"`
	Department   string `json:"department"`
	DocumentType string `json:"document_type"`
	DocumentDate string `json:"document_date"`
}

// LedgerByPaths returns the archived ledger rows whose archive_path is in paths.
func (s *Store) LedgerByPaths(ctx context.Context, paths []string) (map[string]LedgerRef, error) {
	out := map[string]LedgerRef{}
	if len(paths) == 0 {
		return out, nil
	}
	rows, err := s.Pool.Query(ctx, `SELECT archive_path, id::text, original_name, department, document_type, document_date FROM ledger_entries WHERE status='archived' AND archive_path = ANY($1)`, paths)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		var r LedgerRef
		if err := rows.Scan(&p, &r.ID, &r.OriginalName, &r.Department, &r.DocumentType, &r.DocumentDate); err != nil {
			return nil, err
		}
		out[p] = r
	}
	return out, rows.Err()
}

// LedgerMovePath follows a rename/move: a file keeps its row but gets the new path and canonical name;
// a folder rewrites the path prefix of every archived row below it.
func (s *Store) LedgerMovePath(ctx context.Context, oldRel, newRel string, isDir bool) (int64, error) {
	if !isDir {
		name := newRel[strings.LastIndex(newRel, "/")+1:]
		t, err := s.Pool.Exec(ctx, `UPDATE ledger_entries SET archive_path=$2, canonical_name=$3 WHERE archive_path=$1 AND status='archived'`, oldRel, newRel, name)
		return t.RowsAffected(), err
	}
	t, err := s.Pool.Exec(ctx, `UPDATE ledger_entries SET archive_path=$1 || substr(archive_path, $2) WHERE status='archived' AND left(archive_path, $2) = $3`,
		newRel, len([]rune(oldRel))+1, oldRel+"/")
	return t.RowsAffected(), err
}

// LedgerRemove marks the archived rows at (or below) rel as removed into the given trash item and puts
// their files back to 'pending' so the library does not claim they are archived.
func (s *Store) LedgerRemove(ctx context.Context, rel string, isDir bool, trashID string) (int64, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(context.Background())
	var q string
	var args []any
	if isDir {
		q = `UPDATE ledger_entries SET status='removed', removed_trash_id=$1::uuid WHERE status='archived' AND left(archive_path, $2) = $3 RETURNING file_id`
		args = []any{trashID, len([]rune(rel)) + 1, rel + "/"}
	} else {
		q = `UPDATE ledger_entries SET status='removed', removed_trash_id=$2::uuid WHERE status='archived' AND archive_path=$1 RETURNING file_id`
		args = []any{rel, trashID}
	}
	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if len(ids) > 0 {
		if _, err = tx.Exec(ctx, `UPDATE file_records SET archive_status='pending' WHERE id = ANY($1::uuid[])`, ids); err != nil {
			return 0, err
		}
	}
	return int64(len(ids)), tx.Commit(ctx)
}

// LedgerRestore reverses LedgerRemove for one trash item; paths are rewritten when the restore got a new name.
func (s *Store) LedgerRestore(ctx context.Context, trashID, origRel, newRel string, isDir bool) (int64, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(context.Background())
	var rows interface {
		Next() bool
		Scan(...any) error
		Close()
		Err() error
	}
	if isDir {
		rows, err = tx.Query(ctx, `UPDATE ledger_entries SET status='archived', removed_trash_id=NULL, archive_path=$2 || substr(archive_path, $3) WHERE status='removed' AND removed_trash_id=$1::uuid RETURNING file_id::text`,
			trashID, newRel, len([]rune(origRel))+1)
	} else {
		name := newRel[strings.LastIndex(newRel, "/")+1:]
		rows, err = tx.Query(ctx, `UPDATE ledger_entries SET status='archived', removed_trash_id=NULL, archive_path=$2, canonical_name=$3 WHERE status='removed' AND removed_trash_id=$1::uuid RETURNING file_id::text`,
			trashID, newRel, name)
	}
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return 0, err
	}
	if len(ids) > 0 {
		if _, err = tx.Exec(ctx, `UPDATE file_records SET archive_status='archived' WHERE id = ANY($1::uuid[])`, ids); err != nil {
			return 0, err
		}
	}
	return int64(len(ids)), tx.Commit(ctx)
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
