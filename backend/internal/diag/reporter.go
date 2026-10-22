// Package diag records errors so that somebody can find out what happened: grouped, redacted error reports in
// PostgreSQL (with a log-file fallback), panic-safe goroutine wrappers, crash files and the run lock that detects an
// unclean previous exit. Nothing in here may block or crash the caller.
package diag

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Entry is one error occurrence handed to Report. Message, Stack, Details and User are redacted before they are
// stored. Never put document text or file contents into an Entry: use metadata (file id, size, extension).
type Entry struct {
	Source    string // api, pipeline, worker, storage, client, supervisor, startup, llm
	Severity  string // error (default), panic, fatal
	Code      string
	Message   string
	Stack     string
	RequestID string
	User      string
	TaskID    string
	Details   map[string]any
}

var (
	validSource   = map[string]bool{"api": true, "pipeline": true, "worker": true, "storage": true, "client": true, "supervisor": true, "startup": true, "llm": true}
	validSeverity = map[string]bool{"error": true, "panic": true, "fatal": true}
)

const (
	maxMessage    = 1000
	maxStack      = 8000
	maxDetails    = 8 << 10
	maxOccurrence = 20
)

type occurrence struct {
	at        time.Time
	requestID string
	user      string
}

type record struct {
	id, fp                                 string
	source, severity, code, message, stack string
	requestID, user                        string
	details                                []byte
	at                                     time.Time
}

type agg struct {
	rec      record
	first    time.Time
	count    int
	occ      []occurrence
	attempts int
	spilled  bool
}

// Options tunes a Reporter; zero values select the defaults.
type Options struct {
	QueueSize    int           // in-memory queue (default 1024); beyond it reports are dropped and counted
	FlushEvery   time.Duration // default 500ms
	MaxPending   int           // groups kept in memory while the database is unreachable (default 500)
	FallbackPath string        // JSON-lines file for groups the database did not accept
	Logf         func(format string, args ...any)
	Now          func() time.Time
	RatePerSec   int // global intake limit (default 200/s, burst 2x)
}

type sink interface {
	write(ctx context.Context, a *agg) error
}

// Stats are the reporter's counters.
type Stats struct {
	Queued, Pending          int
	Dropped, Written, Failed int64
	DBAttached               bool
}

type Reporter struct {
	opts     Options
	queue    chan record
	flushReq chan chan struct{}
	stop     chan struct{}
	stopped  chan struct{}
	hold     sync.Mutex // tests lock it to freeze the writer
	sk       atomic.Pointer[sinkBox]
	pending  atomic.Int64
	fallback atomic.Pointer[string]

	dropped, written, failed atomic.Int64

	mu       sync.Mutex
	lastLog  map[string]time.Time
	lastWarn time.Time
	tokens   float64
	tokenAt  time.Time
}

type sinkBox struct{ s sink }

// New starts a Reporter with its writer goroutine.
func New(o Options) *Reporter {
	if o.QueueSize <= 0 {
		o.QueueSize = 1024
	}
	if o.FlushEvery <= 0 {
		o.FlushEvery = 500 * time.Millisecond
	}
	if o.MaxPending <= 0 {
		o.MaxPending = 500
	}
	if o.RatePerSec <= 0 {
		o.RatePerSec = 200
	}
	if o.Logf == nil {
		o.Logf = log.Printf
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	r := &Reporter{opts: o, queue: make(chan record, o.QueueSize), flushReq: make(chan chan struct{}), stop: make(chan struct{}), stopped: make(chan struct{}), lastLog: map[string]time.Time{}}
	r.tokens, r.tokenAt = float64(o.RatePerSec*2), o.Now()
	if o.FallbackPath != "" {
		r.SetFallbackPath(o.FallbackPath)
	}
	go r.run()
	return r
}

// SetFallbackPath sets the JSON-lines file that receives groups the database did not accept.
func (r *Reporter) SetFallbackPath(p string) { r.fallback.Store(&p) }

// Close flushes what it can and stops the writer.
func (r *Reporter) Close() {
	select {
	case <-r.stop:
	default:
		close(r.stop)
	}
	select {
	case <-r.stopped:
	case <-time.After(5 * time.Second):
	}
}

func (r *Reporter) setSink(s sink) { r.sk.Store(&sinkBox{s}) }

// Stats returns the counters.
func (r *Reporter) Stats() Stats {
	return Stats{Queued: len(r.queue), Pending: int(r.pending.Load()), Dropped: r.dropped.Load(), Written: r.written.Load(), Failed: r.failed.Load(), DBAttached: r.sk.Load() != nil}
}

type ctxKey int

// WithRequest attaches the request id and user to ctx so reports made further down are correlated.
func WithRequest(ctx context.Context, requestID, user string) context.Context {
	return context.WithValue(ctx, ctxKey(1), [2]string{requestID, user})
}

func scrub(s string) string {
	s = strings.ToValidUTF8(s, "?")
	return strings.ReplaceAll(s, "\x00", "")
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	return Truncate(s, n)
}

func (r *Reporter) allow() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.opts.Now()
	r.tokens += now.Sub(r.tokenAt).Seconds() * float64(r.opts.RatePerSec)
	r.tokenAt = now
	if max := float64(r.opts.RatePerSec * 2); r.tokens > max {
		r.tokens = max
	}
	if r.tokens < 1 {
		return false
	}
	r.tokens--
	return true
}

func (r *Reporter) logOnce(fp, line string) {
	r.mu.Lock()
	now := r.opts.Now()
	last, seen := r.lastLog[fp]
	if seen && now.Sub(last) < 30*time.Second {
		r.mu.Unlock()
		return
	}
	if len(r.lastLog) > 2000 {
		r.lastLog = map[string]time.Time{}
	}
	r.lastLog[fp] = now
	r.mu.Unlock()
	r.opts.Logf("%s", line)
}

// Report redacts and records e and returns the report id (stable per fingerprint). It never blocks and never panics:
// when the queue is full the report is dropped and counted; the one-line log message is always written.
func (r *Reporter) Report(ctx context.Context, e Entry) (id string) {
	defer func() {
		if p := recover(); p != nil {
			r.dropped.Add(1)
			id = ""
		}
	}()
	if ctx != nil {
		if v, ok := ctx.Value(ctxKey(1)).([2]string); ok {
			if e.RequestID == "" {
				e.RequestID = v[0]
			}
			if e.User == "" {
				e.User = v[1]
			}
		}
	}
	if !validSource[e.Source] {
		e.Source = "api"
	}
	if !validSeverity[e.Severity] {
		e.Severity = "error"
	}
	msg := Truncate(scrub(Redact(e.Message)), maxMessage)
	stack := Truncate(scrub(Redact(e.Stack)), maxStack)
	code := Truncate(scrub(RedactSecrets(e.Code)), 80)
	fp, id := Fingerprint(e.Source, code, msg, stack)
	rec := record{id: id, fp: fp, source: e.Source, severity: e.Severity, code: code, message: msg, stack: stack,
		requestID: Truncate(scrub(RedactSecrets(e.RequestID)), 64), user: Truncate(scrub(RedactSecrets(e.User)), 80), at: r.opts.Now().UTC()}
	d := map[string]any{}
	for k, v := range e.Details {
		d[k] = v
	}
	if e.TaskID != "" {
		d["task_id"] = e.TaskID
	}
	if len(d) > 0 {
		b, _ := json.Marshal(RedactValue(d))
		if len(b) > maxDetails {
			b = []byte(`{"truncated":true}`)
		}
		rec.details = []byte(scrub(string(b)))
	}
	line := fmt.Sprintf("diag: [%s/%s] %s %s (错误编号 %s, rid=%s)", rec.source, rec.severity, rec.code, oneLine(rec.message, 300), ShortID(id), rec.requestID)
	r.logOnce(fp, line)
	if !r.allow() {
		r.dropped.Add(1)
		return id
	}
	select {
	case r.queue <- rec:
	default:
		r.dropped.Add(1)
	}
	return id
}

func (r *Reporter) run() {
	defer close(r.stopped)
	t := time.NewTicker(r.opts.FlushEvery)
	defer t.Stop()
	batch := map[string]*agg{}
	for {
		stop := r.step(t.C, batch)
		if stop {
			return
		}
	}
}

func (r *Reporter) step(tick <-chan time.Time, batch map[string]*agg) (stop bool) {
	defer func() {
		if p := recover(); p != nil {
			r.opts.Logf("diag: writer recovered from panic: %v", p)
		}
	}()
	select {
	case rec := <-r.queue:
		r.hold.Lock()
		r.hold.Unlock()
		r.add(batch, rec)
		if len(batch) >= 200 {
			r.flush(batch)
		}
	case <-tick:
		r.hold.Lock()
		r.hold.Unlock()
		r.flush(batch)
	case ack := <-r.flushReq:
		r.drain(batch)
		r.flush(batch)
		close(ack)
	case <-r.stop:
		r.drain(batch)
		r.flush(batch)
		return true
	}
	return false
}

func (r *Reporter) drain(batch map[string]*agg) {
	for {
		select {
		case rec := <-r.queue:
			r.add(batch, rec)
		default:
			return
		}
	}
}

func (r *Reporter) add(batch map[string]*agg, rec record) {
	a := batch[rec.fp]
	if a == nil {
		a = &agg{rec: rec, first: rec.at}
		batch[rec.fp] = a
	} else {
		stack, details := a.rec.stack, a.rec.details
		a.rec = rec
		if rec.stack == "" {
			a.rec.stack = stack
		}
		if rec.details == nil {
			a.rec.details = details
		}
	}
	a.count++
	a.occ = append(a.occ, occurrence{rec.at, rec.requestID, rec.user})
	if len(a.occ) > maxOccurrence {
		a.occ = a.occ[len(a.occ)-maxOccurrence:]
	}
	r.pending.Store(int64(len(batch)))
}

// Flush writes everything queued so far (bounded wait). Returns false on timeout.
func (r *Reporter) Flush(timeout time.Duration) bool {
	ack := make(chan struct{})
	select {
	case r.flushReq <- ack:
	case <-time.After(timeout):
		return false
	case <-r.stopped:
		return true
	}
	select {
	case <-ack:
		return true
	case <-time.After(timeout):
		return false
	}
}

func (r *Reporter) flush(batch map[string]*agg) {
	if len(batch) == 0 {
		return
	}
	sb := r.sk.Load()
	if sb == nil {
		r.trim(batch)
		return
	}
	for fp, a := range batch {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := sb.s.write(ctx, a)
		cancel()
		if err == nil {
			r.written.Add(int64(a.count))
			delete(batch, fp)
			continue
		}
		a.attempts++
		r.failed.Add(1)
		if !a.spilled {
			a.spilled = true
			r.spill(a)
		}
		r.warn("diag: error report could not be stored (%v); %d group(s) pending, kept in the log file", err, len(batch))
		if a.attempts >= 8 {
			r.dropped.Add(int64(a.count))
			delete(batch, fp)
		}
	}
	r.trim(batch)
	r.pending.Store(int64(len(batch)))
}

func (r *Reporter) warn(format string, args ...any) {
	r.mu.Lock()
	now := r.opts.Now()
	ok := now.Sub(r.lastWarn) > 30*time.Second
	if ok {
		r.lastWarn = now
	}
	r.mu.Unlock()
	if ok {
		r.opts.Logf(format, args...)
	}
}

// trim bounds the in-memory backlog while the database is unavailable: the oldest groups are dropped (and counted).
func (r *Reporter) trim(batch map[string]*agg) {
	for len(batch) > r.opts.MaxPending {
		var oldest *agg
		for _, a := range batch {
			if oldest == nil || a.first.Before(oldest.first) {
				oldest = a
			}
		}
		if !oldest.spilled {
			r.spill(oldest)
		}
		r.dropped.Add(int64(oldest.count))
		delete(batch, oldest.rec.fp)
	}
	r.pending.Store(int64(len(batch)))
}

// spill appends the group to the fallback file (JSON lines, already redacted).
func (r *Reporter) spill(a *agg) {
	pp := r.fallback.Load()
	if pp == nil || *pp == "" {
		return
	}
	fp := *pp
	if fi, err := os.Stat(fp); err == nil && fi.Size() > 1<<20 {
		_ = os.Rename(fp, fp+".1")
	}
	f, err := os.OpenFile(fp, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	b, _ := json.Marshal(map[string]any{"at": a.rec.at, "id": a.rec.id, "source": a.rec.source, "severity": a.rec.severity, "code": a.rec.code,
		"message": a.rec.message, "stack": a.rec.stack, "request_id": a.rec.requestID, "user": a.rec.user, "count": a.count, "details": json.RawMessage(orNull(a.rec.details))})
	_, _ = f.Write(append(b, '\n'))
}

func orNull(b []byte) []byte {
	if len(b) == 0 {
		return []byte("null")
	}
	return b
}

var defaultReporter atomic.Pointer[Reporter]

func init() { defaultReporter.Store(New(Options{})) }

// Default is the process-wide reporter used by the package-level functions.
func Default() *Reporter { return defaultReporter.Load() }

// SetDefault swaps the process-wide reporter (tests) and returns the previous one.
func SetDefault(r *Reporter) *Reporter { return defaultReporter.Swap(r) }

// Report records e on the default reporter (see Reporter.Report).
func Report(ctx context.Context, e Entry) string { return Default().Report(ctx, e) }

// Flush waits for the default reporter to write what is queued.
func Flush(timeout time.Duration) bool { return Default().Flush(timeout) }

// GetStats returns the default reporter's counters.
func GetStats() Stats { return Default().Stats() }
