package scheduler

import (
	"strings"
	"testing"
	"time"
)

// beat is one heartbeat of one runner: what the agent measured and what quota
// the runner held when it did.
type beat struct {
	after     time.Duration // since the first heartbeat
	limit     float64
	used      float64
	saturated bool
	demanding bool
	stale     bool // the same sample as the previous heartbeat
	// holderBase and holderUsed are a docker-in-docker pair's busier half.
	holderBase, holderUsed float64
	want                   LoanCode
}

// Each case is a runner followed across heartbeats, because what this rule
// exists to get right -- reclaim once, back off, and do not flap -- only shows
// over a sequence. A guarantee of 2 CPUs throughout; a loan is a limit of 4.
func TestDecideLoanFollowsARunnerAcrossHeartbeats(t *testing.T) {
	const base = 2.0
	s := 30 * time.Second
	cases := []struct {
		name  string
		beats []beat
	}{
		{"a runner using its guarantee is lent", []beat{
			{after: 0, limit: 2, used: 1.9, demanding: true, want: LoanLend},
		}},
		// Codex review: the pair's sum counts the idle half's share as if it
		// were the loan's, and read a daemon using two lent cores as a loan
		// barely touched.
		{"a docker-in-docker daemon using its loan keeps it though the pair's sum is low", []beat{
			{after: 0, limit: 4, used: 2.4, holderBase: 1, holderUsed: 2.3, want: LoanInUse},
			{after: s, limit: 4, used: 2.4, holderBase: 1, holderUsed: 2.3, want: LoanInUse},
			{after: 2 * s, limit: 4, used: 2.4, holderBase: 1, holderUsed: 2.3, want: LoanInUse},
			{after: 3 * s, limit: 4, used: 2.4, holderBase: 1, holderUsed: 2.3, want: LoanInUse},
		}},
		{"a docker-in-docker pair whose busier half ignores its loan loses it", []beat{
			{after: 0, limit: 4, used: 2.6, holderBase: 1, holderUsed: 1.1, want: LoanWatching},
			{after: s, limit: 4, used: 2.6, holderBase: 1, holderUsed: 1.1, want: LoanWatching},
			{after: 2 * s, limit: 4, used: 2.6, holderBase: 1, holderUsed: 1.1, want: LoanReclaimed},
		}},
		{"a quiet runner is not lent", []beat{
			{after: 0, limit: 2, used: 0.4, want: LoanNotDemanding},
		}},
		{"a runner using a quarter of its loan holds it", []beat{
			{after: 0, limit: 4, used: 2.5, want: LoanInUse},
			{after: s, limit: 4, used: 3.9, want: LoanInUse},
		}},
		{"an unused loan is watched, then reclaimed after three samples", []beat{
			{after: 0, limit: 4, used: 2.1, want: LoanWatching},
			{after: s, limit: 4, used: 2.2, want: LoanWatching},
			{after: 2 * s, limit: 4, used: 2.0, want: LoanReclaimed},
		}},
		{"one busy sample resets the count", []beat{
			{after: 0, limit: 4, used: 2.1, want: LoanWatching},
			{after: s, limit: 4, used: 2.1, want: LoanWatching},
			{after: 2 * s, limit: 4, used: 3.5, want: LoanInUse},
			{after: 3 * s, limit: 4, used: 2.1, want: LoanWatching},
			{after: 4 * s, limit: 4, used: 2.1, want: LoanWatching},
			{after: 5 * s, limit: 4, used: 2.1, want: LoanReclaimed},
		}},
		{"a heartbeat repeating a sample does not count towards a reclaim", []beat{
			{after: 0, limit: 4, used: 2.1, want: LoanWatching},
			{after: s, limit: 4, used: 2.1, stale: true, want: LoanWatching},
			{after: 2 * s, limit: 4, used: 2.1, stale: true, want: LoanWatching},
			{after: 3 * s, limit: 4, used: 2.1, want: LoanWatching},
			{after: 4 * s, limit: 4, used: 2.1, want: LoanReclaimed},
		}},
		{"a reclaimed runner saturating its guarantee is not lent again straight away", []beat{
			{after: 0, limit: 4, used: 2.1, want: LoanWatching},
			{after: s, limit: 4, used: 2.1, want: LoanWatching},
			{after: 2 * s, limit: 4, used: 2.1, want: LoanReclaimed},
			// The agent has not moved the quota yet: still no loan.
			{after: 3 * s, limit: 4, used: 2.1, demanding: true, want: LoanBackingOff},
			{after: 4 * s, limit: 2, used: 2.0, demanding: true, saturated: true, want: LoanBackingOff},
			{after: 4*s + 3*time.Minute, limit: 2, used: 2.0, demanding: true, saturated: true, want: LoanBackingOff},
		}},
		{"after the backoff a demanding runner is lent again", []beat{
			{after: 0, limit: 4, used: 2.1, want: LoanWatching},
			{after: s, limit: 4, used: 2.1, want: LoanWatching},
			{after: 2 * s, limit: 4, used: 2.1, want: LoanReclaimed},
			{after: 2*s + LoanBackoff, limit: 2, used: 2.0, demanding: true, want: LoanLend},
		}},
		{"a second wasted loan doubles the backoff", []beat{
			{after: 0, limit: 4, used: 2.1, want: LoanWatching},
			{after: s, limit: 4, used: 2.1, want: LoanWatching},
			{after: 2 * s, limit: 4, used: 2.1, want: LoanReclaimed},
			{after: 2*s + LoanBackoff, limit: 4, used: 2.1, want: LoanWatching},
			{after: 3*s + LoanBackoff, limit: 4, used: 2.1, want: LoanWatching},
			{after: 4*s + LoanBackoff, limit: 4, used: 2.1, want: LoanReclaimed},
			{after: 4*s + 2*LoanBackoff, limit: 2, used: 2.0, demanding: true, want: LoanBackingOff},
			{after: 4*s + 3*LoanBackoff, limit: 2, used: 2.0, demanding: true, want: LoanLend},
		}},
		{"a runner that was quiet under its loan and now fills its guarantee is lent inside the backoff", []beat{
			{after: 0, limit: 4, used: 0.5, want: LoanWatching},
			{after: s, limit: 4, used: 0.6, want: LoanWatching},
			{after: 2 * s, limit: 4, used: 0.5, want: LoanReclaimed},
			{after: 3 * s, limit: 2, used: 0.5, want: LoanBackingOff},
			{after: 4 * s, limit: 2, used: 1.98, demanding: true, want: LoanBackoffOverride},
			{after: 5 * s, limit: 4, used: 3.8, want: LoanInUse},
		}},
	}
	start := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var m LoanMemory
			sampled := time.Time{}
			for i, b := range tc.beats {
				now := start.Add(b.after)
				if !b.stale {
					sampled = now.Add(-time.Second)
				}
				d := DecideLoan(LoanInput{
					Now: now, SampledAt: sampled, BaseCPUs: base, LimitCPUs: b.limit, UsedCPUs: b.used,
					HolderBaseCPUs: b.holderBase, HolderUsedCPUs: b.holderUsed,
					Saturated: b.saturated, Demanding: b.demanding, Memory: m,
				})
				if d.Code != b.want {
					t.Fatalf("heartbeat %d: %s (%q), want %s", i, d.Code, d.Reason, b.want)
				}
				if d.Lend != (b.want == LoanLend || b.want == LoanInUse || b.want == LoanWatching || b.want == LoanBackoffOverride) {
					t.Fatalf("heartbeat %d: lend = %v for %s", i, d.Lend, d.Code)
				}
				if strings.TrimSpace(d.Reason) == "" {
					t.Fatalf("heartbeat %d: %s has no reason an operator can read", i, d.Code)
				}
				m = d.Memory
			}
		})
	}
}

// The flap this rule exists to stop: a job that sized its workers to a little
// over its guarantee is demanding at the guarantee, wastes any loan, and was
// lent, reclaimed and lent again every few heartbeats. Across an hour it may
// change its quota only a handful of times.
func TestDecideLoanDoesNotFlapOnAJobThatUsesALittleMoreThanItsGuarantee(t *testing.T) {
	const base = 2.0
	start := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	var m LoanMemory
	limit, changes := base, 0
	for i := range 120 {
		now := start.Add(time.Duration(i) * 30 * time.Second)
		used := min(limit, 2.2)
		d := DecideLoan(LoanInput{
			Now: now, SampledAt: now, BaseCPUs: base, LimitCPUs: limit, UsedCPUs: used,
			Saturated: used >= limit*LoanSaturatedShare, Demanding: used >= 0.8*base, Memory: m,
		})
		m = d.Memory
		next := base
		if d.Lend {
			next = 4
		}
		if next != limit {
			changes++
		}
		limit = next
	}
	// Lent and reclaimed at 0, 5, 15 and 35 minutes, then held off for the
	// capped half hour: under a dozen changes, where a rule without memory
	// makes one every few heartbeats.
	if changes > 12 {
		t.Fatalf("the quota changed %d times in an hour, want a handful", changes)
	}
}

func TestLoanBackoffIsCapped(t *testing.T) {
	if got := (LoanMemory{Strikes: 20}).Backoff(); got != LoanBackoffMax {
		t.Fatalf("backoff after twenty strikes = %s, want the cap %s", got, LoanBackoffMax)
	}
	if got := (LoanMemory{Strikes: 1}).Backoff(); got != LoanBackoff {
		t.Fatalf("backoff after one strike = %s, want %s", got, LoanBackoff)
	}
}
