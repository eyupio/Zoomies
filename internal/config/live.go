package config

import (
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
)

// Live is the configuration as a running process sees it.
//
// A process reads its configuration from many goroutines at once -- every
// reconcile pass, every request, every problems refresh -- and PATCH /settings
// writes to it from another. Rather than a lock that every one of those readers
// has to remember to take, the configuration is an immutable snapshot behind a
// single atomic pointer: a reader takes the pointer and sees one consistent
// whole, and a change copies the snapshot, alters the copy and swaps it in. A
// reader that took the old pointer finishes its pass on the old values, which
// is exactly what "the change takes effect on the next pass" means.
type Live struct {
	// mu serialises writers only, so two operators changing different keys at
	// the same moment cannot each copy the same snapshot and lose the other's
	// change. Readers never take it.
	mu sync.Mutex
	p  atomic.Pointer[Config]
}

// NewLive wraps the configuration a process started with. The pointer given
// becomes the first snapshot, so a caller that still holds it sees the running
// values until the first Update replaces them.
func NewLive(c *Config) *Live {
	l := &Live{}
	l.p.Store(c)
	return l
}

// Load returns the current snapshot. It must be treated as read-only: a write
// through it is the unsynchronised write this type exists to remove.
func (l *Live) Load() *Config { return l.p.Load() }

// Update applies fn to a copy of the current snapshot and publishes the copy,
// returning the snapshots before and after so the caller can see what changed.
//
// The copy is shallow. fn may assign any field, including a whole slice or
// map, but must not alter a slice or map the old snapshot shares, because a
// reader may be in the middle of ranging over it.
func (l *Live) Update(fn func(*Config)) (before, after *Config) {
	l.mu.Lock()
	defer l.mu.Unlock()
	before = l.p.Load()
	next := *before
	fn(&next)
	l.p.Store(&next)
	return before, &next
}

// ParseLogLevel maps a log.level value onto slog's levels. Anything it does not
// recognise is info, which is what an operator who typed "verbose" expects to
// see rather than nothing at all.
func ParseLogLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
