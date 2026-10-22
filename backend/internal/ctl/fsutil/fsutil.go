// Package fsutil holds the small file helpers every mutating oa-agentctl command relies on.
package fsutil

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Now is replaceable so backup names are deterministic in tests.
var Now = time.Now

// WriteAtomic writes via a temp file in the same directory and renames it, so a crash never
// leaves a half-written .env or compose file behind.
func WriteAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	cleanup := func() { _ = os.Remove(name) }
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Chmod(name, mode); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(name, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// Backup copies path to "<path>.bak-<timestamp>" (same mode) and prunes older backups beyond keep.
// A missing source is not an error: it returns "".
func Backup(path string, keep int) (string, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	mode := os.FileMode(0o600)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	dst := fmt.Sprintf("%s.bak-%s", path, Now().Format("20060102-150405"))
	for i := 1; exists(dst); i++ {
		dst = fmt.Sprintf("%s.bak-%s-%d", path, Now().Format("20060102-150405"), i)
	}
	if err := WriteAtomic(dst, b, mode); err != nil {
		return "", err
	}
	prune(path, keep)
	return dst, nil
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

func prune(path string, keep int) {
	if keep <= 0 {
		return
	}
	matches, _ := filepath.Glob(path + ".bak-*")
	sort.Strings(matches)
	for len(matches) > keep {
		_ = os.Remove(matches[0])
		matches = matches[1:]
	}
}

// Restore puts a backup back in place (atomic). An empty backup path removes path, which is the
// right undo when the file did not exist before.
func Restore(backup, path string, mode os.FileMode) error {
	if backup == "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	b, err := os.ReadFile(backup)
	if err != nil {
		return err
	}
	return WriteAtomic(path, b, mode)
}

// Within reports whether child equals parent or lies below it (both must already be clean).
func Within(parent, child string) bool {
	if parent == child {
		return true
	}
	if parent == "/" {
		return strings.HasPrefix(child, "/")
	}
	return strings.HasPrefix(child, parent+"/")
}
