// Package fsman is the file manager over the archive root: browse, preview, create folders,
// rename/move (never overwriting), and a recoverable trash. Every path is a validated relative path that
// is resolved symlink-safely below the root; hidden internal names (.staging, .journal, .trash) are unreachable.
package fsman

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"oa-agent/backend/internal/archive"
)

var (
	ErrInvalid  = errors.New("invalid path")
	ErrNotFound = errors.New("not found")
	ErrExists   = errors.New("already exists")
	ErrOutside  = errors.New("path escapes the archive root")
	ErrConflict = errors.New("conflicting operation")
)

type Entry struct {
	Name        string    `json:"name"`
	Path        string    `json:"path"`
	IsDir       bool      `json:"is_dir"`
	SizeBytes   int64     `json:"size_bytes"`
	ModifiedAt  time.Time `json:"modified_at"`
	MIMEType    string    `json:"mime_type,omitempty"`
	Previewable bool      `json:"previewable"`
}

type TrashItem struct {
	ID           string    `json:"id"`
	OriginalPath string    `json:"original_path"`
	Name         string    `json:"name"`
	IsDir        bool      `json:"is_dir"`
	SizeBytes    int64     `json:"size_bytes"`
	DeletedAt    time.Time `json:"deleted_at"`
}

// Manager serialises mutating operations; all state lives on disk.
type Manager struct {
	Root string
	mu   sync.Mutex
}

func (m *Manager) realRoot() (string, error) {
	if m.Root == "" {
		return "", errors.New("archive root not configured")
	}
	if err := os.MkdirAll(m.Root, 0o700); err != nil {
		return "", err
	}
	r, err := filepath.EvalSymlinks(m.Root)
	if err != nil {
		return "", err
	}
	return filepath.Abs(r)
}

// Check validates a user-supplied relative path ("" is the root when allowRoot).
func Check(rel string, allowRoot bool) error {
	if rel == "" {
		if allowRoot {
			return nil
		}
		return fmt.Errorf("%w: empty", ErrInvalid)
	}
	if err := archive.ValidateRelative(rel); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	for _, c := range strings.Split(rel, "/") {
		if strings.HasPrefix(c, ".") {
			return fmt.Errorf("%w: names starting with '.' are reserved", ErrInvalid)
		}
	}
	return nil
}

func within(root, p string) bool {
	r, err := filepath.Rel(root, p)
	return err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) && !filepath.IsAbs(r)
}

// resolve returns the absolute path of rel. With mustExist the target must exist and not be a symlink;
// otherwise only its parent must exist (the target itself may or may not).
func (m *Manager) resolve(rel string, allowRoot, mustExist bool) (root, abs string, err error) {
	if err = Check(rel, allowRoot); err != nil {
		return
	}
	if root, err = m.realRoot(); err != nil {
		return
	}
	abs = filepath.Join(root, filepath.FromSlash(rel))
	if rel == "" {
		return
	}
	if mustExist {
		fi, lerr := os.Lstat(abs)
		if lerr != nil {
			err = ErrNotFound
			return
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			err = fmt.Errorf("%w: symbolic links are not managed", ErrInvalid)
			return
		}
	}
	parent := filepath.Dir(abs)
	real, rerr := filepath.EvalSymlinks(parent)
	if rerr != nil {
		err = ErrNotFound
		return
	}
	if !within(root, real) {
		err = ErrOutside
		return
	}
	abs = filepath.Join(real, filepath.Base(abs))
	return
}

// Abs returns the absolute on-disk path of an existing entry (for “reveal in Explorer”).
func (m *Manager) Abs(rel string) (string, error) {
	_, abs, err := m.resolve(rel, true, true)
	return abs, err
}

func previewable(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".pdf", ".png", ".jpg", ".jpeg":
		return true
	}
	return false
}

func entryOf(rel string, fi os.FileInfo) Entry {
	e := Entry{Name: fi.Name(), Path: rel, IsDir: fi.IsDir(), ModifiedAt: fi.ModTime()}
	if !fi.IsDir() {
		e.SizeBytes = fi.Size()
		e.MIMEType = mime.TypeByExtension(strings.ToLower(path.Ext(fi.Name())))
		e.Previewable = previewable(fi.Name())
	}
	return e
}

func join(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

// List returns the visible entries of a folder, folders first, then by name.
func (m *Manager) List(rel string) ([]Entry, error) {
	_, abs, err := m.resolve(rel, true, true)
	if err != nil {
		return nil, err
	}
	if fi, serr := os.Stat(abs); serr != nil {
		return nil, ErrNotFound
	} else if !fi.IsDir() {
		return nil, fmt.Errorf("%w: not a folder", ErrInvalid)
	}
	des, err := os.ReadDir(abs)
	if err != nil {
		return nil, fmt.Errorf("%w: cannot read folder", ErrInvalid)
	}
	out := make([]Entry, 0, len(des))
	for _, de := range des {
		if strings.HasPrefix(de.Name(), ".") || de.Type()&fs.ModeSymlink != 0 {
			continue
		}
		fi, err := de.Info()
		if err != nil {
			continue
		}
		out = append(out, entryOf(join(rel, de.Name()), fi))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

// Stat returns the entry for rel.
func (m *Manager) Stat(rel string) (Entry, error) {
	_, abs, err := m.resolve(rel, false, true)
	if err != nil {
		return Entry{}, err
	}
	fi, err := os.Lstat(abs)
	if err != nil {
		return Entry{}, ErrNotFound
	}
	return entryOf(rel, fi), nil
}

// Open opens a regular file for reading.
func (m *Manager) Open(rel string) (*os.File, os.FileInfo, error) {
	_, abs, err := m.resolve(rel, false, true)
	if err != nil {
		return nil, nil, err
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, nil, ErrNotFound
	}
	fi, err := f.Stat()
	if err != nil || fi.IsDir() {
		f.Close()
		return nil, nil, fmt.Errorf("%w: not a file", ErrInvalid)
	}
	return f, fi, nil
}

func (m *Manager) Mkdir(rel string) (Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, abs, err := m.resolve(rel, false, false)
	if err != nil {
		return Entry{}, err
	}
	if err = os.Mkdir(abs, 0o755); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return Entry{}, ErrExists
		}
		return Entry{}, ErrNotFound // parent missing
	}
	fi, err := os.Lstat(abs)
	if err != nil {
		return Entry{}, err
	}
	return entryOf(rel, fi), nil
}

// Move renames or moves from→to (both full relative paths). Existing targets are never replaced.
func (m *Manager) Move(from, to string) (Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if from == to {
		return Entry{}, fmt.Errorf("%w: source and target are the same", ErrInvalid)
	}
	_, src, err := m.resolve(from, false, true)
	if err != nil {
		return Entry{}, err
	}
	_, dst, err := m.resolve(to, false, false)
	if err != nil {
		return Entry{}, err
	}
	fi, err := os.Lstat(src)
	if err != nil {
		return Entry{}, ErrNotFound
	}
	if fi.IsDir() && strings.HasPrefix(to+"/", from+"/") {
		return Entry{}, fmt.Errorf("%w: cannot move a folder into itself", ErrInvalid)
	}
	if pfi, perr := os.Stat(filepath.Dir(dst)); perr != nil || !pfi.IsDir() {
		return Entry{}, ErrNotFound
	}
	if _, err = os.Lstat(dst); err == nil {
		return Entry{}, ErrExists
	}
	if fi.IsDir() {
		err = os.Rename(src, dst) // fails if dst appeared meanwhile (non-empty dir) on every platform
	} else if err = os.Link(src, dst); err == nil { // link+remove never replaces an existing file
		err = os.Remove(src)
	} else if _, serr := os.Lstat(dst); serr != nil {
		err = os.Rename(src, dst) // filesystem without hard links; existence was checked under the lock
	}
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return Entry{}, ErrExists
		}
		return Entry{}, fmt.Errorf("move failed: %w", err)
	}
	nfi, err := os.Lstat(dst)
	if err != nil {
		return Entry{}, err
	}
	return entryOf(to, nfi), nil
}

// ---- trash ----

const trashDir = ".trash"

type trashMeta struct {
	TrashItem
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func dirSize(p string) int64 {
	var n int64
	_ = filepath.WalkDir(p, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if fi, e := d.Info(); e == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

// Trash moves an entry into <root>/.trash/<id>/item with a sidecar record; Restore reverses it.
func (m *Manager) Trash(rel string) (TrashItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	root, src, err := m.resolve(rel, false, true)
	if err != nil {
		return TrashItem{}, err
	}
	fi, err := os.Lstat(src)
	if err != nil {
		return TrashItem{}, ErrNotFound
	}
	it := TrashItem{ID: newID(), OriginalPath: rel, Name: fi.Name(), IsDir: fi.IsDir(), DeletedAt: time.Now().UTC()}
	if fi.IsDir() {
		it.SizeBytes = dirSize(src)
	} else {
		it.SizeBytes = fi.Size()
	}
	dir := filepath.Join(root, trashDir, it.ID)
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return TrashItem{}, err
	}
	meta, _ := json.Marshal(trashMeta{it})
	if err = os.WriteFile(filepath.Join(dir, "meta.json"), meta, 0o600); err != nil {
		os.RemoveAll(dir)
		return TrashItem{}, err
	}
	if err = os.Rename(src, filepath.Join(dir, "item")); err != nil {
		os.RemoveAll(dir)
		return TrashItem{}, fmt.Errorf("move to trash failed: %w", err)
	}
	return it, nil
}

func (m *Manager) ListTrash() ([]TrashItem, error) {
	root, err := m.realRoot()
	if err != nil {
		return nil, err
	}
	des, err := os.ReadDir(filepath.Join(root, trashDir))
	if errors.Is(err, fs.ErrNotExist) {
		return []TrashItem{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []TrashItem{}
	for _, de := range des {
		raw, err := os.ReadFile(filepath.Join(root, trashDir, de.Name(), "meta.json"))
		if err != nil {
			continue
		}
		var tm trashMeta
		if json.Unmarshal(raw, &tm) == nil && tm.ID == de.Name() {
			out = append(out, tm.TrashItem)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DeletedAt.After(out[j].DeletedAt) })
	return out, nil
}

func validTrashID(id string) bool {
	if len(id) != 36 {
		return false
	}
	_, err := hex.DecodeString(strings.ReplaceAll(id, "-", ""))
	return err == nil
}

// Restore puts a trashed entry back (recreating missing parents). If the original name is taken a
// numbered suffix is added. It returns the final relative path.
func (m *Manager) Restore(id string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !validTrashID(id) {
		return "", ErrNotFound
	}
	root, err := m.realRoot()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, trashDir, id)
	raw, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return "", ErrNotFound
	}
	var tm trashMeta
	if err = json.Unmarshal(raw, &tm); err != nil || Check(tm.OriginalPath, false) != nil {
		return "", fmt.Errorf("%w: corrupt trash record", ErrInvalid)
	}
	parentRel := path.Dir(tm.OriginalPath)
	if parentRel == "." {
		parentRel = ""
	}
	if parentRel != "" {
		// recreate missing parents one level at a time, each validated and symlink-safe
		built := ""
		for _, c := range strings.Split(parentRel, "/") {
			built = join(built, c)
			_, abs, rerr := m.resolve(built, false, false)
			if rerr != nil {
				return "", rerr
			}
			if mkerr := os.Mkdir(abs, 0o755); mkerr != nil && !errors.Is(mkerr, fs.ErrExist) {
				return "", mkerr
			}
		}
	}
	base := path.Base(tm.OriginalPath)
	stem, ext := base, ""
	if !tm.IsDir {
		ext = path.Ext(base)
		stem = strings.TrimSuffix(base, ext)
	}
	final := tm.OriginalPath
	for n := 1; ; n++ {
		_, abs, rerr := m.resolve(final, false, false)
		if rerr != nil {
			return "", rerr
		}
		if _, serr := os.Lstat(abs); errors.Is(serr, fs.ErrNotExist) {
			break
		}
		suffix := " (已恢复)"
		if n > 1 {
			suffix = fmt.Sprintf(" (已恢复 %d)", n)
		}
		final = join(strings.TrimSuffix(parentRel, "."), stem+suffix+ext)
		if n > 1000 {
			return "", ErrConflict
		}
	}
	_, dst, err := m.resolve(final, false, false)
	if err != nil {
		return "", err
	}
	if err = os.Rename(filepath.Join(dir, "item"), dst); err != nil {
		return "", fmt.Errorf("restore failed: %w", err)
	}
	_ = os.RemoveAll(dir)
	return final, nil
}

// TrashPath returns the relative path an item would have in the trash (for ledger bookkeeping).
func (m *Manager) TrashItem(id string) (TrashItem, error) {
	items, err := m.ListTrash()
	if err != nil {
		return TrashItem{}, err
	}
	for _, it := range items {
		if it.ID == id {
			return it, nil
		}
	}
	return TrashItem{}, ErrNotFound
}
