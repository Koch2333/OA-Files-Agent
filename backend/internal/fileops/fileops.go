// Package fileops joins the on-disk file manager (fsman) with the ledger so that renames, moves and
// deletions keep the ledger truthful, and provides the Windows “reveal in Explorer” helper.
package fileops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"

	"oa-agent/backend/internal/fsman"
	"oa-agent/backend/internal/store"
)

type Service struct {
	Store       *store.Store
	FS          *fsman.Manager
	AllowReveal bool // OA_ALLOW_REVEAL != "0"
}

// ListedEntry is an entry plus its ledger record (nil for folders and files outside the ledger).
type ListedEntry struct {
	fsman.Entry
	Ledger *store.LedgerRef `json:"ledger"`
}

type Listing struct {
	Path    string        `json:"path"`
	Parent  *string       `json:"parent"`
	Entries []ListedEntry `json:"entries"`
}

func parentOf(rel string) *string {
	if rel == "" {
		return nil
	}
	p := path.Dir(rel)
	if p == "." {
		p = ""
	}
	return &p
}

// CanReveal reports whether this process may launch Explorer at all (Windows and not disabled).
func (s *Service) CanReveal() bool { return s.AllowReveal && revealSupported() }

func (s *Service) Root() string {
	r, err := s.FS.Abs("")
	if err != nil {
		return ""
	}
	return r
}

func (s *Service) List(ctx context.Context, rel string) (Listing, error) {
	entries, err := s.FS.List(rel)
	if err != nil {
		return Listing{}, err
	}
	paths := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir {
			paths = append(paths, e.Path)
		}
	}
	refs, err := s.Store.LedgerByPaths(ctx, paths)
	if err != nil {
		return Listing{}, err
	}
	out := Listing{Path: rel, Parent: parentOf(rel), Entries: make([]ListedEntry, 0, len(entries))}
	for _, e := range entries {
		le := ListedEntry{Entry: e}
		if r, ok := refs[e.Path]; ok {
			r := r
			le.Ledger = &r
		}
		out.Entries = append(out.Entries, le)
	}
	return out, nil
}

func (s *Service) Content(rel string) (*os.File, os.FileInfo, error) { return s.FS.Open(rel) }

func (s *Service) Mkdir(ctx context.Context, rel, actor string) (ListedEntry, error) {
	e, err := s.FS.Mkdir(rel)
	if err != nil {
		return ListedEntry{}, err
	}
	s.Store.Audit(ctx, actor, "fs.mkdir", rel, "新建文件夹")
	return ListedEntry{Entry: e}, nil
}

// Move renames/moves on disk first, then follows with the ledger. If the ledger update fails the move is
// undone so disk and ledger never disagree.
func (s *Service) Move(ctx context.Context, from, to, actor string) (ListedEntry, int64, error) {
	e, err := s.FS.Move(from, to)
	if err != nil {
		return ListedEntry{}, 0, err
	}
	n, err := s.Store.LedgerMovePath(ctx, from, to, e.IsDir)
	if err != nil {
		if _, uerr := s.FS.Move(to, from); uerr != nil {
			return ListedEntry{}, 0, fmt.Errorf("ledger update failed and the move could not be undone: %v / %v", err, uerr)
		}
		return ListedEntry{}, 0, fmt.Errorf("ledger update failed (move undone): %w", err)
	}
	s.Store.Audit(ctx, actor, "fs.move", from, fmt.Sprintf("移动/重命名为 %s（同步台账 %d 条）", to, n))
	le := ListedEntry{Entry: e}
	if !e.IsDir {
		if refs, rerr := s.Store.LedgerByPaths(ctx, []string{to}); rerr == nil {
			if r, ok := refs[to]; ok {
				le.Ledger = &r
			}
		}
	}
	return le, n, nil
}

func (s *Service) Trash(ctx context.Context, rel, actor string) (fsman.TrashItem, int64, error) {
	it, err := s.FS.Trash(rel)
	if err != nil {
		return fsman.TrashItem{}, 0, err
	}
	n, err := s.Store.LedgerRemove(ctx, rel, it.IsDir, it.ID)
	if err != nil {
		if _, rerr := s.FS.Restore(it.ID); rerr != nil {
			return fsman.TrashItem{}, 0, fmt.Errorf("ledger update failed and the deletion could not be undone: %v / %v", err, rerr)
		}
		return fsman.TrashItem{}, 0, fmt.Errorf("ledger update failed (deletion undone): %w", err)
	}
	s.Store.Audit(ctx, actor, "fs.trash", rel, fmt.Sprintf("删除到回收站（台账标记移除 %d 条）", n))
	return it, n, nil
}

func (s *Service) ListTrash() ([]fsman.TrashItem, error) { return s.FS.ListTrash() }

func (s *Service) Restore(ctx context.Context, id, actor string) (string, int64, error) {
	it, err := s.FS.TrashItem(id)
	if err != nil {
		return "", 0, err
	}
	final, err := s.FS.Restore(id)
	if err != nil {
		return "", 0, err
	}
	n, err := s.Store.LedgerRestore(ctx, id, it.OriginalPath, final, it.IsDir)
	if err != nil {
		return final, n, fmt.Errorf("restored on disk but the ledger update failed: %w", err)
	}
	s.Store.Audit(ctx, actor, "fs.restore", final, fmt.Sprintf("从回收站恢复（台账恢复 %d 条）", n))
	return final, n, nil
}

var (
	ErrRevealUnsupported = errors.New("reveal is only available on Windows")
	ErrRevealDisabled    = errors.New("reveal is disabled by configuration")
)

// Reveal opens Explorer on the folder, or on the containing folder with the file selected.
// rel must resolve inside the archive root; the file itself is never opened or executed.
func (s *Service) Reveal(ctx context.Context, rel, actor string) (string, error) {
	if !s.AllowReveal {
		return "", ErrRevealDisabled
	}
	if !revealSupported() {
		return "", ErrRevealUnsupported
	}
	abs, err := s.FS.Abs(rel)
	if err != nil {
		return "", err
	}
	fi, err := os.Lstat(abs)
	if err != nil {
		return "", fsman.ErrNotFound
	}
	if strings.ContainsAny(abs, "\"\x00\r\n") {
		return "", fsman.ErrInvalid
	}
	if err := launchExplorer(abs, !fi.IsDir()); err != nil {
		return "", fmt.Errorf("could not start Explorer: %w", err)
	}
	s.Store.Audit(ctx, actor, "fs.reveal", rel, "在资源管理器中显示")
	return rel, nil
}
