package archive

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
)

// Result statuses.
const (
	StatusPublished = "published"
	StatusSkipped   = "skipped_exists_identical"
	StatusFailed    = "failed"
	statusPending   = "pending" // journal-only: intent recorded, publish not confirmed
)

const (
	stagingDir  = ".staging"
	journalDir  = ".journal"
	tmpPrefix   = ".oa-tmp-"
	maxSuffixes = 1000

	stateRunning    = "running"
	stateDone       = "done"
	stateIncomplete = "incomplete"
	stateRolledBack = "rolled_back"
)

// Op is one copy-into-archive operation. SourceStored is the app-owned spool
// file (never moved or deleted); Sha256 is the expected hex digest. All ops of
// one Execute call should share OpID (the idempotency key); if it is empty a
// deterministic ID is derived from the op list.
type Op struct {
	OpID         string `json:"op_id,omitempty"`
	FileID       string `json:"file_id"`
	SourceStored string `json:"source_stored"`
	DestRel      string `json:"dest_rel"`
	Sha256       string `json:"sha256"`
}

// OpResult is the outcome of one Op. CreatedDirs lists directories (relative,
// shallow to deep) this op created, used by Rollback for pruning.
type OpResult struct {
	OpID     string `json:"op_id"`
	FileID   string `json:"file_id"`
	FinalRel string `json:"final_rel"`
	Status   string `json:"status"`
	Err      string `json:"error,omitempty"`
	Sha256   string `json:"sha256"`
	// DuplicateOf is the archive path that already holds identical content (Status skipped_exists_identical): no second copy was made.
	DuplicateOf string   `json:"duplicate_of,omitempty"`
	CreatedDirs []string `json:"created_dirs,omitempty"`
}

// Executor publishes files below Root. Root/.staging holds in-flight copies and
// Root/.journal holds one JSON journal per OpID.
//
// Residual risk (TOCTOU): publishing uses a hard link (atomic, fails if the
// destination exists on NTFS and POSIX), which never replaces. If the
// filesystem cannot hard-link, it falls back to "check, then os.Rename" under
// a process-wide per-destination lock; on Windows os.Rename replaces, so a
// different process creating the same destination in that tiny window could be
// overwritten. Writers outside this process must not target the archive Root.
type Executor struct{ Root string }

type journal struct {
	OpID      string     `json:"op_id"`
	State     string     `json:"state"`
	Ops       []Op       `json:"ops"`
	Results   []OpResult `json:"results"`
	UpdatedAt time.Time  `json:"updated_at"`
}

var linkFile = os.Link // replaceable in tests

// ---- process-wide keyed locks and active-temp registry ----

var locks = struct {
	sync.Mutex
	m map[string]*lockEntry
}{m: map[string]*lockEntry{}}

type lockEntry struct {
	mu   sync.Mutex
	refs int
}

func lockKey(k string) func() {
	locks.Lock()
	e := locks.m[k]
	if e == nil {
		e = &lockEntry{}
		locks.m[k] = e
	}
	e.refs++
	locks.Unlock()
	e.mu.Lock()
	return func() {
		e.mu.Unlock()
		locks.Lock()
		if e.refs--; e.refs == 0 {
			delete(locks.m, k)
		}
		locks.Unlock()
	}
}

var active = struct {
	sync.Mutex
	m map[string]int
}{m: map[string]int{}}

func markActive(p string) func() {
	k := strings.ToLower(p)
	active.Lock()
	active.m[k]++
	active.Unlock()
	return func() {
		active.Lock()
		if active.m[k]--; active.m[k] <= 0 {
			delete(active.m, k)
		}
		active.Unlock()
	}
}

func isActive(p string) bool {
	active.Lock()
	defer active.Unlock()
	return active.m[strings.ToLower(p)] > 0
}

// ---- helpers ----

func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
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

func hashFile(ctx context.Context, p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, ctxReader{ctx, f}); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func contained(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func validOpID(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func batchID(ops []Op) (string, error) {
	id := ops[0].OpID
	for _, o := range ops {
		if o.OpID != id {
			return "", errors.New("同一批操作的 OpID 必须一致")
		}
	}
	if id == "" {
		raw, _ := json.Marshal(ops)
		sum := sha256.Sum256(raw)
		return "auto-" + hex.EncodeToString(sum[:])[:32], nil
	}
	if !validOpID(id) {
		return "", errors.New("OpID 无效")
	}
	return id, nil
}

func failAll(ops []Op, id string, err error) []OpResult {
	res := make([]OpResult, len(ops))
	for i, o := range ops {
		res[i] = OpResult{OpID: id, FileID: o.FileID, Status: StatusFailed, Err: err.Error(), Sha256: o.Sha256}
	}
	return res
}

func (e *Executor) prepare() (realRoot string, err error) {
	if e.Root == "" {
		return "", errors.New("archive root not configured")
	}
	for _, d := range []string{e.Root, filepath.Join(e.Root, stagingDir), filepath.Join(e.Root, journalDir)} {
		if err = os.MkdirAll(d, 0o700); err != nil {
			return "", err
		}
	}
	abs, err := filepath.Abs(e.Root)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

func (e *Executor) journalPath(realRoot, id string) string {
	return filepath.Join(realRoot, journalDir, id+".json")
}

func loadJournal(p string) (*journal, error) {
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var j journal
	if err = json.Unmarshal(raw, &j); err != nil {
		return nil, err
	}
	return &j, nil
}

func saveJournal(p string, j *journal) error {
	j.UpdatedAt = time.Now().UTC()
	raw, err := json.MarshalIndent(j, "", " ")
	if err != nil {
		return err
	}
	tmp := p + "." + newUUID() + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(raw)
	serr := f.Sync()
	cerr := f.Close()
	if err = errors.Join(werr, serr, cerr); err != nil {
		os.Remove(tmp)
		return err
	}
	if err = os.Rename(tmp, p); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// ---- Execute ----

// Execute publishes each op. The returned slice has one result per op, in
// order. Re-running with the same OpID and ops returns the journaled results.
func (e *Executor) Execute(ctx context.Context, ops []Op) []OpResult {
	if len(ops) == 0 {
		return []OpResult{}
	}
	id, err := batchID(ops)
	if err != nil {
		return failAll(ops, "", err)
	}
	realRoot, err := e.prepare()
	if err != nil {
		return failAll(ops, id, err)
	}
	unlock := lockKey("op:" + strings.ToLower(realRoot) + "|" + id)
	defer unlock()

	jp := e.journalPath(realRoot, id)
	j, lerr := loadJournal(jp)
	switch {
	case lerr == nil && !reflect.DeepEqual(normalizeOps(j.Ops, id), normalizeOps(ops, id)):
		return failAll(ops, id, errors.New("OpID 已被不同的操作使用"))
	case lerr == nil && j.State == stateDone && len(j.Results) == len(ops):
		return j.Results
	case lerr != nil || j.State == stateRolledBack || len(j.Results) != len(ops):
		j = &journal{OpID: id, Ops: normalizeOps(ops, id), Results: make([]OpResult, len(ops))}
	}
	j.State = stateRunning
	prior := j.Results
	j.Results = make([]OpResult, len(ops))
	copy(j.Results, prior)
	if err = saveJournal(jp, j); err != nil {
		return failAll(ops, id, fmt.Errorf("journal: %w", err))
	}

	cancelled := false
	for i, op := range ops {
		if ctx.Err() != nil {
			cancelled = true
			if j.Results[i].Status != StatusPublished && j.Results[i].Status != StatusSkipped {
				j.Results[i] = OpResult{OpID: id, FileID: op.FileID, Status: StatusFailed, Err: ctx.Err().Error(), Sha256: op.Sha256, CreatedDirs: j.Results[i].CreatedDirs}
			}
			continue
		}
		p := j.Results[i]
		if (p.Status == StatusPublished || p.Status == StatusSkipped) && e.stillValid(ctx, realRoot, p) {
			continue
		}
		intent := func(r OpResult) {
			r.Status = statusPending
			j.Results[i] = r
			_ = saveJournal(jp, j)
		}
		j.Results[i] = e.executeOne(ctx, realRoot, id, op, p, intent)
		if ctx.Err() != nil {
			cancelled = true
		}
		_ = saveJournal(jp, j)
	}
	if cancelled {
		j.State = stateIncomplete
	} else {
		j.State = stateDone
	}
	if err = saveJournal(jp, j); err != nil && !cancelled {
		// Results are real even if the journal write failed; report via copy.
		out := make([]OpResult, len(j.Results))
		copy(out, j.Results)
		return out
	}
	out := make([]OpResult, len(j.Results))
	copy(out, j.Results)
	return out
}

func normalizeOps(ops []Op, id string) []Op {
	out := make([]Op, len(ops))
	for i, o := range ops {
		o.OpID = id
		out[i] = o
	}
	return out
}

func (e *Executor) stillValid(ctx context.Context, realRoot string, r OpResult) bool {
	if ValidateRelative(r.FinalRel) != nil {
		return false
	}
	h, err := hashFile(ctx, filepath.Join(realRoot, filepath.FromSlash(r.FinalRel)))
	return err == nil && h == r.Sha256
}

func (e *Executor) executeOne(ctx context.Context, realRoot, id string, op Op, prior OpResult, intent func(OpResult)) (res OpResult) {
	res = OpResult{OpID: id, FileID: op.FileID, Sha256: op.Sha256, CreatedDirs: prior.CreatedDirs}
	fail := func(err error) OpResult {
		res.Status = StatusFailed
		res.Err = err.Error()
		res.FinalRel = ""
		return res
	}

	if err := ValidateRelative(op.DestRel); err != nil {
		return fail(fmt.Errorf("目标路径无效: %w", err))
	}
	first := strings.ToLower(strings.SplitN(op.DestRel, "/", 2)[0])
	if strings.HasPrefix(first, ".") {
		return fail(errors.New("目标路径不能位于保留目录"))
	}
	if len(op.Sha256) != 64 {
		return fail(errors.New("缺少期望的 SHA-256"))
	}
	want := strings.ToLower(op.Sha256)

	// Resume: an intent recorded before a crash whose file exists with the right hash.
	if prior.Status == statusPending && prior.FinalRel != "" && e.stillValid(ctx, realRoot, prior) {
		res.FinalRel = prior.FinalRel
		res.Status = StatusPublished
		return res
	}

	// Source: app-owned regular file, never a symlink.
	if !filepath.IsAbs(op.SourceStored) {
		return fail(errors.New("源文件路径必须是绝对路径"))
	}
	li, err := os.Lstat(op.SourceStored)
	if err != nil {
		return fail(fmt.Errorf("源文件不可读: %w", err))
	}
	if li.Mode()&os.ModeSymlink != 0 || !li.Mode().IsRegular() {
		return fail(errors.New("源文件必须是普通文件，不允许符号链接"))
	}

	// Stage: copy + fsync + verify hash.
	stage := filepath.Join(realRoot, stagingDir, newUUID())
	doneActive := markActive(stage)
	defer doneActive()
	defer os.Remove(stage)
	got, err := copyHashed(ctx, op.SourceStored, stage)
	if err != nil {
		return fail(fmt.Errorf("复制到暂存区失败: %w", err))
	}
	if got != want {
		return fail(errors.New("SHA-256 校验失败，源文件与预期不一致"))
	}

	// Destination directory (symlink-safe).
	relDir, relName := splitRel(op.DestRel)
	absDir, created, err := resolveDir(realRoot, relDir)
	res.CreatedDirs = appendUnique(res.CreatedDirs, created...)
	if err != nil {
		return fail(err)
	}

	for n := 1; n <= maxSuffixes; n++ {
		if err = ctx.Err(); err != nil {
			return fail(err)
		}
		name := relName
		if n > 1 {
			name = suffixName(relName, n, relDir)
		}
		dest := filepath.Join(absDir, name)
		finalRel := joinRel(relDir, name)
		unlock := lockKey("dest:" + strings.ToLower(dest))
		status, perr := func() (string, error) {
			defer unlock()
			fi, lerr := os.Lstat(dest)
			switch {
			case lerr == nil:
				if fi.Mode().IsRegular() {
					h, herr := hashFile(ctx, dest)
					if herr != nil {
						return "", herr
					}
					if h == want {
						return StatusSkipped, nil
					}
				}
				return "", fs.ErrExist
			case !errors.Is(lerr, fs.ErrNotExist):
				return "", lerr
			}
			r := res
			r.FinalRel = finalRel
			intent(r)
			if err := publishNoReplace(ctx, stage, dest); err != nil {
				return "", err
			}
			return StatusPublished, nil
		}()
		switch {
		case perr == nil:
			res.FinalRel = finalRel
			res.Status = status
			if status == StatusSkipped {
				res.DuplicateOf = finalRel
			}
			return res
		case errors.Is(perr, fs.ErrExist):
			continue
		default:
			return fail(perr)
		}
	}
	return fail(errors.New("无法生成不冲突的目标文件名"))
}

func splitRel(rel string) (dir, name string) {
	if i := strings.LastIndexByte(rel, '/'); i >= 0 {
		return rel[:i], rel[i+1:]
	}
	return "", rel
}

func appendUnique(dst []string, add ...string) []string {
	for _, a := range add {
		found := false
		for _, d := range dst {
			if d == a {
				found = true
				break
			}
		}
		if !found {
			dst = append(dst, a)
		}
	}
	return dst
}

// resolveDir walks relDir under realRoot creating missing directories, and
// rejects any component whose real path escapes the root (symlinks/junctions).
func resolveDir(realRoot, relDir string) (abs string, created []string, err error) {
	cur := realRoot
	if relDir == "" {
		return cur, nil, nil
	}
	rel := ""
	for _, comp := range strings.Split(relDir, "/") {
		next := filepath.Join(cur, comp)
		rel = joinRel(rel, comp)
		if _, lerr := os.Lstat(next); errors.Is(lerr, fs.ErrNotExist) {
			if merr := os.Mkdir(next, 0o755); merr == nil {
				created = append(created, rel)
			} else if !errors.Is(merr, fs.ErrExist) {
				return "", created, merr
			}
		} else if lerr != nil {
			return "", created, lerr
		}
		real, rerr := filepath.EvalSymlinks(next)
		if rerr != nil {
			return "", created, rerr
		}
		if !contained(realRoot, real) {
			return "", created, errors.New("目标路径越出归档根目录")
		}
		fi, serr := os.Stat(real)
		if serr != nil || !fi.IsDir() {
			return "", created, errors.New("目标路径中存在非目录")
		}
		cur = real
	}
	return cur, created, nil
}

func copyHashed(ctx context.Context, src, dst string) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	if st, err := in.Stat(); err != nil || !st.Mode().IsRegular() {
		return "", errors.New("源文件不是普通文件")
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, cerr := io.Copy(io.MultiWriter(out, h), ctxReader{ctx, in})
	serr := out.Sync()
	clerr := out.Close()
	if err = errors.Join(cerr, serr, clerr); err != nil {
		os.Remove(dst)
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// publishNoReplace moves the staged file to dst without ever replacing an
// existing file; fs.ErrExist means dst already exists. Caller holds the
// per-destination lock.
func publishNoReplace(ctx context.Context, src, dst string) error {
	err := linkFile(src, dst)
	if err == nil {
		syncDir(filepath.Dir(dst))
		return nil
	}
	if errors.Is(err, fs.ErrExist) {
		return fs.ErrExist
	}
	// Hard links unavailable: check-then-rename (see Executor docs for the residual risk).
	if _, lerr := os.Lstat(dst); lerr == nil {
		return fs.ErrExist
	}
	if rerr := os.Rename(src, dst); rerr == nil {
		syncDir(filepath.Dir(dst))
		return nil
	}
	// Likely cross-volume: copy next to the destination, then rename in place.
	tmp := filepath.Join(filepath.Dir(dst), tmpPrefix+newUUID())
	done := markActive(tmp)
	defer done()
	if _, cerr := copyHashed(ctx, src, tmp); cerr != nil {
		return cerr
	}
	if _, lerr := os.Lstat(dst); lerr == nil {
		os.Remove(tmp)
		return fs.ErrExist
	}
	if rerr := os.Rename(tmp, dst); rerr != nil {
		os.Remove(tmp)
		return rerr
	}
	syncDir(filepath.Dir(dst))
	return nil
}

func syncDir(d string) {
	if f, err := os.Open(d); err == nil {
		_ = f.Sync() // unsupported on Windows; best effort
		f.Close()
	}
}

// ---- Recover / Rollback ----

// Recover removes orphaned staging/temp files left by a crash (skipping files
// of Executes running in this process) and returns how many were removed.
// Journals in state "running"/"incomplete" are kept so the same OpID resumes.
func (e *Executor) Recover(ctx context.Context) (int, error) {
	realRoot, err := e.prepare()
	if err != nil {
		return 0, err
	}
	removed := 0
	var errs []error
	entries, err := os.ReadDir(filepath.Join(realRoot, stagingDir))
	if err != nil {
		return 0, err
	}
	for _, en := range entries {
		if ctx.Err() != nil {
			return removed, ctx.Err()
		}
		p := filepath.Join(realRoot, stagingDir, en.Name())
		if isActive(p) {
			continue
		}
		if rerr := os.RemoveAll(p); rerr != nil {
			errs = append(errs, rerr)
		} else {
			removed++
		}
	}
	werr := filepath.WalkDir(realRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() && (d.Name() == stagingDir || d.Name() == journalDir) && filepath.Dir(p) == realRoot {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.HasPrefix(d.Name(), tmpPrefix) && !isActive(p) {
			if os.Remove(p) == nil {
				removed++
			}
		}
		return nil
	})
	errs = append(errs, werr)
	return removed, errors.Join(errs...)
}

// Rollback deletes only files that were published by results (and whose hash
// still matches), then prunes directories those results created if empty.
// Modified or missing files are kept and reported in the returned error.
func (e *Executor) Rollback(ctx context.Context, results []OpResult) error {
	realRoot, err := e.prepare()
	if err != nil {
		return err
	}
	var errs []error
	opIDs := map[string]bool{}
	for i := len(results) - 1; i >= 0; i-- {
		if err = ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		r := results[i]
		if r.OpID != "" {
			opIDs[r.OpID] = true
		}
		if r.Status != StatusPublished {
			continue
		}
		if verr := ValidateRelative(r.FinalRel); verr != nil {
			errs = append(errs, fmt.Errorf("%s: 路径无效: %w", r.FileID, verr))
			continue
		}
		dest := filepath.Join(realRoot, filepath.FromSlash(r.FinalRel))
		parent, perr := filepath.EvalSymlinks(filepath.Dir(dest))
		if perr != nil {
			if errors.Is(perr, fs.ErrNotExist) {
				continue
			}
			errs = append(errs, perr)
			continue
		}
		if !contained(realRoot, parent) {
			errs = append(errs, fmt.Errorf("%s: 路径越出归档根目录", r.FileID))
			continue
		}
		dest = filepath.Join(parent, filepath.Base(dest))
		fi, lerr := os.Lstat(dest)
		if lerr != nil {
			if !errors.Is(lerr, fs.ErrNotExist) {
				errs = append(errs, lerr)
			}
			continue
		}
		if !fi.Mode().IsRegular() {
			errs = append(errs, fmt.Errorf("%s: 目标不是普通文件，已保留", r.FinalRel))
			continue
		}
		unlock := lockKey("dest:" + strings.ToLower(dest))
		h, herr := hashFile(ctx, dest)
		switch {
		case herr != nil:
			errs = append(errs, herr)
		case h != r.Sha256:
			errs = append(errs, fmt.Errorf("%s: 文件已被修改，已保留", r.FinalRel))
		default:
			if rerr := os.Remove(dest); rerr != nil {
				errs = append(errs, rerr)
			}
		}
		unlock()
	}

	var dirs []string
	for _, r := range results {
		for _, d := range r.CreatedDirs {
			if ValidateRelative(d) == nil {
				dirs = appendUnique(dirs, d)
			}
		}
	}
	sort.Slice(dirs, func(a, b int) bool { return strings.Count(dirs[a], "/") > strings.Count(dirs[b], "/") })
	for _, d := range dirs {
		p := filepath.Join(realRoot, filepath.FromSlash(d))
		if real, rerr := filepath.EvalSymlinks(p); rerr != nil || !contained(realRoot, real) || real == realRoot {
			continue
		} else {
			_ = os.Remove(real) // fails (kept) when not empty
		}
	}

	for id := range opIDs {
		if !validOpID(id) {
			continue
		}
		jp := e.journalPath(realRoot, id)
		if j, lerr := loadJournal(jp); lerr == nil {
			j.State = stateRolledBack
			_ = saveJournal(jp, j)
		}
	}
	return errors.Join(errs...)
}
