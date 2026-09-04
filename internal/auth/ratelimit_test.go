package auth

import (
	"testing"
	"time"
)

func TestRateLimiterAllowsThenBlocksThenRecovers(t *testing.T) {
	c := newClock()
	l := NewRateLimiter(3, time.Minute, c.Now)

	for i := range 3 {
		if !l.Allow("10.0.0.1") {
			t.Fatalf("attempt %d was refused; the limit is 3", i+1)
		}
	}
	if l.Allow("10.0.0.1") {
		t.Fatal("the fourth attempt inside the window was allowed")
	}
	if got := l.RetryAfter("10.0.0.1"); got != time.Minute {
		t.Errorf("RetryAfter = %v; want a full minute after three attempts at the same instant", got)
	}

	// Another address has its own budget.
	if !l.Allow("10.0.0.2") {
		t.Error("a different address was refused")
	}

	// The window slides rather than resetting on a boundary: after 61 seconds
	// the three attempts have aged out.
	c.Advance(61 * time.Second)
	for i := range 3 {
		if !l.Allow("10.0.0.1") {
			t.Fatalf("attempt %d after the window was refused", i+1)
		}
	}
	if l.Allow("10.0.0.1") {
		t.Error("the limit did not apply again after recovery")
	}
}

func TestRateLimiterSlidesRatherThanBuckets(t *testing.T) {
	c := newClock()
	l := NewRateLimiter(2, time.Minute, c.Now)

	if !l.Allow("ip") {
		t.Fatal("the first attempt was refused")
	}
	c.Advance(30 * time.Second)
	if !l.Allow("ip") {
		t.Fatal("the second attempt was refused")
	}
	if l.Allow("ip") {
		t.Fatal("a third attempt inside the window was allowed")
	}

	// 61 seconds after the first attempt, only that attempt has aged out, so
	// exactly one slot opens. A fixed per-minute bucket would have opened two.
	c.Advance(31 * time.Second)
	if !l.Allow("ip") {
		t.Error("the slot freed by the oldest attempt was not reused")
	}
	if l.Allow("ip") {
		t.Error("two slots opened; only one attempt had aged out")
	}
}

func TestRateLimiterRefusalDoesNotExtendTheLockout(t *testing.T) {
	c := newClock()
	l := NewRateLimiter(1, time.Minute, c.Now)

	if !l.Allow("ip") {
		t.Fatal("the first attempt was refused")
	}
	// Hammering while blocked must not keep pushing the window forward, or a
	// client with a retry loop could never recover.
	for range 5 {
		c.Advance(10 * time.Second)
		if l.Allow("ip") {
			t.Fatal("an attempt inside the window was allowed")
		}
	}
	c.Advance(11 * time.Second) // 61s since the accepted attempt
	if !l.Allow("ip") {
		t.Error("the client never recovered after its window elapsed")
	}
}

func TestRateLimiterZeroMeansUnlimited(t *testing.T) {
	l := NewRateLimiter(0, time.Minute, newClock().Now)
	for i := range 100 {
		if !l.Allow("ip") {
			t.Fatalf("attempt %d was refused by a disabled limiter", i+1)
		}
	}
	if got := l.RetryAfter("ip"); got != 0 {
		t.Errorf("RetryAfter on a disabled limiter = %v; want 0", got)
	}
	// A nil limiter is a disabled one, so callers need no branch.
	var nilLimiter *RateLimiter
	if !nilLimiter.Allow("ip") {
		t.Error("a nil limiter refused an attempt")
	}
	nilLimiter.Reset("ip")
}

func TestRateLimiterReset(t *testing.T) {
	c := newClock()
	l := NewRateLimiter(1, time.Minute, c.Now)
	if !l.Allow("ip") || l.Allow("ip") {
		t.Fatal("limiter did not block after one attempt")
	}
	l.Reset("ip")
	if !l.Allow("ip") {
		t.Error("Reset did not clear the address's attempts")
	}
}

func TestRateLimiterForgetsIdleKeys(t *testing.T) {
	c := newClock()
	l := NewRateLimiter(5, time.Minute, c.Now)
	for _, ip := range []string{"a", "b", "c"} {
		l.Allow(ip)
	}
	if got := l.Keys(); got != 3 {
		t.Fatalf("tracked keys = %d; want 3", got)
	}
	// A stream of one-shot addresses must not grow the map without bound.
	c.Advance(2 * time.Minute)
	l.Allow("d")
	if got := l.Keys(); got != 1 {
		t.Errorf("tracked keys after the sweep = %d; want only the live one", got)
	}
}
