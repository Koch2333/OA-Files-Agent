package store

import (
	"context"
	"time"

	"oa-agent/backend/internal/sqldb"
)

// LedgerHash is what the ledger knows about the content of an archived file: the SHA-256 recorded when it was
// archived and the size of that content (file_records.size_bytes; archiving copies bytes verbatim).
type LedgerHash struct {
	LedgerID   string    `json:"ledger_id"`
	Path       string    `json:"path"`
	SHA256     string    `json:"sha256"`
	SizeBytes  int64     `json:"size_bytes"`
	ExecutedAt time.Time `json:"executed_at"`
}

const ledgerHashCols = `l.id::text, l.archive_path, l.sha256, f.size_bytes, l.executed_at`

func scanLedgerHashes(rows sqldb.Rows) ([]LedgerHash, error) {
	defer rows.Close()
	var out []LedgerHash
	for rows.Next() {
		var h LedgerHash
		if err := rows.Scan(&h.LedgerID, &h.Path, &h.SHA256, &h.SizeBytes, &h.ExecutedAt); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// LedgerHashes returns the archived ledger rows whose archive_path is in paths (key: path).
func (s *Store) LedgerHashes(ctx context.Context, paths []string) (map[string]LedgerHash, error) {
	out := map[string]LedgerHash{}
	for _, part := range chunks(paths) {
		rows, err := s.Pool.Query(ctx, `SELECT `+ledgerHashCols+` FROM ledger_entries l JOIN file_records f ON f.id=l.file_id WHERE l.status='archived' AND l.archive_path IN (`+placeholders(1, len(part))+`)`, toAny(part)...)
		if err != nil {
			return nil, err
		}
		hs, err := scanLedgerHashes(rows)
		if err != nil {
			return nil, err
		}
		for _, h := range hs {
			out[h.Path] = h
		}
	}
	return out, nil
}

// LedgerHashesUnder returns every archived ledger row below the folder rel ("" = the whole archive).
func (s *Store) LedgerHashesUnder(ctx context.Context, rel string) ([]LedgerHash, error) {
	var rows sqldb.Rows
	var err error
	if rel == "" {
		rows, err = s.Pool.Query(ctx, `SELECT `+ledgerHashCols+` FROM ledger_entries l JOIN file_records f ON f.id=l.file_id WHERE l.status='archived' ORDER BY l.archive_path`)
	} else {
		rows, err = s.Pool.Query(ctx, `SELECT `+ledgerHashCols+` FROM ledger_entries l JOIN file_records f ON f.id=l.file_id WHERE l.status='archived' AND left(l.archive_path, $1) = $2 ORDER BY l.archive_path`, len([]rune(rel))+1, rel+"/")
	}
	if err != nil {
		return nil, err
	}
	return scanLedgerHashes(rows)
}

// LedgerByHash lists archived rows with the given content hash (index ledger_sha_idx; no disk scan).
func (s *Store) LedgerByHash(ctx context.Context, sha string, limit int) ([]LedgerHash, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+ledgerHashCols+` FROM ledger_entries l JOIN file_records f ON f.id=l.file_id WHERE l.sha256=$1 AND l.status='archived' ORDER BY l.executed_at DESC LIMIT $2`, sha, limit)
	if err != nil {
		return nil, err
	}
	return scanLedgerHashes(rows)
}

// LedgerRemovedByTrash returns the rows a trash item carries (status 'removed'), used to find where a
// restore would duplicate content.
func (s *Store) LedgerRemovedByTrash(ctx context.Context, trashID string) ([]LedgerHash, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+ledgerHashCols+` FROM ledger_entries l JOIN file_records f ON f.id=l.file_id WHERE l.status='removed' AND l.removed_trash_id=$1::uuid`, trashID)
	if err != nil {
		return nil, err
	}
	return scanLedgerHashes(rows)
}
