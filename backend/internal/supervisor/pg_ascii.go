package supervisor

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// isASCII reports whether s contains only 7-bit characters.
func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// initdbShareDir returns the argument for `initdb -L` when the PostgreSQL share directory sits under a path with non-ASCII characters.
//
// initdb pastes that directory into SQL text (COPY ... FROM '<share>/postgres.description'). On Windows the path arrives in the ANSI
// code page (GBK on a Chinese system) while the bootstrap backend reads the text as UTF8, so initdb dies with
// `invalid byte sequence for encoding "UTF8"` for an install directory such as F:\oa文件智能归档. The share files are therefore
// copied (once, a few tens of MB) into a pure-ASCII scratch directory. The returned cleanup removes it; "" means "no -L needed".
func initdbShareDir(l Layout) (dir string, cleanup func(), err error) {
	src := filepath.Join(l.PGDir, "share")
	if isASCII(src) {
		return "", func() {}, nil
	}
	if _, serr := os.Stat(src); serr != nil {
		return "", func() {}, nil // layout without a share directory next to bin: let initdb find its own files
	}
	// the scratch directory itself must be ASCII: %PUBLIC% (C:\Users\Public) and %ProgramData% are on every Windows system
	var base string
	for _, c := range []string{os.Getenv("PUBLIC"), os.Getenv("ProgramData"), os.TempDir()} {
		if c != "" && isASCII(c) {
			base = c
			break
		}
	}
	if base == "" {
		return "", func() {}, nil
	}
	dst, err := os.MkdirTemp(base, "oa-pgshare-")
	if err != nil {
		return "", func() {}, err
	}
	cleanup = func() { _ = os.RemoveAll(dst) }
	if err := copyTree(src, dst); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return dst, cleanup, nil
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
}
