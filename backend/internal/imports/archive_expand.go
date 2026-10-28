package imports

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"

	"oa-agent/backend/internal/formats"
)

// Limits for expanding a ZIP at intake. A ZIP is attacker-controlled input: entry names are re-validated, sizes are enforced while
// copying (the declared size is never trusted), and nothing is ever executed or written outside the batch directory.
const (
	maxArchiveEntries  = 20000    // entries read from one archive
	maxArchiveBytes    = 20 << 30 // total expanded bytes per archive (20 GiB)
	suspiciousRatio    = 200      // an entry this much bigger than its compressed size ...
	suspiciousMinBytes = 64 << 20 // ... and bigger than this is treated as a zip bomb and skipped
)

func isZipName(name string) bool { return strings.EqualFold(path.Ext(name), ".zip") }

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

// cleanEntryPath turns an entry name into a safe relative path, or "" when it must be skipped (absolute, drive letter, "..", control
// characters, reserved names ...). Directories are not entries here.
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

// expandArchives replaces every readable ZIP in the batch by the files inside it (their source path is "<zip name without .zip>/<path inside>",
// so the folder structure survives). A ZIP that cannot be expanded (damaged, encrypted, too large, a bomb) is left as an ordinary file and
// is then classified from its name like any other unreadable file; entries that are unsafe, encrypted or too large are reported as failures
// of the task instead of failing the whole upload. dir is the batch's spool directory (files are written there with fresh ids).
// It returns the stored paths of the archives that were replaced; remove them only after the task has been saved (a failed save puts the
// uploaded files back so the client can retry).
func (s *Service) expandArchives(dir string, b *Batch) (consumed []string) {
	now := time.Now().UTC()
	out := make([]File, 0, len(b.Files))
	expandedAny := false
	for _, f := range b.Files {
		if !isZipName(f.SourceName) || f.StoredPath == "" {
			out = append(out, f)
			continue
		}
		files, fails, ok := s.expandZip(dir, b.Job.ID, f, now)
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

func (s *Service) expandZip(dir, taskID string, arc File, now time.Time) (files []File, fails []Failure, ok bool) {
	zr, err := zip.OpenReader(arc.StoredPath)
	if err != nil || len(zr.File) == 0 || len(zr.File) > maxArchiveEntries {
		return nil, nil, false
	}
	defer zr.Close()
	var total int64
	for _, zf := range zr.File {
		if zf.FileInfo().IsDir() {
			continue
		}
		total += int64(zf.UncompressedSize64)
		if total > maxArchiveBytes {
			return nil, nil, false
		}
	}
	if err := s.Limits.CheckSpace(dir, total); err != nil {
		return nil, nil, false
	}
	stem := strings.TrimSuffix(arc.SourcePath, path.Ext(arc.SourcePath))
	failed := func(rel, code, msg string) {
		fails = append(fails, Failure{SourcePath: stem + "/" + rel, Code: code, Message: msg, MIMEType: "application/octet-stream"})
	}
	var written []string
	cleanup := func() {
		for _, p := range written {
			_ = os.Remove(p)
		}
	}
	for _, zf := range zr.File {
		if zf.FileInfo().IsDir() {
			continue
		}
		name := zipEntryName(zf)
		rel := cleanEntryPath(name)
		switch {
		case rel == "":
			failed(path.Base(name), "ARCHIVE_UNSAFE_PATH", "压缩包内的路径不安全或无效，已跳过")
			continue
		case !zf.Mode().IsRegular():
			failed(rel, "ARCHIVE_SPECIAL_FILE", "压缩包内的链接或特殊文件，已跳过")
			continue
		case zf.Flags&0x1 != 0:
			failed(rel, "ARCHIVE_ENCRYPTED", "压缩包内的文件已加密，无法读取，已跳过")
			continue
		case s.Limits.TooLarge(int64(zf.UncompressedSize64)):
			failed(rel, "FILE_TOO_LARGE", "超过管理员设置的单文件上限 "+HumanBytes(s.Limits.MaxUploadBytes))
			continue
		case zf.CompressedSize64 > 0 && zf.UncompressedSize64 > suspiciousMinBytes && zf.UncompressedSize64/zf.CompressedSize64 > suspiciousRatio:
			failed(rel, "ARCHIVE_SUSPICIOUS", "压缩比异常（疑似压缩炸弹），已跳过")
			continue
		}
		id, err := NewID()
		if err != nil {
			cleanup()
			return nil, nil, false
		}
		ext := safeExt(rel)
		stored := filepath.Join(dir, id+ext)
		sum, size, err := copyEntry(zf, stored)
		if err != nil {
			_ = os.Remove(stored)
			failed(rel, "ARCHIVE_ENTRY_UNREADABLE", "压缩包内的文件损坏或无法读取，已跳过")
			continue
		}
		written = append(written, stored)
		files = append(files, File{ID: id, TaskID: taskID, SourceName: path.Base(rel), SourcePath: stem + "/" + rel, MIMEType: formats.MIME(rel), SHA256: sum, SizeBytes: size,
			Evidence: []string{}, ReviewRequired: true, ArchiveStatus: "pending", CreatedAt: now, StoredPath: stored})
	}
	if len(files) == 0 {
		cleanup()
		return nil, nil, false // nothing usable inside: keep the archive itself
	}
	return files, fails, true
}

// copyEntry writes one entry, enforcing the declared size as a hard limit (a lying header cannot make us write more).
func copyEntry(zf *zip.File, dst string) (sum string, size int64, err error) {
	rc, err := zf.Open()
	if err != nil {
		return "", 0, err
	}
	defer rc.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", 0, err
	}
	h := sha256.New()
	limit := int64(zf.UncompressedSize64)
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(rc, limit+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && n != limit {
		err = fmt.Errorf("entry size does not match its header")
	}
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
