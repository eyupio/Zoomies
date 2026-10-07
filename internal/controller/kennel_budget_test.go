package controller

import (
	"errors"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/github"
)

// The budget is a share of what the installation last reported, and a limit
// nobody has reported yet is GitHub's own, so a fresh controller does not start
// with a budget of nothing.
func TestTheBudgetIsAShareOfTheLimitTheInstallationLastReported(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 10, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		percent int
		rl      github.RateLimit
		want    int
	}{
		{"nothing reported yet", 20, github.RateLimit{}, 1000},
		{"a larger limit", 20, github.RateLimit{Limit: 15000, Remaining: 15000, ResetAt: now.Add(time.Hour)}, 3000},
		{"the smallest share", 5, github.RateLimit{Limit: 5000, Remaining: 5000, ResetAt: now.Add(time.Hour)}, 250},
		{"the largest share", 50, github.RateLimit{Limit: 5000, Remaining: 5000, ResetAt: now.Add(time.Hour)}, 2500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b kennelBudget
			got := 0
			for b.take(now, tc.percent, tc.rl) {
				got++
				if got > 100000 {
					t.Fatal("the budget never ran out")
				}
			}
			if got != tc.want {
				t.Errorf("%d requests allowed, want %d", got, tc.want)
			}
		})
	}
}

// What was spent is what was spent this hour: the next hour starts again. A
// budget that carried over would let a quiet night pay for a burst, which is the
// opposite of a ceiling.
func TestTheBudgetStartsAgainWhenTheHourTurns(t *testing.T) {
	rl := github.RateLimit{Limit: 5000, Remaining: 5000, ResetAt: time.Date(2026, 10, 6, 14, 0, 0, 0, time.UTC)}
	var b kennelBudget
	late := time.Date(2026, 10, 6, 12, 59, 59, 0, time.UTC)
	for i := 0; b.take(late, 5, rl); i++ {
		if i > 10000 {
			t.Fatal("the budget never ran out")
		}
	}
	if b.take(late, 5, rl) {
		t.Fatal("the budget was not spent")
	}
	if b.take(late.Add(-time.Minute), 5, rl) {
		t.Error("a request earlier in the same hour was allowed after the budget ran out")
	}
	if !b.take(late.Add(time.Second), 5, rl) {
		t.Error("the new hour did not start with a budget")
	}
}

// Under half the limit left is a stop, not a slowdown, until GitHub says the
// limit has reset. The poller, the JIT configurations and the registration reap
// have nothing else to draw on.
func TestKennelClubStopsWhenLessThanHalfTheLimitIsLeft(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 10, 0, 0, time.UTC)
	reset := now.Add(20 * time.Minute)
	for _, tc := range []struct {
		name string
		rl   github.RateLimit
		now  time.Time
		want bool
	}{
		{"exactly half left", github.RateLimit{Limit: 5000, Remaining: 2500, ResetAt: reset}, now, true},
		{"one under half", github.RateLimit{Limit: 5000, Remaining: 2499, ResetAt: reset}, now, false},
		{"almost none", github.RateLimit{Limit: 5000, Remaining: 3, ResetAt: reset}, now, false},
		{"low, but the reset has passed", github.RateLimit{Limit: 5000, Remaining: 3, ResetAt: reset}, reset, true},
		{"low and no reset time given", github.RateLimit{Limit: 5000, Remaining: 3}, now, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b kennelBudget
			if got := b.take(tc.now, 20, tc.rl); got != tc.want {
				t.Errorf("take = %v, want %v", got, tc.want)
			}
		})
	}
}

// A request that was refused is not a request that was made: a held budget does
// not spend itself down by being asked.
func TestARefusedRequestDoesNotUseUpTheBudget(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 10, 0, 0, time.UTC)
	low := github.RateLimit{Limit: 5000, Remaining: 10, ResetAt: now.Add(time.Minute)}
	var b kennelBudget
	for range 50 {
		b.take(now, 20, low)
	}
	if b.spent != 0 {
		t.Errorf("%d requests were counted while Kennel Club was standing down", b.spent)
	}
}

// A GitHub that is answering 500 is not helped by being asked again every
// minute, and the registration path shares it. Kennel Club therefore stands down
// from the installation, for longer each time, and nobody else is held.
func TestKennelClubStandsDownWhenGitHubFailsAndOnlyKennelClub(t *testing.T) {
	f := newKennelFixture(t)
	in := kennelPassInput{cfg: config.Kennel{APIBudgetPercent: 20}}
	if !f.c.kennelTake(f.inst.ID, in, github.RateLimit{}) {
		t.Fatal("a healthy installation was refused")
	}
	f.c.observeKennel(f.inst.ID, github.NewServerError(errors.New("500 []")))
	if f.c.kennelTake(f.inst.ID, in, github.RateLimit{}) {
		t.Error("Kennel Club carried on after a 500")
	}
	if f.c.githubHeld(f.inst.ID, f.c.Now()) {
		t.Error("a 500 on a Kennel Club read paused the poller and scheduler too")
	}
	f.advance(kennelUnwellBase + time.Second)
	if !f.c.kennelTake(f.inst.ID, in, github.RateLimit{}) {
		t.Fatal("Kennel Club never came back")
	}
	f.c.observeKennel(f.inst.ID, github.NewServerError(errors.New("500 []")))
	f.advance(kennelUnwellBase + time.Second)
	if f.c.kennelTake(f.inst.ID, in, github.RateLimit{}) {
		t.Error("the second failure in a row waited no longer than the first")
	}
}
