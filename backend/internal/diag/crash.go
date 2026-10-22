package diag

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"oa-agent/backend/internal/buildinfo"
)

// CodeUncleanExit is the code of the supervisor report written when the previous run did not exit gracefully.
const CodeUncleanExit = "LAST_EXIT_UNCLEAN"

var proc struct {
	mu      sync.Mutex
	logDir  string
	running bool
	release func()
}

// exitFn is os.Exit; tests replace it.
var exitFn = os.Exit

// SetLogDir tells diag where crash files, the run lock and the fallback report file live (data/logs).
func SetLogDir(dir string) {
	proc.mu.Lock()
	proc.logDir = dir
	proc.mu.Unlock()
	if dir != "" {
		_ = os.MkdirAll(dir, 0o700)
		Default().SetFallbackPath(filepath.Join(dir, "error-reports-fallback.jsonl"))
	}
}

// LogDir returns the directory set by SetLogDir ("" when unset).
func LogDir() string {
	proc.mu.Lock()
	defer proc.mu.Unlock()
	return proc.logDir
}

// MarkRunning records that startup finished: later fatal errors count as supervisor errors instead of startup errors.
func MarkRunning() {
	proc.mu.Lock()
	proc.running = true
	proc.mu.Unlock()
}

// SetRunLockRelease registers the function that removes the run lock; Fatalf calls it (the crash is recorded, so
// the next start must not report it a second time as an unclean exit).
func SetRunLockRelease(f func()) {
	proc.mu.Lock()
	proc.release = f
	proc.mu.Unlock()
}

var envWhitelist = map[string]bool{
	"OA_LISTEN_ADDR": true, "OA_STANDALONE": true, "OA_LOG_LEVEL": true, "OA_STORAGE": true, "OA_ALLOW_REVEAL": true, "OA_TRUSTED_PROXY": true,
	"OA_AUTO_MIGRATE": true, "OA_OPEN_BROWSER": true, "OA_EXIT_ON_STDIN_EOF": true, "OA_CORS_ORIGINS": true, "OA_PUBLIC_URL": true,
	"OA_ARCHIVE_ROOT": true, "OA_SPOOL_ROOT": true, "OA_FRONTEND_DIR": true, "OA_MIGRATIONS_DIR": true, "OA_LLM_PROVIDER": true,
	"OA_LLM_MODEL": true, "OA_LLM_TIMEOUT_SECONDS": true, "OA_DATA_DIR": true, "OA_HOME": true, "OA_LOG_DIR": true, "OA_WORKER_URL": true,
}

// EnvVar is one environment entry of the summary.
type EnvVar struct{ Name, Value string }

// EnvSummary lists the OA_* environment variables: values are shown only for a whitelist of non-sensitive settings
// (and still redacted); everything else shows "[set]" or "[empty]".
func EnvSummary() []EnvVar {
	var out []EnvVar
	for _, kv := range os.Environ() {
		i := strings.IndexByte(kv, '=')
		if i < 0 || !strings.HasPrefix(kv[:i], "OA_") {
			continue
		}
		name, val := kv[:i], kv[i+1:]
		switch {
		case val == "":
			val = "[empty]"
		case envWhitelist[name]:
			val = Redact(val)
		default:
			val = "[set]"
		}
		out = append(out, EnvVar{name, val})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// WriteCrashFile writes data/logs/crash-<UTC timestamp>.txt (message, stack, version, redacted env summary) and returns
// its path. It is meant to be the first thing a dying process does.
func WriteCrashFile(kind, msg, stack string) (string, error) {
	dir := LogDir()
	if dir == "" {
		return "", fmt.Errorf("log directory not set")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "OA Agent crash report\ntime: %s\nkind: %s\nversion: %s\npid: %d\n\nmessage:\n%s\n\nstack:\n%s\n\nenvironment (OA_* only; values hidden unless non-sensitive):\n",
		time.Now().UTC().Format(time.RFC3339), kind, buildinfo.String(), os.Getpid(), Redact(msg), Redact(stack))
	for _, e := range EnvSummary() {
		fmt.Fprintf(&b, "%s=%s\n", e.Name, e.Value)
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	for i := 0; i < 100; i++ {
		name := "crash-" + stamp + ".txt"
		if i > 0 {
			name = fmt.Sprintf("crash-%s-%d.txt", stamp, i+1)
		}
		path := filepath.Join(dir, name)
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		_, werr := f.WriteString(b.String())
		cerr := f.Close()
		if werr != nil {
			return path, werr
		}
		return path, cerr
	}
	return "", fmt.Errorf("could not create crash file")
}

func die(kind string, code int, msg, stack string) {
	path, err := WriteCrashFile(kind, msg, stack)
	log.Printf("FATAL: %s", oneLine(Redact(msg), 500))
	if err != nil {
		log.Printf("crash file not written: %v", err)
	} else {
		log.Printf("crash report written to %s", path)
	}
	proc.mu.Lock()
	src, rel := "startup", proc.release
	if proc.running {
		src = "supervisor"
	}
	proc.mu.Unlock()
	d := map[string]any{}
	if path != "" {
		d["crash_file"] = filepath.Base(path)
	}
	Report(context.Background(), Entry{Source: src, Severity: "fatal", Code: strings.ToUpper(kind), Message: msg, Stack: stack, Details: d})
	Flush(3 * time.Second)
	if rel != nil {
		rel()
	}
	exitFn(code)
}

// Fatalf records a fatal error and exits with status 1 (replacement for log.Fatalf): crash file first, then a `fatal`
// report when the database is reachable, then the run lock is released.
func Fatalf(format string, args ...any) {
	die("fatal", 1, fmt.Sprintf(format, args...), string(debug.Stack()))
}

// Fatal is Fatalf with fmt.Sprint formatting (replacement for log.Fatal).
func Fatal(args ...any) { die("fatal", 1, fmt.Sprint(args...), string(debug.Stack())) }

// RecoverMain is deferred at the top of main: a panic on the main goroutine is written to a crash file and
// reported before the process exits with status 2.
func RecoverMain() {
	if v := recover(); v != nil {
		die("panic", 2, fmt.Sprintf("panic in main: %v", v), string(debug.Stack()))
	}
}

// ReleaseRunLock removes the run lock (graceful exit paths that call os.Exit themselves, e.g. stdin EOF).
func ReleaseRunLock() {
	proc.mu.Lock()
	rel := proc.release
	proc.mu.Unlock()
	if rel != nil {
		rel()
	}
}
