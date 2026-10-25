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
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
)

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
	// Limits are the optional operator safety valves and the disk-space reserve (see limits.go). The zero value
	// means: no size/count caps, default reserve.
	Limits Limits

	stageMu  sync.Mutex
	stageLoc map[string]*sync.Mutex // per upload-session locks (stage.go)
	copies   map[string]*PathImport // local-path imports by batch id (frompaths.go)
}

type expectedBytesKey struct{}

// WithExpectedBytes tells Import how many bytes the request announced (Content-Length) so the free-disk check can
// refuse before anything is written.
func WithExpectedBytes(ctx context.Context, n int64) context.Context {
	return context.WithValue(ctx, expectedBytesKey{}, n)
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
	expected, _ := ctx.Value(expectedBytesKey{}).(int64)
	if err = s.Limits.CheckSpace(s.Root, expected); err != nil {
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
			if s.Limits.TooManyFiles(len(paths)) {
				return batch, &Error{413, "TOO_MANY_FILES", fmt.Sprintf("超过管理员设置的每批文件数上限（%d）", s.Limits.MaxBatchFiles)}
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
		if s.Limits.TooManyFiles(batch.Job.TotalFiles) {
			return batch, &Error{413, "TOO_MANY_FILES", fmt.Sprintf("超过管理员设置的每批文件数上限（%d）", s.Limits.MaxBatchFiles)}
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
		if err = s.Limits.CheckSpace(s.Root, 0); err != nil {
			return batch, err
		}
		stored := filepath.Join(dir, fileID+extension)
		partial := stored + ".part"
		file, openErr := os.OpenFile(partial, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if openErr != nil {
			return batch, openErr
		}
		hash := sha256.New()
		var src io.Reader = part
		if s.Limits.MaxUploadBytes > 0 {
			src = io.LimitReader(part, s.Limits.MaxUploadBytes+1)
		}
		size, copyErr := io.Copy(io.MultiWriter(file, hash), src)
		syncErr := file.Sync()
		closeErr := file.Close()
		if copyErr != nil || syncErr != nil || closeErr != nil {
			os.Remove(partial)
			for _, e := range []error{copyErr, syncErr, closeErr} {
				if IsNoSpace(e) {
					return batch, NoSpaceError(max64(expected, size), 0)
				}
			}
			var mbe *http.MaxBytesError
			if errors.As(copyErr, &mbe) {
				return batch, copyErr
			}
			return batch, &Error{400, "UPLOAD_FAILED", "文件接收失败"}
		}
		if s.Limits.TooLarge(size) {
			os.Remove(partial)
			rest, readErr := io.Copy(hash, part)
			if readErr != nil {
				return batch, &Error{400, "INVALID_MULTIPART", "上传内容不完整"}
			}
			batch.Failures = append(batch.Failures, Failure{Index: index, SourcePath: relative, Code: "FILE_TOO_LARGE", Message: "超过管理员设置的单文件上限 " + HumanBytes(s.Limits.MaxUploadBytes), SHA256: hex.EncodeToString(hash.Sum(nil)), SizeBytes: size + rest, MIMEType: contentType})
			fmt.Fprintf(requestHash, "%d:%x;", size+rest, hash.Sum(nil))
			continue
		}
		if size == 0 {
			fmt.Fprintf(requestHash, "%d:%x;", size, hash.Sum(nil))
			os.Remove(partial)
			batch.Failures = append(batch.Failures, Failure{Index: index, SourcePath: relative, Code: "EMPTY_FILE", Message: "文件为空", SHA256: hex.EncodeToString(hash.Sum(nil)), SizeBytes: 0, MIMEType: contentType})
			continue
		}
		fmt.Fprintf(requestHash, "%d:%x;", size, hash.Sum(nil))
		if renameErr := os.Rename(partial, stored); renameErr != nil {
			os.Remove(partial)
			return batch, renameErr
		}
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
