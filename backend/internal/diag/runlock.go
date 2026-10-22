package diag

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// RunInfo is the content of data/logs/run.lock.
type RunInfo struct {
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
}

// PrevRun describes what the run lock said about the previous run.
type PrevRun struct {
	Unclean    bool     // lock present, its process gone and not us: the previous run did not exit gracefully
	OtherAlive bool     // lock belongs to a process that is still running (second instance)
	Info       RunInfo  // from the stale lock
	CrashFiles []string // crash-*.txt written by that run (names only)
	Runtime    string   // redacted contents of a Go runtime crash dump, if any
}

// AcquireRunLock creates dir/run.lock (pid, started_at) and returns what the previous lock implied. probe reports
// whether a pid is a running process (nil = real probe). release removes the lock if it is still ours; call it on
// every graceful exit. If another live process owns the lock the lock is left alone (release is a no-op).
func AcquireRunLock(dir string, probe func(pid int) bool) (prev PrevRun, release func(), err error) {
	if probe == nil {
		probe = processAlive
	}
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return prev, func() {}, err
	}
	path := filepath.Join(dir, "run.lock")
	self := os.Getpid()
	if raw, rerr := os.ReadFile(path); rerr == nil {
		var old RunInfo
		if json.Unmarshal(raw, &old) != nil || old.PID <= 0 {
			// unreadable lock: the writer died mid-write
			prev.Unclean = true
		} else if old.PID != self {
			prev.Info = old
			if probe(old.PID) {
				prev.OtherAlive = true
				return prev, func() {}, nil
			}
			prev.Unclean = true
		}
		if prev.Unclean {
			prev.CrashFiles = crashFilesSince(dir, prev.Info.StartedAt)
			prev.Runtime = readRuntimeCrash(dir)
		}
	}
	info := RunInfo{PID: self, StartedAt: time.Now().UTC()}
	b, _ := json.Marshal(info)
	tmp := path + ".tmp"
	if err = os.WriteFile(tmp, b, 0o600); err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		return prev, func() {}, err
	}
	return prev, func() {
		if raw, rerr := os.ReadFile(path); rerr == nil {
			var cur RunInfo
			if json.Unmarshal(raw, &cur) == nil && cur.PID == self {
				_ = os.Remove(path)
			}
		}
	}, nil
}

func crashFilesSince(dir string, since time.Time) []string {
	m, _ := filepath.Glob(filepath.Join(dir, "crash-*.txt"))
	type cf struct {
		name string
		mod  time.Time
	}
	var all []cf
	for _, p := range m {
		if fi, err := os.Stat(p); err == nil && !fi.ModTime().Before(since.Add(-2*time.Second)) {
			all = append(all, cf{filepath.Base(p), fi.ModTime()})
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].mod.After(all[j].mod) })
	var out []string
	for i, c := range all {
		if i >= 5 {
			break
		}
		out = append(out, c.name)
	}
	return out
}

const runtimeCrashName = "runtime-crash.txt"

func readRuntimeCrash(dir string) string {
	p := filepath.Join(dir, runtimeCrashName)
	raw, err := os.ReadFile(p)
	if err != nil || len(strings.TrimSpace(string(raw))) == 0 {
		return ""
	}
	_ = os.Rename(p, filepath.Join(dir, "crash-"+time.Now().UTC().Format("20060102T150405Z")+"-runtime.txt"))
	return Truncate(Redact(string(raw)), maxStack)
}

// ReportPrevRun turns an unclean previous run into a supervisor error report (grouped by fingerprint, so repeated
// unclean exits only raise the count). Safe to call before the database is attached: the report waits in memory.
func ReportPrevRun(ctx context.Context, p PrevRun) string {
	if !p.Unclean {
		return ""
	}
	d := map[string]any{"previous_pid": p.Info.PID}
	if !p.Info.StartedAt.IsZero() {
		d["previous_started_at"] = p.Info.StartedAt.Format(time.RFC3339)
	}
	if len(p.CrashFiles) > 0 {
		d["crash_files"] = p.CrashFiles
	}
	return Report(ctx, Entry{Source: "supervisor", Severity: "error", Code: CodeUncleanExit,
		Message: "上次运行异常结束，可能被强制终止或崩溃", Stack: p.Runtime, Details: d})
}
