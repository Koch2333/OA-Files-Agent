package imports

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

const MaxFiles = 100
const MaxFileBytes int64 = 20 << 20
const MaxRequestBytes int64 = 128 << 20

type Error struct {
	Status        int
	Code, Message string
}

func (e *Error) Error() string { return e.Code }

var ErrNotFound = errors.New("not found")
var ErrConflict = errors.New("conflict")

type Store interface {
	Save(context.Context, Batch) (Batch, bool, error)
}

type Service struct {
	Store Store
	Root  string
}

func ValidatePath(value string) error {
	if value == "" || len(value) > 240 || strings.ContainsAny(value, "\\:<>\"|?*\x00") || strings.HasPrefix(value, "/") || path.Clean(value) != value {
		return errors.New("invalid relative path")
	}
	for _, ch := range value {
		if unicode.IsControl(ch) {
			return errors.New("control character")
		}
	}
	for _, component := range strings.Split(value, "/") {
		if component == "." || component == ".." || strings.TrimRight(component, ". ") != component {
			return errors.New("invalid path component")
		}
		stem := strings.ToUpper(strings.SplitN(component, ".", 2)[0])
		if stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL" || (len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '1' && stem[3] <= '9') {
			return errors.New("reserved filename")
		}
	}
	return nil
}

var mimeTypes = map[string]string{
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".pdf":  "application/pdf", ".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
}

// Import copies into an application-owned spool; client paths are metadata only.
// The spool root must be owned exclusively by the API user (no untrusted local writers).
func (s *Service) Import(ctx context.Context, reader *multipart.Reader, key, templateID string) (batch Batch, err error) {
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
	batch = Batch{Key: key, TemplateID: templateID, Job: Job{ID: id, Status: "queued", CreatedAt: now, UpdatedAt: now}, Files: []File{}, Failures: []Failure{}}
	applyAI(&batch.Job, AIFrom(ctx))
	optsHandler, _ := ctx.Value(optsCtxKey{}).(OptionsHandler)
	optionsSeen := false
	requestHash := sha256.New()
	var paths []string
	manifestSeen := false
	for {
		if err = ctx.Err(); err != nil {
			return batch, err
		}
		part, nextErr := reader.NextPart()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			return batch, &Error{400, "INVALID_MULTIPART", "上传内容不完整"}
		}
		if part.FormName() == "options" && optsHandler != nil && manifestSeen && !optionsSeen && batch.Job.TotalFiles == 0 {
			raw, readErr := io.ReadAll(io.LimitReader(part, MaxOptionsBytes+1))
			if readErr != nil || len(raw) > MaxOptionsBytes {
				return batch, &Error{400, "INVALID_OPTIONS", "options 必须是不超过 32 KiB 的 JSON 对象"}
			}
			ai, herr := optsHandler(raw)
			if herr != nil {
				return batch, herr
			}
			optionsSeen = true
			applyAI(&batch.Job, ai)
			fmt.Fprintf(requestHash, "opts:%d:%s;", len(ai.Fingerprint), ai.Fingerprint)
			part.Close()
			continue
		}
		if part.FormName() == "paths" && batch.Job.TotalFiles == 0 && !manifestSeen && !optionsSeen {
			raw, readErr := io.ReadAll(io.LimitReader(part, (64<<10)+1))
			if readErr != nil || len(raw) > 64<<10 || json.Unmarshal(raw, &paths) != nil || paths == nil {
				return batch, &Error{400, "INVALID_PATHS", "paths 必须是相对路径数组"}
			}
			if len(paths) > MaxFiles {
				return batch, &Error{413, "TOO_MANY_FILES", "每批最多 100 个文件"}
			}
			manifestSeen = true
			part.Close()
			continue
		}
		if part.FormName() != "files" {
			return batch, &Error{400, "INVALID_MULTIPART", "仅支持 files；paths 必须置于文件之前且仅出现一次，options（可选）必须紧跟在 paths 之后、文件之前且仅一次"}
		}
		index := batch.Job.TotalFiles
		batch.Job.TotalFiles++
		if batch.Job.TotalFiles > MaxFiles {
			return batch, &Error{413, "TOO_MANY_FILES", "每批最多 100 个文件"}
		}
		_, disposition, parseErr := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
		filename := disposition["filename"]
		relative := filename
		if manifestSeen {
			if index >= len(paths) {
				return batch, &Error{400, "INVALID_PATHS", "paths 与 files 数量不一致"}
			}
			relative = paths[index]
		}
		if parseErr != nil || ValidatePath(filename) != nil || strings.Contains(filename, "/") || ValidatePath(relative) != nil || path.Base(relative) != filename {
			return batch, &Error{400, "INVALID_PATH", "文件名或相对路径无效"}
		}
		extension := strings.ToLower(path.Ext(filename))
		// Length-prefixed paths and a per-file digest form a stable semantic request fingerprint.
		fmt.Fprintf(requestHash, "%d:%s:", len(relative), relative)
		contentType, supported := mimeTypes[extension]
		if !supported {
			message := "暂不支持此文件格式"
			if extension == ".doc" || extension == ".xls" {
				message = "请先使用办公软件转换为 DOCX/XLSX 后导入"
			}
			hash := sha256.New()
			size, readErr := io.Copy(hash, part)
			if readErr != nil {
				return batch, &Error{400, "INVALID_MULTIPART", "上传内容不完整"}
			}
			batch.Failures = append(batch.Failures, Failure{Index: index, SourcePath: relative, Code: "UNSUPPORTED_FORMAT", Message: message, SHA256: hex.EncodeToString(hash.Sum(nil)), SizeBytes: size, MIMEType: "application/octet-stream"})
			fmt.Fprintf(requestHash, "%d:%x;", size, hash.Sum(nil))
			continue
		}
		fileID, idErr := NewID()
		if idErr != nil {
			return batch, idErr
		}
		stored := filepath.Join(dir, fileID+extension)
		file, openErr := os.OpenFile(stored, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if openErr != nil {
			return batch, openErr
		}
		hash := sha256.New()
		size, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(part, MaxFileBytes+1))
		syncErr := file.Sync()
		closeErr := file.Close()
		if copyErr != nil || syncErr != nil || closeErr != nil {
			return batch, &Error{400, "UPLOAD_FAILED", "文件接收失败"}
		}
		if size > MaxFileBytes {
			os.Remove(stored)
			rest, readErr := io.Copy(hash, part)
			if readErr != nil {
				return batch, &Error{400, "INVALID_MULTIPART", "上传内容不完整"}
			}
			batch.Failures = append(batch.Failures, Failure{Index: index, SourcePath: relative, Code: "FILE_TOO_LARGE", Message: "单文件上限 20 MiB", SHA256: hex.EncodeToString(hash.Sum(nil)), SizeBytes: size + rest, MIMEType: contentType})
			fmt.Fprintf(requestHash, "%d:%x;", size+rest, hash.Sum(nil))
			continue
		}
		if size == 0 {
			fmt.Fprintf(requestHash, "%d:%x;", size, hash.Sum(nil))
			os.Remove(stored)
			batch.Failures = append(batch.Failures, Failure{Index: index, SourcePath: relative, Code: "EMPTY_FILE", Message: "文件为空", SHA256: hex.EncodeToString(hash.Sum(nil)), SizeBytes: 0, MIMEType: contentType})
			continue
		}
		fmt.Fprintf(requestHash, "%d:%x;", size, hash.Sum(nil))
		batch.Files = append(batch.Files, File{ID: fileID, TaskID: id, SourceName: filename, SourcePath: relative, MIMEType: contentType, SHA256: hex.EncodeToString(hash.Sum(nil)), SizeBytes: size, Evidence: []string{}, ReviewRequired: true, ArchiveStatus: "pending", CreatedAt: now, StoredPath: stored})
	}
	if batch.Job.TotalFiles == 0 || (manifestSeen && len(paths) != batch.Job.TotalFiles) {
		return batch, &Error{400, "INVALID_PATHS", "至少上传一个文件，且 paths 与 files 数量必须一致"}
	}
	batch.Job.FailureCount = len(batch.Failures)
	batch.Job.ProcessedFiles = len(batch.Failures)
	if len(batch.Files) == 0 {
		batch.Job.Status = "failed"
	}
	batch.Fingerprint = hex.EncodeToString(requestHash.Sum(nil))
	saved, replay, saveErr := s.Store.Save(ctx, batch)
	if saveErr != nil {
		return batch, fmt.Errorf("save import: %w", saveErr)
	}
	committed = !replay
	batch = saved
	batch.Replay = replay
	return batch, nil
}

// MaxOptionsBytes caps the multipart "options" part.
const MaxOptionsBytes = 32 << 10
