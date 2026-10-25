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
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Local-path import (Windows desktop / local single-user mode only; the API layer enforces that). The service walks the
// given files and folders and COPIES every supported file into the staging area of a batch. Source files are only ever
// opened read-only: never moved, modified or deleted. Symlinks, junctions and anything that is not a regular file are
// skipped; directory cycles are detected; one unreadable file never aborts the batch.

const maxWalkDepth = 64
const maxPathResults = 2000

// PathOptions tune a local-path import.
type PathOptions struct {
	IncludeHidden bool
}

// PathResult is the outcome for one source file.
type PathResult struct {
	Path    string `json:"path"`
	Status  string `json:"status"` // copied | failed | skipped
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
	Size    int64  `json:"size"`
}

// PathStatus is the progress of a local-path import (polled through GET /v1/imports/batches/{id}).
type PathStatus struct {
	BatchID     string       `json:"batch_id"`
	State       string       `json:"state"` // scanning | running | committing | done | failed | cancelled
	Discovered  int          `json:"discovered"`
	Copied      int          `json:"copied"`
	Failed      int          `json:"failed"`
	Skipped     int          `json:"skipped"`
	BytesTotal  int64        `json:"bytes_total"`
	BytesCopied int64        `json:"bytes_copied"`
	Current     string       `json:"current,omitempty"`
	Results     []PathResult `json:"results"`
	Truncated   bool         `json:"results_truncated,omitempty"`
	TaskID      string       `json:"task_id,omitempty"`
	Error       string       `json:"error,omitempty"`
	StartedAt   time.Time    `json:"started_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
}

// PathImport is a running (or finished) local-path import.
type PathImport struct {
	mu     sync.Mutex
	st     PathStatus
	cancel context.CancelFunc
	bytes  atomic.Int64
}

// Status returns a snapshot.
func (p *PathImport) Status() PathStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	st := p.st
	st.Results = append([]PathResult(nil), p.st.Results...)
	st.BytesCopied = p.bytes.Load()
	return st
}

// Running is true while files are being scanned or copied.
func (p *PathImport) Running() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.st.State == "scanning" || p.st.State == "running"
}

// Cancel stops the copy loop.
func (p *PathImport) Cancel() { p.cancel() }

// SetTask records the task created by the auto-commit (or the commit error) and finishes the import.
func (p *PathImport) SetTask(taskID string, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.st.TaskID = taskID
	if err != nil {
		p.st.State, p.st.Error = "failed", err.Error()
	} else {
		p.st.State = "done"
	}
	p.st.UpdatedAt = time.Now().UTC()
}

func (p *PathImport) update(f func(st *PathStatus)) {
	p.mu.Lock()
	f(&p.st)
	p.st.UpdatedAt = time.Now().UTC()
	p.mu.Unlock()
}

func (p *PathImport) add(r PathResult) {
	p.update(func(st *PathStatus) {
		switch r.Status {
		case "copied":
			st.Copied++
		case "failed":
			st.Failed++
		case "skipped":
			st.Skipped++
		}
		if len(st.Results) < maxPathResults || r.Status == "failed" && len(st.Results) < 2*maxPathResults {
			st.Results = append(st.Results, r)
		} else {
			st.Truncated = true
		}
	})
}

func (s *Service) pathImport(batch string) *PathImport {
	s.stageMu.Lock()
	defer s.stageMu.Unlock()
	return s.copies[batch]
}

// PathImportOf is the progress handle of a batch's local-path import (nil when none).
func (s *Service) PathImportOf(batch string) *PathImport { return s.pathImport(batch) }

type pathFile struct {
	abs, rel string
	size     int64
	explicit bool
}

// ValidateSourcePath refuses what a local import must never read: relative paths, NUL bytes, Windows device namespaces
// (\\.\ and \\?\GLOBALROOT), and anything inside the application's own spool. UNC shares (\\server\share) are allowed
// and treated as read-only sources.
func (s *Service) ValidateSourcePath(p string) error {
	if p == "" || strings.ContainsRune(p, 0) {
		return fmt.Errorf("路径无效")
	}
	if !filepath.IsAbs(p) {
		return fmt.Errorf("必须是绝对路径")
	}
	if runtime.GOOS == "windows" {
		low := strings.ToLower(strings.ReplaceAll(p, "/", `\`))
		if strings.HasPrefix(low, `\\.\`) || strings.HasPrefix(low, `\\?\globalroot`) || strings.HasPrefix(low, `\\?\harddiskvolume`) || strings.HasPrefix(low, `\??\`) {
			return fmt.Errorf("不支持设备路径")
		}
	}
	if s.Root != "" {
		if abs, err := filepath.Abs(s.Root); err == nil {
			if rel, err := filepath.Rel(abs, filepath.Clean(p)); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return fmt.Errorf("不能导入应用自身的数据目录")
			}
		}
	}
	return nil
}

// StartPathImport validates the request and starts copying in the background. onDone (optional) runs after the copy
// finished, with the status in state "committing"; it must call SetTask. Returns immediately.
func (s *Service) StartPathImport(owner, batchID string, paths []string, opt PathOptions, onDone func(*PathImport)) (*PathImport, error) {
	if len(paths) == 0 {
		return nil, &Error{400, "INVALID_PATHS", "请提供要导入的文件或文件夹路径"}
	}
	if batchID == "" {
		id, err := NewID()
		if err != nil {
			return nil, err
		}
		batchID = id
	}
	if !validUUID(batchID) {
		return nil, &Error{400, "INVALID_ARGUMENT", "batch_id 必须是 UUID（小写）"}
	}
	if err := os.MkdirAll(s.stagingRoot(), 0700); err != nil {
		return nil, err
	}
	if err := s.ensureBatch(batchID, owner, true); err != nil {
		return nil, err
	}
	s.stageMu.Lock()
	if s.copies == nil {
		s.copies = map[string]*PathImport{}
	}
	if old := s.copies[batchID]; old != nil && old.Running() {
		s.stageMu.Unlock()
		return nil, &Error{409, "IMPORT_RUNNING", "该批次的本机路径导入仍在进行"}
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &PathImport{cancel: cancel}
	now := time.Now().UTC()
	p.st = PathStatus{BatchID: batchID, State: "scanning", Results: []PathResult{}, StartedAt: now, UpdatedAt: now}
	s.copies[batchID] = p
	s.stageMu.Unlock()
	go func() {
		defer func() {
			if v := recover(); v != nil {
				p.update(func(st *PathStatus) { st.State, st.Error = "failed", "内部错误" })
			}
		}()
		s.runPathImport(ctx, p, owner, batchID, paths, opt)
		st := p.Status()
		if st.State == "running" {
			p.update(func(st *PathStatus) { st.State = "committing" })
			if onDone != nil {
				onDone(p)
			}
			if p.Status().State == "committing" {
				p.update(func(st *PathStatus) { st.State = "done" })
			}
		}
	}()
	return p, nil
}

func (s *Service) runPathImport(ctx context.Context, p *PathImport, owner, batchID string, paths []string, opt PathOptions) {
	var files []pathFile
	for _, raw := range paths {
		if ctx.Err() != nil {
			break
		}
		if err := s.ValidateSourcePath(raw); err != nil {
			p.add(PathResult{Path: raw, Status: "failed", Code: "INVALID_PATH", Message: err.Error()})
			continue
		}
		clean := filepath.Clean(raw)
		info, err := os.Stat(clean)
		switch {
		case err != nil:
			p.add(PathResult{Path: raw, Status: "failed", Code: "NOT_FOUND", Message: "无法访问：" + describeFSError(err)})
		case info.IsDir():
			base := filepath.Base(clean)
			if base == "" || base == "." || base == string(filepath.Separator) || strings.HasSuffix(base, ":") || strings.HasSuffix(base, `:\`) {
				base = "导入文件夹"
			}
			s.walkDir(ctx, p, clean, base, []os.FileInfo{info}, 1, opt, &files)
		case info.Mode().IsRegular():
			files = append(files, pathFile{abs: clean, rel: filepath.Base(clean), size: info.Size(), explicit: true})
		default:
			p.add(PathResult{Path: raw, Status: "failed", Code: "NOT_REGULAR", Message: "不是普通文件（设备、管道等不会被导入）"})
		}
	}
	var total int64
	for _, f := range files {
		total += f.size
	}
	p.update(func(st *PathStatus) { st.Discovered, st.BytesTotal, st.State = len(files), total, "running" })
	if s.Limits.TooManyFiles(len(files)) {
		p.update(func(st *PathStatus) {
			st.State, st.Error = "failed", fmt.Sprintf("超过管理员设置的每批文件数上限（%d）", s.Limits.MaxBatchFiles)
		})
		return
	}
	dir := s.batchDir(batchID)
	for _, f := range files {
		if ctx.Err() != nil {
			p.update(func(st *PathStatus) { st.State = "cancelled" })
			return
		}
		p.update(func(st *PathStatus) { st.Current = f.abs })
		if fatal := s.copyOne(ctx, p, dir, batchID, owner, f); fatal {
			return
		}
	}
	p.update(func(st *PathStatus) { st.Current = "" })
	if ctx.Err() != nil {
		p.update(func(st *PathStatus) { st.State = "cancelled" })
	}
}

func describeFSError(err error) string {
	switch {
	case os.IsNotExist(err):
		return "文件不存在"
	case os.IsPermission(err):
		return "没有读取权限"
	}
	return "读取失败"
}

func (s *Service) walkDir(ctx context.Context, p *PathImport, dir, rel string, ancestors []os.FileInfo, depth int, opt PathOptions, out *[]pathFile) {
	if depth > maxWalkDepth {
		p.add(PathResult{Path: dir, Status: "skipped", Code: "TOO_DEEP", Message: "目录层级过深，已跳过"})
		return
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		p.add(PathResult{Path: dir, Status: "failed", Code: "READ_FAILED", Message: "无法读取文件夹：" + describeFSError(err)})
		return
	}
	for _, e := range ents {
		if ctx.Err() != nil {
			return
		}
		name := e.Name()
		full := filepath.Join(dir, name)
		if !opt.IncludeHidden && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "~$")) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			p.add(PathResult{Path: full, Status: "failed", Code: "READ_FAILED", Message: describeFSError(err)})
			continue
		}
		if !opt.IncludeHidden && isHiddenOrSystem(full, info) {
			continue
		}
		mode := info.Mode()
		switch {
		case mode&os.ModeSymlink != 0 || mode&os.ModeIrregular != 0:
			p.add(PathResult{Path: full, Status: "skipped", Code: "LINK", Message: "符号链接/联接点不会被跟随"})
		case mode.IsDir():
			loop := false
			for _, a := range ancestors {
				if os.SameFile(a, info) {
					loop = true
				}
			}
			if loop {
				p.add(PathResult{Path: full, Status: "skipped", Code: "LOOP", Message: "目录循环，已跳过"})
				continue
			}
			s.walkDir(ctx, p, full, rel+"/"+name, append(ancestors[:len(ancestors):len(ancestors)], info), depth+1, opt, out)
		case mode.IsRegular():
			if _, ok := mimeTypes[strings.ToLower(path.Ext(name))]; !ok {
				p.add(PathResult{Path: full, Status: "skipped", Code: "UNSUPPORTED_FORMAT", Message: "格式不支持，未导入", Size: info.Size()})
				continue
			}
			*out = append(*out, pathFile{abs: full, rel: rel + "/" + name, size: info.Size()})
		default:
			p.add(PathResult{Path: full, Status: "skipped", Code: "NOT_REGULAR", Message: "不是普通文件"})
		}
	}
}

type countWriter struct {
	w io.Writer
	n *atomic.Int64
}

func (c countWriter) Write(b []byte) (int, error) {
	n, err := c.w.Write(b)
	c.n.Add(int64(n))
	return n, err
}

// copyOne stages one file; it returns true only for errors that make every further copy pointless (disk full).
func (s *Service) copyOne(ctx context.Context, p *PathImport, dir, batchID, owner string, f pathFile) (fatal bool) {
	rel := f.rel
	if ValidatePath(rel) != nil {
		rel = safeFailurePath(0, f.rel)
		if ValidatePath(rel) != nil {
			rel = "file" + safeExt(f.abs)
		}
	}
	name := path.Base(rel)
	ext := strings.ToLower(path.Ext(name))
	fid, err := NewID()
	if err != nil {
		p.add(PathResult{Path: f.abs, Status: "failed", Code: "INTERNAL", Message: "内部错误"})
		return false
	}
	now := s.Limits.now().UTC()
	reject := func(code, msg string, size int64) {
		m := sessionMeta{Session: Session{ID: batchID + "_" + fid, BatchID: batchID, Name: name, RelPath: rel, Size: size, Complete: true, Source: "path", CreatedAt: now, UpdatedAt: now, Code: code, Message: msg}, OwnerName: owner}
		_ = writeJSONAtomic(filepath.Join(dir, fid+".json"), m)
		p.add(PathResult{Path: f.abs, Status: "failed", Code: code, Message: msg, Size: size})
	}
	if _, ok := mimeTypes[ext]; !ok {
		reject("UNSUPPORTED_FORMAT", "暂不支持此文件格式", f.size)
		return false
	}
	if f.size == 0 {
		reject("EMPTY_FILE", "文件为空", 0)
		return false
	}
	if s.Limits.TooLarge(f.size) {
		reject("FILE_TOO_LARGE", "超过管理员设置的单文件上限 "+HumanBytes(s.Limits.MaxUploadBytes), f.size)
		return false
	}
	if err := s.Limits.CheckSpace(s.stagingRoot(), f.size); err != nil {
		p.add(PathResult{Path: f.abs, Status: "failed", Code: "INSUFFICIENT_STORAGE", Message: err.(*Error).Message, Size: f.size})
		p.update(func(st *PathStatus) { st.State, st.Error = "failed", err.(*Error).Message })
		return true
	}
	src, err := os.Open(f.abs) // read-only
	if err != nil {
		reject("READ_FAILED", "无法读取："+describeFSError(err), f.size)
		return false
	}
	defer src.Close()
	partial := filepath.Join(dir, fid+".part")
	dst, err := os.OpenFile(partial, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		reject("INTERNAL", "内部错误", f.size)
		return false
	}
	h := sha256.New()
	before := p.bytes.Load()
	n, copyErr := io.Copy(io.MultiWriter(dst, h, countWriter{io.Discard, &p.bytes}), &ctxReader{ctx, src})
	syncErr := dst.Sync()
	closeErr := dst.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil {
		os.Remove(partial)
		p.bytes.Store(before)
		for _, e := range []error{copyErr, syncErr, closeErr} {
			if IsNoSpace(e) {
				ne := NoSpaceError(f.size, 0)
				p.add(PathResult{Path: f.abs, Status: "failed", Code: "INSUFFICIENT_STORAGE", Message: ne.Message, Size: f.size})
				p.update(func(st *PathStatus) { st.State, st.Error = "failed", ne.Message })
				return true
			}
		}
		if ctx.Err() == nil {
			reject("READ_FAILED", "复制时读取失败："+describeFSError(copyErr), f.size)
		}
		return false
	}
	if n == 0 {
		os.Remove(partial)
		reject("EMPTY_FILE", "文件为空", 0)
		return false
	}
	final := filepath.Join(dir, fid+safeExt(name))
	if err := os.Rename(partial, final); err != nil {
		os.Remove(partial)
		reject("INTERNAL", "内部错误", n)
		return false
	}
	m := sessionMeta{Session: Session{ID: batchID + "_" + fid, BatchID: batchID, Name: name, RelPath: rel, Size: n, SHA256: hex.EncodeToString(h.Sum(nil)), Complete: true, Source: "path", CreatedAt: now, UpdatedAt: now}, OwnerName: owner, Ext: safeExt(name)}
	if err := writeJSONAtomic(filepath.Join(dir, fid+".json"), m); err != nil {
		os.Remove(final)
		reject("INTERNAL", "内部错误", n)
		return false
	}
	p.add(PathResult{Path: f.abs, Status: "copied", Size: n})
	return false
}
