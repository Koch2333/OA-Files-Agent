package auth

import (
	"sync"
	"time"
)

// Limiter is an in-memory token bucket per key. Idle buckets are swept lazily so unknown keys (attacker-chosen
// usernames) cannot grow memory without bound.
type Limiter struct {
	Burst    float64
	Interval time.Duration // time to earn one token
	Now      Clock
	MaxKeys  int

	mu        sync.Mutex
	buckets   map[string]*bucket
	lastSweep time.Time
}

type bucket struct {
	tokens float64
	at     time.Time
}

func NewLimiter(burst int, interval time.Duration, now Clock) *Limiter {
	if now == nil {
		now = time.Now
	}
	return &Limiter{Burst: float64(burst), Interval: interval, Now: now, MaxKeys: 100000, buckets: map[string]*bucket{}}
}

// Allow takes one token. When empty it reports how long until one is available.
func (l *Limiter) Allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.Now()
	l.sweep(now)
	b := l.buckets[key]
	if b == nil {
		if len(l.buckets) >= l.MaxKeys { // under key flooding, refuse new keys rather than evict real ones
			return false, l.Interval
		}
		b = &bucket{tokens: l.Burst, at: now}
		l.buckets[key] = b
	}
	if el := now.Sub(b.at); el > 0 {
		b.tokens += float64(el) / float64(l.Interval)
		if b.tokens > l.Burst {
			b.tokens = l.Burst
		}
		b.at = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	wait := time.Duration((1 - b.tokens) * float64(l.Interval))
	if wait < time.Second {
		wait = time.Second
	}
	return false, wait
}

// Reset forgets a key (successful login).
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	delete(l.buckets, key)
	l.mu.Unlock()
}

// Size reports tracked keys (tests).
func (l *Limiter) Size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}

// sweep drops buckets that are full again (they carry no state). Caller holds the mutex.
func (l *Limiter) sweep(now time.Time) {
	if now.Sub(l.lastSweep) < time.Minute {
		return
	}
	l.lastSweep = now
	full := time.Duration(l.Burst * float64(l.Interval))
	for k, b := range l.buckets {
		if now.Sub(b.at) >= full {
			delete(l.buckets, k)
		}
	}
}

// ghostLocks mimics lockout for usernames that do not exist, so "423 ACCOUNT_LOCKED" does not reveal which accounts exist.
type ghostLocks struct {
	mu sync.Mutex
	m  map[string]*ghost
}

type ghost struct {
	fails int
	until time.Time
}

func (g *ghostLocks) locked(name string, now time.Time) (time.Time, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if e := g.m[name]; e != nil && now.Before(e.until) {
		return e.until, true
	}
	return time.Time{}, false
}

// fail counts a failure; it returns true when this failure locks the name.
func (g *ghostLocks) fail(name string, threshold int, lockFor time.Duration, now time.Time) (time.Time, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.m == nil {
		g.m = map[string]*ghost{}
	}
	if len(g.m) > 50000 { // bound memory: drop expired entries, then give up tracking
		for k, e := range g.m {
			if now.After(e.until) && e.fails == 0 {
				delete(g.m, k)
			}
		}
		if len(g.m) > 50000 {
			return time.Time{}, false
		}
	}
	e := g.m[name]
	if e == nil {
		e = &ghost{}
		g.m[name] = e
	}
	e.fails++
	if e.fails >= threshold {
		e.fails = 0
		e.until = now.Add(lockFor)
		return e.until, true
	}
	return time.Time{}, false
}

func (g *ghostLocks) clear(name string) {
	g.mu.Lock()
	delete(g.m, name)
	g.mu.Unlock()
}
