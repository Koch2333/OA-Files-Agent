package diag

import (
	"context"
	"fmt"
	"runtime/debug"
	"strings"
	"time"
)

// SourceOf maps a goroutine name such as "pipeline.loop" to a report source.
func SourceOf(name string) string {
	p := name
	if i := strings.IndexAny(name, ".:/"); i > 0 {
		p = name[:i]
	}
	if validSource[p] {
		return p
	}
	return "api"
}

func panicEntry(name string, v any) Entry {
	return Entry{Source: SourceOf(name), Severity: "panic", Code: "PANIC", Message: fmt.Sprintf("panic in %s: %v", name, v), Stack: string(debug.Stack()), Details: map[string]any{"goroutine": name}}
}

// Safe runs fn and recovers a panic: it records a severity=panic report with the stack and returns the report id
// ("" when fn returned normally). Use the id to tell the user "内部错误，错误编号 …".
func Safe(name string, fn func()) (id string) { return SafeD(name, nil, fn) }

// Go is `go fn()` with panic recovery.
func Go(name string, fn func()) {
	go func() { Safe(name, fn) }()
}

// Loop runs a long-lived loop: if fn panics it is recorded and restarted with exponential backoff (1s … 30s, reset
// after a stable minute) until ctx ends. A normal return ends the loop.
func Loop(ctx context.Context, name string, fn func(context.Context)) {
	LoopBackoff(ctx, name, time.Second, 30*time.Second, fn)
}

// LoopBackoff is Loop with explicit backoff bounds (tests use tiny ones).
func LoopBackoff(ctx context.Context, name string, min, max time.Duration, fn func(context.Context)) {
	delay := min
	for ctx.Err() == nil {
		start := time.Now()
		if Safe(name, func() { fn(ctx) }) == "" {
			return
		}
		if time.Since(start) > time.Minute {
			delay = min
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		if delay *= 2; delay > max {
			delay = max
		}
	}
}

// ReportPanic records an already-recovered panic value (for code that has its own recover, e.g. to convert the panic
// into an error for the caller) and returns the report id.
func ReportPanic(name string, v any) string {
	return Report(context.Background(), panicEntry(name, v))
}

// SafeD is Safe with extra report details (task id, file id, extension, size — metadata only, never content).
func SafeD(name string, details map[string]any, fn func()) (id string) {
	defer func() {
		if v := recover(); v != nil {
			e := panicEntry(name, v)
			for k, d := range details {
				e.Details[k] = d
			}
			id = Report(context.Background(), e)
			if id == "" {
				id = "unknown"
			}
		}
	}()
	fn()
	return ""
}
