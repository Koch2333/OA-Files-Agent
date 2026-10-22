// Package logx adds debug-level logging that exists only in debug builds (go build -tags debug).
// Debug lines must never contain secrets, credentials, document text or request/response bodies:
// log identifiers, counts, statuses and durations only.
package logx

import (
	"fmt"
	"log"
	"os"
	"strings"
	"sync/atomic"

	"oa-agent/backend/internal/buildinfo"
)

var quiet atomic.Bool

func init() {
	// A debug build logs debug lines by default; OA_LOG_LEVEL=info silences them without rebuilding.
	if v := strings.ToLower(os.Getenv("OA_LOG_LEVEL")); v == "info" || v == "warn" || v == "error" {
		quiet.Store(true)
	}
}

// Enabled reports whether Debugf would print.
func Enabled() bool { return buildinfo.Debug && !quiet.Load() }

// Debugf prints "[DEBUG] file:line message" in debug builds; in release builds it does nothing.
func Debugf(format string, args ...any) {
	if !buildinfo.Debug || quiet.Load() {
		return
	}
	_ = log.Output(2, "[DEBUG] "+fmt.Sprintf(format, args...))
}
