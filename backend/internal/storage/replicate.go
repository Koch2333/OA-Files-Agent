package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"path"
	"strings"
	"sync"
	"time"

	"oa-agent/backend/internal/cloud"
	"oa-agent/backend/internal/diag"
	"oa-agent/backend/internal/logx"
	"oa-agent/backend/internal/store"
)

const (
	maxAttempts   = 6
	maxAltNames   = 50
	reconcileEach = 15 * time.Second
	pollEach      = 2 * time.Second
)

// Replicate queues every archived file that has no replication row for the connection yet (manual run).
// Existing rows are untouched, so repeated clicks never duplicate work.
func (s *Service) Replicate(ctx context.Context, id, prefix, actor string) (int64, error) {
	c, err := s.cfg.Store.GetConn(ctx, id)
	if err != nil {
		return 0, err
	}
	if c.Replicate == "off" || !c.Enabled {
		return 0, store.ErrState
	}
	prefix = strings.Trim(prefix, "/")
	if prefix != "" {
		if prefix, err = cloud.CleanRel(prefix); err != nil {
			return 0, translate(err)
		}
	}
	n, err := s.cfg.Store.EnqueueMissing(ctx, id, prefix, nil)
	if err != nil {
		return 0, err
	}
	s.cfg.Store.Audit(ctx, actor, "storage.replicate", id, fmt.Sprintf("将 %d 个已归档文件加入同步队列（连接「%s」）", n, c.Name))
	return n, nil
}

func (s *Service) ListReplications(ctx context.Context, p store.Page, connID, status string) ([]store.Replication, int, error) {
	return s.cfg.Store.ListReplications(ctx, p, connID, status)
}

func (s *Service) RetryReplication(ctx context.Context, id string) (store.Replication, error) {
	return s.cfg.Store.RetryReplication(ctx, id)
}

// Run drives replication until ctx ends: it re-queues interrupted items, queues newly archived files for
// connections with the automatic policy and uploads queued items with a small worker pool.
func (s *Service) Run(ctx context.Context) {
	if n, err := s.cfg.Store.RecoverReplications(ctx); err != nil {
		log.Printf("storage: recover replications: %v", err)
	} else if n > 0 {
		log.Printf("storage: re-queued %d interrupted replications", n)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		diag.Loop(ctx, "storage.reconcile", s.reconcileLoop)
	}()
	for i := 0; i < s.cfg.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			diag.Loop(ctx, "storage.work", s.workLoop)
		}()
	}
	wg.Wait()
	s.mu.Lock()
	ids := make([]string, 0, len(s.cache))
	for id := range s.cache {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	for _, id := range ids {
		s.drop(id)
	}
}

func (s *Service) reconcileLoop(ctx context.Context) {
	t := time.NewTicker(reconcileEach)
	defer t.Stop()
	for {
		conns, err := s.cfg.Store.AutoConnections(ctx)
		if err != nil && ctx.Err() == nil {
			log.Printf("storage: reconcile: %v", err)
		}
		for _, c := range conns {
			// only files archived after the policy was switched on: history is added explicitly (manual run)
			if _, err := s.cfg.Store.EnqueueMissing(ctx, c.ID, "", c.AutoSince); err != nil && ctx.Err() == nil {
				log.Printf("storage: enqueue for %s: %v", c.ID, err)
			}
		}
		s.reap()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Service) workLoop(ctx context.Context) {
	t := time.NewTicker(pollEach)
	defer t.Stop()
	for {
		for {
			r, err := s.cfg.Store.ClaimReplication(ctx)
			if err != nil {
				if !errors.Is(err, store.ErrNotFound) && ctx.Err() == nil {
					log.Printf("storage: claim: %v", err)
				}
				break
			}
			// A panic fails this one replication with a numbered message; the loop and the process live on.
			if id := diag.SafeD("storage.replicate", map[string]any{"replication_id": r.ID, "connection_id": r.ConnectionID, "ledger_id": r.LedgerID}, func() { s.replicateOne(ctx, r) }); id != "" {
				s.fail(ctx, r, errors.New("internal error"), diag.UserMessage(id))
			}
			if ctx.Err() != nil {
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func backoff(attempt int) time.Duration {
	d := 30 * time.Second << (attempt - 1)
	if d > 30*time.Minute || d <= 0 {
		d = 30 * time.Minute
	}
	return d
}

func msg(s string) *string { return &s }

// permanent reports errors that retrying cannot fix (the user must change credentials/permissions and retry by hand).
func permanent(err error) bool {
	return errors.Is(err, cloud.ErrAuth) || errors.Is(err, cloud.ErrPermission) || errors.Is(err, cloud.ErrUnsupported) || errors.Is(err, cloud.ErrTooLarge) || errors.Is(err, store.ErrNotFound)
}

func (s *Service) fail(ctx context.Context, r store.Replication, err error, userMsg string) {
	ctx = context.WithoutCancel(ctx)
	if !permanent(err) && r.Attempts < maxAttempts {
		at := time.Now().Add(backoff(r.Attempts))
		_ = s.cfg.Store.FinishReplication(ctx, r.ID, "queued", msg(userMsg+"（将自动重试）"), "", &at)
		return
	}
	_ = s.cfg.Store.FinishReplication(ctx, r.ID, "failed", msg(userMsg), "", nil)
	diag.Report(ctx, diag.Entry{Source: "storage", Code: "REPLICATION_FAILED", Message: userMsg + ": " + err.Error(),
		Details: map[string]any{"replication_id": r.ID, "connection_id": r.ConnectionID, "ledger_id": r.LedgerID, "attempts": r.Attempts}})
}

// replicateOne copies one archived file to the remote storage. It never overwrites and never deletes:
// an existing remote file of the same size counts as already present; a different one gets a numbered name.
func (s *Service) replicateOne(ctx context.Context, r store.Replication) {
	logx.Debugf("replicate start: id=%s conn=%s attempt=%d", r.ID, r.ConnectionID, r.Attempts)
	bg := context.WithoutCancel(ctx)
	conn, err := s.cfg.Store.GetConn(ctx, r.ConnectionID)
	if err != nil {
		_ = s.cfg.Store.FinishReplication(bg, r.ID, "failed", msg("连接已被删除"), "", nil)
		return
	}
	cur, status, err := s.cfg.Store.LedgerForReplication(ctx, r.LedgerID)
	if err != nil || status != "archived" {
		_ = s.cfg.Store.FinishReplication(bg, r.ID, "skipped", msg("该文件已从台账移除或撤销归档，未同步"), "", nil)
		return
	}
	f, fi, err := s.cfg.Archive.Open(cur)
	if err != nil {
		s.fail(ctx, r, store.ErrNotFound, "本地归档文件不存在或无法读取")
		return
	}
	defer f.Close()
	// integrity gate: what is uploaded must be exactly what was archived
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		s.fail(ctx, r, err, "读取本地文件失败")
		return
	}
	if hex.EncodeToString(h.Sum(nil)) != r.SHA256 {
		s.fail(ctx, r, store.ErrNotFound, "本地文件内容与归档时不一致（可能已被修改），已停止同步")
		return
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		s.fail(ctx, r, err, "读取本地文件失败")
		return
	}
	lock := s.connLock(conn.ID)
	lock.Lock()
	defer lock.Unlock()
	cctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	p, err := s.provider(cctx, conn)
	if err != nil {
		s.evict(conn.ID)
		s.fail(ctx, r, err, userMsgOf(err))
		return
	}
	target := r.RemotePath
	for n := 1; ; n++ {
		ent, serr := p.Stat(cctx, target)
		if serr != nil && !errors.Is(serr, cloud.ErrNotFound) {
			s.evict(conn.ID)
			s.fail(ctx, r, serr, userMsgOf(serr))
			return
		}
		if serr == nil {
			if !ent.IsDir && ent.Size == fi.Size() {
				_ = s.cfg.Store.FinishReplication(bg, r.ID, "skipped", msg("远端已存在同名且大小一致的文件，未重复上传"), target, nil)
				return
			}
			if n > maxAltNames {
				s.fail(ctx, r, cloud.ErrExists, "远端已存在同名文件且无法找到可用的新名称")
				return
			}
			target = altName(r.RemotePath, n+1)
			continue
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			s.fail(ctx, r, err, "读取本地文件失败")
			return
		}
		perr := p.PutNew(cctx, target, f, fi.Size())
		if errors.Is(perr, cloud.ErrExists) { // lost a race: pick the next name
			if n > maxAltNames {
				s.fail(ctx, r, perr, "远端已存在同名文件且无法找到可用的新名称")
				return
			}
			target = altName(r.RemotePath, n+1)
			continue
		}
		if perr != nil {
			s.evict(conn.ID)
			s.fail(ctx, r, perr, userMsgOf(perr))
			return
		}
		break
	}
	ent, err := p.Stat(cctx, target)
	if err != nil || ent.Size != fi.Size() {
		s.fail(ctx, r, cloud.ErrUnsupported, "上传后校验失败：远端文件大小与本地不一致，请检查远端后重试")
		return
	}
	_ = s.cfg.Store.FinishReplication(bg, r.ID, "done", nil, target, nil)
}

func userMsgOf(err error) string {
	var hk *cloud.HostKeyError
	if errors.As(err, &hk) {
		return "SSH 主机密钥尚未确认或已变化：请在连接设置中重新测试并核对指纹"
	}
	if h, ok := err.(interface{ Hint() string }); ok && h.Hint() != "" {
		return h.Hint()
	}
	var ve *ValidationError
	if errors.As(err, &ve) {
		return ve.Msg
	}
	return cloud.Describe(err)
}

// altName inserts " (n)" before the extension: "a/b.pdf" → "a/b (2).pdf".
func altName(p string, n int) string {
	dir, name := path.Split(p)
	ext := path.Ext(name)
	if ext == name { // dot-file such as ".hidden": no extension
		ext = ""
	}
	stem := strings.TrimSuffix(name, ext)
	return fmt.Sprintf("%s%s (%d)%s", dir, stem, n, ext)
}
