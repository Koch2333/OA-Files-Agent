package fileops

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"oa-agent/backend/internal/fsman"
	"oa-agent/backend/internal/store"
)

// Hash statuses shown in the file manager.
const (
	HashNotTask    = "not_task"   // not tracked by the ledger (and every folder)
	HashUnverified = "unverified" // task-related, the ledger hash is known but the file has not been re-hashed yet
	HashVerified   = "verified"   // re-hashed and equal to the ledger
	HashMismatch   = "mismatch"   // re-hashed (or sized) differently: modified outside the system
)

// ledgerSlack tolerates clock/mtime granularity when deciding whether the ledger hash is still trustworthy.
const ledgerSlack = 2 * time.Second

type hashEntry struct {
	size int64
	mod  int64
	sha  string
}

// hashCache remembers SHA-256 digests computed from disk, keyed by path and valid only while size and mtime
// are unchanged. It keeps the listing lazy (no hashing there) while still showing verified/mismatch after a
// verification or a move.
type hashCache struct {
	mu sync.Mutex
	m  map[string]hashEntry
}

const hashCacheMax = 100000

func (c *hashCache) get(rel string, size int64, mod time.Time) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[rel]
	if !ok || e.size != size || e.mod != mod.UnixNano() {
		return "", false
	}
	return e.sha, true
}

func (c *hashCache) put(rel string, size int64, mod time.Time, sha string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil || len(c.m) >= hashCacheMax {
		c.m = map[string]hashEntry{}
	}
	c.m[rel] = hashEntry{size, mod.UnixNano(), sha}
}

func (c *hashCache) drop(rel string) {
	c.mu.Lock()
	delete(c.m, rel)
	c.mu.Unlock()
}

// ledgerTrusted reports whether the ledger hash may stand in for the disk content: same size and not modified
// after the ledger row was written.
func ledgerTrusted(row *store.LedgerHash, size int64, mod time.Time) bool {
	return row != nil && row.SHA256 != "" && row.SizeBytes == size && !mod.After(row.ExecutedAt.Add(ledgerSlack))
}

// hashOf returns the content hash of rel for conflict decisions: the ledger hash when it is trustworthy
// (size and mtime agree), else the cached digest, else a streaming computation from disk.
func (s *Service) hashOf(ctx context.Context, rel string, row *store.LedgerHash) (sum string, size int64, mod time.Time, err error) {
	e, err := s.FS.Stat(rel)
	if err != nil {
		return "", 0, time.Time{}, err
	}
	if e.IsDir {
		return "", 0, time.Time{}, fmt.Errorf("%w: not a file", fsman.ErrInvalid)
	}
	if ledgerTrusted(row, e.SizeBytes, e.ModifiedAt) {
		return row.SHA256, e.SizeBytes, e.ModifiedAt, nil
	}
	if sha, ok := s.hc.get(rel, e.SizeBytes, e.ModifiedAt); ok {
		return sha, e.SizeBytes, e.ModifiedAt, nil
	}
	sum, size, mod, err = s.FS.HashFile(ctx, rel)
	if err != nil {
		return "", 0, time.Time{}, err
	}
	s.hc.put(rel, size, mod, sum)
	return sum, size, mod, nil
}

// verifyDisk always reads the file (unless the cache is still valid) and compares with the ledger row.
func (s *Service) verifyDisk(ctx context.Context, row store.LedgerHash, force bool) (FileCheck, error) {
	fc := FileCheck{Path: row.Path, Expected: row.SHA256, LedgerID: row.LedgerID}
	e, err := s.FS.Stat(row.Path)
	if err != nil {
		if errors.Is(err, fsman.ErrNotFound) {
			fc.Status = CheckMissing
			fc.Message = "台账中有记录，但磁盘上找不到该文件"
			return fc, nil
		}
		return fc, err
	}
	if e.IsDir {
		fc.Status = CheckMismatch
		fc.Message = "该位置现在是文件夹"
		return fc, nil
	}
	fc.SizeBytes = e.SizeBytes
	sha, ok := "", false
	if !force {
		sha, ok = s.hc.get(row.Path, e.SizeBytes, e.ModifiedAt)
	}
	if !ok {
		var size int64
		var mod time.Time
		if sha, size, mod, err = s.FS.HashFile(ctx, row.Path); err != nil {
			if errors.Is(err, fsman.ErrNotFound) {
				fc.Status, fc.Message = CheckMissing, "台账中有记录，但磁盘上找不到该文件"
				return fc, nil
			}
			return fc, err
		}
		s.hc.put(row.Path, size, mod, sha)
		fc.SizeBytes = size
	}
	fc.Actual = sha
	if sha == row.SHA256 {
		fc.Status = CheckMatch
	} else {
		fc.Status = CheckMismatch
		fc.Message = "磁盘内容与台账记录的 SHA-256 不一致（文件可能在系统外被修改）"
	}
	return fc, nil
}

// Check statuses of /v1/fs/verify.
const (
	CheckMatch    = "match"
	CheckMismatch = "mismatch"
	CheckMissing  = "missing"
	CheckNotTask  = "not_task"
)

// FileCheck is the verification result of one file.
type FileCheck struct {
	Path      string `json:"path"`
	Status    string `json:"status"`
	Expected  string `json:"expected_sha256,omitempty"`
	Actual    string `json:"actual_sha256,omitempty"`
	SizeBytes int64  `json:"size_bytes"`
	LedgerID  string `json:"ledger_id,omitempty"`
	Message   string `json:"message,omitempty"`
}

// VerifyReport summarises a verification run.
type VerifyReport struct {
	Items     []FileCheck `json:"items"`
	Checked   int         `json:"checked"`
	Match     int         `json:"match"`
	Mismatch  int         `json:"mismatch"`
	Missing   int         `json:"missing"`
	NotTask   int         `json:"not_task"`
	Truncated bool        `json:"truncated"`
}

const verifyMatchSample = 2000

// Verify re-hashes the task-related files below the selected paths (files, or folders: every archived ledger
// row under them) in a streaming fashion and compares with the ledger. It is read-only and honours ctx
// cancellation. Non-task files are only listed as not_task when selected explicitly.
func (s *Service) Verify(ctx context.Context, paths []string, actor string) (VerifyReport, error) {
	rep := VerifyReport{Items: []FileCheck{}}
	seen := map[string]bool{}
	var rows []store.LedgerHash
	var explicitFiles []string
	for _, p := range paths {
		e, err := s.FS.Stat(p)
		if err != nil {
			return rep, err
		}
		if e.IsDir {
			under, err := s.Store.LedgerHashesUnder(ctx, p)
			if err != nil {
				return rep, err
			}
			rows = append(rows, under...)
		} else {
			explicitFiles = append(explicitFiles, p)
		}
	}
	if len(explicitFiles) > 0 {
		m, err := s.Store.LedgerHashes(ctx, explicitFiles)
		if err != nil {
			return rep, err
		}
		for _, p := range explicitFiles {
			if r, ok := m[p]; ok {
				rows = append(rows, r)
			} else if !seen[p] {
				seen[p] = true
				rep.NotTask++
				rep.Items = append(rep.Items, FileCheck{Path: p, Status: CheckNotTask, Message: "不是任务相关文件（台账中没有记录），无需校验"})
			}
		}
	}
	var bad []FileCheck
	for _, r := range rows {
		if seen[r.Path] {
			continue
		}
		seen[r.Path] = true
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		fc, err := s.verifyDisk(ctx, r, true)
		if err != nil {
			return rep, err
		}
		rep.Checked++
		switch fc.Status {
		case CheckMatch:
			rep.Match++
			if rep.Match > verifyMatchSample {
				rep.Truncated = true
				continue
			}
		case CheckMismatch:
			rep.Mismatch++
			bad = append(bad, fc)
		case CheckMissing:
			rep.Missing++
			bad = append(bad, fc)
		}
		rep.Items = append(rep.Items, fc)
	}
	for i, b := range bad {
		if i >= 20 {
			break
		}
		s.Store.Audit(ctx, actor, "fs.hash_mismatch", b.Path, b.Message)
	}
	s.Store.Audit(ctx, actor, "fs.verify", fmt.Sprintf("%d 个路径", len(paths)), fmt.Sprintf("校验任务相关文件 %d 个：一致 %d，不一致 %d，缺失 %d", rep.Checked, rep.Match, rep.Mismatch, rep.Missing))
	return rep, nil
}

// listHashStatus fills the lazy hash fields of a listing from the ledger row and the digest cache.
func (s *Service) listHashStatus(e *ListedEntry, row *store.LedgerHash) {
	e.HashStatus = HashNotTask
	if row == nil || e.IsDir {
		return
	}
	e.TaskRelated = true
	e.SHA256 = row.SHA256
	if sha, ok := s.hc.get(e.Path, e.SizeBytes, e.ModifiedAt); ok {
		if sha == row.SHA256 {
			e.HashStatus = HashVerified
		} else {
			e.HashStatus = HashMismatch
		}
		return
	}
	if e.SizeBytes != row.SizeBytes {
		e.HashStatus = HashMismatch
		return
	}
	e.HashStatus = HashUnverified
}
