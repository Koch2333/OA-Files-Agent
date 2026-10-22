package diag

import (
	"os"
	"path/filepath"
	"runtime/debug"
)

// CaptureRuntimeCrashes makes a Go runtime fatal error (out of memory, concurrent map write, deadlock) — which no
// recover can catch — also land in dir/runtime-crash.txt. The next start turns a non-empty file into a crash-*.txt
// file and a part of the unclean-exit report.
func CaptureRuntimeCrashes(dir string) {
	if dir == "" {
		return
	}
	_ = os.MkdirAll(dir, 0o700)
	f, err := os.OpenFile(filepath.Join(dir, runtimeCrashName), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	if err := debug.SetCrashOutput(f, debug.CrashOptions{}); err != nil {
		f.Close()
	}
}
