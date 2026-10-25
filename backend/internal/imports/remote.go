package imports

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// RemoteFile is one file selected from a remote storage. RelPath is only metadata (validated like an uploaded
// relative path); the bytes come from Open and are copied into the application-owned spool.
type RemoteFile struct {
	RelPath string
	Size    int64 // declared size, -1 when unknown
	Open    func(ctx context.Context) (io.ReadCloser, error)
}

// ImportRemote ingests remote files with exactly the same rules as a multipart upload: the optional operator caps (Limits),
// supported formats only; unsupported/oversized/empty/unreadable files become per-file
// failures instead of failing the batch. describe turns a read error into a user-facing message.
// seed binds the idempotency fingerprint to the source (e.g. the connection id).
func (s *Service) ImportRemote(ctx context.Context, files []RemoteFile, key, templateID, seed string, describe func(error) string) (batch Batch, err error) {
	if len(files) == 0 {
		return batch, &Error{400, "INVALID_PATHS", "请选择要导入的文件"}
	}
	if s.Limits.TooManyFiles(len(files)) {
		return batch, &Error{413, "TOO_MANY_FILES", fmt.Sprintf("超过管理员设置的每批文件数上限（%d）", s.Limits.MaxBatchFiles)}
	}
	id, err := NewID()
	if err != nil {
		return batch, err
	}
	if err = os.MkdirAll(s.Root, 0700); err != nil {
		return batch, err
	}
	dir, err := os.MkdirTemp(s.Root, "import-")
	if err != nil {
		return batch, err
	}
	committed := false
	defer func() {
		if !committed {
			os.RemoveAll(dir)
		}
	}()
	now := time.Now().UTC()
	batch = Batch{Key: key, TemplateID: templateID, Job: Job{ID: id, Status: "queued", TotalFiles: len(files), CreatedAt: now, UpdatedAt: now}, Files: []File{}, Failures: []Failure{}}
	fp := sha256.New()
	fmt.Fprintf(fp, "remote:%s:", seed)
	ai := AIFrom(ctx)
	applyAI(&batch.Job, ai)
	if ai.Fingerprint != "" {
		fmt.Fprintf(fp, "opts:%d:%s;", len(ai.Fingerprint), ai.Fingerprint)
	}
	for i, rf := range files {
		if err = ctx.Err(); err != nil {
			return batch, err
		}
		rel := rf.RelPath
		fail := func(code, msg, sum string, size int64, mime string) {
			if len(sum) != 64 {
				sum = strings.Repeat("0", 64)
			}
			batch.Failures = append(batch.Failures, Failure{Index: i, SourcePath: rel, Code: code, Message: msg, SHA256: sum, SizeBytes: size, MIMEType: mime})
			fmt.Fprintf(fp, "%d:%s:%s;", len(rel), rel, code)
		}
		if ValidatePath(rel) != nil {
			rel = safeFailurePath(i, rf.RelPath)
			fail("INVALID_PATH", "文件名或路径包含本系统不支持的字符", "", max64(rf.Size, 0), "application/octet-stream")
			continue
		}
		ext := strings.ToLower(path.Ext(rel))
		contentType, supported := mimeTypes[ext]
		if !supported {
			msg := "暂不支持此文件格式"
			if ext == ".doc" || ext == ".xls" {
				msg = "请先使用办公软件转换为 DOCX/XLSX 后导入"
			}
			fail("UNSUPPORTED_FORMAT", msg, "", max64(rf.Size, 0), "application/octet-stream")
			continue
		}
		if s.Limits.TooLarge(rf.Size) {
			fail("FILE_TOO_LARGE", "超过管理员设置的单文件上限 "+HumanBytes(s.Limits.MaxUploadBytes), "", rf.Size, contentType)
			continue
		}
		if cerr := s.Limits.CheckSpace(s.Root, max64(rf.Size, 0)); cerr != nil {
			return batch, cerr
		}
		rc, oerr := rf.Open(ctx)
		if oerr != nil {
			msg := "读取远端文件失败"
			if describe != nil {
				msg = describe(oerr)
			}
			fail("REMOTE_READ_FAILED", msg, "", max64(rf.Size, 0), contentType)
			continue
		}
		fileID, idErr := NewID()
		if idErr != nil {
			rc.Close()
			return batch, idErr
		}
		stored := filepath.Join(dir, fileID+ext)
		f, openErr := os.OpenFile(stored, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if openErr != nil {
			rc.Close()
			return batch, openErr
		}
		h := sha256.New()
		size, copyErr := io.Copy(io.MultiWriter(f, h), limitedReader(rc, s.Limits.MaxUploadBytes))
		rc.Close()
		syncErr := f.Sync()
		closeErr := f.Close()
		sum := hex.EncodeToString(h.Sum(nil))
		switch {
		case IsNoSpace(copyErr) || IsNoSpace(syncErr) || IsNoSpace(closeErr):
			os.Remove(stored)
			return batch, NoSpaceError(max64(rf.Size, size), 0)
		case copyErr != nil || syncErr != nil || closeErr != nil:
			os.Remove(stored)
			msg := "读取远端文件失败"
			if describe != nil && copyErr != nil {
				msg = describe(copyErr)
			}
			fail("REMOTE_READ_FAILED", msg, "", 0, contentType)
		case s.Limits.TooLarge(size):
			os.Remove(stored)
			fail("FILE_TOO_LARGE", "超过管理员设置的单文件上限 "+HumanBytes(s.Limits.MaxUploadBytes), "", size, contentType)
		case size == 0:
			os.Remove(stored)
			fail("EMPTY_FILE", "文件为空", sum, 0, contentType)
		default:
			fmt.Fprintf(fp, "%d:%s:%d:%s;", len(rel), rel, size, sum)
			batch.Files = append(batch.Files, File{ID: fileID, TaskID: id, SourceName: path.Base(rel), SourcePath: rel, MIMEType: contentType, SHA256: sum, SizeBytes: size, Evidence: []string{}, ReviewRequired: true, ArchiveStatus: "pending", CreatedAt: now, StoredPath: stored})
		}
	}
	batch.Job.FailureCount = len(batch.Failures)
	batch.Job.ProcessedFiles = len(batch.Failures)
	if len(batch.Files) == 0 {
		batch.Job.Status = "failed"
	}
	batch.Fingerprint = hex.EncodeToString(fp.Sum(nil))
	saved, replay, saveErr := s.Store.Save(ctx, batch)
	if saveErr != nil {
		return batch, fmt.Errorf("save import: %w", saveErr)
	}
	committed = !replay
	saved.Replay = replay
	return saved, nil
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// safeFailurePath gives an unsupported remote name a harmless display path (it is never used on disk).
func safeFailurePath(i int, raw string) string {
	base := path.Base(strings.ReplaceAll(raw, "\\", "/"))
	clean := strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`\:<>"|?*`, r) {
			return '_'
		}
		return r
	}, base)
	if clean == "" || clean == "." || clean == ".." {
		clean = fmt.Sprintf("远端文件-%d", i+1)
	}
	return clean
}

// limitedReader caps r at max+1 bytes (so an over-cap file is detectable); max <= 0 means unlimited.
func limitedReader(r io.Reader, max int64) io.Reader {
	if max <= 0 {
		return r
	}
	return io.LimitReader(r, max+1)
}
