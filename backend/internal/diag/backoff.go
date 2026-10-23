package diag

import (
	"context"
	"log"
	"time"
)

// Backoff spaces out the retries of a failing background poll (e.g. the database is down) and throttles its log
// lines: the first failure is logged, then every 10th, and the recovery once. Not safe for concurrent use; give each
// loop its own value.
type Backoff struct {
	fails int
}

const backoffMax = 30 * time.Second

// Fail records a failure, logs it when due, and returns how long the caller should wait before polling again.
// It reports whether this was the first failure of a streak (callers report errors only then).
func (b *Backoff) Fail(what string, err error) (time.Duration, bool) {
	b.fails++
	if b.fails == 1 || b.fails%10 == 0 {
		log.Printf("%s: %v (failure %d, retrying with backoff)", what, err, b.fails)
	}
	d := time.Second << min(b.fails-1, 5)
	return min(d, backoffMax), b.fails == 1
}

// OK ends a failure streak.
func (b *Backoff) OK(what string) {
	if b.fails > 0 {
		log.Printf("%s: recovered after %d failed attempts", what, b.fails)
		b.fails = 0
	}
}

// Sleep waits d or until ctx ends; false means ctx ended.
func Sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
