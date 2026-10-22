package diag

import (
	"sync"
	"time"
)

// Limiter is a sliding-window counter per key (e.g. per user): at most Max events in Window. The key map is bounded.
type Limiter struct {
	Max    int
	Window time.Duration
	Now    func() time.Time

	mu sync.Mutex
	m  map[string][]time.Time
}

func NewLimiter(max int, window time.Duration) *Limiter {
	return &Limiter{Max: max, Window: window, Now: time.Now, m: map[string][]time.Time{}}
}

// Allow records an event and reports whether it is within the limit; when it is not, retryAfter is the wait until
// the oldest event leaves the window.
func (l *Limiter) Allow(key string) (ok bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.Now()
	cut := now.Add(-l.Window)
	var ev []time.Time
	for _, t := range l.m[key] {
		if t.After(cut) {
			ev = append(ev, t)
		}
	}
	if len(ev) >= l.Max {
		l.m[key] = ev
		return false, ev[0].Add(l.Window).Sub(now)
	}
	l.m[key] = append(ev, now)
	if len(l.m) > 4096 { // drop idle keys
		for k, v := range l.m {
			if len(v) == 0 || !v[len(v)-1].After(cut) {
				delete(l.m, k)
			}
		}
	}
	return true, 0
}
