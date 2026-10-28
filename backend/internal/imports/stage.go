package imports

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"oa-agent/backend/internal/formats"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Staging: files arrive in a per-batch staging directory (resumable chunked uploads, local-path copies) and become a
// task only when the batch is committed. Layout under <spool>/staging/<batch>/:
//
//	batch.json        owner + creation time
//	<file>.json       session metadata (name, declared size/sha256, state)
//	<file>.part       bytes received so far (append only; its size IS the resume offset)
//	<file><ext>       finished file (after complete)
//	committed.json    idempotency tombstone left after a commit
//
// Upload ids are "<batch uuid>_<file uuid>" so a request can find its directory without an index.

const stagingDir = "staging"

// Session is one resumable upload (or one staged local file).
type Session struct {
	ID        string    `json:"id"`
	BatchID   string    `json:"batch_id"`
	Owner     string    `json:"-"`
	Name      string    `json:"name"`
	RelPath   string    `json:"path"`
	Size      int64     `json:"size"`
	Offset    int64     `json:"offset"`
	SHA256    string    `json:"sha256,omitempty"` // declared by the client (optional) until complete, then the server's digest
	Complete  bool      `json:"complete"`
	Source    string    `json:"source"` // upload | path
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	ExpiresAt time.Time `json:"expires_at"`
	ChunkSize int64     `json:"chunk_size,omitempty"`
	Code      string    `json:"code,omitempty"` // set when the file was rejected at intake (unsupported, empty, unreadable, ...)
	Message   string    `json:"message,omitempty"`
}

type sessionMeta struct {
	Session
	OwnerName string `json:"owner"`
	Ext       string `json:"ext"`
	Declared  string `json:"declared_sha256,omitempty"`
}

type batchMeta struct {
	Owner   string    `json:"owner"`
	Created time.Time `json:"created"`
}

// OffsetError is returned by AppendChunk when the client's offset is ahead of what the server holds.
type OffsetError struct{ Current int64 }

func (e *OffsetError) Error() string { return "offset mismatch" }

func (s *Service) stagingRoot() string { return filepath.Join(s.Root, stagingDir) }
func (s *Service) batchDir(batch string) string {
	return filepath.Join(s.stagingRoot(), batch)
}

func validUUID(v string) bool {
	if len(v) != 36 {
		return false
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// ValidBatchID reports whether v is an acceptable (client supplied) batch id: a lowercase UUID.
func ValidBatchID(v string) bool { return validUUID(v) }

func splitUploadID(id string) (batch, file string, ok bool) {
	if len(id) != 73 || id[36] != '_' {
		return "", "", false
	}
	batch, file = id[:36], id[37:]
	return batch, file, validUUID(batch) && validUUID(file)
}

func (s *Service) sessionLock(id string) *sync.Mutex {
	s.stageMu.Lock()
	defer s.stageMu.Unlock()
	if s.stageLoc == nil {
		s.stageLoc = map[string]*sync.Mutex{}
	}
	m := s.stageLoc[id]
	if m == nil {
		m = &sync.Mutex{}
		s.stageLoc[id] = m
	}
	return m
}

func (s *Service) dropLock(id string) {
	s.stageMu.Lock()
	delete(s.stageLoc, id)
	s.stageMu.Unlock()
}

func writeJSONAtomic(file string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, raw, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

func readJSON(file string, v any) error {
	raw, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

func safeExt(name string) string {
	ext := strings.ToLower(path.Ext(name))
	if len(ext) > 12 {
		return ""
	}
	for _, c := range ext[min(1, len(ext)):] {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return ""
		}
	}
	return ext
}

// ensureBatch creates the staging directory of a batch (owner recorded) or checks ownership of an existing one.
func (s *Service) ensureBatch(batch, owner string, create bool) error {
	dir := s.batchDir(batch)
	var bm batchMeta
	if err := readJSON(filepath.Join(dir, "batch.json"), &bm); err == nil {
		if bm.Owner != owner {
			return &Error{403, "FORBIDDEN", "该导入批次属于其他用户"}
		}
		return nil
	}
	if !create {
		return &Error{404, "NOT_FOUND", "导入批次不存在或已过期"}
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	return writeJSONAtomic(filepath.Join(dir, "batch.json"), batchMeta{Owner: owner, Created: s.Limits.now().UTC()})
}

// CreateUploadReq describes one file about to be uploaded.
type CreateUploadReq struct {
	BatchID string
	Name    string // base file name
	Path    string // relative path including the name (defaults to Name)
	Size    int64
	SHA256  string // optional, verified at complete
}

func (s *Service) fileCount(batch string) int {
	ents, _ := os.ReadDir(s.batchDir(batch))
	n := 0
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".json") && e.Name() != "batch.json" && e.Name() != "committed.json" {
			n++
		}
	}
	return n
}

// CreateUpload opens a resumable upload session. Only the disk-space check and the optional operator caps can refuse it.
func (s *Service) CreateUpload(owner string, req CreateUploadReq) (Session, error) {
	if req.BatchID == "" {
		id, err := NewID()
		if err != nil {
			return Session{}, err
		}
		req.BatchID = id
	}
	if !validUUID(req.BatchID) {
		return Session{}, &Error{400, "INVALID_ARGUMENT", "batch_id 必须是 UUID（小写）"}
	}
	rel := req.Path
	if rel == "" {
		rel = req.Name
	}
	if req.Name == "" || ValidatePath(req.Name) != nil || strings.Contains(req.Name, "/") || ValidatePath(rel) != nil || path.Base(rel) != req.Name {
		return Session{}, &Error{400, "INVALID_PATH", "文件名或相对路径无效"}
	}
	if req.Size < 0 {
		return Session{}, &Error{400, "INVALID_ARGUMENT", "size 不能为负"}
	}
	if req.SHA256 != "" {
		if _, err := hex.DecodeString(req.SHA256); err != nil || len(req.SHA256) != 64 {
			return Session{}, &Error{400, "INVALID_ARGUMENT", "sha256 必须是 64 位十六进制"}
		}
		req.SHA256 = strings.ToLower(req.SHA256)
	}
	if s.Limits.TooLarge(req.Size) {
		return Session{}, &Error{413, "FILE_TOO_LARGE", "超过管理员设置的单文件上限 " + HumanBytes(s.Limits.MaxUploadBytes)}
	}
	if err := os.MkdirAll(s.stagingRoot(), 0700); err != nil {
		return Session{}, err
	}
	if err := s.Limits.CheckSpace(s.stagingRoot(), req.Size); err != nil {
		return Session{}, err
	}
	if err := s.ensureBatch(req.BatchID, owner, true); err != nil {
		return Session{}, err
	}
	if _, err := os.Stat(filepath.Join(s.batchDir(req.BatchID), "committed.json")); err == nil {
		return Session{}, &Error{409, "BATCH_COMMITTED", "该批次已提交，请使用新的 batch_id"}
	}
	n := s.fileCount(req.BatchID)
	if s.Limits.TooManyFiles(n + 1) {
		return Session{}, &Error{413, "TOO_MANY_FILES", fmt.Sprintf("超过管理员设置的每批文件数上限（%d）", s.Limits.MaxBatchFiles)}
	}
	if n >= s.Limits.maxSessions() {
		return Session{}, &Error{429, "TOO_MANY_UPLOADS", "同时打开的上传会话过多，请稍后再试"}
	}
	fid, err := NewID()
	if err != nil {
		return Session{}, err
	}
	now := s.Limits.now().UTC()
	m := sessionMeta{Session: Session{ID: req.BatchID + "_" + fid, BatchID: req.BatchID, Name: req.Name, RelPath: rel, Size: req.Size, Source: "upload", CreatedAt: now, UpdatedAt: now}, OwnerName: owner, Ext: safeExt(req.Name), Declared: req.SHA256}
	dir := s.batchDir(req.BatchID)
	f, err := os.OpenFile(filepath.Join(dir, fid+".part"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return Session{}, err
	}
	f.Close()
	if err := writeJSONAtomic(filepath.Join(dir, fid+".json"), m); err != nil {
		os.Remove(filepath.Join(dir, fid+".part"))
		return Session{}, err
	}
	return s.view(m, 0), nil
}

func (s *Service) view(m sessionMeta, offset int64) Session {
	v := m.Session
	v.Offset = offset
	if m.Complete {
		v.Offset = m.Size
	}
	if m.Declared != "" && !m.Complete {
		v.SHA256 = m.Declared
	}
	v.ExpiresAt = v.UpdatedAt.Add(s.Limits.ttl())
	v.ChunkSize = DefaultChunkBytes
	return v
}

func (s *Service) loadSession(owner, id string) (sessionMeta, string, error) {
	batch, file, ok := splitUploadID(id)
	if !ok {
		return sessionMeta{}, "", &Error{404, "NOT_FOUND", "上传会话不存在或已过期"}
	}
	if err := s.ensureBatch(batch, owner, false); err != nil {
		return sessionMeta{}, "", err
	}
	var m sessionMeta
	dir := s.batchDir(batch)
	if err := readJSON(filepath.Join(dir, file+".json"), &m); err != nil {
		return m, "", &Error{404, "NOT_FOUND", "上传会话不存在或已过期"}
	}
	return m, filepath.Join(dir, file), nil
}

func (s *Service) received(m sessionMeta, base string) int64 {
	if m.Complete {
		return m.Size
	}
	st, err := os.Stat(base + ".part")
	if err != nil {
		return 0
	}
	return st.Size()
}

// GetUpload returns the session with the offset the server holds (resume point).
func (s *Service) GetUpload(owner, id string) (Session, error) {
	m, base, err := s.loadSession(owner, id)
	if err != nil {
		return Session{}, err
	}
	return s.view(m, s.received(m, base)), nil
}

// AppendChunk appends body at offset. A retry of bytes the server already holds is accepted (the overlap is skipped), so
// resending a chunk after a lost response is idempotent; an offset beyond the received length is an *OffsetError.
func (s *Service) AppendChunk(ctx context.Context, owner, id string, offset int64, body io.Reader, contentLength int64) (Session, error) {
	m, base, err := s.loadSession(owner, id)
	if err != nil {
		return Session{}, err
	}
	if offset < 0 {
		return Session{}, &Error{400, "INVALID_ARGUMENT", "offset 无效"}
	}
	lk := s.sessionLock(id)
	if !lk.TryLock() {
		return Session{}, &Error{409, "UPLOAD_BUSY", "该上传正在被另一个请求写入，请稍后查询 offset 后重试"}
	}
	defer lk.Unlock()
	if m.Complete {
		if offset >= m.Size {
			return s.view(m, m.Size), nil
		}
		return s.view(m, m.Size), &OffsetError{Current: m.Size}
	}
	have := s.received(m, base)
	if offset > have {
		return s.view(m, have), &OffsetError{Current: have}
	}
	if skip := have - offset; skip > 0 {
		if _, err := io.CopyN(io.Discard, body, skip); err != nil {
			if errors.Is(err, io.EOF) { // the whole chunk was already stored
				return s.view(m, have), nil
			}
			return s.view(m, have), &Error{400, "UPLOAD_FAILED", "读取上传内容失败"}
		}
	}
	room := m.Size - have
	if room <= 0 {
		// everything is stored; any extra byte is a client error
		var one [1]byte
		if n, _ := body.Read(one[:]); n > 0 {
			return s.view(m, have), &Error{422, "CHUNK_EXCEEDS_SIZE", "上传内容超过声明的文件大小"}
		}
		return s.view(m, have), nil
	}
	need := contentLength - (have - offset)
	if need < 0 || contentLength < 0 {
		need = 0
	}
	if need > room {
		need = room
	}
	if err := s.Limits.CheckSpace(s.stagingRoot(), need); err != nil {
		return s.view(m, have), err
	}
	f, err := os.OpenFile(base+".part", os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return Session{}, err
	}
	overflow := false
	n, copyErr := io.Copy(f, io.LimitReader(&ctxReader{ctx, body}, room+1))
	if n > room { // too many bytes: keep exactly the declared size
		overflow = true
		n = room
		copyErr = &Error{422, "CHUNK_EXCEEDS_SIZE", "上传内容超过声明的文件大小"}
	}
	syncErr := f.Sync()
	f.Close()
	if overflow {
		_ = os.Truncate(base+".part", m.Size)
	}
	now := s.Limits.now().UTC()
	m.UpdatedAt = now
	_ = writeJSONAtomic(base+".json", m)
	cur := have + n
	if st, e := os.Stat(base + ".part"); e == nil {
		cur = st.Size()
	}
	switch {
	case IsNoSpace(copyErr) || IsNoSpace(syncErr):
		return s.view(m, cur), NoSpaceError(need, 0)
	case copyErr != nil:
		var ie *Error
		if errors.As(copyErr, &ie) {
			return s.view(m, cur), ie
		}
		return s.view(m, cur), &Error{400, "UPLOAD_FAILED", "上传中断；请查询 offset 后从该位置继续"}
	case syncErr != nil:
		return s.view(m, cur), syncErr
	}
	return s.view(m, cur), nil
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// CompleteUpload verifies size (and the declared sha256), computes the server's digest and finalises the file.
func (s *Service) CompleteUpload(ctx context.Context, owner, id string) (Session, error) {
	m, base, err := s.loadSession(owner, id)
	if err != nil {
		return Session{}, err
	}
	lk := s.sessionLock(id)
	if !lk.TryLock() {
		return Session{}, &Error{409, "UPLOAD_BUSY", "该上传正在被另一个请求写入"}
	}
	defer lk.Unlock()
	if m.Complete {
		return s.view(m, m.Size), nil
	}
	have := s.received(m, base)
	if have != m.Size {
		return s.view(m, have), &OffsetError{Current: have}
	}
	f, err := os.Open(base + ".part")
	if err != nil {
		return Session{}, err
	}
	h := sha256.New()
	_, err = io.Copy(h, &ctxReader{ctx, f})
	f.Close()
	if err != nil {
		return Session{}, err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if m.Declared != "" && m.Declared != sum {
		os.Remove(base + ".part")
		os.Remove(base + ".json")
		s.dropLock(id)
		return Session{}, &Error{422, "CHECKSUM_MISMATCH", "文件校验失败（SHA-256 与声明不一致），请重新上传该文件"}
	}
	if err := os.Rename(base+".part", base+m.Ext); err != nil {
		return Session{}, err
	}
	m.Complete, m.SHA256, m.UpdatedAt = true, sum, s.Limits.now().UTC()
	if err := writeJSONAtomic(base+".json", m); err != nil {
		return Session{}, err
	}
	return s.view(m, m.Size), nil
}

// AbortUpload deletes the session and its bytes.
func (s *Service) AbortUpload(owner, id string) error {
	m, base, err := s.loadSession(owner, id)
	if err != nil {
		return err
	}
	lk := s.sessionLock(id)
	lk.Lock()
	defer lk.Unlock()
	os.Remove(base + ".part")
	os.Remove(base + m.Ext)
	os.Remove(base + ".json")
	s.dropLock(id)
	return nil
}

// BatchStatus lists a staging batch (sessions + local-path import progress).
type BatchStatus struct {
	BatchID   string      `json:"batch_id"`
	Files     []Session   `json:"files"`
	Committed bool        `json:"committed"`
	PathScan  *PathStatus `json:"path_import,omitempty"`
}

func (s *Service) stagedSessions(batch string) ([]sessionMeta, error) {
	dir := s.batchDir(batch)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []sessionMeta
	for _, e := range ents {
		n := e.Name()
		if !strings.HasSuffix(n, ".json") || n == "batch.json" || n == "committed.json" {
			continue
		}
		var m sessionMeta
		if readJSON(filepath.Join(dir, n), &m) == nil {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// GetBatch returns the staged files of a batch.
func (s *Service) GetBatch(owner, batch string) (BatchStatus, error) {
	if !validUUID(batch) {
		return BatchStatus{}, &Error{404, "NOT_FOUND", "导入批次不存在或已过期"}
	}
	if err := s.ensureBatch(batch, owner, false); err != nil {
		return BatchStatus{}, err
	}
	ms, err := s.stagedSessions(batch)
	if err != nil {
		return BatchStatus{}, err
	}
	st := BatchStatus{BatchID: batch, Files: []Session{}}
	if _, e := os.Stat(filepath.Join(s.batchDir(batch), "committed.json")); e == nil {
		st.Committed = true
	}
	for _, m := range ms {
		_, file, _ := splitUploadID(m.ID)
		st.Files = append(st.Files, s.view(m, s.received(m, filepath.Join(s.batchDir(batch), file))))
	}
	if p := s.pathImport(batch); p != nil {
		ps := p.Status()
		st.PathScan = &ps
	}
	return st, nil
}

// DiscardBatch removes a staging batch and everything in it.
func (s *Service) DiscardBatch(owner, batch string) error {
	if !validUUID(batch) {
		return &Error{404, "NOT_FOUND", "导入批次不存在或已过期"}
	}
	if err := s.ensureBatch(batch, owner, false); err != nil {
		return err
	}
	if p := s.pathImport(batch); p != nil {
		p.Cancel()
	}
	return os.RemoveAll(s.batchDir(batch))
}

type committedMeta struct {
	Fingerprint string `json:"fingerprint"`
	TemplateID  string `json:"template_id"`
}

// CommitStaged turns the completed files of a staging batch into one task (same rules as a multipart import: unsupported,
// empty or (optionally) oversized files become per-file failures). Incomplete uploads are not ignored: the call is refused
// with 409 UPLOADS_INCOMPLETE so no file is silently dropped.
func (s *Service) CommitStaged(ctx context.Context, owner, batchID, key, templateID string) (batch Batch, err error) {
	if !validUUID(batchID) {
		return batch, &Error{404, "NOT_FOUND", "导入批次不存在或已过期"}
	}
	dirStage := s.batchDir(batchID)
	// Replay of an already committed batch (same Idempotency-Key): the store answers from the fingerprint kept in the tombstone.
	var cm committedMeta
	if readJSON(filepath.Join(dirStage, "committed.json"), &cm) == nil {
		if err = s.ensureBatch(batchID, owner, false); err != nil {
			return batch, err
		}
		if key == "" || cm.TemplateID != templateID {
			return batch, &Error{409, "BATCH_COMMITTED", "该批次已提交"}
		}
		saved, replay, e := s.Store.Save(ctx, Batch{Key: key, Fingerprint: cm.Fingerprint, TemplateID: templateID})
		if e != nil {
			return batch, e
		}
		saved.Replay = replay
		return saved, nil
	}
	if err = s.ensureBatch(batchID, owner, false); err != nil {
		return batch, err
	}
	if p := s.pathImport(batchID); p != nil && p.Running() {
		return batch, &Error{409, "IMPORT_RUNNING", "本机路径导入仍在进行"}
	}
	ms, err := s.stagedSessions(batchID)
	if err != nil {
		return batch, err
	}
	if len(ms) == 0 {
		return batch, &Error{400, "INVALID_PATHS", "批次中没有文件"}
	}
	for _, m := range ms {
		if !m.Complete {
			return batch, &Error{409, "UPLOADS_INCOMPLETE", "仍有文件未上传完成，请先完成或取消"}
		}
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
	type move struct{ from, to string }
	var moves []move
	committed := false
	defer func() {
		if committed {
			return
		}
		for i := len(moves) - 1; i >= 0; i-- { // put the files back so the client can retry
			_ = os.Rename(moves[i].to, moves[i].from)
		}
		os.RemoveAll(dir)
	}()
	now := time.Now().UTC()
	batch = Batch{Key: key, TemplateID: templateID, Job: Job{ID: id, Status: "queued", TotalFiles: len(ms), CreatedAt: now, UpdatedAt: now}, Files: []File{}, Failures: []Failure{}}
	applyAI(&batch.Job, AIFrom(ctx))
	fp := sha256.New()
	fmt.Fprintf(fp, "staged:%s:", batchID)
	if ai := AIFrom(ctx); ai.Fingerprint != "" {
		fmt.Fprintf(fp, "opts:%d:%s;", len(ai.Fingerprint), ai.Fingerprint)
	}
	var drop []string
	for i, m := range ms {
		_, file, _ := splitUploadID(m.ID)
		src := filepath.Join(dirStage, file+m.Ext)
		fmt.Fprintf(fp, "%d:%s:%d:%s;", len(m.RelPath), m.RelPath, m.Size, m.SHA256)
		fail := func(code, msg, mime string) {
			batch.Failures = append(batch.Failures, Failure{Index: i, SourcePath: m.RelPath, Code: code, Message: msg, SHA256: m.SHA256, SizeBytes: m.Size, MIMEType: mime})
			drop = append(drop, src, filepath.Join(dirStage, file+".json"))
		}
		mime := formats.MIME(m.Name)
		switch {
		case m.Code != "":
			fail(m.Code, m.Message, "application/octet-stream")
		case s.Limits.TooLarge(m.Size):
			fail("FILE_TOO_LARGE", "超过管理员设置的单文件上限 "+HumanBytes(s.Limits.MaxUploadBytes), mime)
		default:
			stored := filepath.Join(dir, file+m.Ext)
			if err = os.Rename(src, stored); err != nil {
				return batch, err
			}
			moves = append(moves, move{src, stored})
			batch.Files = append(batch.Files, File{ID: file, TaskID: id, SourceName: m.Name, SourcePath: m.RelPath, MIMEType: mime, SHA256: m.SHA256, SizeBytes: m.Size, Evidence: []string{}, ReviewRequired: true, ArchiveStatus: "pending", CreatedAt: now, StoredPath: stored})
		}
	}
	consumed := s.expandArchives(dir, &batch) // ZIPs are replaced by the files inside them (see archive_expand.go)
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
	if committed {
		for _, p := range consumed {
			_ = os.Remove(p) // the expanded archives; kept until now so a failed save could be retried
		}
	}
	if replay {
		// the earlier task owns its copies; ours are removed by the deferred cleanup (moves are not restored on replay)
		moves = nil
	}
	saved.Replay = replay
	for _, d := range drop {
		os.Remove(d)
	}
	// leave a tombstone (idempotent retry) and drop everything else
	if ents, e := os.ReadDir(dirStage); e == nil {
		for _, en := range ents {
			if en.Name() != "batch.json" {
				os.RemoveAll(filepath.Join(dirStage, en.Name()))
			}
		}
	}
	_ = writeJSONAtomic(filepath.Join(dirStage, "committed.json"), committedMeta{Fingerprint: batch.Fingerprint, TemplateID: templateID}) // kept until GC so status polling and idempotent retries still work
	if replay {
		os.RemoveAll(dir)
	}
	return saved, nil
}

// GC removes abandoned staging batches (no activity for Limits.SessionTTL) and returns how many were removed.
func (s *Service) GC() int {
	ents, err := os.ReadDir(s.stagingRoot())
	if err != nil {
		return 0
	}
	cutoff := s.Limits.now().Add(-s.Limits.ttl())
	removed := 0
	for _, e := range ents {
		if !e.IsDir() || !validUUID(e.Name()) {
			continue
		}
		if p := s.pathImport(e.Name()); p != nil && p.Running() {
			continue
		}
		dir := filepath.Join(s.stagingRoot(), e.Name())
		newest := time.Time{}
		if st, e := os.Stat(dir); e == nil {
			newest = st.ModTime()
		}
		sub, _ := os.ReadDir(dir)
		for _, f := range sub {
			if info, e := f.Info(); e == nil && info.ModTime().After(newest) {
				newest = info.ModTime()
			}
		}
		if newest.Before(cutoff) {
			if os.RemoveAll(dir) == nil {
				removed++
			}
		}
	}
	return removed
}

// StartGC runs GC at startup and then hourly until ctx ends. Stale ".part" files of crashed multipart imports in the
// spool root ("import-*" directories older than a day) are removed as well.
func (s *Service) StartGC(ctx context.Context) {
	run := func() {
		s.GC()
		s.sweepStaleImports()
	}
	run()
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			run()
		}
	}
}

// sweepStaleImports removes "import-*" directories that never made it into the database (crash during intake):
// they contain only .part files and are older than the session TTL.
func (s *Service) sweepStaleImports() {
	ents, _ := os.ReadDir(s.Root)
	cutoff := s.Limits.now().Add(-s.Limits.ttl())
	for _, e := range ents {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "import-") {
			continue
		}
		dir := filepath.Join(s.Root, e.Name())
		sub, _ := os.ReadDir(dir)
		onlyParts := len(sub) > 0
		for _, f := range sub {
			if !strings.HasSuffix(f.Name(), ".part") {
				onlyParts = false
			}
		}
		if !onlyParts {
			continue
		}
		if st, err := os.Stat(dir); err == nil && st.ModTime().Before(cutoff) {
			os.RemoveAll(dir)
		}
	}
}
