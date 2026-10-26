package fsman

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"oa-agent/backend/internal/archive"
)

// CaseInsensitive says whether names that differ only in case are the same name (Windows and macOS
// filesystems). Tests may flip it.
var CaseInsensitive = runtime.GOOS == "windows" || runtime.GOOS == "darwin"

// NameKey is the comparison key of a file name: Unicode NFC, and lower-cased on case-insensitive filesystems.
func NameKey(name string) string {
	k := archive.NFC(name)
	if CaseInsensitive {
		k = strings.ToLower(k)
	}
	return k
}

// Lookup finds the entry of folder dirRel whose name equals name under NameKey (so "A.PDF" finds "a.pdf" on
// Windows, and an NFD name finds its NFC twin). The returned entry carries the name as stored on disk.
func (m *Manager) Lookup(dirRel, name string) (Entry, bool, error) {
	_, abs, err := m.resolve(dirRel, true, true)
	if err != nil {
		return Entry{}, false, err
	}
	des, err := os.ReadDir(abs)
	if err != nil {
		return Entry{}, false, ErrNotFound
	}
	want := NameKey(name)
	for _, de := range des {
		if strings.HasPrefix(de.Name(), ".") || NameKey(de.Name()) != want {
			continue
		}
		fi, err := os.Lstat(filepath.Join(abs, de.Name()))
		if err != nil {
			continue
		}
		e := entryOf(join(dirRel, de.Name()), fi)
		if fi.Mode()&fs.ModeSymlink != 0 {
			e.IsDir = false
			e.MIMEType = "inode/symlink"
		}
		return e, true, nil
	}
	return Entry{}, false, nil
}

// IsSymlink reports whether rel exists and is a symbolic link (links are never managed, moved or replaced).
func (m *Manager) IsSymlink(rel string) bool {
	_, abs, err := m.resolve(rel, false, false)
	if err != nil {
		return false
	}
	fi, err := os.Lstat(abs)
	return err == nil && fi.Mode()&fs.ModeSymlink != 0
}

// Walk visits everything below the folder rel (not rel itself), folders before their content, in a
// deterministic order. Hidden internal names and symbolic links are skipped. Stops at the first error.
func (m *Manager) Walk(ctx context.Context, rel string, fn func(Entry) error) error {
	list, err := m.List(rel)
	if err != nil {
		return err
	}
	for _, e := range list {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := fn(e); err != nil {
			return err
		}
		if e.IsDir {
			if err := m.Walk(ctx, e.Path, fn); err != nil {
				return err
			}
		}
	}
	return nil
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// CtxReader wraps r so that reads fail once ctx is cancelled (client abort stops streaming work).
func CtxReader(ctx context.Context, r io.Reader) io.Reader { return ctxReader{ctx, r} }

// HashFile streams a regular file through SHA-256 (constant memory, cancellable). It returns the digest and
// the size and modification time observed on the same open handle.
func (m *Manager) HashFile(ctx context.Context, rel string) (sum string, size int64, mod time.Time, err error) {
	f, fi, err := m.Open(rel)
	if err != nil {
		return "", 0, time.Time{}, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, ctxReader{ctx, f}); err != nil {
		return "", 0, time.Time{}, err
	}
	return hex.EncodeToString(h.Sum(nil)), fi.Size(), fi.ModTime(), nil
}

// RemoveEmptyDir removes a folder only if it is empty (never recursive).
func (m *Manager) RemoveEmptyDir(rel string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, abs, err := m.resolve(rel, false, true)
	if err != nil {
		return err
	}
	return os.Remove(abs) // fails on a non-empty folder and on files with a different error; both are fine for callers
}

// EnsureDirs creates the folder rel and its missing parents, one validated level at a time.
func (m *Manager) EnsureDirs(rel string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ensureDirsLocked(rel)
}

func (m *Manager) ensureDirsLocked(rel string) error {
	built := ""
	for _, c := range strings.Split(rel, "/") {
		if c == "" {
			continue
		}
		built = join(built, c)
		_, abs, err := m.resolve(built, false, false)
		if err != nil {
			return err
		}
		if err := os.Mkdir(abs, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		if fi, err := os.Lstat(abs); err != nil || !fi.IsDir() {
			return fmt.Errorf("%w: %s is not a folder", ErrInvalid, built)
		}
	}
	return nil
}

// sameNameDifferentSpelling: from and to are the same entry spelled differently (case or normalisation) in one folder.
func sameEntryRename(from, to string) bool {
	return path.Dir(from) == path.Dir(to) && from != to && NameKey(path.Base(from)) == NameKey(path.Base(to))
}

// renameSpelling renames an entry whose target "exists" only because it is the entry itself (case-only
// rename on a case-insensitive filesystem) through a hidden temporary name.
func renameSpelling(src, dst string) error {
	tmp := filepath.Join(filepath.Dir(src), ".oa-rn-"+newID())
	if err := os.Rename(src, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Rename(tmp, src)
		return err
	}
	return nil
}

// RestoreAs puts a trashed entry back at exactly final (recreating missing parents) and never replaces
// anything: ErrExists when final is occupied.
func (m *Manager) RestoreAs(id, final string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !validTrashID(id) {
		return ErrNotFound
	}
	root, err := m.realRoot()
	if err != nil {
		return err
	}
	dir := filepath.Join(root, trashDir, id)
	raw, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return ErrNotFound
	}
	var tm trashMeta
	if err = json.Unmarshal(raw, &tm); err != nil || Check(tm.OriginalPath, false) != nil {
		return fmt.Errorf("%w: corrupt trash record", ErrInvalid)
	}
	if err = Check(final, false); err != nil {
		return err
	}
	if parent := path.Dir(final); parent != "." {
		if err = m.ensureDirsLocked(parent); err != nil {
			return err
		}
	}
	_, dst, err := m.resolve(final, false, false)
	if err != nil {
		return err
	}
	if _, err = os.Lstat(dst); err == nil {
		return ErrExists
	}
	src := filepath.Join(dir, "item")
	if tm.IsDir {
		err = os.Rename(src, dst)
	} else if err = os.Link(src, dst); err == nil {
		err = os.Remove(src)
	} else if _, serr := os.Lstat(dst); serr != nil {
		err = os.Rename(src, dst)
	}
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return ErrExists
		}
		return fmt.Errorf("restore failed: %w", err)
	}
	_ = os.RemoveAll(dir)
	return nil
}

// HashTrashed streams the content of a trashed file (never a folder) through SHA-256.
func (m *Manager) HashTrashed(ctx context.Context, id string) (string, error) {
	if !validTrashID(id) {
		return "", ErrNotFound
	}
	root, err := m.realRoot()
	if err != nil {
		return "", err
	}
	p := filepath.Join(root, trashDir, id, "item")
	fi, err := os.Lstat(p)
	if err != nil {
		return "", ErrNotFound
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%w: not a file", ErrInvalid)
	}
	f, err := os.Open(p)
	if err != nil {
		return "", ErrNotFound
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, ctxReader{ctx, f}); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
