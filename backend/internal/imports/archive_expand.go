package imports

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bodgit/sevenzip"
	"github.com/nwaples/rardecode/v2"
	"golang.org/x/text/encoding/simplifiedchinese"

	"oa-agent/backend/internal/formats"
)

// Limits for expanding an archive (ZIP, 7z, RAR) at intake. An archive is attacker-controlled input: entry names are re-validated, sizes are
// enforced while copying (a declared size is never trusted), and nothing is ever executed or written outside the batch directory.
const (
	maxArchiveEntries  = 20000    // entries read from one archive
	maxArchiveBytes    = 20 << 30 // total expanded bytes per archive (20 GiB)
	suspiciousRatio    = 200      // expanded data this much bigger than the archive itself ...
	suspiciousMinBytes = 64 << 20 // ... and bigger than this is treated as a bomb
)

// archiveKind returns "zip", "7z" or "rar" for a file name, "" for anything else. Archives inside an archive are not expanded again:
// they stay plain files and are classified from their name.
func archiveKind(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".zip":
		return "zip"
	case ".7z":
		return "7z"
	case ".rar":
		return "rar"
	}
	return ""
}

// entry is one item of an archive as the walkers report it; size is -1 when the archive does not say.
type entry struct {
	name                    string
	size                    int64
	csize                   int64 // compressed size when known (0 = unknown)
	dir, regular, encrypted bool
}

// errStop makes a walker stop and the whole archive be left alone (too many entries, too big, a bomb, no space ...).
var errStop = errors.New("archive left as it is")

type walkFn func(e entry, open func() (io.ReadCloser, error)) error

func walkArchive(kind, file string, fn walkFn) error {
	switch kind {
	case "zip":
		return walkZip(file, fn)
	case "7z":
		return walk7z(file, fn)
	case "rar":
		return walkRar(file, fn)
	}
	return errStop
}

// zipEntryName returns the entry name as UTF-8: Windows tools write GBK names without the UTF-8 flag.
func zipEntryName(f *zip.File) string {
	n := f.Name
	if f.NonUTF8 && !utf8.ValidString(n) {
		if s, err := simplifiedchinese.GBK.NewDecoder().String(n); err == nil {
			n = s
		}
	}
	return strings.ReplaceAll(n, "\\", "/")
}

func walkZip(file string, fn walkFn) error {
	zr, err := zip.OpenReader(file)
	if err != nil {
		return err
	}
	defer zr.Close()
	if len(zr.File) == 0 || len(zr.File) > maxArchiveEntries {
		return errStop
	}
	for _, zf := range zr.File {
		e := entry{name: zipEntryName(zf), size: int64(zf.UncompressedSize64), csize: int64(zf.CompressedSize64),
			dir: zf.FileInfo().IsDir(), regular: zf.Mode().IsRegular(), encrypted: zf.Flags&0x1 != 0}
		if err := fn(e, zf.Open); err != nil {
			return err
		}
	}
	return nil
}

func walk7z(file string, fn walkFn) error {
	r, err := sevenzip.OpenReader(file) // no password: encrypted headers or contents fail here or on Open, and the archive is left alone
	if err != nil {
		return err
	}
	defer r.Close()
	if len(r.File) == 0 || len(r.File) > maxArchiveEntries {
		return errStop
	}
	for _, f := range r.File {
		m := f.Mode()
		e := entry{name: strings.ReplaceAll(f.Name, "\\", "/"), size: int64(f.UncompressedSize), dir: m.IsDir(), regular: m.IsRegular()}
		if err := fn(e, f.Open); err != nil {
			return err
		}
	}
	return nil
}

func walkRar(file string, fn walkFn) error {
	rr, err := rardecode.OpenReader(file) // no password: encrypted headers fail here, encrypted files are flagged below
	if err != nil {
		return err
	}
	defer rr.Close()
	n := 0
	for {
		h, err := rr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if n++; n > maxArchiveEntries {
			return errStop
		}
		size := h.UnPackedSize
		if h.UnKnownSize {
			size = -1
		}
		e := entry{name: strings.ReplaceAll(h.Name, "\\", "/"), size: size, csize: h.PackedSize, dir: h.IsDir, regular: h.LinkType == 0, encrypted: h.Encrypted}
		if err := fn(e, func() (io.ReadCloser, error) { return io.NopCloser(rr), nil }); err != nil {
			return err
		}
	}
}

// cleanEntryPath turns an entry name into a safe relative path, or "" when it must be skipped (drive letter, "..", control characters,
// reserved names ...). A leading "/" is stripped like common unzip tools do; directories are not entries here.
func cleanEntryPath(name string) string {
	name = strings.TrimLeft(name, "/")
	if name == "" || strings.HasSuffix(name, "/") {
		return ""
	}
	if c := path.Clean(name); c != name || ValidatePath(name) != nil {
		return ""
	}
	return name
}

// expandArchives replaces every readable archive in the batch by the files inside it (their source path is "<archive name without extension>/<path
// inside>", so the folder structure survives). An archive that cannot be expanded (damaged, encrypted, too large, a bomb) is left as an ordinary
// file and is then classified from its name like any other unreadable file; entries that are unsafe, encrypted or too large are reported as
// failures of the task instead of failing the whole upload. dir is the batch's spool directory (files are written there with fresh ids).
// It returns the stored paths of the archives that were replaced; remove them only after the task has been saved (a failed save puts the
// uploaded files back so the client can retry).
func (s *Service) expandArchives(dir string, b *Batch) (consumed []string) {
	now := time.Now().UTC()
	out := make([]File, 0, len(b.Files))
	expandedAny := false
	for _, f := range b.Files {
		kind := archiveKind(f.SourceName)
		if kind == "" || f.StoredPath == "" {
			out = append(out, f)
			continue
		}
		files, fails, ok := s.expandArchive(kind, dir, b.Job.ID, f, now)
		if !ok {
			out = append(out, f)
			continue
		}
		expandedAny = true
		consumed = append(consumed, f.StoredPath) // the archive itself is replaced by its content; the caller removes it once the task is saved
		out = append(out, files...)
		for _, fl := range fails {
			fl.Index = len(b.Failures)
			b.Failures = append(b.Failures, fl)
		}
	}
	if !expandedAny {
		return nil
	}
	b.Files = out
	b.Job.TotalFiles = len(b.Files) + len(b.Failures)
	b.Job.FailureCount = len(b.Failures)
	b.Job.ProcessedFiles = len(b.Failures)
	return consumed
}

func (s *Service) expandArchive(kind, dir, taskID string, arc File, now time.Time) (files []File, fails []Failure, ok bool) {
	fi, err := os.Stat(arc.StoredPath)
	if err != nil {
		return nil, nil, false
	}
	arcSize := fi.Size()
	stem := strings.TrimSuffix(arc.SourcePath, path.Ext(arc.SourcePath))
	failed := func(rel, code, msg string) {
		fails = append(fails, Failure{SourcePath: stem + "/" + rel, Code: code, Message: msg, MIMEType: "application/octet-stream"})
	}
	var written []string
	var declared, wrote int64
	err = walkArchive(kind, arc.StoredPath, func(e entry, open func() (io.ReadCloser, error)) error {
		if e.dir {
			return nil
		}
		rel := cleanEntryPath(e.name)
		switch {
		case rel == "":
			failed(path.Base(e.name), "ARCHIVE_UNSAFE_PATH", "压缩包内的路径不安全或无效，已跳过")
			return nil
		case !e.regular:
			failed(rel, "ARCHIVE_SPECIAL_FILE", "压缩包内的链接或特殊文件，已跳过")
			return nil
		case e.encrypted:
			failed(rel, "ARCHIVE_ENCRYPTED", "压缩包内的文件已加密，无法读取，已跳过")
			return nil
		case e.size >= 0 && s.Limits.TooLarge(e.size):
			failed(rel, "FILE_TOO_LARGE", "超过管理员设置的单文件上限 "+HumanBytes(s.Limits.MaxUploadBytes))
			return nil
		case e.csize > 0 && e.size > suspiciousMinBytes && e.size/e.csize > suspiciousRatio:
			failed(rel, "ARCHIVE_SUSPICIOUS", "压缩比异常（疑似压缩炸弹），已跳过")
			return nil
		}
		if e.size > 0 {
			if declared += e.size; declared > maxArchiveBytes {
				return errStop
			}
			if s.Limits.CheckSpace(dir, e.size) != nil {
				return errStop
			}
		}
		id, err := NewID()
		if err != nil {
			return errStop
		}
		stored := filepath.Join(dir, id+safeExt(rel))
		rc, err := open()
		if err != nil {
			failed(rel, "ARCHIVE_ENTRY_UNREADABLE", "压缩包内的文件损坏或无法读取，已跳过")
			return nil
		}
		sum, size, err := copyEntry(rc, e.size, stored, maxArchiveBytes-wrote)
		rc.Close()
		if err != nil {
			_ = os.Remove(stored)
			if errors.Is(err, errBudget) {
				return errStop
			}
			failed(rel, "ARCHIVE_ENTRY_UNREADABLE", "压缩包内的文件损坏或无法读取，已跳过")
			return nil
		}
		written = append(written, stored)
		// the archive as a whole must not unpack to far more than it weighs, whatever the entries claim
		if wrote += size; wrote > suspiciousMinBytes && arcSize > 0 && wrote/arcSize > suspiciousRatio {
			return errStop
		}
		files = append(files, File{ID: id, TaskID: taskID, SourceName: path.Base(rel), SourcePath: stem + "/" + rel, MIMEType: formats.MIME(rel), SHA256: sum, SizeBytes: size,
			Evidence: []string{}, ReviewRequired: true, ArchiveStatus: "pending", CreatedAt: now, StoredPath: stored})
		return nil
	})
	if err != nil || len(files) == 0 {
		for _, p := range written {
			_ = os.Remove(p)
		}
		return nil, nil, false // damaged, encrypted, too big, a bomb, or nothing usable inside: keep the archive itself
	}
	return files, fails, true
}

var errBudget = errors.New("archive exceeds the expansion budget")

// copyEntry writes one entry, enforcing the declared size as a hard limit (a lying header cannot make us write more). declared < 0 means the
// archive does not say; then only the remaining budget limits it.
func copyEntry(r io.Reader, declared int64, dst string, budget int64) (sum string, size int64, err error) {
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", 0, err
	}
	h := sha256.New()
	limit := budget
	if declared >= 0 && declared < budget {
		limit = declared
	}
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(r, limit+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
	case declared >= 0 && n != declared:
		err = fmt.Errorf("entry size does not match its header")
		if n > budget {
			err = errBudget
		}
	case declared < 0 && n > budget:
		err = errBudget
	}
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
