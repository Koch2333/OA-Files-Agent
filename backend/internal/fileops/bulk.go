package fileops

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"oa-agent/backend/internal/fsman"
	"oa-agent/backend/internal/store"
)

// Policy is what to do when a name (or content) collides.
type Policy string

const (
	PolicyAsk     Policy = "ask"     // change nothing, return the structured conflict list
	PolicySkip    Policy = "skip"    // leave the conflicting item where it is
	PolicyRename  Policy = "rename"  // keep both: the incoming item gets " (2)", " (3)" ...
	PolicyReplace Policy = "replace" // the existing file goes to the recoverable trash first, then the new one is placed
)

// ParsePolicy validates a request value ("" = def).
func ParsePolicy(s string, def Policy) (Policy, bool) {
	switch Policy(s) {
	case "":
		return def, true
	case PolicyAsk, PolicySkip, PolicyRename, PolicyReplace:
		return Policy(s), true
	}
	return "", false
}

// Conflict kinds.
const (
	KindNameExists       = "name_exists"       // same name, different content (identical content is a silent 'duplicate')
	KindTypeConflict     = "type_conflict"     // a file meets a folder (never replaced, in either direction)
	KindFolderExists     = "folder_exists"     // restore of a folder onto an existing folder (moves merge instead)
	KindDuplicateContent = "duplicate_content" // other name, same SHA-256 already archived nearby
)

// Item statuses.
const (
	StMoved     = "moved"
	StRenamed   = "renamed"
	StReplaced  = "replaced"
	StSkipped   = "skipped"
	StDuplicate = "duplicate" // identical content already there: nothing was copied, the source stays
	StPartial   = "partial"
	StFailed    = "failed"
)

// MoveSpec is one selected item and its final destination path (folder + name).
type MoveSpec struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// MoveRequest is a bulk move. Resolutions (key: the selected item's path) override OnConflict per item.
type MoveRequest struct {
	Items       []MoveSpec
	OnConflict  Policy
	Resolutions map[string]Policy
}

// FileInfo describes one side of a conflict.
type FileInfo struct {
	Path        string    `json:"path"`
	Name        string    `json:"name"`
	IsDir       bool      `json:"is_dir"`
	SizeBytes   int64     `json:"size_bytes"`
	ModifiedAt  time.Time `json:"modified_at"`
	SHA256      string    `json:"sha256,omitempty"`
	TaskRelated bool      `json:"task_related"`
}

// Conflict is one collision found by the all-or-nothing preflight.
type Conflict struct {
	Item      string    `json:"item"` // selected source path: the key for per-item resolutions
	Kind      string    `json:"kind"`
	Name      string    `json:"name"`
	Incoming  FileInfo  `json:"incoming"`
	Existing  *FileInfo `json:"existing,omitempty"`
	Identical bool      `json:"identical"` // same SHA-256 (informational; identical same-name files never block, they are 'duplicate')
	Message   string    `json:"message"`
	Choices   []string  `json:"choices"` // policies that make sense for this conflict
}

// Counts are per-leaf outcomes (a merged folder has many leaves).
type Counts struct {
	Moved     int `json:"moved"`
	Renamed   int `json:"renamed"`
	Replaced  int `json:"replaced"`
	Skipped   int `json:"skipped"`
	Duplicate int `json:"duplicate"`
	Failed    int `json:"failed"`
}

// ItemResult is the outcome for one selected item.
type ItemResult struct {
	Path            string   `json:"path"`
	Status          string   `json:"status"`
	FinalPath       string   `json:"final_path,omitempty"`
	Code            string   `json:"code,omitempty"`
	Message         string   `json:"message,omitempty"`
	LedgerUpdated   int64    `json:"ledger_updated"`
	HashStatus      string   `json:"hash_status"` // verified | modified_outside | not_task
	ModifiedOutside []string `json:"modified_outside,omitempty"`
	DuplicateOf     string   `json:"duplicate_of,omitempty"`
	Counts          Counts   `json:"counts"`
}

// BatchResult is what a bulk operation returns. Conflicts is non-empty only when nothing was changed.
type BatchResult struct {
	Items     []ItemResult `json:"items"`
	Summary   Counts       `json:"summary"`
	Conflicts []Conflict   `json:"conflicts"`
}

// Leaf actions.
const (
	actMove    = "move"
	actSkip    = "skip"
	actDup     = "dup"
	actFail    = "fail"
	actCleanup = "cleanup"
)

type leaf struct {
	root     int
	from, to string
	isDir    bool
	action   string
	rename   bool // keep both: to was made unique
	replace  *fsman.Entry
	dupOf    string
	code     string
	msg      string
	// result
	status  string
	final   string
	ledger  int64
	hash    []string // modified-outside paths
	tasked  bool
	checked bool
}

type rootState struct {
	spec   MoveSpec
	policy Policy
	res    ItemResult
	done   bool // decided before planning leaves (failed / skipped)
	leaves []*leaf
	merged string // destination folder when the item merged into an existing folder
}

type movePlan struct {
	ctx       context.Context
	s         *Service
	roots     []*rootState
	conflicts []Conflict
	taken     map[string]bool
	destRows  map[string]map[string][]store.LedgerHash // dest folder -> sha -> rows already archived there
	moving    map[string]bool                          // source paths of this batch
}

func baseOf(rel string) string { return path.Base(rel) }
func dirOf(rel string) string {
	d := path.Dir(rel)
	if d == "." {
		return ""
	}
	return d
}

func takenKey(rel string) string { return fsman.NameKey(rel) }

func inside(p, dir string) bool { return p != dir && strings.HasPrefix(p, dir+"/") }

func (s *Service) fileInfo(ctx context.Context, e fsman.Entry, withHash bool, row *store.LedgerHash) FileInfo {
	fi := FileInfo{Path: e.Path, Name: e.Name, IsDir: e.IsDir, SizeBytes: e.SizeBytes, ModifiedAt: e.ModifiedAt, TaskRelated: row != nil}
	if withHash && !e.IsDir {
		if sha, _, _, err := s.hashOf(ctx, e.Path, row); err == nil {
			fi.SHA256 = sha
		}
	}
	return fi
}

func (s *Service) rowFor(ctx context.Context, rel string) *store.LedgerHash {
	m, err := s.Store.LedgerHashes(ctx, []string{rel})
	if err != nil {
		return nil
	}
	if r, ok := m[rel]; ok {
		return &r
	}
	return nil
}

// uniqueName returns "stem (n).ext" (n >= 2) for the first n whose name is free in dir on disk and in this batch.
func (p *movePlan) uniqueName(dir, name string, isDir bool) (string, error) {
	stem, ext := name, ""
	if !isDir {
		ext = path.Ext(name)
		stem = strings.TrimSuffix(name, ext)
		if stem == "" {
			stem, ext = name, ""
		}
	}
	for n := 2; n <= 1000; n++ {
		cand := fmt.Sprintf("%s (%d)%s", stem, n, ext)
		rel := cand
		if dir != "" {
			rel = dir + "/" + cand
		}
		if p.taken[takenKey(rel)] {
			continue
		}
		if _, found, err := p.s.FS.Lookup(dir, cand); err != nil {
			return "", err
		} else if !found {
			return cand, nil
		}
	}
	return "", fsman.ErrConflict
}

func (p *movePlan) destHashes(dir string) map[string][]store.LedgerHash {
	if m, ok := p.destRows[dir]; ok {
		return m
	}
	m := map[string][]store.LedgerHash{}
	if rows, err := p.s.Store.LedgerHashesUnder(p.ctx, dir); err == nil {
		for _, r := range rows {
			m[r.SHA256] = append(m[r.SHA256], r)
		}
	}
	p.destRows[dir] = m
	return m
}

func (p *movePlan) addConflict(r *rootState, c Conflict) {
	c.Item = r.spec.From
	p.conflicts = append(p.conflicts, c)
}

// planPair decides what happens to src when it is to be placed at to.
func (p *movePlan) planPair(ri int, r *rootState, src fsman.Entry, to string) error {
	if err := p.ctx.Err(); err != nil {
		return err
	}
	s := p.s
	dir, name := dirOf(to), baseOf(to)
	ex, found, err := s.FS.Lookup(dir, name)
	if err != nil {
		return err
	}
	add := func(l *leaf) { l.root = ri; r.leaves = append(r.leaves, l) }

	if !found {
		key := takenKey(to)
		if p.taken[key] {
			return p.batchCollision(ri, r, src, to)
		}
		// same content under another name in the destination folder: warn, never drop silently
		if !src.IsDir {
			if dupPath, sha := p.contentNeighbour(r, src, dir); dupPath != "" {
				switch r.policy {
				case PolicyAsk:
					other, _ := s.FS.Stat(dupPath)
					oi := s.fileInfo(p.ctx, other, false, nil)
					oi.SHA256, oi.TaskRelated = sha, true
					in := s.fileInfo(p.ctx, src, false, nil)
					in.SHA256 = sha
					p.addConflict(r, Conflict{Kind: KindDuplicateContent, Name: name, Incoming: in, Existing: &oi, Identical: true,
						Message: fmt.Sprintf("目标文件夹中已有内容完全相同的文件“%s”（SHA-256 相同，名称不同）", dupPath), Choices: []string{"skip", "rename"}})
					return nil
				case PolicySkip:
					add(&leaf{from: src.Path, to: to, action: actDup, dupOf: dupPath, msg: "目标中已有相同内容的文件：" + dupPath})
					return nil
				}
			}
		}
		p.taken[key] = true
		add(&leaf{from: src.Path, to: to, isDir: src.IsDir, action: actMove})
		return nil
	}

	// the "existing" entry is the source itself under another spelling: a case-only rename
	if ex.Path == src.Path {
		p.taken[takenKey(to)] = true
		add(&leaf{from: src.Path, to: to, isDir: src.IsDir, action: actMove})
		return nil
	}

	if src.IsDir && ex.IsDir {
		// merge: every child is placed into the existing folder with the same policy
		r.merged = ex.Path
		children, err := s.FS.List(src.Path)
		if err != nil {
			return err
		}
		for _, c := range children {
			if err := p.planPair(ri, r, c, ex.Path+"/"+c.Name); err != nil {
				return err
			}
		}
		add(&leaf{from: src.Path, to: ex.Path, action: actCleanup, isDir: true})
		return nil
	}

	exRow := (*store.LedgerHash)(nil)
	if !ex.IsDir {
		exRow = s.rowFor(p.ctx, ex.Path)
	}
	srcRow := (*store.LedgerHash)(nil)
	if !src.IsDir {
		srcRow = s.rowFor(p.ctx, src.Path)
	}
	c := Conflict{Name: name, Message: ""}
	typeConflict := src.IsDir != ex.IsDir || s.FS.IsSymlink(ex.Path)
	switch {
	case typeConflict:
		c.Kind = KindTypeConflict
		c.Message = "类型冲突：文件与文件夹同名，不会互相替换"
		c.Choices = []string{"skip", "rename"}
	default:
		c.Kind = KindNameExists
		c.Message = "目标位置已有同名文件，内容不同"
		c.Choices = []string{"skip", "rename", "replace"}
	}
	withHash := !typeConflict && (r.policy == PolicyAsk || src.SizeBytes == ex.SizeBytes)
	c.Incoming = s.fileInfo(p.ctx, src, withHash, srcRow)
	exi := s.fileInfo(p.ctx, ex, withHash, exRow)
	c.Existing = &exi
	if !typeConflict && c.Incoming.SHA256 != "" && c.Incoming.SHA256 == exi.SHA256 {
		c.Identical = true
		if r.policy != PolicyRename {
			// same name, same hash: an identical duplicate. Never a second copy; the source stays untouched.
			add(&leaf{from: src.Path, to: to, action: actDup, dupOf: ex.Path, msg: "目标位置已有相同内容的同名文件，未重复放入"})
			return nil
		}
	}
	switch r.policy {
	case PolicyAsk:
		p.addConflict(r, c)
	case PolicySkip:
		add(&leaf{from: src.Path, to: to, action: actSkip, msg: "已跳过：" + c.Message})
	case PolicyRename:
		nn, err := p.uniqueName(dir, name, src.IsDir)
		if err != nil {
			return err
		}
		final := nn
		if dir != "" {
			final = dir + "/" + nn
		}
		p.taken[takenKey(final)] = true
		add(&leaf{from: src.Path, to: final, isDir: src.IsDir, action: actMove, rename: true})
	case PolicyReplace:
		if typeConflict {
			add(&leaf{from: src.Path, to: to, action: actFail, code: "TYPE_CONFLICT", msg: "文件与文件夹同名，不能互相替换（请选择跳过或保留两者）"})
			return nil
		}
		e := ex
		p.taken[takenKey(to)] = true
		add(&leaf{from: src.Path, to: to, action: actMove, replace: &e})
	}
	return nil
}

func (p *movePlan) batchCollision(ri int, r *rootState, src fsman.Entry, to string) error {
	add := func(l *leaf) { l.root = ri; r.leaves = append(r.leaves, l) }
	switch r.policy {
	case PolicyAsk:
		p.addConflict(r, Conflict{Kind: KindNameExists, Name: baseOf(to), Incoming: p.s.fileInfo(p.ctx, src, false, nil),
			Message: "所选项目中有另一项移动后会得到同样的名称", Choices: []string{"skip", "rename"}})
	case PolicySkip:
		add(&leaf{from: src.Path, to: to, action: actSkip, msg: "已跳过：同批次中另一项目标相同"})
	case PolicyRename:
		nn, err := p.uniqueName(dirOf(to), baseOf(to), src.IsDir)
		if err != nil {
			return err
		}
		final := nn
		if d := dirOf(to); d != "" {
			final = d + "/" + nn
		}
		p.taken[takenKey(final)] = true
		add(&leaf{from: src.Path, to: final, isDir: src.IsDir, action: actMove, rename: true})
	default:
		add(&leaf{from: src.Path, to: to, action: actFail, code: "BATCH_DUPLICATE", msg: "同一批次中另一项目标相同，不能替换"})
	}
	return nil
}

// contentNeighbour finds an archived file with the same SHA-256 as src below the destination folder.
func (p *movePlan) contentNeighbour(r *rootState, src fsman.Entry, destDir string) (string, string) {
	rows := p.destHashes(destDir)
	if len(rows) == 0 {
		return "", ""
	}
	row := p.s.rowFor(p.ctx, src.Path)
	sha, _, _, err := p.s.hashOf(p.ctx, src.Path, row)
	if err != nil {
		return "", ""
	}
	for _, h := range rows[sha] {
		if h.Path != src.Path && !p.moving[h.Path] {
			if _, err := p.s.FS.Stat(h.Path); err == nil {
				return h.Path, sha
			}
		}
	}
	return "", ""
}

// MoveBatch moves/renames the selected items with an explicit conflict policy.
//
// The preflight reads and hashes but changes nothing; with unresolved conflicts it returns them (Conflicts
// non-empty, Items empty). Otherwise every item is executed with the no-overwrite primitives of fsman, so
// anything that appears between the check and the write is kept and reported instead of clobbered.
func (s *Service) MoveBatch(ctx context.Context, req MoveRequest, actor string) (BatchResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if req.OnConflict == "" {
		req.OnConflict = PolicyAsk
	}
	p := &movePlan{ctx: ctx, s: s, taken: map[string]bool{}, destRows: map[string]map[string][]store.LedgerHash{}, moving: map[string]bool{}}
	for _, it := range req.Items {
		p.moving[it.From] = true
	}
	for i, it := range req.Items {
		pol := req.OnConflict
		if v, ok := req.Resolutions[it.From]; ok && v != "" {
			pol = v
		}
		r := &rootState{spec: it, policy: pol, res: ItemResult{Path: it.From, HashStatus: HashNotTask}}
		p.roots = append(p.roots, r)
		p.prepareRoot(i, r, req.Items)
	}
	if len(p.conflicts) > 0 {
		sort.SliceStable(p.conflicts, func(i, j int) bool { return p.conflicts[i].Incoming.Path < p.conflicts[j].Incoming.Path })
		return BatchResult{Items: []ItemResult{}, Conflicts: p.conflicts}, nil
	}
	if err := ctx.Err(); err != nil {
		return BatchResult{}, err
	}
	out := BatchResult{Items: make([]ItemResult, 0, len(p.roots)), Conflicts: []Conflict{}}
	for _, r := range p.roots {
		if !r.done {
			s.executeRoot(ctx, r, actor)
		}
		out.Items = append(out.Items, r.res)
		out.Summary.add(r.res.Counts)
	}
	if len(req.Items) == 1 && len(out.Items) == 1 && out.Items[0].Status != StFailed {
		it := out.Items[0]
		s.Store.Audit(ctx, actor, "fs.move", it.Path, fmt.Sprintf("移动/重命名为 %s（%s，同步台账 %d 条）", it.FinalPath, it.Status, it.LedgerUpdated))
	} else {
		s.Store.Audit(ctx, actor, "fs.bulk_move", fmt.Sprintf("%d 项", len(req.Items)), fmt.Sprintf("批量移动：移动 %d，保留两者 %d，替换 %d，跳过 %d，重复 %d，失败 %d（策略 %s）",
			out.Summary.Moved, out.Summary.Renamed, out.Summary.Replaced, out.Summary.Skipped, out.Summary.Duplicate, out.Summary.Failed, req.OnConflict))
	}
	return out, nil
}

func (c *Counts) add(o Counts) {
	c.Moved += o.Moved
	c.Renamed += o.Renamed
	c.Replaced += o.Replaced
	c.Skipped += o.Skipped
	c.Duplicate += o.Duplicate
	c.Failed += o.Failed
}

func (r *rootState) finish(status, code, msg string) {
	r.done = true
	r.res.Status, r.res.Code, r.res.Message = status, code, msg
	switch status {
	case StFailed:
		r.res.Counts.Failed = 1
	case StSkipped:
		r.res.Counts.Skipped = 1
	}
}

func (p *movePlan) prepareRoot(ri int, r *rootState, all []MoveSpec) {
	s := p.s
	it := r.spec
	if err := fsman.Check(it.From, false); err != nil {
		r.finish(StFailed, "INVALID_PATH", "路径无效")
		return
	}
	if err := fsman.Check(it.To, false); err != nil {
		r.finish(StFailed, "INVALID_PATH", "目标路径无效")
		return
	}
	src, err := s.FS.Stat(it.From)
	if err != nil {
		r.finish(StFailed, "NOT_FOUND", "文件或文件夹不存在")
		return
	}
	if s.FS.IsSymlink(it.From) {
		r.finish(StFailed, "INVALID_PATH", "不管理符号链接")
		return
	}
	for _, o := range all {
		if o.From != it.From && inside(it.From, o.From) {
			if oe, err := s.FS.Stat(o.From); err == nil && oe.IsDir {
				r.finish(StSkipped, "", "已包含在所选文件夹中")
				return
			}
		}
	}
	if it.From == it.To {
		r.finish(StSkipped, "", "已在目标位置")
		return
	}
	if src.IsDir && (it.To == it.From || inside(it.To, it.From)) {
		r.finish(StFailed, "INVALID_PATH", "不能把文件夹移动到它自身内部")
		return
	}
	if d := dirOf(it.To); d != "" {
		if pe, err := s.FS.Stat(d); err != nil || !pe.IsDir {
			r.finish(StFailed, "NOT_FOUND", "目标文件夹不存在")
			return
		}
	}
	if err := p.planPair(ri, r, src, it.To); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			r.finish(StFailed, "CANCELLED", "已取消")
			return
		}
		r.finish(StFailed, "FAILED", errText(err))
	}
}

func errText(err error) string {
	switch {
	case errors.Is(err, fsman.ErrNotFound):
		return "文件或文件夹不存在"
	case errors.Is(err, fsman.ErrExists):
		return "目标在检查之后被创建，已保留，未覆盖"
	case errors.Is(err, fsman.ErrInvalid), errors.Is(err, fsman.ErrOutside):
		return "路径无效"
	}
	return "操作失败：" + err.Error()
}

func errCode(err error) string {
	switch {
	case errors.Is(err, fsman.ErrNotFound):
		return "NOT_FOUND"
	case errors.Is(err, fsman.ErrExists):
		return "ALREADY_EXISTS"
	case errors.Is(err, fsman.ErrInvalid), errors.Is(err, fsman.ErrOutside):
		return "INVALID_PATH"
	}
	return "FAILED"
}

// ---- execution ----

type preFile struct {
	path string
	size int64
	mod  time.Time
	row  store.LedgerHash
	ok   bool // hash matched the ledger before the move
}

// preVerify hashes the task-related files below from (file or folder) before they move.
func (s *Service) preVerify(ctx context.Context, from string, isDir bool) ([]preFile, error) {
	var rows []store.LedgerHash
	var err error
	if isDir {
		rows, err = s.Store.LedgerHashesUnder(ctx, from)
	} else if m, merr := s.Store.LedgerHashes(ctx, []string{from}); merr == nil {
		if r, ok := m[from]; ok {
			rows = []store.LedgerHash{r}
		}
	} else {
		err = merr
	}
	if err != nil {
		return nil, err
	}
	var out []preFile
	for _, r := range rows {
		e, err := s.FS.Stat(r.Path)
		if err != nil || e.IsDir {
			continue // missing files have nothing to verify (and nothing to move)
		}
		fc, err := s.verifyDisk(ctx, r, false)
		if err != nil {
			return nil, err
		}
		out = append(out, preFile{path: r.Path, size: e.SizeBytes, mod: e.ModifiedAt, row: r, ok: fc.Status == CheckMatch})
	}
	return out, nil
}

// postVerify confirms the moved files kept size and mtime (a rename or link never changes content) and
// transfers the verified digests to the new paths; anything that changed is re-hashed against the ledger.
func (s *Service) postVerify(ctx context.Context, pre []preFile, from, final string) (modified []string) {
	for _, pf := range pre {
		newPath := final + strings.TrimPrefix(pf.path, from)
		s.hc.drop(pf.path)
		e, err := s.FS.Stat(newPath)
		if err != nil {
			continue
		}
		if e.SizeBytes == pf.size && e.ModifiedAt.Equal(pf.mod) {
			if pf.ok {
				s.hc.put(newPath, e.SizeBytes, e.ModifiedAt, pf.row.SHA256)
			} else if sha, _, _, herr := s.FS.HashFile(ctx, newPath); herr == nil {
				s.hc.put(newPath, e.SizeBytes, e.ModifiedAt, sha)
				modified = append(modified, newPath)
			}
			continue
		}
		sha, size, mod, herr := s.FS.HashFile(ctx, newPath)
		if herr == nil {
			s.hc.put(newPath, size, mod, sha)
			if sha != pf.row.SHA256 {
				modified = append(modified, newPath)
			}
		}
	}
	return modified
}

func (s *Service) executeRoot(ctx context.Context, r *rootState, actor string) {
	var cleanups []*leaf
	for _, l := range r.leaves {
		if l.action == actCleanup {
			cleanups = append(cleanups, l)
			continue
		}
		s.executeLeaf(ctx, l, actor)
	}
	for i := len(cleanups) - 1; i >= 0; i-- {
		_ = s.FS.RemoveEmptyDir(cleanups[i].from) // only succeeds when everything left it
	}
	res := &r.res
	tasked, bad, nLeaves := 0, 0, 0
	for _, l := range r.leaves {
		if l.action == actCleanup {
			continue
		}
		nLeaves++
		switch l.status {
		case StMoved:
			res.Counts.Moved++
		case StRenamed:
			res.Counts.Renamed++
		case StReplaced:
			res.Counts.Replaced++
		case StSkipped:
			res.Counts.Skipped++
		case StDuplicate:
			res.Counts.Duplicate++
			if res.DuplicateOf == "" {
				res.DuplicateOf = l.dupOf
			}
		case StFailed:
			res.Counts.Failed++
			if res.Code == "" {
				res.Code, res.Message = l.code, l.msg
			}
		}
		res.LedgerUpdated += l.ledger
		if l.tasked {
			tasked++
		}
		if len(l.hash) > 0 {
			bad += len(l.hash)
			res.ModifiedOutside = append(res.ModifiedOutside, l.hash...)
		}
		if l.msg != "" && res.Message == "" && l.status != StFailed {
			res.Message = l.msg
		}
	}
	c := res.Counts
	total := c.Moved + c.Renamed + c.Replaced + c.Skipped + c.Duplicate + c.Failed
	switch {
	case total == 0:
		res.Status = StMoved
	case c.Failed == total:
		res.Status = StFailed
	case c.Skipped+c.Duplicate+c.Failed > 0 && c.Moved+c.Renamed+c.Replaced+c.Skipped+c.Duplicate+c.Failed > 0 && c.Moved+c.Renamed+c.Replaced > 0:
		res.Status = StPartial
	case c.Duplicate == total:
		res.Status = StDuplicate
	case c.Skipped+c.Duplicate == total:
		res.Status = StSkipped
	case c.Failed > 0:
		res.Status = StPartial
	case c.Replaced > 0 && c.Moved+c.Renamed == 0 && nLeaves == 1:
		res.Status = StReplaced
	case c.Renamed > 0 && c.Moved+c.Replaced == 0 && nLeaves == 1:
		res.Status = StRenamed
	default:
		res.Status = StMoved
	}
	if r.merged != "" {
		res.FinalPath = r.merged
	} else if len(r.leaves) == 1 {
		res.FinalPath = r.leaves[0].final
	} else if len(r.leaves) > 0 {
		res.FinalPath = r.spec.To
	}
	switch {
	case bad > 0:
		res.HashStatus = "modified_outside"
	case tasked > 0:
		res.HashStatus = "verified"
	default:
		res.HashStatus = HashNotTask
	}
}

func (s *Service) executeLeaf(ctx context.Context, l *leaf, actor string) {
	fail := func(code, msg string) { l.status, l.code, l.msg = StFailed, code, msg }
	switch l.action {
	case actSkip:
		l.status = StSkipped
		return
	case actDup:
		l.status = StDuplicate
		return
	case actFail:
		fail(l.code, l.msg)
		return
	}
	if err := ctx.Err(); err != nil {
		fail("CANCELLED", "已取消")
		return
	}
	pre, err := s.preVerify(ctx, l.from, l.isDir)
	if err != nil {
		fail(errCode(err), errText(err))
		return
	}
	l.tasked = len(pre) > 0
	for _, pf := range pre {
		if !pf.ok {
			l.hash = append(l.hash, pf.path)
		}
	}
	if s.testHookBeforeWrite != nil {
		s.testHookBeforeWrite(l.from, l.to)
	}
	var trashed *fsman.TrashItem
	if l.replace != nil {
		// the existing file goes to the recoverable trash first (never a hard delete)
		it, _, terr := s.Trash(ctx, l.replace.Path, actor)
		if terr != nil {
			fail(errCode(terr), "无法把旧文件移入回收站，已保留："+errText(terr))
			return
		}
		trashed = &it
	}
	undoTrash := func() {
		if trashed != nil {
			if rerr := s.FS.RestoreAs(trashed.ID, trashed.OriginalPath); rerr == nil {
				_, _ = s.Store.LedgerRestore(ctx, trashed.ID, trashed.OriginalPath, trashed.OriginalPath, trashed.IsDir)
			}
		}
	}
	e, merr := s.FS.Move(l.from, l.to)
	if merr != nil {
		undoTrash()
		if errors.Is(merr, fsman.ErrExists) {
			fail("ALREADY_EXISTS", "目标在检查之后被创建，已保留，未覆盖")
		} else {
			fail(errCode(merr), errText(merr))
		}
		return
	}
	n, lerr := s.Store.LedgerMovePath(ctx, l.from, l.to, e.IsDir)
	if lerr != nil {
		if _, uerr := s.FS.Move(l.to, l.from); uerr != nil {
			fail("LEDGER_FAILED", "台账更新失败且移动无法撤销，请联系管理员")
			return
		}
		undoTrash()
		fail("LEDGER_FAILED", "台账更新失败，移动已撤销")
		return
	}
	l.ledger, l.final = n, l.to
	l.hash = append([]string(nil), l.hash...)
	if mod := s.postVerify(ctx, pre, l.from, l.to); len(mod) > 0 {
		l.hash = mod
	} else if len(l.hash) > 0 {
		// the mismatch was already there before the move; report the new locations
		for i, h := range l.hash {
			l.hash[i] = l.to + strings.TrimPrefix(h, l.from)
		}
	}
	for _, h := range l.hash {
		s.Store.Audit(ctx, actor, "fs.hash_mismatch", h, "移动时发现磁盘内容与台账 SHA-256 不一致：文件在系统外被修改，已标记")
	}
	switch {
	case l.replace != nil:
		l.status = StReplaced
	case l.rename:
		l.status = StRenamed
	default:
		l.status = StMoved
	}
}

// ---- bulk trash ----

// BulkTrash moves every selected item to the trash and follows the ledger, item by item (partial failures
// are reported, never rolled back as a whole).
func (s *Service) BulkTrash(ctx context.Context, paths []string, actor string) (BatchResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := BatchResult{Items: make([]ItemResult, 0, len(paths)), Conflicts: []Conflict{}}
	seen := map[string]bool{}
	var kept []string
	for _, p := range paths {
		if seen[p] {
			continue
		}
		seen[p] = true
		kept = append(kept, p)
	}
	covered := map[string]bool{} // decided before anything moves: inside another selected folder
	for _, p := range kept {
		for _, o := range kept {
			if o != p && inside(p, o) {
				if oe, err := s.FS.Stat(o); err == nil && oe.IsDir {
					covered[p] = true
				}
			}
		}
	}
	for _, p := range kept {
		it := ItemResult{Path: p, HashStatus: HashNotTask}
		skip := covered[p]
		if skip {
			it.Status, it.Message, it.Counts.Skipped = StSkipped, "已包含在所选文件夹中", 1
		} else if err := ctx.Err(); err != nil {
			it.Status, it.Code, it.Message, it.Counts.Failed = StFailed, "CANCELLED", "已取消", 1
		} else if t, n, err := s.Trash(ctx, p, actor); err != nil {
			it.Status, it.Code, it.Message, it.Counts.Failed = StFailed, errCode(err), errText(err), 1
		} else {
			it.Status, it.LedgerUpdated, it.Counts.Moved = StMoved, n, 1
			it.FinalPath = ".trash/" + t.ID
		}
		out.Items = append(out.Items, it)
		out.Summary.add(it.Counts)
	}
	s.Store.Audit(ctx, actor, "fs.bulk_trash", fmt.Sprintf("%d 项", len(kept)), fmt.Sprintf("批量删除到回收站：成功 %d，跳过 %d，失败 %d", out.Summary.Moved, out.Summary.Skipped, out.Summary.Failed))
	return out, nil
}

// ---- restore with a conflict policy ----

// RestoreResult is the outcome of RestoreWith.
type RestoreResult struct {
	Status        string     `json:"status"` // restored | renamed | replaced | skipped | duplicate
	Path          string     `json:"path,omitempty"`
	LedgerUpdated int64      `json:"ledger_updated"`
	HashStatus    string     `json:"hash_status"`
	DuplicateOf   string     `json:"duplicate_of,omitempty"`
	Message       string     `json:"message,omitempty"`
	Conflicts     []Conflict `json:"conflicts"`
}

// RestoreWith restores a trashed item to its original place under an explicit policy: same name + same hash is a
// duplicate (the item stays in the trash), same name + other content is a real conflict, and content that is
// already archived under another name is reported as duplicate content. Without conflicts it behaves like Restore.
func (s *Service) RestoreWith(ctx context.Context, id string, pol Policy, actor string) (RestoreResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	res := RestoreResult{Conflicts: []Conflict{}, HashStatus: HashNotTask}
	it, err := s.FS.TrashItem(id)
	if err != nil {
		return res, err
	}
	rows, err := s.Store.LedgerRemovedByTrash(ctx, id)
	if err != nil {
		return res, err
	}
	var inSHA string
	if !it.IsDir {
		if len(rows) == 1 {
			inSHA = rows[0].SHA256
		} else if h, herr := s.FS.HashTrashed(ctx, id); herr == nil {
			inSHA = h
		}
	}
	orig := it.OriginalPath
	dir, name := dirOf(orig), baseOf(orig)
	incoming := FileInfo{Path: ".trash/" + id, Name: it.Name, IsDir: it.IsDir, SizeBytes: it.SizeBytes, ModifiedAt: it.DeletedAt, SHA256: inSHA, TaskRelated: len(rows) > 0}
	ex, found, lerr := s.FS.Lookup(dir, name)
	if lerr != nil && !errors.Is(lerr, fsman.ErrNotFound) {
		return res, lerr
	}
	final := orig
	conflict := func(kind, msg string, e *FileInfo, identical bool, choices ...string) Conflict {
		return Conflict{Item: id, Kind: kind, Name: name, Incoming: incoming, Existing: e, Identical: identical, Message: msg, Choices: choices}
	}
	var replace *fsman.Entry
	if lerr == nil && found {
		var exRow *store.LedgerHash
		if !ex.IsDir {
			exRow = s.rowFor(ctx, ex.Path)
		}
		exi := s.fileInfo(ctx, ex, !ex.IsDir, exRow)
		switch {
		case it.IsDir != ex.IsDir || s.FS.IsSymlink(ex.Path):
			c := conflict(KindTypeConflict, "类型冲突：原位置现在是同名的"+map[bool]string{true: "文件", false: "文件夹"}[it.IsDir]+"，不会互相替换", &exi, false, "skip", "rename")
			switch pol {
			case PolicyAsk:
				res.Conflicts = append(res.Conflicts, c)
				return res, nil
			case PolicySkip:
				return s.restoreSkipped(res, "已跳过：类型冲突"), nil
			case PolicyReplace:
				return res, fmt.Errorf("%w: file and folder cannot replace each other", fsman.ErrInvalid)
			}
		case it.IsDir:
			c := conflict(KindFolderExists, "原位置已有同名文件夹（还原不会合并或替换文件夹）", &exi, false, "skip", "rename")
			switch pol {
			case PolicyAsk:
				res.Conflicts = append(res.Conflicts, c)
				return res, nil
			case PolicySkip:
				return s.restoreSkipped(res, "已跳过：原位置已有同名文件夹"), nil
			case PolicyReplace:
				return res, fmt.Errorf("%w: a folder is never replaced", fsman.ErrInvalid)
			}
		default:
			if inSHA != "" && inSHA == exi.SHA256 && pol != PolicyRename {
				res.Status, res.Path, res.DuplicateOf = StDuplicate, ex.Path, ex.Path
				res.Message = "原位置已有相同内容的同名文件，未重复还原（项目仍在回收站）"
				return res, nil
			}
			c := conflict(KindNameExists, "原位置已有同名文件，内容不同", &exi, false, "skip", "rename", "replace")
			switch pol {
			case PolicyAsk:
				res.Conflicts = append(res.Conflicts, c)
				return res, nil
			case PolicySkip:
				return s.restoreSkipped(res, "已跳过：原位置已有同名文件"), nil
			case PolicyReplace:
				e := ex
				replace = &e
			}
		}
		if pol == PolicyRename {
			p := &movePlan{ctx: ctx, s: s, taken: map[string]bool{}}
			nn, uerr := p.uniqueName(dir, name, it.IsDir)
			if uerr != nil {
				return res, uerr
			}
			final = nn
			if dir != "" {
				final = dir + "/" + nn
			}
			res.Status = StRenamed
		}
	} else if inSHA != "" {
		// not occupied: but is this content already archived somewhere else?
		if dups, derr := s.Store.LedgerByHash(ctx, inSHA, 5); derr == nil {
			for _, d := range dups {
				if _, serr := s.FS.Stat(d.Path); serr != nil {
					continue
				}
				oe, _ := s.FS.Stat(d.Path)
				oi := s.fileInfo(ctx, oe, false, nil)
				oi.SHA256, oi.TaskRelated = d.SHA256, true
				c := conflict(KindDuplicateContent, fmt.Sprintf("相同内容的文件已归档在“%s”（SHA-256 相同，名称或位置不同）", d.Path), &oi, true, "skip", "rename")
				switch pol {
				case PolicyAsk:
					res.Conflicts = append(res.Conflicts, c)
					return res, nil
				case PolicySkip:
					r2 := s.restoreSkipped(res, "已跳过：相同内容已归档在 "+d.Path)
					r2.DuplicateOf = d.Path
					return r2, nil
				}
				res.DuplicateOf = d.Path
				break
			}
		}
	}
	if replace != nil {
		if _, _, terr := s.Trash(ctx, replace.Path, actor); terr != nil {
			return res, terr
		}
		res.Status = StReplaced
	}
	if s.testHookBeforeWrite != nil {
		s.testHookBeforeWrite(".trash/"+id, final)
	}
	if err := s.FS.RestoreAs(id, final); err != nil {
		return res, err
	}
	n, err := s.Store.LedgerRestore(ctx, id, orig, final, it.IsDir)
	if err != nil {
		return res, fmt.Errorf("restored on disk but the ledger update failed: %w", err)
	}
	if res.Status == "" {
		res.Status = "restored"
	}
	res.Path, res.LedgerUpdated = final, n
	// the restored files are task-related again: re-hash them against the ledger
	if rows2, herr := s.restoredRows(ctx, final, it.IsDir); herr == nil && len(rows2) > 0 {
		res.HashStatus = HashVerified
		for _, row := range rows2 {
			if fc, verr := s.verifyDisk(ctx, row, false); verr == nil && fc.Status == CheckMismatch {
				res.HashStatus = "modified_outside"
				s.Store.Audit(ctx, actor, "fs.hash_mismatch", row.Path, "还原时发现磁盘内容与台账 SHA-256 不一致：文件在系统外被修改，已标记")
			}
		}
	}
	s.Store.Audit(ctx, actor, "fs.restore", final, fmt.Sprintf("从回收站恢复（%s，台账恢复 %d 条）", res.Status, n))
	return res, nil
}

func (s *Service) restoredRows(ctx context.Context, rel string, isDir bool) ([]store.LedgerHash, error) {
	if isDir {
		return s.Store.LedgerHashesUnder(ctx, rel)
	}
	m, err := s.Store.LedgerHashes(ctx, []string{rel})
	if err != nil {
		return nil, err
	}
	if r, ok := m[rel]; ok {
		return []store.LedgerHash{r}, nil
	}
	return nil, nil
}

func (s *Service) restoreSkipped(res RestoreResult, msg string) RestoreResult {
	res.Status, res.Message = StSkipped, msg
	return res
}
