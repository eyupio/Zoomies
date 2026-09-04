package auth

import (
	"sync"
	"time"
)

// RateLimiter is a sliding-window counter keyed by client address.
//
// It keeps the timestamps of the attempts still inside the window rather than a
// per-minute bucket, so an attacker cannot get 2N attempts by straddling a
// bucket boundary.
//
// It is per-process and in-memory, which is fine: Zoomies is a single
// controller, and pushing every login attempt through SQLite would put a write
// -- serialised behind the single writer -- in front of the login path for no
// security gain. If this ever becomes more than one controller, this type is
// the piece that has to move to shared state.
type RateLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	now    func() time.Time
	hits   map[string][]time.Time
	// swept is when stale keys were last discarded, so a stream of one-shot
	// addresses cannot grow the map without bound.
	swept time.Time
}

// NewRateLimiter returns a limiter allowing limit attempts per key per window.
// A limit of zero or less disables limiting entirely, which is what setting
// security.rate_limit_logins to 0 means. clock may be nil for time.Now.
func NewRateLimiter(limit int, window time.Duration, clock func() time.Time) *RateLimiter {
	if clock == nil {
		clock = time.Now
	}
	if window <= 0 {
		window = time.Minute
	}
	return &RateLimiter{
		limit:  limit,
		window: window,
		now:    clock,
		hits:   map[string][]time.Time{},
	}
}

// Allow records an attempt for key and reports whether it is within the limit.
// A refused attempt is not recorded, so a client that keeps hammering does not
// extend its own lockout indefinitely -- it recovers one window after its last
// accepted attempt.
func (l *RateLimiter) Allow(key string) bool {
	if l == nil || l.limit <= 0 {
		return true
	}
	return l.AllowAt(key, l.now())
}

// AllowAt is Allow at an explicit time, which is how the tests move the window
// without sleeping.
func (l *RateLimiter) AllowAt(key string, now time.Time) bool {
	if l == nil || l.limit <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweep(now)

	hits := within(l.hits[key], now.Add(-l.window))
	if len(hits) >= l.limit {
		l.hits[key] = hits
		return false
	}
	l.hits[key] = append(hits, now)
	return true
}

// RetryAfter returns how long until key would be allowed again, or zero if it
// is allowed now. Handlers put it in a Retry-After header.
func (l *RateLimiter) RetryAfter(key string) time.Duration {
	if l == nil || l.limit <= 0 {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	hits := within(l.hits[key], now.Add(-l.window))
	l.hits[key] = hits
	if len(hits) < l.limit {
		return 0
	}
	// The oldest attempt in the window is the one that has to age out.
	if d := hits[0].Add(l.window).Sub(now); d > 0 {
		return d
	}
	return 0
}

// Reset forgets a key's attempts. Login calls it after a correct password, so
// one person's typos do not lock out the next person behind the same NAT.
func (l *RateLimiter) Reset(key string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	delete(l.hits, key)
	l.mu.Unlock()
}

// Keys returns how many distinct keys are being tracked. It exists for the
// memory-growth test and for a debug endpoint.
func (l *RateLimiter) Keys() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.hits)
}

// sweep drops keys with nothing left inside the window. It runs at most once
// per window, so the common path stays O(1) in the number of tracked keys.
func (l *RateLimiter) sweep(now time.Time) {
	if now.Sub(l.swept) < l.window {
		return
	}
	l.swept = now
	cut := now.Add(-l.window)
	for k, hits := range l.hits {
		if remaining := within(hits, cut); len(remaining) == 0 {
			delete(l.hits, k)
		} else {
			l.hits[k] = remaining
		}
	}
}

// within returns the timestamps at or after cut. The slice is ordered by
// construction, so this is a prefix drop rather than a filter.
func within(hits []time.Time, cut time.Time) []time.Time {
	for i, h := range hits {
		if h.After(cut) {
			return hits[i:]
		}
	}
	return nil
}
